package server

import (
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// mustUpload saves a report as the owner and fails the test unless the
// library took it.
func (h *harness) mustUpload(sid, slug, title, status string, data []byte) {
	h.t.Helper()
	if res := h.upload(sid, slug, title, status, data); res.Status != 303 {
		h.t.Fatalf("upload %q: %d %s", slug, res.Status, res.Body)
	}
}

// mustAct runs an owner lifecycle action (unpublish, delete) on a slug.
func (h *harness) mustAct(sid, slug, action string) {
	h.t.Helper()
	if res := h.serve("POST", "/admin/reports/"+slug+"/"+action, h.withSID(sid)); res.Status != 303 {
		h.t.Fatalf("%s %q: %d %s", action, slug, res.Status, res.Body)
	}
}

// headerSig is every header but Date, in a form two responses can be compared
// by.
func headerSig(h http.Header) string {
	var lines []string
	for k, vs := range h {
		if strings.EqualFold(k, "Date") {
			continue
		}
		lines = append(lines, k+": "+strings.Join(vs, " | "))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// sameResponse reports how two responses differ, or "" when they are equal in
// status, body and every header but Date.
func sameResponse(a, b result) string {
	switch {
	case a.Status != b.Status:
		return "status " + strconv.Itoa(a.Status) + " vs " + strconv.Itoa(b.Status)
	case a.Body != b.Body:
		return "body " + strconv.Quote(a.Body) + " vs " + strconv.Quote(b.Body)
	case headerSig(a.Header) != headerSig(b.Header):
		return "headers\n" + headerSig(a.Header) + "\nvs\n" + headerSig(b.Header)
	}
	return ""
}

var (
	iframeRE = regexp.MustCompile(`(?is)<iframe\b([^>]*)>`)
	attrRE   = regexp.MustCompile(`([a-zA-Z-]+)="([^"]*)"`)
	titleRE  = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
	h1RE     = regexp.MustCompile(`(?is)<h1>(.*?)</h1>`)
)

// iframes returns the attributes of every <iframe> in a page, decoded.
func iframes(body string) []map[string]string {
	var out []map[string]string
	for _, m := range iframeRE.FindAllStringSubmatch(body, -1) {
		attrs := map[string]string{}
		for _, a := range attrRE.FindAllStringSubmatch(m[1], -1) {
			attrs[a[1]] = html.UnescapeString(a[2])
		}
		out = append(out, attrs)
	}
	return out
}

// signInNext is the decoded next value of the page's sign-in anchor.
func signInNext(t *testing.T, body string) string {
	t.Helper()
	m := anchorRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no sign-in anchor in %s", body)
	}
	u, err := url.Parse(html.UnescapeString(m[1]))
	if err != nil {
		t.Fatalf("sign-in href %q: %v", m[1], err)
	}
	return u.Query().Get("next")
}

func TestReaderHomeEmpty(t *testing.T) {
	h := newHarness(t, false)
	check := func(when string) {
		t.Helper()
		res := h.serve("GET", "/")
		if res.Status != 200 {
			t.Fatalf("%s: GET / = %d, want 200", when, res.Status)
		}
		if !strings.Contains(res.Body, "No reports yet") {
			t.Errorf("%s: no empty state:\n%s", when, res.Body)
		}
		if strings.Contains(res.Body, "<iframe") || strings.Contains(res.Body, "Reports cannot be shown") {
			t.Errorf("%s: an empty home page frames a report or says it is unavailable:\n%s", when, res.Body)
		}
		if got := res.Header.Get("Cache-Control"); got != "private, no-cache" {
			t.Errorf("%s: Cache-Control = %q", when, got)
		}
	}
	check("nothing at all")

	sid := h.mint(testOwnerID, "the-owner")
	h.mustUpload(sid, "only-a-draft", "Secret Draft Title", "draft", reportHTML(300, "draft"))
	check("only a draft")
	if res := h.serve("GET", "/"); strings.Contains(res.Body, "Secret Draft Title") || strings.Contains(res.Body, "only-a-draft") {
		t.Fatalf("a draft shows on the home page:\n%s", res.Body)
	}

	// A report that was published and then taken down leaves the page empty again.
	h.mustUpload(sid, "was-live", "Was Live", "published", reportHTML(300, "live"))
	if res := h.serve("GET", "/"); !strings.Contains(res.Body, "Was Live") {
		t.Fatalf("control: the published report is not on the home page:\n%s", res.Body)
	}
	h.mustAct(sid, "was-live", "unpublish")
	check("after an unpublish")
}

func TestReaderHomeShowsNewest(t *testing.T) {
	h := newHarness(t, false)
	sid := h.mint(testOwnerID, "the-owner")
	h.mustUpload(sid, "a", "Alpha Quarterly", "published", reportHTML(300, "a"))
	h.clock.Advance(time.Hour)
	h.mustUpload(sid, "b", "Bravo Quarterly", "published", reportHTML(300, "b"))
	h.clock.Advance(time.Hour)
	h.mustUpload(sid, "c", "Charlie Draft", "draft", reportHTML(300, "c"))

	res := h.serve("GET", "/")
	if res.Status != 200 {
		t.Fatalf("GET /: %d", res.Status)
	}
	frames := iframes(res.Body)
	if len(frames) != 1 || frames[0]["src"] != "/raw/b" {
		t.Fatalf("the home page frames %v, want exactly /raw/b:\n%s", frames, res.Body)
	}
	if !strings.Contains(res.Body, "<h1>Bravo Quarterly</h1>") {
		t.Errorf("the newest report's title is not shown:\n%s", res.Body)
	}
	for _, other := range []string{"Alpha Quarterly", "Charlie Draft", "/raw/a", "/raw/c", "/reports/a", "/reports/c"} {
		if strings.Contains(res.Body, other) {
			t.Errorf("the home page names another report (%q):\n%s", other, res.Body)
		}
	}

	// Publishing the draft makes it the newest.
	h.clock.Advance(time.Hour)
	h.mustAct(sid, "c", "publish")
	if res := h.serve("GET", "/"); !strings.Contains(res.Body, "<h1>Charlie Draft</h1>") || strings.Contains(res.Body, "Bravo Quarterly") {
		t.Fatalf("after publishing c:\n%s", res.Body)
	}
}

func TestReportPage(t *testing.T) {
	h := newHarness(t, false)
	sid := h.mint(testOwnerID, "the-owner")
	title := `Q3 <b>"review"</b> & more`
	h.mustUpload(sid, "q3-review", title, "published", reportHTML(300, "q3"))

	res := h.serve("GET", "/reports/q3-review")
	if res.Status != 200 {
		t.Fatalf("GET /reports/q3-review: %d", res.Status)
	}
	if got := res.Header.Get("Cache-Control"); got != "private, no-cache" {
		t.Errorf("Cache-Control = %q, want private, no-cache", got)
	}
	if strings.Contains(res.Body, `<b>"review"</b>`) || strings.Contains(res.Body, "<b>") {
		t.Errorf("the title reached the page unescaped:\n%s", res.Body)
	}
	if m := titleRE.FindStringSubmatch(res.Body); m == nil || html.UnescapeString(m[1]) != title+" - hband" {
		t.Errorf("<title> = %v, want the title + ' - hband'", m)
	}
	if m := h1RE.FindStringSubmatch(res.Body); m == nil || html.UnescapeString(m[1]) != title {
		t.Errorf("<h1> = %v, want the title", m)
	}

	frames := iframes(res.Body)
	if len(frames) != 1 {
		t.Fatalf("%d iframes, want exactly 1:\n%s", len(frames), res.Body)
	}
	f := frames[0]
	tokens := strings.Fields(f["sandbox"])
	sort.Strings(tokens)
	if strings.Join(tokens, " ") != "allow-popups allow-popups-to-escape-sandbox" {
		t.Errorf("sandbox = %q, want exactly allow-popups and allow-popups-to-escape-sandbox", f["sandbox"])
	}
	if f["referrerpolicy"] != "no-referrer" || f["src"] != "/raw/q3-review" || f["title"] != title {
		t.Errorf("iframe attributes = %v", f)
	}
	if _, inline := f["style"]; inline || f["width"] == "" || f["height"] == "" {
		t.Errorf("the frame must be sized by attributes and a class, not an inline style: %v", f)
	}
	if !strings.Contains(res.Body, `href="/reports/q3-review"`) {
		t.Errorf("no permalink on the page:\n%s", res.Body)
	}
}

func TestReaderPagesShareOneLayout(t *testing.T) {
	h := newHarness(t, false)
	owner := h.mint(testOwnerID, "the-owner")
	h.mustUpload(owner, "older", "The Older One", "published", reportHTML(300, "older"))
	h.clock.Advance(time.Hour)
	h.mustUpload(owner, "newest", "The Newest One", "published", reportHTML(300, "newest"))

	// A thread on the newest report, with a reply, an owner comment and a
	// placeholder, is part of what both routes draw: the two must stay the same
	// bytes for every viewer.
	somebody := h.mint(7, "somebody")
	top := h.say(somebody, "newest", "a comment from a reader", 0)
	h.say(owner, "newest", "the owner answers", top)
	gone := h.say(somebody, "newest", "a comment that will be removed", 0)
	h.say(somebody, "newest", "a reply that outlives it", gone)
	h.exec(`UPDATE comments SET removed_at = 5 WHERE id = ?`, gone)
	// A private message from the non-owner on the newest report, and the author's
	// answer to it. The sender is shown their own thread beneath the box and nobody
	// else is; it is read for the report shown and the viewer, so the two routes
	// still match for the sender.
	h.sendFeedback(somebody, "newest", "a private note to the author")
	h.mustReply(owner, 1, "newest", "a private answer from the author")

	for name, sid := range map[string]string{"owner": owner, "non-owner": somebody} {
		home := h.serve("GET", "/", h.withSID(sid))
		page := h.serve("GET", "/reports/newest", h.withSID(sid))
		if home.Status != 200 || page.Status != 200 {
			t.Fatalf("%s: / %d, /reports/newest %d", name, home.Status, page.Status)
		}
		if d := sameResponse(home, page); d != "" {
			t.Errorf("%s: / and /reports/newest differ: %s", name, d)
		}
		if !strings.Contains(home.Body, "Signed in as @") {
			t.Errorf("%s: the header does not show who is signed in", name)
		}
		for _, want := range []string{"a comment from a reader", "the owner answers", "a reply that outlives it", "comment removed", badgeMarkup} {
			if !strings.Contains(home.Body, want) {
				t.Errorf("%s: the home page does not show %q", name, want)
			}
		}
		// The message and the answer are the sender's alone, after the box's form.
		shown := name == "non-owner"
		for _, text := range []string{"a private note to the author", "a private answer from the author"} {
			if strings.Contains(home.Body, text) != shown {
				t.Errorf("%s: the page shows the private text %q = %v, want %v", name, text, !shown, shown)
			}
		}
		if shown {
			inOrder(t, name+": /", home.Body, `<form method="post" action="/reports/newest/feedback">`, `<ol class="own-feedback">`,
				"a private note to the author", "a private answer from the author")
		}
		if name == "non-owner" && !strings.Contains(home.Body, `<form method="post" action="/reports/newest/feedback">`) {
			t.Errorf("%s: the page has no feedback form for the newest report", name)
		}
	}
	// Another signed-in reader is shown neither, and the two routes still match.
	other := h.mint(8, "other")
	otherHome, otherPage := h.serve("GET", "/", h.withSID(other)), h.serve("GET", "/reports/newest", h.withSID(other))
	if d := sameResponse(otherHome, otherPage); d != "" {
		t.Errorf("another reader: / and /reports/newest differ: %s", d)
	}
	if strings.Contains(otherHome.Body, "a private note to the author") || strings.Contains(otherHome.Body, "a private answer from the author") || strings.Contains(otherHome.Body, "own-feedback") {
		t.Errorf("another reader is shown the sender's thread:\n%s", otherHome.Body)
	}

	home := h.serve("GET", "/")
	page := h.serve("GET", "/reports/newest")
	if home.Status != 200 || page.Status != 200 {
		t.Fatalf("anonymous: / %d, /reports/newest %d", home.Status, page.Status)
	}
	if !strings.Contains(home.Body, "a comment from a reader") || strings.Contains(home.Body, "<textarea") {
		t.Errorf("anonymous: the thread is not shown, or a form is offered:\n%s", home.Body)
	}
	if got, want := signInNext(t, home.Body), "/"; got != want {
		t.Errorf("home page sign-in next = %q, want %q", got, want)
	}
	if got, want := signInNext(t, page.Body), "/reports/newest"; got != want {
		t.Errorf("report page sign-in next = %q, want %q", got, want)
	}
	// Apart from where sign-in returns to, the two pages are the same bytes.
	const anchor = `<a href="SIGN-IN"`
	home.Body, page.Body = anchorRE.ReplaceAllString(home.Body, anchor), anchorRE.ReplaceAllString(page.Body, anchor)
	if d := sameResponse(home, page); d != "" {
		t.Errorf("anonymous: / and /reports/newest differ beyond the sign-in anchor: %s", d)
	}
}

func TestHiddenReportsAreIndistinguishable(t *testing.T) {
	h := newHarness(t, false)
	owner := h.mint(testOwnerID, "the-owner")
	h.mustUpload(owner, "live", "Live", "published", reportHTML(300, "live"))
	h.mustUpload(owner, "drafty", "Drafty", "draft", reportHTML(300, "drafty"))
	h.mustUpload(owner, "taken-down", "Taken down", "published", reportHTML(300, "down"))
	h.mustAct(owner, "taken-down", "unpublish")
	h.mustUpload(owner, "deleted", "Deleted", "published", reportHTML(300, "deleted"))
	h.mustAct(owner, "deleted", "delete")

	// Controls: the published report answers on both paths.
	for _, p := range []string{"/reports/live", "/raw/live"} {
		if res := h.serve("GET", p); res.Status != 200 {
			t.Fatalf("control: GET %s = %d", p, res.Status)
		}
	}

	hidden := []string{"drafty", "taken-down", "deleted", "unknown-slug", "UPPER-CASE", strings.Repeat("a", 81)}
	baseline := h.serve("GET", "/nowhere")
	if baseline.Status != 404 || baseline.Body != "404 page not found\n" {
		t.Fatalf("baseline 404: %d %q", baseline.Status, baseline.Body)
	}
	for viewer, opts := range map[string][]func(*http.Request){
		"anonymous": nil,
		"owner":     {h.withSID(owner)},
	} {
		for _, slug := range hidden {
			for _, prefix := range []string{"/reports/", "/raw/"} {
				res := h.serve("GET", prefix+slug, opts...)
				if d := sameResponse(res, baseline); d != "" {
					t.Errorf("%s: GET %s%s differs from an unregistered path: %s", viewer, prefix, slug, d)
				}
			}
		}
	}
	// 404 carries the site policy, not a report one: frame-ancestors 'none'.
	if csp := baseline.Header.Get("Content-Security-Policy"); csp != siteCSP {
		t.Errorf("404 CSP = %q, want the site policy", csp)
	}
	if baseline.Header.Get("Cache-Control") != "" {
		t.Errorf("a 404 carries Cache-Control %q", baseline.Header.Get("Cache-Control"))
	}
}

func TestPublicRawHeaders(t *testing.T) {
	for _, tls := range []bool{false, true} {
		name := "http"
		if tls {
			name = "https"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, tls)
			sid := h.mint(testOwnerID, "the-owner")
			data := reportHTML(500, "RAW-BYTES")
			h.mustUpload(sid, "raw-one", "Raw one", "published", data)
			wantCSP := (&app{cfg: h.cfg}).rawCSP()
			wantETag := `"` + sha256Hex(string(data)) + `"`

			res := h.serve("GET", "/raw/raw-one")
			if res.Status != 200 || res.Body != string(data) {
				t.Fatalf("GET /raw/raw-one: %d, %d bytes, want 200 and the uploaded bytes", res.Status, len(res.Body))
			}
			hd := res.Header
			if got := hd.Values("Content-Security-Policy"); len(got) != 1 || got[0] != wantCSP || got[0] == siteCSP {
				t.Errorf("Content-Security-Policy = %q, want exactly the report policy %q", got, wantCSP)
			}
			for k, want := range map[string]string{
				"Content-Type":                 "text/html; charset=utf-8",
				"X-Content-Type-Options":       "nosniff",
				"Referrer-Policy":              "no-referrer",
				"Cross-Origin-Resource-Policy": "same-origin",
				"ETag":                         wantETag,
				"Cache-Control":                "no-cache",
			} {
				if got := hd.Get(k); got != want {
					t.Errorf("%s = %q, want %q", k, got, want)
				}
			}
			if len(res.Cookies) != 0 {
				t.Errorf("the raw path set cookies: %v", res.Cookies)
			}

			// It reads no identity: a signed-in reader gets the very same response.
			if signed := h.serve("GET", "/raw/raw-one", h.withSID(sid)); sameResponse(res, signed) != "" {
				t.Errorf("a signed-in request is answered differently: %s", sameResponse(res, signed))
			}

			head := h.serve("HEAD", "/raw/raw-one")
			if head.Status != 200 || head.Body != "" {
				t.Errorf("HEAD: %d with %d body bytes", head.Status, len(head.Body))
			}
			for _, k := range []string{"Content-Security-Policy", "ETag", "Cache-Control", "Content-Type", "X-Content-Type-Options", "Referrer-Policy", "Cross-Origin-Resource-Policy"} {
				if head.Header.Get(k) != res.Header.Get(k) {
					t.Errorf("HEAD %s = %q, GET has %q", k, head.Header.Get(k), res.Header.Get(k))
				}
			}

			cond := h.serve("GET", "/raw/raw-one", withHeader("If-None-Match", wantETag))
			if cond.Status != 304 || cond.Body != "" {
				t.Errorf("If-None-Match: %d with %d body bytes, want 304", cond.Status, len(cond.Body))
			}
		})
	}
}

