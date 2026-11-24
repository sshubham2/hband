package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sshubham2/hband/internal/config"
	"github.com/sshubham2/hband/internal/reports"
	"github.com/sshubham2/hband/internal/store"
	"github.com/sshubham2/hband/internal/testutil/fakegithub"
)

const (
	testOwnerID = 42
	testSecret  = "test-secret-value"
	// testUploadCap is the harness's HBAND_MAX_UPLOAD_BYTES: small enough that a
	// body of exactly the cap and one byte over are cheap to send.
	testUploadCap = 100000
)

var bg = context.Background()

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// harness is the whole app on a real listener, with a fake GitHub and a real
// SQLite file in a temp dir.
type harness struct {
	t      *testing.T
	cfg    config.Config
	store   *store.Store
	lib     *reports.Library
	dbPath  string
	dataDir string
	fake    *fakegithub.Fake
	srv    *httptest.Server
	clock  *fakeClock
	base   *url.URL

	mu      sync.Mutex
	handler http.Handler
	// app is the *app behind handler, kept so that a test can change a seam (app.tree)
	// before it sends a request. rebuild replaces both.
	app *app
}

func newHarness(t *testing.T, useTLS bool) *harness {
	t.Helper()
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "hband.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	fake := fakegithub.New("test-client", testSecret, "")
	ts := httptest.NewUnstartedServer(nil)
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	baseURL := scheme + "://" + ts.Listener.Addr().String()
	env := map[string]string{
		"HBAND_OWNER_GITHUB_ID":      "42",
		"HBAND_GITHUB_CLIENT_ID":     "test-client",
		"HBAND_GITHUB_CLIENT_SECRET": testSecret,
		"HBAND_BASE_URL":             baseURL,
		"HBAND_GITHUB_URL":           fake.URL(),
		"HBAND_GITHUB_API_URL":       fake.URL(),
		"HBAND_DATA_DIR":             dataDir,
		"HBAND_MAX_UPLOAD_BYTES":     strconv.Itoa(testUploadCap),
	}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	fake.SetRedirectURI(cfg.CallbackURL)
	h := &harness{t: t, cfg: cfg, store: st, dbPath: dbPath, dataDir: dataDir, fake: fake, srv: ts,
		clock: &fakeClock{t: time.Unix(1_800_000_000, 0)}}
	h.lib, err = reports.Open(reports.Options{DB: st.DB(), DataDir: cfg.DataDir, MaxUploadBytes: cfg.MaxUploadBytes, Now: h.clock.Now})
	if err != nil {
		t.Fatalf("reports.Open: %v", err)
	}
	h.app = newApp(cfg, st, h.lib, h.clock.Now)
	h.handler = h.app.handler()
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		cur := h.handler
		h.mu.Unlock()
		cur.ServeHTTP(w, r)
	})
	if useTLS {
		ts.StartTLS()
	} else {
		ts.Start()
	}
	h.base, _ = url.Parse(ts.URL)
	t.Cleanup(func() {
		ts.Close()
		fake.Close()
		st.Close()
	})
	return h
}

// rebuild swaps in a handler built on the same store with another owner.
func (h *harness) rebuild(owner int64) {
	cfg := h.cfg
	cfg.OwnerGitHubID = owner
	h.mu.Lock()
	h.app = newApp(cfg, h.store, h.lib, h.clock.Now)
	h.handler = h.app.handler()
	h.mu.Unlock()
}

func (h *harness) current() http.Handler {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.handler
}

func (h *harness) sidName() string {
	if h.cfg.SecureCookies {
		return "__Host-hband_sid"
	}
	return "hband_sid"
}

func (h *harness) oauthName() string {
	if h.cfg.SecureCookies {
		return "__Host-hband_oauth"
	}
	return "hband_oauth"
}

// result is a captured response.
type result struct {
	Status  int
	Header  http.Header
	Body    string
	Cookies []string // every Set-Cookie line
}

func (r result) Location() string { return r.Header.Get("Location") }

// serve sends one request straight into the handler, with the request target
// passed through verbatim and no redirect followed.
func (h *harness) serve(method, target string, opts ...func(*http.Request)) result {
	h.t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	h.current().ServeHTTP(rec, req)
	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	return result{Status: res.StatusCode, Header: res.Header, Body: string(body), Cookies: res.Header.Values("Set-Cookie")}
}

func withCookie(name, value string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: name, Value: value}) }
}

func withHeader(k, v string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(k, v) }
}

func (h *harness) withSID(value string) func(*http.Request) {
	return withCookie(h.sidName(), value)
}

