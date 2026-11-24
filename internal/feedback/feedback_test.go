package feedback

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sshubham2/hband/internal/comments"
	"github.com/sshubham2/hband/internal/store"
)

var bg = context.Background()

// clock is a settable time source for the book under test.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// world is a store with two users, a published report (id 1), a draft (id 2),
// another published report (id 3), and a book on a clock the test controls.
type world struct {
	db   *sql.DB
	book *Book
	clk  *clock
}

func newWorld(t *testing.T) *world {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hband.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Unix(1_800_000_000, 0)
	for _, u := range []store.User{
		{GitHubID: 7, Login: "reader-seven", AvatarURL: "https://avatars.githubusercontent.com/u/7?v=4"},
		{GitHubID: 8, Login: "reader-eight"},
	} {
		if err := st.SignIn(bg, store.SignInArgs{User: u, NewTokenHash: "h" + u.Login, ExpiresAt: now.Add(time.Hour), Now: now}); err != nil {
			t.Fatalf("sign in %d: %v", u.GitHubID, err)
		}
	}
	for i, r := range []struct{ slug, status string }{{"x", "published"}, {"d", "draft"}, {"y", "published"}} {
		if _, err := st.DB().Exec(`INSERT INTO reports (slug, title, status, published_at, size_bytes, sha256, created_at, updated_at)
			VALUES (?, ?, ?, 1, 10, 'x', 1, 1)`, r.slug, r.slug, r.status); err != nil {
			t.Fatalf("report %d: %v", i+1, err)
		}
	}
	clk := &clock{t: now}
	return &world{db: st.DB(), book: New(st.DB(), clk.now), clk: clk}
}