func TestPublicRawAfterReplace(t *testing.T) {
	h := newHarness(t, false)
	sid := h.mint(testOwnerID, "the-owner")
	v1, v2 := reportHTML(400, "version-one"), reportHTML(600, "version-two")
	h.mustUpload(sid, "stable", "First title", "published", v1)
	first := h.serve("GET", "/raw/stable")
	if first.Status != 200 || first.Body != string(v1) {
		t.Fatalf("v1: %d", first.Status)
	}
	etag1 := first.Header.Get("ETag")
	if page := h.serve("GET", "/reports/stable"); !strings.Contains(page.Body, "<h1>First title</h1>") {
		t.Fatalf("v1 page:\n%s", page.Body)
	}

	h.clock.Advance(time.Hour)
	h.mustUpload(sid, "stable", "Second title", "published", v2)

	page := h.serve("GET", "/reports/stable")
	if page.Status != 200 || !strings.Contains(page.Body, "<h1>Second title</h1>") || strings.Contains(page.Body, "First title") {
		t.Fatalf("the share URL does not show the replacement:\n%s", page.Body)
	}
	second := h.serve("GET", "/raw/stable")
	if second.Status != 200 || second.Body != string(v2) {
		t.Fatalf("v2: %d, %d bytes", second.Status, len(second.Body))
	}
	if etag2 := second.Header.Get("ETag"); etag2 == etag1 || etag2 != `"`+sha256Hex(string(v2))+`"` {
		t.Errorf("ETag after replace = %q (before %q)", etag2, etag1)
	}
	// A reader holding the old validator must get the new bytes, not a 304.
	stale := h.serve("GET", "/raw/stable", withHeader("If-None-Match", etag1))
	if stale.Status != 200 || stale.Body != string(v2) {
		t.Errorf("If-None-Match with the old ETag: %d, %d bytes, want 200 and the new bytes", stale.Status, len(stale.Body))
	}
}

