package server

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sshubham2/hband/internal/testutil/fakegithub"
)

var anchorRE = regexp.MustCompile(`<a[^>]*href="(/auth/github/login\?next=[^"]*)"`)

func TestHomeSignInIsAnchor(t *testing.T) {
	h := newHarness(t, false)
	res := h.get(h.client(false), "/")
	if res.Status != 200 {
		t.Fatalf("GET /: %d", res.Status)
	}
	if !anchorRE.MatchString(res.Body) {
		t.Fatalf("the sign-in control is not an anchor to /auth/github/login?next=: %s", res.Body)
	}
	for _, form := range regexp.MustCompile(`(?is)<form\b[^>]*>`).FindAllString(res.Body, -1) {
		if strings.Contains(form, "/auth/github/login") {
			t.Errorf("a form targets the sign-in URL: %s", form)
		}
	}
	if strings.Contains(res.Body, "Signed in as") || hasMarker(res.Body) {
		t.Fatal("an anonymous home page shows a signed-in view")
	}
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self'") || !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("site CSP missing or weakened: %q", csp)
	}

	// Signed in: the page greets the account; only the owner sees the Admin link.
	owner := h.mint(testOwnerID, "the-owner")
	res = h.serve("GET", "/", h.withSID(owner))
	if !strings.Contains(res.Body, "Signed in as @the-owner") || !strings.Contains(res.Body, `href="/admin"`) ||
		!strings.Contains(res.Body, `action="/auth/logout"`) || anchorRE.MatchString(res.Body) {
		t.Fatalf("owner home page: %s", res.Body)
	}
	other := h.mint(7, "somebody")
	res = h.serve("GET", "/", h.withSID(other))
	if !strings.Contains(res.Body, "Signed in as @somebody") || strings.Contains(res.Body, `href="/admin"`) || hasMarker(res.Body) {
		t.Fatalf("non-owner home page: %s", res.Body)
	}
}

func TestOwnerSignInReachesAdmin(t *testing.T) {
	t.Run("from the home page anchor", func(t *testing.T) {
		h := newHarness(t, false)
		c := h.client(true) // a browser follows every redirect of the flow
		home := h.get(c, "/")
		m := anchorRE.FindStringSubmatch(home.Body)
		if m == nil {
			t.Fatalf("no sign-in anchor: %s", home.Body)
		}
		if res := h.get(c, m[1]); res.Status != 200 || !strings.Contains(res.Body, "Signed in as @octocat") {
			t.Fatalf("after sign-in: %d %s", res.Status, res.Body)
		}

		// The exact path, no trailing slash, answers 200 directly.
		nf := h.client(false)
		nf.Jar = c.Jar
		res := h.get(nf, "/admin")
		if res.Status != 200 || !hasMarker(res.Body) || !strings.Contains(res.Body, "@octocat") {
			t.Fatalf("GET /admin: %d %s", res.Status, res.Body)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("GET /admin Cache-Control = %q", res.Header.Get("Cache-Control"))
		}

		if n := h.count("sessions"); n != 1 {
			t.Fatalf("sessions rows = %d, want exactly 1", n)
		}
		cookie := h.sidFrom(c)
		if cookie == "" {
			t.Fatal("no session cookie in the jar")
		}
		var stored string
		if err := h.store.DB().QueryRow("SELECT token_hash FROM sessions").Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != sha256Hex(cookie) || stored == cookie {
			t.Fatalf("token_hash = %q, want hex(sha256(cookie)) and not the cookie %q", stored, cookie)
		}
	})

	t.Run("sent to sign-in from /admin/", func(t *testing.T) {
		h := newHarness(t, false)
		c := h.client(true)
		res := h.get(c, "/admin/")
		if res.Status != 200 || !hasMarker(res.Body) || !strings.Contains(res.Body, "@octocat") {
			t.Fatalf("the flow from /admin/ did not end on the admin page: %d %s", res.Status, res.Body)
		}
	})
}

