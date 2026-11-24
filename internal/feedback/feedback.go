// Package feedback keeps the private messages a signed-in reader sends to the
// author of a report. They are not comments: they live in their own table,
// nothing that reads the public thread can reach them, and only two views show
// them: the author's admin page, and the sender's own page, which shows that
// reader their own messages and the author's replies.
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
// every message on a report with each sender's login and avatar, whether the
// sender is blocked, and the author's replies; OwnerInboxBySender returns every
// message one account sent, for the Blocked users page; OwnerReplyTarget and
// OwnerReply serve the author's reply route. All four are for the owner's admin
// handlers alone, and the fitness rules keep every other file from naming them.
// Blocking never hides or drops a message from the author: a blocked sender's
// messages stay in the inbox, marked, and Send refuses only new ones. SenderThreads is for the reader page alone
// and is keyed by the signed-in viewer's own id. Send hands back no row id, and
// the types a reader is shown (Sent, Reply) have none: the only public caller is
// the sender's own form, and an id is a counter shared with every other reader's
// messages.
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
	// ErrBadBody is returned by Send and OwnerReply for a body that is not
	// acceptable plain text: empty, too long, not UTF-8 or holding a control. It
	// is the same value as comments.ErrBadBody, so feedback and comments follow
	// one set of rules and a caller tests for one error.
	ErrBadBody = comments.ErrBadBody
	// ErrNoTarget is returned by Send when no published report has that id at the
	// moment of the write (it is a draft, was unpublished or was deleted), and by
	// OwnerReplyTarget and OwnerReply when no message has that id.
	ErrNoTarget = errors.New("feedback: nothing to send feedback on")
	// ErrBlocked is returned by Send when the sender's account is blocked. It is the
	// same value as comments.ErrBlocked, as ErrBadBody is, so a caller tests for one
	// error.
	ErrBlocked = comments.ErrBlocked
)

// Book is the feedback of one database.
type Book struct {
	db  *sql.DB
	now func() time.Time
	// isBlocked says whether an account has a blocked_accounts row. It is the read
	// that picks the refusal after a send was refused; it is a field only so that a
	// test can make that one read fail.
	isBlocked func(ctx context.Context, githubID int64) (bool, error)
}

// New returns the book over db, which must have foreign keys enforced (the
// store's serving pool does) so that deleting a report deletes its feedback.
func New(db *sql.DB, now func() time.Time) *Book {
	b := &Book{db: db, now: now}
	b.isBlocked = b.blockedRow
	return b
}

// blockedRow reads whether the account is blocked. This package only reads
// blocked_accounts (here, and as the blocked column of the owner's inbox);
// internal/comments is the only writer of the table.
func (b *Book) blockedRow(ctx context.Context, githubID int64) (bool, error) {
	var blocked bool
	err := b.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM blocked_accounts WHERE github_id = ?)`, githubID).Scan(&blocked)
	return blocked, err
}

// sendSQL stores one message, and only when the report is published at the
// moment of the write and the sender is not blocked: both checks are inside the
// statement, so a message can never attach to a report that was unpublished or
// deleted after the caller looked it up, and a block that lands mid-request
// cannot let one last message through.
const sendSQL = `
INSERT INTO feedback (report_id, github_id, body, created_at)
SELECT r.id, ?, ?, ?
FROM reports r
WHERE r.id = ? AND r.status = 'published'
  AND NOT EXISTS (SELECT 1 FROM blocked_accounts k WHERE k.github_id = ?)`

// Send stores a message from githubID on the report. The body is normalised
// here (comments.Normalize), so every message that is stored satisfies the body
// rules whoever calls; ErrBadBody is returned before any SQL runs. When no row was
// written a second read only picks the answer: ErrBlocked for a blocked sender,
// ErrNoTarget otherwise (the report is not published at write time). If that read
// fails the error is wrapped and returned, never turned into either answer.
//
// It returns no id on purpose. The caller is the public handler, and a message's
// id is global across reports and readers: a sender who learned it could count
// the messages other readers sent in between.
func (b *Book) Send(ctx context.Context, reportID, githubID int64, body string) error {
	body, err := comments.Normalize(body)
	if err != nil {
		return err
	}
	res, err := b.db.ExecContext(ctx, sendSQL, githubID, body, b.now().Unix(), reportID, githubID)
	if err != nil {
		return fmt.Errorf("feedback: send: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("feedback: send: %w", err)
	}
	if n == 0 {
		blocked, err := b.isBlocked(ctx, githubID)
		if err != nil {
			return fmt.Errorf("feedback: send: is blocked: %w", err)
		}
		if blocked {
			return ErrBlocked
		}
		return ErrNoTarget
	}
	return nil
}

// Reply is the author's answer to one message. It carries no author and no id:
// every reply is the pinned owner's, and a reader is never shown a row id.
type Reply struct {
	Body string
	// When is the time the reply was written, in UTC.
	When time.Time
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
	// Blocked is set when the sender's account is blocked now. The message was
	// stored all the same (it may predate the block) and is not hidden from the
	// author; the owner's page marks it.
	Blocked bool
	// Replies are the author's answers to this message, oldest first.
	Replies []Reply
}

// SenderMessage is one message of a single sender, for the author's Blocked users
// page, with the report it was sent on. Like Message it is the owner's type.
type SenderMessage struct {
	ID   int64
	Body string
	// When is the time the message was sent, in UTC.
	When time.Time
	// Slug and Title name the report, and Published says whether it has a reader
	// page.
	Slug      string
	Title     string
	Published bool
}

// Sent is one of a reader's own messages as that reader is shown it, with the
// author's replies. It deliberately has no ID field: a message's id is a counter
// shared with every other reader's messages, so a template that could print it
// would tell a reader how much feedback others sent.
type Sent struct {
	Body string
	// When is the time the message was sent, in UTC.
	When time.Time
	// Replies are the author's answers, oldest first.
	Replies []Reply
}

// unixUTC is the one conversion from a stored created_at to a time: unix
// seconds, UTC. Every scanned created_at goes through it, so the template's
// literal "UTC" is true whatever zone the machine runs in.
func unixUTC(s int64) time.Time { return time.Unix(s, 0).UTC() }

const inboxSQL = `
SELECT f.id, f.github_id, u.login, u.avatar_url, f.body, f.created_at,
       EXISTS (SELECT 1 FROM blocked_accounts k WHERE k.github_id = f.github_id)
