// E-001, finish line 3: A report saved as a draft or unpublished answers 404 at /reports/<slug> to a visitor who is not signed in and never shows on the home page; a deleted report answers 404 and its stored file is gone.
//
// It builds ./cmd/hband from this tree, runs it with the HBAND_* settings in start, and drives it
// over HTTP as the VPS proxy forwards a browser, signing in through a stand-in GitHub of its own.
package e001hiddenanddeleted

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/fs"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const site, chrome = "https://hband.test", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36"

type account struct {
	id           int
	login, image string
}

var owner, stranger = account{4242, "report-author", "https://avatars.example/4242"}, account{9001, "passer-by", "https://avatars.example/9001"}

func hexes(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func mark(word string) string { return word + "-" + hexes(6) }

func report(title, marker, extra string) []byte {
	return []byte(`<!doctype html><html><head><meta charset="utf-8"><title>` + title + `</title><style>body{margin:2em}</style></head><body><h1>` +
		title + `</h1><p>` + marker + `</p>` + extra + `</body></html>`)
}

type page struct {
	url    string
	status int
	header http.Header
	text   string
}

// app is the server `go build ./cmd/hband` makes of this tree, on a free port with a fresh data
// folder, the owner pinned by GitHub user ID and a stand-in GitHub: a code it gave out buys a
// token at /login/oauth/access_token, and the token buys that account at /user.
type app struct {
	t          *testing.T
	data, port string
	gh         *httptest.Server
	mu         sync.Mutex
	grants     map[string]account
	cmd        *exec.Cmd
	done       chan bool
}

func start(t *testing.T, maxUpload int) *app {
	work, _ := os.MkdirTemp("", "hband-acceptance-")
	a := &app{t: t, data: filepath.Join(work, "data"), grants: map[string]account{}, done: make(chan bool)}
	t.Cleanup(func() {
		if a.halt(); a.gh != nil {
			a.gh.Close()
		}
		for i := 0; i < 20 && os.RemoveAll(work) != nil; i++ {
			time.Sleep(250 * time.Millisecond)
		}
	})
	exe, root := filepath.Join(work, "hband.exe"), filepath.Join("..", "..", "..")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", exe, "./cmd/hband")
	build.Dir, build.Env = root, append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("the product's build failed: `go build ./cmd/hband` in %s: %v\n%s", root, err, out)
	}
	a.gh = httptest.NewServer(http.HandlerFunc(a.github))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a.port = strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	l.Close()
	log, _ := os.Create(filepath.Join(work, "server.log"))
	os.MkdirAll(a.data, 0o755)
	a.cmd = exec.Command(exe)
	a.cmd.Dir, a.cmd.Stdout, a.cmd.Stderr = work, log, log
	a.cmd.Env = append(os.Environ(), "HBAND_ADDR=127.0.0.1:"+a.port, "HBAND_DATA_DIR="+a.data, "HBAND_BASE_URL="+site,
		"HBAND_OWNER_GITHUB_ID=4242", "HBAND_GITHUB_CLIENT_ID=hband-test", "HBAND_GITHUB_CLIENT_SECRET=hband-test-secret",
		"HBAND_GITHUB_URL="+a.gh.URL, "HBAND_GITHUB_API_URL="+a.gh.URL, "HBAND_MAX_UPLOAD_BYTES="+strconv.Itoa(maxUpload),
		"HBAND_TRUSTED_PROXIES=127.0.0.1")
	if err := a.cmd.Start(); err != nil {
		t.Fatalf("the server did not start: %v", err)
	}
	go func() {
		a.cmd.Wait()
		log.Close()
		close(a.done)
	}()
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(250 * time.Millisecond) {
		if _, err := a.browser("192.0.2.1", chrome).send("GET", "/", nil, nil); err == nil {
			return a
		}
		select {
		case <-a.done:
			f, _ := os.Open(log.Name())
			said, _ := io.ReadAll(f)
			f.Close()
			t.Fatalf("the server exited (%v):\n%s", a.cmd.ProcessState, said)
		default:
			if time.Now().After(deadline) {
				t.Fatalf("the server did not answer in a minute")
			}
		}
	}
}

