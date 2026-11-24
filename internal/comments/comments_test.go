package comments

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/sshubham2/hband/internal/reports"
	"github.com/sshubham2/hband/internal/store"
)

var ctx = context.Background()

// fixedNow is the clock of every test here: the order of a thread never depends
// on it, so it need not move.
func fixedNow() time.Time { return time.Unix(1_800_000_000, 0) }

// testOwner is the pinned owner's id of every book here: no fixture account has it.
const testOwner = 9000

// fx is a real store on a temp file with a Book over its serving pool.
type fx struct {
	t    *testing.T
	st   *store.Store
	book *Book
}

func newFx(t *testing.T) *fx {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hband.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return &fx{t: t, st: st, book: New(st.DB(), fixedNow, testOwner)}
}

func (f *fx) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.st.DB().Exec(q, args...); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
}

func (f *fx) scalar(q string, args ...any) int64 {
	f.t.Helper()
	var n int64
	if err := f.st.DB().QueryRow(q, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (f *fx) user(id int64, login string) {
	f.t.Helper()
	f.exec(`INSERT INTO users (github_id, login, avatar_url, created_at, updated_at) VALUES (?, ?, ?, 1, 1)`,
		id, login, "https://avatars.githubusercontent.com/u/"+login+"?v=4")
}

func (f *fx) report(slug, status string) int64 {
	f.t.Helper()
	res, err := f.st.DB().Exec(`INSERT INTO reports (slug, title, status, published_at, size_bytes, sha256, created_at, updated_at)
		VALUES (?, ?, ?, 1, 10, 'x', 1, 1)`, slug, slug, status)
	if err != nil {
		f.t.Fatalf("insert report %s: %v", slug, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// post stores a comment through Post and fails the test unless it is taken.
func (f *fx) post(report, who, parent int64, body string) int64 {
	f.t.Helper()
	id, err := f.book.Post(ctx, report, who, parent, body)
	if err != nil {
		f.t.Fatalf("Post(report %d, by %d, parent %d, %q): %v", report, who, parent, body, err)
	}
	return id
}

func (f *fx) count(where string, args ...any) int64 {
	f.t.Helper()
	return f.scalar(`SELECT count(*) FROM comments WHERE `+where, args...)
}

func sqliteCode(err error) int {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code()
	}
	return 0
}

const sqliteConstraintForeignKey = 787

// TestSchema is the regression for the spike's measured answers: the composite
// parent key skips a NULL parent, refuses a parent on another report or a
// parent that does not exist with code 787, and raises no "foreign key
// mismatch" or foreign key violation of its own.
func TestSchema(t *testing.T) {
	f := newFx(t)
	if fk := f.scalar("PRAGMA foreign_keys"); fk != 1 {
		t.Fatalf("foreign_keys = %d on the serving pool: the checks below would mean nothing", fk)
	}
	if v := f.scalar("PRAGMA user_version"); v < 4 {
		t.Fatalf("user_version = %d, want at least 4", v)
	}
	f.user(1001, "writer")
	a, b := f.report("a", "published"), f.report("b", "published")
	ins := func(report int64, parent any) (int64, error) {
		res, err := f.st.DB().Exec(`INSERT INTO comments (report_id, parent_id, github_id, body, created_at) VALUES (?, ?, 1001, 'x', 1)`, report, parent)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}

	p, err := ins(a, nil)
	if err != nil {
		t.Fatalf("(a) a top-level comment with a NULL parent: %v", err)
	}
	if _, err := ins(a, p); err != nil {
		t.Fatalf("(b) a reply on the same report: %v", err)
	}
	q, err := ins(b, nil)
	if err != nil {
		t.Fatalf("(b2) a top-level comment on the second report: %v", err)
	}
	if _, err := ins(b, q); err != nil {
		t.Fatalf("(b2) a reply on the second report: %v", err)
	}
	if n := f.count("1 = 1"); n != 4 {
		t.Fatalf("%d comments, want 4", n)
	}

	_, err = ins(b, p)
	if err == nil || sqliteCode(err) != sqliteConstraintForeignKey || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Errorf("(c) a raw cross-report reply: %v (code %d), want FOREIGN KEY constraint failed, 787", err, sqliteCode(err))
	}
	_, err = ins(a, 99999)
	if err == nil || sqliteCode(err) != sqliteConstraintForeignKey {
		t.Errorf("(d) a reply to a comment that does not exist: %v (code %d), want 787", err, sqliteCode(err))
	}
	if n := f.count("1 = 1"); n != 4 {
		t.Errorf("%d comments after the refused inserts, want 4", n)
	}
	rows, err := f.st.DB().Query("PRAGMA foreign_key_check(comments)")
	if err != nil {
		t.Fatalf("(e) foreign_key_check: %v", err)
	}
	if rows.Next() {
		t.Error("(e) foreign_key_check(comments) returned a row")
	}
	rows.Close()

	// A comment that has replies is never deleted: the schema refuses, so
	// removal is always the removed_at flag.
	if _, err := f.st.DB().Exec(`DELETE FROM comments WHERE id = ?`, p); sqliteCode(err) != sqliteConstraintForeignKey {
		t.Errorf("deleting a comment that has a reply: %v, want 787", err)
	}
	// A reader who owns comments cannot be deleted either.
	if _, err := f.st.DB().Exec(`DELETE FROM users WHERE github_id = 1001`); sqliteCode(err) != sqliteConstraintForeignKey {
		t.Errorf("deleting a user who has comments: %v, want 787", err)
	}
	// An empty body is refused by the table too.
	if _, err := f.st.DB().Exec(`INSERT INTO comments (report_id, github_id, body, created_at) VALUES (?, 1001, '', 1)`, a); err == nil {
		t.Error("the table accepted an empty body")
	}
	// blocked_accounts: positive ids only, and no key to users: a block is a fact
	// about an account that outlives its row.
	if _, err := f.st.DB().Exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (0, 1)`); err == nil {
		t.Error("blocked_accounts accepted id 0")
	}
	if _, err := f.st.DB().Exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (5555, 1)`); err != nil {
		t.Errorf("blocked_accounts refused an account with no users row: %v", err)
	}
}

// TestDeleteReportWithDeepChain deletes a report that holds a reply chain far
// deeper than SQLite's trigger depth through the library's own Delete, and
// checks that nothing of it is left, that the interleaved comments of another
// report are untouched, and that the foreign key is live (the control).
func TestDeleteReportWithDeepChain(t *testing.T) {
	for _, depth := range []int{1500, 10000} {
		f := newFx(t)
		lib, err := reports.Open(reports.Options{DB: f.st.DB(), DataDir: t.TempDir(), MaxUploadBytes: 1 << 20, Now: fixedNow})
		if err != nil {
			t.Fatal(err)
		}
		f.user(1001, "writer")
		a, c := f.report("a", "published"), f.report("c", "published")

		tx, err := f.st.DB().Begin()
		if err != nil {
			t.Fatal(err)
		}
		stmt, err := tx.Prepare(`INSERT INTO comments (report_id, parent_id, github_id, body, created_at) VALUES (?, ?, 1001, 'x', 1)`)
		if err != nil {
			t.Fatal(err)
		}
		var parent, cParent any
		var firstC int64
		for i := 1; i <= depth; i++ {
			res, err := stmt.Exec(a, parent)
			if err != nil {
				t.Fatalf("depth %d: %v", i, err)
			}
			parent, _ = res.LastInsertId()
			if i%(depth/6) == 0 {
				res, err := stmt.Exec(c, cParent)
				if err != nil {
					t.Fatal(err)
				}
				id, _ := res.LastInsertId()
				if firstC == 0 {
					firstC = id
				}
				cParent = id
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		f.exec(`INSERT INTO votes (report_id, github_id, kind, created_at, updated_at) VALUES (?, 1001, 'up', 1, 1)`, a)
		if n := f.count("report_id = ?", a); n != int64(depth) {
			t.Fatalf("seeded %d comments on a, want %d", n, depth)
		}
		cCount := f.count("report_id = ?", c)
		if cCount != 6 {
			t.Fatalf("seeded %d comments on c, want 6", cCount)
		}
		control := func(when string) {
			t.Helper()
			if _, err := f.st.DB().Exec(`DELETE FROM comments WHERE id = ?`, firstC); sqliteCode(err) != sqliteConstraintForeignKey {
				t.Fatalf("control %s: deleting a comment with a reply: %v, want 787 (the foreign key is inert, so a 0-row result means nothing)", when, err)
			}
		}
		control("before")

		start := time.Now()
		if err := lib.Delete(ctx, "a"); err != nil {
			t.Fatalf("depth %d: Delete: %v", depth, err)
		}
		t.Logf("depth %d: Delete took %s", depth, time.Since(start))
		if n := f.count("report_id = ?", a); n != 0 {
			t.Errorf("depth %d: %d comments of the deleted report remain", depth, n)
		}
		if n := f.count("report_id = ?", c); n != cCount {
			t.Errorf("depth %d: the other report has %d comments, want %d", depth, n, cCount)
		}
		if n := f.scalar(`SELECT count(*) FROM votes`); n != 0 {
			t.Errorf("depth %d: %d votes remain", depth, n)
		}
		if f.scalar(`SELECT count(*) FROM pragma_foreign_key_check`) != 0 {
			t.Errorf("depth %d: foreign_key_check reports violations", depth)
		}
		control("after")
	}
}

func TestNormalize(t *testing.T) {
	bad := ErrBadBody
	for _, c := range []struct {
		name, in, want string
		err            error
	}{
		{"plain", "hello", "hello", nil},
		{"trimmed", "  hi \n", "hi", nil},
		{"CRLF becomes LF", "a\r\nb", "a\nb", nil},
		{"a lone CR becomes LF", "a\rb", "a\nb", nil},
		{"a lone CR then LF then CR", "a\r\n\rb", "a\n\nb", nil},
		{"tab and newline inside are kept", "a\tb\nc", "a\tb\nc", nil},
		{"CRLF at both ends is trimmed", "\r\nhi\r\n", "hi", nil},
		{"markup is stored as typed", `<b>x</b> & "q" {{.}}`, `<b>x</b> & "q" {{.}}`, nil},
		{"4-byte runes", "😀 ok", "😀 ok", nil},
		{"empty", "", "", bad},
		{"white space only", "  \n\t ", "", bad},
		{"a no-break space only", " ", "", bad},
		{"NUL", "a\x00b", "", bad},
		{"C0 SOH", "a\x01b", "", bad},
		{"C0 ESC", "a\x1bb", "", bad},
		{"form feed", "a\fb", "", bad},
		{"vertical tab", "a\vb", "", bad},
		{"DEL", "a\x7fb", "", bad},
		{"C1 NEL U+0085", "a\u0085b", "", bad},
		{"C1 U+009F", "a\u009fb", "", bad},
		{"C1 U+0080", "a\u0080b", "", bad},
		{"invalid UTF-8", "a\xffb", "", bad},
		{"a truncated rune", "ab\xc3", "", bad},
		{"exactly MaxRunes", strings.Repeat("a", MaxRunes), strings.Repeat("a", MaxRunes), nil},
		{"one over MaxRunes", strings.Repeat("a", MaxRunes+1), "", bad},
		{"exactly MaxRunes 4-byte runes", strings.Repeat("😀", MaxRunes), strings.Repeat("😀", MaxRunes), nil},
		{"one over MaxRunes 4-byte runes", strings.Repeat("😀", MaxRunes+1), "", bad},
		{"the length is counted after CRLF becomes LF", strings.Repeat("a\r\n", MaxRunes/2), strings.TrimSpace(strings.Repeat("a\n", MaxRunes/2)), nil},
		{"the length is counted after trimming", strings.Repeat(" ", 10) + strings.Repeat("a", MaxRunes) + strings.Repeat(" ", 10), strings.Repeat("a", MaxRunes), nil},
	} {
		got, err := Normalize(c.in)
		if !errors.Is(err, c.err) || got != c.want {
			t.Errorf("%s: Normalize = %q, %v; want %q, %v", c.name, got, err, c.want, c.err)
		}
	}
}

// TestPostRefusals feeds Post every shape that cannot be stored: each must
// return the one error for its kind and leave the table as it was.
func TestPostRefusals(t *testing.T) {
	f := newFx(t)
	f.user(1, "writer")
	f.user(2, "other")
	pub, other, draft := f.report("pub", "published"), f.report("other", "published"), f.report("draft", "draft")
	top := f.post(pub, 1, 0, "top")
	otherTop := f.post(other, 1, 0, "top on another report")
	removed := f.post(pub, 1, 0, "to be removed")
	f.exec(`UPDATE comments SET removed_at = 5 WHERE id = ?`, removed)
	byBlocked := f.post(pub, 2, 0, "by an account blocked later")
	f.exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (2, 5)`)
	before := f.count("1 = 1")

	for _, c := range []struct {
		name   string
		report int64
		who    int64
		parent int64
		body   string
		want   error
	}{
		{"an unknown report", 9999, 1, 0, "x", ErrNoTarget},
		{"a draft report", draft, 1, 0, "x", ErrNoTarget},
		{"a reply under a draft report", draft, 1, top, "x", ErrNoTarget},
		{"a parent that does not exist", pub, 1, 9999, "x", ErrNoTarget},
		{"a parent on another report", pub, 1, otherTop, "x", ErrNoTarget},
		{"a removed parent", pub, 1, removed, "x", ErrNoTarget},
		{"a parent written by a blocked account", pub, 1, byBlocked, "x", ErrNoTarget},
		{"a negative parent", pub, 1, -1, "x", ErrNoTarget},
		{"an empty body", pub, 1, 0, "", ErrBadBody},
		{"an invalid body under a missing report", 9999, 1, 0, "a\x00b", ErrBadBody},
	} {
		if _, err := f.book.Post(ctx, c.report, c.who, c.parent, c.body); !errors.Is(err, c.want) {
			t.Errorf("%s: Post = %v, want %v", c.name, err, c.want)
		}
	}
	if n := f.count("1 = 1"); n != before {
		t.Errorf("%d comments after the refusals, want %d", n, before)
	}

	// The report is unpublished after the caller looked it up and before the
	// write: the statement itself says no.
	f.exec(`UPDATE reports SET status = 'draft' WHERE id = ?`, pub)
	if _, err := f.book.Post(ctx, pub, 1, 0, "late"); !errors.Is(err, ErrNoTarget) {
		t.Errorf("a report unpublished between the lookup and the write: Post = %v, want ErrNoTarget", err)
	}
	if _, err := f.book.Post(ctx, pub, 1, top, "late reply"); !errors.Is(err, ErrNoTarget) {
		t.Errorf("a reply on a report unpublished between the lookup and the write: Post = %v, want ErrNoTarget", err)
	}
	f.exec(`UPDATE reports SET status = 'published' WHERE id = ?`, pub)
	if n := f.count("1 = 1"); n != before {
		t.Errorf("%d comments after the late posts, want %d", n, before)
	}

	// Controls: the same calls are taken when nothing is wrong, at any depth, and
	// the body is stored as Normalize left it.
	id1 := f.post(pub, 1, 0, "a\r\nb")
	id2 := f.post(pub, 1, id1, "reply")
	id3 := f.post(pub, 1, id2, "reply to a reply")
	if !(id1 < id2 && id2 < id3) {
		t.Errorf("ids %d, %d, %d are not rising", id1, id2, id3)
	}
	var body string
	if err := f.st.DB().QueryRow(`SELECT body FROM comments WHERE id = ?`, id1).Scan(&body); err != nil || body != "a\nb" {
		t.Errorf("stored body %q, %v; want a\\nb", body, err)
	}
	// An unblocked account can be answered again.
	f.exec(`DELETE FROM blocked_accounts WHERE github_id = 2`)
	f.post(pub, 1, byBlocked, "now answerable")

	// A body that fails the rules is refused before any SQL runs: on a store that
	// is closed it is still ErrBadBody, and a good body shows the store is closed.
	dead := newFx(t)
	dead.st.Close()
	if _, err := dead.book.Post(ctx, 1, 1, 0, ""); !errors.Is(err, ErrBadBody) {
		t.Errorf("a bad body on a closed store: %v, want ErrBadBody (no SQL should run)", err)
	}
	if _, err := dead.book.Post(ctx, 1, 1, 0, "fine"); err == nil || errors.Is(err, ErrBadBody) || errors.Is(err, ErrNoTarget) {
		t.Errorf("a good body on a closed store: %v, want the driver's error", err)
	}

	// A writer with no users row is the driver's refusal, not "nothing to comment on".
	if _, err := f.book.Post(ctx, pub, 777, 0, "ghost"); err == nil || errors.Is(err, ErrNoTarget) || errors.Is(err, ErrBadBody) {
		t.Errorf("a writer with no account: %v, want the foreign key error", err)
	}
}

// TestConcurrentPostsNoBusy posts from 16 goroutines at once through the
// serving pool. Single statements wait on busy_timeout, so none may fail with
// SQLITE_BUSY, none may be lost, ids stay unique and a reply's parent id is
// always the smaller.
func TestConcurrentPostsNoBusy(t *testing.T) {
	f := newFx(t)
	f.user(1, "writer")
	rep := f.report("pub", "published")
	var seeds []int64
	for i := 0; i < 4; i++ {
		seeds = append(seeds, f.post(rep, 1, 0, "seed"))
	}
	const workers, each = 16, 25
	var wg sync.WaitGroup
	errs := make(chan error, workers*each)
	ids := make(chan int64, workers*each)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				parent := int64(0)
				if i%2 == 1 {
					parent = seeds[(w+i)%len(seeds)]
				}
				id, err := f.book.Post(ctx, rep, 1, parent, "concurrent")
				if err != nil {
					errs <- err
					continue
				}
				ids <- id
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		t.Errorf("a concurrent post failed: %v", err)
	}
	seen := map[int64]bool{}
	for id := range ids {
		if seen[id] {
			t.Errorf("id %d was handed out twice", id)
		}
		seen[id] = true
	}
	if len(seen) != workers*each {
		t.Errorf("%d posts stored, want %d", len(seen), workers*each)
	}
	if n := f.count("parent_id IS NOT NULL AND parent_id >= id"); n != 0 {
		t.Errorf("%d replies have a parent id that is not smaller than their own", n)
	}
	if n := f.count("1 = 1"); n != int64(len(seeds)+workers*each) {
		t.Errorf("%d rows in the table, want %d", n, len(seeds)+workers*each)
	}
}
