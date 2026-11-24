package store

import (
	"bytes"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

// A deployed database at version 5 (reports, votes and comments, with readers who
// hold them) takes 0006_opens.sql without losing a row of anything, starts with
// no opens (nothing is backfilled), and the new table enforces its keys on the
// serving pool: a deleted report takes its opens with it, an unpublished one
// keeps them.
func TestMigrateFiveToSix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hband.db")

	upToFive := fstest.MapFS{}
	for _, name := range []string{"0001_identity.sql", "0002_reports.sql", "0003_votes.sql", "0004_comments.sql", "0005_feedback.sql"} {
		b, err := embeddedFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		upToFive[name] = &fstest.MapFile{Data: b}
	}
	old, err := openWith(path, upToFive)
	if err != nil {
		t.Fatalf("open at version 5: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)
	for id, login := range map[int64]string{7: "seven", 8: "eight"} {
		if err := signIn(t, old, id, login, login+"-hash", "", now); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO reports (slug, title, status, published_at, size_bytes, sha256, created_at, updated_at) VALUES ('a', 'A', 'published', 1, 10, 'a', 1, 1)`,
		`INSERT INTO reports (slug, title, status, published_at, size_bytes, sha256, created_at, updated_at) VALUES ('b', 'B', 'published', 2, 10, 'b', 1, 1)`,
		`INSERT INTO reports (slug, title, status, published_at, size_bytes, sha256, created_at, updated_at) VALUES ('c', 'C', 'draft', NULL, 10, 'c', 1, 1)`,
		`INSERT INTO votes (report_id, github_id, kind, created_at, updated_at) VALUES (1, 7, 'up', 1, 1)`,
		`INSERT INTO votes (report_id, github_id, kind, created_at, updated_at) VALUES (2, 8, 'down', 1, 1)`,
		`INSERT INTO comments (report_id, parent_id, github_id, body, created_at) VALUES (1, NULL, 7, 'first', 1)`,
		`INSERT INTO comments (report_id, parent_id, github_id, body, created_at) VALUES (1, 1, 8, 'reply', 2)`,
	} {
		if _, err := old.DB().Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	if v := scalarInt(t, old.DB(), "PRAGMA user_version"); v != 5 {
		t.Fatalf("seed database at version %d, want 5", v)
	}
	old.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a version 5 database: %v", err)
	}
	defer s.Close()
	db := s.DB()
	if v, want := scalarInt(t, db, "PRAGMA user_version"), newestVersion(t); v != want || v != 6 {
		t.Fatalf("user_version = %d, want %d (6)", v, want)
	}
	for table, want := range map[string]int64{"users": 2, "sessions": 2, "reports": 3, "votes": 2, "comments": 2, "report_opens": 0} {
		if n := scalarInt(t, db, "SELECT count(*) FROM "+table); n != want {
			t.Errorf("%s has %d rows after the migration, want %d (no backfill)", table, n, want)
		}
	}
	if n := scalarInt(t, db, `SELECT count(*) FROM pragma_table_list('report_opens') WHERE strict = 1 AND wr = 1`); n != 1 {
		t.Error("report_opens is not a STRICT, WITHOUT ROWID table")
	}

	// The keys hold: an open needs a report, a day that is not negative and a 16-byte
	// visitor, and one visitor has one row per report and day.
	visitor := func(b byte) []byte { return bytes.Repeat([]byte{b}, 16) }
	insert := `INSERT INTO report_opens (report_id, day, visitor) VALUES (?, ?, ?)`
	for _, args := range [][]any{{1, 20000, visitor(1)}, {1, 20000, visitor(2)}, {1, 20001, visitor(1)}, {2, 20000, visitor(1)}, {2, 20000, visitor(3)}} {
		if _, err := db.Exec(insert, args...); err != nil {
			t.Fatalf("a valid open %v: %v", args[:2], err)
		}
	}
	for name, args := range map[string][]any{
		"the same visitor twice on one day": {1, 20000, visitor(1)},
		"a report that does not exist":      {99, 20000, visitor(9)},
		"a negative day":                    {1, -1, visitor(9)},
		"a visitor of 15 bytes":             {1, 20000, visitor(9)[:15]},
		"a visitor of 17 bytes":             {1, 20000, append(visitor(9), 9)},
		"a visitor that is text":            {1, 20000, "198.51.100.7"},
		"no visitor":                        {1, 20000, nil},
	} {
		if _, err := db.Exec(insert, args...); err == nil {
			t.Errorf("the schema accepted %s", name)
		}
	}

	// A report with opens is deleted: its opens go, its votes and comments go (as
	// before), and the other report's rows are untouched. A report that is
	// unpublished keeps its opens.
	if _, err := db.Exec("UPDATE reports SET status = 'draft' WHERE id = 2"); err != nil {
		t.Fatal(err)
	}
	if n := scalarInt(t, db, "SELECT count(*) FROM report_opens WHERE report_id = 2"); n != 2 {
		t.Fatalf("unpublishing report 2 left %d of its 2 opens", n)
	}
	if _, err := db.Exec("DELETE FROM reports WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]int64{
		"SELECT count(*) FROM report_opens WHERE report_id = 1": 0,
		"SELECT count(*) FROM report_opens WHERE report_id = 2": 2,
		"SELECT count(*) FROM votes":                            1,
		"SELECT count(*) FROM comments":                         0,
		"SELECT count(*) FROM sessions":                         2,
		"SELECT count(*) FROM users":                            2,
	} {
		if n := scalarInt(t, db, q); n != want {
			t.Errorf("after deleting report 1: %s = %d, want %d", q, n, want)
		}
	}

	// A second start finds the schema current and changes nothing.
	s.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer again.Close()
	if v := scalarInt(t, again.DB(), "PRAGMA user_version"); v != 6 {
		t.Errorf("user_version after a second start = %d", v)
	}
	if n := scalarInt(t, again.DB(), "SELECT count(*) FROM report_opens"); n != 2 {
		t.Errorf("a second start changed the opens: %d rows", n)
	}
}
