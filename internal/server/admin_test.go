package server

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

var routeParam = regexp.MustCompile(`\{[a-z]+\}`)

// adminPaths turns every adminRoutes pattern into a request path.
func adminPaths() []string {
	var out []string
	for _, rt := range adminRoutes(nil) {
		p := strings.ReplaceAll(rt.Pattern, "{$}", "")
		out = append(out, routeParam.ReplaceAllString(p, "x"))
	}
	return out
}

type variant struct{ path, loc string }

var (
	// cleaned: ServeMux redirects before any handler runs, for every method.
	cleanedVariants = []variant{{"//admin/", "/admin/"}, {"/admin/../admin/", "/admin/"}, {"//admin", "/admin"}}
	// case: ServeMux is case-sensitive, so these match no pattern.
	caseVariants = []string{"/ADMIN", "/ADMIN/"}
	// gated: these reach one of the two RequireOwner mounts.
	gatedVariants = []string{"/admin", "/admin/", "/%61dmin", "/%61dmin/", "/admin/%2e%2e/admin/", "/admin/does-not-exist"}
)

var matrixMethods = []string{"GET", "HEAD", "POST"}

// muxCleanStatus is what the standard library's ServeMux answers when it
// redirects a path it had to clean. The design recalled 301; go1.27.4's
// net/http/server.go passes StatusTemporaryRedirect (307) at every such
// redirect, and the observed response is what this class is pinned to.
const muxCleanStatus = http.StatusTemporaryRedirect

func allGated() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range append(append([]string{}, gatedVariants...), adminPaths()...) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// invariant is the one rule that decides security: whatever the variant,
// method or identity, a visitor who is not the owner never gets a 2xx, never
// the admin marker, and is only ever sent to the admin paths or to sign-in.
func invariant(t *testing.T, who, method, path string, res result) {
	t.Helper()
	if res.Status >= 200 && res.Status < 300 {
		t.Errorf("%s %s %s: status %d for a non-owner", who, method, path, res.Status)
	}
	if hasMarker(res.Body) {
		t.Errorf("%s %s %s: the admin marker reached a non-owner", who, method, path)
	}
	if loc := res.Location(); loc != "" && loc != "/admin" && loc != "/admin/" && loc != "/auth/github/login" &&
		!strings.HasPrefix(loc, "/auth/github/login?") {
		t.Errorf("%s %s %s: Location %q is neither an admin path nor sign-in", who, method, path, loc)
	}
}

// ownerRouteStatus is what the owner gets on a gated path that is not the
// landing page, per the responses observed once the report rows existed:
//   - a path no row has (/admin/does-not-exist, /admin/%2e%2e/admin/): 404 from the admin mux;
//   - a path some row has, asked with a method that row does not accept: 405
//     (GET and HEAD on the POST-only rows, POST on the GET-only rows);
//   - a row asked with its own method, with no report "x" stored: 404 from the
//     handler, except POST /admin/reports with no body, which is not a
//     multipart form: 400.
//
// A GET row also answers HEAD. Routing is not changed to fit this table.
func ownerRouteStatus(method, path string) int {
	matched := false
	for _, rt := range adminRoutes(nil) {
		p := routeParam.ReplaceAllString(strings.ReplaceAll(rt.Pattern, "{$}", ""), "x")
		if p != path {
			continue
		}
		matched = true
		if rt.Method == method || (method == "HEAD" && rt.Method == "GET") {
			if rt.Method == "POST" && rt.Pattern == "/admin/reports" {
				return http.StatusBadRequest
			}
			return http.StatusNotFound
		}
	}
	if matched {
		return http.StatusMethodNotAllowed
	}
	return http.StatusNotFound
}