func TestPublicRawMissingFile(t *testing.T) {
	h := newHarness(t, false)
	sid := h.mint(testOwnerID, "the-owner")
	data := reportHTML(400, "MISSING-FILE-BYTES")
	h.mustUpload(sid, "lost", "Lost file", "published", data)
	id, _, _, ok := h.reportRow("lost")
	if !ok {
		t.Fatal("no row")
	}
	if res := h.serve("GET", "/raw/lost"); res.Status != 200 {
		t.Fatalf("control: %d", res.Status)
	}
	if err := os.Remove(filepath.Join(h.dataDir, "reports", strconv.FormatInt(id, 10)+".html")); err != nil {
		t.Fatal(err)
	}

	res := h.serve("GET", "/raw/lost")
	if res.Status != 500 || res.Body != "internal error\n" {
		t.Fatalf("/raw with the file gone: %d %q, want 500 'internal error'", res.Status, res.Body)
	}
	if strings.Contains(res.Body, "MISSING-FILE-BYTES") || res.Header.Get("Content-Security-Policy") != siteCSP {
		t.Errorf("the failure carries report bytes or a report policy: %q %q", res.Body, res.Header.Get("Content-Security-Policy"))
	}
	// The page is a library lookup and reads no file, so it still opens.
	if page := h.serve("GET", "/reports/lost"); page.Status != 200 || !strings.Contains(page.Body, `src="/raw/lost"`) {
		t.Errorf("the page of a report whose file is gone: %d", page.Status)
	}
}

