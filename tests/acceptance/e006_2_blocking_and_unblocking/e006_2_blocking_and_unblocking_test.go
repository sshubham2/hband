// E-006, finish line 2: a blocked account's comments leave public view and every
// count on every report and at any depth, and its attempts to comment, reply, send
// feedback or vote are refused and stored nowhere while its vote stays counted; the
// Blocked users page lists its comments (with report and parent) and its feedback,
// which its report's admin view marks as blocked; unblocking there restores its
// comments and counts, and it can take part again.
//
// It is the Go port of the retired test_e006_2_blocking_and_unblocking.py, driving
// the built ./cmd/hband over HTTP. Each assertion site of that body has a
// ported:<id> tag here or in the harness, and a row in tests/acceptance/ported.
package e006blockingandunblocking

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sshubham2/hband/internal/testutil/fakegithub"
	"github.com/sshubham2/hband/internal/testutil/program"
)

var (
	feedbackID = regexp.MustCompile(`^feedback-\d+$`)
	blockedRE  = regexp.MustCompile(`(?i)\bblocked\b`)
)

func TestE006BlockingAndUnblocking(t *testing.T) {
	// ported:E62-185
	p := program.Start(t, program.Build(t), program.Options{})
	a, z := "e006-block-alpha", "e006-block-beta"
	// ported:E62-375
	owner := p.Owner(t)
	// ported:E62-376
	owner.Publish(t, a, "Alpha Notes", program.Report("Alpha Notes"))
	// ported:E62-377
	owner.Publish(t, z, "Beta Notes", program.Report("Beta Notes"))
	X, Y, R, Q := program.Account(8101, ""), program.Account(8102, ""), program.Account(8103, ""), program.Account(8104, "")
	var signed []*program.Visitor
	// ported:E62-379
	for _, u := range []fakegithub.User{X, Y, R, Q} {
		signed = append(signed, p.SignIn(t, u))
	}
	x, y, rr, q := signed[0], signed[1], signed[2], signed[3]
	anon, tx := p.Anonymous(), map[string]string{}
	for _, k := range []string{"Ra", "Qb", "Xa", "Xr", "Xb", "Yb", "Xf", "Yf", "Rf", "Na", "Nr", "Nf", "Back"} {
		tx[k] = program.Mark(k)
	}
	// ported:E62-381
	ids := map[string]string{"Ra": program.Say(t, rr, a, tx["Ra"], "", nil), "Qb": program.Say(t, q, z, tx["Qb"], "", nil)}
	// ported:E62-382
	ids["Xa"] = program.Say(t, x, a, tx["Xa"], "", nil)
	ids["Xr"] = program.Say(t, x, a, tx["Xr"], ids["Ra"], nil)
	ids["Xb"] = program.Say(t, x, z, tx["Xb"], "", nil)
	ids["Yb"] = program.Say(t, y, z, tx["Yb"], ids["Qb"], nil)
	for _, c := range []struct {
		b       *program.Visitor
		slug, k string
	}{{x, a, "Xf"}, {y, z, "Yf"}, {rr, a, "Rf"}} {
		c.b.GetFollow(t, "/reports/"+c.slug)
		// ported:E62-384
		program.Expect(t, c.b.PostFollow(t, "/reports/"+c.slug+"/feedback", url.Values{"body": {tx[c.k]}}).Status < 400, "sending feedback failed")
	}
	// ported:E62-385
	program.Vote(t, x, a, "up")
	program.Vote(t, q, a, "up")
	program.Vote(t, y, a, "down")
	x.GetFollow(t, "/reports/"+a) // the page its attempts after the block are sent from
	likes := func(slug string) int {
		// ported:E62-386
		return program.Count(t, anon.GetFollow(t, "/reports/"+slug).Doc(), "likes", "/reports/"+slug)
	}
	// ported:E62-387
	program.ExpectEq(t, []int{likes(a), program.Dislikes(t, owner, a), program.Visible(t, a, anon), program.Visible(t, z, anon)}, []int{2, 1, 3, 3}, "before any block")
	for _, u := range []fakegithub.User{X, Y} {
		owner.GetFollow(t, "/reports/"+a)
		// ported:E62-389
		program.Expect(t, owner.PostFollow(t, fmt.Sprintf("/admin/users/%d/block", u.ID), nil).Status < 400, "blocking failed")
	}
	for _, path := range []string{"/reports/" + a, "/reports/" + z, "/"} {
		for _, b := range []*program.Visitor{anon, rr} {
			page := b.GetFollow(t, path).Body
			var still []string
			for _, k := range []string{"Xa", "Xr", "Xb", "Yb"} {
				if strings.Contains(page, tx[k]) {
					still = append(still, k)
				}
			}
			for _, u := range []fakegithub.User{X, Y} {
				if strings.Contains(page, u.Login) {
					still = append(still, u.Login)
				}
			}
			who := b.Login
			if who == "" {
				who = "a visitor"
			}
			// ported:E62-393
			program.Expect(t, len(still) == 0, "%s still shows %s what a blocked account posted: %v", path, who, still)
		}
	}
	// ported:E62-395
	program.ExpectEq(t, [2]int{program.Visible(t, a, anon), program.Visible(t, z, anon)}, [2]int{1, 1}, "the public comment counts still count a blocked account's comments")
	// ported:E62-396
	program.ExpectEq(t, [2]int{likes(a), program.Dislikes(t, owner, a)}, [2]int{2, 1}, "blocking changed a vote count")
	for _, c := range []struct {
		path   string
		fields url.Values
	}{
		{"/reports/" + a + "/comments", url.Values{"body": {tx["Na"]}}},
		{"/reports/" + a + "/comments", url.Values{"body": {tx["Nr"]}, "parent": {ids["Ra"]}}},
		{"/reports/" + a + "/feedback", url.Values{"body": {tx["Nf"]}}},
		{"/reports/" + a + "/vote", url.Values{"vote": {"down"}}},
		{"/reports/" + a + "/vote", url.Values{"vote": {"none"}}},
		{"/reports/" + z + "/vote", url.Values{"vote": {"up"}}},
	} {
		// sent from the report page the account was served before the block
		// ported:E62-400
		program.Expect(t, x.Post(t, c.path, c.fields).Status < 500, "%s broke for a blocked account", c.path)
	}
	// ported:E62-401
	program.ExpectEq(t, []int{likes(a), likes(z), program.Dislikes(t, owner, a)}, []int{2, 0, 1}, "a blocked account cast, changed or withdrew a vote")
	listing := owner.GetFollow(t, "/admin/blocked")
	views := map[string]program.Page{a: owner.GetFollow(t, "/admin/reports/"+a), z: owner.GetFollow(t, "/admin/reports/"+z)}
	report := owner.GetFollow(t, "/reports/"+a)
	var kept []string
	for _, k := range []string{"Na", "Nr", "Nf"} {
		for _, pg := range []program.Page{listing, views[a], views[z], report} {
			if strings.Contains(pg.Body, tx[k]) {
				kept = append(kept, k)
				break
			}
		}
	}
	// ported:E62-404
	program.Expect(t, len(kept) == 0, "a blocked account's attempts were stored: %v", kept)
	posts := []string{"Xa", "Xr", "Xb", "Yb", "Xf", "Yf"}
	for _, c := range []struct {
		u      fakegithub.User
		theirs []string
		other  fakegithub.User
	}{{X, []string{"Xa", "Xr", "Xb", "Xf"}, Y}, {Y, []string{"Yb", "Yf"}, X}} {
		others := []string{c.other.Login}
		for _, k := range posts {
			if !contains(c.theirs, k) {
				others = append(others, tx[k])
			}
		}
		// ported:E62-407
		box := program.Part(t, listing, c.u.Login, others)
		var missing []string
		for _, k := range c.theirs {
			if !strings.Contains(box.Text(nil), tx[k]) {
				missing = append(missing, k)
			}
		}
		// ported:E62-408
		program.Expect(t, len(missing) == 0, "the Blocked users page does not list everything %s posted: %v", c.u.Login, missing)
	}
	for _, c := range []struct{ k, slug, title, answered string }{
		{"Xa", a, "Alpha Notes", ""}, {"Xr", a, "Alpha Notes", "Ra"}, {"Xb", z, "Beta Notes", ""}, {"Yb", z, "Beta Notes", "Qb"},
	} {
		var others []string
		for _, o := range posts {
			if o != c.k {
				others = append(others, tx[o])
			}
		}
		// ported:E62-411
		entry := program.Part(t, listing, tx[c.k], others)
		var links []string
		for _, n := range append([]*program.Node{entry}, entry.Walk()...) {
			if n.Tag == "a" {
				links = append(links, n.Attrs["href"])
			}
		}
		toReport := regexp.MustCompile(`/reports/` + regexp.QuoteMeta(c.slug) + `\b`)
		named := strings.Contains(entry.Text(nil), c.title)
		for _, h := range links {
			named = named || toReport.MatchString(h)
		}
		// ported:E62-413
		program.Expect(t, named, "the Blocked users page lists %s without its report", c.k)
		answered := c.answered == "" || strings.Contains(entry.Text(nil), tx[c.answered])
		for _, h := range links {
			answered = answered || (c.answered != "" && strings.Contains(h, "comment-"+ids[c.answered]))
		}
		// ported:E62-414
		program.Expect(t, answered, "the Blocked users page lists %s without the comment it replied to", c.k)
	}
	for _, c := range []struct{ slug, k, fine string }{{a, "Xf", "Rf"}, {z, "Yf", ""}} {
		var items []*program.Node
		for _, n := range views[c.slug].Doc().Walk() {
			if feedbackID.MatchString(n.Attrs["id"]) {
				items = append(items, n)
			}
		}
		marked := func(key string) bool {
			for _, n := range items {
				if strings.Contains(n.Text(nil), tx[key]) && blockedRE.MatchString(n.Text(nil)) {
					return true
				}
			}
			return false
		}
		// ported:E62-419
		program.Expect(t, marked(c.k), "feedback %s is not in /admin/reports/%s marked as blocked", c.k, c.slug)
		// ported:E62-420
		program.Expect(t, !(c.fine != "" && marked(c.fine)), "feedback from an account that is not blocked is marked as blocked")
	}
	// ported:E62-421
	program.Expect(t, strings.Contains(listing.Body, fmt.Sprintf("/admin/users/%d/unblock", X.ID)), "the Blocked users page offers no Unblock for the account")
	// ported:E62-422
	program.Expect(t, owner.PostFollow(t, fmt.Sprintf("/admin/users/%d/unblock", X.ID), nil).Status < 400, "unblocking failed")
	_, got := program.Shown(anon.GetFollow(t, "/reports/"+a).Doc())
	back := got[ids["Xa"]] != nil && got[ids["Xr"]] != nil
	if back {
		back = false
		for _, n := range got[ids["Xr"]].Chain() {
			back = back || n == got[ids["Ra"]]
		}
	}
	// ported:E62-424
	program.Expect(t, back, "unblocking did not bring its comments back")
	_, onZ := program.Shown(anon.GetFollow(t, "/reports/"+z).Doc())
	// ported:E62-425
	program.Expect(t, onZ[ids["Xb"]] != nil && onZ[ids["Yb"]] == nil, "unblocking one account")
	// ported:E62-426
	program.ExpectEq(t, [2]int{program.Visible(t, a, anon), program.Visible(t, z, anon)}, [2]int{3, 2}, "unblocking did not count its comments again")
	// ported:E62-427
	program.Expect(t, program.Say(t, x, a, program.Mark("Again"), "", nil) != "", "the unblocked account cannot comment")
	x.GetFollow(t, "/reports/"+a)
	// ported:E62-428
	program.Expect(t, x.PostFollow(t, "/reports/"+a+"/feedback", url.Values{"body": {tx["Back"]}}).Status < 400, "the unblocked account's feedback failed")
	// ported:E62-429
	program.Expect(t, strings.Contains(owner.GetFollow(t, "/admin/reports/"+a).Body, tx["Back"]), "the unblocked account cannot send feedback")
	// ported:E62-430
	program.Vote(t, x, a, "down")
	// ported:E62-431
	program.ExpectEq(t, [2]int{likes(a), program.Dislikes(t, owner, a)}, [2]int{1, 2}, "the unblocked account cannot vote")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
