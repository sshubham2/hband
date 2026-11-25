// E-006, finish line 1: the pinned owner removes comments and replies at any depth:
// their text and author leave view and the visible-comment count drops; a removed or
// blocked comment with a visible reply beneath becomes a bare 'comment removed'
// placeholder holding it, one with nothing beneath vanishes; removing, blocking,
// unblocking and the Blocked users page are refused to everyone else.
//
// It is the Go port of the retired test_e006_1_removal_and_placeholders.py, driving
// the built ./cmd/hband over HTTP. Each assertion site of that body has a
// ported:<id> tag here or in the harness, and a row in tests/acceptance/ported.
package e006removalandplaceholders

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/sshubham2/hband/internal/testutil/fakegithub"
	"github.com/sshubham2/hband/internal/testutil/program"
)

var removedWords = regexp.MustCompile(`(?i)comment removed`)

func TestE006RemovalAndPlaceholders(t *testing.T) {
	// ported:E61-184,CB-72,CB-85
	p := program.Start(t, program.Build(t), program.Options{})
	a, z := "e006-remove-alpha", "e006-remove-beta"
	// ported:E61-374
	owner := p.Owner(t)
	// ported:E61-375
	owner.Publish(t, z, "Beta Notes", program.Report("Beta Notes"))
	// ported:E61-376
	owner.Publish(t, a, "Alpha Notes", program.Report("Alpha Notes"))
	var acc []fakegithub.User
	for i := 0; i < 4; i++ {
		acc = append(acc, program.Account(int64(8001+i), ""))
	}
	var r []*program.Visitor
	// ported:E61-378
	for _, x := range acc {
		r = append(r, p.SignIn(t, x))
	}
	anon, twin := p.Anonymous(), p.SignIn(t, program.Account(4243, "report-author"))
	type node struct {
		key    string
		writer int
		to     string
	}
	tree := []node{{"Top", 0, ""}, {"Ans", 1, "Top"}, {"Mid", 0, "Ans"}, {"End", 1, "Mid"}, {"Side", 2, ""}, {"Kid", 0, "Side"},
		{"Up", 1, ""}, {"Leaf", 2, "Up"}, {"Lone", 3, ""}}
	text, ids, writer, parent := map[string]string{}, map[string]string{}, map[string]int{}, map[string]string{}
	var keys []string
	for _, n := range tree {
		text[n.key], writer[n.key], parent[n.key] = program.Mark(n.key), n.writer, n.to
		keys = append(keys, n.key)
		// ported:E61-384
		ids[n.key] = program.Say(t, r[n.writer], a, text[n.key], ids[n.to], nil)
	}
	// ported:E61-385
	program.Say(t, r[1], z, program.Mark("Far"), "", nil)
	state := map[string]string{}
	for _, k := range keys {
		state[k] = "live"
	}

	look := func(step string) {
		t.Helper()
		for _, c := range []struct {
			path string
			b    *program.Visitor
		}{{"/reports/" + a, anon}, {"/", anon}, {"/reports/" + a, r[1]}} {
			page := c.b.GetFollow(t, c.path)
			_, got := program.Shown(page.Doc())
			for _, k := range keys {
				s := state[k]
				n := got[ids[k]]
				if s == "gone" {
					// ported:E61-395
					program.Expect(t, n == nil && !strings.Contains(page.Body, text[k]), "after %s, %s still shows comment %s", step, c.path, k)
					continue
				}
				held := ""
				if s == "held" {
					held = " as a placeholder"
				}
				// ported:E61-397
				program.Expect(t, n != nil, "after %s, %s does not show comment %s%s", step, c.path, k, held)
				own := program.Own(n)
				// ported:E61-398
				program.Expect(t, s == "live" || (removedWords.MatchString(own) && !strings.Contains(own, text[k]) && !strings.Contains(own, acc[writer[k]].Login)),
					"after %s, comment %s on %s is no bare 'comment removed' placeholder: %q", step, k, c.path, own)
				under := parent[k] == ""
				if !under {
					chain := n.Chain()
					for _, x := range chain[:len(chain)-1] {
						under = under || x == got[ids[parent[k]]]
					}
				}
				// ported:E61-400
				program.Expect(t, under, "after %s, comment %s on %s is not nested under the comment it answers", step, k, c.path)
			}
			for i, x := range acc {
				live := false
				for _, k := range keys {
					live = live || (state[k] == "live" && writer[k] == i)
				}
				// ported:E61-403
				program.Expect(t, c.b == r[i] || !strings.Contains(page.Body, x.Login) || live, "after %s, %s still shows %s, none of whose comments is left in view", step, c.path, x.Login)
			}
		}
		lives := 0
		for _, s := range state {
			if s == "live" {
				lives++
			}
		}
		// ported:E61-405
		program.ExpectEq(t, [2]int{program.Visible(t, a, anon), program.Visible(t, z, anon)}, [2]int{lives, 1}, "after %s: the visible-comment counts", step)
	}

	press := func(path string) {
		t.Helper()
		owner.GetFollow(t, "/reports/"+a)
		got := owner.PostFollow(t, path, nil)
		// ported:E61-409
		program.Expect(t, got.Status < 400, "the owner's POST %s answered %d", path, got.Status)
	}
	// ported:E61-410
	look("posting")
	// ported:E61-411
	press("/admin/comments/" + ids["Lone"] + "/remove")
	state["Lone"] = "gone"
	// ported:E61-413
	look("removing a top-level comment with no replies")
	// ported:E61-414
	press("/admin/comments/" + ids["Mid"] + "/remove")
	state["Mid"] = "held"
	// ported:E61-416
	look("removing a reply three levels deep that has a reply beneath it")
	// ported:E61-417
	press("/admin/comments/" + ids["Ans"] + "/remove")
	state["Ans"] = "held"
	// ported:E61-419
	look("removing the reply above it")
	// ported:E61-420
	press(fmt.Sprintf("/admin/users/%d/block", acc[2].ID))
	state["Side"], state["Leaf"] = "held", "gone"
	// ported:E61-422
	look("blocking the writer of a top-level comment with a reply and of a reply with none")
	for _, b := range []*program.Visitor{anon, r[0], twin} {
		who := b.Login
		if who == "" {
			who = "a visitor"
		}
		b.GetFollow(t, "/reports/"+a)
		for _, path := range []string{"/admin/comments/" + ids["Top"] + "/remove", fmt.Sprintf("/admin/users/%d/block", acc[1].ID), fmt.Sprintf("/admin/users/%d/unblock", acc[2].ID)} {
			// ported:E61-426
			program.Expect(t, b.Post(t, path, nil).Status < 500, "%s broke for %s", path, who)
		}
		got := b.Get(t, "/admin/blocked")
		// ported:E61-428
		program.Expect(t, got.Status < 500 && !strings.Contains(got.Body, text["Side"]) && !strings.Contains(got.Body, acc[2].Login), "%s was shown the Blocked users page", who)
	}
	// ported:E61-430
	look("the same presses by everyone but the owner")
	text["Still"], writer["Still"], parent["Still"], state["Still"] = program.Mark("Still"), 1, "", "live"
	keys = append(keys, "Still")
	// ported:E61-432
	ids["Still"] = program.Say(t, r[1], a, text["Still"], "", nil)
	// ported:E61-433
	program.Expect(t, ids["Still"] != "", "a reader whom others tried to block can no longer comment")
	// ported:E61-434
	press("/admin/comments/" + ids["End"] + "/remove")
	state["End"], state["Mid"], state["Ans"] = "gone", "gone", "gone"
	// ported:E61-436
	look("removing the last reply beneath two placeholders")
}
