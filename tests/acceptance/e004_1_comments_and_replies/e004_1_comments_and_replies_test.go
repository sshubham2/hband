// E-004, finish line 1: plain-text comments and replies at any depth show under
// their report and on the home page with the writer's GitHub login and avatar,
// nested beneath what they answer, top-level oldest first; typed HTML stays literal
// text; visitors and cross-report replies are refused; only the pinned owner's
// posts carry an 'Author' badge.
//
// It is the Go port of the retired test_e004_1_comments_and_replies.py, driving the
// built ./cmd/hband over HTTP. Each assertion site of that body has a ported:<id>
// tag here or in the harness, and a row in tests/acceptance/ported.
package e004commentsandreplies

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sshubham2/hband/internal/testutil/fakegithub"
	"github.com/sshubham2/hband/internal/testutil/program"
)

var wordAuthor = regexp.MustCompile(`(?i)author`)

// hasAuthorBadge is the word 'author', not part of a longer word or a hyphenated one.
func hasAuthorBadge(s string) bool {
	for _, m := range wordAuthor.FindAllStringIndex(s, -1) {
		before, after := byte(' '), byte(' ')
		if m[0] > 0 {
			before = s[m[0]-1]
		}
		if m[1] < len(s) {
			after = s[m[1]]
		}
		word := func(c byte) bool {
			return c == '_' || c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
		}
		if !word(before) && !word(after) {
			return true
		}
	}
	return false
}

type wrote struct {
	text string
	who  fakegithub.User
	to   string
}

