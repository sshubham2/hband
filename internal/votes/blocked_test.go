package votes

import (
	"context"
	"errors"
	"testing"
)

// TestBlockedVoterIsFrozen: a blocked account's existing vote is neither changed
// nor withdrawn nor cast anew, each attempt is ErrBlocked, and the vote keeps
// counting. Unblocking lets it vote again.
func TestBlockedVoterIsFrozen(t *testing.T) {
	b, st := book(t)
	addUser(t, st, 7)
	addUser(t, st, 8)
	r := addReport(t, st, "x", "published")
	other := addReport(t, st, "y", "published")
	if err := b.Cast(bg, r, 7, Up); err != nil {
		t.Fatal(err)
	}
	if err := b.Cast(bg, r, 8, Down); err != nil {
		t.Fatal(err)
	}
	pub, tal := public(t, b, r, 7), tally(t, b, r)
	if pub != (PublicView{Likes: 1, Mine: "up"}) || tal != (Tally{Likes: 1, Dislikes: 1}) {
		t.Fatalf("before the block: %+v %+v", pub, tal)
	}
	if _, err := st.DB().Exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (7, 5)`); err != nil {
		t.Fatal(err)
	}

	// Blocking changes no number.
	if got := public(t, b, r, 7); got != pub {
		t.Errorf("Public after the block = %+v, want %+v", got, pub)
	}
	if got := tally(t, b, r); got != tal {
		t.Errorf("OwnerTally after the block = %+v, want %+v", got, tal)
	}

	for name, err := range map[string]error{
		"down on the report it voted on":   b.Cast(bg, r, 7, Down),
		"up again on the same report":      b.Cast(bg, r, 7, Up),
		"up on a report it never voted on": b.Cast(bg, other, 7, Up),
		"a withdrawal":                     b.Withdraw(bg, r, 7),
		"a withdrawal of no vote":          b.Withdraw(bg, other, 7),
	} {
		if !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: %v, want ErrBlocked", name, err)
		}
	}
	if got := public(t, b, r, 7); got != pub {
		t.Errorf("Public after the refused attempts = %+v, want the frozen %+v", got, pub)
	}
	if got := tally(t, b, r); got != tal {
		t.Errorf("OwnerTally after the refused attempts = %+v, want %+v", got, tal)
	}
	if got := public(t, b, other, 7); got != (PublicView{}) {
		t.Errorf("a blocked account cast a vote on another report: %+v", got)
	}
	if n := rows(t, st); n != 2 {
		t.Errorf("%d vote rows, want the two that existed", n)
	}

	// Another account is not affected, and the old answers stay for a refusal that
	// is not a block.
	if err := b.Cast(bg, r, 8, Up); err != nil {
		t.Errorf("an unblocked voter's Cast: %v", err)
	}
	draft := addReport(t, st, "d", "draft")
	if err := b.Cast(bg, draft, 8, Up); !errors.Is(err, ErrNotOpen) {
		t.Errorf("an unblocked voter on a draft: %v, want ErrNotOpen", err)
	}
	if err := b.Withdraw(bg, other, 8); err != nil {
		t.Errorf("withdrawing no vote, not blocked: %v, want nil", err)
	}

	// Unblocked, the account takes part again.
	if _, err := st.DB().Exec(`DELETE FROM blocked_accounts WHERE github_id = 7`); err != nil {
		t.Fatal(err)
	}
	if err := b.Cast(bg, r, 7, Down); err != nil {
		t.Fatalf("Cast after the block was lifted: %v", err)
	}
	if err := b.Withdraw(bg, r, 7); err != nil {
		t.Fatalf("Withdraw after the block was lifted: %v", err)
	}
	if got := public(t, b, r, 7); got.Mine != "" {
		t.Errorf("the vote was not withdrawn once unblocked: %+v", got)
	}
}

// TestBlockedVoteIsInTheStatement: the refusal is inside Cast's and Withdraw's own
// statements, not in the read that picks the answer. With that read made to say
// "not blocked", a blocked voter's vote is still not changed.
func TestBlockedVoteIsInTheStatement(t *testing.T) {
	b, st := book(t)
	addUser(t, st, 7)
	r := addReport(t, st, "x", "published")
	if err := b.Cast(bg, r, 7, Up); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (7, 5)`); err != nil {
		t.Fatal(err)
	}
	b.isBlocked = func(context.Context, int64) (bool, error) { return false, nil }
	if err := b.Cast(bg, r, 7, Down); !errors.Is(err, ErrNotOpen) {
		t.Errorf("Cast = %v, want the wrong answer ErrNotOpen from a read that said not blocked", err)
	}
	if err := b.Withdraw(bg, r, 7); err != nil {
		t.Errorf("Withdraw = %v, want nil from a read that said not blocked", err)
	}
	if got := public(t, b, r, 7); got != (PublicView{Likes: 1, Mine: "up"}) {
		t.Fatalf("a blocked voter's vote moved: %+v", got)
	}
}

// TestBlockedReadFails: when the read that picks the answer fails, Cast and
// Withdraw return a wrapped error: never ErrBlocked or ErrNotOpen, and above all
// never nil for Withdraw, which would tell a blocked account its vote was
// withdrawn when nothing happened.
func TestBlockedReadFails(t *testing.T) {
	b, st := book(t)
	addUser(t, st, 7)
	r := addReport(t, st, "x", "published")
	if err := b.Cast(bg, r, 7, Up); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (7, 5)`); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("the blocked read failed")
	b.isBlocked = func(context.Context, int64) (bool, error) { return false, boom }
	for name, err := range map[string]error{
		"Cast by a blocked voter":     b.Cast(bg, r, 7, Down),
		"Withdraw by a blocked voter": b.Withdraw(bg, r, 7),
	} {
		if !errors.Is(err, boom) || errors.Is(err, ErrBlocked) || errors.Is(err, ErrNotOpen) {
			t.Errorf("%s: %v, want an error wrapping the read's failure and neither sentinel", name, err)
		}
	}
	// A withdrawal that removed nothing needs the read too; one that removed a row does not.
	addUser(t, st, 8)
	if err := b.Withdraw(bg, r, 8); !errors.Is(err, boom) {
		t.Errorf("Withdraw of no vote with a failing read = %v, want the read's failure", err)
	}
	if err := b.Cast(bg, r, 8, Up); err != nil {
		t.Fatalf("a cast that wrote a row needs no read: %v", err)
	}
	if err := b.Withdraw(bg, r, 8); err != nil {
		t.Errorf("a withdrawal that removed a row needs no read: %v", err)
	}
	if got := public(t, b, r, 7); got != (PublicView{Likes: 1, Mine: "up"}) {
		t.Errorf("the blocked voter's vote moved: %+v", got)
	}
}
