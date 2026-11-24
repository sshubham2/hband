package opens

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/sshubham2/hband/internal/store"
)

var bg = context.Background()

// utc is a UTC time from a date and a clock reading.
func utc(y int, m time.Month, d, hh, mm, ss int) time.Time {
	return time.Date(y, m, d, hh, mm, ss, 0, time.UTC)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// world is a real database file with three reports: 1 and 2 published, 3 a draft.
type world struct {
	t    *testing.T
	dir  string
	st   *store.Store
	clk  *clock
	book *Book
	key  []byte
}

func newWorld(t *testing.T) *world {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "hband.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, r := range []struct {
		slug, status string
	}{{"one", "published"}, {"two", "published"}, {"draft", "draft"}} {
		if _, err := st.DB().Exec(`INSERT INTO reports (slug, title, status, published_at, size_bytes, sha256, created_at, updated_at)
			VALUES (?, ?, ?, 1, 10, 'x', 1, 1)`, r.slug, r.slug, r.status); err != nil {
			t.Fatal(err)
		}
	}
	key := bytes.Repeat([]byte{0x5a}, 32)
	clk := &clock{t: utc(2027, 1, 15, 8, 0, 0)}
	return &world{t: t, dir: dir, st: st, clk: clk, key: key, book: newBook(st.DB(), clk.Now, key)}
}

func (w *world) record(report int64, addr string) {
	w.t.Helper()
	if err := w.book.Record(bg, report, netip.MustParseAddr(addr)); err != nil {
		w.t.Fatalf("Record(%d, %s): %v", report, addr, err)
	}
}

