package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// A deployed database at version 2 (identity and reports) or at version 3 (plus
// votes) takes the comments migration without losing a row. A version 2 file
// runs 0003 and 0004 in one start, which is what the first deploy does if the
// production database never reached version 3. Afterwards the new tables enforce
// their keys on the serving pool, and a database newer than the binary is still
// refused.
func TestCommentsMigrationFromV2AndV3(t *testing.T) {
	migrationFiles := []string{"0001_identity.sql", "0002_reports.sql", "0003_votes.sql"}
	for _, from := range []int{2, 3} {
		t.Run(fmt.Sprintf("from version %d", from), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hband.db")
			old := fstest.MapFS{}
			for _, name := range migrationFiles[:from] {
				b, err := embeddedFS.ReadFile("migrations/" + name)
				if err != nil {
					t.Fatal(err)
				}
				old[name] = &fstest.MapFile{Data: b}
			}
			s, err := openWith(path, old)
			if err != nil {
				t.Fatalf("open at version %d: %v", from, err)
			}
			if err := signIn(t, s, 7, "seven", "h1", "", time.Unix(1_700_000_000, 0)); err != nil {
				t.Fatal(err)
			}
			for _, slug := range []string{"x", "y"} {
				if _, err := s.DB().Exec(`INSERT INTO reports (slug, title, status, published_at, size_bytes, sha256, created_at, updated_at)
					VALUES (?, ?, 'published', 1, 10, 'x', 1, 1)`, slug, slug); err != nil {
					t.Fatal(err)
				}
			}
			want := map[string]int64{"users": 1, "sessions": 1, "reports": 2}
			if from >= 3 {
				if _, err := s.DB().Exec(`INSERT INTO votes (report_id, github_id, kind, created_at, updated_at) VALUES (1, 7, 'up', 1, 1)`); err != nil {
					t.Fatal(err)
				}
				want["votes"] = 1
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
			if v, newest := scalarInt(t, s.DB(), "PRAGMA user_version"), newestVersion(t); v != newest || v < 4 {
				t.Fatalf("user_version = %d, want %d (at least 4)", v, newest)
			}
			for table, n := range want {
				if got := scalarInt(t, s.DB(), "SELECT count(*) FROM "+table); got != n {
					t.Errorf("%s has %d rows after the migration, want %d", table, got, n)
				}
			}
			if login := scalarString(t, s.DB(), "SELECT login FROM users WHERE github_id = 7"); login != "seven" {
				t.Errorf("the reader's login is %q after the migration", login)
			}
			for table, n := range map[string]int64{"comments": 0, "blocked_accounts": 0} {
				if got := scalarInt(t, s.DB(), "SELECT count(*) FROM "+table); got != n {
					t.Errorf("the new table %s has %d rows, want %d", table, got, n)
				}
			}
			if n := scalarInt(t, s.DB(), "SELECT count(*) FROM pragma_foreign_key_check"); n != 0 {
				t.Errorf("foreign_key_check reports %d violations", n)
			}

			// The keys hold on the serving pool.
			insert := `INSERT INTO comments (report_id, parent_id, github_id, body, created_at) VALUES (?, ?, ?, 'x', 1)`
			if _, err := s.DB().Exec(insert, 1, nil, 7); err != nil {
				t.Fatalf("a top-level comment: %v", err)
			}
			if _, err := s.DB().Exec(insert, 1, 1, 7); err != nil {
				t.Fatalf("a reply: %v", err)
			}
			for name, args := range map[string][]any{
				"a reply on another report than its parent's": {2, 1, 7},
				"a reply to a comment that does not exist":    {1, 99, 7},
				"a comment by a reader who does not exist":    {1, nil, 99},
				"a comment on a report that does not exist":   {99, nil, 7},
			} {
				if _, err := s.DB().Exec(insert, args...); err == nil {
					t.Errorf("the schema accepted %s", name)
				}
			}
			if _, err := s.DB().Exec(`DELETE FROM reports WHERE id = 1`); err != nil {
				t.Fatal(err)
			}
			if n := scalarInt(t, s.DB(), "SELECT count(*) FROM comments"); n != 0 {
				t.Errorf("%d comments survive the deletion of their report", n)
			}
			if n := scalarInt(t, s.DB(), "SELECT count(*) FROM sessions"); n != 1 {
				t.Errorf("deleting a report removed %d sessions", 1-n)
			}

			// A binary that knows fewer versions than the file refuses it.
			if _, err := s.DB().Exec(fmt.Sprintf("PRAGMA user_version = %d", newestVersion(t)+1)); err != nil {
				t.Fatal(err)
			}
			s.Close()
			if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "newer than this binary") {
				t.Errorf("Open on a database newer than the binary: %v", err)
			}
		})
	}
}