func TestReaderStoreDown(t *testing.T) {
	h := newHarness(t, false)
	owner := h.mint(testOwnerID, "the-owner")
	other := h.mint(7, "somebody")
	h.mustUpload(owner, "up", "Up", "published", reportHTML(300, "up"))
	for _, p := range []string{"/", "/reports/up", "/raw/up"} {
		if res := h.serve("GET", p); res.Status != 200 {
			t.Fatalf("control: GET %s = %d", p, res.Status)
		}
	}
	h.store.Close()

	for viewer, opts := range map[string][]func(*http.Request){
		"anonymous": nil,
		"owner":     {h.withSID(owner)},
		"non-owner": {h.withSID(other)},
	} {
		for _, p := range []string{"/", "/reports/up"} {
			res := h.serve("GET", p, opts...)
			if res.Status != 503 {
				t.Errorf("%s: GET %s = %d, want 503", viewer, p, res.Status)
				continue
			}
			if !strings.Contains(res.Body, "Reports cannot be shown right now") {
				t.Errorf("%s: GET %s does not say the reports are unavailable:\n%s", viewer, p, res.Body)
			}
			if strings.Contains(res.Body, "<iframe") || strings.Contains(res.Body, "No reports yet") {
				t.Errorf("%s: GET %s frames a report or claims there are none:\n%s", viewer, p, res.Body)
			}
			if strings.Contains(res.Body, "Signed in as") || strings.Contains(res.Body, `href="/admin"`) {
				t.Errorf("%s: GET %s shows a signed-in header with the store down", viewer, p)
			}
			if got := signInNext(t, res.Body); got != "/" {
				t.Errorf("%s: GET %s sign-in next = %q, want / (no stored slug to return to)", viewer, p, got)
			}
			if got := res.Header.Get("Cache-Control"); got != "private, no-cache" {
				t.Errorf("%s: GET %s Cache-Control = %q", viewer, p, got)
			}
		}
		if res := h.serve("GET", "/raw/up", opts...); res.Status != 500 {
			t.Errorf("%s: GET /raw/up = %d, want 500", viewer, res.Status)
		}
	}
}

