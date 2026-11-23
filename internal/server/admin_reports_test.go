package server

import (
	"bytes"
	"database/sql"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

type testFile struct {
	field, name string
	data        []byte
}

// reportHTML is a valid report of exactly n bytes carrying tag.
func reportHTML(n int, tag string) []byte {
	head := "<!doctype html><title>" + tag + "</title><p>" + tag + " "
	return []byte(head + strings.Repeat("x", n-len(head)))
}

func multipartBody(t *testing.T, fields map[string]string, files ...testFile) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, k := range []string{"title", "slug", "status"} {
		if v, ok := fields[k]; ok {
			mw.WriteField(k, v)
		}
	}
	for _, f := range files {
		w, err := mw.CreateFormFile(f.field, f.name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(f.data)
	}
	mw.Close()
	return buf.Bytes(), mw.FormDataContentType()
}

func withBody(ctype string, b []byte) func(*http.Request) {
	return func(r *http.Request) {
		r.Body = io.NopCloser(bytes.NewReader(b))
		r.ContentLength = int64(len(b))
		r.Header.Set("Content-Type", ctype)
	}
}

// upload sends the upload form as the owner.
func (h *harness) upload(sid, slug, title, status string, data []byte, extra ...func(*http.Request)) result {
	h.t.Helper()
	body, ctype := multipartBody(h.t, map[string]string{"title": title, "slug": slug, "status": status},
		testFile{"file", slug + ".html", data})
	return h.serve("POST", "/admin/reports", append([]func(*http.Request){h.withSID(sid), withBody(ctype, body)}, extra...)...)
}

func (h *harness) reportRow(slug string) (id int64, status string, published sql.NullInt64, ok bool) {
	h.t.Helper()
	err := h.store.DB().QueryRow("SELECT id, status, published_at FROM reports WHERE slug = ?", slug).Scan(&id, &status, &published)
	if err == sql.ErrNoRows {
		return 0, "", sql.NullInt64{}, false
	}
	if err != nil {
		h.t.Fatalf("report row %q: %v", slug, err)
	}
	return id, status, published, true
}

func (h *harness) reportFile(id int64) ([]byte, error) {
	return os.ReadFile(filepath.Join(h.dataDir, "reports", strconv.FormatInt(id, 10)+".html"))
}

func (h *harness) tmpEntries() []string {
	h.t.Helper()
	es, err := os.ReadDir(filepath.Join(h.dataDir, "tmp"))
	if err != nil {
		h.t.Fatalf("read tmp/: %v", err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestAdminLandingOffersTheUploadForm(t *testing.T) {
	h := newHarness(t, false)
	res := h.serve("GET", "/admin", h.withSID(h.mint(testOwnerID, "the-owner")))
	if res.Status != 200 || !hasMarker(res.Body) {
		t.Fatalf("GET /admin: %d", res.Status)
	}
	for _, want := range []string{
		`method="post" action="/admin/reports" enctype="multipart/form-data"`,
		`<input type="file" name="file"`,
		`name="title"`, `name="slug"`, `name="status"`,
		`value="published"`, `value="draft"`,
		"No reports yet",
	} {
		if !strings.Contains(res.Body, want) {
			t.Errorf("the landing page lacks %q:\n%s", want, res.Body)
		}
	}
}

func TestOwnerReportLifecycle(t *testing.T) {
	h := newHarness(t, false)
	sid := h.mint(testOwnerID, "the-owner")
	v1, v2 := reportHTML(600, "first-version"), reportHTML(800, "second-version")

	// Publish now.
	res := h.upload(sid, "my-report", "My <report>", "published", v1)
	if res.Status != 303 || res.Location() != "/admin/reports/my-report" {
		t.Fatalf("upload: %d %q %s", res.Status, res.Location(), res.Body)
	}
	id, status, published, ok := h.reportRow("my-report")
	if !ok || status != "published" || !published.Valid || published.Int64 != h.clock.Now().Unix() {
		t.Fatalf("row: id %d status %q published %v ok %v", id, status, published, ok)
	}
	if got, err := h.reportFile(id); err != nil || !bytes.Equal(got, v1) {
		t.Fatalf("<data>/reports/%d.html is not byte-equal to the upload: %v", id, err)
	}
	if names := h.tmpEntries(); len(names) != 0 {
		t.Fatalf("tmp/ = %v", names)
	}

	// The list and the edit page.
	if land := h.serve("GET", "/admin", h.withSID(sid)); !strings.Contains(land.Body, `href="/admin/reports/my-report"`) ||
		!strings.Contains(land.Body, "My &lt;report&gt;") {
		t.Fatalf("the report is not listed (or its title is not escaped): %s", land.Body)
	}
	page := h.serve("GET", "/admin/reports/my-report", h.withSID(sid))
	if page.Status != 200 || !hasMarker(page.Body) {
		t.Fatalf("edit page: %d", page.Status)
	}
	for _, want := range []string{
		`<iframe sandbox="allow-popups allow-popups-to-escape-sandbox" referrerpolicy="no-referrer" src="/admin/reports/my-report/raw"`,
		`value="my-report"`, `/admin/reports/my-report/unpublish`, `/admin/reports/my-report/delete`, `<input type="file" name="file"`,
	} {
		if !strings.Contains(page.Body, want) {
			t.Errorf("edit page lacks %q", want)
		}
	}
	if strings.Contains(page.Body, "allow-scripts") || strings.Contains(page.Body, "allow-same-origin") {
		t.Error("the preview frame is not sandboxed tightly")
	}
	if strings.Contains(page.Body, "first-version") {
		t.Error("report bytes appear in a site page")
	}

	// Replace under the same slug: same id, same date, new bytes and title.
	h.clock.Advance(72 * time.Hour)
	res = h.upload(sid, "my-report", "Renamed", "published", v2)
	if res.Status != 303 || res.Location() != "/admin/reports/my-report" {
		t.Fatalf("replace: %d %s", res.Status, res.Body)
	}
	id2, _, published2, _ := h.reportRow("my-report")
	if id2 != id || published2.Int64 != published.Int64 {
		t.Fatalf("a replace moved the identity: id %d -> %d, published_at %d -> %d", id, id2, published.Int64, published2.Int64)
	}
	if got, _ := h.reportFile(id); !bytes.Equal(got, v2) {
		t.Fatal("the file was not replaced")
	}
	if n := h.count("reports"); n != 1 {
		t.Fatalf("%d reports after a replace", n)
	}

	// Unpublish and republish keep the date.
	h.clock.Advance(24 * time.Hour)
	if res := h.serve("POST", "/admin/reports/my-report/unpublish", h.withSID(sid)); res.Status != 303 || res.Location() != "/admin/reports/my-report" {
		t.Fatalf("unpublish: %d %q", res.Status, res.Location())
	}
	if _, status, pub, _ := h.reportRow("my-report"); status != "draft" || pub.Int64 != published.Int64 {
		t.Fatalf("after unpublish: %q %v", status, pub)
	}
	if res := h.serve("POST", "/admin/reports/my-report/publish", h.withSID(sid)); res.Status != 303 {
		t.Fatalf("publish: %d", res.Status)
	}
	if _, status, pub, _ := h.reportRow("my-report"); status != "published" || pub.Int64 != published.Int64 {
		t.Fatalf("after republish: %q %v, want the original date %d", status, pub, published.Int64)
	}

	// Delete removes the row and the file.
	if res := h.serve("POST", "/admin/reports/my-report/delete", h.withSID(sid)); res.Status != 303 || res.Location() != "/admin" {
		t.Fatalf("delete: %d %q", res.Status, res.Location())
	}
	if _, _, _, ok := h.reportRow("my-report"); ok {
		t.Fatal("the row survives a delete")
	}
	if _, err := h.reportFile(id); !os.IsNotExist(err) {
		t.Fatalf("the file survives a delete: %v", err)
	}
	if res := h.serve("GET", "/admin/reports/my-report/raw", h.withSID(sid)); res.Status != 404 {
		t.Fatalf("preview of a deleted report: %d", res.Status)
	}
}

func TestDraftUploadHasNoPublishDate(t *testing.T) {
	h := newHarness(t, false)
	sid := h.mint(testOwnerID, "o")
	if res := h.upload(sid, "later", "Later", "draft", reportHTML(300, "d")); res.Status != 303 {
		t.Fatalf("upload: %d %s", res.Status, res.Body)
	}
	if _, status, pub, _ := h.reportRow("later"); status != "draft" || pub.Valid {
		t.Fatalf("a draft: status %q published_at %v, want NULL", status, pub)
	}
}

// m4: the gated preview is never cached and carries the report policy, not the
// site policy. It works for drafts, and also under the production cookie name.
func TestPreviewHeaders(t *testing.T) {
	for _, tls := range []bool{false, true} {
		name := "http cookie"
		if tls {
			name = "https __Host- cookie"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, tls)
			sid := h.mint(testOwnerID, "o")
			data := reportHTML(500, "preview")
			if res := h.upload(sid, "draft-one", "Draft one", "draft", data); res.Status != 303 {
				t.Fatalf("upload: %d", res.Status)
			}
			id, _, _, _ := h.reportRow("draft-one")

			res := h.serve("GET", "/admin/reports/draft-one/raw", h.withSID(sid))
			if res.Status != 200 || res.Body != string(data) {
				t.Fatalf("preview of a draft: %d, %d bytes", res.Status, len(res.Body))
			}
			hd := res.Header
			if got := hd.Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			csp := hd.Get("Content-Security-Policy")
			if csp == siteCSP || len(hd.Values("Content-Security-Policy")) != 1 {
				t.Errorf("the site CSP was not replaced: %q", hd.Values("Content-Security-Policy"))
			}
			for _, want := range []string{"sandbox allow-popups allow-popups-to-escape-sandbox;", "default-src 'none'", "script-src 'none'",
				"style-src 'unsafe-inline'", "img-src data:", "font-src data:", "form-action 'none'", "base-uri 'none'", "frame-ancestors " + h.cfg.Origin} {
				if !strings.Contains(csp, want) {
					t.Errorf("CSP %q lacks %q", csp, want)
				}
			}
			if strings.Contains(csp, "allow-scripts") || strings.Contains(csp, "allow-same-origin") {
				t.Errorf("CSP %q allows scripts or the site origin", csp)
			}
			for k, want := range map[string]string{
				"Content-Type":                 "text/html; charset=utf-8",
				"X-Content-Type-Options":       "nosniff",
				"Referrer-Policy":              "no-referrer",
				"Cross-Origin-Resource-Policy": "same-origin",
			} {
				if got := hd.Get(k); got != want {
					t.Errorf("%s = %q, want %q", k, got, want)
				}
			}
			etag := hd.Get("ETag")
			if etag != `"`+sha256Hex(string(data))+`"` {
				t.Errorf("ETag = %q", etag)
			}

			if res := h.serve("HEAD", "/admin/reports/draft-one/raw", h.withSID(sid)); res.Status != 200 || res.Body != "" ||
				res.Header.Get("Content-Security-Policy") != csp {
				t.Errorf("HEAD: %d %d bytes", res.Status, len(res.Body))
			}
			if res := h.serve("GET", "/admin/reports/draft-one/raw", h.withSID(sid), withHeader("If-None-Match", etag)); res.Status != 304 {
				t.Errorf("If-None-Match: %d", res.Status)
			}

			// Anyone but the owner gets nothing from the preview.
			if res := h.serve("GET", "/admin/reports/draft-one/raw"); res.Status != 303 || strings.Contains(res.Body, "preview") {
				t.Errorf("anonymous preview: %d", res.Status)
			}
			other := h.mint(7, "somebody")
			if res := h.serve("GET", "/admin/reports/draft-one/raw", h.withSID(other)); res.Status != 403 || strings.Contains(res.Body, "preview") {
				t.Errorf("non-owner preview: %d", res.Status)
			}
			_ = id
		})
	}
}

func TestUploadRefusalsOverHTTP(t *testing.T) {
	good := reportHTML(300, "good")
	zipBytes := append([]byte("PK\x03\x04\x14\x00\x00\x00\x08\x00"), make([]byte, 64)...)
	pngBytes := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), make([]byte, 64)...)
	fields := func(slug string) map[string]string {
		return map[string]string{"title": "T", "slug": slug, "status": "published"}
	}
	cases := []struct {
		name   string
		fields map[string]string
		files  []testFile
		status int
	}{
		{"one byte over the cap", fields("big"), []testFile{{"file", "big.html", reportHTML(testUploadCap+1, "big")}}, 413},
		{"two file parts", fields("two"), []testFile{{"file", "a.html", good}, {"file", "b.html", good}}, 400},
		{"a .zip name", fields("zip"), []testFile{{"file", "a.zip", good}}, 400},
		{"a .txt name", fields("txt"), []testFile{{"file", "a.txt", good}}, 400},
		{"zip bytes named x.html", fields("zipb"), []testFile{{"file", "x.html", zipBytes}}, 400},
		{"PNG bytes named x.html", fields("pngb"), []testFile{{"file", "x.html", pngBytes}}, 400},
		{"slug with a space", fields("Bad Slug"), []testFile{{"file", "x.html", good}}, 400},
		{"slug with dots", fields("../x"), []testFile{{"file", "x.html", good}}, 400},
		{"empty slug", fields(""), []testFile{{"file", "x.html", good}}, 400},
		{"81-character slug", fields(strings.Repeat("a", 81)), []testFile{{"file", "x.html", good}}, 400},
		{"empty title", map[string]string{"title": "", "slug": "t", "status": "draft"}, []testFile{{"file", "x.html", good}}, 400},
		{"status live", map[string]string{"title": "T", "slug": "live", "status": "live"}, []testFile{{"file", "x.html", good}}, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, false)
			sid := h.mint(testOwnerID, "o")
			body, ctype := multipartBody(t, c.fields, c.files...)
			res := h.serve("POST", "/admin/reports", h.withSID(sid), withBody(ctype, body))
			if res.Status != c.status {
				t.Fatalf("status %d, want %d: %s", res.Status, c.status, res.Body)
			}
			if !hasMarker(res.Body) || !strings.Contains(res.Body, `role="alert"`) {
				t.Errorf("the refusal does not say why on the admin page")
			}
			if n := h.count("reports"); n != 0 {
				t.Fatalf("%d report rows after a refusal", n)
			}
			if es, _ := os.ReadDir(filepath.Join(h.dataDir, "reports")); len(es) != 0 {
				t.Fatalf("files in reports/ after a refusal: %v", es)
			}
			if names := h.tmpEntries(); len(names) != 0 {
				t.Fatalf("tmp/ = %v", names)
			}
		})
	}

	t.Run("a body of exactly the cap", func(t *testing.T) {
		h := newHarness(t, false)
		sid := h.mint(testOwnerID, "o")
		data := reportHTML(testUploadCap, "cap")
		if res := h.upload(sid, "cap", "Cap", "published", data); res.Status != 303 {
			t.Fatalf("a file of exactly the cap: %d %s", res.Status, res.Body)
		}
		if id, _, _, _ := h.reportRow("cap"); id == 0 {
			t.Fatal("not stored")
		}
	})

	t.Run("not multipart", func(t *testing.T) {
		h := newHarness(t, false)
		sid := h.mint(testOwnerID, "o")
		res := h.serve("POST", "/admin/reports", h.withSID(sid), withBody("application/x-www-form-urlencoded", []byte("title=x&slug=x&status=draft")))
		if res.Status != 400 || h.count("reports") != 0 {
			t.Fatalf("%d, %d rows", res.Status, h.count("reports"))
		}
	})
}

