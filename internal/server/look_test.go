package server

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/sshubham2/hband/internal/store"
	"github.com/sshubham2/hband/internal/testutil/headless"
)

//go:embed testdata/look/bare.html testdata/look/probe.js testdata/look/site.before.css
var lookFS embed.FS

// This file holds the look of every screen to what the design says, in a real
// Chrome (CHROME_BIN, through headless.Open), as measured computed styles and
// layout, never as a picture somebody judges. A screen is measured at 1280 px and
// at a true 375 CSS px mobile viewport, under prefers-color-scheme light and dark,
// signed out, as a reader and as the owner, closed and with every details opened.
// The page-side measurements are testdata/look/probe.js; the browser's own
// default look is read from testdata/look/bare.html, which has no stylesheet.

// lookWorld is a seeded site: an owner, a reader with a 39-character login, a
// second reader who is blocked, a published report with a thread deeper than twice
// the depth cap, a removed comment with a visible reply, the author's own comment,
// a vote, the reader's feedback with the author's reply, and a draft.
type lookWorld struct {
	h        *harness
	owner    string // the owner's session
	reader   string // a signed-in reader, who is not the owner
	slug     string
	branch   string // the branch page the report page's own "continue this thread" points at
	readerID int64
}

func seedLook(t *testing.T) *lookWorld {
	t.Helper()
	h := newHarness(t, false)
	// A same-origin path that answers 404: the avatar is a broken image, and Chrome
	// never leaves the machine.
	const avatar = "/static/no-such-avatar.png"
	w := &lookWorld{h: h, slug: "x", readerID: 7}
	w.owner = h.mintUser(store.User{GitHubID: testOwnerID, Login: "the-owner", AvatarURL: avatar})
	w.reader = h.mintUser(store.User{GitHubID: w.readerID, Login: "reader-" + strings.Repeat("x", 32), AvatarURL: avatar}) // 39 characters
	troll := h.mintUser(store.User{GitHubID: 9, Login: "troll", AvatarURL: avatar})
	h.mustUpload(w.owner, "x", "Report X", "published", reportHTML(2000, "x"))
	h.mustUpload(w.owner, "draft", "A draft", "draft", reportHTML(400, "draft"))

	// A chain of 2*cap+2 levels, with an unbroken 300-character body at the cap.
	rep, _, _, _ := h.reportRow("x")
	book := h.book()
	var chain []int64
	parent := int64(0)
	for i := 1; i <= 2*threadDepthCap+2; i++ {
		body := "chain-" + strconv.Itoa(i) + "-end"
		if i == threadDepthCap {
			body = strings.Repeat("W", 300)
		}
		id, err := book.Post(bg, rep, w.readerID, parent, body)
		if err != nil {
			t.Fatalf("seed chain level %d: %v", i, err)
		}
		chain = append(chain, id)
		parent = id
	}
	h.say(w.owner, "x", "The author answers the first comment.", chain[0])
	gone := h.say(w.reader, "x", "This one is removed.", 0)
	h.say(w.reader, "x", "A reply under the removed comment.", gone)
	res := h.serve("POST", "/admin/comments/"+strconv.FormatInt(gone, 10)+"/remove", h.withSID(w.owner), withBody(formType, []byte("from=admin")))
	if res.Status != http.StatusSeeOther {
		t.Fatalf("seed: remove a comment: %d %q", res.Status, res.Body)
	}
	h.say(troll, "x", "A comment from an account that is then blocked.", 0)
	h.sendFeedback(troll, "x", "A message from an account that is then blocked.")
	h.mustBlock(w.owner, 9)

	if r := h.postVote(w.reader, "x", "vote=up"); r.Status != http.StatusSeeOther {
		t.Fatalf("seed: vote: %d", r.Status)
	}
	h.sendFeedback(w.reader, "x", "A private note to the author.")
	var fid int64
	if err := h.store.DB().QueryRow(`SELECT id FROM feedback WHERE github_id = ?`, w.readerID).Scan(&fid); err != nil {
		t.Fatalf("seed: the reader's feedback id: %v", err)
	}
	h.mustReply(w.owner, fid, "x", "The author's answer to the private note.")

	page := h.serve("GET", "/reports/x", h.withSID(w.reader))
	m := regexp.MustCompile(`href="(/reports/x/comments/\d+)"`).FindStringSubmatch(page.Body)
	if m == nil {
		t.Fatalf("the report page has no continue-this-thread link after a chain of %d levels", len(chain))
	}
	w.branch = m[1]
	return w
}

