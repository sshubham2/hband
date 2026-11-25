// E-004, finish line 2: no page nests a comment more than five levels deep; a
// 'continue this thread' link opens the deeper branch on its own page, which links
// back to the report and takes replies; each comment with replies has a toggle
// that, in a browser and by the site's own script, hides and shows what is beneath
// it; the report path's Content-Security-Policy still forbids every script.
//
// It is the Go port of the retired test_e004_2_deep_threads_and_toggle.py, and of
// the signed-in look of .taad/slices/SL-008/run_finish_e004_2.py, in two tests:
//
//   - TestE004DeepThreadsAndBranchPages makes every assertion that goes over HTTP,
//     on the production-shaped https base, with no browser, and the look's seed;
//   - TestE004FoldAndFitInBrowser makes the assertions only a browser can: the
//     body's toggle, signed out, and the look, signed in, at 360 and 400 px
//     (mobile) and 1000 px (desktop). Its browser comes only from headless.Open,
//     which skips the test, before anything is built, where there is none to use.
//
// Each assertion site of the two Python sources has a ported:<id> tag here or in
// the harness, and a row in tests/acceptance/ported.
package e004deepthreadsandtoggle

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sshubham2/hband/internal/testutil/headless"
	"github.com/sshubham2/hband/internal/testutil/program"
)

var continueRE = regexp.MustCompile(`(?i)continue\s+this\s+thread`)

// body is what the finish body's posting steps leave behind.
type body struct {
	a           string
	ids         [6]string
	side, under string
	texts       [8]string
	branch      string
	reportPage  program.Page
	readers     [2]*program.Visitor
	owner       *program.Visitor
}

