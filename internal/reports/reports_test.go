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
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sshubham2/hband/internal/store"
)

const testCap = 4096

var bg = context.Background()

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// rig is a real SQLite file and a real data directory.
type rig struct {
	t     *testing.T
	st    *store.Store
	lib   *Library
	root  string
	clock *clock
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "hband.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	r := &rig{t: t, st: st, root: root, clock: &clock{t: time.Unix(1_800_000_000, 0)}}
	r.lib = r.open()
	return r
}

// open (re)opens the library on the same database and data directory, which
// is what a restart does.
func (r *rig) open() *Library {
	r.t.Helper()
	lib, err := Open(Options{DB: r.st.DB(), DataDir: r.root, MaxUploadBytes: testCap, Now: r.clock.Now})
	if err != nil {
		r.t.Fatalf("Open: %v", err)
	}
	return lib
}

// htmlOf returns a valid report of exactly n bytes.
func htmlOf(n int, tag string) []byte {
	head := "<!doctype html><title>" + tag + "</title><p>"
	if n < len(head) {
		panic("htmlOf: too small")
	}
	return []byte(head + strings.Repeat("x", n-len(head)))
}

type filePart struct {
	field, name string
	data        []byte
}

type formSpec struct {
	fields map[string]string
	files  []filePart
}

// goodForm is a valid upload of data under slug.
func goodForm(slug, title, status string, data []byte) formSpec {
	return formSpec{
		fields: map[string]string{"title": title, "slug": slug, "status": status},
		files:  []filePart{{"file", slug + ".html", data}},
	}
}

