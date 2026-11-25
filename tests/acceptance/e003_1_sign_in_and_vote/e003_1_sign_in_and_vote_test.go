// E-003, finish line 1: a reader signs in with GitHub from a report and lands back
// on it (a return target off this site lands home); votes count once per reader per
// report, can be changed or withdrawn, are refused to visitors and to requests from
// other sites; the dislike count reaches no one but the pinned owner, who sees it
// for each report.
//
// It builds ./cmd/hband, starts it with a throwaway configuration and the stand-in
// GitHub, and talks to it only over HTTP, as the VPS proxy forwards a browser. It
// is the Go port of the retired test_e003_1_sign_in_and_vote.py: each assertion
// site of that body has a ported:<id> tag here or in the harness, and a row in
// tests/acceptance/ported.
package e003signinandvote

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sshubham2/hband/internal/testutil/program"
)

const base = "https://hband.test"

var signInLink = regexp.MustCompile(`(?i)sign\s*in\s+with\s+github`)

// forged is a POST that another site sends with a reader's session: no header of
// its own, but the cross-site ones a browser adds.
func forged(t *testing.T, v *program.Visitor, path string, form url.Values) program.Page {
	t.Helper()
	req := v.Request("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Referer", "https://evil.example/")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	return v.Do(t, req)
}