func TestReportBytesNeverInReaderPages(t *testing.T) {
	const sentinel = "READER-SENTINEL-9d41c7"
	h := newHarness(t, false)
	owner := h.mint(testOwnerID, "the-owner")
	data := reportHTML(500, sentinel)
	h.mustUpload(owner, "inline-check", "Inline check", "published", data)

	if raw := h.serve("GET", "/raw/inline-check"); !strings.Contains(raw.Body, sentinel) {
		t.Fatal("control: the sentinel is not in the report bytes at /raw")
	}
	for viewer, opts := range map[string][]func(*http.Request){
		"anonymous": nil,
		"non-owner": {h.withSID(h.mint(7, "somebody"))},
		"owner":     {h.withSID(owner)},
	} {
		for _, p := range []string{"/", "/reports/inline-check"} {
			res := h.serve("GET", p, opts...)
			if res.Status != 200 {
				t.Fatalf("%s: GET %s = %d", viewer, p, res.Status)
			}
			if strings.Contains(res.Body, sentinel) || strings.Contains(res.Body, "<p>"+sentinel) {
				t.Errorf("%s: report bytes are inlined in %s", viewer, p)
			}
			if !strings.Contains(res.Body, `src="/raw/inline-check"`) {
				t.Errorf("%s: %s does not frame the report (the check would be vacuous)", viewer, p)
			}
		}
	}
}

