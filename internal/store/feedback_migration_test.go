package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// oldMigrations is the first n embedded migrations, as a binary of that age had
// them.
func oldMigrations(t *testing.T, n int) fstest.MapFS {
	t.Helper()
	names := []string{"0001_identity.sql", "0002_reports.sql", "0003_votes.sql", "0004_comments.sql"}
	old := fstest.MapFS{}
	for _, name := range names[:n] {
		b, err := embeddedFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		old[name] = &fstest.MapFile{Data: b}
	}
	return old
}

// A deployed database at version 4 (identity, reports, votes and comments) takes
// the feedback migration without losing a row, and the new tables enforce their
// keys on the serving pool: a message needs a report and a reader, a reply needs
// its message, and deleting a report removes its messages and their replies (two
// hops of ON DELETE CASCADE).
func TestFeedbackMigrationFromPopulatedV4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hband.db")
	s, err := openWith(path, oldMigrations(t, 4))
	if err != nil {
		t.Fatalf("open at version 4: %v", err)
	}
	for id, login := range map[int64]string{7: "seven", 9: "nine"} {
		if err := signIn(t, s, id, login, fmt.Sprintf("h%d", id), "", time.Unix(1_700_000_000, 0)); err != nil {
			t.Fatal(err)
		}
	}
	for _, slug := range []string{"x", "y"} {
		if _, err := s.DB().Exec(`INSERT INTO reports (slug, title, status, published_at, size_bytes, sha256, created_at, updated_at)
			VALUES (?, ?, 'published', 1, 10, 'x', 1, 1)`, slug, slug); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB().Exec(`INSERT INTO comments (report_id, parent_id, github_id, body, created_at) VALUES (1, NULL, 7, 'a comment', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO votes (report_id, github_id, kind, created_at, updated_at) VALUES (1, 7, 'up', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"users": 2, "sessions": 2, "reports": 2, "comments": 1, "votes": 1}
	if v := scalarInt(t, s.DB(), "PRAGMA user_version"); v != 4 {
		t.Fatalf("seed database at version %d, want 4", v)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatalf("Open on a version 4 database: %v", err)
	}
	defer s.Close()
	if v, newest := scalarInt(t, s.DB(), "PRAGMA user_version"), newestVersion(t); v != newest || v < 5 {
		t.Fatalf("user_version = %d, want %d (at least 5)", v, newest)
	}
	for table, n := range want {
		if got := scalarInt(t, s.DB(), "SELECT count(*) FROM "+table); got != n {
			t.Errorf("%s has %d rows after the migration, want %d", table, got, n)
		}
	}
	if body := scalarString(t, s.DB(), "SELECT body FROM comments WHERE id = 1"); body != "a comment" {
		t.Errorf("the v4 comment reads %q after the migration", body)
	}
	for _, table := range []string{"feedback", "feedback_replies"} {
		if got := scalarInt(t, s.DB(), "SELECT count(*) FROM "+table); got != 0 {
			t.Errorf("the new table %s has %d rows, want 0", table, got)
		}
	}
	if n := scalarInt(t, s.DB(), "SELECT count(*) FROM pragma_foreign_key_check"); n != 0 {
		t.Errorf("foreign_key_check reports %d violations", n)
	}

	// The keys hold on the serving pool.
	msg := `INSERT INTO feedback (report_id, github_id, body, created_at) VALUES (?, ?, 'hello', 1)`
	for name, args := range map[string][]any{
		"a message on a report that does not exist": {99, 7},
		"a message by a reader who does not exist":  {1, 99},
	} {
		if _, err := s.DB().Exec(msg, args...); err == nil {
			t.Errorf("the schema accepted %s", name)
		}
	}
	if _, err := s.DB().Exec(`INSERT INTO feedback_replies (feedback_id, body, created_at) VALUES (99, 'x', 1)`); err == nil {
		t.Error("the schema accepted a reply to a message that does not exist")
	}
	if _, err := s.DB().Exec(`INSERT INTO feedback (report_id, github_id, body, created_at) VALUES (1, 7, '', 1)`); err == nil {
		t.Error("the schema accepted an empty message")
	}
	if _, err := s.DB().Exec(`INSERT INTO feedback_replies (feedback_id, body, created_at) VALUES (1, '', 1)`); err == nil {
		t.Error("the schema accepted an empty reply")
	}
	// Report 2's message comes first, so the highest ids belong to report 1 and
	// go with it: only AUTOINCREMENT keeps the next id above them.
	for _, args := range [][]any{{2, 7}, {1, 7}, {1, 9}} {
		if _, err := s.DB().Exec(msg, args...); err != nil {
			t.Fatalf("a message %v: %v", args, err)
		}
	}
	for _, id := range []int{1, 2, 3} {
		if _, err := s.DB().Exec(`INSERT INTO feedback_replies (feedback_id, body, created_at) VALUES (?, 'reply', 1)`, id); err != nil {
			t.Fatalf("a reply to message %d: %v", id, err)
		}
	}
	// A reader's row cannot go while it owns a message: no action clause.
	if _, err := s.DB().Exec(`DELETE FROM users WHERE github_id = 9`); err == nil {
		t.Error("a reader who owns a message was deleted")
	}
	// The report goes, with its messages and, through them, their replies.
	if _, err := s.DB().Exec(`DELETE FROM reports WHERE id = 1`); err != nil {
		t.Fatalf("deleting a report that has messages and replies: %v", err)
	}
	if n := scalarInt(t, s.DB(), "SELECT count(*) FROM feedback WHERE report_id = 1"); n != 0 {
		t.Errorf("%d messages survive the deletion of their report", n)
	}
	if n := scalarInt(t, s.DB(), "SELECT count(*) FROM feedback"); n != 1 {
		t.Errorf("%d messages left, want only report 2's", n)
	}
	if n := scalarInt(t, s.DB(), "SELECT count(*) FROM feedback_replies"); n != 1 {
		t.Errorf("%d replies left, want only the one under report 2's message", n)
	}
	if n := scalarInt(t, s.DB(), "SELECT count(*) FROM pragma_foreign_key_check"); n != 0 {
		t.Errorf("foreign_key_check reports %d violations after the delete", n)
	}
	if n := scalarInt(t, s.DB(), "SELECT count(*) FROM sessions"); n != 2 {
		t.Errorf("deleting a report removed %d sessions", 2-n)
	}
	// AUTOINCREMENT: the ids of the deleted messages are not handed out again.
	if _, err := s.DB().Exec(msg, 2, 7); err != nil {
		t.Fatal(err)
	}
	if id := scalarInt(t, s.DB(), "SELECT max(id) FROM feedback"); id != 4 {
		t.Errorf("the next message has id %d, want 4 (ids are never reused)", id)
	}

	// A binary that knows fewer versions than the file refuses it.
	if _, err := s.DB().Exec(fmt.Sprintf("PRAGMA user_version = %d", newestVersion(t)+1)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Errorf("Open on a database newer than the binary: %v", err)
	}
}

// A database that has never held a row, at each version from 1 to 4, reaches the
// newest version in one start with an empty foreign_key_check and the feedback
// tables in place.
func TestFeedbackMigrationFromFreshVersions(t *testing.T) {
	for from := 1; from <= 4; from++ {
		t.Run(fmt.Sprintf("from version %d", from), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hband.db")
			s, err := openWith(path, oldMigrations(t, from))
			if err != nil {
				t.Fatalf("open at version %d: %v", from, err)
			}
			if v := scalarInt(t, s.DB(), "PRAGMA user_version"); v != int64(from) {
				t.Fatalf("seed database at version %d, want %d", v, from)
			}
			s.Close()

			s, err = Open(path)
			if err != nil {
				t.Fatalf("Open on a version %d database: %v", from, err)
			}
			defer s.Close()
			if v, newest := scalarInt(t, s.DB(), "PRAGMA user_version"), newestVersion(t); v != newest || v < 5 {
				t.Fatalf("user_version = %d, want %d (at least 5)", v, newest)
			}
			for _, table := range []string{"feedback", "feedback_replies"} {
				if n := scalarInt(t, s.DB(), "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table); n != 1 {
					t.Errorf("table %s is missing after the migration", table)
				}
			}
			for _, index := range []string{"feedback_report", "feedback_replies_by_feedback"} {
				if n := scalarInt(t, s.DB(), "SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?", index); n != 1 {
					t.Errorf("index %s is missing after the migration", index)
				}
			}
			if n := scalarInt(t, s.DB(), "SELECT count(*) FROM pragma_foreign_key_check"); n != 0 {
				t.Errorf("foreign_key_check reports %d violations", n)
			}
		})
	}
}