func (w *world) rows(report int64) int {
	w.t.Helper()
	var n int
	if err := w.st.DB().QueryRow(`SELECT count(*) FROM report_opens WHERE report_id = ?`, report).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *world) total() int {
	w.t.Helper()
	var n int
	if err := w.st.DB().QueryRow(`SELECT count(*) FROM report_opens`).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// TestRecord pins what counts as one open: a visitor, a report and a UTC day.
func TestRecord(t *testing.T) {
	// A local zone that is not UTC and whose midnight is not UTC's: a day taken from
	// local time would split or join these opens wrongly.
	old := time.Local
	time.Local = time.FixedZone("IST", 5*3600+30*60)
	t.Cleanup(func() { time.Local = old })

	t.Run("the same address twice on one UTC day is one open", func(t *testing.T) {
		w := newWorld(t)
		w.record(1, "198.51.100.7")
		w.record(1, "198.51.100.7")
		w.clk.Set(utc(2027, 1, 15, 23, 59, 59))
		w.record(1, "198.51.100.7")
		if n := w.rows(1); n != 1 {
			t.Fatalf("%d opens, want 1", n)
		}
		w.record(1, "198.51.100.8")
		if n := w.rows(1); n != 2 {
			t.Fatalf("a second visitor: %d opens, want 2", n)
		}
	})

	t.Run("the day is UTC, not local", func(t *testing.T) {
		w := newWorld(t)
		// 18:29:59 UTC is 23:59:59 in IST: the local date changes at 18:30 UTC, and
		// the UTC date does not.
		w.clk.Set(utc(2027, 1, 15, 18, 29, 59))
		w.record(1, "198.51.100.7")
		w.clk.Set(utc(2027, 1, 15, 18, 30, 1))
		w.record(1, "198.51.100.7")
		if n := w.rows(1); n != 1 {
			t.Fatalf("across the local midnight, inside one UTC day: %d opens, want 1", n)
		}
		// 23:59:59 UTC to 00:00:01 UTC is a new day, though the local date is the same
		// at both (05:29:59 and 05:30:01 IST on the 16th).
		w.clk.Set(utc(2027, 1, 15, 23, 59, 59))
		w.record(1, "198.51.100.7")
		if n := w.rows(1); n != 1 {
			t.Fatalf("at 23:59:59 UTC: %d opens, want 1", n)
		}
		w.clk.Set(utc(2027, 1, 16, 0, 0, 1))
		w.record(1, "198.51.100.7")
		if n := w.rows(1); n != 2 {
			t.Fatalf("the next UTC day: %d opens, want 2", n)
		}
		var days []int64
		rows, err := w.st.DB().Query(`SELECT day FROM report_opens ORDER BY day`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var d int64
			rows.Scan(&d)
			days = append(days, d)
		}
		want := []int64{utc(2027, 1, 15, 0, 0, 0).Unix() / 86400, utc(2027, 1, 16, 0, 0, 0).Unix() / 86400}
		if len(days) != 2 || days[0] != want[0] || days[1] != want[1] {
			t.Fatalf("stored days %v, want %v (whole UTC days since 1970)", days, want)
		}
	})

	t.Run("IPv6 visitors are told apart by their /64", func(t *testing.T) {
		w := newWorld(t)
		w.record(1, "2001:db8:1:2::1")
		w.record(1, "2001:db8:1:2:ffff:ffff:ffff:ffff") // the same /64: the same host's network
		if n := w.rows(1); n != 1 {
			t.Fatalf("two addresses of one /64: %d opens, want 1", n)
		}
		w.record(1, "2001:db8:1:3::1") // another /64
		if n := w.rows(1); n != 2 {
			t.Fatalf("a second /64: %d opens, want 2", n)
		}
	})

	t.Run("an IPv4-mapped address is the IPv4 address, and a zone is dropped", func(t *testing.T) {
		w := newWorld(t)
		w.record(1, "198.51.100.7")
		w.record(1, "::ffff:198.51.100.7")
		w.record(1, "2001:db8::5")
		w.record(1, "2001:db8::5%eth0")
		if n := w.rows(1); n != 2 {
			t.Fatalf("%d opens, want 2", n)
		}
	})

	t.Run("one address on two reports is two rows with different stored values", func(t *testing.T) {
		w := newWorld(t)
		w.record(1, "198.51.100.7")
		w.record(2, "198.51.100.7")
		if w.rows(1) != 1 || w.rows(2) != 1 {
			t.Fatalf("opens of report 1 and 2: %d and %d, want 1 and 1", w.rows(1), w.rows(2))
		}
		var a, b []byte
		w.st.DB().QueryRow(`SELECT visitor FROM report_opens WHERE report_id = 1`).Scan(&a)
		w.st.DB().QueryRow(`SELECT visitor FROM report_opens WHERE report_id = 2`).Scan(&b)
		if len(a) != 16 || len(b) != 16 {
			t.Fatalf("stored values are %d and %d bytes, want 16", len(a), len(b))
		}
		if bytes.Equal(a, b) {
			t.Fatal("one visitor is linkable across reports: the same stored value on two reports")
		}
	})

	t.Run("the stored value is the keyed hash and nothing else", func(t *testing.T) {
		w := newWorld(t)
		w.record(1, "198.51.100.7")
		w.record(1, "2001:db8:1:2::9")
		day := utc(2027, 1, 15, 0, 0, 0).Unix() / 86400
		mac := func(family byte, raw []byte) []byte {
			m := hmac.New(sha256.New, w.key)
			var n [8]byte
			binary.BigEndian.PutUint64(n[:], 1)
			m.Write(n[:])
			binary.BigEndian.PutUint64(n[:], uint64(day))
			m.Write(n[:])
			m.Write([]byte{family})
			m.Write(raw)
			return m.Sum(nil)[:16]
		}
		want := map[string]bool{
			string(mac(4, []byte{198, 51, 100, 7})):                                true,
			string(mac(6, []byte{0x20, 0x01, 0x0d, 0xb8, 0x00, 0x01, 0x00, 0x02})): true,
		}
		rows, err := w.st.DB().Query(`SELECT visitor FROM report_opens`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var v []byte
			rows.Scan(&v)
			if !want[string(v)] {
				t.Errorf("stored value %x is not the HMAC-SHA256 (report, day, family, address) cut to 16 bytes", v)
			}
			delete(want, string(v))
		}
		if len(want) != 0 {
			t.Errorf("%d expected values were not stored", len(want))
		}
	})

	t.Run("a draft, an unpublished report and an unknown id write nothing", func(t *testing.T) {
		w := newWorld(t)
		w.record(3, "198.51.100.7") // the draft
		w.record(99, "198.51.100.7")
		if n := w.total(); n != 0 {
			t.Fatalf("%d rows for a draft and an unknown report", n)
		}
		w.record(1, "198.51.100.7")
		w.st.DB().Exec(`UPDATE reports SET status = 'draft' WHERE id = 1`)
		w.record(1, "198.51.100.8")
		if n := w.rows(1); n != 1 {
			t.Fatalf("an unpublished report took an open: %d rows, want the 1 from before", n)
		}
		// Unpublishing and republishing keep what was counted.
		w.st.DB().Exec(`UPDATE reports SET status = 'published' WHERE id = 1`)
		if n := w.rows(1); n != 1 {
			t.Fatalf("republishing changed the opens: %d", n)
		}
	})

	t.Run("deleting a report deletes its opens", func(t *testing.T) {
		w := newWorld(t)
		w.record(1, "198.51.100.7")
		w.record(2, "198.51.100.7")
		if _, err := w.st.DB().Exec(`DELETE FROM reports WHERE id = 1`); err != nil {
			t.Fatal(err)
		}
		if w.rows(1) != 0 || w.rows(2) != 1 {
			t.Fatalf("after deleting report 1: %d and %d rows, want 0 and 1", w.rows(1), w.rows(2))
		}
	})

	t.Run("an address that is not one is an error that carries none", func(t *testing.T) {
		w := newWorld(t)
		err := w.book.Record(bg, 1, netip.Addr{})
		if err == nil || !strings.HasPrefix(err.Error(), "opens: record:") {
			t.Fatalf("Record of the zero address: %v", err)
		}
		if n := w.total(); n != 0 {
			t.Fatalf("%d rows written for no address", n)
		}
	})

	t.Run("Counts is one number per report with opens, and an error is never a partial map", func(t *testing.T) {
		w := newWorld(t)
		for _, a := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
			w.record(1, a)
		}
		w.record(2, "198.51.100.1")
		got, err := w.book.Counts(bg)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[1] != 3 || got[2] != 1 || got[3] != 0 {
			t.Fatalf("Counts = %v, want report 1: 3, report 2: 1, nothing for the draft", got)
		}
		if _, err := w.st.DB().Exec(`ALTER TABLE report_opens RENAME TO report_opens_gone`); err != nil {
			t.Fatal(err)
		}
		if m, err := w.book.Counts(bg); err == nil || m != nil || !strings.HasPrefix(err.Error(), "opens: count:") {
			t.Fatalf("Counts on a missing table = %v, %v; want a nil map and an 'opens: count:' error", m, err)
		}
		if err := w.book.Record(bg, 1, netip.MustParseAddr("198.51.100.9")); err == nil || !strings.HasPrefix(err.Error(), "opens: record:") {
			t.Fatalf("Record on a missing table: %v", err)
		}
	})

	t.Run("New draws a key of its own for each book", func(t *testing.T) {
		w := newWorld(t)
		a, b := New(w.st.DB(), w.clk.Now), New(w.st.DB(), w.clk.Now)
		if len(a.key) != 32 || bytes.Equal(a.key, b.key) || bytes.Equal(a.key, make([]byte, 32)) {
			t.Fatalf("keys %x and %x are not 32 random bytes each", a.key, b.key)
		}
	})
}