type lookState struct {
	name     string
	w        *lookWorld
	sid      string
	path     string
	refuse   bool // POST an invalid upload from the /admin form in the browser and measure the answer
	scripts  int  // <script> tags the page owes: 1 where a thread is drawn, else 0
	capped   bool // the page draws a comment at the depth cap
	shots    bool // owner screens, held to the signal-colour assay
	forced   bool // re-measured with hover and focus forced on every control
	adminNav bool // the page is an admin page, so it draws the sidebar, and no other page does
}

const refuseUpload = `(() => { const f = document.querySelector('form[action="/admin/reports"]'); f.noValidate = true;
  f.querySelector('[name=title]').value = 'T'; f.querySelector('[name=slug]').value = 'Bad Slug!'; f.submit(); })()`

// roleSeen lists every role the probe judges. A role that no pass of the run met is
// a role nothing measured.
var roleSeen = []string{"danger confirm in .remove", "danger confirm in .block", "danger delete", "accent sign in",
	"accent post comment", "accent post reply", "accent send feedback", "accent send reply", "accent upload",
	"danger summary of .remove", "danger summary of .block",
	"accent page-head action", "neutral page-head action", "current admin nav item"}

type probeResult struct {
	Violations []string       `json:"violations"`
	Seen       map[string]int `json:"seen"`
	Stats      map[string]any `json:"stats"`
}

type lookBrowser struct {
	t    *testing.T
	tab  *headless.Tab
	js   string
	bare map[string]map[string]map[string]string // scheme: kind: property: value
	tok  map[string][2]string
}

func (b *lookBrowser) eval(call string, args any, out any) {
	b.t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		b.t.Fatal(err)
	}
	b.tab.Eval(b.js+"."+call+"("+string(raw)+")", out)
}

func (b *lookBrowser) setScheme(dark bool) {
	v := "light"
	if dark {
		v = "dark"
	}
	b.tab.Call("Emulation.setEmulatedMedia", map[string]any{"features": []map[string]string{{"name": "prefers-color-scheme", "value": v}}})
}

func (b *lookBrowser) setViewer(h *harness, sid string) {
	b.tab.Call("Network.clearBrowserCookies", nil)
	if sid != "" {
		b.tab.Call("Network.setCookie", map[string]any{"name": h.sidName(), "value": sid, "url": h.srv.URL})
	}
}

// visit opens a state as a new document: through about:blank, so that a fragment
// like #feedback-sent is a navigation that fires a load event and not a jump.
func (b *lookBrowser) visit(st lookState) {
	b.t.Helper()
	b.setViewer(st.w.h, st.sid)
	b.tab.Navigate("about:blank")
	if st.refuse {
		b.tab.Navigate(st.w.h.srv.URL + "/admin")
		b.tab.Submit(refuseUpload)
		var got bool
		b.tab.Eval(`document.querySelector('p.error[role=alert]') !== null`, &got)
		if !got {
			b.t.Fatalf("%s: the refused upload was not answered with the page's p.error", st.name)
		}
		return
	}
	b.tab.Navigate(st.w.h.srv.URL + st.path)
}

