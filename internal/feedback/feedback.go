// Package feedback keeps the private messages a signed-in reader sends to the
// author of a report. They are not comments: they live in their own table,
// nothing that reads the public thread can reach them, and only the author's
// admin page lists them.
//
// It is the only code that names the feedback tables in SQL. Every write is one
// SQL statement and this package never opens a transaction: a transaction that
// reads first and writes second is upgraded from a read snapshot, and under WAL
// SQLite refuses that upgrade at once, without waiting on busy_timeout, whenever
// another connection has written in between. Single statements wait their turn
// instead, so readers sending feedback at the same time never see "database is
// locked".
//
// What may leave this package is decided by who calls it. OwnerInbox returns
// every message on a report with each sender's login and avatar, and is for the
// owner's admin page alone; the fitness rules keep every other file from naming
// it. Send hands back no row id: the only public caller is the sender's own
// form, and an id is a counter shared with every other reader's messages.
package feedback

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sshubham2/hband/internal/comments"
)

var (
	// ErrBadBody is returned by Send for a body that is not acceptable plain
	// text: empty, too long, not UTF-8 or holding a control. It is the same value
	// as comments.ErrBadBody, so feedback and comments follow one set of rules
	// and a caller tests for one error.
	ErrBadBody = comments.ErrBadBody
	// ErrNoTarget is returned by Send when no published report has that id at the
	// moment of the write: it is a draft, was unpublished or was deleted.
	ErrNoTarget = errors.New("feedback: nothing to send feedback on")
)

// Book is the feedback of one database.
type Book struct {
	db  *sql.DB
	now func() time.Time
}

// New returns the book over db, which must have foreign keys enforced (the
// store's serving pool does) so that deleting a report deletes its feedback.
func New(db *sql.DB, now func() time.Time) *Book {
	return &Book{db: db, now: now}
}

// sendSQL stores one message, and only when the report is published at the
// moment of the write: the check is inside the statement, so a message can never
// attach to a report that was unpublished or deleted after the caller looked it
// up.
const sendSQL = `
INSERT INTO feedback (report_id, github_id, body, created_at)
SELECT r.id, ?, ?, ?
FROM reports r
WHERE r.id = ? AND r.status = 'published'`

// Send stores a message from githubID on the report. The body is normalised
// here (comments.Normalize), so every message that is stored satisfies the body
// rules whoever calls; ErrBadBody is returned before any SQL runs. It returns
// ErrNoTarget when the report is not published at write time.
//
// It returns no id on purpose. The caller is the public handler, and a message's
// id is global across reports and readers: a sender who learned it could count
// the messages other readers sent in between.
func (b *Book) Send(ctx context.Context, reportID, githubID int64, body string) error {
	body, err := comments.Normalize(body)
	if err != nil {
		return err
	}
	res, err := b.db.ExecContext(ctx, sendSQL, githubID, body, b.now().Unix(), reportID)
	if err != nil {
		return fmt.Errorf("feedback: send: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("feedback: send: %w", err)
	}
	if n == 0 {
		return ErrNoTarget
	}
	return nil
}

// Message is one message as the owner sees it: with its row id, who sent it
// and when. It is the owner's type; nothing a reader is shown is built from it.
type Message struct {
	ID       int64
	GitHubID int64
	// Login and AvatarURL are the sender's current ones, from the users row, so a
	// renamed account shows under its new name. AvatarURL is empty when GitHub
	// gave none.
	Login     string
	AvatarURL string
	Body      string
	// When is the time the message was sent, in UTC.
	When time.Time
}

// unixUTC is the one conversion from a stored created_at to a time: unix
// seconds, UTC. Every scanned created_at goes through it, so the template's
// literal "UTC" is true whatever zone the machine runs in.
func unixUTC(s int64) time.Time { return time.Unix(s, 0).UTC() }

const inboxSQL = `
SELECT f.id, f.github_id, u.login, u.avatar_url, f.body, f.created_at
FROM feedback f JOIN users u ON u.github_id = f.github_id
WHERE f.report_id = ?
ORDER BY f.id DESC`

// OwnerInbox returns every message on the report, newest first, each with its
// sender. It applies no limit. It is for the owner's admin page only.
func (b *Book) OwnerInbox(ctx context.Context, reportID int64) ([]Message, error) {
	rows, err := b.db.QueryContext(ctx, inboxSQL, reportID)
	if err != nil {
		return nil, fmt.Errorf("feedback: inbox: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var created int64
		if err := rows.Scan(&m.ID, &m.GitHubID, &m.Login, &m.AvatarURL, &m.Body, &created); err != nil {
			return nil, fmt.Errorf("feedback: inbox: %w", err)
		}
		m.When = unixUTC(created)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("feedback: inbox: %w", err)
	}
	return out, nil
}
