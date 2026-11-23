// Package reports is the report library: the catalogue of uploaded HTML
// reports and their lifecycle. A report is one self-contained .html file kept
// at <data>/reports/<id>.html, with a row in the reports table that is its
// identity (id, slug, title, status, publish date, size and hash).
//
// Every write (create, replace, publish, unpublish, delete) runs under one
// package lock, so the row and the file are only ever changed together. A
// replace keeps the report's id and its first publish date, which is what
// keeps later votes and comments attached to it.
package reports

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Report statuses.
const (
	StatusDraft     = "draft"
	StatusPublished = "published"
)

// ErrNotFound means no report (or no published report) has that slug.
var ErrNotFound = errors.New("report not found")

// ErrBusy means a replace could not set the old file aside because something
// still has it open (Windows refuses to rename an open file). The old report
// is untouched.
var ErrBusy = errors.New("report file is in use")

// Report is one row of the reports table.
type Report struct {
	ID    int64
	Slug  string
	Title string
	// Status is StatusDraft or StatusPublished.
	Status string
	// PublishedAt is when the report was first published; the zero time while
	// it never has been. Unpublishing does not clear it.
	PublishedAt time.Time
	SizeBytes   int64
	SHA256      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Published reports whether the report is currently visible to readers.
func (r Report) Published() bool { return r.Status == StatusPublished }

// HasPublishDate reports whether the report was ever published.
func (r Report) HasPublishDate() bool { return !r.PublishedAt.IsZero() }

// Options configures Open.
type Options struct {
	DB *sql.DB
	// DataDir holds reports/ and tmp/. It may be relative; it is resolved once.
	DataDir string
	// MaxUploadBytes is the largest file accepted.
	MaxUploadBytes int64
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

// Library is the report library. Its reads and writes are safe for concurrent
// use.
type Library struct {
	db  *sql.DB
	dir string // <data>/reports
	tmp string // <data>/tmp
	cap int64
	now func() time.Time

	// Test seams. commit finishes a write transaction; setAside* bound how long
	// a replace waits for a reader to let go of the old file.
	commit        func(*sql.Tx) error
	setAsideTries int
	setAsideWait  time.Duration
}

// writeMu is the package write lock: every write holds it exclusively, and a
// reader holds it shared while it looks a report up and opens its file, so it
// never sees the moment during a replace when the canonical path is empty.
var writeMu sync.RWMutex

// Open prepares the data directory (reports/ and tmp/), recovers from an
// interrupted write and clears what it left behind:
//
//   - a file set aside by a replace (tmp/<id>.old) is put back when the row
//     still describes it and the file in place does not, and is removed
//     otherwise;
//   - everything else in tmp/ is removed;
//   - a reports/<n>.html with no row is removed.
func Open(o Options) (*Library, error) {
	if o.DB == nil {
		return nil, errors.New("reports: no database")
	}
	if o.MaxUploadBytes < 1 {
		return nil, errors.New("reports: the upload cap must be positive")
	}
	root, err := filepath.Abs(o.DataDir)
	if err != nil {
		return nil, fmt.Errorf("reports: resolve data directory: %w", err)
	}
	l := &Library{
		db:            o.DB,
		dir:           filepath.Join(root, "reports"),
		tmp:           filepath.Join(root, "tmp"),
		cap:           o.MaxUploadBytes,
		now:           o.Now,
		commit:        func(tx *sql.Tx) error { return tx.Commit() },
		setAsideTries: 20,
		setAsideWait:  100 * time.Millisecond,
	}
	if l.now == nil {
		l.now = time.Now
	}
	for _, d := range []string{l.dir, l.tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("reports: create %s: %w", d, err)
		}
	}
	if err := l.sweep(context.Background()); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Library) path(id int64) string {
	return filepath.Join(l.dir, strconv.FormatInt(id, 10)+".html")
}

func (l *Library) setAsidePath(id int64) string {
	return filepath.Join(l.tmp, strconv.FormatInt(id, 10)+".old")
}

var (
	reportFileName = regexp.MustCompile(`^([0-9]{1,18})\.html$`)
	setAsideName   = regexp.MustCompile(`^([0-9]{1,18})\.old$`)
)

// sweep runs once, from Open, before anything is served.
func (l *Library) sweep(ctx context.Context) error {
	writeMu.Lock()
	defer writeMu.Unlock()

	tmpEntries, err := os.ReadDir(l.tmp)
	if err != nil {
		return fmt.Errorf("reports: read %s: %w", l.tmp, err)
	}
	// Recover first: the set-aside file may be the only copy of the bytes the
	// row describes, so it must be looked at before tmp/ is cleared. One that
	// could not be put back (or could not be judged) is kept, and Open refuses
	// to start rather than serve a file that does not match its row: the next
	// start tries again.
	kept := map[string]bool{}
	var stuck []error
	for _, e := range tmpEntries {
		if m := setAsideName.FindStringSubmatch(e.Name()); m != nil {
			id, _ := strconv.ParseInt(m[1], 10, 64)
			if _, err := l.recoverSetAside(ctx, id); err != nil {
				kept[e.Name()] = true
				stuck = append(stuck, err)
			}
		}
	}
	for _, e := range tmpEntries {
		if kept[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(l.tmp, e.Name())); err != nil {
			slog.Warn("reports: could not clear a temporary file", "name", e.Name(), "error", err)
		}
	}

	files, err := os.ReadDir(l.dir)
	if err != nil {
		return errors.Join(append(stuck, fmt.Errorf("reports: read %s: %w", l.dir, err))...)
	}
	for _, e := range files {
		m := reportFileName.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		id, _ := strconv.ParseInt(m[1], 10, 64)
		var n int
		if err := l.db.QueryRowContext(ctx, "SELECT count(*) FROM reports WHERE id = ?", id).Scan(&n); err != nil {
			return errors.Join(append(stuck, fmt.Errorf("reports: look up report %d: %w", id, err))...)
		}
		if n == 0 {
			if err := os.Remove(filepath.Join(l.dir, e.Name())); err != nil {
				slog.Warn("reports: could not remove an orphan file", "name", e.Name(), "error", err)
			} else {
				slog.Info("reports: removed an orphan file", "name", e.Name())
			}
		}
	}
	return errors.Join(stuck...)
}

// recovery names where recoverSetAside ended for one tmp/<id>.old. The error
// it returns with the name is non-nil exactly when the set-aside file must be
// KEPT: it may be the only copy of the bytes the row describes, and it was
// neither restored nor proven unneeded. The corpus testdata/sweep_recovery.json
// has a row for every name below; TestSweepRecoveryExits holds the two in step.
type recovery string

const (
	// Deleted with the rest of tmp/: nothing needs the file.
	recNoRow       recovery = "no-row"                  // no report has this id
	recInPlace     recovery = "committed-file-in-place" // the file in place is the row's
	recOldGone     recovery = "old-gone"                // it vanished before it was read
	recOldNotAFile recovery = "old-not-a-file"          // a directory or other junk, not bytes
	recOldDiffers  recovery = "old-differs"             // not the bytes the row describes
	recRestored    recovery = "restored"                // put back in place
	// Kept, and Open fails: the file may be the only copy.
	recLookupFailed  recovery = "lookup-failed"  // the row could not be read
	recOldUnreadable recovery = "old-unreadable" // it could not be examined
	recRenameFailed  recovery = "rename-failed"  // it could not be put back
)

// recoverSetAside decides, for one tmp/<id>.old, what the row describes. A
// crash between "set the old file aside" and "commit" leaves the new bytes in
// place with the old row; the old bytes are then in .old, and are put back.
// A file is released for deletion only when it is provably not needed; any
// failure on the way returns an error and the caller keeps the file.
func (l *Library) recoverSetAside(ctx context.Context, id int64) (recovery, error) {
	var want string
	err := l.db.QueryRowContext(ctx, "SELECT sha256 FROM reports WHERE id = ?", id).Scan(&want)
	if errors.Is(err, sql.ErrNoRows) {
		return recNoRow, nil
	}
	if err != nil {
		return recLookupFailed, fmt.Errorf("reports: report %d: cannot read its row, so tmp/%d.old is kept: %w", id, id, err)
	}
	cur, old := l.path(id), l.setAsidePath(id)
	if got, err := fileSum(cur); err == nil && got == want {
		return recInPlace, nil
	}
	fi, err := os.Lstat(old)
	if errors.Is(err, os.ErrNotExist) {
		return recOldGone, nil
	}
	if err != nil {
		return recOldUnreadable, fmt.Errorf("reports: report %d: cannot examine %s, so it is kept: %w", id, old, err)
	}
	if !fi.Mode().IsRegular() {
		return recOldNotAFile, nil
	}
	got, err := fileSum(old)
	if errors.Is(err, os.ErrNotExist) {
		return recOldGone, nil
	}
	if err != nil {
		return recOldUnreadable, fmt.Errorf("reports: report %d: cannot read %s, so it is kept: %w", id, old, err)
	}
	if got != want {
		return recOldDiffers, nil
	}
	if err := os.Rename(old, cur); err != nil {
		slog.Error("reports: could not restore a set-aside file; it is kept for the next start", "id", id, "error", err)
		return recRenameFailed, fmt.Errorf("reports: report %d: the file in place does not match its row and %s could not be put back (it is kept; fix %s and start again): %w", id, old, cur, err)
	}
	slog.Warn("reports: restored the previous file of an interrupted replace", "id", id)
	return recRestored, nil
}

// scanner is *sql.Row or *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

const columns = "id, slug, title, status, published_at, size_bytes, sha256, created_at, updated_at"

func scanReport(s scanner) (Report, error) {
	var (
		r         Report
		published sql.NullInt64
		created   int64
		updated   int64
	)
	if err := s.Scan(&r.ID, &r.Slug, &r.Title, &r.Status, &published, &r.SizeBytes, &r.SHA256, &created, &updated); err != nil {
		return Report{}, err
	}
	if published.Valid {
		r.PublishedAt = time.Unix(published.Int64, 0).UTC()
	}
	r.CreatedAt = time.Unix(created, 0).UTC()
	r.UpdatedAt = time.Unix(updated, 0).UTC()
	return r, nil
}

func oneReport(row *sql.Row) (Report, error) {
	r, err := scanReport(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Report{}, ErrNotFound
	}
	return r, err
}

// BySlug returns the report with that slug, draft or published.
func (l *Library) BySlug(ctx context.Context, slug string) (Report, error) {
	return oneReport(l.db.QueryRowContext(ctx, "SELECT "+columns+" FROM reports WHERE slug = ?", slug))
}

// PublishedBySlug returns the report with that slug only while it is published.
func (l *Library) PublishedBySlug(ctx context.Context, slug string) (Report, error) {
	return oneReport(l.db.QueryRowContext(ctx,
		"SELECT "+columns+" FROM reports WHERE slug = ? AND status = 'published'", slug))
}

// Latest returns the most recently published report: newest publish date,
// then highest id. ErrNotFound when nothing is published.
func (l *Library) Latest(ctx context.Context) (Report, error) {
	return oneReport(l.db.QueryRowContext(ctx,
		"SELECT "+columns+" FROM reports WHERE status = 'published' ORDER BY published_at DESC, id DESC LIMIT 1"))
}

// ListAll returns every report, drafts included, newest id first. It is the
// admin list.
func (l *Library) ListAll(ctx context.Context) ([]Report, error) {
	rows, err := l.db.QueryContext(ctx, "SELECT "+columns+" FROM reports ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Report
	for rows.Next() {
		r, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// OpenRaw looks a report up and opens its file as one step under the library
// lock, so the row and the bytes always belong to the same version. With
// publishedOnly, a draft is ErrNotFound. The caller closes the file.
func (l *Library) OpenRaw(ctx context.Context, slug string, publishedOnly bool) (Report, *os.File, error) {
	writeMu.RLock()
	defer writeMu.RUnlock()
	q := "SELECT " + columns + " FROM reports WHERE slug = ?"
	if publishedOnly {
		q += " AND status = 'published'"
	}
	rep, err := oneReport(l.db.QueryRowContext(ctx, q, slug))
	if err != nil {
		return Report{}, nil, err
	}
	f, err := os.Open(l.path(rep.ID))
	if err != nil {
		return Report{}, nil, fmt.Errorf("open the file of report %d: %w", rep.ID, err)
	}
	return rep, f, nil
}

func nullTime(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.Unix(), Valid: true}
}

// Publish makes a report visible. Its publish date is set the first time and
// kept after that.
func (l *Library) Publish(ctx context.Context, slug string) error {
	ctx = context.WithoutCancel(ctx)
	writeMu.Lock()
	defer writeMu.Unlock()
	now := l.now().Unix()
	return l.update(ctx, "UPDATE reports SET status = 'published', published_at = COALESCE(published_at, ?), updated_at = ? WHERE slug = ?",
		now, now, slug)
}

// Unpublish hides a report. Its publish date is kept, so republishing does
// not move it.
func (l *Library) Unpublish(ctx context.Context, slug string) error {
	ctx = context.WithoutCancel(ctx)
	writeMu.Lock()
	defer writeMu.Unlock()
	return l.update(ctx, "UPDATE reports SET status = 'draft', updated_at = ? WHERE slug = ?", l.now().Unix(), slug)
}

func (l *Library) update(ctx context.Context, query string, args ...any) error {
	res, err := l.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes the report's row and then its file. A file that cannot be
// removed (a reader holds it open) is left as an orphan for the next Open to
// sweep; the id is never reused, so nothing can collide with it.
func (l *Library) Delete(ctx context.Context, slug string) error {
	ctx = context.WithoutCancel(ctx)
	writeMu.Lock()
	defer writeMu.Unlock()

	var id int64
	if err := l.db.QueryRowContext(ctx, "SELECT id FROM reports WHERE slug = ?", slug).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM reports WHERE id = ?", id); err != nil {
		return err
	}
	if err := l.commit(tx); err != nil {
		return err
	}
	if err := os.Remove(l.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("reports: the file of a deleted report could not be removed; the next start sweeps it", "id", id, "error", err)
	}
	return nil
}