func TestSignInRotatesSession(t *testing.T) {
	h := newHarness(t, false)
	c := h.client(false)
	if res := h.signIn(c, "/admin"); res.Status != 303 || res.Location() != "/admin" {
		t.Fatalf("first sign-in: %d %q", res.Status, res.Location())
	}
	first := h.sidFrom(c)
	if res := h.serve("GET", "/admin", h.withSID(first)); res.Status != 200 {
		t.Fatalf("first session does not open /admin: %d", res.Status)
	}
	// The browser still holds the first cookie when it signs in again.
	if res := h.signIn(c, "/admin"); res.Status != 303 {
		t.Fatalf("second sign-in: %d", res.Status)
	}
	second := h.sidFrom(c)
	if second == "" || second == first {
		t.Fatalf("the sign-in did not issue a new token (first %q second %q)", first, second)
	}
	if res := h.serve("GET", "/admin", h.withSID(first)); res.Status != 303 || !strings.HasPrefix(res.Location(), "/auth/github/login") {
		t.Fatalf("rotated-away cookie: %d %q, want anonymous 303 to login", res.Status, res.Location())
	}
	if res := h.serve("GET", "/admin", h.withSID(second)); res.Status != 200 {
		t.Fatalf("new cookie: %d", res.Status)
	}
	if n := h.count("sessions"); n != 1 {
		t.Fatalf("sessions rows = %d, want 1", n)
	}
}

func hasOAuthClear(t *testing.T, h *harness, cookies []string) bool {
	t.Helper()
	for _, line := range cookies {
		ck, err := http.ParseSetCookie(line)
		if err != nil {
			t.Fatalf("unparseable Set-Cookie %q: %v", line, err)
		}
		if ck.Name == h.oauthName() && ck.MaxAge < 0 && ck.Value == "" {
			return true
		}
	}
	return false
}

func hasSID(t *testing.T, h *harness, cookies []string) bool {
	t.Helper()
	for _, line := range cookies {
		if ck, err := http.ParseSetCookie(line); err == nil && ck.Name == h.sidName() {
			return true
		}
	}
	return false
}

func TestCallbackRefusesBadState(t *testing.T) {
	state := strings.Repeat("s", 43)
	verifier := strings.Repeat("v", 43)
	nextPart := base64.RawURLEncoding.EncodeToString([]byte("/"))
	good := state + "." + verifier + "." + nextPart

	cases := []struct {
		name, cookie, query string
	}{
		{"no cookie", "", "code=c&state=" + state},
		{"cookie with two parts", state + "." + verifier, "code=c&state=" + state},
		{"cookie with four parts", good + ".x", "code=c&state=" + state},
		{"state of the wrong length", state[:42] + "." + verifier + "." + nextPart, "code=c&state=" + state[:42]},
		{"state mismatch", good, "code=c&state=" + strings.Repeat("t", 43)},
		{"no state in the query", good, "code=c"},
		{"error=access_denied", good, "error=access_denied&state=" + state},
		{"missing code", good, "state=" + state},
		{"empty code", good, "code=&state=" + state},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, false)
			var opts []func(*http.Request)
			if tc.cookie != "" {
				opts = append(opts, withCookie(h.oauthName(), tc.cookie))
			}
			res := h.serve("GET", "/auth/github/callback?"+tc.query, opts...)
			if res.Status != 400 {
				t.Fatalf("status %d, want 400", res.Status)
			}
			if h.fake.TokenCalls() != 0 {
				t.Fatalf("the token endpoint was called %d time(s)", h.fake.TokenCalls())
			}
			if h.count("users") != 0 || h.count("sessions") != 0 {
				t.Fatal("a refused callback left rows behind")
			}
			if !hasOAuthClear(t, h, res.Cookies) || hasSID(t, h, res.Cookies) {
				t.Fatalf("cookies %v: want the OAuth cookie cleared and no session cookie", res.Cookies)
			}
		})
	}

	t.Run("fake GitHub denies at authorize", func(t *testing.T) {
		h := newHarness(t, false)
		h.fake.Configure(func(s *fakegithub.Switches) { s.DenyAtAuthorize = true })
		res := h.signIn(h.client(false), "/")
		if res.Status != 400 || h.fake.TokenCalls() != 0 || h.count("users") != 0 || h.count("sessions") != 0 {
			t.Fatalf("denied sign-in: %d token calls %d", res.Status, h.fake.TokenCalls())
		}
		if !hasOAuthClear(t, h, res.Cookies) {
			t.Fatalf("OAuth cookie not cleared: %v", res.Cookies)
		}
	})

	t.Run("control: a genuine flow creates the rows the others must not", func(t *testing.T) {
		h := newHarness(t, false)
		res := h.signIn(h.client(false), "/")
		if res.Status != 303 || h.fake.TokenCalls() != 1 || h.count("users") != 1 || h.count("sessions") != 1 {
			t.Fatalf("control flow: %d token calls %d users %d sessions %d", res.Status, h.fake.TokenCalls(), h.count("users"), h.count("sessions"))
		}
		if !hasOAuthClear(t, h, res.Cookies) || !hasSID(t, h, res.Cookies) {
			t.Fatalf("success cookies: %v", res.Cookies)
		}
	})
}

