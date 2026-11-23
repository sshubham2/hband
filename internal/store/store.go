// Package store owns the SQLite database: opening it, migrating it and the
// identity queries that sign-in needs.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, registers "sqlite"
)

//go:embed migrations/*.sql
var embeddedFS embed.FS

// embeddedMigrations is the migration set compiled into the binary.
func embeddedMigrations() fs.FS {
	sub, err := fs.Sub(embeddedFS, "migrations")
	if err != nil {
		panic(err) // the directory is embedded above, so this cannot happen
	}
	return sub
}

// Store is the serving handle to the database.
type Store struct {
	db *sql.DB
}

// Open migrates the database at path to the newest embedded schema and opens
// the serving pool on it.
func Open(path string) (*Store, error) {
	return openWith(path, embeddedMigrations())
}

// openWith is Open with an injectable migration set (tests).
func openWith(path string, migrations fs.FS) (*Store, error) {
	// A file: URI cannot carry a relative path (the first segment would be read
	// as the URI authority, or as the root of the current drive), so the path is
	// resolved against the working directory once, here, for both handles.
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	if err := migrate(path, migrations); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn(path, servingPragmas))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	return &Store{db: db}, nil
}

// Pragmas applied by the driver to every new pooled connection.
const (
	servingPragmas   = "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	migrationPragmas = "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
)

// dsn builds a file: URI for the driver from an ABSOLUTE path (openWith
// resolves it). The path is percent-escaped, so a data directory containing
// '?', '#' or spaces still names the right file.
func dsn(path, pragmas string) string {
	p := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: pragmas}
	return u.String()
}

// Close closes the serving pool.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the serving pool to the packages that own later tables.
func (s *Store) DB() *sql.DB { return s.db }

// Ping reports whether the database answers.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func unix(t time.Time) int64 { return t.Unix() }
