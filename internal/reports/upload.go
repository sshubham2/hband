package reports

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// multipartSlack is what a form may weigh beyond the file: part headers,
	// boundaries and the small text fields.
	multipartSlack = 65536
	// maxTextPart caps each text field; maxSniff is how much of the file is
	// looked at to decide its type.
	maxTextPart = 4096
	maxSniff    = 512

	maxSlugLen  = 80
	maxTitleLen = 200
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Refusal is an upload the library will not take. It carries the HTTP status
// to answer with and a message that is safe to show the owner.
type Refusal struct {
	Status  int
	Message string
}

func (e *Refusal) Error() string { return e.Message }

func refuse(status int, format string, args ...any) *Refusal {
	return &Refusal{Status: status, Message: fmt.Sprintf(format, args...)}
}

// staged is an upload that passed every check and is waiting in tmp/.
type staged struct {
	slug, title, status string
	path                string
	size                int64
	sum                 string
}

// SaveUpload reads a multipart form from r (fields title, slug and status,
// and exactly one file part named "file") and saves it by slug: a new slug
// creates a report, an existing slug replaces that report's file and title
// while keeping its id and first publish date.
//
// Anything it refuses is a *Refusal, and a refusal leaves no row change, no
// file in reports/ and nothing in tmp/. A file over the cap is a 413.
func (l *Library) SaveUpload(w http.ResponseWriter, r *http.Request) (Report, error) {
	up, err := l.stage(w, r)
	if err != nil {
		return Report{}, err
	}
	rep, err := l.save(context.WithoutCancel(r.Context()), up)
	if err != nil {
		os.Remove(up.path) // a no-op once save moved the file into place
		return Report{}, err
	}
	return rep, nil
}

// stage reads the form, streaming the file to tmp/ while hashing it.
func (l *Library) stage(w http.ResponseWriter, r *http.Request) (_ *staged, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, l.cap+multipartSlack)
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, refuse(http.StatusBadRequest, "The upload must be a multipart form with a file.")
	}

	up := &staged{}
	defer func() {
		if err != nil && up.path != "" {
			os.Remove(up.path)
		}
	}()

	fields := map[string]string{}
	haveFile := false
	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			return nil, readFailure(perr)
		}
		name := part.FormName()

		if part.FileName() != "" {
			// Every part with a filename counts, whatever field it arrives in.
			if haveFile {
				return nil, refuse(http.StatusBadRequest, "Send exactly one file.")
			}
			haveFile = true
			if name != "file" {
				return nil, refuse(http.StatusBadRequest, "The file must be sent in the field named file.")
			}
			if ext := strings.ToLower(filepath.Ext(part.FileName())); ext != ".html" && ext != ".htm" {
				return nil, refuse(http.StatusBadRequest, "Only a single .html file can be uploaded.")
			}
			if err := l.streamFile(part, up); err != nil {
				return nil, err
			}
			continue
		}

		switch name {
		case "title", "slug", "status":
			if _, dup := fields[name]; dup {
				return nil, refuse(http.StatusBadRequest, "The field %s was sent twice.", name)
			}
			b, rerr := io.ReadAll(io.LimitReader(part, maxTextPart+1))
			if rerr != nil {
				return nil, readFailure(rerr)
			}
			if len(b) > maxTextPart {
				return nil, refuse(http.StatusBadRequest, "The field %s is too long.", name)
			}
			fields[name] = string(b)
		default:
			// Unknown fields (and a text part that is not a file) are discarded.
			if _, rerr := io.Copy(io.Discard, part); rerr != nil {
				return nil, readFailure(rerr)
			}
		}
	}

	if !haveFile {
		return nil, refuse(http.StatusBadRequest, "Choose a .html file to upload.")
	}
	if up.slug, err = checkSlug(fields["slug"]); err != nil {
		return nil, err
	}
	if up.title, err = checkTitle(fields["title"]); err != nil {
		return nil, err
	}
	switch s := fields["status"]; s {
	case StatusPublished, StatusDraft:
		up.status = s
	default:
		return nil, refuse(http.StatusBadRequest, "The status must be published or draft.")
	}
	return up, nil
}