// seedBody is the finish body's own sequence: it publishes two reports, posts a
// thread eight levels deep (levels 7 and 8 from the branch page) and three more
// comments, and makes, on the way, every assertion the body makes over HTTP.
func seedBody(t *testing.T, p *program.Program) body {
	t.Helper()
	base, _ := url.Parse(p.Base)
	b := body{a: "e004-branch-alpha"}
	a := b.a
	// ported:E42-397
	b.owner = p.Owner(t)
	// ported:E42-398
	b.owner.Publish(t, "e004-branch-beta", "Beta Notes", program.Report("Beta Notes"))
	// ported:E42-399
	b.owner.Publish(t, a, "Alpha Notes", program.Report("Alpha Notes"))
	// ported:E42-400
	for i := range b.readers {
		b.readers[i] = p.SignIn(t, program.Account(int64(7501+i), ""))
	}
	for i := range b.texts {
		b.texts[i] = program.Mark("Level")
	}
	rs, texts := b.readers, b.texts
	var ids []string
	for k := 0; k < 6; k++ { // levels 1 to 6, each answered from the report page; the sixth is not shown there
		parent := ""
		if len(ids) > 0 {
			parent = ids[len(ids)-1]
		}
		// ported:E42-402
		ids = append(ids, program.Say(t, rs[k%2], a, texts[k], parent, nil))
	}
	copy(b.ids[:], ids)
	page := p.Anonymous().GetFollow(t, "/reports/"+a)
	_, got := program.Shown(page.Doc())
	allShown := true
	for _, id := range ids[:5] {
		allShown = allShown && id != ""
	}
	// ported:E42-405
	program.Expect(t, allShown && !strings.Contains(page.Body, texts[5]), "/reports/%s should show levels 1 to 5 and not level 6", a)
	var depths []int
	for _, id := range ids[:5] {
		if got[id] == nil {
			depths = append(depths, 0)
			continue
		}
		depths = append(depths, program.Depth(got[id]))
	}
	// ported:E42-406
	program.ExpectEq(t, depths, []int{1, 2, 3, 4, 5}, "the five levels are not nested one in another")
	var more []*program.Node
	for _, x := range got[ids[4]].Walk() {
		if x.Tag == "a" && continueRE.MatchString(x.Text(nil)) {
			more = append(more, x)
		}
	}
	// ported:E42-408
	program.Expect(t, len(more) > 0, "comment-%s, five levels deep, has no 'continue this thread' link", ids[4])
	branch := program.Resolve(page.URL, more[0].Attrs["href"])
	b.branch = branch
	for k := 6; k <= 7; k++ { // levels 7 and 8, answered from the branch page
		there := rs[k%2].GetFollow(t, branch)
		// ported:E42-412
		program.Expect(t, strings.Contains(there.Body, "/reports/"+a+"/comments"), "the branch page %s offers a signed-in reader no reply form", branch)
		// ported:E42-413
		program.Expect(t, program.Find(there.Doc(), texts[k-1]) != "", "the branch page %s does not show level %d", branch, k)
		// ported:E42-414
		program.Expect(t, program.Say(t, rs[k%2], a, texts[k], program.Find(there.Doc(), texts[k-1]), &there) != "", "a reply sent from the branch page does not show there")
	}
	there := p.Anonymous().GetFollow(t, branch)
	deeper := true
	for _, tx := range texts[5:] {
		deeper = deeper && program.Find(there.Doc(), tx) != ""
	}
	// ported:E42-416
	program.Expect(t, deeper, "the branch page does not show levels 6 to 8")
	back := false
	for _, x := range there.Doc().Walk() {
		if x.Tag == "a" {
			to := program.Resolve(there.URL, x.Attrs["href"])
			if i := strings.IndexAny(to, "?#"); i >= 0 {
				to = to[:i]
			}
			back = back || to == p.Base+"/reports/"+a
		}
	}
	// ported:E42-417
	program.Expect(t, back, "the branch page has no link back to the full report")
	for _, pg := range []program.Page{p.Anonymous().GetFollow(t, "/reports/"+a), p.Anonymous().GetFollow(t, "/"), there} {
		_, by := program.Shown(pg.Doc())
		deepest := 0
		for _, n := range by {
			if d := program.Depth(n); d > deepest {
				deepest = d
			}
		}
		// ported:E42-420
		program.Expect(t, deepest <= 5, "%s nests a comment more than five levels deep", pg.URL)
		shows := false
		for _, tx := range texts[5:] {
			shows = shows || strings.Contains(pg.Body, tx)
		}
		// ported:E42-421
		program.Expect(t, pg.URL == there.URL || !shows, "%s shows a reply deeper than five levels", pg.URL)
	}
	// ported:E42-422
	b.side = program.Say(t, rs[0], a, program.Mark("Aside"), "", nil)
	// ported:E42-423
	b.under = program.Say(t, rs[1], a, program.Mark("Under"), b.side, nil)
	// ported:E42-424
	program.Say(t, rs[0], a, program.Mark("Below"), b.under, nil)
	page = p.Anonymous().GetFollow(t, "/reports/"+a)
	b.reportPage = page
	own := false
	for _, x := range page.Doc().Walk() {
		if x.Tag == "script" && x.Attrs["src"] != "" {
			s := program.Resolve(page.URL, x.Attrs["src"])
			if u, err := url.Parse(s); err == nil && u.Host == base.Host && p.Anonymous().GetFollow(t, s).Status == 200 {
				own = true
			}
		}
	}
	// ported:E42-427
	program.Expect(t, own, "the report page loads no script from the site's own path")
	var frames []string
	for _, x := range page.Doc().Walk() {
		if x.Tag == "iframe" {
			frames = append(frames, program.Resolve(page.URL, x.Attrs["src"]))
		}
	}
	// ported:E42-430
	program.Expect(t, len(frames) > 0, "the report page embeds no report")
	csp := p.Anonymous().GetFollow(t, frames[0]).Header.Get("Content-Security-Policy")
	rules := map[string][]string{}
	for _, part := range strings.Split(csp, ";") {
		f := strings.Fields(part)
		if len(f) == 0 {
			continue
		}
		var vals []string
		for _, s := range f[1:] {
			vals = append(vals, strings.ToLower(s))
		}
		rules[strings.ToLower(f[0])] = vals
	}
	get := func(k string) ([]string, bool) { v, ok := rules[k]; return v, ok }
	forbids := true
	for _, k := range []string{"script-src", "script-src-elem", "script-src-attr"} {
		v, ok := get(k)
		if !ok {
			if v, ok = get("script-src"); !ok {
				v = rules["default-src"]
			}
		}
		forbids = forbids && len(v) == 1 && v[0] == "'none'"
	}
	// ported:E42-433
	program.Expect(t, forbids, "the report path's CSP does not forbid every script: %q", csp)
	return b
}

