package comments

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// rowsOf reads rows written as "id:parent:s" or "id:parent:h" (s: shown, h:
// hidden), parent 0 meaning top level.
func rowsOf(t *testing.T, spec string) []row {
	t.Helper()
	var out []row
	for _, f := range strings.Fields(spec) {
		p := strings.Split(f, ":")
		if len(p) != 3 {
			t.Fatalf("bad row %q", f)
		}
		id, _ := strconv.ParseInt(p[0], 10, 64)
		parent, _ := strconv.ParseInt(p[1], 10, 64)
		r := row{id: id, parent: parent, shown: p[2] == "s"}
		if r.shown {
			r.githubID, r.login, r.body = 7, "writer", "text "+p[0]
		}
		out = append(out, r)
	}
	return out
}

// shape draws a forest as text: a node is its id, a placeholder carries a *,
// its replies follow in parentheses, siblings are joined with commas.
func shape(nodes []*Node) string {
	var parts []string
	for _, n := range nodes {
		s := strconv.FormatInt(n.ID, 10)
		if n.Placeholder {
			s += "*"
		}
		if len(n.Replies) > 0 {
			s += "(" + shape(n.Replies) + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ",")
}

func TestArrange(t *testing.T) {
	for _, c := range []struct{ name, rows, want string }{
		{"nothing", "", ""},
		{"one comment", "1:0:s", "1"},
		{"a removed leaf is dropped", "1:0:s 2:1:h", "1"},
		{"a removed top-level leaf is dropped", "1:0:h", ""},
		{"a removed comment with a visible reply three levels down", "1:0:h 2:1:h 3:2:h 4:3:s", "1*(2*(3*(4)))"},
		{"removed over removed over visible", "1:0:h 2:1:h 3:2:s", "1*(2*(3))"},
		{"an entirely hidden subtree takes everything under it", "1:0:h 2:1:h 3:2:h", ""},
		{"a hidden subtree beside a visible reply", "1:0:s 2:1:h 3:2:h 4:1:s", "1(4)"},
		{"a removed chain five deep over one visible leaf at depth six", "1:0:h 2:1:h 3:2:h 4:3:h 5:4:h 6:5:s", "1*(2*(3*(4*(5*(6)))))"},
		{"a visible comment with a removed parent keeps its place", "1:0:h 2:1:s 3:1:h", "1*(2)"},
		{"every level is oldest first", "1:0:s 2:1:s 3:0:s 4:1:s 5:0:s 6:3:s", "1(2,4),3(6),5"},
		{"replies interleaved in time stay with their parent", "1:0:s 2:0:s 3:1:s 4:2:s 5:1:s 6:0:s", "1(3,5),2(4),6"},
		{"two hidden roots, one with a visible grandchild", "1:0:h 2:0:s 3:1:h 4:3:s 5:0:h", "1*(3*(4)),2"},
		{"a visible reply under a visible comment under a hidden root", "1:0:h 2:1:s 3:2:s", "1*(2(3))"},
	} {
		got, err := arrange(rowsOf(t, c.rows))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if s := shape(got); s != c.want {
			t.Errorf("%s: shape %q, want %q", c.name, s, c.want)
		}
	}

	// A placeholder keeps its id and nothing else.
	got, _ := arrange(rowsOf(t, "1:0:h 2:1:s"))
	if n := got[0]; !n.Placeholder || n.ID != 1 || n.GitHubID != 0 || n.Login != "" || n.AvatarURL != "" || n.Body != "" {
		t.Errorf("placeholder = %+v", *n)
	}
	if n := got[0].Replies[0]; n.Placeholder || n.Body != "text 2" || n.GitHubID != 7 {
		t.Errorf("the visible reply = %+v", *n)
	}
}

func TestArrangeRefusesRowsThatAreNotATree(t *testing.T) {
	for _, c := range []struct{ name, rows string }{
		{"a parent that is missing", "1:0:s 2:9:s"},
		{"a parent that is not smaller", "1:0:s 3:5:s 5:0:s"},
		{"a comment that is its own parent", "1:1:s"},
		{"ids out of order", "2:0:s 1:0:s"},
		{"a repeated id", "1:0:s 1:0:s"},
	} {
		if got, err := arrange(rowsOf(t, c.rows)); err == nil {
			t.Errorf("%s: drew %q instead of failing", c.name, shape(got))
		}
	}
}

// chainRows is n rows, each a reply to the one before; shownFrom says which of
// them are shown.
func chainRows(n int, shown func(i int) bool) []row {
	rows := make([]row, n)
	for i := range rows {
		rows[i] = row{id: int64(i + 1), parent: int64(i), shown: shown(i + 1)}
	}
	return rows
}

// depthOf walks a forest down its only branch without recursion.
func depthOf(roots []*Node) (depth int, last *Node) {
	for n := roots; len(n) > 0; n = n[0].Replies {
		depth++
		last = n[0]
	}
	return depth, last
}

func TestArrangeDeepChain(t *testing.T) {
	const n = 10000
	// All shown: one branch, n deep.
	roots, err := arrange(chainRows(n, func(int) bool { return true }))
	if err != nil {
		t.Fatal(err)
	}
	if d, last := depthOf(roots); d != n || last.ID != n {
		t.Errorf("all shown: depth %d, last id %v; want %d, %d", d, last, n, n)
	}
	// Only the deepest shown: every ancestor is a placeholder, all of them kept.
	roots, err = arrange(chainRows(n, func(i int) bool { return i == n }))
	if err != nil {
		t.Fatal(err)
	}
	d, last := depthOf(roots)
	if d != n || last.Placeholder {
		t.Fatalf("only the leaf shown: depth %d, leaf placeholder %v; want %d, false", d, last.Placeholder, n)
	}
	placeholders := 0
	for r := roots; len(r) > 0; r = r[0].Replies {
		if r[0].Placeholder {
			placeholders++
		}
	}
	if placeholders != n-1 {
		t.Errorf("%d placeholders, want %d", placeholders, n-1)
	}
	// None shown: nothing is kept.
	roots, err = arrange(chainRows(n, func(int) bool { return false }))
	if err != nil || len(roots) != 0 {
		t.Errorf("none shown: %d roots, %v; want none", len(roots), err)
	}
	// The deepest comment is found, with its whole path, without recursion.
	roots, _ = arrange(chainRows(n, func(int) bool { return true }))
	if p := Find(roots, n); len(p) != n || p[0].ID != 1 || p[n-1].ID != n {
		t.Errorf("Find at depth %d returned a path of %d", n, len(p))
	}
}

func TestFind(t *testing.T) {
	forest, err := arrange(rowsOf(t, "1:0:s 2:0:s 3:1:s 4:3:s 5:2:h 6:0:h 7:6:h"))
	if err != nil {
		t.Fatal(err)
	}
	ids := func(p []*Node) string {
		var s []string
		for _, n := range p {
			s = append(s, strconv.FormatInt(n.ID, 10))
		}
		return strings.Join(s, ">")
	}
	for id, want := range map[int64]string{1: "1", 2: "2", 3: "1>3", 4: "1>3>4"} {
		if got := ids(Find(forest, id)); got != want {
			t.Errorf("Find(%d) = %q, want %q", id, got, want)
		}
	}
	for _, id := range []int64{0, 5, 6, 7, 99, -1} {
		if p := Find(forest, id); p != nil {
			t.Errorf("Find(%d) = %s, want nil (pruned, absent or never there)", id, ids(p))
		}
	}
	if Find(nil, 1) != nil {
		t.Error("Find in an empty forest returned a path")
	}
}

// walk visits every node of a forest.
func walk(nodes []*Node, f func(*Node)) {
	for _, n := range nodes {
		f(n)
		walk(n.Replies, f)
	}
}

// TestTreeRedactsHidden reads through the database: the text, login and avatar
// of a hidden comment never reach Go, and unblocking an account restores its
// comments without a write to any of them.
func TestTreeRedactsHidden(t *testing.T) {
	f := newFx(t)
	f.user(1, "writer-one")
	f.user(2, "blocked-writer")
	f.user(3, "reader")
	rep, other := f.report("pub", "published"), f.report("other", "published")

	c1 := f.post(rep, 1, 0, "SECRET-REMOVED-TEXT")
	c2 := f.post(rep, 3, c1, "visible reply to the removed one")
	c3 := f.post(rep, 2, 0, "SECRET-BLOCKED-TEXT")
	c4 := f.post(rep, 3, c3, "visible reply to the blocked one")
	c5 := f.post(rep, 3, 0, "plain")
	f.post(other, 3, 0, "on another report")
	f.exec(`UPDATE comments SET removed_at = 9 WHERE id = ?`, c1)
	f.exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (2, 9)`)
	rowsBefore := f.count("1 = 1")

	forest, err := f.book.Tree(ctx, rep)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := shape(forest), fmt.Sprintf("%d*(%d),%d*(%d),%d", c1, c2, c3, c4, c5); got != want {
		t.Fatalf("shape %q, want %q", got, want)
	}
	walk(forest, func(n *Node) {
		all := n.Login + n.AvatarURL + n.Body
		if strings.Contains(all, "SECRET") || strings.Contains(all, "writer-one") || strings.Contains(all, "blocked-writer") {
			t.Errorf("node %d carries hidden text: %+v", n.ID, *n)
		}
		if n.Placeholder && (n.GitHubID != 0 || all != "") {
			t.Errorf("placeholder %d is not blank: %+v", n.ID, *n)
		}
		if !n.Placeholder && (n.GitHubID != 3 || n.Login != "reader" || n.AvatarURL == "" || n.Body == "") {
			t.Errorf("visible node %d is not complete: %+v", n.ID, *n)
		}
		if n.Posted.Location().String() != "UTC" || n.Posted.Unix() != 1_800_000_000 {
			t.Errorf("node %d posted %v, want the stored time in UTC", n.ID, n.Posted)
		}
	})

	// Unblocking restores the blocked account's comment, with no write to a comment.
	f.exec(`DELETE FROM blocked_accounts WHERE github_id = 2`)
	forest, err = f.book.Tree(ctx, rep)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := shape(forest), fmt.Sprintf("%d*(%d),%d(%d),%d", c1, c2, c3, c4, c5); got != want {
		t.Errorf("after unblocking: shape %q, want %q", got, want)
	}
	if n := f.count("1 = 1"); n != rowsBefore {
		t.Errorf("%d comment rows, want %d", n, rowsBefore)
	}
	if n := f.count("removed_at IS NOT NULL"); n != 1 {
		t.Errorf("%d comments carry removed_at, want only the one that was removed", n)
	}

	// A report with no comments is an empty forest, and an error is never one.
	if forest, err := f.book.Tree(ctx, f.report("empty", "published")); err != nil || len(forest) != 0 {
		t.Errorf("an empty report: %d roots, %v", len(forest), err)
	}
	dead := newFx(t)
	dead.st.Close()
	if forest, err := dead.book.Tree(ctx, 1); err == nil {
		t.Errorf("a closed store was read as %d roots with no error", len(forest))
	}
}
