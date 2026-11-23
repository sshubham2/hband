package server

import (
	"fmt"
	"html"
	"image"
	"image/color"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/sshubham2/hband/internal/testutil/headless"
)

// This file proves, in a real browser, that a report's own script never runs:
// not framed on the reader page, and not opened top-level on its raw path.
//
// It is run like a laboratory assay. The report is a REAGENT: painted green by
// inline CSS (which the report policy allows, so green means "rendered, and no
// script reached the page") and painted red by any script or event handler
// (so red means "a script ran"). The readout is the browser's own screenshot,
// counted in whole-image pixels, so it needs no coordinates. A negative result
// counts only when the same run also shows that the instrument works:
//
//	instrument blanks  the real reader chrome, in the empty state and around a
//	                   neutral report, shows neither colour;
//	W4  positive       the reagent under sandbox=allow-scripts and no policy
//	                   turns red and sends its script beacon;
//	W4b positive       the same with the inline script removed, so only the
//	                   onerror handler is left, turns red too;
//	W5  blank          the real raw path framed from another origin is refused
//	                   by frame-ancestors and shows neither colour.
//
// A failed control makes the run INVALID and fails the test. It never passes on
// silence: a blind or too-early capture cannot read as "no script ran".
//
// The verdict wells are: W1 the reader page over http, W1s the same over
// https, W2 the raw path opened top-level (the policy's sandbox alone), and W3
// the reagent with no policy under the sandbox attribute parsed from the real
// reader page (the attribute alone).
//
// What the request logs prove, and what they do not. A script that ran under
// the report policy could not send its beacon anyway (default-src 'none',
// img-src data:), so the zero-beacon check on the hband servers measures the
// policy's blocking of requests, not whether a script ran: the red-pixel count
// is the only script verdict. Its control is W3: the reagent's plain image tag
// does fetch its beacon when nothing blocks it, tagged ?w=W3 in Q's log.
//
// The reagent's green rule comes last in the body, after the script and the
// onerror image, and carries no !important; both repaints go through
// element.style, which outranks a stylesheet. For the inline script, green in a
// well therefore means the parser got past the script before the capture. The
// onerror image is not covered that way (the handler fires later), which is
// what W4b is for.

const (
	signalMinPixels = 40000
	signalTolerance = 8
)

var (
	signalGreen = color.RGBA{0x00, 0xff, 0x00, 0xff}
	signalRed   = color.RGBA{0xff, 0x00, 0x00, 0xff}
)

// reagent is the report under test. tag labels its beacons (?w=<tag>) so a
// request in a log can be traced to the well that sent it.
func reagent(tag string, withScript bool) []byte {
	const repaint = "document.documentElement.style.background='#ff0000';document.body.style.background='#ff0000'"
	var b strings.Builder
	fmt.Fprintf(&b, "<!doctype html>\n<html><head><meta charset=\"utf-8\"><title>reagent %s</title></head>\n<body>\n", tag)
	b.WriteString("<p id=\"s\">SENTINEL-STATIC</p>\n")
	fmt.Fprintf(&b, "<img src=\"/beacon/img?w=%s\" alt=\"\">\n", tag)
	if withScript {
		fmt.Fprintf(&b, "<script>%s;new Image().src='/beacon/script?w=%s';</script>\n", repaint, tag)
	}
	fmt.Fprintf(&b, "<img src=\"x\" alt=\"\" onerror=\"%s\">\n", repaint)
	b.WriteString("<style>html,body{background:#00ff00}</style>\n</body></html>\n")
	return []byte(b.String())
}

// neutralReport is a report with no script and no signal colour.
func neutralReport() []byte {
	return []byte("<!doctype html>\n<html><head><meta charset=\"utf-8\"><title>blank</title>" +
		"<style>html,body{background:#808080}</style></head>\n<body><p>A neutral report.</p></body></html>\n")
}

// reqLog records every request a server receives, as "METHOD /path?query". One
// type logs the hband servers and the control server alike, so a beacon seen
// in one proves the others' logs would show a beacon too.
type reqLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *reqLog) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		l.lines = append(l.lines, r.Method+" "+r.URL.RequestURI())
		l.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (l *reqLog) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

func (l *reqLog) since(n int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines[n:]...)
}

// logRequests puts a request log around the harness's whole handler, outside
// New's chain, so it also sees what the mux answers with a 404. The harness's
// listener reads the handler under h.mu, so the swap is safe while it serves.
func (h *harness) logRequests() *reqLog {
	l := &reqLog{}
	h.mu.Lock()
	h.handler = l.wrap(h.handler)
	h.mu.Unlock()
	return l
}