// TestE004DeepThreadsAndBranchPages makes every HTTP assertion of the finish body,
// on the https base the program runs behind in use, and the look's seed.
func TestE004DeepThreadsAndBranchPages(t *testing.T) {
	// ported:E42-184
	p := program.Start(t, program.Build(t), program.Options{})
	seedBody(t, p)
	program.DeepThread(t, p)
}

// fold is the finish body's toggle script (FOLD), verbatim.
const fold = `(async id => {
  const top = document.getElementById("comment-" + id);
  if (!top) return "the page has no comment-" + id;
  const holder = e => { for (let x = e; x; x = x.parentElement) if (/^comment-\d+$/.test(x.id)) return x; };
  const nested = [...top.querySelectorAll("[id]")].filter(e => e !== top && /^comment-\d+$/.test(e.id));
  const said = e => [e.textContent, e.getAttribute("aria-label"), e.getAttribute("title")].join(" ").trim();
  const toggle = [...top.querySelectorAll("button, summary, a, [role=button]")].find(e => holder(e) === top &&
    /collapse|expand|hide|show|toggle|^\[?\s*[-+−–]\s*\]?$/i.test(said(e)));
  if (!toggle) return "comment-" + id + " has no toggle of its own";
  const seen = () => nested.map(e => e.getClientRects().length > 0 && getComputedStyle(e).visibility !== "hidden");
  const pause = () => new Promise(r => setTimeout(r, 400));
  const before = seen(); toggle.click(); await pause(); const folded = seen(); toggle.click(); await pause();
  return JSON.stringify({before, folded, after: seen()});
})("ID")`

// toggle is the look's toggle script (run_finish_e004_2.py), verbatim but for the id.
const toggle = `(async () => {
          const out = {};
          const root = document.getElementById("comment-%s");
          const btn = [...root.children].find(e => e.matches("button.toggle"));
          const replies = document.getElementById(btn.getAttribute("aria-controls"));
          const pause = () => new Promise(r => setTimeout(r, 300));
          out.shown = !btn.hidden; out.before = [replies.hidden, btn.getAttribute("aria-expanded"), btn.textContent];
          btn.click(); await pause();
          out.folded = [replies.hidden, btn.getAttribute("aria-expanded"), btn.textContent, replies.getClientRects().length];
          btn.click(); await pause();
          out.after = [replies.hidden, btn.getAttribute("aria-expanded"), btn.textContent, replies.getClientRects().length > 0];
          out.replyOpen = [...document.querySelectorAll("details.reply")].length;
          return JSON.stringify(out);
        })()`

