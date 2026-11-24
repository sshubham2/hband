package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// trustPeer rebuilds the app so that the peer every harness request comes from
// (httptest's 192.0.2.1) is a listed proxy: X-Forwarded-For is then believed, and
// each test visitor is the address it names. Call it before changing a seam.
func (h *harness) trustPeer() {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("192.0.2.1/32")}
	h.app = newApp(h.cfg, h.store, h.lib, h.clock.Now)
	h.handler = h.app.handler()
}

// visit is a browser at address ip asking for a path, through the trusted proxy.
func (h *harness) visit(path, ip string, opts ...func(*http.Request)) result {
	h.t.Helper()
	all := append([]func(*http.Request){withHeader("User-Agent", chromeUA), withHeader("X-Forwarded-For", ip)}, opts...)
	return h.serve("GET", path, all...)
}

// opensOf is how many opens are stored for the report with that slug, and all is
// how many are stored for every report.
func (h *harness) opensOf(slug string) int {
	h.t.Helper()
	id, _, _, ok := h.reportRow(slug)
	if !ok {
		h.t.Fatalf("no report %s", slug)
	}
	var n int
	if err := h.store.DB().QueryRow(`SELECT count(*) FROM report_opens WHERE report_id = ?`, id).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) allOpens() int { h.t.Helper(); return h.count("report_opens") }

// captureLog sends the default logger to a buffer for the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{w: &buf}, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// openWorld is two published reports (x older, y newest, so GET / draws y), a draft
// d, and a comment on y whose branch page can be asked for. The harness's peer is a
// trusted proxy.
func openWorld(t *testing.T) (h *harness, owner string, comment int64) {
	t.Helper()
	h = newHarness(t, false)
	h.trustPeer()
	owner = h.mint(testOwnerID, "the-owner")
	h.mustUpload(owner, "x", "Report X", "published", reportHTML(300, "x"))
	h.clock.Advance(time.Hour)
	h.mustUpload(owner, "y", "Report Y", "published", reportHTML(300, "y"))
	h.mustUpload(owner, "d", "Draft D", "draft", reportHTML(300, "d"))
	comment = h.say(h.mint(7, "reader"), "y", "a comment whose branch page is asked for", 0)
	if n := h.allOpens(); n != 0 {
		t.Fatalf("%d opens before anyone visited", n)
	}
	return h, owner, comment
}