// mint creates a live session for a GitHub account and returns its cookie value.
func (h *harness) mint(id int64, login string) string {
	return h.mintUser(store.User{GitHubID: id, Login: login})
}

// mintUser is mint for a user with more than an id and a login: an avatar, say.
func (h *harness) mintUser(u store.User) string {
	h.t.Helper()
	var b [32]byte
	rand.Read(b[:])
	token := base64.RawURLEncoding.EncodeToString(b[:])
	now := h.clock.Now()
	if err := h.store.SignIn(bg, store.SignInArgs{
		User:         u,
		NewTokenHash: sha256Hex(token),
		ExpiresAt:    now.Add(30 * 24 * time.Hour),
		Now:          now,
	}); err != nil {
		h.t.Fatalf("mint session: %v", err)
	}
	return token
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (h *harness) count(table string) int {
	h.t.Helper()
	return countAt(h.t, h.store, table)
}

func countAt(t *testing.T, st *store.Store, table string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// reopenCount counts rows through a fresh handle on the same file, which is
// how a store that was closed on purpose is inspected afterwards.
func (h *harness) reopenCount(table string) int {
	h.t.Helper()
	st, err := store.Open(h.dbPath)
	if err != nil {
		h.t.Fatalf("reopen: %v", err)
	}
	defer st.Close()
	return countAt(h.t, st, table)
}

// client returns a browser-like client with its own cookie jar. It never
// follows a redirect unless follow is set.
func (h *harness) client(follow bool) *http.Client {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Transport: h.srv.Client().Transport, Jar: jar, Timeout: 20 * time.Second}
	if !follow {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return c
}

func (h *harness) do(c *http.Client, method, rawURL string) result {
	h.t.Helper()
	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		h.t.Fatalf("new request %q: %v", rawURL, err)
	}
	resp, err := c.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return result{Status: resp.StatusCode, Header: resp.Header, Body: string(body), Cookies: resp.Header.Values("Set-Cookie")}
}

func (h *harness) get(c *http.Client, path string) result {
	h.t.Helper()
	return h.do(c, "GET", h.srv.URL+path)
}

// walk follows a sign-in by hand: login, then GitHub's authorize, until the
// request that reaches the app's callback, and returns the callback's own
// response. cookies holds every Set-Cookie line seen on the way. tamper, when
// set, rewrites the OAuth cookie's value after the login step.
func (h *harness) walk(c *http.Client, loginURL string, tamper func(string) string) (final result, cookies []string) {
	h.t.Helper()
	next := loginURL
	for hop := 0; hop < 8; hop++ {
		res := h.do(c, "GET", next)
		cookies = append(cookies, res.Cookies...)
		if hop == 0 && tamper != nil {
			for _, line := range res.Cookies {
				if ck, err := http.ParseSetCookie(line); err == nil && ck.Name == h.oauthName() && ck.MaxAge >= 0 {
					c.Jar.SetCookies(h.base, []*http.Cookie{{Name: ck.Name, Value: tamper(ck.Value), Path: "/"}})
				}
			}
		}
		loc := res.Location()
		if loc == "" || res.Status < 300 || res.Status > 399 {
			return res, cookies
		}
		prev, _ := url.Parse(next)
		u, err := prev.Parse(loc)
		if err != nil {
			h.t.Fatalf("bad Location %q", loc)
		}
		next = u.String()
		if u.Host == h.base.Host && u.Path == "/auth/github/callback" {
			res = h.do(c, "GET", next)
			cookies = append(cookies, res.Cookies...)
			return res, cookies
		}
	}
	h.t.Fatalf("sign-in did not reach the callback")
	return
}

// signIn runs the whole flow for a given next and returns the callback's response.
func (h *harness) signIn(c *http.Client, next string) result {
	h.t.Helper()
	u := h.srv.URL + "/auth/github/login"
	if next != "" {
		u += "?next=" + url.QueryEscape(next)
	}
	res, _ := h.walk(c, u, nil)
	return res
}

func (h *harness) sidFrom(c *http.Client) string {
	for _, ck := range c.Jar.Cookies(h.base) {
		if ck.Name == h.sidName() {
			return ck.Value
		}
	}
	return ""
}

func (h *harness) ownerClient() *http.Client {
	h.t.Helper()
	c := h.client(false)
	if res := h.signIn(c, "/"); res.Status != http.StatusSeeOther {
		h.t.Fatalf("owner sign-in: %d %s", res.Status, res.Body)
	}
	return c
}

const marker = "data-hband-admin"

func hasMarker(s string) bool { return strings.Contains(s, marker) }
