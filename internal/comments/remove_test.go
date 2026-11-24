package comments

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// removeFx is a report "a" with two writers, 7 and 8, and a book whose clock
// can be told apart from the one that stored the comments.
func removeFx(t *testing.T) (f *fx, report int64) {
	t.Helper()
	f = newFx(t)
	f.user(7, "seven")
	f.user(8, "eight")
	return f, f.report("a", "published")
}

// removedAt is the stored removal time of a comment, or -1 when it has none.
func (f *fx) removedAt(id int64) int64 {
	f.t.Helper()
	var at *int64
	if err := f.st.DB().QueryRow(`SELECT removed_at FROM comments WHERE id = ?`, id).Scan(&at); err != nil {
		f.t.Fatalf("read removed_at of %d: %v", id, err)
	}
	if at == nil {
		return -1
	}
	return *at
}

func (f *fx) remove(id int64) {
	f.t.Helper()
	if err := f.book.Remove(ctx, id); err != nil {
		f.t.Fatalf("Remove(%d): %v", id, err)
	}
}

// view is the forest of a report as shape draws it, and the count readers see.
func (f *fx) view(report int64) (string, int) {
	f.t.Helper()
	nodes, err := f.book.Tree(ctx, report)
	if err != nil {
		f.t.Fatalf("Tree: %v", err)
	}
	return shape(nodes), Visible(nodes)
}

// visibleInSQL is the control total: the comments of the report that pass the one
// visibility predicate, counted by the database and not by the forest.
func (f *fx) visibleInSQL(report int64) int {
	f.t.Helper()
	return int(f.count("report_id = ? AND "+fmtVisible("comments"), report))
}