// TestOpenCounting: only a GET that renderReader answers 200 for a found report
// counts, / and /reports/<newest> share one visitor key, and every other shape
// writes nothing. A write that fails leaves the page as it was, and one that
// waits for the database is cut off at the 2 s limit without harming the pool.
func TestOpenCounting(t *testing.T) {
	t.Run("a visit counts, and / and the newest report's own page are one open", func(t *testing.T) {
		h, _, _ := openWorld(t)
		if res := h.visit("/reports/y", "198.51.100.1"); res.Status != 200 {
			t.Fatalf("GET /reports/y: %d", res.Status)
		}
		if h.opensOf("y") != 1 || h.opensOf("x") != 0 {
			t.Fatalf("after one visit: y %d, x %d; want 1, 0", h.opensOf("y"), h.opensOf("x"))
		}
		// The same visitor reads / (which draws y in full) and y again: still one.
		for _, p := range []string{"/", "/reports/y", "/"} {
			if res := h.visit(p, "198.51.100.1"); res.Status != 200 || !strings.Contains(res.Body, "Report Y") {
				t.Fatalf("GET %s: %d", p, res.Status)
			}
		}
		if h.opensOf("y") != 1 {
			t.Fatalf("/ and /reports/y by one visitor: %d opens, want 1", h.opensOf("y"))
		}
		// Another visitor, through /: a second open of y, and none of x.
		h.visit("/", "198.51.100.2")
		if h.opensOf("y") != 2 || h.opensOf("x") != 0 {
			t.Fatalf("a second visitor via /: y %d, x %d; want 2, 0", h.opensOf("y"), h.opensOf("x"))
		}
		// The same visitor on another report is a row of its own.
		h.visit("/reports/x", "198.51.100.1")
		if h.opensOf("x") != 1 || h.opensOf("y") != 2 {
			t.Fatalf("one visitor on x: x %d, y %d; want 1, 2", h.opensOf("x"), h.opensOf("y"))
		}
		// The next UTC day counts the first visitor again.
		h.clock.Advance(24 * time.Hour)
		h.visit("/reports/y", "198.51.100.1")
		if h.opensOf("y") != 3 {
			t.Fatalf("the next day: %d opens of y, want 3", h.opensOf("y"))
		}
	})

	t.Run("the page is the same bytes whoever's open it is", func(t *testing.T) {
		h, _, _ := openWorld(t)
		bot := h.serve("GET", "/reports/y", withHeader("User-Agent", "curl/8.4.0"), withHeader("X-Forwarded-For", "198.51.100.1"))
		human := h.visit("/reports/y", "198.51.100.1")
		if d := sameResponse(bot, human); d != "" {
			t.Fatalf("counting changed the page: %s", d)
		}
		if h.opensOf("y") != 1 {
			t.Fatalf("%d opens", h.opensOf("y"))
		}
	})

	t.Run("shapes that write nothing", func(t *testing.T) {
		h, owner, comment := openWorld(t)
		reader := h.mint(9, "nine")
		cases := []struct {
			name   string
			do     func() result
			status int
		}{
			{"HEAD of the report", func() result {
				return h.serve("HEAD", "/reports/y", withHeader("User-Agent", chromeUA), withHeader("X-Forwarded-For", "198.51.100.10"))
			}, 200},
			{"HEAD of /", func() result {
				return h.serve("HEAD", "/", withHeader("User-Agent", chromeUA), withHeader("X-Forwarded-For", "198.51.100.10"))
			}, 200},
			{"the iframe's /raw/<slug>", func() result { return h.visit("/raw/y", "198.51.100.11") }, 200},
			{"the branch page", func() result { return h.visit("/reports/y/comments/"+strconv.FormatInt(comment, 10), "198.51.100.12") }, 200},
			{"the show-all page", func() result { return h.visit("/reports", "198.51.100.13") }, 200},
			{"an admin preview", func() result { return h.visit("/admin/reports/y", "198.51.100.14", h.withSID(owner)) }, 200},
			{"a draft", func() result { return h.visit("/reports/d", "198.51.100.15") }, 404},
			{"an unknown report", func() result { return h.visit("/reports/nope", "198.51.100.16") }, 404},
			{"a POST to the report", func() result {
				return h.serve("POST", "/reports/y", withHeader("User-Agent", chromeUA), withHeader("X-Forwarded-For", "198.51.100.17"))
			}, 405},
			{"the signed-in owner on the report", func() result { return h.visit("/reports/y", "198.51.100.18", h.withSID(owner)) }, 200},
			{"the signed-in owner on /", func() result { return h.visit("/", "198.51.100.19", h.withSID(owner)) }, 200},
			{"an empty User-Agent", func() result {
				return h.serve("GET", "/reports/y", withHeader("X-Forwarded-For", "198.51.100.20"))
			}, 200},
			{"Go's default User-Agent", func() result {
				return h.serve("GET", "/reports/y", withHeader("User-Agent", "Go-http-client/1.1"), withHeader("X-Forwarded-For", "198.51.100.21"))
			}, 200},
			{"Googlebot, which carries Mozilla/", func() result {
				return h.serve("GET", "/reports/y", withHeader("User-Agent", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"), withHeader("X-Forwarded-For", "198.51.100.22"))
			}, 200},
			{"a trusted peer that forwards no X-Forwarded-For", func() result {
				return h.serve("GET", "/reports/y", withHeader("User-Agent", chromeUA))
			}, 200},
			{"a trusted peer whose client entry is junk", func() result { return h.visit("/reports/y", "unknown") }, 200},
			{"a client behind an unlisted proxy (a private address)", func() result { return h.visit("/reports/y", "10.0.0.7") }, 200},
		}
		for _, c := range cases {
			before := h.allOpens()
			if res := c.do(); res.Status != c.status {
				t.Errorf("%s: answered %d, want %d", c.name, res.Status, c.status)
			}
			if after := h.allOpens(); after != before {
				t.Errorf("%s: wrote %d opens, want none", c.name, after-before)
			}
		}
		// Sensitivity: the same requests from a real reader do count, so the zeros
		// above are the rules and not a broken counter.
		if res := h.visit("/reports/y", "198.51.100.30", h.withSID(reader)); res.Status != 200 || h.opensOf("y") != 1 {
			t.Fatalf("a signed-in reader's visit: %d, %d opens of y; want 200, 1", res.Status, h.opensOf("y"))
		}
	})

	t.Run("nothing is counted when the page is a 503", func(t *testing.T) {
		h, _, _ := openWorld(t)
		h.exec(`ALTER TABLE votes RENAME TO votes_gone`)
		if res := h.visit("/reports/y", "198.51.100.40"); res.Status != 503 {
			t.Fatalf("answered %d, want 503", res.Status)
		}
		if res := h.visit("/", "198.51.100.40"); res.Status != 503 {
			t.Fatalf("GET / answered %d, want 503", res.Status)
		}
		if n := h.allOpens(); n != 0 {
			t.Fatalf("%d opens counted for pages that were not shown", n)
		}
	})

	t.Run("the opens of an unpublished report are not added to, and are kept", func(t *testing.T) {
		h, owner, _ := openWorld(t)
		h.visit("/reports/x", "198.51.100.50")
		h.mustAct(owner, "x", "unpublish")
		if res := h.visit("/reports/x", "198.51.100.51"); res.Status != 404 {
			t.Fatalf("an unpublished report answered %d", res.Status)
		}
		if h.opensOf("x") != 1 {
			t.Fatalf("%d opens of x while unpublished, want the 1 from before", h.opensOf("x"))
		}
		h.mustAct(owner, "x", "publish")
		if h.opensOf("x") != 1 {
			t.Fatalf("republishing changed the opens: %d", h.opensOf("x"))
		}
		h.visit("/reports/x", "198.51.100.51")
		if h.opensOf("x") != 2 {
			t.Fatalf("%d opens after republishing and a new visitor, want 2", h.opensOf("x"))
		}
	})

	t.Run("a write that fails leaves the page as it was, and logs no address", func(t *testing.T) {
		h, _, _ := openWorld(t)
		logs := captureLog(t)
		bot := h.serve("GET", "/reports/y", withHeader("User-Agent", "curl/8.4.0"), withHeader("X-Forwarded-For", "198.51.100.60"))
		var calls int
		var gotID int64
		var gotAddr netip.Addr
		h.app.recordOpen = func(_ context.Context, id int64, a netip.Addr) error {
			calls++
			gotID, gotAddr = id, a
			return errors.New("opens: record: injected write failure")
		}
		res := h.visit("/reports/y", "198.51.100.60")
		if res.Status != 200 {
			t.Fatalf("a failed open write turned the page into a %d", res.Status)
		}
		if d := sameResponse(bot, res); d != "" {
			t.Fatalf("a failed open write changed the page: %s", d)
		}
		id, _, _, _ := h.reportRow("y")
		if calls != 1 || gotID != id || gotAddr != netip.MustParseAddr("198.51.100.60") {
			t.Fatalf("recordOpen called %d times with (%d, %v); want once with (%d, 198.51.100.60)", calls, gotID, gotAddr, id)
		}
		out := logs.String()
		if !strings.Contains(out, "injected write failure") || strings.Contains(out, "198.51.100") {
			t.Fatalf("the failure must be logged without the address:\n%s", out)
		}
		if n := h.allOpens(); n != 0 {
			t.Fatalf("%d opens stored by a write that failed", n)
		}
	})

	t.Run("a write that waits for the database is cut off at 2 s, and the pool is whole afterwards", func(t *testing.T) {
		h, _, _ := openWorld(t)
		logs := captureLog(t)
		conn, err := h.store.DB().Conn(bg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(bg, `BEGIN IMMEDIATE`); err != nil {
			t.Fatal(err)
		}
		held := true
		release := func() {
			if held {
				conn.ExecContext(bg, `ROLLBACK`)
				conn.Close()
				held = false
			}
		}
		defer release()

		start := time.Now()
		res := h.visit("/reports/y", "198.51.100.70")
		waited := time.Since(start)
		t.Logf("the page waited %s for a database that another connection holds for writing", waited.Round(10*time.Millisecond))
		if res.Status != 200 || !strings.Contains(res.Body, "Report Y") {
			t.Fatalf("answered %d while the database was locked for writing", res.Status)
		}
		if waited > 3*time.Second {
			t.Fatalf("the page waited %s: the open write is bounded at 2 s, not by busy_timeout", waited)
		}
		if waited < 1500*time.Millisecond {
			t.Fatalf("the page waited only %s: the write never waited on the lock this test holds, so it proves nothing", waited)
		}
		if !strings.Contains(logs.String(), "count an open") {
			t.Errorf("the timeout was not logged:\n%s", logs.String())
		}
		if n := h.allOpens(); n != 0 {
			t.Fatalf("%d opens stored while the database was locked", n)
		}

		// Release the lock. The connection that was interrupted went back to the pool:
		// cycle the pool well past its idle size and require every page to be shown and
		// every new visitor to count exactly once.
		release()
		for i := 1; i <= 12; i++ {
			ip := "198.51.100." + strconv.Itoa(100+i)
			if res := h.visit("/reports/y", ip); res.Status != 200 || !strings.Contains(res.Body, "Report Y") {
				t.Fatalf("visit %d after the lock was released: %d", i, res.Status)
			}
			if got := h.opensOf("y"); got != i {
				t.Fatalf("after visit %d the count is %d, want %d (an interrupted connection must not lose or double an open)", i, got, i)
			}
			if res := h.serve("GET", "/reports"); res.Status != 200 {
				t.Fatalf("GET /reports after visit %d: %d", i, res.Status)
			}
		}
	})
}

// A reader of the page is never told the open limit: nothing in a page names it.
func TestOpenWriteLimitIsTwoSeconds(t *testing.T) {
	if openWriteLimit != 2*time.Second {
		t.Fatalf("openWriteLimit = %s, want 2s (the contract the page is promised)", openWriteLimit)
	}
	// The package that writes must be the one the app was built with.
	h := newHarness(t, false)
	if h.app.opens == nil || h.app.recordOpen == nil || h.app.client == nil {
		t.Fatal("newApp left the open-counting seams unset")
	}
}