const (
	chromeWin  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	firefox    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0"
	safariMac  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15"
	edge       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0"
	iphone     = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1"
	androidChr = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36"
	samsung    = "Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/25.0 Chrome/121.0.0.0 Mobile Safari/537.36"
	crios      = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/131.0.6778.73 Mobile/15E148 Safari/604.1"
	firefoxAnd = "Mozilla/5.0 (Android 14; Mobile; rv:133.0) Gecko/133.0 Firefox/133.0"
	cubot      = "Mozilla/5.0 (Linux; Android 8.0.0; CUBOT_P20) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/70.0.3538.110 Mobile Safari/537.36"
	cubotX18   = "Mozilla/5.0 (Linux; Android 9; CUBOT X18 Plus) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/80.0.3987.162 Mobile Safari/537.36"
)

// TestObviousBot feeds both branches of the rule: a User-Agent that does not start
// as a browser's does, and a token inside one that does. Real browsers, mobile
// ones included, must never be dropped.
func TestObviousBot(t *testing.T) {
	bots := map[string]string{
		"empty":                       "",
		"blank":                       "  \t ",
		"Go default":                  "Go-http-client/1.1",
		"python-requests":             "python-requests/2.31.0",
		"python urllib":               "Python-urllib/3.12",
		"curl":                        "curl/8.4.0",
		"wget":                        "Wget/1.21.4",
		"java":                        "Java/17.0.2",
		"okhttp":                      "okhttp/4.12.0",
		"scrapy":                      "Scrapy/2.11.0 (+https://scrapy.org)",
		"no Mozilla prefix":           "Opera/9.80 (Windows NT 6.1) Presto/2.12 Version/12.16",
		"Googlebot (carries Mozilla)": "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
		"bingbot (carries Mozilla)":   "Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)",
		"Googlebot smartphone":        "Mozilla/5.0 (Linux; Android 6.0.1; Nexus 5X Build/MMB29P) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
		"Yahoo Slurp":                 "Mozilla/5.0 (compatible; Yahoo! Slurp; http://help.yahoo.com/help/us/ysearch/slurp)",
		"spider":                      "Mozilla/5.0 (compatible; Baiduspider/2.0; +http://www.baidu.com/search/spider.html)",
		"crawler":                     "Mozilla/5.0 (compatible; SomeCrawler/1.0)",
		"HeadlessChrome":              "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/131.0.0.0 Safari/537.36",
		"Lighthouse":                  "Mozilla/5.0 (Linux; Android 11; moto g power (2022)) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/109.0.0.0 Mobile Safari/537.36 Chrome-Lighthouse",
		"link preview":                "Mozilla/5.0 (compatible; Discordbot/2.0; +https://discordapp.com) link preview",
		"facebookexternalhit":         "facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php) Mozilla/5.0",
		"upper case token":            "Mozilla/5.0 (compatible; AHREFSBOT/7.0)",
		"a bot that says Cubot":       "Mozilla/5.0 (compatible; cubot; SomeBot/1.0)",
	}
	for name, ua := range bots {
		if !ObviousBot(ua) {
			t.Errorf("%s: %q is a bot and was counted as a reader", name, ua)
		}
	}
	browsers := map[string]string{
		"Chrome on Windows": chromeWin, "Firefox": firefox, "Safari on a Mac": safariMac, "Edge": edge,
		"iPhone Safari": iphone, "Android Chrome": androidChr, "Samsung Internet": samsung,
		"Chrome on iOS": crios, "Firefox on Android": firefoxAnd,
		// m3: a Cubot phone's own Chrome names the make, and the make holds "bot".
		"Cubot P20": cubot, "Cubot X18": cubotX18,
		"surrounding whitespace": "  " + chromeWin + "  ",
	}
	for name, ua := range browsers {
		if ObviousBot(ua) {
			t.Errorf("%s: %q is a reader and was dropped as a bot", name, ua)
		}
	}
}

