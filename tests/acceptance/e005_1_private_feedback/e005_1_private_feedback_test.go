// E-005, finish line 1: feedback a signed-in reader sends from a report stays out of
// public view, comments and comment counts, and visitors cannot send any; the pinned
// owner reads each message with its sender beside the report's dislike count in its
// admin view, which no one else may open; the owner's reply shows, with the reader's
// own message, beneath the feedback box to that reader alone.
//
// It is the Go port of the retired test_e005_1_private_feedback.py, driving the built
// ./cmd/hband over HTTP. Each assertion site of that body has a ported:<id> tag here
// or in the harness, and a row in tests/acceptance/ported.
package e005privatefeedback

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sshubham2/hband/internal/testutil/program"
)

var feedbackID = regexp.MustCompile(`^feedback-(\d+)$`)

func TestE005PrivateFeedback(t *testing.T) {
	// ported:E5-184
	p := program.Start(t, program.Build(t), program.Options{})
	a, z := "e005-feedback-alpha", "e005-feedback-beta"
	// ported:E5-318
	owner := p.Owner(t)
	// ported:E5-319
	owner.Publish(t, z, "Beta Notes", program.Report("Beta Notes"))
	// ported:E5-320
	owner.Publish(t, a, "Alpha Notes", program.Report("Alpha Notes"))
	p1, p2 := program.Account(7901, ""), program.Account(7902, "")
	// ported:E5-322
	s1, s2, anon := p.SignIn(t, p1), p.SignIn(t, p2), p.Anonymous()
	// ported:E5-323
	program.Expect(t, strings.Contains(s1.GetFollow(t, "/reports/"+a).Body, "/reports/"+a+"/feedback"), "the report page offers a signed-in reader no 'feedback for the author' form")
	f1, f2, f3, ghost, answer := program.Mark("Kind"), program.Mark("Blunt"), program.Mark("Other"), program.Mark("Ghost"), program.Mark("Answer")
	for _, c := range []struct {
		b          *program.Visitor
		slug, text string
	}{{s1, a, f1}, {s2, a, f2}, {s1, z, f3}} {
		c.b.GetFollow(t, "/reports/"+c.slug)
		// ported:E5-326
		program.Expect(t, c.b.PostFollow(t, "/reports/"+c.slug+"/feedback", url.Values{"body": {c.text}}).Status < 400, "sending feedback failed")
	}
	anon.GetFollow(t, "/reports/"+a)
	// ported:E5-327
	program.Expect(t, anon.Post(t, "/reports/"+a+"/feedback", url.Values{"body": {ghost}}).Status < 500, "a visitor's feedback broke the program")
	// ported:E5-328
	program.Vote(t, s1, a, "down")
	program.Vote(t, s2, a, "down")
	for _, c := range []struct {
		b      *program.Visitor
		hidden []string
	}{{anon, []string{f1, f2, f3}}, {s2, []string{f1, f3}}} {
		for _, path := range []string{"/", "/reports", "/reports/" + a, "/reports/" + z, "/reports/" + a + "/feedback"} {
			got := c.b.GetFollow(t, path)
			who := c.b.Login
			if who == "" {
				who = "a visitor"
			}
			shown := false
			for _, h := range c.hidden {
				shown = shown || strings.Contains(got.Body, h)
			}
			// ported:E5-332
			program.Expect(t, !shown, "%s shows %s feedback another reader sent", path, who)
			// ported:E5-333
			program.Expect(t, atMost(program.Counts(got.Doc())["comments"], 0), "%s counts feedback as a comment", path)
		}
	}
	page := s1.GetFollow(t, "/reports/"+a)
	// ported:E5-335
	program.Expect(t, atMost(program.Counts(page.Doc())["comments"], 0), "the sender's report page counts their feedback as a comment")
	among := false
	for _, n := range page.Doc().Walk() {
		among = among || (program.IsComment(n) && strings.Contains(n.Text(nil), f1))
	}
	// ported:E5-336
	program.Expect(t, !among, "the feedback shows among the comments")
	view := owner.GetFollow(t, "/admin/reports/"+a)
	// ported:E5-339
	program.Expect(t, !strings.Contains(view.Body, ghost), "a visitor who is not signed in sent feedback")
	items := map[string]*program.Node{}
	var order []string
	for _, n := range view.Doc().Walk() {
		if m := feedbackID.FindStringSubmatch(n.Attrs["id"]); m != nil {
			if _, dup := items[m[1]]; !dup {
				order = append(order, m[1])
			}
			items[m[1]] = n
		}
	}
	holding := func(text string) (string, *program.Node) {
		for _, id := range order {
			if strings.Contains(items[id].Text(nil), text) {
				return id, items[id]
			}
		}
		return "", nil
	}
	for _, c := range []struct {
		text       string
		who, other string
	}{{f1, p1.Login, p2.Login}, {f2, p2.Login, p1.Login}} {
		_, item := holding(c.text)
		// ported:E5-343
		program.Expect(t, item != nil, "/admin/reports/%s (%d) shows no element id=feedback-<n> holding %s", a, view.Status, c.text)
		said := item.Text(nil)
		// ported:E5-344
		program.Expect(t, strings.Contains(said, c.who) && !strings.Contains(said, c.other), "feedback %s is not shown with the GitHub account that sent it", c.text)
	}
	// ported:E5-346
	program.Expect(t, !strings.Contains(view.Body, f3), "this report's admin view shows feedback sent on another report")
	// ported:E5-347
	program.ExpectEq(t, program.Count(t, view.Doc(), "dislikes", "/admin/reports/"+a), 2, "the admin view does not show the report's dislike count")
	// ported:E5-348
	for _, b := range []*program.Visitor{p.Anonymous(), s1, p.SignIn(t, program.Account(4243, "report-author"))} {
		got := b.Get(t, "/admin/reports/"+a)
		who := b.Login
		if who == "" {
			who = "a visitor"
		}
		// ported:E5-350
		program.Expect(t, got.Status < 500 && !strings.Contains(got.Body, f1) && !strings.Contains(got.Body, f2), "%s was shown the admin view", who)
	}
	fid, _ := holding(f1)
	// ported:E5-352
	program.Expect(t, strings.Contains(view.Body, "/admin/feedback/"+fid+"/reply"), "the admin view offers no reply to the feedback")
	// ported:E5-353
	program.Expect(t, owner.PostFollow(t, "/admin/feedback/"+fid+"/reply", url.Values{"body": {answer}}).Status < 400, "the owner's reply failed")
	page = s1.GetFollow(t, "/reports/"+a)
	box := strings.Index(page.Body, "/reports/"+a+"/feedback")
	text := page.Doc().Text(nil)
	// ported:E5-356
	program.Expect(t, strings.Contains(text, f1) && strings.Contains(text, answer), "the reader's report page does not show their feedback and the reply")
	// ported:E5-357
	program.Expect(t, box >= 0 && box < strings.Index(page.Body, f1) && box < strings.Index(page.Body, answer), "their feedback and the reply are not shown beneath the feedback box")
	for _, b := range []*program.Visitor{s2, p.Anonymous()} {
		got := b.GetFollow(t, "/reports/"+a).Body
		who := b.Login
		if who == "" {
			who = "a visitor"
		}
		// ported:E5-360
		program.Expect(t, !strings.Contains(got, f1) && !strings.Contains(got, answer), "%s sees another reader's feedback or the reply to it", who)
	}
	// ported:E5-361
	program.Expect(t, strings.Contains(s2.GetFollow(t, "/reports/"+a).Doc().Text(nil), f2), "a reader does not see their own feedback beneath the box")
}

// atMost is Python's set <= {n}: no number, or only n.
func atMost(got map[int]bool, n int) bool {
	for x := range got {
		if x != n {
			return false
		}
	}
	return true
}