func TestCallbackRefusesGitHubFailures(t *testing.T) {
	cases := []struct {
		name string
		set  func(*fakegithub.Switches)
	}{
		{"TokenError200", func(s *fakegithub.Switches) { s.TokenError200 = true }},
		{"Token500", func(s *fakegithub.Switches) { s.Token500 = true }},
		{"wrong verifier", func(s *fakegithub.Switches) { s.WrongVerifier = true }},
		{"User401", func(s *fakegithub.Switches) { s.User401 = true }},
		{"UserMissingID", func(s *fakegithub.Switches) { s.UserMissingID = true }},
		{"UserIDZero", func(s *fakegithub.Switches) { s.UserIDZero = true }},
		{"UserIDString", func(s *fakegithub.Switches) { s.UserIDString = true }},
		{"UserIDFloat", func(s *fakegithub.Switches) { s.UserIDFloat = true }},
		{"UserEmptyLogin", func(s *fakegithub.Switches) { s.UserEmptyLogin = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, false)
			h.fake.Configure(tc.set)
			res := h.signIn(h.client(false), "/admin")
			if res.Status != 502 {
				t.Fatalf("status %d, want 502 (%s)", res.Status, res.Body)
			}
			if h.fake.TokenCalls() == 0 {
				t.Fatal("the token endpoint was never reached: the case is vacuous")
			}
			if h.count("users") != 0 || h.count("sessions") != 0 {
				t.Fatal("a refused sign-in left rows behind")
			}
			if !hasOAuthClear(t, h, res.Cookies) || hasSID(t, h, res.Cookies) {
				t.Fatalf("cookies %v: want the OAuth cookie cleared and no session cookie", res.Cookies)
			}
			if strings.Contains(res.Body, testSecret) {
				t.Fatal("the refusal leaks the client secret")
			}
		})
	}
}

func TestLoginNextIsLocalOnly(t *testing.T) {
	h := newHarness(t, false)
	bad := []string{
		"//evil.example", "/\\evil.example", "/./\\evil.example", "/x/../\\evil.example", "/admin/../\\evil.example",
		"/%5Cevil.example", "/%5cevil.example", "https://evil.example/x", "javascript:alert(1)",
		"\t/evil.example", "/\t/evil.example", "/a\x00b", "/ a", "/" + strings.Repeat("a", 512),
	}
	for _, next := range bad {
		res := h.signIn(h.client(false), next)
		if res.Status != 303 || res.Location() != "/" {
			t.Errorf("next %q: callback answered %d Location %q, want 303 to /", next, res.Status, res.Location())
		}
	}
	for _, next := range []string{"/admin", "/reports/a-b?x=1"} {
		res := h.signIn(h.client(false), next)
		if res.Status != 303 || res.Location() != next {
			t.Errorf("next %q: callback answered %d Location %q, want it kept verbatim", next, res.Status, res.Location())
		}
	}

	// The callback checks the value again: a cookie whose next part was
	// swapped after the login step still cannot leave the site.
	for _, next := range bad {
		c := h.client(false)
		res, _ := h.walk(c, h.srv.URL+"/auth/github/login", func(v string) string {
			parts := strings.Split(v, ".")
			parts[2] = base64.RawURLEncoding.EncodeToString([]byte(next))
			return strings.Join(parts, ".")
		})
		if res.Status != 303 || res.Location() != "/" {
			t.Errorf("tampered next %q: callback answered %d Location %q, want 303 to /", next, res.Status, res.Location())
		}
	}

	// From the gate: an admin URL that is not a safe local path never travels as next.
	anon := h.client(false)
	gate := h.get(anon, "/admin/%2e%2e/%5Cevil.example")
	if gate.Status != 303 || !strings.HasPrefix(gate.Location(), "/auth/github/login") {
		t.Fatalf("gate: %d %q", gate.Status, gate.Location())
	}
	res, _ := h.walk(anon, h.srv.URL+gate.Location(), nil)
	if res.Status != 303 || res.Location() != "/" {
		t.Fatalf("flow from the gate ended %d %q, want 303 to /", res.Status, res.Location())
	}

	// A next that is fine travels through the gate and back.
	gate = h.get(h.client(false), "/admin/")
	if want := "/auth/github/login?next=" + url.QueryEscape("/admin/"); gate.Location() != want {
		t.Fatalf("gate Location %q, want %q", gate.Location(), want)
	}
	res, _ = h.walk(h.client(false), h.srv.URL+gate.Location(), nil)
	if res.Status != 303 || res.Location() != "/admin/" {
		t.Fatalf("flow from /admin/ ended %d %q", res.Status, res.Location())
	}
}

