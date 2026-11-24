package comments

import (
	"context"
	"errors"
	"testing"
	"time"
)

func (f *fx) blockedRows() int64 { return f.scalar(`SELECT count(*) FROM blocked_accounts`) }

// TestBlockStore: Block and Unblock are one statement each and idempotent, the
// first blocked_at is kept, the author's own account and every non-account id are
// refused before any SQL, and a book with no owner pinned refuses everyone.
func TestBlockStore(t *testing.T) {
	t.Run("block, repeat, unblock, repeat", func(t *testing.T) {
		f := newFx(t)
		if err := f.book.Block(ctx, 5); err != nil {
			t.Fatalf("Block: %v", err)
		}
		if n := f.scalar(`SELECT blocked_at FROM blocked_accounts WHERE github_id = 5`); n != fixedNow().Unix() {
			t.Errorf("blocked_at = %d, want the clock's %d", n, fixedNow().Unix())
		}
		// A later book (a later clock) repeats the block: still one row, first time kept.
		later := New(f.st.DB(), func() time.Time { return time.Unix(1_900_000_000, 0) }, testOwner)
		if err := later.Block(ctx, 5); err != nil {
			t.Fatalf("a repeated Block: %v", err)
		}
		if n := f.blockedRows(); n != 1 {
			t.Errorf("%d rows after blocking twice, want 1", n)
		}
		if n := f.scalar(`SELECT blocked_at FROM blocked_accounts WHERE github_id = 5`); n != fixedNow().Unix() {
			t.Errorf("a repeated Block changed blocked_at to %d, want the first block's %d", n, fixedNow().Unix())
		}
		if err := f.book.Unblock(ctx, 5); err != nil {
			t.Fatalf("Unblock: %v", err)
		}
		if err := f.book.Unblock(ctx, 5); err != nil {
			t.Fatalf("a repeated Unblock: %v", err)
		}
		if err := f.book.Unblock(ctx, 6); err != nil {
			t.Fatalf("Unblock of an account that was never blocked: %v", err)
		}
		if n := f.blockedRows(); n != 0 {
			t.Errorf("%d rows after unblocking, want 0", n)
		}
	})

	t.Run("the author is refused by the store", func(t *testing.T) {
		f := newFx(t)
		if err := f.book.Block(ctx, testOwner); !errors.Is(err, ErrOwnerBlock) {
			t.Fatalf("Block(owner) = %v, want ErrOwnerBlock", err)
		}
		if n := f.blockedRows(); n != 0 {
			t.Errorf("%d rows after refusing the author", n)
		}
	})

	t.Run("no owner pinned fails closed", func(t *testing.T) {
		f := newFx(t)
		for _, owner := range []int64{0, -3} {
			b := New(f.st.DB(), fixedNow, owner)
			// The id 0 would equal a zero owner id, and any other account would be let
			// through by a store that read "no owner" as "nobody is the owner".
			for _, u := range []int64{owner, 5, testOwner} {
				err := b.Block(ctx, u)
				if err == nil || errors.Is(err, ErrOwnerBlock) {
					t.Errorf("owner %d: Block(%d) = %v, want an error that is not ErrOwnerBlock", owner, u, err)
				}
			}
		}
		if n := f.blockedRows(); n != 0 {
			t.Errorf("%d rows written by books with no owner", n)
		}
	})

	t.Run("ids that are not accounts", func(t *testing.T) {
		f := newFx(t)
		for _, u := range []int64{0, -1} {
			if err := f.book.Block(ctx, u); err == nil || errors.Is(err, ErrOwnerBlock) {
				t.Errorf("Block(%d) = %v, want an error", u, err)
			}
		}
		if n := f.blockedRows(); n != 0 {
			t.Errorf("%d rows", n)
		}
	})

	t.Run("an account that never signed in", func(t *testing.T) {
		f := newFx(t)
		if err := f.book.Block(ctx, 123456); err != nil {
			t.Fatalf("Block of an id with no users row: %v", err)
		}
		got, err := f.book.Blocked(ctx)
		if err != nil || len(got) != 1 || got[0].GitHubID != 123456 || got[0].Login != "" || got[0].AvatarURL != "" || len(got[0].Comments) != 0 {
			t.Fatalf("Blocked() = %+v, %v, want the one id with no login and no comments", got, err)
		}
	})
}

