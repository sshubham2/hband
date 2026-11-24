package comments

import (
	"context"
	"fmt"
	"time"
)

// Node is one comment as a reader may see it, with the replies drawn under it.
type Node struct {
	ID int64
	// GitHubID is the writer's account id, or 0 for a placeholder. A caller
	// decides what the id means (the Author badge); this package knows no owner.
	GitHubID  int64
	Login     string
	AvatarURL string
	Body      string
	// Posted is when the comment was stored, in UTC.
	Posted time.Time
	// Placeholder is true for a hidden comment kept only because something
	// visible lies below it. It has no writer, avatar, text or id of a writer:
	// those never left the database.
	Placeholder bool
	// Replies are the comments that answer this one, oldest first.
	Replies []*Node
}

// row is one comment as the query returns it, hidden text already blanked.
type row struct {
	id        int64
	parent    int64 // 0: top level
	created   int64
	shown     bool
	githubID  int64
	login     string
	avatarURL string
	body      string
}

// commentRows is the row set every read of a report's comments starts from: a
// comment and its writer's account. Tree's statement and VisibleCounts' both
// take it from here, so the two cannot come to read different rows (a change to
// the join changes both, or breaks both at once).
const commentRows = `FROM comments c JOIN users u ON u.github_id = c.github_id`

// treeSQL reads one report's whole thread, oldest first. The text of a hidden
// comment is replaced inside the statement, by the same predicate Post uses, so
// a removed comment's text and a blocked writer's login and avatar are never
// scanned into Go.
var treeSQL = `
SELECT id, parent_id, created_at, shown,
       CASE WHEN shown THEN github_id ELSE 0 END,
       CASE WHEN shown THEN login ELSE '' END,
       CASE WHEN shown THEN avatar_url ELSE '' END,
       CASE WHEN shown THEN body ELSE '' END
FROM (
    SELECT c.id AS id, COALESCE(c.parent_id, 0) AS parent_id, c.created_at AS created_at,
           ` + fmtVisible("c") + ` AS shown,
           c.github_id AS github_id, u.login AS login, u.avatar_url AS avatar_url, c.body AS body
    ` + commentRows + `
    WHERE c.report_id = ?
)
ORDER BY id`

// Tree returns the comments of the report as a forest: the top-level comments
// oldest first, each with its replies, oldest first at every level. Hidden
// comments are pruned as described in the package comment. It is one read, and
// an error is returned for a query or scan failure or for rows that do not form
// a tree; it is never answered with an empty forest instead.
func (b *Book) Tree(ctx context.Context, reportID int64) ([]*Node, error) {
	rows, err := b.db.QueryContext(ctx, treeSQL, reportID)
	if err != nil {
		return nil, fmt.Errorf("comments: read: %w", err)
	}
	defer rows.Close()
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.parent, &r.created, &r.shown, &r.githubID, &r.login, &r.avatarURL, &r.body); err != nil {
			return nil, fmt.Errorf("comments: read: %w", err)
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("comments: read: %w", err)
	}
	nodes, err := arrange(all)
	if err != nil {
		return nil, fmt.Errorf("comments: read: %w", err)
	}
	return nodes, nil
}

// visibleCountsSQL counts, per report, the comments a reader sees. It starts from
// the same rows as treeSQL (commentRows) and filters with the same predicate
// (fmtVisible), so for every report its count is Visible(Tree(report)).
var visibleCountsSQL = `
SELECT c.report_id, COUNT(*)
` + commentRows + `
WHERE ` + fmtVisible("c") + `
GROUP BY c.report_id`

