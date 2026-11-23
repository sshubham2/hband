package store

import (
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

// A deployed database at version 2 (identity and reports, with a signed-in
// reader and a published report) takes 0003_votes.sql without losing a row of
// either, and the new table enforces its keys on the serving pool.
func TestVotesMigrationOnAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hband.db")

	upToReports := fstest.MapFS{}
	for _, name := range []string{"0001_identity.sql", "0002_reports.sql"} {
		b, err := embeddedFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		upToReports[name] = &fstest.MapFile{Data: b}
	}
	old, err := openWith(path, upToReports)
	if err != nil {
		t.Fatalf("open at version 2: %v", err)
	}
	if err := signIn(t, old, 7, "seven", "h1", "", time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := old.DB().Exec(`INSERT INTO reports (slug, title, status, published_at, size_bytes, sha256, created_at, updated_at)
		VALUES ('x', 'X', 'published', 1, 10, 'x', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if v := scalarInt(t, old.DB(), "PRAGMA user_version"); v != 2 {
		t.Fatalf("seed database at version %d, want 2", v)
	}
	old.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a version 2 database: %v", err)
	}
	defer s.Close()
	if v, want := scalarInt(t, s.DB(), "PRAGMA user_version"), newestVersion(t); v != want || v < 3 {
		t.Fatalf("user_version = %d, want %d (at least 3)", v, want)
	}
	for table, want := range map[string]int64{"users": 1, "sessions": 1, "reports": 1, "votes": 0} {
		if n := scalarInt(t, s.DB(), "SELECT count(*) FROM "+table); n != want {
			t.Errorf("%s has %d rows after the migration, want %d", table, n, want)
		}
	}

	// The keys hold: a vote needs a report and a reader, kind is up or down, and
	// one reader has one row per report.
	insert := `INSERT INTO votes (report_id, github_id, kind, created_at, updated_at) VALUES (?, ?, ?, 1, 1)`
	if _, err := s.DB().Exec(insert, 1, 7, "up"); err != nil {
		t.Fatalf("a valid vote: %v", err)
	}
	for name, args := range map[string][]any{
		"a second row for the same reader and report": {1, 7, "down"},
		"a report that does not exist":                {99, 7, "up"},
		"a reader that does not exist":                {1, 99, "up"},
		"a kind that is neither up nor down":          {1, 8, "sideways"},
	} {
		if _, err := s.DB().Exec(insert, args...); err == nil {
			t.Errorf("the schema accepted %s", name)
		}
	}
	if _, err := s.DB().Exec("DELETE FROM reports WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if n := scalarInt(t, s.DB(), "SELECT count(*) FROM votes"); n != 0 {
		t.Fatalf("%d votes survive the deletion of their report", n)
	}
	if n := scalarInt(t, s.DB(), "SELECT count(*) FROM sessions"); n != 1 {
		t.Fatalf("deleting a report removed %d sessions", 1-n)
	}
}