func (b *lookBrowser) tokens(dark bool) map[string]string {
	i := 0
	if dark {
		i = 1
	}
	rgb := func(name string) string {
		r, g, bl := hexRGB(b.tok[name][i])
		return fmt.Sprintf("rgb(%d, %d, %d)", r, g, bl)
	}
	return map[string]string{"danger": rgb("danger"), "onDanger": rgb("on-danger"), "accent": rgb("accent"), "onAccent": rgb("on-accent"),
		"surface2": rgb("surface-2"), "text": rgb("text")}
}

func (b *lookBrowser) args(st lookState, dark bool, width int, mobile bool) map[string]any {
	scheme := "light"
	if dark {
		scheme = "dark"
	}
	return map[string]any{"scripts": st.scripts, "width": width, "mobile": mobile, "bare": b.bare[scheme], "tokens": b.tokens(dark),
		"cap": threadDepthCap, "minBody": 240, "adminNav": st.adminNav}
}

func (b *lookBrowser) measure(st lookState, dark bool, width int, mobile bool) probeResult {
	b.t.Helper()
	var res probeResult
	b.eval("look", b.args(st, dark, width, mobile), &res)
	return res
}

// forcedPass re-measures contrast with :hover, then :focus-visible, forced on every
// control, which no seeded page ever draws on its own.
func (b *lookBrowser) forcedPass(st lookState, dark bool, width int, mobile bool) (violations []string) {
	b.t.Helper()
	var doc struct {
		Root struct {
			NodeID int `json:"nodeId"`
		} `json:"root"`
	}
	if err := json.Unmarshal(b.tab.Call("DOM.getDocument", map[string]any{"depth": 0}), &doc); err != nil {
		b.t.Fatal(err)
	}
	var q struct {
		NodeIDs []int `json:"nodeIds"`
	}
	raw := b.tab.Call("DOM.querySelectorAll", map[string]any{"nodeId": doc.Root.NodeID, "selector": "a[href], button, summary, input, select, textarea, tbody tr"})
	if err := json.Unmarshal(raw, &q); err != nil {
		b.t.Fatal(err)
	}
	if len(q.NodeIDs) == 0 {
		b.t.Fatalf("%s: no controls to force a state on", st.name)
	}
	for _, state := range []string{"hover", "focus-visible"} {
		for _, id := range q.NodeIDs {
			b.tab.Call("CSS.forcePseudoState", map[string]any{"nodeId": id, "forcedPseudoClasses": []string{state}})
		}
		args := b.args(st, dark, width, mobile)
		args["only"], args["focus"] = "contrast", state == "focus-visible"
		var res probeResult
		b.eval("look", args, &res)
		for _, v := range res.Violations {
			violations = append(violations, "forced "+state+": "+v)
		}
		for _, id := range q.NodeIDs {
			b.tab.Call("CSS.forcePseudoState", map[string]any{"nodeId": id, "forcedPseudoClasses": []string{}})
		}
	}
	return violations
}

// dump writes the page as a PNG under dir/look, for a person to look at, cut off
// at 6000 px tall. Its caller calls it only when HBAND_TEST_ARTIFACTS is set (the
// one test setting the headless package already reads for its screenshots). The
// test judges nothing from it.
func (b *lookBrowser) dump(dir, state, viewport, scheme string, width int) {
	b.t.Helper()
	var h float64
	b.tab.Eval(`Math.min(document.documentElement.scrollHeight, 6000)`, &h)
	raw := b.tab.Call("Page.captureScreenshot", map[string]any{"format": "png", "captureBeyondViewport": true,
		"clip": map[string]any{"x": 0, "y": 0, "width": width, "height": h, "scale": 1}})
	var res struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		b.t.Fatal(err)
	}
	png, err := base64.StdEncoding.DecodeString(res.Data)
	if err != nil {
		b.t.Fatal(err)
	}
	name := regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(state+" "+viewport+" "+scheme), "-")
	if err := os.MkdirAll(filepath.Join(dir, "look"), 0o755); err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "look", name+".png"), png, 0o644); err != nil {
		b.t.Fatal(err)
	}
}