func contains(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func withPrefix(lines []string, prefix string) []string {
	var out []string
	for _, l := range lines {
		if strings.Contains(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

func TestReportScriptsNeverRun(t *testing.T) {
	browser := headless.Browser(t)

	p := newHarness(t, false) // the reader site over http
	s := newHarness(t, true)  // the same over https, the production scheme
	pLog, sLog := p.logRequests(), s.logRequests()
	pOwner, sOwner := p.mint(testOwnerID, "the-owner"), s.mint(testOwnerID, "the-owner")

	// Q is another origin on another port. It serves the control pages.
	qLog := &reqLog{}
	qMux := http.NewServeMux()
	qMux.HandleFunc("GET /copy/{well}", func(w http.ResponseWriter, r *http.Request) {
		well := r.PathValue("well")
		if well != "W3" && well != "W4" && well != "W4b" { // /copy/x is the failing image
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8") // and no policy at all
		w.Write(reagent(well, well != "W4b"))
	})
	qMux.HandleFunc("GET /beacon/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	qMux.HandleFunc("GET /page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html>\n<html><body><iframe sandbox=\"%s\" src=\"%s\" width=\"100%%\" height=\"800\"></iframe></body></html>\n",
			html.EscapeString(r.URL.Query().Get("sandbox")), html.EscapeString(r.URL.Query().Get("src")))
	})
	q := httptest.NewServer(qLog.wrap(qMux))
	t.Cleanup(q.Close)

	// well takes one screenshot and returns it with the requests that Shot alone
	// added to each log, so every well is judged on its own traffic.
	type traffic struct{ p, s, q []string }
	well := func(name, target string, flags ...string) (image.Image, traffic) {
		t.Helper()
		pn, sn, qn := pLog.len(), sLog.len(), qLog.len()
		img := headless.Shot(t, browser, name, target, flags...)
		tr := traffic{p: pLog.since(pn), s: sLog.since(sn), q: qLog.since(qn)}
		g, r := headless.Count(img, signalGreen, signalTolerance), headless.Count(img, signalRed, signalTolerance)
		t.Logf("%-14s green=%7d red=%7d  (p %d, s %d, q %d requests)", name, g, r, len(tr.p), len(tr.s), len(tr.q))
		return img, tr
	}
	green := func(img image.Image) int { return headless.Count(img, signalGreen, signalTolerance) }
	red := func(img image.Image) int { return headless.Count(img, signalRed, signalTolerance) }
	frame := func(sandbox, src string) string {
		return q.URL + "/page?sandbox=" + url.QueryEscape(sandbox) + "&src=" + url.QueryEscape(src)
	}

	// Instrument blank 1: the real chrome with nothing published.
	img, _ := well("blank-empty", p.srv.URL+"/")
	if green(img) != 0 || red(img) != 0 {
		t.Fatalf("INVALID RUN: the site chrome contains a signal colour (empty state: green=%d red=%d)", green(img), red(img))
	}

	// Reports go in through the real upload route.
	p.mustUpload(pOwner, "blank", "Blank", "published", neutralReport())
	p.mustUpload(pOwner, "probe", "Probe", "published", reagent("P", true))
	s.mustUpload(sOwner, "probe", "Probe", "published", reagent("S", true))

	// Instrument blank 2: the real chrome around a found report, anonymous like
	// the wells below. Whatever the like count and comments add to this layout
	// later is measured here, not mistaken for a script.
	img, _ = well("blank-found", p.srv.URL+"/reports/blank")
	if green(img) != 0 || red(img) != 0 {
		t.Fatalf("INVALID RUN: the site chrome contains a signal colour (report page: green=%d red=%d)", green(img), red(img))
	}

	// The sandbox attribute exactly as the real reader page ships it.
	page := p.serve("GET", "/reports/probe")
	frames := iframes(page.Body)
	if page.Status != 200 || len(frames) != 1 || frames[0]["sandbox"] == "" {
		t.Fatalf("the reader page has no sandboxed frame to take the attribute from: %d %v", page.Status, frames)
	}
	realSandbox := frames[0]["sandbox"]

	// W4, the positive control: scripts allowed, no policy.
	img, tr := well("W4", frame("allow-scripts", q.URL+"/copy/W4"))
	if red(img) < signalMinPixels || !contains(tr.q, "GET /beacon/script?w=W4") {
		t.Fatalf("INVALID RUN: the positive control did not fire (red=%d, script beacon %v). The capture is blind to framed pages or came too early, so no negative below would mean anything",
			red(img), contains(tr.q, "GET /beacon/script?w=W4"))
	}

	// W4b, the positive control for the onerror handler alone.
	img, _ = well("W4b", frame("allow-scripts", q.URL+"/copy/W4b"))
	if red(img) < signalMinPixels {
		t.Fatalf("INVALID RUN: the onerror handler did not fire within the capture (red=%d)", red(img))
	}

	// W5, the blank: the real raw path framed from another origin.
	img, tr = well("W5", frame(realSandbox, p.srv.URL+"/raw/probe"))
	if green(img) != 0 || red(img) != 0 {
		t.Fatalf("INVALID RUN: the blank shows a signal colour (green=%d red=%d): frame-ancestors did not refuse a foreign origin", green(img), red(img))
	}
	if !contains(tr.p, "GET /raw/probe") {
		t.Fatalf("INVALID RUN: the blank never asked the hband server for the report: %v", tr.p)
	}

	// W3, the sandbox attribute alone: no policy, the attribute from the real page.
	img, tr = well("W3", frame(realSandbox, q.URL+"/copy/W3"))
	if red(img) != 0 || contains(tr.q, "GET /beacon/script?w=W3") {
		t.Fatalf("the sandbox attribute alone let a script run (red=%d, script beacon %v)", red(img), contains(tr.q, "GET /beacon/script?w=W3"))
	}
	if green(img) < signalMinPixels {
		t.Fatalf("INVALID RUN: the attribute-alone well did not render (green=%d)", green(img))
	}
	if !contains(tr.q, "GET /beacon/img?w=W3") {
		t.Fatalf("INVALID RUN: the reagent's image beacon was not fetched when nothing blocks it, so a silent hband log would mean nothing: %v", tr.q)
	}

	// The verdict wells.
	for _, w := range []struct {
		name, target string
		reqs         func(traffic) []string
	}{
		{"W1", p.srv.URL + "/reports/probe", func(tr traffic) []string { return tr.p }},
		{"W1s", s.srv.URL + "/reports/probe", func(tr traffic) []string { return tr.s }},
		{"W2", p.srv.URL + "/raw/probe", func(tr traffic) []string { return tr.p }},
	} {
		flags := []string(nil)
		if strings.HasPrefix(w.target, "https:") {
			flags = []string{"--ignore-certificate-errors"}
		}
		img, tr := well(w.name, w.target, flags...)
		reqs := w.reqs(tr)
		if !contains(reqs, "GET /raw/probe") {
			t.Errorf("%s: INVALID RUN: the hband server never saw GET /raw/probe, so the report did not load: %v", w.name, reqs)
		}
		if g := green(img); g < signalMinPixels {
			t.Errorf("%s: the report did not render (green=%d, want at least %d)", w.name, g, signalMinPixels)
		}
		if r := red(img); r != 0 {
			t.Errorf("%s: a report script ran (red=%d)", w.name, r)
		}
		if b := withPrefix(reqs, "/beacon/"); len(b) != 0 {
			t.Errorf("%s: the report made same-origin requests: %v", w.name, b)
		}
	}
}

// TestReagentShape pins the properties the assay's reading depends on, so a
// later edit to the reagent cannot quietly turn the check into a vacuous one.
func TestReagentShape(t *testing.T) {
	r := string(reagent("T", true))
	if strings.Contains(strings.ToLower(r), "!important") {
		t.Error("the green rule must not carry !important: it would defeat the red repaint in every well")
	}
	script, onerror, green := strings.Index(r, "<script>"), strings.Index(r, "onerror="), strings.Index(r, "<style>")
	if script < 0 || onerror < 0 || green < 0 || green < script || green < onerror {
		t.Errorf("the green style must come after the script and the onerror image (script %d, onerror %d, style %d)", script, onerror, green)
	}
	if strings.Count(r, "style.background='#ff0000'") != 4 {
		t.Errorf("both repaints must set element.style, which outranks a stylesheet:\n%s", r)
	}
	if strings.Contains(string(reagent("T", false)), "<script>") {
		t.Error("the onerror-only reagent still carries a script")
	}
	// The beacons are tagged, so a request in a log traces to its well.
	for _, want := range []string{"/beacon/img?w=T", "/beacon/script?w=T"} {
		if !strings.Contains(r, want) {
			t.Errorf("the reagent lacks the beacon %q", want)
		}
	}

}
