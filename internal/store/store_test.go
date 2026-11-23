package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

var ctx = context.Background()

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hband.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func scalarInt(t *testing.T, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func scalarString(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(q).Scan(&s); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return s
}

func signIn(t *testing.T, s *Store, id int64, login, newHash, oldHash string, now time.Time) error {
	t.Helper()
	return s.SignIn(ctx, SignInArgs{
		User:         User{GitHubID: id, Login: login},
		OldTokenHash: oldHash,
		NewTokenHash: newHash,
		ExpiresAt:    now.Add(time.Hour),
		Now:          now,
	})
}

func TestOpenFreshDatabase(t *testing.T) {
	s, path := openTemp(t)
	if v := scalarInt(t, s.DB(), "PRAGMA user_version"); v != 1 {
		t.Fatalf("user_version = %d, want 1", v)
	}
	if m := scalarString(t, s.DB(), "PRAGMA journal_mode"); m != "wal" {
		t.Fatalf("journal_mode = %q, want wal", m)
	}
	s.Close()

	// Reopening is idempotent.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if v := scalarInt(t, s2.DB(), "PRAGMA user_version"); v != 1 {
		t.Fatalf("user_version after reopen = %d, want 1", v)
	}
}

func TestServingPoolHasForeignKeys(t *testing.T) {
	s, _ := openTemp(t)
	const n = 6
	conns := make([]*sql.Conn, 0, n)
	for i := 0; i < n; i++ {
		c, err := s.DB().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for i, c := range conns {
		var on int
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&on); err != nil {
			t.Fatal(err)
		}
		if on != 1 {
			t.Errorf("pooled connection %d: foreign_keys = %d, want 1", i, on)
		}
		c.Close()
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hband.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	if s2, err := Open(path); err == nil {
		s2.Close()
		t.Fatal("Open accepted user_version 99")
	} else if !strings.Contains(err.Error(), "newer") {
		t.Fatalf("error does not say the schema is newer: %v", err)
	}
}

func TestUserDeleteCascadesToSessions(t *testing.T) {
	s, _ := openTemp(t)
	now := time.Unix(1_700_000_000, 0)
	if err := signIn(t, s, 7, "seven", "h1", "", now); err != nil {
		t.Fatal(err)
	}
	if err := signIn(t, s, 8, "eight", "h2", "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec("DELETE FROM users WHERE github_id = 7"); err != nil {
		t.Fatal(err)
	}
	if n := scalarInt(t, s.DB(), "SELECT count(*) FROM sessions WHERE github_id = 7"); n != 0 {
		t.Fatalf("sessions of the deleted user survive: %d", n)
	}
	if n := scalarInt(t, s.DB(), "SELECT count(*) FROM sessions WHERE github_id = 8"); n != 1 {
		t.Fatalf("another user's session was removed: %d", n)
	}
}

func TestUsersHasNoStoredOwnerColumn(t *testing.T) {
	s, _ := openTemp(t)
	rows, err := s.DB().Query("SELECT name FROM pragma_table_info('users')")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		n++
		switch strings.ToLower(name) {
		case "role", "owner", "is_owner", "admin", "is_admin":
			t.Errorf("users has a stored-ownership column %q", name)
		}
	}
	if n == 0 {
		t.Fatal("users has no columns: the check saw nothing")
	}
}

// migrationsWith returns the embedded 0001 plus an extra injected migration.
func migrationsWith(t *testing.T, name, body string) fstest.MapFS {
	t.Helper()
	first, err := embeddedFS.ReadFile("migrations/0001_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	return fstest.MapFS{
		"0001_identity.sql": {Data: first},
		name:                {Data: []byte(body)},
	}
}

const rebuildUsers = `
CREATE TABLE users_new (
    github_id  INTEGER PRIMARY KEY CHECK (github_id > 0),
    login      TEXT    NOT NULL,
    avatar_url TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    note       TEXT    NOT NULL DEFAULT ''
) STRICT;
INSERT INTO users_new (github_id, login, avatar_url, created_at, updated_at)
    SELECT github_id, login, avatar_url, created_at, updated_at FROM users;
DROP TABLE users;
ALTER TABLE users_new RENAME TO users;
`