// shoot is a screenshot of the viewport.
func shoot(t *testing.T, tab *headless.Tab) image.Image {
	t.Helper()
	var res struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(tab.Call("Page.captureScreenshot", map[string]any{"format": "png"}), &res); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(res.Data)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	return img
}

func newLookBrowser(t *testing.T) *lookBrowser {
	t.Helper()
	tab := headless.Open(t)
	src, err := lookFS.ReadFile("testdata/look/probe.js")
	if err != nil {
		t.Fatal(err)
	}
	css, err := staticFS.ReadFile("static/site.css")
	if err != nil {
		t.Fatal(err)
	}
	b := &lookBrowser{t: t, tab: tab, js: strings.TrimSpace(string(src)), tok: tokenTable(string(css)), bare: map[string]map[string]map[string]string{}}
	tab.Call("DOM.enable", nil)
	tab.Call("CSS.enable", nil)

	// The browser's own look, read once per scheme from a page with no stylesheet.
	html, _ := lookFS.ReadFile("testdata/look/bare.html")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(html)
	}))
	t.Cleanup(srv.Close)
	for _, dark := range []bool{false, true} {
		b.setScheme(dark)
		tab.Navigate("about:blank")
		tab.Navigate(srv.URL)
		scheme := "light"
		if dark {
			scheme = "dark"
		}
		var bare map[string]map[string]string
		tab.Eval(b.js+".bare()", &bare)
		if len(bare) < 9 {
			t.Fatalf("read the bare look of %d kinds of element, want 9", len(bare))
		}
		b.bare[scheme] = bare
	}
	return b
}

