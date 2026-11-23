package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// PRAGMA user_version is a signed 32-bit integer. A database whose version is
// negative was not written by this binary: Open must refuse it with an error,
// not panic.
func TestOpenRefusesNegativeSchemaVersion(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hband.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(p))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = -1"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	var st *Store
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Open panicked on user_version -1: %v", r)
			}
		}()
		st, err = Open(p)
	}()
	if err == nil {
		st.Close()
		t.Fatal("Open accepted a database whose user_version is -1")
	}
}