// TestPostRefusesBlocked: a blocked writer's comment is refused inside Post's own
// statement, whatever the report and the parent are, and nothing is stored.
func TestPostRefusesBlocked(t *testing.T) {
	f := newFx(t)
	f.user(1, "visible")
	f.user(2, "blocked")
	pub, draft := f.report("pub", "published"), f.report("draft", "draft")
	top := f.post(pub, 1, 0, "a visible comment")
	if err := f.book.Block(ctx, 2); err != nil {
		t.Fatal(err)
	}
	before := f.count("1 = 1")
	for name, c := range map[string]struct{ report, parent int64 }{
		"a top-level comment":            {pub, 0},
		"a reply to a visible comment":   {pub, top},
		"a reply to a missing comment":   {pub, 9999},
		"a comment on a draft report":    {draft, 0},
		"a comment on a missing report":  {9999, 0},
		"a reply with a negative parent": {pub, -1},
	} {
		_, err := f.book.Post(ctx, c.report, 2, c.parent, "from a blocked account")
		switch {
		case c.parent < 0:
			// The parent is refused before any SQL, as for every writer: ErrNoTarget.
			if !errors.Is(err, ErrNoTarget) {
				t.Errorf("%s: Post = %v, want ErrNoTarget (refused before SQL)", name, err)
			}
		case !errors.Is(err, ErrBlocked):
			t.Errorf("%s: Post = %v, want ErrBlocked", name, err)
		}
	}
	if after := f.count("1 = 1"); after != before {
		t.Errorf("%d comments, want %d: a blocked account's comment was stored", after, before)
	}
	// Control: the same account is not refused for any other reason once unblocked.
	if err := f.book.Unblock(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.book.Post(ctx, pub, 2, top, "taking part again"); err != nil {
		t.Errorf("Post after Unblock: %v", err)
	}
	// And a writer who is not blocked still gets the old answers.
	if _, err := f.book.Post(ctx, draft, 1, 0, "x"); !errors.Is(err, ErrNoTarget) {
		t.Errorf("an unblocked writer on a draft: %v, want ErrNoTarget", err)
	}
}

// TestPostBlockIsInTheStatement: the refusal is in Post's single statement, not in
// the read that picks the answer. With that read made to say "not blocked", a
// blocked writer still stores nothing (the answer is only the wrong one).
func TestPostBlockIsInTheStatement(t *testing.T) {
	f := newFx(t)
	f.user(2, "blocked")
	pub := f.report("pub", "published")
	f.exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (2, 5)`)
	f.book.isBlocked = func(context.Context, int64) (bool, error) { return false, nil }
	if _, err := f.book.Post(ctx, pub, 2, 0, "slipped through?"); !errors.Is(err, ErrNoTarget) {
		t.Errorf("Post = %v, want ErrNoTarget from a read that said not blocked", err)
	}
	if n := f.count("1 = 1"); n != 0 {
		t.Fatalf("%d comments stored by a blocked account: the refusal is not in the statement", n)
	}
}

// TestPostBlockReadFails: when the read that picks the answer fails, Post returns
// a wrapped error, never ErrBlocked or ErrNoTarget (which would be a refusal the
// store did not decide) and never a stored comment.
func TestPostBlockReadFails(t *testing.T) {
	f := newFx(t)
	f.user(2, "blocked")
	pub, draft := f.report("pub", "published"), f.report("draft", "draft")
	f.exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (2, 5)`)
	boom := errors.New("the blocked read failed")
	f.book.isBlocked = func(context.Context, int64) (bool, error) { return false, boom }
	// A blocked writer, and a writer refused for another reason (a draft report): both
	// leave the write with no row, and both need the read.
	for name, c := range map[string]struct{ report, who int64 }{"a blocked writer": {pub, 2}, "a draft report": {draft, 2}} {
		_, err := f.book.Post(ctx, c.report, c.who, 0, "x")
		if !errors.Is(err, boom) || errors.Is(err, ErrBlocked) || errors.Is(err, ErrNoTarget) {
			t.Errorf("%s: Post = %v, want an error wrapping the read's failure and neither sentinel", name, err)
		}
	}
	if n := f.count("1 = 1"); n != 0 {
		t.Errorf("%d comments stored", n)
	}
}