func TestE004CommentsAndReplies(t *testing.T) {
	// ported:E41-182
	p := program.Start(t, program.Build(t), program.Options{})
	a, z := "e004-talk-alpha", "e004-talk-beta"
	// ported:E41-351
	owner := p.Owner(t)
	// ported:E41-352
	owner.Publish(t, z, "Beta Notes", program.Report("Beta Notes"))
	// ported:E41-353
	owner.Publish(t, a, "Alpha Notes", program.Report("Alpha Notes"))
	people := []fakegithub.User{program.Account(program.OwnerID, "report-author"), program.Account(7401, ""), program.Account(7402, ""), program.Account(4243, "report-author")}
	b := []*program.Visitor{owner}
	// ported:E41-355
	for _, who := range people[1:] {
		b = append(b, p.SignIn(t, who))
	}
	// ported:E41-356
	program.Expect(t, strings.Contains(b[1].GetFollow(t, "/reports/"+a).Body, "/reports/"+a+"/comments"), "the report page offers a signed-in reader no comment form")
	typed := ` <b>bold</b> <script>window.hbandRan=1</script> <img src=x onerror="alert(1)"> & done`
	written := map[string]wrote{}
	var order []string

	post := func(k int, text, to string) string {
		// ported:E41-361
		i := program.Say(t, b[k], a, text, to, nil)
		// ported:E41-362
		program.Expect(t, i != "", "/reports/%s does not show the comment %s once it is posted", a, text)
		written[i] = wrote{text, people[k], to}
		order = append(order, i)
		return i
	}
	// ported:E41-365
	first := post(1, program.Mark("First")+typed, "")
	time.Sleep(1100 * time.Millisecond)
	// ported:E41-367
	second := post(0, program.Mark("Second"), "")
	time.Sleep(1100 * time.Millisecond)
	// ported:E41-369
	third := post(3, program.Mark("Third"), "")
	// ported:E41-370
	post(1, program.Mark("Deeper"), post(0, program.Mark("Owned"), post(2, program.Mark("Answer"), first)))
	// ported:E41-371
	post(3, program.Mark("Echo"), second)
	// ported:E41-372
	far, anon, ghosts := program.Say(t, b[1], z, program.Mark("Elsewhere"), "", nil), p.Anonymous(), []string{program.Mark("Ghost"), program.Mark("Ghost"), program.Mark("Ghost")}
	anon.GetFollow(t, "/reports/"+a)
	// ported:E41-373
	program.Expect(t, anon.Post(t, "/reports/"+a+"/comments", url.Values{"body": {ghosts[0]}}).Status < 500, "a visitor's comment broke the program")
	// ported:E41-374
	program.Expect(t, anon.Post(t, "/reports/"+a+"/comments", url.Values{"body": {ghosts[1]}, "parent": {first}}).Status < 500, "a visitor's reply broke the program")
	// ported:E41-375
	program.Expect(t, b[1].Post(t, "/reports/"+a+"/comments", url.Values{"body": {ghosts[2]}, "parent": {far}}).Status < 500, "a cross-report reply broke the program")
	for _, c := range []struct {
		path   string
		viewer *program.Visitor
	}{{"/reports/" + a, p.Anonymous()}, {"/", p.Anonymous()}, {"/reports/" + a, owner}, {"/reports/" + z, owner}} {
		page := c.viewer.GetFollow(t, c.path)
		shown := false
		for _, g := range ghosts {
			shown = shown || strings.Contains(page.Body, g)
		}
		// ported:E41-378
		program.Expect(t, !shown, "%s shows a comment that should have been refused", c.path)
		want := len(written)
		if strings.HasSuffix(c.path, z) {
			want = 1
		}
		counted := program.Counts(page.Doc())["comments"]
		// ported:E41-379
		program.Expect(t, len(counted) == 0 || (len(counted) == 1 && counted[want]), "%s counts a refused comment: %v", c.path, program.Numbers(counted))
		if strings.HasSuffix(c.path, z) {
			continue
		}
		showOrder, got := program.Shown(page.Doc())
		for _, i := range order {
			w := written[i]
			n := got[i]
			// ported:E41-385
			program.Expect(t, n != nil && strings.Contains(program.Own(n), strings.SplitN(w.text, " ", 2)[0]), "%s does not show comment-%s", c.path, i)
			// ported:E41-386
			program.Expect(t, strings.Contains(program.Own(n), w.who.Login), "comment-%s on %s does not show its writer's GitHub login", i, c.path)
			avatar := strings.SplitN(w.who.AvatarURL, "?", 2)[0]
			hasImg := false
			for _, x := range program.Mine(n) {
				hasImg = hasImg || (x.Tag == "img" && strings.HasPrefix(x.Attrs["src"], avatar))
			}
			// ported:E41-387
			program.Expect(t, hasImg, "comment-%s on %s does not show its writer's GitHub avatar", i, c.path)
			// ported:E41-389
			program.ExpectEq(t, hasAuthorBadge(program.Own(n)), w.who.ID == program.OwnerID, "comment-%s on %s by GitHub user %d: an 'Author' badge only on the pinned owner's", i, c.path, w.who.ID)
			beneath := w.to == ""
			if !beneath {
				for _, anc := range n.Chain()[:len(n.Chain())-1] {
					beneath = beneath || anc == got[w.to]
				}
			}
			// ported:E41-391
			program.Expect(t, beneath, "comment-%s on %s is not shown beneath comment-%s, which it answers", i, c.path, w.to)
		}
		// ported:E41-392
		program.Expect(t, strings.Contains(program.Own(got[first]), strings.TrimSpace(typed)), "%s does not show the HTML typed into a comment as literal text", c.path)
		markup := false
		for _, x := range page.Doc().Walk() {
			raw := ""
			for _, k := range x.Kids {
				raw += k.Data
			}
			markup = markup || (x.Tag == "b" && strings.Contains(x.Text(nil), "bold")) ||
				(x.Tag == "script" && strings.Contains(raw, "hbandRan")) || x.Attrs["onerror"] != ""
		}
		// ported:E41-393
		program.Expect(t, !markup, "HTML typed into a comment is markup on %s", c.path)
		var tops []string
		for _, i := range showOrder {
			if i == first || i == second || i == third {
				tops = append(tops, i)
			}
		}
		// ported:E41-395
		program.ExpectEq(t, tops, []string{first, second, third}, "%s lists top-level comments out of order", c.path)
	}
}