func TestReportPageSignInReturnsThere(t *testing.T) {
	slug := strings.Repeat("ab-", 26) + "a9" // 80 characters: letters, digits, hyphens
	if len(slug) != 80 {
		t.Fatalf("test slug is %d characters", len(slug))
	}
	h := newHarness(t, false)
	owner := h.mint(testOwnerID, "the-owner")
	h.mustUpload(owner, slug, "Longest slug", "published", reportHTML(300, "long"))
	pagePath := "/reports/" + slug

	page := h.serve("GET", pagePath)
	if page.Status != 200 {
		t.Fatalf("GET %s: %d", pagePath, page.Status)
	}
	if got := signInNext(t, page.Body); got != pagePath {
		t.Errorf("sign-in next on the report page = %q, want %q", got, pagePath)
	}
	if got := signInNext(t, h.serve("GET", "/").Body); got != "/" {
		t.Errorf("sign-in next on the home page = %q, want /", got)
	}
	for _, form := range regexp.MustCompile(`(?is)<form\b[^>]*>`).FindAllString(page.Body, -1) {
		if strings.Contains(form, "/auth/github/login") {
			t.Errorf("a form targets the sign-in URL: %s", form)
		}
	}

	// The whole flow, as a browser does it, ends back on the report.
	c := h.client(true)
	m := anchorRE.FindStringSubmatch(page.Body)
	back := h.get(c, html.UnescapeString(m[1]))
	if back.Status != 200 || !strings.Contains(back.Body, "Signed in as @octocat") || !strings.Contains(back.Body, `src="/raw/`+slug+`"`) {
		t.Errorf("after signing in from the report page: %d\n%s", back.Status, back.Body)
	}

	// Signed in: no anchor, a logout form, the Admin link for the owner only.
	for who, sid := range map[string]string{"owner": owner, "non-owner": h.mint(7, "somebody")} {
		res := h.serve("GET", pagePath, h.withSID(sid))
		if anchorRE.MatchString(res.Body) || !strings.Contains(res.Body, `action="/auth/logout"`) || !strings.Contains(res.Body, "Signed in as @") {
			t.Errorf("%s: signed-in header:\n%s", who, res.Body)
		}
		if has := strings.Contains(res.Body, `href="/admin"`); has != (who == "owner") {
			t.Errorf("%s: Admin link present = %v", who, has)
		}
		if hasMarker(res.Body) {
			t.Errorf("%s: a reader page carries the admin marker", who)
		}
	}
}

