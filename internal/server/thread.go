package server

import (
	"time"

	"github.com/sshubham2/hband/internal/auth"
	"github.com/sshubham2/hband/internal/comments"
)

// threadDepthCap is how many levels of a thread one page draws; top-level
// comments are level 1. It is the only copy of the number. Browsers stop
// building nested elements at a fixed depth (Chrome 158 flattened the nesting
// at the 256th reply of a chain, measured), so a page must never nest without
// limit. Writes accept any depth: replies below the cap are stored and are
// beyond what this page draws.
const threadDepthCap = 100

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