// readFailure turns a read error into the refusal it deserves.
func readFailure(err error) *Refusal {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return refuse(http.StatusRequestEntityTooLarge, "The upload is too large.")
	}
	return refuse(http.StatusBadRequest, "The upload could not be read.")
}

func checkSlug(s string) (string, error) {
	if len(s) < 1 || len(s) > maxSlugLen || !slugPattern.MatchString(s) {
		return "", refuse(http.StatusBadRequest,
			"The slug must be 1 to %d characters: lower-case letters and digits, with single hyphens between them.", maxSlugLen)
	}
	return s, nil
}

func checkTitle(s string) (string, error) {
	t := strings.TrimSpace(s)
	n := utf8.RuneCountInString(t)
	if !utf8.ValidString(t) || n < 1 || n > maxTitleLen {
		return "", refuse(http.StatusBadRequest, "The title must be 1 to %d characters.", maxTitleLen)
	}
	for _, r := range t {
		if unicode.IsControl(r) {
			return "", refuse(http.StatusBadRequest, "The title must not contain control characters.")
		}
	}
	return t, nil
}

// sniffOK is the file-type check: after removing one UTF-8 byte-order mark,
// the first 512 bytes must sniff as UTF-8 HTML or UTF-8 text (a file that
// begins with a <meta> tag sniffs as text). Binary files (zip, PNG) and
// UTF-16 text do not pass.
func sniffOK(head []byte) bool {
	head = bytes.TrimPrefix(head, utf8BOM)
	switch http.DetectContentType(head) {
	case "text/html; charset=utf-8", "text/plain; charset=utf-8":
		return true
	}
	return false
}

// streamFile copies one file part into tmp/upload-<random>, hashing it and
// counting its size. It records the path in up as soon as the file exists, so
// the caller can remove it on any later refusal.
func (l *Library) streamFile(part *multipart.Part, up *staged) error {
	head := make([]byte, maxSniff)
	n, rerr := io.ReadFull(part, head)
	if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
		return readFailure(rerr)
	}
	head = head[:n]
	if int64(n) > l.cap {
		return refuse(http.StatusRequestEntityTooLarge, "The file is larger than the %d byte limit.", l.cap)
	}
	if n == 0 {
		return refuse(http.StatusBadRequest, "The file is empty.")
	}
	if !sniffOK(head) {
		return refuse(http.StatusBadRequest, "The file is not an HTML file (it must be UTF-8 text).")
	}

	f, err := os.CreateTemp(l.tmp, "upload-*")
	if err != nil {
		return fmt.Errorf("create a temporary file: %w", err)
	}
	up.path = f.Name()
	sum := sha256.New()
	size := int64(n)
	werr := func() error {
		if _, err := f.Write(head); err != nil {
			return sinkError{err}
		}
		sum.Write(head)
		copied, err := io.Copy(sink{io.MultiWriter(f, sum)}, io.LimitReader(part, l.cap+1-size))
		size += copied
		return err
	}()
	cerr := f.Close()
	var disk sinkError
	switch {
	case errors.As(werr, &disk):
		return fmt.Errorf("write the upload: %w", disk.err)
	case werr != nil:
		// The request body failed to read: too large, dropped or malformed.
		return readFailure(werr)
	case cerr != nil:
		return fmt.Errorf("write the upload: %w", cerr)
	case size > l.cap:
		return refuse(http.StatusRequestEntityTooLarge, "The file is larger than the %d byte limit.", l.cap)
	}
	up.size = size
	up.sum = hex.EncodeToString(sum.Sum(nil))
	return nil
}

// sinkError marks a failure to write the destination file, which is the
// server's fault, as opposed to a failure to read the request, which is the
// client's.
type sinkError struct{ err error }

func (e sinkError) Error() string { return e.err.Error() }
func (e sinkError) Unwrap() error { return e.err }

// sink wraps the destination so every write failure is a sinkError.
type sink struct{ w io.Writer }