FROM feedback f JOIN users u ON u.github_id = f.github_id
WHERE f.report_id = ?
ORDER BY f.id DESC`

// inboxRepliesSQL reads every reply to a message on the report in one pass, in
// the order they were written; OwnerInbox groups them by message in Go.
const inboxRepliesSQL = `
SELECT p.feedback_id, p.body, p.created_at
FROM feedback_replies p JOIN feedback f ON f.id = p.feedback_id
WHERE f.report_id = ?
ORDER BY p.id`

// OwnerInbox returns every message on the report, newest first, each with its
// sender and the author's replies to it. It applies no limit. It is for the
// owner's admin page only.
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
		if err := rows.Scan(&m.ID, &m.GitHubID, &m.Login, &m.AvatarURL, &m.Body, &created, &m.Blocked); err != nil {
			return nil, fmt.Errorf("feedback: inbox: %w", err)
		}
		m.When = unixUTC(created)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("feedback: inbox: %w", err)
	}
	if len(out) == 0 {
		return out, nil
	}
	replies, err := b.repliesOf(ctx, inboxRepliesSQL, reportID)
	if err != nil {
		return nil, fmt.Errorf("feedback: inbox: %w", err)
	}
	for i := range out {
		out[i].Replies = replies[out[i].ID]
	}
	return out, nil
}

const bySenderSQL = `
SELECT f.id, f.body, f.created_at, r.slug, r.title, r.status = 'published'
FROM feedback f JOIN reports r ON r.id = f.report_id
WHERE f.github_id = ?
ORDER BY f.id`

// OwnerInboxBySender returns every message the account sent, on any report,
// oldest first, each with the report it was sent on. It applies no limit. It is
// for the owner's Blocked users page only, which lists a blocked account's
// messages under its name; an id that sent nothing is answered with no messages.
func (b *Book) OwnerInboxBySender(ctx context.Context, githubID int64) ([]SenderMessage, error) {
	rows, err := b.db.QueryContext(ctx, bySenderSQL, githubID)
	if err != nil {
		return nil, fmt.Errorf("feedback: inbox by sender: %w", err)
	}
	defer rows.Close()
	var out []SenderMessage
	for rows.Next() {
		var m SenderMessage
		var created int64
		if err := rows.Scan(&m.ID, &m.Body, &created, &m.Slug, &m.Title, &m.Published); err != nil {
			return nil, fmt.Errorf("feedback: inbox by sender: %w", err)
		}
		m.When = unixUTC(created)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("feedback: inbox by sender: %w", err)
	}
	return out, nil
}

// repliesOf runs one of the reply queries (three columns: the message's id, the
// reply's body and its created_at, oldest reply first) and groups the replies by
// the message they answer.
func (b *Book) repliesOf(ctx context.Context, query string, args ...any) (map[int64][]Reply, error) {
	rows, err := b.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Reply{}
	for rows.Next() {
		var id, created int64
		var r Reply
		if err := rows.Scan(&id, &r.Body, &created); err != nil {
			return nil, err
		}
		r.When = unixUTC(created)
		out[id] = append(out[id], r)
	}
	return out, rows.Err()
}

const senderSQL = `
SELECT f.id, f.body, f.created_at
FROM feedback f
WHERE f.report_id = ? AND f.github_id = ?
ORDER BY f.id`

const senderRepliesSQL = `
SELECT p.feedback_id, p.body, p.created_at
FROM feedback_replies p JOIN feedback f ON f.id = p.feedback_id
WHERE f.report_id = ? AND f.github_id = ?
ORDER BY p.id`

// SenderThreads returns the messages githubID sent on the report, oldest first,
// each with the author's replies. It is the reader's own view: the caller must
// pass the signed-in viewer's own id from the session, never a value from the
// request, because the id is the only thing that keeps one reader's thread from
// another's. An id that is not a real account (zero or negative) is answered with
// nothing and no query.
func (b *Book) SenderThreads(ctx context.Context, reportID, githubID int64) ([]Sent, error) {
	if githubID <= 0 {
		return nil, nil
	}
	rows, err := b.db.QueryContext(ctx, senderSQL, reportID, githubID)
	if err != nil {
		return nil, fmt.Errorf("feedback: sender threads: %w", err)
	}
	defer rows.Close()
	var out []Sent
	var ids []int64
	for rows.Next() {
		var id, created int64
		var s Sent
		if err := rows.Scan(&id, &s.Body, &created); err != nil {
			return nil, fmt.Errorf("feedback: sender threads: %w", err)
		}
		s.When = unixUTC(created)
		out = append(out, s)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("feedback: sender threads: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	replies, err := b.repliesOf(ctx, senderRepliesSQL, reportID, githubID)
	if err != nil {
		return nil, fmt.Errorf("feedback: sender threads: %w", err)
	}
	for i := range out {
		out[i].Replies = replies[ids[i]]
	}
	return out, nil
}

const replyTargetSQL = `
SELECT r.slug
FROM feedback f JOIN reports r ON r.id = f.report_id
WHERE f.id = ?`

// OwnerReplyTarget returns the slug of the report a message was sent on, so the
// owner's reply can be answered with a redirect back to that report's admin
// page. It returns ErrNoTarget when there is no such message. It is for the
// owner's reply route alone.
func (b *Book) OwnerReplyTarget(ctx context.Context, feedbackID int64) (string, error) {
	var slug string
	err := b.db.QueryRowContext(ctx, replyTargetSQL, feedbackID).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoTarget
	}
	if err != nil {
		return "", fmt.Errorf("feedback: reply target: %w", err)
	}
	return slug, nil
}

// replySQL stores one reply, and only when the message still exists at the
// moment of the write: the check is inside the statement, so a reply can never
// attach to a message that was deleted with its report after the caller looked
// it up. Message ids are never handed out again (AUTOINCREMENT), so a form left
// open in an old tab cannot land on another reader's message.
const replySQL = `
INSERT INTO feedback_replies (feedback_id, body, created_at)
SELECT f.id, ?, ?
FROM feedback f
WHERE f.id = ?`

// OwnerReply stores the author's reply to a message. The body is normalised here
// (comments.Normalize), so every reply that is stored satisfies the same body
// rules as a message; ErrBadBody is returned before any SQL runs. It returns
// ErrNoTarget when the message is not there at write time. Whatever the report's
// state, a reply is stored: the reader sees it once the report is published.
//
// It records no author: the route that calls it is the owner's, and a reply is
// the owner's by construction. It returns no id; nothing needs one.
func (b *Book) OwnerReply(ctx context.Context, feedbackID int64, body string) error {
	body, err := comments.Normalize(body)
	if err != nil {
		return err
	}
	res, err := b.db.ExecContext(ctx, replySQL, body, b.now().Unix(), feedbackID)
	if err != nil {
		return fmt.Errorf("feedback: reply: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("feedback: reply: %w", err)
	}
	if n == 0 {
		return ErrNoTarget
	}
	return nil
}