func seedTwoSessions(t *testing.T, path string) {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1_700_000_000, 0)
	if err := signIn(t, s, 7, "seven", "h1", "", now); err != nil {
		t.Fatal(err)
	}
	if err := signIn(t, s, 8, "eight", "h2", "", now); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationRebuildKeepsChildRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hband.db")
	seedTwoSessions(t, path)

	s, err := openWith(path, migrationsWith(t, "0002_rebuild_users.sql", rebuildUsers))
	if err != nil {
		t.Fatalf("openWith: %v", err)
	}
	defer s.Close()
	if v := scalarInt(t, s.DB(), "PRAGMA user_version"); v != 2 {
		t.Fatalf("user_version = %d, want 2", v)
	}
	if n := scalarInt(t, s.DB(), "SELECT count(*) FROM sessions"); n != 2 {
		t.Fatalf("a users rebuild left %d sessions rows, want both kept", n)
	}
	if n := scalarInt(t, s.DB(), "SELECT count(*) FROM pragma_table_info('users') WHERE name = 'note'"); n != 1 {
		t.Fatal("the injected rebuild did not run: users has no 'note' column")
	}
	// Positive control: the serving pool still enforces the cascade afterwards.
	if _, err := s.DB().Exec("DELETE FROM users WHERE github_id = 7"); err != nil {
		t.Fatal(err)
	}
	if n := scalarInt(t, s.DB(), "SELECT count(*) FROM sessions"); n != 1 {
		t.Fatalf("cascade no longer works after the rebuild: %d sessions", n)
	}
}

// TestRebuildWithForeignKeysOnWouldCascade is the positive control for the
// test above: the same SQL run on a connection that has foreign keys ON does
// delete the sessions, so the previous test is not passing vacuously.
func TestRebuildWithForeignKeysOnWouldCascade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hband.db")
	seedTwoSessions(t, path)

	db, err := sql.Open("sqlite", dsn(path, servingPragmas))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(rebuildUsers); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := scalarInt(t, db, "SELECT count(*) FROM sessions"); n != 0 {
		t.Fatalf("control: expected the rebuild with foreign keys on to cascade-delete sessions, %d remain", n)
	}
}

func TestMigrationOrphanFailsOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hband.db")
	seedTwoSessions(t, path)

	orphan := `INSERT INTO sessions (token_hash, github_id, created_at, expires_at) VALUES ('orphan', 999, 1, 2);`
	s, err := openWith(path, migrationsWith(t, "0002_orphan.sql", orphan))
	if err == nil {
		s.Close()
		t.Fatal("openWith accepted a migration that leaves an orphan session")
	}
	if !strings.Contains(err.Error(), "0002_orphan.sql") || !strings.Contains(err.Error(), "sessions") {
		t.Fatalf("error does not name the migration and the table: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after the failed migration: %v", err)
	}
	defer s2.Close()
	if v := scalarInt(t, s2.DB(), "PRAGMA user_version"); v != 1 {
		t.Fatalf("user_version = %d after a failed migration, want 1", v)
	}
	if n := scalarInt(t, s2.DB(), "SELECT count(*) FROM sessions WHERE token_hash = 'orphan'"); n != 0 {
		t.Fatal("the orphan row was committed")
	}
	if n := scalarInt(t, s2.DB(), "SELECT count(*) FROM sessions"); n != 2 {
		t.Fatalf("seeded sessions changed: %d", n)
	}
}

func TestMigrationNamesMustBeSequential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hband.db")
	fsys := fstest.MapFS{"0002_gap.sql": {Data: []byte("SELECT 1;")}}
	if s, err := openWith(path, fsys); err == nil {
		s.Close()
		t.Fatal("openWith accepted a migration set that starts at 0002")
	}
}