// TestE004FoldAndFitInBrowser makes the assertions only a browser can: the finish
// body's toggle on three comments, signed out, and the signed-in look of a branch
// page and its report at phone and desktop widths, with the toggle read exactly.
func TestE004FoldAndFitInBrowser(t *testing.T) {
	tab := headless.Open(t) // first: with no browser to use, the test ends here, before anything is built
	p := program.Start(t, program.Build(t), program.Options{Loopback: true})
	b := seedBody(t, p)

	// ported:E42-437
	tab.Navigate(p.Base + "/reports/" + b.a)
	for _, cid := range []string{b.ids[0], b.side, b.under} {
		var said string
		// ported:E42-439
		tab.Eval(strings.Replace(fold, `"ID"`, `"`+cid+`"`, 1), &said)
		// ported:E42-440
		program.Expect(t, strings.HasPrefix(said, "{"), "%s", said)
		var seen struct{ Before, Folded, After []bool }
		if err := json.Unmarshal([]byte(said), &seen); err != nil {
			t.Fatalf("the toggle's reading %q: %v", said, err)
		}
		// ported:E42-442
		program.Expect(t, len(seen.Before) > 0 && all(seen.Before) && !anyTrue(seen.Folded) && all(seen.After),
			"comment-%s's toggle does not hide and then show the replies beneath it: %+v", cid, seen)
	}

	// ported:RE-209
	th := program.DeepThread(t, p)
	p.Fake.SetUser(program.Account(8802, "plain-reader"))
	tab.Navigate(p.Base + "/auth/github/login?next=" + url.QueryEscape(th.Branch))
	var landed struct {
		Path    string `json:"path"`
		Replies int    `json:"replies"`
	}
	tab.Eval(`({path: location.pathname, replies: document.querySelectorAll("details.reply").length})`, &landed)
	// not ported, added: the look must measure the signed-in page, with its reply forms
	if landed.Path != th.Branch || landed.Replies == 0 {
		t.Fatalf("Chrome did not sign itself in through the stand-in GitHub: it is on %q with %d reply form(s), want %s with some", landed.Path, landed.Replies, th.Branch)
	}
	for _, v := range []struct {
		width, height int
		mobile        bool
	}{{360, 780, true}, {400, 860, true}, {1000, 900, false}} {
		tab.Viewport(v.width, v.height, v.mobile)
		for _, where := range []string{th.Branch, "/reports/" + th.Slug} {
			tab.Navigate(p.Base + where)
			tab.Eval(`document.querySelectorAll('details.reply').forEach(d => { d.open = true; })`, nil)
			var wide [2]float64
			tab.Eval(`[document.documentElement.scrollWidth, window.innerWidth]`, &wide)
			t.Logf("%dpx %s: scrollWidth %v, innerWidth %v", v.width, where, wide[0], wide[1])
			// against the width asked for, not innerWidth: a mobile page that overflows widens its own layout viewport
			// ported:RE-221,RE-260
			if wide[0] > float64(v.width) {
				t.Errorf("at %d px, %s is %v px wide: it does not fit", v.width, where, wide[0])
			}
		}
	}
	tab.Viewport(400, 860, true)
	tab.Navigate(p.Base + th.Branch)
	var state string
	tab.Eval(strings.Replace(toggle, "%s", th.IDs[4], 1), &state)
	t.Logf("toggle clicked twice: %s", state)
	var s struct {
		Shown  bool  `json:"shown"`
		Before []any `json:"before"`
		Folded []any `json:"folded"`
		After  []any `json:"after"`
	}
	if err := json.Unmarshal([]byte(state), &s); err != nil {
		t.Fatalf("the toggle's reading %q: %v", state, err)
	}
	// ported:RE-245
	program.ExpectEq(t, append([]any{s.Shown}, s.Before...), []any{true, false, "true", "Hide replies"}, "before a click: %s", state)
	// ported:RE-246
	program.ExpectEq(t, s.Folded, []any{true, "false", "Show replies", float64(0)}, "after one click: %s", state)
	// ported:RE-247
	program.ExpectEq(t, s.After, []any{false, "true", "Hide replies", true}, "after a second click: %s", state)
}

func all(xs []bool) bool {
	for _, x := range xs {
		if !x {
			return false
		}
	}
	return true
}

func anyTrue(xs []bool) bool {
	for _, x := range xs {
		if x {
			return true
		}
	}
	return false
}
