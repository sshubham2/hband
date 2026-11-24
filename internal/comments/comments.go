// Package comments keeps the public comments under a report: a tree of any
// depth, where a reply is a comment with a parent.
//
// It is the only code that names the comments table in SQL. Every write is one
// SQL statement and this package never opens a transaction: a transaction that
// reads first and writes second is upgraded from a read snapshot, and under WAL
// SQLite refuses that upgrade at once, without waiting on busy_timeout, whenever
// another connection has written in between. Single statements wait their turn
// instead, so concurrent readers posting never see "database is locked".
//
// What a reader may see is decided here and nowhere else. A comment is hidden
// when it was removed (removed_at is set) or when its writer is blocked (a row
// in blocked_accounts). Hidden text is blanked inside the query that reads it,
// so it never reaches Go, let alone a template. A hidden comment that still has
// a visible comment below it, at any depth, stays in the tree as a placeholder
// so the thread still reads; one with nothing visible below it is dropped.
// Nothing derived is stored, so unblocking an account restores its comments
// without writing to any of them.
//
// Remove is the only write of removed_at; nothing un-removes. A removal is never
// a row delete: the self-referencing foreign key refuses to delete a comment
// that has replies, and a deleted row could not be listed or kept apart from a
// block. Removal and blocking are two separate facts, so they commute: a second
// removal changes nothing, and lifting a block never brings a removed comment
// back.
package comments

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxRunes is the longest body a comment may have, counted in runes.
const MaxRunes = 5000

var (
	// ErrBadBody is returned by Post (and Normalize) for a body that is not
	// acceptable plain text: empty, too long, not UTF-8 or holding a control.
	ErrBadBody = errors.New("comments: the body is not acceptable")
	// ErrNoTarget is returned by Post when nothing can be commented on: there is
	// no published report with that id, or the parent is missing, belongs to
	// another report, was removed or was written by a blocked account. It is one
	// answer for all of them, so it says nothing about comments the reader may
	// not see. RemoveTarget and Remove return it too, when no comment has the id
	// (never stored, or deleted with its report).
	ErrNoTarget = errors.New("comments: nothing to comment on")
)

// Book is the comments of one database.
type Book struct {
	db  *sql.DB
	now func() time.Time
}

// New returns the book over db, which must have foreign keys enforced (the
// store's serving pool does) so that deleting a report deletes its comments.
func New(db *sql.DB, now func() time.Time) *Book {
	return &Book{db: db, now: now}
}

// visibleSQL is the one definition of "a reader may see this comment", over the
// comments row aliased by %[1]s. Post's parent check and Tree's SELECT both
// use it, so a reply is never accepted under a comment that Tree would draw as
// removed.
const visibleSQL = `(%[1]s.removed_at IS NULL AND NOT EXISTS (SELECT 1 FROM blocked_accounts k WHERE k.github_id = %[1]s.github_id))`

func fmtVisible(alias string) string { return fmt.Sprintf(visibleSQL, alias) }

var postSQL = `
INSERT INTO comments (report_id, parent_id, github_id, body, created_at)
SELECT r.id, NULLIF(?, 0), ?, ?, ?
FROM reports r
WHERE r.id = ? AND r.status = 'published'
  AND (? = 0 OR EXISTS (SELECT 1 FROM comments p WHERE p.id = ? AND p.report_id = r.id AND ` + fmtVisible("p") + `))`

// Post adds a comment by githubID under the report, as a reply to parentID, or
// as a top-level comment when parentID is 0, and returns its id. The body is
// normalised here (see Normalize), so every comment that is stored satisfies
// the body rules whoever calls; ErrBadBody is returned before any SQL runs.
//
// It is one statement. The report's published status, and for a reply that the
// parent is on the same report and visible, are checked inside that same
// statement, so a comment can never attach to a report that was unpublished or
// deleted between the caller's lookup and this write, or under a comment that
// cannot be answered; ErrNoTarget says so.
func (b *Book) Post(ctx context.Context, reportID, githubID, parentID int64, body string) (int64, error) {
	body, err := Normalize(body)
	if err != nil {
		return 0, err
	}
	if parentID < 0 {
		return 0, ErrNoTarget
	}
	res, err := b.db.ExecContext(ctx, postSQL,
		parentID, githubID, body, b.now().Unix(), reportID, parentID, parentID)
	if err != nil {
		return 0, fmt.Errorf("comments: post: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("comments: post: %w", err)
	}
	if n == 0 {
		return 0, ErrNoTarget
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("comments: post: %w", err)
	}
	return id, nil
}

// removeTargetSQL finds the report a comment is on and whether that report has a
// reader page. It reads the comment whatever its visibility: a removed comment,
// or one hidden by a block, is still a comment that can be removed.
const removeTargetSQL = `
SELECT r.slug, r.status = 'published'
FROM comments c JOIN reports r ON r.id = c.report_id
WHERE c.id = ?`

// RemoveTarget returns the slug of the report the comment is on and whether
// that report is published, or ErrNoTarget when no comment has the id. It is one
// read and writes nothing, so a caller can look the target up before it reads
// the rest of a request.
func (b *Book) RemoveTarget(ctx context.Context, id int64) (slug string, published bool, err error) {
	err = b.db.QueryRowContext(ctx, removeTargetSQL, id).Scan(&slug, &published)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrNoTarget
	}
	if err != nil {
		return "", false, fmt.Errorf("comments: look up a comment to remove: %w", err)
	}
	return slug, published, nil
}

// removeSQL keeps the time of the first removal: removing a comment that is
// already removed matches the row and writes the same value back.
const removeSQL = `UPDATE comments SET removed_at = COALESCE(removed_at, ?) WHERE id = ?`

// Remove hides the comment from every reader, at any depth, on any report
// whatever its publish state. It is one UPDATE keyed by the id: no transaction,
// no delete, and nothing written to the comment's replies or to blocked_accounts.
// What readers then see is Tree's business: the comment is a placeholder while
// something visible lies below it and is dropped otherwise.
//
// It is idempotent. Removing a comment that is already removed, or whose writer
// is blocked, is a success that keeps the first removal time. ErrNoTarget is
// returned when no comment has the id.
func (b *Book) Remove(ctx context.Context, id int64) error {
	res, err := b.db.ExecContext(ctx, removeSQL, b.now().Unix(), id)
	if err != nil {
		return fmt.Errorf("comments: remove: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("comments: remove: %w", err)
	}
	if n == 0 {
		return ErrNoTarget
	}
	return nil
}

// Normalize returns the text a comment is stored as, or ErrBadBody. In order:
//
//  1. invalid UTF-8 is refused;
//  2. CRLF and a lone CR become LF;
//  3. any C0 control except newline and tab, DEL and any C1 control is refused;
//  4. leading and trailing white space is trimmed;
//  5. an empty result is refused;
//  6. more than MaxRunes runes is refused.
//
// Nothing is rewritten as HTML: escaping is html/template's job, at the place
// the text is drawn, so markup is stored as the characters that were typed.
// Apart from the line-ending and trimming steps (2 and 4), the stored text is
// what was typed.
func Normalize(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", ErrBadBody
	}
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	for _, r := range s {
		switch {
		case r < 0x20 && r != '\n' && r != '\t':
			return "", ErrBadBody
		case r == 0x7f, r >= 0x80 && r <= 0x9f:
			return "", ErrBadBody
		}
	}
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > MaxRunes {
		return "", ErrBadBody
	}
	return s, nil
}