// TestBlockedList: Blocked is one read in the order the page draws: oldest block
// first, each account's comments oldest first, the text of a removed comment
// blanked inside the query, drafts and unpublished reports included, an account
// with no comment kept.
func TestBlockedList(t *testing.T) {
	f := newFx(t)
	f.user(1, "visible")
	f.user(2, "second")
	f.user(3, "third")
	pub, other, draft := f.report("pub", "published"), f.report("other", "published"), f.report("draft", "draft")
	top := f.post(pub, 2, 0, "second top")
	reply := f.post(pub, 2, top, "second reply")
	f.post(pub, 1, top, "a visible reply, not listed")
	removed := f.post(other, 2, 0, "second removed text")
	f.exec(`UPDATE comments SET removed_at = 7 WHERE id = ?`, removed)
	f.exec(`INSERT INTO comments (report_id, github_id, body, created_at) VALUES (?, 3, 'third on a draft', 3)`, draft)
	// Blocks: 3 first, then 2, then an id with no row anywhere, then 4 (with no comments).
	f.exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (3, 10), (2, 20), (4, 20), (999, 30)`)
	f.user(4, "fourth")

	got, err := f.book.Blocked(ctx)
	if err != nil {
		t.Fatalf("Blocked: %v", err)
	}
	var ids []int64
	for _, a := range got {
		ids = append(ids, a.GitHubID)
	}
	if want := []int64{3, 2, 4, 999}; len(ids) != len(want) || ids[0] != 3 || ids[1] != 2 || ids[2] != 4 || ids[3] != 999 {
		t.Fatalf("accounts %v, want %v (oldest block first, then by id)", ids, want)
	}
	if got[0].BlockedAt.Unix() != 10 || got[0].BlockedAt.Location() != time.UTC {
		t.Errorf("BlockedAt = %v, want unix 10 in UTC", got[0].BlockedAt)
	}
	third := got[0].Comments
	if len(third) != 1 || third[0].Body != "third on a draft" || third[0].Published || third[0].Slug != "draft" || third[0].Removed {
		t.Errorf("the third account's comments: %+v, want one comment on the draft report", third)
	}
	second := got[1]
	if second.Login != "second" || len(second.Comments) != 3 {
		t.Fatalf("the second account: %+v, want login and three comments", second)
	}
	c0, c1, c2 := second.Comments[0], second.Comments[1], second.Comments[2]
	if c0.ID != top || c1.ID != reply || c2.ID != removed {
		t.Errorf("comment order %d %d %d, want %d %d %d (oldest first)", c0.ID, c1.ID, c2.ID, top, reply, removed)
	}
	if c0.ParentID != 0 || c1.ParentID != top || c0.Body != "second top" || c1.Body != "second reply" || !c0.Published || c0.Slug != "pub" || c0.ReportID != pub || c0.Title != "pub" {
		t.Errorf("top and reply: %+v %+v", c0, c1)
	}
	if !c2.Removed || c2.Body != "" || c2.Slug != "other" {
		t.Errorf("the removed comment: %+v, want Removed with no text", c2)
	}
	if len(got[2].Comments) != 0 || got[3].Login != "" || len(got[3].Comments) != 0 {
		t.Errorf("an account with no comments, and one with no users row: %+v %+v", got[2], got[3])
	}
}

// TestUnblockKeepsRemoved: a comment removed while its writer was blocked stays
// removed after a real Unblock; the rest of the account's comments come back.
func TestUnblockKeepsRemoved(t *testing.T) {
	f := newFx(t)
	f.user(2, "writer")
	pub := f.report("pub", "published")
	kept := f.post(pub, 2, 0, "kept")
	gone := f.post(pub, 2, 0, "to be removed while blocked")
	if err := f.book.Block(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := f.book.Remove(ctx, gone); err != nil {
		t.Fatal(err)
	}
	tree, err := f.book.Tree(ctx, pub)
	if err != nil || len(tree) != 0 {
		t.Fatalf("while blocked: %v, %v, want nothing drawn", tree, err)
	}
	if err := f.book.Unblock(ctx, 2); err != nil {
		t.Fatal(err)
	}
	tree, err = f.book.Tree(ctx, pub)
	if err != nil || len(tree) != 1 || tree[0].ID != kept {
		t.Fatalf("after Unblock: %+v, %v, want only the comment that was not removed", tree, err)
	}
	if n := f.count("id = ? AND removed_at IS NOT NULL", gone); n != 1 {
		t.Errorf("the removed comment's removal was lost by Unblock")
	}
}