func (w *world) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := w.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestSendAndInbox(t *testing.T) {
	w := newWorld(t)
	if err := w.book.Send(bg, 1, 7, "  a message\r\nin two lines  "); err != nil {
		t.Fatalf("Send: %v", err)
	}
	msgs, err := w.book.OwnerInbox(bg, 1)
	if err != nil {
		t.Fatalf("OwnerInbox: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("%d messages, want 1", len(msgs))
	}
	m := msgs[0]
	if m.ID != 1 || m.GitHubID != 7 || m.Login != "reader-seven" || m.AvatarURL != "https://avatars.githubusercontent.com/u/7?v=4" {
		t.Errorf("message %+v: wrong id or sender", m)
	}
	if m.Body != "a message\nin two lines" {
		t.Errorf("body %q, want the normalised text", m.Body)
	}
	if !m.When.Equal(w.clk.now()) {
		t.Errorf("When = %v, want the clock's %v", m.When, w.clk.now())
	}
	if n := w.count(t, "SELECT count(*) FROM comments"); n != 0 {
		t.Errorf("%d comments written by a feedback message", n)
	}
}

// Send returns only an error, so the public handler never holds a row id it
// could put in a Location. The assignment below fails to compile if Send grows
// a value to return.
func TestSendReturnsNoID(t *testing.T) {
	w := newWorld(t)
	var err error = w.book.Send(bg, 1, 7, "hello")
	if err != nil {
		t.Fatal(err)
	}
}

func TestSendNeedsPublishedReport(t *testing.T) {
	w := newWorld(t)
	for name, id := range map[string]int64{"a draft": 2, "a report that does not exist": 99, "a negative id": -1, "zero": 0} {
		if err := w.book.Send(bg, id, 7, "hello"); !errors.Is(err, ErrNoTarget) {
			t.Errorf("%s: Send = %v, want ErrNoTarget", name, err)
		}
	}
	if n := w.count(t, "SELECT count(*) FROM feedback"); n != 0 {
		t.Fatalf("%d rows written for reports that were not published", n)
	}
	// Unpublishing after a first message stops the next one, and keeps the first.
	if err := w.book.Send(bg, 1, 7, "before"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Exec(`UPDATE reports SET status = 'draft' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := w.book.Send(bg, 1, 7, "after"); !errors.Is(err, ErrNoTarget) {
		t.Errorf("Send to a report unpublished since = %v, want ErrNoTarget", err)
	}
	if n := w.count(t, "SELECT count(*) FROM feedback"); n != 1 {
		t.Errorf("%d rows, want only the one sent while it was published", n)
	}
}

func TestSendRefusedBodyRunsNoSQL(t *testing.T) {
	w := newWorld(t)
	// With the table gone, a body that is refused must still be ErrBadBody: the
	// refusal comes before any SQL. A body that is accepted reaches the missing
	// table and is some other error.
	if _, err := w.db.Exec(`ALTER TABLE feedback RENAME TO feedback_gone`); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"empty": "", "white space": " \n\t ", "NUL": "a\x00b", "DEL": "a\x7fb", "U+0085": "a\u0085b",
		"invalid UTF-8": "a\xffb", "5001 runes": strings.Repeat("a", 5001),
	} {
		if err := w.book.Send(bg, 1, 7, body); !errors.Is(err, ErrBadBody) {
			t.Errorf("%s: Send = %v, want ErrBadBody", name, err)
		}
	}
	err := w.book.Send(bg, 1, 7, strings.Repeat("a", 5000))
	if err == nil || errors.Is(err, ErrBadBody) || errors.Is(err, ErrNoTarget) {
		t.Errorf("an acceptable body with the table missing: %v, want a store error", err)
	}
	if !errors.Is(ErrBadBody, comments.ErrBadBody) || !errors.Is(comments.ErrBadBody, ErrBadBody) {
		t.Error("feedback.ErrBadBody is not comments.ErrBadBody")
	}
}

func TestInboxOrderAndScope(t *testing.T) {
	w := newWorld(t)
	for _, s := range []struct {
		report, who int64
		body        string
	}{
		{1, 7, "first on x"}, {3, 7, "only on y"}, {1, 8, "second on x"}, {1, 7, "third on x"},
	} {
		w.clk.t = w.clk.t.Add(time.Minute)
		if err := w.book.Send(bg, s.report, s.who, s.body); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := w.book.OwnerInbox(bg, 1)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range msgs {
		got = append(got, m.Body+"/"+m.Login)
	}
	if want := "third on x/reader-seven second on x/reader-eight first on x/reader-seven"; strings.Join(got, " ") != want {
		t.Errorf("inbox of x = %v, want newest first, only x's: %s", got, want)
	}
	if msgs[1].AvatarURL != "" {
		t.Errorf("the sender with no avatar has %q", msgs[1].AvatarURL)
	}
	if empty, err := w.book.OwnerInbox(bg, 99); err != nil || len(empty) != 0 {
		t.Errorf("inbox of a report with none = %v, %v", empty, err)
	}
	// A login that changes shows under its new name: the join reads users.
	if _, err := w.db.Exec(`UPDATE users SET login = 'renamed' WHERE github_id = 7`); err != nil {
		t.Fatal(err)
	}
	msgs, _ = w.book.OwnerInbox(bg, 1)
	if msgs[0].Login != "renamed" || msgs[2].Login != "renamed" || msgs[1].Login != "reader-eight" {
		t.Errorf("after a rename: %+v", msgs)
	}
}

// A stored time is unix seconds; it is read back in UTC whatever the machine's
// zone is, because the page prints a literal "UTC" after it. The zone is forced
// away from UTC here, so the test is not blind on a machine that already runs
// in UTC.
func TestInboxTimesAreUTC(t *testing.T) {
	saved := time.Local
	time.Local = time.FixedZone("test+5:30", 5*3600+1800)
	t.Cleanup(func() { time.Local = saved })

	w := newWorld(t)
	sentAt := time.Date(2027, 1, 15, 23, 45, 6, 0, time.UTC)
	w.clk.t = sentAt
	if err := w.book.Send(bg, 1, 7, "late"); err != nil {
		t.Fatal(err)
	}
	msgs, err := w.book.OwnerInbox(bg, 1)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("%v, %v", msgs, err)
	}
	m := msgs[0]
	if m.When.Location() != time.UTC {
		t.Errorf("When is in %v, want UTC", m.When.Location())
	}
	if !m.When.Equal(sentAt) {
		t.Errorf("When = %v, want %v", m.When, sentAt)
	}
	if got := m.When.Format("2006-01-02 15:04 UTC"); got != "2027-01-15 23:45 UTC" {
		t.Errorf("drawn as %q", got)
	}
}
