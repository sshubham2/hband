package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sshubham2/hband/internal/config"
)

func TestIsOwner(t *testing.T) {
	cases := []struct {
		owner, id int64
		want      bool
	}{
		{0, 0, false}, {-1, -1, false}, {42, 0, false}, {42, 43, false}, {42, 42, true},
		{0, 42, false}, {42, -42, false},
	}
	for _, c := range cases {
		if got := IsOwner(c.owner, c.id); got != c.want {
			t.Errorf("IsOwner(%d, %d) = %v, want %v", c.owner, c.id, got, c.want)
		}
	}
}

func TestOwnerRouteWithoutProof(t *testing.T) {
	calls := 0
	var got Owner
	h := OwnerRoute(func(w http.ResponseWriter, r *http.Request, o Owner) {
		calls++
		got = o
		w.WriteHeader(http.StatusOK)
	})
	serve := func(ctx context.Context) int {
		req := httptest.NewRequest("GET", "/admin", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	bg := context.Background()

	refused := map[string]context.Context{
		"bare request": bg,
		"viewer who is not the owner": context.WithValue(bg, viewerKey{},
			Viewer{GitHubID: 7, Login: "x", IsOwner: false}),
		"viewer flagged IsOwner but no proof": context.WithValue(bg, viewerKey{},
			Viewer{GitHubID: 42, Login: "owner", IsOwner: true}),
	}
	for name, ctx := range refused {
		if code := serve(ctx); code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, code)
		}
		if calls != 0 {
			t.Fatalf("%s: the handler was called %d time(s)", name, calls)
		}
	}

	// Positive control: with the proof RequireOwner attaches, it is called once.
	proof := context.WithValue(bg, ownerKey{}, Owner{GitHubID: 42, Login: "owner"})
	if code := serve(proof); code != http.StatusOK || calls != 1 {
		t.Fatalf("with proof: status %d calls %d, want 200 and 1", code, calls)
	}
	if got != (Owner{GitHubID: 42, Login: "owner"}) {
		t.Fatalf("handler received %+v", got)
	}
}

func TestRequireOwnerAttachesProofOnlyForOwner(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := OwnerFrom(r.Context()); !ok {
			t.Error("inner handler reached without proof")
		}
		w.WriteHeader(http.StatusOK)
	})
	gate := RequireOwner(inner)
	withViewer := func(v *Viewer, method string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/admin", nil)
		if v != nil {
			req = req.WithContext(context.WithValue(req.Context(), viewerKey{}, *v))
		}
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, req)
		return rec
	}
	if rec := withViewer(&Viewer{GitHubID: 42, Login: "o", IsOwner: true}, "GET"); rec.Code != 200 {
		t.Fatalf("owner: %d", rec.Code)
	}
	nonOwner := withViewer(&Viewer{GitHubID: 7, Login: "n"}, "GET")
	if nonOwner.Code != 403 || !strings.Contains(nonOwner.Body.String(), "This GitHub account cannot open admin pages") ||
		strings.Contains(nonOwner.Body.String(), "data-hband-admin") {
		t.Fatalf("non-owner: %d %q", nonOwner.Code, nonOwner.Body.String())
	}
	if rec := withViewer(nil, "GET"); rec.Code != 303 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("anonymous GET: %d %v", rec.Code, rec.Header())
	}
	if rec := withViewer(nil, "POST"); rec.Code != 403 {
		t.Fatalf("anonymous POST: %d", rec.Code)
	}
}

func TestLocalPathCorpus(t *testing.T) {
	bad := []string{
		"", "x", "admin", "//evil.example", "/\\evil.example", "/./\\evil.example", "/x/../\\evil.example",
		"/admin/../\\evil.example", "/%5Cevil.example", "/%5cevil.example", "/a%5Cb", "https://evil.example/x",
		"javascript:alert(1)", "\t/evil.example", "/\t/evil.example", "/\n/evil.example", "/\r/evil.example", "/a\x00b",
		"/ a", " /a", "/a ", "/\x7f", "/é", "/a\\b", "http:/evil.example", "/" + strings.Repeat("a", 512),
		"data:text/html,x", "//", "/\\", "\\\\evil.example",
	}
	for _, s := range bad {
		if got, ok := LocalPath(s); ok || got != "/" {
			t.Errorf("LocalPath(%q) = %q, %v; want \"/\", false", s, got, ok)
		}
	}
	good := []string{"/", "/admin", "/admin/", "/reports/a-b?x=1", "/a//b", "/..//evil.example", "/%2e%2e/x", "/a?next=%2Fb",
		"/" + strings.Repeat("a", 511)}
	for _, s := range good {
		if got, ok := LocalPath(s); !ok || got != s {
			t.Errorf("LocalPath(%q) = %q, %v; want it kept verbatim", s, got, ok)
		}
	}
}

