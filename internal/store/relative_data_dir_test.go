package store

import (
	"os"
	"path/filepath"
	"testing"
)

// The configured default HBAND_DATA_DIR is the relative ./data, and realMain
// passes filepath.Join(DataDir, "hband.db") to Open. A relative path must name
// a file under the working directory.
func TestOpenAcceptsRelativeDataDir(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll("data", 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := Open(filepath.Join("./data", "hband.db"))
	if err != nil {
		t.Fatalf("Open refused the default relative data directory: %v", err)
	}
	defer st.Close()
	if _, err := os.Stat(filepath.Join(dir, "data", "hband.db")); err != nil {
		t.Fatalf("the database is not under the working directory's data folder: %v", err)
	}
}