func TestAdminAccessMatrix(t *testing.T) {
	h := newHarness(t, false)
	ownerSID := h.mint(testOwnerID, "the-owner")
	otherSID := h.mint(7, "somebody")

	// Positive controls (#814): the owner does get the marker, so "no marker"
	// below is not passing on a handler that never renders it.
	if res := h.serve("GET", "/admin", h.withSID(ownerSID)); res.Status != 200 || !hasMarker(res.Body) {
		t.Fatalf("control: the owner's GET /admin = %d, marker %v", res.Status, hasMarker(res.Body))
	}
	if paths := adminPaths(); len(paths) < 2 {
		t.Fatalf("adminRoutes yielded %v: the matrix would not cover the table", paths)
	}

	both := func(t *testing.T, who string, opts []func(*http.Request), gated func(t *testing.T, method, path string, res result)) {
		for _, method := range matrixMethods {
			for _, v := range cleanedVariants {
				res := h.serve(method, v.path, opts...)
				if res.Status != muxCleanStatus || res.Location() != v.loc {
					t.Errorf("%s %s %s: %d %q, want %d to %s", who, method, v.path, res.Status, res.Location(), muxCleanStatus, v.loc)
				}
				invariant(t, who, method, v.path, res)
			}
			for _, p := range caseVariants {
				res := h.serve(method, p, opts...)
				if res.Status != 404 {
					t.Errorf("%s %s %s: %d, want 404", who, method, p, res.Status)
				}
				invariant(t, who, method, p, res)
			}
			for _, p := range allGated() {
				res := h.serve(method, p, opts...)
				if res.Header.Get("Cache-Control") != "no-store" {
					t.Errorf("%s %s %s: Cache-Control %q, want no-store", who, method, p, res.Header.Get("Cache-Control"))
				}
				gated(t, method, p, res)
			}
		}
	}

	t.Run("anonymous", func(t *testing.T) {
		both(t, "anonymous", nil, func(t *testing.T, method, path string, res result) {
			invariant(t, "anonymous", method, path, res)
			if method == "POST" {
				if res.Status != 403 {
					t.Errorf("anonymous POST %s: %d, want 403", path, res.Status)
				}
				return
			}
			want := "/auth/github/login?next=" + url.QueryEscape(path)
			if res.Status != 303 || res.Location() != want {
				t.Errorf("anonymous %s %s: %d %q, want 303 to %s", method, path, res.Status, res.Location(), want)
			}
		})

		// Long URLs: the escaped sign-in URL must still be the sign-in URL.
		long := "/admin/" + strings.Repeat("a/", 200)
		exact := "/admin/" + strings.Repeat("x", 477) // login URL is exactly 512 bytes
		for _, method := range []string{"GET", "HEAD"} {
			if res := h.serve(method, long); res.Status != 303 || res.Location() != "/auth/github/login" {
				t.Errorf("%s of a slash-dense %d-byte path: %d %q, want 303 to the plain login URL", method, len(long), res.Status, res.Location())
			}
			res := h.serve(method, exact)
			next, _ := url.Parse(res.Location())
			if res.Status != 303 || !strings.HasPrefix(res.Location(), "/auth/github/login?next=") || next.Query().Get("next") != exact {
				t.Errorf("%s of the boundary path: %d %q", method, res.Status, res.Location())
			}
			if res := h.serve(method, exact+"x"); res.Status != 303 || res.Location() != "/auth/github/login" {
				t.Errorf("%s one byte over the boundary: %d %q", method, res.Status, res.Location())
			}
		}
	})

	t.Run("non_owner", func(t *testing.T) {
		opts := []func(*http.Request){h.withSID(otherSID)}
		both(t, "non-owner", opts, func(t *testing.T, method, path string, res result) {
			invariant(t, "non-owner", method, path, res)
			if res.Status != 403 {
				t.Errorf("non-owner %s %s: %d, want 403", method, path, res.Status)
			}
			if method == "GET" && !strings.Contains(res.Body, "This GitHub account cannot open admin pages") {
				t.Errorf("non-owner GET %s: the 403 page does not say why: %q", path, res.Body)
			}
		})
		// Still signed in: the home page greets the account.
		if res := h.serve("GET", "/", opts...); !strings.Contains(res.Body, "Signed in as @somebody") {
			t.Errorf("the non-owner's session is not recognised on the home page: %s", res.Body)
		}
		// Long paths do not turn a 403 into anything else.
		if res := h.serve("GET", "/admin/"+strings.Repeat("a/", 200), opts...); res.Status != 403 {
			t.Errorf("non-owner on a long path: %d", res.Status)
		}
	})

	t.Run("owner", func(t *testing.T) {
		opts := []func(*http.Request){h.withSID(ownerSID)}
		both(t, "owner", opts, func(t *testing.T, method, path string, res result) {
			if res.Status == 401 || res.Status == 403 || res.Status == 503 {
				t.Errorf("owner %s %s: %d", method, path, res.Status)
			}
			landing := path == "/admin" || path == "/admin/" || path == "/%61dmin" || path == "/%61dmin/"
			switch {
			case landing && method != "POST":
				if res.Status != 200 || (method == "GET" && !hasMarker(res.Body)) {
					t.Errorf("owner %s %s: %d marker %v, want 200 with the marker", method, path, res.Status, hasMarker(res.Body))
				}
			case landing:
				if res.Status != 405 {
					t.Errorf("owner POST %s: %d, want 405", path, res.Status)
				}
			default:
				if want := ownerRouteStatus(method, path); res.Status != want {
					t.Errorf("owner %s %s: %d, want %d from the admin mux", method, path, res.Status, want)
				}
			}
		})
	})
}

