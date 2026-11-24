// Package votes keeps one thumbs up or thumbs down per reader per report.
//
// It is the only code that reads or writes the votes table. Every write is one
// SQL statement and this package never opens a transaction: a transaction that
// reads first and writes second is upgraded from a read snapshot, and under WAL
// SQLite refuses that upgrade at once, without waiting on busy_timeout, whenever
// another connection has written in between. Single statements wait their turn
// instead, so concurrent voters and sign-ins never see "database is locked".
//
// Two views come out of it, and they are different types on purpose. PublicView
// is all that reader pages ever receive and has no slot for a dislike count, so
// no reader template can show one. The dislike count is produced only by
// OwnerTally, which only the owner's admin page may call.
package votes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Kind is the direction of a vote.
type Kind string

const (
	Up   Kind = "up"
	Down Kind = "down"
)

// ErrNotOpen is returned by Cast when no published report has the id: it never
// existed, or it was unpublished or deleted after the caller looked it up.
var ErrNotOpen = errors.New("votes: no published report with that id")

// Book is the votes of one database.
type Book struct {
	db  *sql.DB
	now func() time.Time
}

// New returns the book over db, which must have foreign keys enforced (the
// store's serving pool does) so that deleting a report deletes its votes.
func New(db *sql.DB, now func() time.Time) *Book {
	return &Book{db: db, now: now}
}

// Cast sets the reader's vote on the report to k: it adds the vote, or changes
// the one already held, and casting the same kind again leaves it as it is. It
// is one statement. The report's published status is checked inside that same
// statement, so a vote can never attach to a report that was unpublished or
// deleted between the caller's lookup and this write; ErrNotOpen says so.
func (b *Book) Cast(ctx context.Context, reportID, githubID int64, k Kind) error {
	if k != Up && k != Down {
		return fmt.Errorf("votes: unknown kind %q", string(k))
	}
	now := b.now().Unix()
	res, err := b.db.ExecContext(ctx, `
INSERT INTO votes (report_id, github_id, kind, created_at, updated_at)
SELECT id, ?, ?, ?, ? FROM reports WHERE id = ? AND status = 'published'
ON CONFLICT (report_id, github_id) DO UPDATE SET kind = excluded.kind, updated_at = excluded.updated_at`,
		githubID, string(k), now, now, reportID)
	if err != nil {
		return fmt.Errorf("votes: cast: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("votes: cast: %w", err)
	}
	if n == 0 {
		return ErrNotOpen
	}
	return nil
}

// Withdraw removes the reader's vote on the report. Holding no vote is not an
// error: withdrawing twice, or from a stale page, ends in the same state.
func (b *Book) Withdraw(ctx context.Context, reportID, githubID int64) error {
	if _, err := b.db.ExecContext(ctx,
		`DELETE FROM votes WHERE report_id = ? AND github_id = ?`, reportID, githubID); err != nil {
		return fmt.Errorf("votes: withdraw: %w", err)
	}
	return nil
}

// PublicView is what any reader page may know about a report's votes.
type PublicView struct {
	// Likes is the number of thumbs up.
	Likes int
	// Mine is the viewer's own vote: "", "up" or "down".
	Mine string
}

// Public returns the like count and the viewer's own vote in one read.
// viewerID 0 is an anonymous viewer and never matches a row (users.github_id
// is positive).
func (b *Book) Public(ctx context.Context, reportID, viewerID int64) (PublicView, error) {
	var v PublicView
	err := b.db.QueryRowContext(ctx, `
SELECT COUNT(CASE WHEN kind = 'up' THEN 1 END),
       COALESCE(MAX(CASE WHEN github_id = ? THEN kind END), '')
FROM votes WHERE report_id = ?`, viewerID, reportID).Scan(&v.Likes, &v.Mine)
	if err != nil {
		return PublicView{}, fmt.Errorf("votes: read: %w", err)
	}
	return v, nil
}

// Tally is a report's votes as its author sees them.
type Tally struct {
	Likes, Dislikes int
}

// OwnerTally returns both counts. Only the owner's admin page may call it;
// the fitness rules enforce that.
func (b *Book) OwnerTally(ctx context.Context, reportID int64) (Tally, error) {
	var t Tally
	err := b.db.QueryRowContext(ctx, `
SELECT COUNT(CASE WHEN kind = 'up' THEN 1 END),
       COUNT(CASE WHEN kind = 'down' THEN 1 END)
FROM votes WHERE report_id = ?`, reportID).Scan(&t.Likes, &t.Dislikes)
	if err != nil {
		return Tally{}, fmt.Errorf("votes: tally: %w", err)
	}
	return t, nil
}
