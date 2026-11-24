package comments

import (
	"strconv"
	"strings"
	"testing"
)

// agree fails unless VisibleCounts says, for every report, what Visible says of
// the forest Tree returns for it (and nothing for a report with no visible
// comment), and returns the counts.
func agree(t *testing.T, f *fx, reports map[string]int64) map[int64]int {
	t.Helper()
	got, err := f.book.VisibleCounts(ctx)
	if err != nil {
		t.Fatalf("VisibleCounts: %v", err)
	}
	for name, id := range reports {
		nodes, err := f.book.Tree(ctx, id)
		if err != nil {
			t.Fatalf("Tree(%s): %v", name, err)
		}
		if want := Visible(nodes); got[id] != want {
			t.Errorf("report %s: VisibleCounts = %d, Visible(Tree) = %d", name, got[id], want)
		}
	}
	return got
}

// TestVisibleCounts feeds the spike's four fixtures through the one-statement
// count and the forest count: a removed root with a visible reply and a blocked
// writer at depth with a visible child (A), a chain deeper than any page draws
// (B), no comments (C) and every comment by a blocked writer (D). The two must
// agree on each, before and after the block is lifted.
func TestVisibleCounts(t *testing.T) {
	f := newFx(t)
	for id, login := range map[int64]string{7: "seven", 8: "eight", 9: "nine"} {
		f.user(id, login)
	}
	a, b, c, d := f.report("a", "published"), f.report("b", "published"), f.report("c", "published"), f.report("d", "published")
	draft := f.report("draft", "draft")
	reports := map[string]int64{"A": a, "B": b, "C": c, "D": d, "draft": draft}

	// A
	removedRoot := f.post(a, 7, 0, "a root that is removed")
	f.post(a, 8, removedRoot, "a visible reply under it")
	c1 := f.post(a, 7, 0, "c1")
	c2 := f.post(a, 9, c1, "c2, by the writer who is blocked below")
	f.post(a, 7, c2, "c3, a visible child of a blocked comment")
	f.post(a, 9, 0, "a blocked writer's leaf")
	removedLeaf := f.post(a, 7, c1, "a removed leaf")
	f.exec(`UPDATE comments SET removed_at = 5 WHERE id IN (?, ?)`, removedRoot, removedLeaf)
	// B: 110 deep, deeper than the cap of any page.
	var parent int64
	for i := 0; i < 110; i++ {
		parent = f.post(b, 7, parent, "level "+strconv.Itoa(i))
	}
	// D: every comment by the blocked writer.
	d1 := f.post(d, 9, 0, "d1")
	d2 := f.post(d, 9, d1, "d2")
	f.post(d, 9, d2, "d3")
	// A draft with visible comments is counted too: the show-all list is what
	// leaves the draft out, not this count.
	// (Post refuses a draft, so this one is written the way a comment made before the
	// report was unpublished would be.)
	f.exec(`INSERT INTO comments (report_id, parent_id, github_id, body, created_at) VALUES (?, NULL, 7, 'a comment on a draft', 1)`, draft)

	f.exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (9, 5)`)
	got := agree(t, f, reports)
	for name, want := range map[string]int{"A": 3, "B": 110, "C": 0, "D": 0, "draft": 1} {
		if got[reports[name]] != want {
			t.Errorf("fixture %s while 9 is blocked: %d, want %d", name, got[reports[name]], want)
		}
	}
	if _, present := got[c]; present {
		t.Error("a report with no comment is in the map: absence is how a zero is told")
	}
	if _, present := got[d]; present {
		t.Error("a report whose every comment is hidden is in the map")
	}

	// Lifting the block restores the comments, with nothing written to them, and the
	// two counts move together.
	f.exec(`DELETE FROM blocked_accounts WHERE github_id = 9`)
	got = agree(t, f, reports)
	for name, want := range map[string]int{"A": 5, "B": 110, "C": 0, "D": 3, "draft": 1} {
		if got[reports[name]] != want {
			t.Errorf("fixture %s after the block is lifted: %d, want %d", name, got[reports[name]], want)
		}
	}

	t.Run("an error is never a partial map", func(t *testing.T) {
		f.exec(`ALTER TABLE comments RENAME TO comments_gone`)
		m, err := f.book.VisibleCounts(ctx)
		if err == nil || m != nil || !strings.HasPrefix(err.Error(), "comments: count:") {
			t.Fatalf("VisibleCounts on a missing table = %v, %v; want a nil map and a 'comments: count:' error", m, err)
		}
	})
}

// The count and the thread read the same rows by construction: both statements
// are built from the one commentRows fragment and the one visibility predicate,
// and neither spells the join again.
func TestVisibleCountsReadTheRowsTreeReads(t *testing.T) {
	for name, sql := range map[string]string{"treeSQL": treeSQL, "visibleCountsSQL": visibleCountsSQL} {
		if !strings.Contains(sql, commentRows) {
			t.Errorf("%s does not take its FROM and JOIN from commentRows", name)
		}
		if n := strings.Count(sql, "FROM comments"); n != 1 {
			t.Errorf("%s names FROM comments %d times, want only the one inside commentRows", name, n)
		}
		if n := strings.Count(sql, "JOIN users"); n != 1 {
			t.Errorf("%s joins users %d times, want only the one inside commentRows", name, n)
		}
		if !strings.Contains(sql, fmtVisible("c")) {
			t.Errorf("%s does not filter with fmtVisible", name)
		}
	}
}