func TestE003SignInAndVote(t *testing.T) {
	// ported:E3-186
	p := program.Start(t, program.Build(t), program.Options{})
	a, z := "e003-vote-older", "e003-vote-newest"
	// ported:E3-328
	owner := p.Owner(t)
	// ported:E3-329
	owner.Publish(t, a, "Older Notes", program.Report("Older Notes"))
	// ported:E3-330
	owner.Publish(t, z, "Newest Notes", program.Report("Newest Notes"))
	who := program.Account(7201, "")
	anon, r1 := p.Anonymous(), p.Anonymous()
	r1.Login = who.Login
	page := r1.GetFollow(t, "/reports/"+a)

	var links []string
	for _, n := range page.Doc().Walk() {
		if n.Tag == "a" && signInLink.MatchString(n.Text(nil)+" "+n.Attrs["aria-label"]) {
			links = append(links, program.Resolve(page.URL, n.Attrs["href"]))
		}
	}
	// ported:E3-336
	program.Expect(t, len(links) > 0, "/reports/%s (%d) has no 'Sign in with GitHub' link", a, page.Status)
	u, _ := url.Parse(links[0])
	query := u.Query()
	key := ""
	for k, vs := range query {
		if pu, err := url.Parse(vs[0]); err == nil && pu.Path == "/reports/"+a {
			key = k
		}
	}
	// ported:E3-339
	program.Expect(t, key != "", "the sign-in link %s carries no return target naming /reports/%s", links[0], a)
	p.Fake.SetUser(who)
	// ported:E3-340
	landed := p.Approve(t, r1, r1.Get(t, links[0]))
	// ported:E3-341
	program.ExpectEq(t, []any{landed.Status, pathOf(landed.URL)}, []any{200, base + "/reports/" + a},
		"signing in from the report landed on %s (%d)", landed.URL, landed.Status)
	// ported:E3-343
	program.Expect(t, strings.Contains(landed.Doc().Text(nil), who.Login), "the report page does not show the reader signed in, by their GitHub login")
	for _, bad := range []string{"https://evil.example/e003", "//evil.example/e003", "/\\evil.example/e003", "javascript:alert(1)"} {
		b := p.Anonymous()
		aimedQuery := u.Query()
		aimedQuery.Set(key, bad)
		aimed := *u
		aimed.RawQuery = aimedQuery.Encode()
		p.Fake.SetUser(program.Account(7209, ""))
		// ported:E3-347
		landed := p.Approve(t, b, b.Get(t, aimed.String()))
		// ported:E3-348
		program.ExpectEq(t, []any{landed.Status, pathOf(landed.URL)}, []any{200, base + "/"},
			"the return target %q landed on %s (%d) %s", bad, landed.URL, landed.Status, landed.Location)
	}
	var rs [3]*program.Visitor
	// ported:E3-350
	for i := range rs {
		rs[i] = p.SignIn(t, program.Account(int64(7202+i), ""))
	}
	r2, r3, r4 := rs[0], rs[1], rs[2]
	likes := func(slug string) int {
		// ported:E3-351
		return program.Count(t, anon.GetFollow(t, "/reports/"+slug).Doc(), "likes", "/reports/"+slug)
	}
	// ported:E3-352
	program.Expect(t, strings.Contains(r1.GetFollow(t, "/reports/"+z).Body, "/reports/"+z+"/vote"), "the report page offers a signed-in reader no vote form")
	// ported:E3-353
	program.Vote(t, r1, z, "up")
	// ported:E3-354
	program.ExpectEq(t, [2]int{likes(z), program.Count(t, anon.GetFollow(t, "/").Doc(), "likes", "/")}, [2]int{1, 1}, "one thumbs up, on the report page and the home page")
	// ported:E3-355
	program.Vote(t, r1, z, "up")
	// ported:E3-356
	program.ExpectEq(t, likes(z), 1, "pressing thumbs up again changed the like count")
	// ported:E3-357
	program.Vote(t, r1, z, "down")
	// ported:E3-358
	program.ExpectEq(t, [2]int{likes(z), program.Dislikes(t, owner, z)}, [2]int{0, 1}, "switching to thumbs down: no like, one dislike")
	// ported:E3-359
	program.Vote(t, r1, z, "none")
	// ported:E3-360
	program.ExpectEq(t, [2]int{likes(z), program.Dislikes(t, owner, z)}, [2]int{0, 0}, "withdrawing the vote: neither")
	// ported:E3-361
	program.Vote(t, r1, a, "up")
	program.Vote(t, r2, a, "up")
	program.Vote(t, r1, z, "up")
	// ported:E3-362
	program.ExpectEq(t, [2]int{likes(a), likes(z)}, [2]int{2, 1}, "two readers' likes on one report, one reader's likes on two")
	for _, kind := range []string{"up", "down"} { // a visitor who is not signed in, and another site sending r3's session
		anon.GetFollow(t, "/reports/"+a)
		// ported:E3-364
		program.Expect(t, anon.Post(t, "/reports/"+a+"/vote", url.Values{"vote": {kind}}).Status < 500, "a visitor's vote request broke the program")
		// ported:E3-365
		program.Expect(t, forged(t, r3, "/reports/"+a+"/vote", url.Values{"vote": {kind}}).Status < 500, "a forged vote request broke the program")
	}
	// ported:E3-366
	program.ExpectEq(t, []int{likes(a), likes(z), program.Dislikes(t, owner, a), program.Dislikes(t, owner, z)}, []int{2, 1, 0, 0}, "a refused vote changed a count")
	// ported:E3-367
	program.Vote(t, r2, a, "none")
	program.Vote(t, r1, z, "none")
	all := []*program.Visitor{anon, r1, r2, r3, r4}
	for _, b := range all {
		b.Seen = nil
	}
	for _, c := range []struct {
		b    *program.Visitor
		slug string
	}{{r2, a}, {r3, a}, {r4, a}, {r3, z}, {r4, z}} {
		// ported:E3-371
		program.Vote(t, c.b, c.slug, "down")
	}
	for _, b := range all {
		for _, path := range []string{"/", "/reports", "/reports/" + a, "/reports/" + z, "/admin", "/admin/reports/" + a, "/admin/reports/" + z} {
			b.Get(t, path)
		}
	}
	var leaks []string
	for _, b := range all {
		for _, pg := range b.Seen {
			if beside := program.Beside(pg); beside[2] || beside[3] {
				who := b.Login
				if who == "" {
					who = "a visitor"
				}
				leaks = append(leaks, pg.URL+" to "+who)
			}
		}
	}
	// ported:E3-376
	program.Expect(t, len(leaks) == 0, "a dislike count was shown to someone who is not the owner: %v", leaks)
	// ported:E3-377
	program.ExpectEq(t, []int{likes(a), likes(z), program.Dislikes(t, owner, a), program.Dislikes(t, owner, z)}, []int{1, 0, 3, 2}, "each report's likes and the owner's dislikes")
}

// pathOf is a URL without its query and fragment.
func pathOf(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		return u[:i]
	}
	return u
}