func TestPinChangeRevokesAdmin(t *testing.T) {
	h := newHarness(t, false)
	sid := h.mint(testOwnerID, "was-owner")
	if res := h.serve("GET", "/admin/", h.withSID(sid)); res.Status != 200 || !hasMarker(res.Body) {
		t.Fatalf("control: the pinned owner is refused: %d", res.Status)
	}

	h.rebuild(43) // same database, a different pin
	res := h.serve("GET", "/admin/", h.withSID(sid))
	if res.Status != 403 || hasMarker(res.Body) {
		t.Fatalf("after the pin moved to 43: %d, want 403 with no marker", res.Status)
	}
	if home := h.serve("GET", "/", h.withSID(sid)); !strings.Contains(home.Body, "Signed in as @was-owner") {
		t.Fatalf("the session should still be signed in: %s", home.Body)
	}
	if res := h.serve("GET", "/admin", h.withSID(h.mint(43, "new-owner"))); res.Status != 200 {
		t.Fatalf("the new owner is refused: %d", res.Status)
	}
}

func TestDeadSessionIsAnonymous(t *testing.T) {
	anonymous := func(t *testing.T, res result) {
		t.Helper()
		if res.Status != 303 || !strings.HasPrefix(res.Location(), "/auth/github/login") {
			t.Errorf("got %d %q, want anonymous 303 to login", res.Status, res.Location())
		}
	}

	t.Run("expired", func(t *testing.T) {
		h := newHarness(t, false)
		sid := h.mint(testOwnerID, "o")
		if res := h.serve("GET", "/admin", h.withSID(sid)); res.Status != 200 {
			t.Fatalf("control: the live session is refused: %d", res.Status)
		}
		h.clock.Advance(30*24*time.Hour + time.Second)
		anonymous(t, h.serve("GET", "/admin", h.withSID(sid)))
	})

	t.Run("malformed", func(t *testing.T) {
		h := newHarness(t, false)
		real := h.mint(testOwnerID, "o")
		h.store.Close() // any query now fails, so a 303 proves none was made
		if res := h.serve("GET", "/admin", h.withSID(real)); res.Status != 503 {
			t.Fatalf("control: a well-formed cookie should reach the closed store (503), got %d", res.Status)
		}
		for _, v := range []string{"x", strings.Repeat("a", 42), strings.Repeat("a", 44), strings.Repeat("a", 42) + "+",
			strings.Repeat("a", 42) + "=", strings.Repeat("a", 42) + ".", strings.Repeat("a", 42) + " ", "' OR 1=1 --"} {
			anonymous(t, h.serve("GET", "/admin", h.withSID(v)))
		}
	})

	t.Run("unknown token", func(t *testing.T) {
		h := newHarness(t, false)
		anonymous(t, h.serve("GET", "/admin", h.withSID(strings.Repeat("a", 43))))
	})
}

