package server

import (
	"time"

	"github.com/sshubham2/hband/internal/auth"
	"github.com/sshubham2/hband/internal/comments"
)

// threadDepthCap is how many levels of a thread one page draws; top-level
// comments are level 1. It is the only copy of the number. It is a readability
// limit before it is anything else: each level indents by about 1.3rem
// (site.css), so five levels leave a phone a usable body width
// (TestThreadFitsPhoneWidth holds the two together), and browsers stop building
// nested elements at a fixed depth (Chrome 158 flattened the nesting at the
// 256th reply of a chain, measured), so a page must never nest without limit.
// Writes accept any depth: a comment below the cap is stored, and drawn on the
// branch page that continues the thread (/reports/<slug>/comments/<id>), whose
// root is drawn at level 1 again.
const threadDepthCap = 5

// branchRoot below would never end for a cap under 2.
const _ uint = threadDepthCap - 2

// commentView is one comment as the template draws it.
type commentView struct {
	ID        int64
	Login     string
	AvatarURL string
	Body      string
	When      time.Time
	// Author marks a comment written by the pinned owner's GitHub account.
	Author bool
	// Placeholder is a removed or blocked comment kept for the replies below it:
	// the template draws a notice and nothing of the comment.
	Placeholder bool
	// CanReply is true for a signed-in viewer on a comment that is shown. It has
	// no depth term, so a reply is never a dead end.
	CanReply bool
	Slug     string
	Replies  []commentView
	// Cut is true when the comment has replies that this page does not draw
	// because it is at the depth cap.
	Cut bool
}

// buildThread turns the pruned forest into what the template draws. Roots are
// at depth 1 and a comment at depth limit has its replies left out, with Cut
// set. The Author badge is decided here, once: the account that wrote the
// comment is the pinned owner's (auth.IsOwner on the stored GitHub id), never
// the viewer, and never anything read from the login. A placeholder carries
// GitHubID 0, and is excluded as well, so it cannot carry the badge.
func buildThread(nodes []*comments.Node, slug string, signedIn bool, ownerID int64, limit int) []commentView {
	return buildLevel(nodes, slug, signedIn, ownerID, limit, 1)
}

// branchRoot says which page draws a comment. path is the comment's path from a
// top-level comment down to itself (comments.Find), so its length is its depth
// in the report. A comment at or above the cap is drawn on the report page and
// branchRoot is nil. A deeper one is drawn on a branch page, whose root is a
// comment that an earlier page cut: the report page cuts at depth cap, the
// branch page rooted there cuts at depth cap+(cap-1), and so on. The page chosen
// is the first whose cut comes at or below the comment, so the comment is drawn
// at level 2 or deeper of it (or is the cut comment of that page, with its own
// "continue this thread" link). It is the one place that repeats buildLevel's
// cut rule, and TestReplyRedirectTable checks it against what the pages really
// draw.
func branchRoot(path []*comments.Node) *comments.Node {
	depth := len(path)
	if depth <= threadDepthCap {
		return nil
	}
	root := threadDepthCap
	for depth > root+threadDepthCap-1 {
		root += threadDepthCap - 1
	}
	return path[root-1]
}

// buildLevel draws one level; it recurses at most limit levels deep, whatever
// the depth of the thread.
func buildLevel(nodes []*comments.Node, slug string, signedIn bool, ownerID int64, limit, depth int) []commentView {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]commentView, 0, len(nodes))
	for _, n := range nodes {
		v := commentView{
			ID:          n.ID,
			Login:       n.Login,
			AvatarURL:   n.AvatarURL,
			Body:        n.Body,
			When:        n.Posted,
			Author:      !n.Placeholder && auth.IsOwner(ownerID, n.GitHubID),
			Placeholder: n.Placeholder,
			CanReply:    signedIn && !n.Placeholder,
			Slug:        slug,
		}
		if depth >= limit {
			v.Cut = len(n.Replies) > 0
		} else {
			v.Replies = buildLevel(n.Replies, slug, signedIn, ownerID, limit, depth+1)
		}
		out = append(out, v)
	}
	return out
}