func TestRemove(t *testing.T) {
	t.Run("leaf", func(t *testing.T) {
		f, a := removeFx(t)
		top := f.post(a, 7, 0, "a lone comment")
		other := f.post(a, 8, 0, "a second comment")
		if got, n := f.view(a); got != "1,2" || n != 2 {
			t.Fatalf("before: %q, %d visible", got, n)
		}
		f.remove(top)
		got, n := f.view(a)
		if got != "2" || n != 1 {
			t.Errorf("after removing a leaf: %q with %d visible, want the other comment (%d) alone", got, n, other)
		}
		if at := f.removedAt(top); at != fixedNow().Unix() {
			t.Errorf("removed_at = %d, want the clock's %d", at, fixedNow().Unix())
		}
		if f.count("id = ?", top) != 1 {
			t.Error("the row of a removed comment is gone: removal must never delete it")
		}
	})

	t.Run("held", func(t *testing.T) {
		f, a := removeFx(t)
		c1 := f.post(a, 7, 0, "one")
		c2 := f.post(a, 8, c1, "two")
		c3 := f.post(a, 7, c2, "three: the reply three levels deep")
		f.post(a, 8, c3, "four: a visible reply beneath it")
		if got, n := f.view(a); got != "1(2(3(4)))" || n != 4 {
			t.Fatalf("before: %q, %d", got, n)
		}
		// The reply three levels deep has a visible reply: it stays, as a placeholder.
		f.remove(c3)
		if got, n := f.view(a); got != "1(2(3*(4)))" || n != 3 {
			t.Errorf("after removing the reply with a reply: %q, %d visible, want 1(2(3*(4))) and 3", got, n)
		}
		// And then the reply above it: two placeholders held, the count one lower again.
		f.remove(c2)
		if got, n := f.view(a); got != "1(2*(3*(4)))" || n != 2 {
			t.Errorf("after removing the reply above it: %q, %d visible, want 1(2*(3*(4))) and 2", got, n)
		}
		nodes, _ := f.book.Tree(ctx, a)
		for _, id := range []int64{c2, c3} {
			n := Find(nodes, id)[len(Find(nodes, id))-1]
			if !n.Placeholder || n.Body != "" || n.Login != "" || n.GitHubID != 0 || n.AvatarURL != "" {
				t.Errorf("comment %d is held with %+v: a placeholder carries no writer, avatar or text", id, *n)
			}
		}
	})

	t.Run("chain-collapses", func(t *testing.T) {
		f, a := removeFx(t)
		c1 := f.post(a, 7, 0, "one")
		c2 := f.post(a, 8, c1, "two")
		c3 := f.post(a, 7, c2, "three")
		c4 := f.post(a, 8, c3, "four: the last visible reply")
		f.remove(c2)
		f.remove(c3)
		if got, n := f.view(a); got != "1(2*(3*(4)))" || n != 2 {
			t.Fatalf("a chain of two placeholders over the last reply: %q, %d", got, n)
		}
		// One removal collapses three nodes, and lowers the count by one.
		f.remove(c4)
		if got, n := f.view(a); got != "1" || n != 1 {
			t.Errorf("after removing the last visible reply: %q, %d visible, want 1 and 1", got, n)
		}
		if f.count("report_id = ?", a) != 4 {
			t.Error("a row was deleted by the collapse")
		}
	})

	t.Run("deep-300", func(t *testing.T) {
		f, a := removeFx(t)
		ids := make([]int64, 0, 300)
		var parent int64
		for i := 1; i <= 300; i++ {
			parent = f.post(a, 7+int64(i%2), parent, fmt.Sprintf("level %d", i))
			ids = append(ids, parent)
		}
		if _, n := f.view(a); n != 300 {
			t.Fatalf("seeded %d visible, want 300", n)
		}
		// Below the report page's cap, and then the end of the chain.
		f.remove(ids[6])
		if _, n := f.view(a); n != 299 {
			t.Errorf("after removing depth 7: %d visible, want 299", n)
		}
		f.remove(ids[299])
		if _, n := f.view(a); n != 298 {
			t.Errorf("after removing depth 300: %d visible, want 298", n)
		}
		forest, _ := f.book.Tree(ctx, a)
		if path := Find(forest, ids[299]); path != nil {
			t.Error("the end of the chain is still in the forest")
		}
		if path := Find(forest, ids[6]); path == nil || !path[len(path)-1].Placeholder || len(path) != 7 {
			t.Errorf("depth 7 is not held as a placeholder in its place: %v", path)
		}
	})

	t.Run("already-removed", func(t *testing.T) {
		f, a := removeFx(t)
		c1 := f.post(a, 7, 0, "one")
		f.post(a, 8, c1, "a visible reply")
		f.remove(c1)
		first := f.removedAt(c1)
		_, before := f.view(a)

		// A second removal, by a book whose clock has moved on, is a success that
		// changes nothing: the first time is kept, the count does not drop again.
		later := New(f.st.DB(), func() time.Time { return time.Unix(1_900_000_000, 0) })
		if err := later.Remove(ctx, c1); err != nil {
			t.Fatalf("removing a removed comment: %v, want success", err)
		}
		if at := f.removedAt(c1); at != first {
			t.Errorf("removed_at moved from %d to %d on a repeat", first, at)
		}
		if _, n := f.view(a); n != before {
			t.Errorf("a repeat removal changed the count from %d to %d", before, n)
		}
	})

	t.Run("blocked-writer-commutes", func(t *testing.T) {
		f, a := removeFx(t)
		c1 := f.post(a, 8, 0, "by a writer who is blocked")
		f.post(a, 7, c1, "a visible reply")
		f.exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (8, 5)`)
		if got, n := f.view(a); got != "1*(2)" || n != 1 {
			t.Fatalf("with the writer blocked: %q, %d", got, n)
		}
		// The comment is already hidden by the block. Removing it is a success and
		// the count does not drop a second time.
		f.remove(c1)
		if got, n := f.view(a); got != "1*(2)" || n != 1 {
			t.Errorf("removing a comment a block already hides: %q, %d visible, want no change", got, n)
		}
		if f.removedAt(c1) < 0 {
			t.Error("the removal was not recorded under the block")
		}
		// Lifting the block must not bring a removed comment back.
		f.exec(`DELETE FROM blocked_accounts WHERE github_id = 8`)
		if got, n := f.view(a); got != "1*(2)" || n != 1 {
			t.Errorf("after the block was lifted: %q, %d visible, want the comment still removed", got, n)
		}
		// Control: a comment that was blocked and not removed does come back.
		c3 := f.post(a, 8, 0, "blocked and never removed")
		f.exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (8, 5)`)
		if _, n := f.view(a); n != 1 {
			t.Fatalf("control: the new comment is visible under the block: %d visible", n)
		}
		f.exec(`DELETE FROM blocked_accounts WHERE github_id = 8`)
		if got, n := f.view(a); got != "1*(2),3" || n != 2 {
			t.Errorf("control: unblocking did not restore comment %d: %q, %d", c3, got, n)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		f, a := removeFx(t)
		f.post(a, 7, 0, "one")
		for _, id := range []int64{0, -1, 2, 999} {
			if err := f.book.Remove(ctx, id); !errors.Is(err, ErrNoTarget) {
				t.Errorf("Remove(%d) = %v, want ErrNoTarget", id, err)
			}
		}
		if f.count("removed_at IS NOT NULL") != 0 {
			t.Error("a removal of an unknown id wrote a removal")
		}
	})

	t.Run("report-deleted", func(t *testing.T) {
		f, a := removeFx(t)
		c1 := f.post(a, 7, 0, "one")
		f.exec(`DELETE FROM reports WHERE id = ?`, a)
		if err := f.book.Remove(ctx, c1); !errors.Is(err, ErrNoTarget) {
			t.Errorf("Remove after the report went = %v, want ErrNoTarget", err)
		}
		if _, _, err := f.book.RemoveTarget(ctx, c1); !errors.Is(err, ErrNoTarget) {
			t.Errorf("RemoveTarget after the report went = %v, want ErrNoTarget", err)
		}
	})

	t.Run("closed-store", func(t *testing.T) {
		f, a := removeFx(t)
		c1 := f.post(a, 7, 0, "one")
		f.st.Close()
		err := f.book.Remove(ctx, c1)
		if err == nil || errors.Is(err, ErrNoTarget) {
			t.Errorf("Remove on a closed store = %v, want a wrapped store error and not ErrNoTarget", err)
		}
		_, _, err = f.book.RemoveTarget(ctx, c1)
		if err == nil || errors.Is(err, ErrNoTarget) {
			t.Errorf("RemoveTarget on a closed store = %v, want a wrapped store error and not ErrNoTarget", err)
		}
	})
}

