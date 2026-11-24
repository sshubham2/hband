package votes

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sshubham2/hband/internal/store"
)

var bg = context.Background()

var epoch = time.Unix(1_800_000_000, 0)

// book opens a real store in a temp dir, as the program does, and returns the
// book over its serving pool.
func book(t *testing.T) (*Book, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hband.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st.DB(), func() time.Time { return epoch }), st
}

func addUser(t *testing.T, st *store.Store, id int64) {
	t.Helper()
	if _, err := st.DB().Exec(
		`INSERT INTO users (github_id, login, avatar_url, created_at, updated_at) VALUES (?, ?, '', 1, 1)`,
		id, fmt.Sprintf("reader%d", id)); err != nil {
		t.Fatalf("add user %d: %v", id, err)
	}
}

func addReport(t *testing.T, st *store.Store, slug, status string) int64 {
	t.Helper()
	res, err := st.DB().Exec(
		`INSERT INTO reports (slug, title, status, published_at, size_bytes, sha256, created_at, updated_at)
		 VALUES (?, ?, ?, 1, 10, 'x', 1, 1)`, slug, slug, status)
	if err != nil {
		t.Fatalf("add report %s: %v", slug, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func tally(t *testing.T, b *Book, report int64) Tally {
	t.Helper()
	got, err := b.OwnerTally(bg, report)
	if err != nil {
		t.Fatalf("OwnerTally: %v", err)
	}
	return got
}

func rows(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(`SELECT count(*) FROM votes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func public(t *testing.T, b *Book, report, viewer int64) PublicView {
	t.Helper()
	got, err := b.Public(bg, report, viewer)
	if err != nil {
		t.Fatalf("Public: %v", err)
	}
	return got
}

// A reader who already holds a thumbs up and sends it again: the conflict
// update must count as a change (so it is not mistaken for "no such report"),
// and the like count stays one.
func TestCastSameVoteTwice(t *testing.T) {
	b, st := book(t)
	addUser(t, st, 7)
	r := addReport(t, st, "x", "published")

	for i := 1; i <= 3; i++ {
		if err := b.Cast(bg, r, 7, Up); err != nil {
			t.Fatalf("cast %d of the same up vote: %v", i, err)
		}
		if got := public(t, b, r, 7); got != (PublicView{Likes: 1, Mine: "up"}) {
			t.Fatalf("after %d identical casts: %+v", i, got)
		}
	}
	if n := rows(t, st); n != 1 {
		t.Fatalf("%d vote rows after pressing the same thumb three times, want 1", n)
	}
	// The same holds for a repeated thumbs down.
	for i := 0; i < 2; i++ {
		if err := b.Cast(bg, r, 7, Down); err != nil {
			t.Fatalf("repeat down: %v", err)
		}
	}
	if got := tally(t, b, r); got != (Tally{Likes: 0, Dislikes: 1}) {
		t.Fatalf("after a repeated down: %+v", got)
	}
}

// Switching replaces the vote: the row is changed, not added to.
func TestCastSwitches(t *testing.T) {
	b, st := book(t)
	addUser(t, st, 7)
	r := addReport(t, st, "x", "published")

	if err := b.Cast(bg, r, 7, Up); err != nil {
		t.Fatal(err)
	}
	if err := b.Cast(bg, r, 7, Down); err != nil {
		t.Fatal(err)
	}
	if got := tally(t, b, r); got != (Tally{Likes: 0, Dislikes: 1}) {
		t.Fatalf("up then down: %+v, want 0 likes and 1 dislike", got)
	}
	if got := public(t, b, r, 7); got != (PublicView{Likes: 0, Mine: "down"}) {
		t.Fatalf("the reader's own view after switching: %+v", got)
	}
	if err := b.Cast(bg, r, 7, Up); err != nil {
		t.Fatal(err)
	}
	if got := tally(t, b, r); got != (Tally{Likes: 1, Dislikes: 0}) {
		t.Fatalf("down then up: %+v", got)
	}
	if n := rows(t, st); n != 1 {
		t.Fatalf("%d rows after switching, want 1", n)
	}
	var created, updated int64
	if err := st.DB().QueryRow(`SELECT created_at, updated_at FROM votes`).Scan(&created, &updated); err != nil {
		t.Fatal(err)
	}
	if created != epoch.Unix() || updated != epoch.Unix() {
		t.Fatalf("times are unix seconds from the clock: created %d updated %d", created, updated)
	}
}

// Withdrawing when there is nothing to withdraw is not an error, and neither
// is withdrawing twice.
func TestWithdrawWithoutVote(t *testing.T) {
	b, st := book(t)
	addUser(t, st, 7)
	addUser(t, st, 8)
	r := addReport(t, st, "x", "published")

	if err := b.Withdraw(bg, r, 7); err != nil {
		t.Fatalf("withdraw without ever voting: %v", err)
	}
	if err := b.Cast(bg, r, 7, Up); err != nil {
		t.Fatal(err)
	}
	if err := b.Cast(bg, r, 8, Up); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := b.Withdraw(bg, r, 7); err != nil {
			t.Fatalf("withdraw %d: %v", i+1, err)
		}
	}
	if got := public(t, b, r, 7); got != (PublicView{Likes: 1, Mine: ""}) {
		t.Fatalf("after withdrawing: %+v, want the other reader's like only", got)
	}
	// Withdrawing from a report that does not exist is the same quiet no-op.
	if err := b.Withdraw(bg, 9999, 7); err != nil {
		t.Fatalf("withdraw from a missing report: %v", err)
	}
	if n := rows(t, st); n != 1 {
		t.Fatalf("%d rows, want the other reader's 1", n)
	}
}

// One vote per reader per report: a key on the reader alone, or on the report
// alone, would overwrite the wrong row.
func TestVotesScopedPerReaderAndReport(t *testing.T) {
	b, st := book(t)
	addUser(t, st, 7)
	addUser(t, st, 8)
	a := addReport(t, st, "a", "published")
	z := addReport(t, st, "z", "published")

	for _, c := range []struct {
		report, reader int64
		kind           Kind
	}{{a, 7, Up}, {a, 8, Up}, {z, 7, Up}, {z, 8, Down}} {
		if err := b.Cast(bg, c.report, c.reader, c.kind); err != nil {
			t.Fatal(err)
		}
	}
	if got := tally(t, b, a); got != (Tally{Likes: 2, Dislikes: 0}) {
		t.Fatalf("two readers on one report: %+v, want 2 likes", got)
	}
	if got := tally(t, b, z); got != (Tally{Likes: 1, Dislikes: 1}) {
		t.Fatalf("one reader's like on a second report: %+v", got)
	}
	// Reader 7's two likes count once on each report, and 7 changing one leaves
	// the other alone.
	if err := b.Cast(bg, a, 7, Down); err != nil {
		t.Fatal(err)
	}
	if got := public(t, b, z, 7); got != (PublicView{Likes: 1, Mine: "up"}) {
		t.Fatalf("report z after 7 changed a: %+v", got)
	}
	if got := public(t, b, a, 8); got != (PublicView{Likes: 1, Mine: "up"}) {
		t.Fatalf("report a seen by 8: %+v", got)
	}
	if n := rows(t, st); n != 4 {
		t.Fatalf("%d rows, want one per reader per report (4)", n)
	}
}

// Anonymous (viewer 0) never owns a vote, and Mine is only ever the viewer's.
func TestPublicAnonymousHasNoVote(t *testing.T) {
	b, st := book(t)
	addUser(t, st, 7)
	r := addReport(t, st, "x", "published")
	if err := b.Cast(bg, r, 7, Down); err != nil {
		t.Fatal(err)
	}
	if got := public(t, b, r, 0); got != (PublicView{}) {
		t.Fatalf("anonymous view of a report holding only a dislike: %+v", got)
	}
	if got := public(t, b, r, 8); got != (PublicView{}) {
		t.Fatalf("another reader's view: %+v", got)
	}
}

// Cast on a report that is a draft or no longer exists changes nothing and says
// so, and a kind that is not up or down never reaches SQL.
func TestCastRefusesUnpublished(t *testing.T) {
	b, st := book(t)
	addUser(t, st, 7)
	draft := addReport(t, st, "draft", "draft")
	live := addReport(t, st, "live", "published")

	for name, id := range map[string]int64{"draft": draft, "missing": 9999} {
		if err := b.Cast(bg, id, 7, Up); !errors.Is(err, ErrNotOpen) {
			t.Errorf("%s: Cast = %v, want ErrNotOpen", name, err)
		}
	}
	if n := rows(t, st); n != 0 {
		t.Fatalf("%d vote rows attached to a hidden report", n)
	}

	// Control: it is the status that refuses, not the reader or the SQL.
	if err := b.Cast(bg, live, 7, Up); err != nil {
		t.Fatalf("control: %v", err)
	}
	if _, err := st.DB().Exec(`UPDATE reports SET status = 'draft' WHERE id = ?`, live); err != nil {
		t.Fatal(err)
	}
	if err := b.Cast(bg, live, 7, Down); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("a vote on a report unpublished after the reader's page was drawn: %v, want ErrNotOpen", err)
	}
	if got := tally(t, b, live); got != (Tally{Likes: 1, Dislikes: 0}) {
		t.Fatalf("the refused switch changed the kept vote: %+v", got)
	}
}

func TestCastRefusesUnknownKind(t *testing.T) {
	b, st := book(t)
	addUser(t, st, 7)
	r := addReport(t, st, "x", "published")
	for _, k := range []Kind{"", "none", "sideways", "UP", "up "} {
		err := b.Cast(bg, r, 7, k)
		if err == nil || errors.Is(err, ErrNotOpen) {
			t.Errorf("Cast(%q) = %v, want a refusal that is not ErrNotOpen", string(k), err)
		}
	}
	if n := rows(t, st); n != 0 {
		t.Fatalf("%d rows written for a refused kind", n)
	}
}

// A report's votes follow its deletion and a reader's deletion: the foreign keys
// are enforced on the serving pool.
func TestVotesCascade(t *testing.T) {
	b, st := book(t)
	addUser(t, st, 7)
	addUser(t, st, 8)
	a := addReport(t, st, "a", "published")
	z := addReport(t, st, "z", "published")
	for _, id := range []int64{7, 8} {
		for _, r := range []int64{a, z} {
			if err := b.Cast(bg, r, id, Up); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := st.DB().Exec(`DELETE FROM reports WHERE id = ?`, a); err != nil {
		t.Fatal(err)
	}
	if n := rows(t, st); n != 2 {
		t.Fatalf("%d rows after deleting a report with 2 votes, want 2 left", n)
	}
	if _, err := st.DB().Exec(`DELETE FROM users WHERE github_id = 7`); err != nil {
		t.Fatal(err)
	}
	if n := rows(t, st); n != 1 {
		t.Fatalf("%d rows after deleting a reader with 1 vote, want 1 left", n)
	}
}

// A closed store is an error, never a zero.
func TestReadsFailLoudly(t *testing.T) {
	b, st := book(t)
	r := addReport(t, st, "x", "published")
	st.Close()
	if _, err := b.Public(bg, r, 0); err == nil {
		t.Error("Public on a closed store returned no error")
	}
	if _, err := b.OwnerTally(bg, r); err == nil {
		t.Error("OwnerTally on a closed store returned no error")
	}
	if err := b.Cast(bg, r, 7, Up); err == nil || errors.Is(err, ErrNotOpen) {
		t.Errorf("Cast on a closed store: %v", err)
	}
	if err := b.Withdraw(bg, r, 7); err == nil {
		t.Error("Withdraw on a closed store returned no error")
	}
}

// TestConcurrentVotesNoBusy is the concurrency spike, run against the real
// Cast and Withdraw: 50 readers cast, switch and withdraw at the same moment as
// 10 goroutines sign in, on the pool exactly as store.Open ships it. Cast is
// INSERT ... SELECT ... ON CONFLICT, a shape the spike did not measure; if any
// call comes back "database is locked" or SQLITE_BUSY, or the final count is
// not 50, that shape has to change.
func TestConcurrentVotesNoBusy(t *testing.T) {
	const voters, signers, rounds = 50, 10, 12
	for run := 1; run <= 2; run++ {
		b, st := book(t)
		r := addReport(t, st, "x", "published")
		for id := int64(1); id <= voters; id++ {
			addUser(t, st, id)
		}

		var (
			wg    sync.WaitGroup
			mu    sync.Mutex
			fails []string
		)
		fail := func(who string, err error) {
			mu.Lock()
			defer mu.Unlock()
			fails = append(fails, who+": "+err.Error())
		}
		start := make(chan struct{})
		for id := int64(1); id <= voters; id++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for i := 0; i < rounds; i++ {
					for _, step := range []func() error{
						func() error { return b.Cast(bg, r, id, Up) },
						func() error { return b.Cast(bg, r, id, Down) },
						func() error { return b.Withdraw(bg, r, id) },
					} {
						if err := step(); err != nil {
							fail(fmt.Sprintf("voter %d", id), err)
						}
					}
				}
				if err := b.Cast(bg, r, id, Up); err != nil {
					fail(fmt.Sprintf("voter %d final", id), err)
				}
			}()
		}
		for j := 0; j < signers; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for k := 0; k < rounds; k++ {
					err := st.SignIn(bg, store.SignInArgs{
						User:         store.User{GitHubID: int64(1 + (j+k)%signers), Login: fmt.Sprintf("reader%d", 1+(j+k)%signers)},
						NewTokenHash: fmt.Sprintf("hash-%d-%d-%d", run, j, k),
						ExpiresAt:    epoch.Add(time.Hour),
						Now:          epoch,
					})
					if err != nil {
						fail(fmt.Sprintf("sign-in %d", j), err)
					}
				}
			}()
		}
		close(start)
		wg.Wait()

		for _, f := range fails {
			if strings.Contains(strings.ToLower(f), "lock") || strings.Contains(strings.ToLower(f), "busy") {
				t.Errorf("run %d: %s", run, f)
			}
		}
		if len(fails) > 0 {
			t.Fatalf("run %d: %d of the calls failed, first: %s", run, len(fails), fails[0])
		}
		if n := rows(t, st); n != voters {
			t.Fatalf("run %d: %d vote rows, want %d (one per reader)", run, n, voters)
		}
		if got := tally(t, b, r); got != (Tally{Likes: voters}) {
			t.Fatalf("run %d: %+v", run, got)
		}
	}
}