func TestSignInRollsBackOnInsertFailure(t *testing.T) {
	s, _ := openTemp(t)
	now := time.Unix(1_700_000_000, 0)
	if err := signIn(t, s, 7, "old-login", "dup", "", now); err != nil {
		t.Fatal(err)
	}
	if err := signIn(t, s, 8, "eight", "incoming", "", now); err != nil {
		t.Fatal(err)
	}

	// New sign-in for user 7 (login changed), dropping session "incoming",
	// but whose new hash collides with the existing "dup".
	err := signIn(t, s, 7, "new-login", "dup", "incoming", now)
	if err == nil {
		t.Fatal("SignIn accepted a duplicate token_hash")
	}
	if got := scalarString(t, s.DB(), "SELECT login FROM users WHERE github_id = 7"); got != "old-login" {
		t.Fatalf("the user upsert survived the rollback: login = %q", got)
	}
	if n := scalarInt(t, s.DB(), "SELECT count(*) FROM sessions WHERE token_hash = 'incoming'"); n != 1 {
		t.Fatal("the old-session delete survived the rollback")
	}
	if n := scalarInt(t, s.DB(), "SELECT count(*) FROM sessions"); n != 2 {
		t.Fatalf("sessions = %d, want 2", n)
	}
}

func TestSignInRotatesAndSweeps(t *testing.T) {
	s, _ := openTemp(t)
	t0 := time.Unix(1_700_000_000, 0)
	if err := signIn(t, s, 7, "seven", "a", "", t0); err != nil {
		t.Fatal(err)
	}
	// Two hours later: "a" is expired (1h), the new sign-in sweeps it.
	t1 := t0.Add(2 * time.Hour)
	if err := signIn(t, s, 7, "seven", "b", "", t1); err != nil {
		t.Fatal(err)
	}
	if n := scalarInt(t, s.DB(), "SELECT count(*) FROM sessions WHERE token_hash = 'a'"); n != 0 {
		t.Fatal("expired session was not swept")
	}
	if _, ok, err := s.LookupSession(ctx, "b", t1); err != nil || !ok {
		t.Fatalf("LookupSession(b) = ok %v err %v", ok, err)
	}
	// Exactly at expiry the session no longer counts.
	if _, ok, _ := s.LookupSession(ctx, "b", t1.Add(time.Hour)); ok {
		t.Fatal("a session at expires_at still counts")
	}
	// Rotation: signing in with the old hash deletes it.
	if err := signIn(t, s, 7, "seven", "c", "b", t1); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.LookupSession(ctx, "b", t1); ok {
		t.Fatal("rotated-away session still resolves")
	}
	if sess, ok, _ := s.LookupSession(ctx, "c", t1); !ok || sess.User.GitHubID != 7 || sess.User.Login != "seven" {
		t.Fatalf("LookupSession(c) = %+v ok %v", sess, ok)
	}
}

func TestDeleteSessionAndClosedStore(t *testing.T) {
	s, _ := openTemp(t)
	now := time.Unix(1_700_000_000, 0)
	if err := signIn(t, s, 7, "seven", "a", "", now); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, "never-existed"); err != nil {
		t.Fatalf("deleting a missing session must not fail: %v", err)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := s.Ping(ctx); err == nil {
		t.Fatal("Ping succeeded on a closed store")
	}
	if err := s.DeleteSession(ctx, "a"); err == nil {
		t.Fatal("DeleteSession succeeded on a closed store")
	}
}

func TestConcurrentSignIns(t *testing.T) {
	s, _ := openTemp(t)
	now := time.Unix(1_700_000_000, 0)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- s.SignIn(ctx, SignInArgs{
				User:         User{GitHubID: int64(i + 1), Login: "u"},
				NewTokenHash: strings.Repeat("x", i+1),
				ExpiresAt:    now.Add(time.Hour),
				Now:          now,
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent SignIn: %v", err)
		}
	}
	if n := scalarInt(t, s.DB(), "SELECT count(*) FROM sessions"); n != 16 {
		t.Fatalf("sessions = %d, want 16", n)
	}
}