// TestReaderPathVariants pins what each odd spelling of a reader path gets,
// from the responses go1.27.4's ServeMux gives (cleaned paths are redirected
// with 307; the rest is the mux's own 404 or 405, before any handler runs).
// Each variant is asked twice, once with x a draft and once with x unknown:
// the two must be the same response, so no spelling tells them apart.
func TestReaderPathVariants(t *testing.T) {
	withDraft := newHarness(t, false)
	withDraft.mustUpload(withDraft.mint(testOwnerID, "the-owner"), "x", "X draft", "draft", reportHTML(300, "x"))
	unknown := newHarness(t, false)

	notFound := func(r result) string {
		if r.Status != 404 || r.Body != "404 page not found\n" {
			return "want the mux's 404"
		}
		return ""
	}
	redirect := func(to string) func(result) string {
		return func(r result) string {
			if r.Status != http.StatusTemporaryRedirect || r.Location() != to {
				return "want a 307 to " + to
			}
			return ""
		}
	}
	notAllowed := func(r result) string {
		if r.Status != 405 || r.Header.Get("Allow") != "GET, HEAD" {
			return "want 405 with Allow: GET, HEAD"
		}
		return ""
	}
	// The vote route takes only POST, so another method on its exact path is
	// the mux's 405 naming POST. A POST to it answers http.NotFound for a draft
	// and for an unknown report alike (anonymous: the report is looked up first).
	voteNotAllowed := func(r result) string {
		if r.Status != 405 || r.Header.Get("Allow") != "POST" {
			return "want 405 with Allow: POST"
		}
		return ""
	}
	commentNotAllowed := func(r result) string {
		if r.Status != 405 || r.Header.Get("Allow") != "POST" {
			return "want 405 with Allow: POST"
		}
		return ""
	}
	// The feedback route has the same shape as the vote and comment routes: POST
	// only.
	feedbackNotAllowed := func(r result) string {
		if r.Status != 405 || r.Header.Get("Allow") != "POST" {
			return "want 405 with Allow: POST"
		}
		return ""
	}
	hiddenVote := func(r result) string {
		if r.Status != 404 || r.Body != "404 page not found\n" {
			return "want http.NotFound's 404"
		}
		return ""
	}
	for _, v := range []struct {
		method, target string
		check          func(result) string
	}{
		{"GET", "//reports/x", redirect("/reports/x")},
		{"GET", "/reports/x/", notFound},
		{"GET", "/reports/", notFound},
		{"GET", "/reports", notFound},
		{"GET", "/REPORTS/x", notFound},
		{"GET", "/reports/x/../y", redirect("/reports/y")},
		{"GET", "//raw/x", redirect("/raw/x")},
		{"GET", "/raw/x/", notFound},
		{"GET", "/raw/", notFound},
		{"GET", "/raw", notFound},
		{"GET", "/raw/x/../y", redirect("/raw/y")},
		{"POST", "/reports/x", notAllowed},
		{"POST", "/raw/x", notAllowed},
		{"GET", "/reports/x/vote", voteNotAllowed},
		{"HEAD", "/reports/x/vote", voteNotAllowed},
		{"PUT", "/reports/x/vote", voteNotAllowed},
		{"POST", "/reports/x/vote/", notFound},
		{"POST", "/reports/x/vote/extra", notFound},
		{"POST", "/reports/vote", notAllowed}, // a report whose slug is "vote": the page route, GET only
		{"POST", "//reports/x/vote", redirect("/reports/x/vote")},
		{"POST", "/reports/x/../y/vote", redirect("/reports/y/vote")},
		{"POST", "/reports/x/vote", hiddenVote},
		// The comment route is the vote route's twin: POST only, and an anonymous
		// POST to a draft x and to an unknown x is the same http.NotFound.
		{"GET", "/reports/x/comments", commentNotAllowed},
		{"HEAD", "/reports/x/comments", commentNotAllowed},
		{"PUT", "/reports/x/comments", commentNotAllowed},
		{"POST", "/reports/x/comments/", notFound},
		// One more segment is the branch page's path (GET /reports/{slug}/comments/{id}),
		// so a POST to it is the mux's 405 naming GET and HEAD, whatever the segment is.
		{"POST", "/reports/x/comments/extra", notAllowed},
		{"POST", "/reports/x/comments/1", notAllowed},
		{"PUT", "/reports/x/comments/1", notAllowed},
		{"POST", "/reports/x/comments/1/", notFound},
		{"POST", "/reports/x/comments/1/extra", notFound},
		{"POST", "/reports/comments", notAllowed}, // a report whose slug is "comments": the page route, GET only
		{"POST", "//reports/x/comments", redirect("/reports/x/comments")},
		{"POST", "/reports/x/../y/comments", redirect("/reports/y/comments")},
		{"POST", "/reports/x/comments", hiddenVote},
		// The branch page: for a draft x and for an unknown x alike, every spelling of
		// the id is the same http.NotFound the handler writes (there is no comment
		// anywhere to find), and the odd spellings of the path are the mux's.
		{"GET", "/reports/x/comments/1", hiddenVote},
		{"HEAD", "/reports/x/comments/1", hiddenVote},
		{"GET", "/reports/x/comments/abc", hiddenVote},
		{"GET", "/reports/x/comments/01", hiddenVote},
		{"GET", "/reports/x/comments/0", hiddenVote},
		{"GET", "/reports/x/comments/-1", hiddenVote},
		{"GET", "/reports/x/comments/%2B1", hiddenVote},
		{"GET", "/reports/x/comments/1/", notFound},
		{"GET", "/reports/x/comments/1/extra", notFound},
		{"GET", "//reports/x/comments/1", redirect("/reports/x/comments/1")},
		{"GET", "/reports/x/../y/comments/1", redirect("/reports/y/comments/1")},
		// The feedback route is the comment route's twin again, with the same
		// answers for the same spellings.
		{"GET", "/reports/x/feedback", feedbackNotAllowed},
		{"HEAD", "/reports/x/feedback", feedbackNotAllowed},
		{"PUT", "/reports/x/feedback", feedbackNotAllowed},
		{"POST", "/reports/x/feedback/", notFound},
		{"POST", "/reports/x/feedback/extra", notFound},
		{"POST", "/reports/feedback", notAllowed}, // a report whose slug is "feedback": the page route, GET only
		{"POST", "//reports/x/feedback", redirect("/reports/x/feedback")},
		{"POST", "/reports/x/../y/feedback", redirect("/reports/y/feedback")},
		{"POST", "/reports/x/feedback", hiddenVote},
	} {
		a, b := withDraft.serve(v.method, v.target), unknown.serve(v.method, v.target)
		if msg := v.check(a); msg != "" {
			t.Errorf("%s %s: %d %q %q: %s", v.method, v.target, a.Status, a.Location(), a.Body, msg)
		}
		if d := sameResponse(a, b); d != "" {
			t.Errorf("%s %s answers a draft x and an unknown x differently: %s", v.method, v.target, d)
		}
	}
}