// Cross-origin POSTs are refused before any handler runs, so nothing changes.
func TestAdminReportPostsAreCrossOriginProtected(t *testing.T) {
	h := newHarness(t, false)
	sid := h.mint(testOwnerID, "o")
	if res := h.upload(sid, "target", "Target", "published", reportHTML(300, "t")); res.Status != 303 {
		t.Fatalf("setup upload: %d", res.Status)
	}
	id, _, pub, _ := h.reportRow("target")

	hdrs := [][2]string{{"Sec-Fetch-Site", "cross-site"}, {"Sec-Fetch-Site", "same-site"}, {"Origin", "https://evil.example"}}
	for _, hdr := range hdrs {
		extra := withHeader(hdr[0], hdr[1])
		if res := h.upload(sid, "target", "Hijacked", "draft", reportHTML(400, "evil"), extra); res.Status != 403 {
			t.Errorf("%s %s: upload answered %d, want 403", hdr[0], hdr[1], res.Status)
		}
		if res := h.upload(sid, "brand-new", "New", "published", reportHTML(400, "evil"), extra); res.Status != 403 {
			t.Errorf("%s %s: a new upload answered %d, want 403", hdr[0], hdr[1], res.Status)
		}
		for _, act := range []string{"publish", "unpublish", "delete"} {
			if res := h.serve("POST", "/admin/reports/target/"+act, h.withSID(sid), extra); res.Status != 403 {
				t.Errorf("%s %s: %s answered %d, want 403", hdr[0], hdr[1], act, res.Status)
			}
		}
	}
	if _, status, pub2, ok := h.reportRow("target"); !ok || status != "published" || pub2 != pub {
		t.Fatalf("a cross-origin request changed the report: %q %v", status, pub2)
	}
	if got, _ := h.reportFile(id); !bytes.Equal(got, reportHTML(300, "t")) {
		t.Fatal("a cross-origin request changed the file")
	}
	if _, _, _, ok := h.reportRow("brand-new"); ok {
		t.Fatal("a cross-origin upload created a report")
	}

	// The site's own origin is fine.
	if res := h.upload(sid, "target", "Same origin", "published", reportHTML(350, "ok"), withHeader("Origin", h.cfg.Origin)); res.Status != 303 {
		t.Fatalf("an upload from the site's own origin: %d", res.Status)
	}
}