// TestNoPlainAddressStored plants addresses through Record, then scans every
// SQLite file the database leaves (the database, its write-ahead log and the
// shared-memory file), while it is open and after it is closed, for each address
// in text, in UTF-16 and as raw bytes.
func TestNoPlainAddressStored(t *testing.T) {
	w := newWorld(t)
	v4 := []string{"198.51.100.23", "203.0.113.77", "192.0.2.211"}
	v6 := []string{"2001:db8:aaaa:bbbb::1", "2001:db8:aaaa:cccc:1:2:3:4"}
	for _, a := range append(append([]string{}, v4...), v6...) {
		w.record(1, a)
		w.record(2, a)
	}
	if n := w.total(); n != 2*(len(v4)+len(v6)) {
		t.Fatalf("%d rows planted, want %d", n, 2*(len(v4)+len(v6)))
	}

	var needles [][]byte
	var named []string
	add := func(label string, b []byte) { needles = append(needles, b); named = append(named, label) }
	utf16le := func(s string) []byte {
		var out []byte
		for _, u := range utf16.Encode([]rune(s)) {
			out = append(out, byte(u), byte(u>>8))
		}
		return out
	}
	for _, a := range append(append([]string{}, v4...), v6...) {
		ip := netip.MustParseAddr(a)
		add(a+" as text", []byte(a))
		add(a+" in UTF-16", utf16le(a))
		raw := ip.AsSlice()
		if ip.Is6() {
			raw = raw[:8] // the /64 is what the key is taken from: even it must not appear
		}
		add(a+" as raw bytes", raw)
	}

	scan := func(when string) {
		t.Helper()
		entries, err := os.ReadDir(w.dir)
		if err != nil {
			t.Fatal(err)
		}
		seen := 0
		for _, e := range entries {
			if e.IsDir() || !strings.HasPrefix(e.Name(), "hband.db") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(w.dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			seen++
			for i, n := range needles {
				if bytes.Contains(data, n) {
					t.Errorf("%s: %s holds %s", when, e.Name(), named[i])
				}
			}
		}
		if seen == 0 {
			t.Fatalf("%s: no SQLite file to scan in %s", when, w.dir)
		}
	}
	// The write-ahead log holds the rows now, and is the file a plain address would
	// show in first.
	if fi, err := os.Stat(filepath.Join(w.dir, "hband.db-wal")); err != nil || fi.Size() == 0 {
		t.Fatalf("the write-ahead log is empty or missing (%v): the scan would test nothing", err)
	}
	scan("while open")
	if err := w.st.Close(); err != nil {
		t.Fatal(err)
	}
	scan("after close")

	// Sensitivity: the scan can see what it looks for. A value written the plain way
	// is found, in the same files.
	st, err := store.Open(filepath.Join(w.dir, "hband.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.DB().Exec(`CREATE TABLE plain_probe (a TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO plain_probe VALUES (?)`, v4[0]); err != nil {
		t.Fatal(err)
	}
	var found bool
	entries, _ := os.ReadDir(w.dir)
	for _, e := range entries {
		if data, err := os.ReadFile(filepath.Join(w.dir, e.Name())); err == nil && bytes.Contains(data, []byte(v4[0])) {
			found = true
		}
	}
	if !found {
		t.Fatal("the scan is blind: a plain address written on purpose was not found")
	}
}
