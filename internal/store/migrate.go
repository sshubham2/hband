package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var migrationName = regexp.MustCompile(`^([0-9]{4})_[a-z0-9_]+\.sql$`)

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations(migrations fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(migrations, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration %q: name must be NNNN_name.sql", e.Name())
		}
		body, err := fs.ReadFile(migrations, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", e.Name(), err)
		}
		v, _ := strconv.Atoi(m[1])
		out = append(out, migration{version: v, name: e.Name(), sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration %q: versions must run 1, 2, 3 without gaps", m.name)
		}
	}
	return out, nil
}

// migrate brings the file at path up to the newest migration under
// possession: it runs on its own *sql.DB with a single connection, foreign
// keys off, and closes that handle before returning, so the serving pool is
// only ever opened on a schema that foreign_key_check passed.
func migrate(path string, migrations fs.FS) (err error) {
	all, err := loadMigrations(migrations)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dsn(path, migrationPragmas))
	if err != nil {
		return fmt.Errorf("open database for migration: %w", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close migration handle: %w", cerr)
		}
	}()
	db.SetMaxOpenConns(1)

	// Not the serving context: a shutdown signal must not leave the handle
	// half-way through a migration or with foreign keys off.
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open database for migration: %w", err)
	}
	defer conn.Close()

	var current int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current < 0 {
		// user_version is a signed 32-bit integer; a negative one was not
		// written by this program.
		return fmt.Errorf("database schema version %d is not one this binary wrote: refusing to start", current)
	}
	if current > len(all) {
		return fmt.Errorf("database schema version %d is newer than this binary understands (%d): refusing to start", current, len(all))
	}

	defer func() {
		// Handback: foreign keys back on whatever happened above.
		if _, ferr := conn.ExecContext(context.Background(), "PRAGMA foreign_keys = ON"); ferr != nil && err == nil {
			err = fmt.Errorf("re-enable foreign keys: %w", ferr)
		}
	}()

	for _, m := range all[current:] {
		if err := applyMigration(ctx, conn, m); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, conn *sql.Conn, m migration) error {
	// PRAGMA foreign_keys does nothing inside a transaction, so it is turned
	// off first: a table rebuild (create, copy, drop, rename) would otherwise
	// cascade-delete every child row through DROP TABLE.
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("migration %s: disable foreign keys: %w", m.name, err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return fmt.Errorf("migration %s: begin: %w", m.name, err)
	}
	committed := false
	defer func() {
		if !committed {
			conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if _, err := conn.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	if tables, err := foreignKeyViolations(ctx, conn); err != nil {
		return fmt.Errorf("migration %s: foreign_key_check: %w", m.name, err)
	} else if len(tables) > 0 {
		return fmt.Errorf("migration %s leaves foreign key violations in table(s) %s", m.name, strings.Join(tables, ", "))
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return fmt.Errorf("migration %s: set version: %w", m.name, err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("migration %s: commit: %w", m.name, err)
	}
	committed = true
	return nil
}

// foreignKeyViolations returns the names of tables holding rows that break a
// foreign key. PRAGMA foreign_key_check returns one row per violation.
func foreignKeyViolations(ctx context.Context, conn *sql.Conn) ([]string, error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var tables []string
	for rows.Next() {
		var table string
		var rowid sql.NullInt64
		var parent string
		var fkid int
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return nil, err
		}
		if !seen[table] {
			seen[table] = true
			tables = append(tables, table)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}