func (a *app) halt() {
	if a.cmd != nil && a.cmd.Process != nil {
		a.cmd.Process.Kill()
		<-a.done
	}
}

func (a *app) github(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sent, _ := io.ReadAll(r.Body)
	asked, _ := url.ParseQuery(string(sent) + "&" + r.URL.RawQuery)
	auth := strings.Fields("- " + r.Header.Get("Authorization"))
	code, tok := asked.Get("code"), auth[len(auth)-1]
	w.Header().Set("Content-Type", "application/json")
	if u, ok := a.grants[code]; ok && !strings.HasPrefix(code, "gho_") && r.Method == "POST" &&
		strings.Contains(r.URL.Path, "/login/oauth/access_token") {
		delete(a.grants, code)
		tok = "gho_" + hexes(16)
		a.grants[tok] = u
		if !strings.Contains(r.Header.Get("Accept"), "json") {
			w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
			io.WriteString(w, "access_token="+tok+"&token_type=bearer&scope=")
			return
		}
		fmt.Fprintf(w, `{"access_token": %q, "token_type": "bearer", "scope": ""}`, tok)
	} else if u, ok := a.grants[tok]; ok && strings.HasPrefix(tok, "gho_") && strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/user") {
		json.NewEncoder(w).Encode(map[string]any{"id": u.id, "login": u.login, "avatar_url": u.image, "name": u.login, "type": "User"})
	} else {
		w.WriteHeader(401)
		io.WriteString(w, `{"message": "Bad credentials"}`)
	}
}

var plain = &http.Transport{DisableKeepAlives: true, DisableCompression: true}

// via sends a browser's request as the VPS proxy forwards it: to this server, with the public
// host, https, and the visitor's address in X-Forwarded-For and X-Real-IP.
type via struct{ port, ip, agent string }

func (v via) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = "http", "127.0.0.1:"+v.port
	for k, x := range map[string]string{"User-Agent": v.agent, "Accept": "text/html,*/*", "X-Forwarded-For": v.ip, "X-Real-IP": v.ip, "X-Forwarded-Proto": "https"} {
		r.Header.Set(k, x)
	}
	return plain.RoundTrip(r)
}

type browser struct {
	t *testing.T
	c *http.Client
}