func TestSeeOtherWritesVerbatim(t *testing.T) {
	for _, target := range []string{"/admin", "/reports/a-b?x=1", "/a//b/./c"} {
		rec := httptest.NewRecorder()
		SeeOther(rec, target)
		if rec.Code != 303 || rec.Header().Get("Location") != target {
			t.Errorf("SeeOther(%q): %d %q", target, rec.Code, rec.Header().Get("Location"))
		}
	}
	for _, target := range []string{"//evil.example", "/./\\evil.example", "https://evil.example"} {
		rec := httptest.NewRecorder()
		SeeOther(rec, target)
		if rec.Code != 303 || rec.Header().Get("Location") != "/" {
			t.Errorf("SeeOther(%q): %d %q, want 303 to /", target, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestToProviderRefusesOtherTargets(t *testing.T) {
	cfg := config.Config{GitHub: config.GitHubEndpoints{AuthURL: "https://github.com/login/oauth/authorize"}}
	rec := httptest.NewRecorder()
	ToProvider(rec, cfg, "https://github.com/login/oauth/authorize?client_id=x")
	if rec.Code != 302 || rec.Header().Get("Location") != "https://github.com/login/oauth/authorize?client_id=x" {
		t.Fatalf("good target: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, target := range []string{"https://evil.example/login/oauth/authorize?x=1", "https://github.com.evil.example/?x", "/admin",
		"https://github.com/login/oauth/authorize"} {
		rec := httptest.NewRecorder()
		ToProvider(rec, cfg, target)
		if rec.Code != 500 || rec.Header().Get("Location") != "" {
			t.Errorf("ToProvider(%q): %d %q", target, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestCookieHelpersAttributes(t *testing.T) {
	for _, secure := range []bool{true, false} {
		cfg := config.Config{SecureCookies: secure}
		rec := httptest.NewRecorder()
		setCookie(rec, cfg, sidBase, "value", sidMaxAge)
		clearCookie(rec, cfg, oauthBase)
		lines := rec.Header().Values("Set-Cookie")
		if len(lines) != 2 {
			t.Fatalf("got %d Set-Cookie lines", len(lines))
		}
		for i, line := range lines {
			c, err := http.ParseSetCookie(line)
			if err != nil {
				t.Fatalf("%q: %v", line, err)
			}
			wantName := []string{sidBase, oauthBase}[i]
			if secure {
				wantName = "__Host-" + wantName
			}
			if c.Name != wantName || c.Secure != secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" || c.SameSite != http.SameSiteLaxMode {
				t.Errorf("secure=%v %q parsed as %+v", secure, line, c)
			}
			if i == 1 && (c.MaxAge >= 0 || c.Value != "") {
				t.Errorf("clear is not a deletion: %+v", c)
			}
			if i == 0 && c.MaxAge != sidMaxAge {
				t.Errorf("session cookie is not persistent: %+v", c)
			}
		}
	}
}

func TestLoginTargetFallsBackToPlainLogin(t *testing.T) {
	const base = "/auth/github/login"
	target := func(uri string) string {
		req := httptest.NewRequest("GET", uri, nil)
		return loginTarget(req)
	}
	if got := target("/admin"); got != base+"?next=%2Fadmin" {
		t.Fatalf("short path: %q", got)
	}
	// 24 bytes of prefix + "%2Fadmin%2F" (11) + k = 512 at k = 477.
	exact := "/admin/" + strings.Repeat("x", 477)
	got := target(exact)
	if !strings.HasPrefix(got, base+"?next=") || len(got) != 512 {
		t.Fatalf("512-byte target: len %d %q", len(got), got[:30])
	}
	if u, _ := url.Parse(got); u.Query().Get("next") != exact {
		t.Fatal("next does not decode to the request URI")
	}
	if got := target(exact + "x"); got != base {
		t.Fatalf("513-byte target should fall back to the plain login URL, got len %d", len(got))
	}
	if got := target("/admin/" + strings.Repeat("a/", 200)); got != base {
		t.Fatalf("slash-dense path: %q", got)
	}
	if got := target("/admin/%2e%2e/%5Cevil.example"); got != base {
		t.Fatalf("backslash path must not travel as next: %q", got)
	}
}