func TestStoreErrorDeniesAdmin(t *testing.T) {
	t.Run("before a callback that would succeed", func(t *testing.T) {
		h := newHarness(t, false)
		h.store.Close()
		res := h.signIn(h.client(false), "/admin")
		if res.Status != 503 {
			t.Fatalf("callback: %d %s", res.Status, res.Body)
		}
		if hasSID(t, h, res.Cookies) || !hasOAuthClear(t, h, res.Cookies) {
			t.Fatalf("cookies %v: want no session cookie and the OAuth cookie cleared", res.Cookies)
		}
		if h.fake.TokenCalls() != 1 {
			t.Fatalf("control: the flow should have reached the token endpoint (calls %d)", h.fake.TokenCalls())
		}
		if n := h.reopenCount("sessions"); n != 0 {
			t.Fatalf("%d sessions rows after reopening", n)
		}
		if n := h.reopenCount("users"); n != 0 {
			t.Fatalf("%d users rows after reopening", n)
		}
	})

	t.Run("before logout", func(t *testing.T) {
		h := newHarness(t, false)
		sid := h.mint(testOwnerID, "o")
		h.store.Close()
		res := h.serve("POST", "/auth/logout", h.withSID(sid))
		if res.Status != 503 || len(res.Cookies) != 0 {
			t.Fatalf("logout: %d cookies %v, want 503 and no Set-Cookie", res.Status, res.Cookies)
		}
		if n := h.reopenCount("sessions"); n != 1 {
			t.Fatalf("the session row did not survive a failed logout: %d", n)
		}
	})

	t.Run("after sign-in", func(t *testing.T) {
		h := newHarness(t, false)
		c := h.ownerClient()
		sid := h.sidFrom(c)
		if res := h.serve("GET", "/admin", h.withSID(sid)); res.Status != 200 || !hasMarker(res.Body) {
			t.Fatalf("control: %d", res.Status)
		}
		h.store.Close()
		res := h.serve("GET", "/admin", h.withSID(sid))
		if res.Status != 503 || hasMarker(res.Body) {
			t.Fatalf("/admin with the store down: %d marker %v", res.Status, hasMarker(res.Body))
		}
		home := h.serve("GET", "/", h.withSID(sid))
		if home.Status != 200 || strings.Contains(home.Body, "Signed in as") || !anchorRE.MatchString(home.Body) {
			t.Fatalf("GET / with the store down should render as anonymous: %d", home.Status)
		}
		if hz := h.serve("GET", "/healthz"); hz.Status != 503 {
			t.Fatalf("/healthz with the store down: %d", hz.Status)
		}
	})
}

