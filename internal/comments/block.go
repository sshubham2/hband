package comments

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrOwnerBlock is returned by Block for the pinned owner's own account. Blocking
// the author would hide every Author comment on the site and refuse the author's
// own writes, so the refusal is the store's: no caller can forget it.
var ErrOwnerBlock = errors.New("comments: the author cannot be blocked")

// blockSQL records a block. A repeat changes nothing and keeps the first
// blocked_at, so it needs no look at the rows it touched.
const blockSQL = `
INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (?, ?)
ON CONFLICT (github_id) DO NOTHING`

// Block blocks the account: from now on its comments are hidden from every reader
// and Post, and the votes and feedback writes, refuse it. It is one statement and
// idempotent. It writes nothing to the account's comments or votes, and nothing it
// hid is lost: Unblock brings it all back.
//
// It refuses the pinned owner's own account with ErrOwnerBlock, before any SQL. A
// book built with no owner (ownerID not positive) cannot tell the author from any
// other account, so it refuses everyone with an error and never reads that as
// "nobody is the owner".
func (b *Book) Block(ctx context.Context, githubID int64) error {
	if b.ownerID <= 0 {
		return errors.New("comments: block: no owner is pinned, so the author cannot be told from another account")
	}
	if githubID == b.ownerID {
		return ErrOwnerBlock
	}
	if githubID <= 0 {
		return fmt.Errorf("comments: block: %d is not an account id", githubID)
	}
	if _, err := b.db.ExecContext(ctx, blockSQL, githubID, b.now().Unix()); err != nil {
		return fmt.Errorf("comments: block: %w", err)
	}
	return nil
}

// Unblock lifts the block: the account's comments are visible again, and it may
// take part again. It is one DELETE and idempotent. A comment that was removed
// while its writer was blocked stays removed: removal is its own fact
// (removed_at), which this never touches.
func (b *Book) Unblock(ctx context.Context, githubID int64) error {
	if _, err := b.db.ExecContext(ctx, `DELETE FROM blocked_accounts WHERE github_id = ?`, githubID); err != nil {
		return fmt.Errorf("comments: unblock: %w", err)
	}
	return nil
}

// blockedRowSQL is the one read that picks Post's answer, and (as its own copy in
// each package) the answer of votes and feedback, after a write was refused.
const blockedRowSQL = `SELECT EXISTS (SELECT 1 FROM blocked_accounts WHERE github_id = ?)`

// blockedRow is what New sets Book.isBlocked to.
func (b *Book) blockedRow(ctx context.Context, githubID int64) (bool, error) {
	var blocked bool
	if err := b.db.QueryRowContext(ctx, blockedRowSQL, githubID).Scan(&blocked); err != nil {
		return false, err
	}
	return blocked, nil
}

// BlockedComment is one comment of a blocked account, for the author's Blocked
// users page.
type BlockedComment struct {
	ID int64
	// ParentID is the comment it answers, or 0 for a top-level comment.
	ParentID int64
	// Body is the text, or empty when Removed: a removed comment's text is blanked
	// inside the query and is shown to nobody, the author included.
	Body string
	// Posted is when the comment was stored, in UTC.
	Posted  time.Time
	Removed bool
	// ReportID, Slug and Title name the report it is on, and Published says whether
	// that report has a reader page.
	ReportID  int64
	Slug      string
	Title     string
	Published bool
}

// BlockedAccount is one blocked account with everything it ever posted.
type BlockedAccount struct {
	GitHubID int64
	// Login and AvatarURL are the account's current ones from the users row, and
	// empty when the account never signed in here: a block is a fact about an id,
	// and the id need not have a users row.
	Login     string
	AvatarURL string
	// BlockedAt is when the block was made, in UTC.
	BlockedAt time.Time
	// Comments are every comment the account wrote, oldest first, removed ones and
	// ones on draft or unpublished reports included.
	Comments []BlockedComment
}

// blockedSQL reads every blocked account with every comment it wrote, in one
// pass, oldest block first and each account's comments oldest first. The account
// is kept when it has no comment, and when it has no users row.
const blockedSQL = `
SELECT b.github_id, COALESCE(u.login, ''), COALESCE(u.avatar_url, ''), b.blocked_at,
       COALESCE(c.id, 0), COALESCE(c.parent_id, 0),
       CASE WHEN c.removed_at IS NULL THEN COALESCE(c.body, '') ELSE '' END,
       COALESCE(c.created_at, 0), c.removed_at IS NOT NULL,
       COALESCE(r.id, 0), COALESCE(r.slug, ''), COALESCE(r.title, ''), COALESCE(r.status = 'published', 0)
FROM blocked_accounts b
LEFT JOIN users u ON u.github_id = b.github_id
LEFT JOIN comments c ON c.github_id = b.github_id
LEFT JOIN reports r ON r.id = c.report_id
ORDER BY b.blocked_at, b.github_id, c.id`

// Blocked returns the blocked accounts, oldest block first, each with its
// comments. A removed comment comes back with no text. It is one read and
// applies no limit; it is for the owner's Blocked users page alone. An error is
// returned for a query or scan failure, never an empty list standing in for it.
func (b *Book) Blocked(ctx context.Context) ([]BlockedAccount, error) {
	rows, err := b.db.QueryContext(ctx, blockedSQL)
	if err != nil {
		return nil, fmt.Errorf("comments: blocked accounts: %w", err)
	}
	defer rows.Close()
	var out []BlockedAccount
	for rows.Next() {
		var (
			a                 BlockedAccount
			c                 BlockedComment
			blockedAt, posted int64
		)
		if err := rows.Scan(&a.GitHubID, &a.Login, &a.AvatarURL, &blockedAt,
			&c.ID, &c.ParentID, &c.Body, &posted, &c.Removed,
			&c.ReportID, &c.Slug, &c.Title, &c.Published); err != nil {
			return nil, fmt.Errorf("comments: blocked accounts: %w", err)
		}
		if len(out) == 0 || out[len(out)-1].GitHubID != a.GitHubID {
			a.BlockedAt = time.Unix(blockedAt, 0).UTC()
			out = append(out, a)
		}
		if c.ID != 0 {
			c.Posted = time.Unix(posted, 0).UTC()
			cur := &out[len(out)-1]
			cur.Comments = append(cur.Comments, c)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("comments: blocked accounts: %w", err)
	}
	return out, nil
}