func (a *app) browser(ip, agent string) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{a.t, &http.Client{Jar: jar, Timeout: time.Minute, Transport: via{a.port, ip, agent},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func resolve(base, ref string) (string, error) {
	b, err := url.Parse(base)
	r, err2 := url.Parse(strings.TrimSpace(ref))
	if err != nil || err2 != nil {
		return "", fmt.Errorf("%q from %q does not parse", ref, base)
	}
	return b.ResolveReference(r).String(), nil
}

func (b *browser) send(method, target string, body []byte, headers map[string]string) (*page, error) {
	to, err := resolve(site+"/", target)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequest(method, to, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	text, err := io.ReadAll(resp.Body)
	return &page{to, resp.StatusCode, resp.Header, string(text)}, err
}

func (b *browser) do(method, target string, body []byte, headers map[string]string) *page {
	b.t.Helper()
	pg, err := b.send(method, target, body, headers)
	if err != nil {
		b.t.Fatalf("%s %s: %v", method, target, err)
	}
	return pg
}

func (b *browser) get(target string) *page {
	b.t.Helper()
	return b.do("GET", target, nil, nil)
}

var attrRE = regexp.MustCompile(`([^\s"'<>/=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+)))?`)

func tags(text, name string) (out []map[string]string) {
	for _, m := range regexp.MustCompile(`(?i)<`+name+`\b((?:[^>"']|"[^"]*"|'[^']*')*)>`).FindAllStringSubmatch(text, -1) {
		attrs := map[string]string{}
		for _, a := range attrRE.FindAllStringSubmatch(m[1], -1) {
			attrs[strings.ToLower(a[1])] = html.UnescapeString(a[2] + a[3] + a[4])
		}
		out = append(out, attrs)
	}
	return out
}

type frame struct {
	attrs map[string]string
	got   *page
}

func (b *browser) frames(pg *page) (out []frame) {
	b.t.Helper()
	for _, attrs := range tags(pg.text, "iframe") {
		f, src := frame{attrs, nil}, strings.TrimSpace(attrs["src"])
		if to, err := resolve(pg.url, src); err == nil && src != "" && !regexp.MustCompile(`(?i)^(about|data|javascript|blob):`).MatchString(src) {
			f.got = b.get(to)
		}
		out = append(out, f)
	}
	return out
}

// signIn is a browser that went through GitHub sign-in as user, and the answer when GitHub sent it back.
func (a *app) signIn(user account, ip string) (*browser, *page) {
	a.t.Helper()
	b := a.browser(ip, chrome)
	start := b.get("/auth/github/login")
	to := start.header.Get("Location")
	u, err := url.Parse(to)
	if start.status/100 != 3 || err != nil || !strings.HasPrefix(to, a.gh.URL+"/login/oauth/authorize") {
		a.t.Fatalf("GET /auth/github/login answered %d %q, not GitHub's authorize page", start.status, to)
	}
	code, back := hexes(10), u.Query().Get("redirect_uri")
	a.mu.Lock()
	a.grants[code] = user
	a.mu.Unlock()
	if back == "" {
		back = site + "/auth/github/callback"
	}
	cb, err := url.Parse(back)
	if err != nil {
		a.t.Fatalf("the redirect_uri %q: %v", back, err)
	}
	q := cb.Query()
	q.Set("code", code)
	q.Set("state", u.Query().Get("state"))
	cb.RawQuery, cb.Fragment = q.Encode(), ""
	return b, b.get(cb.String())
}

// form is what the admin page's forms send besides their own fields: its CSRF token, from hidden
// inputs or a meta tag sent as X-CSRF-Token.
func (b *browser) form() (url.Values, map[string]string) {
	b.t.Helper()
	pg, fields, headers := b.get("/admin"), url.Values{}, map[string]string{}
	for _, n := range tags(pg.text, "input") {
		if strings.EqualFold(n["type"], "hidden") && regexp.MustCompile(`(?i)csrf|xsrf|token|authenticity`).MatchString(n["name"]) {
			fields.Set(n["name"], n["value"])
		}
	}
	for _, n := range tags(pg.text, "meta") {
		if _, set := headers["X-CSRF-Token"]; !set && regexp.MustCompile(`(?i)csrf|xsrf`).MatchString(n["name"]) {
			headers["X-CSRF-Token"] = n["content"]
		}
	}
	return fields, headers
}

type part struct {
	name string
	data []byte
	kind string
}

func (b *browser) upload(slug, title, status string, files ...part) (*page, error) {
	b.t.Helper()
	fields, headers := b.form()
	fields.Set("title", title)
	fields.Set("slug", slug)
	fields.Set("status", status)
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for k := range fields {
		w.WriteField(k, fields.Get(k))
	}
	for _, f := range files {
		p, _ := w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="file"; filename="` + f.name + `"`}, "Content-Type": {f.kind}})
		p.Write(f.data)
	}
	w.Close()
	headers["Content-Type"] = w.FormDataContentType()
	return b.send("POST", "/admin/reports", body.Bytes(), headers)
}

func (b *browser) publish(slug, title string, doc []byte, status string) {
	b.t.Helper()
	got, err := b.upload(slug, title, status, part{slug + ".html", doc, "text/html"})
	if err != nil {
		b.t.Fatalf("uploading %s (%s): %v", slug, status, err)
	}
	if got.status < 200 || got.status >= 400 {
		b.t.Fatalf("uploading %s (%s) answered %d: %.300s", slug, status, got.status, got.text)
	}
}

// shows says whether one of the page's iframes loads the report holding marker.
func (b *browser) shows(pg *page, marker string) bool {
	b.t.Helper()
	for _, f := range b.frames(pg) {
		if f.got != nil && f.got.status == 200 && strings.Contains(f.got.text, marker) {
			return true
		}
	}
	return false
}

// act is POST /admin/reports/<slug>/<action> as the admin page's button sends it.
func (b *browser) act(slug, action string) *page {
	b.t.Helper()
	fields, headers := b.form()
	headers["Content-Type"] = "application/x-www-form-urlencoded"
	return b.do("POST", "/admin/reports/"+slug+"/"+action, []byte(fields.Encode()), headers)
}

// storedWith lists the files under folder, other than the SQLite database's own, that hold needle.
func storedWith(t *testing.T, folder, needle string) (hits []string) {
	t.Helper()
	err := filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if n := d.Name(); strings.HasSuffix(n, "-wal") || strings.HasSuffix(n, "-shm") || strings.HasSuffix(n, "-journal") {
			return nil
		}
		f, err := os.Open(path)
		if os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
		data, err := io.ReadAll(f)
		f.Close()
		if bytes.Contains(data, []byte(needle)) && !bytes.HasPrefix(data, []byte("SQLite format 3\x00")) {
			hits = append(hits, path)
		}
		return err
	})
	if err != nil {
		t.Fatalf("reading what is stored under %s: %v", folder, err)
	}
	return hits
}

func TestE001HiddenAndDeleted(t *testing.T) {
	a := start(t, 10<<20)
	own, _ := a.signIn(owner, "192.0.2.10")
	v := a.browser("192.0.2.50", chrome)
	kept := mark("KEPT")
	home := func(title, marker string) {
		t.Helper()
		if pg := v.get("/"); strings.Contains(pg.text, title) || v.shows(pg, marker) || !v.shows(pg, kept) {
			t.Fatalf("the home page names or shows %q, or does not show the newest report still published", title)
		}
	}
	// served is where the page's iframes load the report holding marker; gone checks nothing is there now.
	served := func(slug, marker string) (urls []string) {
		t.Helper()
		for _, f := range v.frames(v.get("/reports/" + slug)) {
			if f.got != nil && f.got.status == 200 && strings.Contains(f.got.text, marker) {
				urls = append(urls, f.got.url)
			}
		}
		if len(urls) == 0 {
			t.Fatalf("the published report does not show at /reports/%s", slug)
		}
		return urls
	}
	gone := func(slug, marker string, urls []string) {
		t.Helper()
		if s := v.get("/reports/" + slug).status; s != 404 {
			t.Fatalf("/reports/%s answers %d to a visitor who is not signed in, not 404", slug, s)
		}
		for _, u := range urls {
			if got := v.get(u); got.status == 200 && strings.Contains(got.text, marker) {
				t.Fatalf("the report's HTML is still served at %s", u)
			}
		}
	}
	own.publish("e001-kept-report", "Kept Report", report("Kept Report", kept, ""), "published")
	time.Sleep(1200 * time.Millisecond)
	draft := mark("DRAFT")
	own.publish("e001-draft-report", "Draft Report", report("Draft Report", draft, ""), "draft")
	gone("e001-draft-report", draft, nil)
	home("Draft Report", draft)

	time.Sleep(1200 * time.Millisecond)
	withdrawn := mark("WITHDRAWN")
	own.publish("e001-withdrawn-report", "Withdrawn Report", report("Withdrawn Report", withdrawn, ""), "published")
	urls := served("e001-withdrawn-report", withdrawn)
	if !v.shows(v.get("/"), withdrawn) {
		t.Fatalf("the newest published report is not on the home page")
	}
	if got := own.act("e001-withdrawn-report", "unpublish"); got.status >= 400 {
		t.Fatalf("unpublishing answered %d", got.status)
	}
	gone("e001-withdrawn-report", withdrawn, urls)
	home("Withdrawn Report", withdrawn)

	time.Sleep(1200 * time.Millisecond)
	doomed := mark("DELETED")
	own.publish("e001-deleted-report", "Deleted Report", report("Deleted Report", doomed, ""), "published")
	urls = served("e001-deleted-report", doomed)
	if len(storedWith(t, a.data, doomed)) == 0 {
		t.Fatalf("the uploaded report is not kept as a file under HBAND_DATA_DIR")
	}
	if got := own.act("e001-deleted-report", "delete"); got.status >= 400 {
		t.Fatalf("deleting answered %d", got.status)
	}
	gone("e001-deleted-report", doomed, urls)
	if left := storedWith(t, a.data, doomed); len(left) > 0 {
		t.Fatalf("the deleted report's file is still stored: %v", left)
	}
	home("Deleted Report", doomed)
}
