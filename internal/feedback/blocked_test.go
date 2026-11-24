package feedback

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sshubham2/hband/internal/comments"
)

func (w *world) block(t *testing.T, id int64) {
	t.Helper()
	// This package only reads blocked_accounts; the row is put in by SQL here.
	if _, err := w.db.Exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (?, 5)`, id); err != nil {
		t.Fatal(err)
	}
}

// TestSendRefusesBlocked: a blocked sender's message is refused inside Send's own
// statement with ErrBlocked, which is comments.ErrBlocked, and nothing is stored;
// the answers for any other refusal are the old ones.
func TestSendRefusesBlocked(t *testing.T) {
	w := newWorld(t)
	if err := w.book.Send(bg, 1, 7, "before the block"); err != nil {
		t.Fatal(err)
	}
	w.block(t, 7)
	if ErrBlocked != comments.ErrBlocked {
		t.Fatal("feedback.ErrBlocked is not comments.ErrBlocked: a caller would have two errors to test for")
	}
	for name, report := range map[string]int64{"a published report": 1, "another published report": 3, "a draft": 2, "a report that does not exist": 99} {
		if err := w.book.Send(bg, report, 7, "after the block"); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: Send = %v, want ErrBlocked", name, err)
		}
	}
	if n := w.count(t, "SELECT count(*) FROM feedback"); n != 1 {
		t.Errorf("%d messages, want only the one sent before the block", n)
	}
	// A refused body is still ErrBadBody, before any SQL.
	if err := w.book.Send(bg, 1, 7, "  "); !errors.Is(err, ErrBadBody) {
		t.Errorf("an empty body from a blocked sender = %v, want ErrBadBody", err)
	}
	// Another sender is untouched, and a draft is still ErrNoTarget for them.
	if err := w.book.Send(bg, 1, 8, "from another reader"); err != nil {
		t.Errorf("an unblocked sender: %v", err)
	}
	if err := w.book.Send(bg, 2, 8, "x"); !errors.Is(err, ErrNoTarget) {
		t.Errorf("an unblocked sender on a draft: %v, want ErrNoTarget", err)
	}
	// Unblocked, the account sends again.
	if _, err := w.db.Exec(`DELETE FROM blocked_accounts WHERE github_id = 7`); err != nil {
		t.Fatal(err)
	}
	if err := w.book.Send(bg, 1, 7, "after the block was lifted"); err != nil {
		t.Errorf("Send after the block was lifted: %v", err)
	}
}

// TestSendBlockIsInTheStatement: the refusal is in Send's single statement, not in
// the read that picks the answer.
func TestSendBlockIsInTheStatement(t *testing.T) {
	w := newWorld(t)
	w.block(t, 7)
	w.book.isBlocked = func(context.Context, int64) (bool, error) { return false, nil }
	if err := w.book.Send(bg, 1, 7, "slipped through?"); !errors.Is(err, ErrNoTarget) {
		t.Errorf("Send = %v, want the wrong answer ErrNoTarget from a read that said not blocked", err)
	}
	if n := w.count(t, "SELECT count(*) FROM feedback"); n != 0 {
		t.Fatalf("%d messages stored by a blocked sender: the refusal is not in the statement", n)
	}
}

// TestSendBlockReadFails: a failing read returns a wrapped error, never ErrBlocked
// or ErrNoTarget, and nothing is stored.
func TestSendBlockReadFails(t *testing.T) {
	w := newWorld(t)
	w.block(t, 7)
	boom := errors.New("the blocked read failed")
	w.book.isBlocked = func(context.Context, int64) (bool, error) { return false, boom }
	for name, report := range map[string]int64{"a blocked sender": 1, "a draft": 2} {
		err := w.book.Send(bg, report, 7, "x")
		if !errors.Is(err, boom) || errors.Is(err, ErrBlocked) || errors.Is(err, ErrNoTarget) {
			t.Errorf("%s: Send = %v, want an error wrapping the read's failure and neither sentinel", name, err)
		}
	}
	if n := w.count(t, "SELECT count(*) FROM feedback"); n != 0 {
		t.Errorf("%d messages stored", n)
	}
}

// TestInboxMarksBlocked: the owner's inbox marks the messages of a blocked sender,
// and only those; the messages themselves stay, with their replies.
func TestInboxMarksBlocked(t *testing.T) {
	w := newWorld(t)
	for _, m := range []struct {
		who  int64
		body string
	}{{7, "from seven"}, {8, "from eight"}, {7, "from seven again"}} {
		if err := w.book.Send(bg, 1, m.who, m.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.book.OwnerReply(bg, 1, "an answer to seven"); err != nil {
		t.Fatal(err)
	}
	before, err := w.book.OwnerInbox(bg, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range before {
		if m.Blocked {
			t.Fatalf("message %d marked blocked before any block", m.ID)
		}
	}
	w.block(t, 7)
	after, err := w.book.OwnerInbox(bg, 1)
	if err != nil || len(after) != 3 {
		t.Fatalf("OwnerInbox = %+v, %v, want the three messages kept", after, err)
	}
	for _, m := range after {
		if want := m.GitHubID == 7; m.Blocked != want {
			t.Errorf("message %d from %d: Blocked = %v, want %v", m.ID, m.GitHubID, m.Blocked, want)
		}
	}
	if len(after[2].Replies) != 1 {
		t.Errorf("the blocked sender's message lost its reply: %+v", after[2])
	}
}

// TestOwnerInboxBySender: every message one account sent, on every report, oldest
// first, with the report each was sent on.
func TestOwnerInboxBySender(t *testing.T) {
	w := newWorld(t)
	for _, m := range []struct {
		report, who int64
		body        string
	}{{1, 7, "first from seven"}, {1, 8, "from eight"}, {3, 7, "second from seven"}} {
		w.clk.t = w.clk.t.Add(time.Minute)
		if err := w.book.Send(bg, m.report, m.who, m.body); err != nil {
			t.Fatal(err)
		}
	}
	got, err := w.book.OwnerInboxBySender(bg, 7)
	if err != nil || len(got) != 2 {
		t.Fatalf("OwnerInboxBySender(7) = %+v, %v, want two messages", got, err)
	}
	if got[0].Body != "first from seven" || got[0].Slug != "x" || !got[0].Published || got[0].ID != 1 ||
		got[1].Body != "second from seven" || got[1].Slug != "y" || got[1].ID != 3 {
		t.Errorf("messages %+v, want oldest first with their reports", got)
	}
	if got[0].When.Location() != time.UTC || !got[0].When.Before(got[1].When) {
		t.Errorf("When = %v, %v, want UTC and oldest first", got[0].When, got[1].When)
	}
	// A report that was unpublished is still named, and is not published.
	if _, err := w.db.Exec(`UPDATE reports SET status = 'draft' WHERE id = 3`); err != nil {
		t.Fatal(err)
	}
	if got, _ = w.book.OwnerInboxBySender(bg, 7); got[1].Published {
		t.Error("an unpublished report is listed as published")
	}
	if got, err := w.book.OwnerInboxBySender(bg, 999); err != nil || len(got) != 0 {
		t.Errorf("an id that sent nothing: %+v, %v", got, err)
	}
}