func TestRemoveTarget(t *testing.T) {
	f, a := removeFx(t)
	draft := f.report("drafty", "published")
	top := f.post(a, 7, 0, "on a published report")
	reply := f.post(a, 8, top, "a reply")
	onDraft := f.post(draft, 7, 0, "written while published")
	f.exec(`UPDATE reports SET status = 'draft' WHERE id = ?`, draft)

	for _, c := range []struct {
		name      string
		id        int64
		slug      string
		published bool
	}{
		{"published top level", top, "a", true},
		{"published reply", reply, "a", true},
		{"a report that is a draft or was unpublished", onDraft, "drafty", false},
	} {
		slug, published, err := f.book.RemoveTarget(ctx, c.id)
		if err != nil || slug != c.slug || published != c.published {
			t.Errorf("%s: RemoveTarget = (%q, %v, %v), want (%q, %v, nil)", c.name, slug, published, err, c.slug, c.published)
		}
	}
	// Whatever hides the comment, it can still be looked up and removed.
	f.exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (8, 5)`)
	f.remove(top)
	for _, id := range []int64{top, reply} {
		if slug, _, err := f.book.RemoveTarget(ctx, id); err != nil || slug != "a" {
			t.Errorf("a hidden comment %d: RemoveTarget = (%q, %v)", id, slug, err)
		}
	}
	for _, id := range []int64{0, -3, 99} {
		if _, _, err := f.book.RemoveTarget(ctx, id); !errors.Is(err, ErrNoTarget) {
			t.Errorf("RemoveTarget(%d) = %v, want ErrNoTarget", id, err)
		}
	}
}

func TestVisible(t *testing.T) {
	hidden := func(replies ...*Node) *Node { return &Node{Placeholder: true, Replies: replies} }
	shown := func(replies ...*Node) *Node { return &Node{Login: "w", Body: "b", Replies: replies} }
	for _, c := range []struct {
		name   string
		forest []*Node
		want   int
	}{
		{"nil", nil, 0},
		{"empty", []*Node{}, 0},
		{"one", []*Node{shown()}, 1},
		{"a placeholder is not counted", []*Node{hidden(shown())}, 1},
		{"replies under placeholders are counted", []*Node{hidden(hidden(shown(), shown())), shown(hidden(shown()))}, 4},
		{"at every depth", []*Node{shown(shown(shown(shown(shown()))))}, 5},
	} {
		if got := Visible(c.forest); got != c.want {
			t.Errorf("%s: Visible = %d, want %d", c.name, got, c.want)
		}
	}
	// A chain of 10,000 is safe: no recursion.
	var root *Node
	for i := 0; i < 10000; i++ {
		root = &Node{Placeholder: i%4 == 0, Replies: nilOr(root)}
	}
	if got := Visible([]*Node{root}); got != 7500 {
		t.Errorf("a 10,000-deep chain with a quarter hidden: Visible = %d, want 7500", got)
	}
}

func nilOr(n *Node) []*Node {
	if n == nil {
		return nil
	}
	return []*Node{n}
}

// TestCountReconciles is the control total: after every step of a random
// sequence of removals, repeated removals, blocks and unblocks over random
// forests on two reports, the count readers see equals what the database counts
// under the one visibility predicate, and every comment ever removed is hidden.
func TestCountReconciles(t *testing.T) {
	for seed := int64(1); seed <= 5; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			f := newFx(t)
			for id := int64(1); id <= 4; id++ {
				f.user(id, fmt.Sprintf("w%d", id))
			}
			reports := []int64{f.report("a", "published"), f.report("b", "published")}
			byReport := map[int64][]int64{}
			for i := 0; i < 70; i++ {
				rep := reports[rng.Intn(2)]
				var parent any
				if ids := byReport[rep]; len(ids) > 0 && rng.Intn(4) > 0 {
					parent = ids[rng.Intn(len(ids))]
				}
				res, err := f.st.DB().Exec(`INSERT INTO comments (report_id, parent_id, github_id, body, created_at) VALUES (?, ?, ?, 'x', 1)`,
					rep, parent, 1+rng.Intn(4))
				if err != nil {
					t.Fatal(err)
				}
				id, _ := res.LastInsertId()
				byReport[rep] = append(byReport[rep], id)
			}
			removed := map[int64]bool{}
			check := func(step string) {
				t.Helper()
				for _, rep := range reports {
					nodes, err := f.book.Tree(ctx, rep)
					if err != nil {
						t.Fatalf("%s: Tree: %v", step, err)
					}
					if got, want := Visible(nodes), f.visibleInSQL(rep); got != want {
						t.Fatalf("%s: report %d: the count readers see is %d, the database counts %d", step, rep, got, want)
					}
					for id := range removed {
						if path := Find(nodes, id); path != nil && !path[len(path)-1].Placeholder {
							t.Fatalf("%s: removed comment %d is shown", step, id)
						}
					}
				}
			}
			check("start")
			all := append(append([]int64(nil), byReport[reports[0]]...), byReport[reports[1]]...)
			for step := 0; step < 120; step++ {
				switch rng.Intn(4) {
				case 0, 1:
					id := all[rng.Intn(len(all))]
					before := f.removedAt(id)
					f.remove(id)
					if before >= 0 && f.removedAt(id) != before {
						t.Fatalf("step %d: a repeat removal of %d moved removed_at", step, id)
					}
					removed[id] = true
				case 2:
					f.exec(`INSERT OR IGNORE INTO blocked_accounts (github_id, blocked_at) VALUES (?, 5)`, 1+rng.Intn(4))
				default:
					f.exec(`DELETE FROM blocked_accounts WHERE github_id = ?`, 1+rng.Intn(4))
				}
				check(fmt.Sprintf("step %d", step))
			}
		})
	}
}