func TestCookieAttributes(t *testing.T) {
	check := func(t *testing.T, h *harness, lines []string, wantSecure bool) (clears, sets int) {
		t.Helper()
		for _, line := range lines {
			ck, err := http.ParseSetCookie(line)
			if err != nil {
				t.Errorf("unparseable Set-Cookie %q: %v", line, err)
				continue
			}
			wantPrefix := ""
			if wantSecure {
				wantPrefix = "__Host-"
			}
			if ck.Name != wantPrefix+"hband_sid" && ck.Name != wantPrefix+"hband_oauth" {
				t.Errorf("unexpected cookie name %q in %q", ck.Name, line)
			}
			if ck.Secure != wantSecure || !ck.HttpOnly || ck.Path != "/" || ck.Domain != "" || ck.SameSite != http.SameSiteLaxMode {
				t.Errorf("attributes wrong in %q: %+v", line, ck)
			}
			if ck.Value == "" {
				clears++
				if ck.MaxAge > 0 || ck.MaxAge == 0 {
					t.Errorf("a clear without Max-Age <= 0: %q", line)
				}
			} else {
				sets++
				if ck.MaxAge <= 0 {
					t.Errorf("a set cookie without a positive Max-Age: %q", line)
				}
			}
		}
		return
	}

	t.Run("https base", func(t *testing.T) {
		var all []string
		add := func(lines ...string) { all = append(all, lines...) }

		// login, a callback refusal, a store failure (503), and the success path.
		h := newHarness(t, true)
		if !h.cfg.SecureCookies {
			t.Fatal("an https base did not turn Secure cookies on")
		}
		_, cookies := h.walk(h.client(false), h.srv.URL+"/auth/github/login", nil)
		add(cookies...)
		h.fake.Configure(func(s *fakegithub.Switches) { s.User401 = true })
		res, cookies := h.walk(h.client(false), h.srv.URL+"/auth/github/login", nil)
		if res.Status != 502 {
			t.Fatalf("refusal: %d", res.Status)
		}
		add(cookies...)
		h.fake.Configure(func(s *fakegithub.Switches) { s.User401 = false })
		c := h.client(false)
		res, cookies = h.walk(c, h.srv.URL+"/auth/github/login", nil)
		if res.Status != 303 {
			t.Fatalf("success: %d", res.Status)
		}
		add(cookies...)
		bad := h.serve("GET", "/auth/github/callback?code=c", withCookie(h.oauthName(), "junk"))
		add(bad.Cookies...)

		// logout success
		out := h.serve("POST", "/auth/logout", h.withSID(h.sidFrom(c)))
		if out.Status != 303 {
			t.Fatalf("logout: %d", out.Status)
		}
		add(out.Cookies...)

		// store closed: callback 503 and logout failure
		h2 := newHarness(t, true)
		session := h2.mint(testOwnerID, "o")
		h2.store.Close()
		res, cookies = h2.walk(h2.client(false), h2.srv.URL+"/auth/github/login", nil)
		if res.Status != 503 {
			t.Fatalf("callback with the store closed: %d", res.Status)
		}
		add(cookies...)
		fail := h2.serve("POST", "/auth/logout", h2.withSID(session))
		if fail.Status != 503 {
			t.Fatalf("logout with the store closed: %d", fail.Status)
		}
		add(fail.Cookies...)

		clears, sets := check(t, h, all, true)
		if clears < 5 || sets < 4 {
			t.Fatalf("only %d clears and %d sets were collected: the check is not seeing the paths", clears, sets)
		}
	})

	t.Run("loopback http base", func(t *testing.T) {
		h := newHarness(t, false)
		if h.cfg.SecureCookies {
			t.Fatal("a loopback http base turned Secure cookies on")
		}
		c := h.client(false)
		_, cookies := h.walk(c, h.srv.URL+"/auth/github/login", nil)
		out := h.serve("POST", "/auth/logout", h.withSID(h.sidFrom(c)))
		clears, sets := check(t, h, append(cookies, out.Cookies...), false)
		if clears < 2 || sets < 2 {
			t.Fatalf("only %d clears and %d sets collected", clears, sets)
		}
	})
}