func (f formSpec) request(t *testing.T) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	keys := make([]string, 0, len(f.fields))
	for k := range f.fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := mw.WriteField(k, f.fields[k]); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range f.files {
		w, err := mw.CreateFormFile(p.field, p.name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(p.data)
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/admin/reports", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return httptest.NewRecorder(), req
}

func (r *rig) save(f formSpec) (Report, error) {
	r.t.Helper()
	w, req := f.request(r.t)
	return r.lib.SaveUpload(w, req)
}

func (r *rig) mustSave(f formSpec) Report {
	r.t.Helper()
	rep, err := r.save(f)
	if err != nil {
		r.t.Fatalf("save %q: %v", f.fields["slug"], err)
	}
	return rep
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (r *rig) fileOf(id int64) []byte {
	r.t.Helper()
	b, err := os.ReadFile(r.lib.path(id))
	if err != nil {
		r.t.Fatalf("read the file of report %d: %v", id, err)
	}
	return b
}

func (r *rig) tmpNames() []string {
	r.t.Helper()
	es, err := os.ReadDir(r.lib.tmp)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// publishedAtRaw returns published_at exactly as stored (nil for NULL).
func (r *rig) publishedAtRaw(slug string) *int64 {
	r.t.Helper()
	var v sql.NullInt64
	if err := r.st.DB().QueryRow("SELECT published_at FROM reports WHERE slug = ?", slug).Scan(&v); err != nil {
		r.t.Fatalf("published_at of %q: %v", slug, err)
	}
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

// snapshot is everything a refused upload must leave exactly as it was: every
// row, the id counter, and every file in reports/ with its hash.
func (r *rig) snapshot() string {
	r.t.Helper()
	var sb strings.Builder
	rows, err := r.st.DB().Query("SELECT " + columns + " FROM reports ORDER BY id")
	if err != nil {
		r.t.Fatal(err)
	}
	for rows.Next() {
		rep, err := scanReport(rows)
		if err != nil {
			r.t.Fatal(err)
		}
		fmt.Fprintf(&sb, "%+v\n", rep)
	}
	rows.Close()
	var seq sql.NullInt64
	r.st.DB().QueryRow("SELECT seq FROM sqlite_sequence WHERE name = 'reports'").Scan(&seq)
	fmt.Fprintf(&sb, "seq=%v\n", seq)
	es, _ := os.ReadDir(r.lib.dir)
	for _, e := range es {
		h, _ := fileSum(filepath.Join(r.lib.dir, e.Name()))
		fmt.Fprintf(&sb, "file %s %s\n", e.Name(), h)
	}
	return sb.String()
}

func TestSavePublishedAndDraft(t *testing.T) {
	r := newRig(t)
	data := htmlOf(900, "one")
	pub := r.mustSave(goodForm("first-report", "First report", "published", data))
	if pub.ID < 1 || pub.Slug != "first-report" || pub.Title != "First report" || pub.Status != StatusPublished || !pub.Published() {
		t.Fatalf("report = %+v", pub)
	}
	if pub.SizeBytes != 900 || pub.SHA256 != sum(data) {
		t.Fatalf("size %d sha %s, want 900 %s", pub.SizeBytes, pub.SHA256, sum(data))
	}
	if got := r.fileOf(pub.ID); !bytes.Equal(got, data) {
		t.Fatal("reports/<id>.html is not byte-equal to the upload")
	}
	if at := r.publishedAtRaw("first-report"); at == nil || *at != r.clock.Now().Unix() {
		t.Fatalf("published_at = %v, want the publish time %d", at, r.clock.Now().Unix())
	}
	if !pub.PublishedAt.Equal(r.clock.Now()) {
		t.Fatalf("PublishedAt = %v", pub.PublishedAt)
	}

	draft := r.mustSave(goodForm("a-draft", "A draft", "draft", htmlOf(200, "two")))
	if draft.Status != StatusDraft || draft.Published() || draft.HasPublishDate() {
		t.Fatalf("draft = %+v", draft)
	}
	if at := r.publishedAtRaw("a-draft"); at != nil {
		t.Fatalf("a draft's published_at = %d, want NULL", *at)
	}
	if draft.ID <= pub.ID {
		t.Fatalf("ids do not grow: %d then %d", pub.ID, draft.ID)
	}
	if names := r.tmpNames(); len(names) != 0 {
		t.Fatalf("tmp/ is not empty after saves: %v", names)
	}
}

func TestAcceptedUploads(t *testing.T) {
	cases := []struct {
		name, filename string
		data           []byte
	}{
		{"a body of exactly the cap", "r.html", htmlOf(testCap, "cap")},
		{"a UTF-8 BOM", "r.html", append([]byte{0xEF, 0xBB, 0xBF}, htmlOf(300, "bom")...)},
		{"starting with a meta tag", "r.html", []byte("<meta charset=\"utf-8\"><title>t</title><p>hello")},
		{"an upper-case extension", "R.HTML", htmlOf(100, "up")},
		{"the .htm extension", "r.htm", htmlOf(100, "htm")},
		{"a plain-text-looking document", "r.html", []byte("just some text, no tags at all")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			f := goodForm("accepted", "T", "published", c.data)
			f.files[0].name = c.filename
			rep := r.mustSave(f)
			if got := r.fileOf(rep.ID); !bytes.Equal(got, c.data) {
				t.Fatal("stored bytes differ from the upload")
			}
			if rep.SizeBytes != int64(len(c.data)) || rep.SHA256 != sum(c.data) {
				t.Fatalf("size %d sha %s", rep.SizeBytes, rep.SHA256)
			}
		})
	}
}

func TestRefusedUploads(t *testing.T) {
	good := htmlOf(300, "ok")
	pngBytes := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), make([]byte, 64)...)
	zipBytes := append([]byte("PK\x03\x04\x14\x00\x00\x00\x08\x00"), make([]byte, 64)...)
	utf16 := []byte{0xFF, 0xFE}
	for _, c := range "<html><body>hi</body></html>" {
		utf16 = append(utf16, byte(c), 0)
	}
	base := func() formSpec { return goodForm("fresh", "Fresh", "published", good) }
	mutate := func(fn func(*formSpec)) formSpec { f := base(); fn(&f); return f }

	cases := []struct {
		name   string
		form   formSpec
		status int
	}{
		{"one byte over the cap", goodForm("fresh", "Fresh", "published", htmlOf(testCap+1, "big")), http.StatusRequestEntityTooLarge},
		{"far over the cap", goodForm("fresh", "Fresh", "published", htmlOf(testCap+multipartSlack+10, "big")), http.StatusRequestEntityTooLarge},
		{"two file parts", mutate(func(f *formSpec) { f.files = append(f.files, filePart{"file", "b.html", good}) }), 400},
		{"a second file part in another field", mutate(func(f *formSpec) { f.files = append(f.files, filePart{"extra", "b.html", good}) }), 400},
		{"the file in the wrong field", mutate(func(f *formSpec) { f.files[0].field = "upload" }), 400},
		{"a .zip name", mutate(func(f *formSpec) { f.files[0].name = "x.zip" }), 400},
		{"a .txt name", mutate(func(f *formSpec) { f.files[0].name = "x.txt" }), 400},
		{"no extension", mutate(func(f *formSpec) { f.files[0].name = "report" }), 400},
		{"html.zip", mutate(func(f *formSpec) { f.files[0].name = "x.html.zip" }), 400},
		{"zip bytes named x.html", mutate(func(f *formSpec) { f.files[0].data = zipBytes }), 400},
		{"PNG bytes named x.html", mutate(func(f *formSpec) { f.files[0].data = pngBytes }), 400},
		{"a UTF-16 BOM file", mutate(func(f *formSpec) { f.files[0].data = utf16 }), 400},
		{"an empty file", mutate(func(f *formSpec) { f.files[0].data = nil }), 400},
		{"no file at all", mutate(func(f *formSpec) { f.files = nil }), 400},
		{"a file field sent as text", mutate(func(f *formSpec) { f.files = nil; f.fields["file"] = string(good) }), 400},
		{"slug with a space and capitals", mutate(func(f *formSpec) { f.fields["slug"] = "Bad Slug" }), 400},
		{"slug with path dots", mutate(func(f *formSpec) { f.fields["slug"] = "../x" }), 400},
		{"empty slug", mutate(func(f *formSpec) { f.fields["slug"] = "" }), 400},
		{"missing slug", mutate(func(f *formSpec) { delete(f.fields, "slug") }), 400},
		{"81-character slug", mutate(func(f *formSpec) { f.fields["slug"] = strings.Repeat("a", 81) }), 400},
		{"slug ending in a hyphen", mutate(func(f *formSpec) { f.fields["slug"] = "abc-" }), 400},
		{"slug with a double hyphen", mutate(func(f *formSpec) { f.fields["slug"] = "a--b" }), 400},
		{"slug with a trailing newline", mutate(func(f *formSpec) { f.fields["slug"] = "abc\n" }), 400},
		{"slug with a slash", mutate(func(f *formSpec) { f.fields["slug"] = "a/b" }), 400},
		{"slug with an underscore", mutate(func(f *formSpec) { f.fields["slug"] = "a_b" }), 400},
		{"empty title", mutate(func(f *formSpec) { f.fields["title"] = "" }), 400},
		{"blank title", mutate(func(f *formSpec) { f.fields["title"] = "   " }), 400},
		{"missing title", mutate(func(f *formSpec) { delete(f.fields, "title") }), 400},
		{"201-character title", mutate(func(f *formSpec) { f.fields["title"] = strings.Repeat("t", 201) }), 400},
		{"a title with a control character", mutate(func(f *formSpec) { f.fields["title"] = "two\nlines" }), 400},
		{"a title over the field cap", mutate(func(f *formSpec) { f.fields["title"] = strings.Repeat("t", maxTextPart+1) }), 400},
		{"status live", mutate(func(f *formSpec) { f.fields["status"] = "live" }), 400},
		{"missing status", mutate(func(f *formSpec) { delete(f.fields, "status") }), 400},
		{"status in capitals", mutate(func(f *formSpec) { f.fields["status"] = "Published" }), 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			// A stored report, so "no row change" means something.
			r.mustSave(goodForm("existing", "Existing", "published", htmlOf(100, "keep")))
			before := r.snapshot()

			_, err := r.save(c.form)
			var ref *Refusal
			if !errors.As(err, &ref) {
				t.Fatalf("err = %v, want a refusal", err)
			}
			if ref.Status != c.status {
				t.Fatalf("status %d (%s), want %d", ref.Status, ref.Message, c.status)
			}
			if after := r.snapshot(); after != before {
				t.Fatalf("a refused upload changed the stored reports:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if names := r.tmpNames(); len(names) != 0 {
				t.Fatalf("tmp/ is not empty after a refusal: %v", names)
			}
		})
	}

	t.Run("a refusal of an existing slug leaves that report alone", func(t *testing.T) {
		r := newRig(t)
		rep := r.mustSave(goodForm("same", "Same", "published", htmlOf(100, "v1")))
		before := r.snapshot()
		f := goodForm("same", "Changed", "published", pngBytes)
		if _, err := r.save(f); err == nil {
			t.Fatal("a PNG replaced a report")
		}
		if r.snapshot() != before || !bytes.Equal(r.fileOf(rep.ID), htmlOf(100, "v1")) {
			t.Fatal("the stored report changed")
		}
	})

	t.Run("not a multipart request", func(t *testing.T) {
		r := newRig(t)
		req := httptest.NewRequest("POST", "/admin/reports", strings.NewReader("title=x"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		_, err := r.lib.SaveUpload(httptest.NewRecorder(), req)
		var ref *Refusal
		if !errors.As(err, &ref) || ref.Status != 400 {
			t.Fatalf("err = %v", err)
		}
		if names := r.tmpNames(); len(names) != 0 {
			t.Fatalf("tmp/ = %v", names)
		}
	})
}

func TestReplaceKeepsIdentity(t *testing.T) {
	r := newRig(t)
	v1, v2 := htmlOf(500, "v1"), htmlOf(700, "v2")
	first := r.mustSave(goodForm("keep-me", "Old title", "published", v1))
	firstAt := r.publishedAtRaw("keep-me")

	r.clock.Advance(48 * time.Hour)
	second := r.mustSave(goodForm("keep-me", "New title", "published", v2))

	if second.ID != first.ID {
		t.Fatalf("id changed from %d to %d", first.ID, second.ID)
	}
	if at := r.publishedAtRaw("keep-me"); at == nil || *at != *firstAt {
		t.Fatalf("published_at moved from %v to %v", firstAt, at)
	}
	if second.Title != "New title" || second.SizeBytes != 700 || second.SHA256 != sum(v2) {
		t.Fatalf("replaced report = %+v", second)
	}
	if !bytes.Equal(r.fileOf(second.ID), v2) {
		t.Fatal("the file was not swapped")
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("updated_at did not move: %v then %v", first.UpdatedAt, second.UpdatedAt)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("created_at moved: %v then %v", first.CreatedAt, second.CreatedAt)
	}
	if n := r.count("reports"); n != 1 {
		t.Fatalf("%d rows after a replace, want 1", n)
	}
	if names := r.tmpNames(); len(names) != 0 {
		t.Fatalf("tmp/ is not empty after a replace (the set-aside file was left): %v", names)
	}
}

func (r *rig) count(table string) int {
	r.t.Helper()
	var n int
	if err := r.st.DB().QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func TestReplaceAndStatus(t *testing.T) {
	t.Run("replacing a draft with status published sets the date once", func(t *testing.T) {
		r := newRig(t)
		r.mustSave(goodForm("d", "D", "draft", htmlOf(100, "a")))
		if r.publishedAtRaw("d") != nil {
			t.Fatal("a draft has a date")
		}
		r.clock.Advance(time.Hour)
		r.mustSave(goodForm("d", "D", "published", htmlOf(110, "b")))
		at := r.publishedAtRaw("d")
		if at == nil || *at != r.clock.Now().Unix() {
			t.Fatalf("published_at = %v", at)
		}
		r.clock.Advance(time.Hour)
		r.mustSave(goodForm("d", "D", "published", htmlOf(120, "c")))
		if again := r.publishedAtRaw("d"); again == nil || *again != *at {
			t.Fatalf("the date moved on a second replace: %v -> %v", at, again)
		}
	})
	t.Run("replacing with status draft hides it and keeps the date", func(t *testing.T) {
		r := newRig(t)
		r.mustSave(goodForm("p", "P", "published", htmlOf(100, "a")))
		at := *r.publishedAtRaw("p")
		r.clock.Advance(time.Hour)
		rep := r.mustSave(goodForm("p", "P", "draft", htmlOf(110, "b")))
		if rep.Published() {
			t.Fatal("still published")
		}
		if got := r.publishedAtRaw("p"); got == nil || *got != at {
			t.Fatalf("the date was lost: %v", got)
		}
	})
}

func TestPublishUnpublishKeepTheDate(t *testing.T) {
	r := newRig(t)
	r.mustSave(goodForm("life", "Life", "published", htmlOf(100, "a")))
	at := *r.publishedAtRaw("life")

	r.clock.Advance(24 * time.Hour)
	if err := r.lib.Unpublish(bg, "life"); err != nil {
		t.Fatal(err)
	}
	rep, _ := r.lib.BySlug(bg, "life")
	if rep.Published() || r.publishedAtRaw("life") == nil || *r.publishedAtRaw("life") != at {
		t.Fatalf("after unpublish: %+v date %v", rep, r.publishedAtRaw("life"))
	}
	if _, err := r.lib.PublishedBySlug(bg, "life"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unpublished report is still public: %v", err)
	}

	r.clock.Advance(24 * time.Hour)
	if err := r.lib.Publish(bg, "life"); err != nil {
		t.Fatal(err)
	}
	rep, _ = r.lib.BySlug(bg, "life")
	if !rep.Published() || *r.publishedAtRaw("life") != at {
		t.Fatalf("after republish: %+v date %v, want the original %d", rep, r.publishedAtRaw("life"), at)
	}

	// A draft that was never published gets its date at the first publish.
	r.mustSave(goodForm("never", "Never", "draft", htmlOf(100, "b")))
	r.clock.Advance(time.Hour)
	if err := r.lib.Publish(bg, "never"); err != nil {
		t.Fatal(err)
	}
	if got := r.publishedAtRaw("never"); got == nil || *got != r.clock.Now().Unix() {
		t.Fatalf("first publish date = %v", got)
	}

	for _, fn := range []func(context.Context, string) error{r.lib.Publish, r.lib.Unpublish, r.lib.Delete} {
		if err := fn(bg, "no-such-report"); !errors.Is(err, ErrNotFound) {
			t.Errorf("an action on a missing slug: %v, want ErrNotFound", err)
		}
	}
}

func TestDeleteRemovesRowAndFile(t *testing.T) {
	r := newRig(t)
	rep := r.mustSave(goodForm("gone", "Gone", "published", htmlOf(100, "a")))
	keep := r.mustSave(goodForm("stay", "Stay", "published", htmlOf(100, "b")))
	if err := r.lib.Delete(bg, "gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.lib.BySlug(bg, "gone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the row survives: %v", err)
	}
	if _, err := os.Stat(r.lib.path(rep.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat of the deleted file: %v, want not-exist", err)
	}
	if _, err := os.Stat(r.lib.path(keep.ID)); err != nil {
		t.Fatalf("another report's file was removed: %v", err)
	}
}

// TestDeletedIDNotReused (M-add-2): AUTOINCREMENT never hands out an id twice,
// so a vote or comment that references a deleted report can never attach to a
// different one.
func TestDeletedIDNotReused(t *testing.T) {
	r := newRig(t)
	r.mustSave(goodForm("one", "One", "published", htmlOf(100, "a")))
	newest := r.mustSave(goodForm("two", "Two", "published", htmlOf(100, "b")))
	if err := r.lib.Delete(bg, "two"); err != nil {
		t.Fatal(err)
	}
	next := r.mustSave(goodForm("three", "Three", "published", htmlOf(100, "c")))
	if next.ID <= newest.ID {
		t.Fatalf("the new id %d is not greater than the deleted newest id %d", next.ID, newest.ID)
	}
	// Even across a restart.
	if err := r.lib.Delete(bg, "three"); err != nil {
		t.Fatal(err)
	}
	r.lib = r.open()
	again := r.mustSave(goodForm("four", "Four", "published", htmlOf(100, "d")))
	if again.ID <= next.ID {
		t.Fatalf("after a restart the new id %d is not greater than %d", again.ID, next.ID)
	}
}

// TestReplaceCommitFailureKeepsFileConsistent (m8): when the commit of a
// replace fails, the file bytes, the hash and the size all still describe the
// old report, and tmp/ is empty.
func TestReplaceCommitFailureKeepsFileConsistent(t *testing.T) {
	r := newRig(t)
	v1 := htmlOf(500, "v1")
	rep := r.mustSave(goodForm("steady", "Old title", "published", v1))
	before := r.snapshot()

	boom := errors.New("injected commit failure")
	r.lib.commit = func(tx *sql.Tx) error { tx.Rollback(); return boom }
	_, err := r.save(goodForm("steady", "New title", "published", htmlOf(900, "v2")))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the injected failure", err)
	}
	r.lib.commit = func(tx *sql.Tx) error { return tx.Commit() }

	if got := r.fileOf(rep.ID); !bytes.Equal(got, v1) {
		t.Fatal("the file bytes are not the old report's")
	}
	now, err := r.lib.BySlug(bg, "steady")
	if err != nil {
		t.Fatal(err)
	}
	if now.SHA256 != sum(v1) || now.SizeBytes != 500 || now.Title != "Old title" {
		t.Fatalf("the row no longer describes the old report: %+v", now)
	}
	if after := r.snapshot(); after != before {
		t.Fatalf("state changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if names := r.tmpNames(); len(names) != 0 {
		t.Fatalf("tmp/ is not empty: %v", names)
	}
}

func TestCreateCommitFailureLeavesNothing(t *testing.T) {
	r := newRig(t)
	boom := errors.New("injected commit failure")
	r.lib.commit = func(tx *sql.Tx) error { tx.Rollback(); return boom }
	before := r.snapshot()
	if _, err := r.save(goodForm("never", "Never", "published", htmlOf(100, "a"))); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if after := r.snapshot(); after != before {
		t.Fatalf("a failed create left something behind:\n%s", after)
	}
	if names := r.tmpNames(); len(names) != 0 {
		t.Fatalf("tmp/ = %v", names)
	}
	// The id was not consumed either.
	r.lib.commit = func(tx *sql.Tx) error { return tx.Commit() }
	if rep := r.mustSave(goodForm("now", "Now", "published", htmlOf(100, "b"))); rep.ID != 1 {
		t.Fatalf("id = %d, want 1: the failed create consumed an id", rep.ID)
	}
}

// On Windows an open file cannot be renamed, so a replace while something is
// reading the report cannot set the old file aside. It must refuse and leave
// the old report whole.
func TestReplaceWhileTheFileIsOpen(t *testing.T) {
	r := newRig(t)
	r.lib.setAsideTries, r.lib.setAsideWait = 3, time.Millisecond
	v1 := htmlOf(500, "v1")
	rep := r.mustSave(goodForm("busy", "Old", "published", v1))
	before := r.snapshot()

	_, f, err := r.lib.OpenRaw(bg, "busy", true)
	if err != nil {
		t.Fatal(err)
	}
	_, saveErr := r.save(goodForm("busy", "New", "published", htmlOf(900, "v2")))
	f.Close()

	if runtime.GOOS == "windows" {
		if !errors.Is(saveErr, ErrBusy) {
			t.Fatalf("err = %v, want ErrBusy: renaming an open file should fail on Windows", saveErr)
		}
		if after := r.snapshot(); after != before {
			t.Fatalf("a refused replace changed state:\n%s", after)
		}
		if got := r.fileOf(rep.ID); !bytes.Equal(got, v1) {
			t.Fatal("the old file changed")
		}
		if names := r.tmpNames(); len(names) != 0 {
			t.Fatalf("tmp/ = %v", names)
		}
		// Once the reader lets go, the same replace works.
		r.mustSave(goodForm("busy", "New", "published", htmlOf(900, "v2")))
	} else if saveErr != nil {
		t.Fatalf("err = %v", saveErr)
	}
	if got, _ := r.lib.BySlug(bg, "busy"); got.Title != "New" || got.SHA256 != sum(htmlOf(900, "v2")) {
		t.Fatalf("after the retry: %+v", got)
	}
}

func TestOpenSweeps(t *testing.T) {
	r := newRig(t)
	rep := r.mustSave(goodForm("keep", "Keep", "published", htmlOf(100, "a")))

	plant := func(path string, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plant(filepath.Join(r.lib.tmp, "upload-123"), "half an upload")
	plant(filepath.Join(r.lib.tmp, "7.old"), "no row has id 7")
	if err := os.Mkdir(filepath.Join(r.lib.tmp, "a-directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	plant(filepath.Join(r.lib.dir, "999.html"), "an orphan")
	plant(filepath.Join(r.lib.dir, "notes.txt"), "not a report file: left alone")

	r.lib = r.open()

	if names := r.tmpNames(); len(names) != 0 {
		t.Fatalf("tmp/ after Open: %v", names)
	}
	if _, err := os.Stat(filepath.Join(r.lib.dir, "999.html")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the orphan 999.html survives: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.lib.dir, "notes.txt")); err != nil {
		t.Fatalf("a file that is not <n>.html was removed: %v", err)
	}
	if !bytes.Equal(r.fileOf(rep.ID), htmlOf(100, "a")) {
		t.Fatal("the report with a row was touched")
	}
}

// m10: a crash during a replace must never lose the only copy of the bytes the
// row describes. Each case plants the state a crash leaves, then restarts.
func TestOpenRecoversAnInterruptedReplace(t *testing.T) {
	v1, v2 := htmlOf(300, "v1"), htmlOf(400, "v2")

	t.Run("after the new file is in place but before the commit", func(t *testing.T) {
		r := newRig(t)
		rep := r.mustSave(goodForm("crash", "Crash", "published", v1))
		// The replace moved v1 aside and renamed v2 in; the row still says v1.
		must(t, os.WriteFile(r.lib.setAsidePath(rep.ID), v1, 0o600))
		must(t, os.WriteFile(r.lib.path(rep.ID), v2, 0o600))
		r.lib = r.open()
		if !bytes.Equal(r.fileOf(rep.ID), v1) {
			t.Fatal("the row's bytes were not restored")
		}
		if names := r.tmpNames(); len(names) != 0 {
			t.Fatalf("tmp/ = %v", names)
		}
	})

	t.Run("while the canonical path is empty", func(t *testing.T) {
		r := newRig(t)
		rep := r.mustSave(goodForm("crash", "Crash", "published", v1))
		must(t, os.Rename(r.lib.path(rep.ID), r.lib.setAsidePath(rep.ID)))
		r.lib = r.open()
		if !bytes.Equal(r.fileOf(rep.ID), v1) {
			t.Fatal("the only copy was not restored")
		}
	})

	t.Run("after the commit but before the set-aside file was removed", func(t *testing.T) {
		r := newRig(t)
		r.mustSave(goodForm("crash", "Crash", "published", v1))
		rep := r.mustSave(goodForm("crash", "Crash", "published", v2)) // committed: row says v2
		must(t, os.WriteFile(r.lib.setAsidePath(rep.ID), v1, 0o600))
		r.lib = r.open()
		if !bytes.Equal(r.fileOf(rep.ID), v2) {
			t.Fatal("the committed file was replaced by the set-aside copy")
		}
		if names := r.tmpNames(); len(names) != 0 {
			t.Fatalf("tmp/ = %v", names)
		}
	})

	t.Run("a set-aside file that matches nothing is not restored", func(t *testing.T) {
		r := newRig(t)
		rep := r.mustSave(goodForm("crash", "Crash", "published", v1))
		must(t, os.WriteFile(r.lib.setAsidePath(rep.ID), []byte("unrelated"), 0o600))
		must(t, os.WriteFile(r.lib.path(rep.ID), v2, 0o600))
		r.lib = r.open()
		if !bytes.Equal(r.fileOf(rep.ID), v2) {
			t.Fatal("an unrelated set-aside file replaced the file in place")
		}
	})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestQueries(t *testing.T) {
	r := newRig(t)
	if _, err := r.lib.Latest(bg); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Latest of an empty library: %v", err)
	}
	r.mustSave(goodForm("old", "Old", "published", htmlOf(100, "a")))
	r.clock.Advance(time.Hour)
	r.mustSave(goodForm("draft", "Draft", "draft", htmlOf(100, "b")))
	r.clock.Advance(time.Hour)
	newest := r.mustSave(goodForm("new", "New", "published", htmlOf(100, "c")))

	if got, err := r.lib.Latest(bg); err != nil || got.Slug != "new" {
		t.Fatalf("Latest = %+v %v", got, err)
	}
	r.lib.Unpublish(bg, "new")
	if got, err := r.lib.Latest(bg); err != nil || got.Slug != "old" {
		t.Fatalf("Latest after unpublishing the newest = %+v %v", got, err)
	}
	all, err := r.lib.ListAll(bg)
	if err != nil || len(all) != 3 || all[0].ID != newest.ID {
		t.Fatalf("ListAll = %+v %v", all, err)
	}
	if _, err := r.lib.PublishedBySlug(bg, "draft"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a draft is public: %v", err)
	}

	// OpenRaw: drafts only when asked for.
	if _, _, err := r.lib.OpenRaw(bg, "draft", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("OpenRaw(published only) of a draft: %v", err)
	}
	rep, f, err := r.lib.OpenRaw(bg, "draft", false)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	if rep.Slug != "draft" || !bytes.Equal(b, htmlOf(100, "b")) {
		t.Fatalf("OpenRaw returned %+v with %d bytes", rep, len(b))
	}
}

// Saves of different slugs from many goroutines all land: the package lock
// serialises the writers.
func TestConcurrentSaves(t *testing.T) {
	r := newRig(t)
	const n = 12
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w, req := goodForm(fmt.Sprintf("r-%d", i), "T", "published", htmlOf(200+i, fmt.Sprint(i))).request(t)
			_, err := r.lib.SaveUpload(w, req)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent save: %v", err)
		}
	}
	if got := r.count("reports"); got != n {
		t.Fatalf("%d rows, want %d", got, n)
	}
	if names := r.tmpNames(); len(names) != 0 {
		t.Fatalf("tmp/ = %v", names)
	}
}

func TestSchema(t *testing.T) {
	r := newRig(t)
	db := r.st.DB()
	if _, err := db.Exec("INSERT INTO reports (slug, title, status, size_bytes, sha256, created_at, updated_at) VALUES ('a','t','live',1,'x',1,1)"); err == nil {
		t.Fatal("the status CHECK accepted 'live'")
	}
	r.mustSave(goodForm("dup", "T", "draft", htmlOf(100, "a")))
	if _, err := db.Exec("INSERT INTO reports (slug, title, status, size_bytes, sha256, created_at, updated_at) VALUES ('dup','t','draft',1,'x',1,1)"); err == nil {
		t.Fatal("a second report with the same slug was accepted")
	}
}

func TestOpenRefusesBadOptions(t *testing.T) {
	r := newRig(t)
	if _, err := Open(Options{DataDir: r.root, MaxUploadBytes: 1}); err == nil {
		t.Error("Open without a database")
	}
	if _, err := Open(Options{DB: r.st.DB(), DataDir: r.root}); err == nil {
		t.Error("Open without a cap")
	}
}