// VisibleCounts is the public comment count of every report that has one, in one
// read: for each report it equals Visible(Tree(report)), the whole-thread count
// the report page prints. A report with no visible comment is absent from the
// map, which reads as 0. It reads counts and never text. An error is returned for
// a query or scan failure and never a partial map.
func (b *Book) VisibleCounts(ctx context.Context) (map[int64]int, error) {
	rows, err := b.db.QueryContext(ctx, visibleCountsSQL)
	if err != nil {
		return nil, fmt.Errorf("comments: count: %w", err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("comments: count: %w", err)
		}
		out[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("comments: count: %w", err)
	}
	return out, nil
}

// arrange prunes and links rows, which must be in ascending id order, into the
// forest Tree returns. It is pure and uses no recursion, so a thread of any
// depth is safe.
//
// Every parent has a smaller id than its replies (ids only ever grow), so
// walking the rows from the last to the first meets every reply before the
// comment it answers: a post-order. That pass decides which rows are kept: a
// row is kept when it is shown or when a row kept below it names it as parent.
// A second pass, first to last, links each kept row into its parent's replies
// or into the top level, so every level comes out oldest first. A row whose
// parent is missing, or is not smaller than its own id, is an error and never
// a wrong picture.
func arrange(rows []row) ([]*Node, error) {
	index := make(map[int64]int, len(rows))
	for i, r := range rows {
		if i > 0 && r.id <= rows[i-1].id {
			return nil, fmt.Errorf("comment %d is out of order", r.id)
		}
		index[r.id] = i
	}
	parent := make([]int, len(rows)) // index of the parent row, or -1
	for i, r := range rows {
		parent[i] = -1
		if r.parent == 0 {
			continue
		}
		p, ok := index[r.parent]
		if !ok || r.parent >= r.id {
			return nil, fmt.Errorf("comment %d answers comment %d, which is not an earlier comment of the report", r.id, r.parent)
		}
		parent[i] = p
	}

	keep := make([]bool, len(rows))
	keptBelow := make([]int, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		keep[i] = rows[i].shown || keptBelow[i] > 0
		if keep[i] && parent[i] >= 0 {
			keptBelow[parent[i]]++
		}
	}

	nodes := make([]*Node, len(rows))
	var roots []*Node
	for i, r := range rows {
		if !keep[i] {
			continue
		}
		n := &Node{
			ID: r.id, GitHubID: r.githubID, Login: r.login, AvatarURL: r.avatarURL, Body: r.body,
			Posted: time.Unix(r.created, 0).UTC(), Placeholder: !r.shown,
		}
		nodes[i] = n
		if parent[i] < 0 {
			roots = append(roots, n)
		} else {
			// A kept row's parent is kept, and comes earlier, so it exists.
			p := nodes[parent[i]]
			p.Replies = append(p.Replies, n)
		}
	}
	return roots, nil
}

// Visible is the public comment count of a forest Tree returned: the comments a
// reader sees, at every depth. A placeholder is not counted, and the visible
// replies under it are, so removing one visible comment lowers the count by
// exactly one whether it leaves a placeholder or nothing. arrange keeps every
// shown row and makes a placeholder of exactly the rows that are hidden, so this
// equals the number of the report's comments that pass visibleSQL. It is pure
// and uses no recursion, so a thread of any depth is safe.
func Visible(forest []*Node) int {
	n := 0
	stack := append([]*Node(nil), forest...)
	for len(stack) > 0 {
		at := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if !at.Placeholder {
			n++
		}
		stack = append(stack, at.Replies...)
	}
	return n
}

// Find returns the path from a top-level comment down to the comment with the
// given id in a forest Tree returned, the comment itself last, or nil when the
// forest holds no such comment (it was never there, belongs to another report,
// or was pruned). It is pure and uses no recursion.
func Find(forest []*Node, id int64) []*Node {
	up := make(map[*Node]*Node)
	stack := append([]*Node(nil), forest...)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n.ID == id {
			var path []*Node
			for at := n; at != nil; at = up[at] {
				path = append(path, at)
			}
			for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
				path[i], path[j] = path[j], path[i]
			}
			return path
		}
		for _, r := range n.Replies {
			up[r] = n
			stack = append(stack, r)
		}
	}
	return nil
}