func TestEveryScreenIsDesigned(t *testing.T) {
	w := seedLook(t)
	empty := newHarness(t, false)
	emptyOwner := empty.mint(testOwnerID, "the-owner")
	we := &lookWorld{h: empty, owner: emptyOwner}

	b := newLookBrowser(t)
	tab := b.tab

	states := []lookState{
		{name: "anonymous home", w: w, path: "/", scripts: 1, capped: true},
		{name: "anonymous report", w: w, path: "/reports/x", scripts: 1, capped: true},
		{name: "anonymous all reports", w: w, path: "/reports"},
		{name: "anonymous branch", w: w, path: w.branch, scripts: 1, capped: true},
		{name: "reader home", w: w, sid: w.reader, path: "/", scripts: 1, capped: true},
		{name: "reader report", w: w, sid: w.reader, path: "/reports/x", scripts: 1, capped: true, forced: true},
		{name: "reader report after sending feedback", w: w, sid: w.reader, path: "/reports/x#feedback-sent", scripts: 1, capped: true},
		{name: "reader all reports", w: w, sid: w.reader, path: "/reports"},
		{name: "reader branch", w: w, sid: w.reader, path: w.branch, scripts: 1, capped: true, forced: true},
		{name: "reader at the admin door", w: w, sid: w.reader, path: "/admin"},
		{name: "owner report", w: w, sid: w.owner, path: "/reports/x", scripts: 1, capped: true, shots: true, forced: true},
		{name: "owner admin", w: w, sid: w.owner, path: "/admin", shots: true, forced: true, adminNav: true},
		{name: "owner admin after a refused upload", w: w, sid: w.owner, refuse: true, shots: true, adminNav: true},
		{name: "owner admin report", w: w, sid: w.owner, path: "/admin/reports/x", shots: true, forced: true, adminNav: true},
		{name: "owner admin report of a draft", w: w, sid: w.owner, path: "/admin/reports/draft", shots: true, adminNav: true},
		{name: "owner blocked users", w: w, sid: w.owner, path: "/admin/blocked", shots: true, forced: true, adminNav: true},
		{name: "owner empty home", w: we, sid: we.owner, path: "/"},
		{name: "owner empty all reports", w: we, sid: we.owner, path: "/reports"},
		{name: "owner empty admin", w: we, sid: we.owner, path: "/admin", adminNav: true},
		{name: "owner empty blocked users", w: we, sid: we.owner, path: "/admin/blocked", adminNav: true},
	}
	type vp struct {
		name   string
		w, h   int
		mobile bool
	}
	viewports := []vp{{"desktop 1280", 1280, 900, false}, {"phone 375", 375, 812, true}}

	seen := map[string]int{}
	minText, texts, controls := 99.0, 0, 0
	htmlBg := map[string][2]string{}
	var problems []string
	note := func(st lookState, v vp, scheme, pass string, lines []string) {
		for _, l := range lines {
			problems = append(problems, fmt.Sprintf("%s | %s | %s | %s: %s", st.name, v.name, scheme, pass, l))
		}
	}
	record := func(st lookState, v vp, scheme string, dark bool, pass string, res probeResult) {
		note(st, v, scheme, pass, res.Violations)
		for role, n := range res.Seen {
			seen[role] += n
		}
		if m, ok := res.Stats["minText"].(float64); ok && m < minText {
			minText = m
		}
		if n, ok := res.Stats["texts"].(float64); ok {
			texts += int(n)
		}
		if n, ok := res.Stats["controls"].(float64); ok {
			controls += int(n)
		}
		if pass == "closed" {
			key := st.name + " | " + v.name
			bgs := htmlBg[key]
			i := 0
			if dark {
				i = 1
			}
			bgs[i], _ = res.Stats["htmlBg"].(string)
			htmlBg[key] = bgs
			if st.capped && v.mobile {
				if n, _ := res.Stats["atCap"].(float64); n < 1 {
					note(st, v, scheme, pass, []string{"thread-width: no comment body at depth " + strconv.Itoa(threadDepthCap) + " on the page, so the width check measured nothing"})
				}
			}
		}
	}

	for _, v := range viewports {
		tab.Viewport(v.w, v.h, v.mobile)
		for _, dark := range []bool{false, true} {
			b.setScheme(dark)
			scheme := "light"
			if dark {
				scheme = "dark"
			}
			for _, st := range states {
				b.visit(st)
				record(st, v, scheme, dark, "closed", b.measure(st, dark, v.w, v.mobile))
				var details float64
				tab.Eval(`document.querySelectorAll('details').length`, &details)
				if details > 0 {
					tab.Eval(`document.querySelectorAll('details').forEach((d) => { d.open = true; })`, nil)
					record(st, v, scheme, dark, "details open", b.measure(st, dark, v.w, v.mobile))
				}
				if dir := os.Getenv("HBAND_TEST_ARTIFACTS"); dir != "" {
					b.dump(dir, st.name, v.name, scheme, v.w)
				}
				if !v.mobile && st.forced {
					note(st, v, scheme, "forced", b.forcedPass(st, dark, v.w, v.mobile))
				}
				if !v.mobile && st.shots {
					img := shoot(t, tab)
					for _, sig := range []color.RGBA{{255, 0, 0, 255}, {0, 255, 0, 255}} {
						if n := headless.Count(img, sig, 8); n != 0 {
							note(st, v, scheme, "screenshot", []string{fmt.Sprintf("signal-colour: %d pixels within 8 of %v", n, sig)})
						}
					}
				}
			}
		}
	}

	// The thread at the depth cap on the narrowest phone the design names: 360 px.
	tab.Viewport(360, 800, true)
	b.setScheme(false)
	for _, dark := range []bool{false, true} {
		b.setScheme(dark)
		scheme := "light"
		if dark {
			scheme = "dark"
		}
		for _, st := range states {
			if st.capped && st.sid != w.owner && (strings.Contains(st.name, "branch") || st.name == "reader report") {
				b.visit(st)
				v := vp{"phone 360", 360, 800, true}
				record(st, v, scheme, dark, "closed", b.measure(st, dark, 360, true))
				tab.Eval(`document.querySelectorAll('details').forEach((d) => { d.open = true; })`, nil)
				record(st, v, scheme, dark, "details open", b.measure(st, dark, 360, true))
			}
		}
	}

	// (e) the scheme switch took effect: the page background differs between schemes.
	for key, bgs := range htmlBg {
		if bgs[0] == "" || bgs[1] == "" || bgs[0] == bgs[1] {
			problems = append(problems, fmt.Sprintf("%s: html background is %q in light and %q in dark: the scheme did not switch", key, bgs[0], bgs[1]))
		}
	}
	for _, role := range roleSeen {
		if seen[role] == 0 {
			problems = append(problems, "role never measured on any screen: "+role)
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		shown := problems
		if len(shown) > 60 {
			shown = shown[:60]
		}
		t.Errorf("%d problems with the look of the screens:\n%s", len(problems), strings.Join(shown, "\n"))
	}
	t.Logf("%d states x 2 widths x 2 schemes measured; %d texts (lowest %.2f:1) and %d controls judged; roles seen %v", len(states), texts, minText, controls, seen)
}

// The measurement has to be able to fail. The stylesheet before this slice drew
// every control and link as the browser does and scrolled /admin/reports/<slug>
// sideways at 375 px; run the same probe on it and it must say so.
func TestLookProbeSeesTheStylesheetBeforeTheDesign(t *testing.T) {
	old, err := lookFS.ReadFile("testdata/look/site.before.css")
	if err != nil {
		t.Fatal(err)
	}
	w := seedLook(t)
	h := w.h
	inner := h.current()
	h.mu.Lock()
	h.handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/static/site.css" {
			rw.Header().Set("Content-Type", "text/css; charset=utf-8")
			rw.Write(old)
			return
		}
		inner.ServeHTTP(rw, r)
	})
	h.mu.Unlock()
	b := newLookBrowser(t)

	codes := func(res probeResult) map[string]bool {
		m := map[string]bool{}
		for _, v := range res.Violations {
			m[strings.SplitN(v, ":", 2)[0]] = true
		}
		return m
	}
	b.tab.Viewport(1280, 900, false)
	b.setScheme(false)
	got := map[string]bool{}
	for _, st := range []lookState{
		{name: "reader report", w: w, sid: w.reader, path: "/reports/x", scripts: 1},
		{name: "owner admin", w: w, sid: w.owner, path: "/admin", adminNav: true},
	} {
		b.visit(st)
		var details float64
		b.tab.Eval(`document.querySelectorAll('details').forEach((d) => { d.open = true; }); document.querySelectorAll('details').length`, &details)
		for c := range codes(b.measure(st, false, 1280, false)) {
			got[c] = true
		}
	}
	for _, want := range []string{"default-link", "default-button", "default-textarea", "default-th", "default-td", "default-text", "default-select"} {
		if !got[want] {
			t.Errorf("the stylesheet before the design is not flagged %q: the default-look check cannot fail (flagged: %v)", want, got)
		}
	}

	// At 375 px the old /admin/reports/<slug> overflows, and the measurement says so
	// against the width asked for, not against the widened layout viewport.
	b.tab.Viewport(375, 812, true)
	st := lookState{name: "owner admin report", w: w, sid: w.owner, path: "/admin/reports/x"}
	b.visit(st)
	res := b.measure(st, false, 375, true)
	sw, _ := res.Stats["scrollWidth"].(float64)
	iw, _ := res.Stats["innerWidth"].(float64)
	t.Logf("the stylesheet before the design at 375 px: scrollWidth %v, innerWidth %v", sw, iw)
	if sw <= 375 || !codes(res)["fit"] {
		t.Errorf("the old /admin/reports/x at 375 px reads scrollWidth %v and flags %v: the fit check cannot fail", sw, codes(res))
	}
}