func TestLogout(t *testing.T) {
	t.Run("same origin", func(t *testing.T) {
		for _, opts := range [][]func(*http.Request){
			{withHeader("Sec-Fetch-Site", "same-origin")},
			{}, // the finish-line harness sends neither header
		} {
			h := newHarness(t, false)
			sid := h.mint(testOwnerID, "o")
			res := h.serve("POST", "/auth/logout", append(opts, h.withSID(sid))...)
			if res.Status != 303 || res.Location() != "/" {
				t.Fatalf("logout: %d %q", res.Status, res.Location())
			}
			cleared := false
			for _, line := range res.Cookies {
				if ck, _ := http.ParseSetCookie(line); ck != nil && ck.Name == h.sidName() && ck.MaxAge < 0 {
					cleared = true
				}
			}
			if !cleared {
				t.Fatalf("session cookie not cleared: %v", res.Cookies)
			}
			if n := h.count("sessions"); n != 0 {
				t.Fatalf("sessions rows = %d after logout", n)
			}
			if res := h.serve("GET", "/admin", h.withSID(sid)); res.Status != 303 || !strings.HasPrefix(res.Location(), "/auth/github/login") {
				t.Fatalf("replaying the old cookie: %d %q", res.Status, res.Location())
			}
		}
	})

	t.Run("trusted origin header", func(t *testing.T) {
		h := newHarness(t, false)
		sid := h.mint(testOwnerID, "o")
		if res := h.serve("POST", "/auth/logout", h.withSID(sid), withHeader("Origin", h.cfg.Origin)); res.Status != 303 {
			t.Fatalf("a POST from the site's own origin: %d", res.Status)
		}
	})

	t.Run("cross origin", func(t *testing.T) {
		for _, hdr := range [][2]string{{"Sec-Fetch-Site", "cross-site"}, {"Sec-Fetch-Site", "same-site"}, {"Origin", "https://evil.example"}} {
			h := newHarness(t, false)
			sid := h.mint(testOwnerID, "o")
			res := h.serve("POST", "/auth/logout", h.withSID(sid), withHeader(hdr[0], hdr[1]))
			if res.Status != 403 {
				t.Errorf("%s: %s: logout answered %d, want 403", hdr[0], hdr[1], res.Status)
			}
			if n := h.count("sessions"); n != 1 {
				t.Errorf("%s: %s: the session row was deleted (%d rows)", hdr[0], hdr[1], n)
			}
			if res := h.serve("GET", "/admin", h.withSID(sid)); res.Status != 200 {
				t.Errorf("%s: %s: the session no longer opens /admin: %d", hdr[0], hdr[1], res.Status)
			}
		}
	})

	t.Run("GET is not allowed", func(t *testing.T) {
		h := newHarness(t, false)
		sid := h.mint(testOwnerID, "o")
		if res := h.serve("GET", "/auth/logout", h.withSID(sid)); res.Status != 405 {
			t.Fatalf("GET /auth/logout: %d", res.Status)
		}
		if n := h.count("sessions"); n != 1 {
			t.Fatalf("GET /auth/logout deleted the session")
		}
	})

	t.Run("no valid session", func(t *testing.T) {
		h := newHarness(t, false)
		if res := h.serve("POST", "/auth/logout"); res.Status != 303 || res.Location() != "/" {
			t.Fatalf("logout without a cookie: %d %q", res.Status, res.Location())
		}
		if res := h.serve("POST", "/auth/logout", h.withSID("junk")); res.Status != 303 || res.Location() != "/" {
			t.Fatalf("logout with a junk cookie: %d %q", res.Status, res.Location())
		}
	})
}

func TestSecurityHeadersAndHealthz(t *testing.T) {
	h := newHarness(t, false)
	for _, p := range []string{"/", "/healthz", "/nowhere", "/admin"} {
		res := h.serve("GET", p)
		if res.Header.Get("Content-Security-Policy") != siteCSP {
			t.Errorf("%s: CSP = %q", p, res.Header.Get("Content-Security-Policy"))
		}
		if res.Header.Get("X-Content-Type-Options") != "nosniff" || res.Header.Get("Referrer-Policy") != "same-origin" {
			t.Errorf("%s: missing nosniff or Referrer-Policy", p)
		}
	}
	if res := h.serve("GET", "/healthz"); res.Status != 200 || res.Body != "ok" {
		t.Fatalf("/healthz: %d %q", res.Status, res.Body)
	}
	if res := h.serve("GET", "/static/site.css"); res.Status != 200 || !strings.Contains(res.Header.Get("Content-Type"), "css") {
		t.Fatalf("/static/site.css: %d %q", res.Status, res.Header.Get("Content-Type"))
	}
	if res := h.serve("GET", "/nowhere"); res.Status != 404 {
		t.Fatalf("/nowhere: %d", res.Status)
	}
}
