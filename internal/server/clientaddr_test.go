package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"testing"
)

// clientAddrRow is one row of testdata/client_addr_corpus.json.
type clientAddrRow struct {
	ID      string            `json:"id"`
	Class   string            `json:"class"`
	Trusted *[]string         `json:"trusted"`
	Peer    string            `json:"peer"`
	XFF     []string          `json:"xff"`
	Other   map[string]string `json:"other"`
	Want    string            `json:"want"`
	Warn    string            `json:"warn"`
	Why     string            `json:"why"`
}

func loadClientAddrCorpus(t *testing.T) []clientAddrRow {
	t.Helper()
	data, err := os.ReadFile("testdata/client_addr_corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Rows []clientAddrRow `json:"rows"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("client_addr_corpus.json: %v", err)
	}
	return c.Rows
}

func prefixes(t *testing.T, list []string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, s := range list {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p)
		} else if a, err := netip.ParseAddr(s); err == nil {
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		} else {
			t.Fatalf("bad trusted entry %q", s)
		}
	}
	return out
}

func (r clientAddrRow) request(t *testing.T) *http.Request {
	t.Helper()
	req := httptest.NewRequest("GET", "/reports/x", nil)
	req.RemoteAddr = r.Peer
	for _, line := range r.XFF {
		req.Header.Add("X-Forwarded-For", line)
	}
	for k, v := range r.Other {
		req.Header.Set(k, v)
	}
	return req
}

// warnings says which of the three once-only warnings a log holds.
func warnings(log string) map[string]int {
	out := map[string]int{}
	for kind, marker := range map[string]string{
		"untrusted": "is not in HBAND_TRUSTED_PROXIES",
		"no-entry":  "sent no X-Forwarded-For client entry",
		"internal":  "resolved to an internal address",
	} {
		out[kind] = strings.Count(log, marker)
	}
	return out
}

var ipLiteral = regexp.MustCompile(`[0-9a-fA-F:.]*[.:][0-9a-fA-F:.]*`)

// TestClientAddr runs the corpus of client addresses: a trusted peer that forwards
// no client entry (no header, an empty one, one whose every entry is trusted, only
// X-Real-IP or Forwarded) is not counted and says so once; so is a client that
// resolves to an internal address (a chain whose outer proxy is not listed). The
// rightmost untrusted entry is the client, never the leftmost; a header from a
// peer nobody listed is ignored and said so once; an unparsable entry that is
// reached is not counted. No warning carries an address.
func TestClientAddr(t *testing.T) {
	rows := loadClientAddrCorpus(t)
	if len(rows) < 60 {
		t.Fatalf("the corpus holds %d rows; it has been trimmed", len(rows))
	}
	classes := map[string]int{}
	for _, row := range rows {
		classes[row.Class]++
		t.Run(row.ID, func(t *testing.T) {
			trusted := []string{"127.0.0.1"}
			if row.Trusted != nil {
				trusted = *row.Trusted
			}
			var logbuf bytes.Buffer
			c := newClientResolver(prefixes(t, trusted), slog.New(slog.NewTextHandler(&logbuf, nil)))
			got, ok := c.addr(row.request(t))
			if row.Want == "" {
				if ok {
					t.Errorf("%s: resolved %v, want the request not counted", row.Why, got)
				}
			} else if !ok || got != netip.MustParseAddr(row.Want) {
				t.Errorf("%s: resolved %v, %v; want %s", row.Why, got, ok, row.Want)
			}
			w := warnings(logbuf.String())
			for kind, n := range w {
				want := 0
				if kind == row.Warn {
					want = 1
				}
				if n != want {
					t.Errorf("%s: %d %q warnings, want %d (%s)", row.ID, n, kind, want, row.Why)
				}
			}
			// No warning carries an address: not the peer's, not an entry's.
			for _, lit := range ipLiteral.FindAllString(strings.ReplaceAll(logbuf.String(), "HBAND_TRUSTED_PROXIES", ""), -1) {
				if _, err := netip.ParseAddr(strings.Trim(lit, ".:")); err == nil {
					t.Errorf("a warning carries an address: %q in %s", lit, logbuf.String())
				}
			}
		})
	}
	for _, class := range []string{"no-client-entry", "internal-client", "resolved", "unparsable", "untrusted-peer", "peer"} {
		if classes[class] == 0 {
			t.Errorf("the corpus has no row of the class %q", class)
		}
	}
}

// Each warning is logged once per resolver, however many requests raise it, and a
// resolver of its own keeps one test's warnings from another's (the Onces are not
// package-level, so the result does not depend on test order or on -count).
func TestClientAddrWarnsOncePerResolver(t *testing.T) {
	byID := map[string]clientAddrRow{}
	for _, row := range loadClientAddrCorpus(t) {
		byID[row.ID] = row
	}
	for pass := 0; pass < 2; pass++ { // a second pass is a second test run in one process
		var logbuf bytes.Buffer
		c := newClientResolver(prefixes(t, []string{"127.0.0.1"}), slog.New(slog.NewTextHandler(&logbuf, nil)))
		for i := 0; i < 5; i++ {
			// np-001 raises "untrusted" (its peer is not in the default list used
			// here), m1-001 and m1-004 raise "no-entry", m1-020 and m1-021 "internal".
			for _, id := range []string{"np-001", "m1-001", "m1-004", "m1-020", "m1-021"} {
				c.addr(byID[id].request(t))
			}
		}
		for kind, n := range warnings(logbuf.String()) {
			if n != 1 {
				t.Errorf("pass %d: %d %q warnings after 25 requests, want exactly 1", pass, n, kind)
			}
		}
	}
	// Two apps do not share a resolver.
	h1, h2 := newHarness(t, false), newHarness(t, false)
	if h1.app.client == h2.app.client {
		t.Error("two apps share one client resolver, and so one set of once-only warnings")
	}
}