func (s sink) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)
	if err != nil {
		err = sinkError{err}
	}
	return n, err
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// save writes one staged upload by slug. It owns up.path: on success the file
// has moved to reports/<id>.html; on failure the caller removes it.
func (l *Library) save(ctx context.Context, up *staged) (Report, error) {
	writeMu.Lock()
	defer writeMu.Unlock()

	now := l.now()
	var id int64
	var published sql.NullInt64
	err := l.db.QueryRowContext(ctx, "SELECT id, published_at FROM reports WHERE slug = ?", up.slug).Scan(&id, &published)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		id, err = l.create(ctx, up, now)
	case err != nil:
		return Report{}, fmt.Errorf("look up slug: %w", err)
	default:
		err = l.replace(ctx, up, id, published, now)
	}
	if err != nil {
		return Report{}, err
	}
	return l.BySlug(ctx, up.slug)
}

// create inserts the row and moves the file to reports/<id>.html, then
// commits. If the commit fails the file is removed again.
func (l *Library) create(ctx context.Context, up *staged, now time.Time) (int64, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var published sql.NullInt64
	if up.status == StatusPublished {
		published = sql.NullInt64{Int64: now.Unix(), Valid: true}
	}
	res, err := tx.ExecContext(ctx,
		"INSERT INTO reports (slug, title, status, published_at, size_bytes, sha256, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		up.slug, up.title, up.status, published, up.size, up.sum, now.Unix(), now.Unix())
	if err != nil {
		return 0, fmt.Errorf("insert report: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	dst := l.path(id)
	if err := os.Rename(up.path, dst); err != nil {
		return 0, fmt.Errorf("move the upload into place: %w", err)
	}
	if err := l.commit(tx); err != nil {
		os.Remove(dst)
		return 0, fmt.Errorf("commit: %w", err)
	}
	return id, nil
}

// replace swaps the file and the row's title, status, size and hash while
// keeping the id and the first publish date (set now only if the report was
// never published and is being published). The old file is set aside in tmp/
// first and put back if anything fails before the commit, so the bytes, hash
// and size always describe the same report.
func (l *Library) replace(ctx context.Context, up *staged, id int64, published sql.NullInt64, now time.Time) error {
	if up.status == StatusPublished && !published.Valid {
		published = sql.NullInt64{Int64: now.Unix(), Valid: true}
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		"UPDATE reports SET title = ?, status = ?, published_at = ?, size_bytes = ?, sha256 = ?, updated_at = ? WHERE id = ?",
		up.title, up.status, published, up.size, up.sum, now.Unix(), id); err != nil {
		return fmt.Errorf("update report: %w", err)
	}

	dst, old := l.path(id), l.setAsidePath(id)
	moved, err := l.setAside(dst, old)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBusy, err)
	}
	restore := func() {
		if !moved {
			return
		}
		if err := os.Rename(old, dst); err != nil {
			slog.Error("reports: could not put the previous file back; the next start recovers it", "id", id, "error", err)
		}
	}
	if err := os.Rename(up.path, dst); err != nil {
		restore()
		return fmt.Errorf("move the upload into place: %w", err)
	}
	if err := l.commit(tx); err != nil {
		// The new file is in place and the row is not: put the old one back,
		// or, when there was none, take the new one away again.
		if moved {
			restore()
		} else {
			os.Remove(dst)
		}
		return fmt.Errorf("commit: %w", err)
	}
	if moved {
		if err := os.Remove(old); err != nil {
			slog.Warn("reports: could not remove the set-aside file; the next start sweeps it", "id", id, "error", err)
		}
	}
	return nil
}

// setAside moves src to dst. A file that does not exist is not an error
// (moved is false). Another handle holding src open makes Windows refuse the
// rename, so it is retried for a short, bounded time before giving up.
func (l *Library) setAside(src, dst string) (moved bool, err error) {
	for try := 1; ; try++ {
		err = os.Rename(src, dst)
		if err == nil {
			return true, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if try >= l.setAsideTries {
			return false, err
		}
		time.Sleep(l.setAsideWait)
	}
}