func TestOnlyTheOwnerCanManageReports(t *testing.T) {
	h := newHarness(t, false)
	owner := h.mint(testOwnerID, "o")
	if res := h.upload(owner, "secret", "Secret", "draft", reportHTML(300, "the-secret-bytes")); res.Status != 303 {
		t.Fatalf("setup: %d", res.Status)
	}
	other := h.mint(7, "somebody")

	for who, opts := range map[string][]func(*http.Request){"anonymous": nil, "non-owner": {h.withSID(other)}} {
		body, ctype := multipartBody(t, map[string]string{"title": "x", "slug": "intruder", "status": "published"},
			testFile{"file", "x.html", reportHTML(300, "intruder")})
		res := h.serve("POST", "/admin/reports", append([]func(*http.Request){withBody(ctype, body)}, opts...)...)
		if res.Status != 403 {
			t.Errorf("%s upload: %d, want 403", who, res.Status)
		}
		for _, act := range []string{"publish", "unpublish", "delete"} {
			if res := h.serve("POST", "/admin/reports/secret/"+act, opts...); res.Status != 403 {
				t.Errorf("%s %s: %d, want 403", who, act, res.Status)
			}
		}
		for _, p := range []string{"/admin/reports/secret", "/admin/reports/secret/raw"} {
			res := h.serve("GET", p, opts...)
			if res.Status == 200 || hasMarker(res.Body) || strings.Contains(res.Body, "the-secret-bytes") || strings.Contains(res.Body, "Secret") {
				t.Errorf("%s GET %s: %d leaked", who, p, res.Status)
			}
		}
	}
	if _, status, _, ok := h.reportRow("secret"); !ok || status != "draft" {
		t.Fatalf("the report changed: %q %v", status, ok)
	}
	if _, _, _, ok := h.reportRow("intruder"); ok {
		t.Fatal("a non-owner created a report")
	}
}

func TestReportBytesNeverAppearInSitePages(t *testing.T) {
	h := newHarness(t, false)
	sid := h.mint(testOwnerID, "o")
	data := reportHTML(500, "UNIQUE-REPORT-MARKER")
	if res := h.upload(sid, "inline-check", "Inline check", "published", data); res.Status != 303 {
		t.Fatalf("upload: %d", res.Status)
	}
	for _, p := range []string{"/", "/admin", "/admin/reports/inline-check"} {
		if res := h.serve("GET", p, h.withSID(sid)); strings.Contains(res.Body, "UNIQUE-REPORT-MARKER") {
			t.Errorf("GET %s contains report bytes", p)
		}
	}
	if res := h.serve("GET", "/admin/reports/inline-check/raw", h.withSID(sid)); !strings.Contains(res.Body, "UNIQUE-REPORT-MARKER") {
		t.Fatal("control: the preview does not serve the bytes")
	}
}

// On Windows a replace cannot set aside a file somebody is reading: the owner
// is told, and the old report is untouched.
func TestReplaceWhileThePreviewIsOpen(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("renaming an open file only fails on Windows")
	}
	h := newHarness(t, false)
	sid := h.mint(testOwnerID, "o")
	v1 := reportHTML(400, "v1")
	if res := h.upload(sid, "busy", "Busy", "published", v1); res.Status != 303 {
		t.Fatalf("upload: %d", res.Status)
	}
	id, _, _, _ := h.reportRow("busy")
	_, f, err := h.lib.OpenRaw(bg, "busy", false)
	if err != nil {
		t.Fatal(err)
	}
	res := h.upload(sid, "busy", "Changed", "published", reportHTML(500, "v2"))
	f.Close()
	if res.Status != http.StatusConflict || !strings.Contains(res.Body, "Nothing changed") {
		t.Fatalf("replace of an open report: %d %s", res.Status, res.Body)
	}
	if got, _ := h.reportFile(id); !bytes.Equal(got, v1) {
		t.Fatal("the old file changed")
	}
	if names := h.tmpEntries(); len(names) != 0 {
		t.Fatalf("tmp/ = %v", names)
	}
	if res := h.upload(sid, "busy", "Changed", "published", reportHTML(500, "v2")); res.Status != 303 {
		t.Fatalf("the retry after the reader let go: %d", res.Status)
	}
}
