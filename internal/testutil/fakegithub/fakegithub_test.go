package fakegithub

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const (
	cid      = "cid"
	secret   = "s3cret"
	redirect = "https://hband.test/auth/github/callback"
)

func noFollow() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func challengeOf(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func authQuery(change func(url.Values)) url.Values {
	q := url.Values{
		"client_id": {cid}, "redirect_uri": {redirect}, "response_type": {"code"}, "state": {"st"},
		"code_challenge": {challengeOf("verifier")}, "code_challenge_method": {"S256"},
	}
	if change != nil {
		change(q)
	}
	return q
}

// The authorize page refuses every request the program's sign-in must never
// make: the checks the Python stand-in made, and the ones that guard the flow
// (state, PKCE S256, no scope, the one redirect address, the one client).
func TestAuthorizeRefusesABadRequest(t *testing.T) {
	f := New(cid, secret, redirect)
	defer f.Close()
	cases := map[string]func(url.Values){
		"a different client_id":         func(q url.Values) { q.Set("client_id", "other") },
		"a different redirect_uri":      func(q url.Values) { q.Set("redirect_uri", "https://evil.example/cb") },
		"response_type other than code": func(q url.Values) { q.Set("response_type", "token") },
		"an empty state":                func(q url.Values) { q.Del("state") },
		"no PKCE challenge":             func(q url.Values) { q.Del("code_challenge") },
		"PKCE method plain":             func(q url.Values) { q.Set("code_challenge_method", "plain") },
		"a non-empty scope":             func(q url.Values) { q.Set("scope", "read:user") },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			resp, err := noFollow().Get(f.URL() + "/login/oauth/authorize?" + authQuery(change).Encode())
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("authorize with %s answered %d, want 400", name, resp.StatusCode)
			}
		})
	}
	resp, err := noFollow().Get(f.URL() + "/login/oauth/authorize?" + authQuery(nil).Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || back.Query().Get("code") == "" || back.Query().Get("state") != "st" {
		t.Fatalf("a good request answered %d %q, want a 302 back carrying code and the state", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func approve(t *testing.T, f *Fake) string {
	t.Helper()
	resp, err := noFollow().Get(f.URL() + "/login/oauth/authorize?" + authQuery(nil).Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back, _ := url.Parse(resp.Header.Get("Location"))
	return back.Query().Get("code")
}

func exchange(t *testing.T, f *Fake, change func(url.Values)) string {
	t.Helper()
	form := url.Values{
		"client_id": {cid}, "client_secret": {secret}, "redirect_uri": {redirect},
		"code": {approve(t, f)}, "code_verifier": {"verifier"},
	}
	if change != nil {
		change(form)
	}
	req, _ := http.NewRequest(http.MethodPost, f.URL()+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

// The token endpoint gives a token only for a code it handed out, once, with the
// verifier that matches, to the client and redirect address it was given to; every
// other exchange is a 200 carrying an error, as GitHub's is.
func TestTokenExchangeRefusesABadRequest(t *testing.T) {
	f := New(cid, secret, redirect)
	defer f.Close()
	if body := exchange(t, f, nil); !strings.Contains(body, "access_token") {
		t.Fatalf("a good exchange did not give a token: %s", body)
	}
	cases := map[string]func(url.Values){
		"a wrong verifier":      func(v url.Values) { v.Set("code_verifier", "another") },
		"an unknown code":       func(v url.Values) { v.Set("code", "not-a-code") },
		"a wrong client_id":     func(v url.Values) { v.Set("client_id", "other") },
		"a wrong client_secret": func(v url.Values) { v.Set("client_secret", "other") },
		"a wrong redirect_uri":  func(v url.Values) { v.Set("redirect_uri", "https://evil.example/cb") },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			body := exchange(t, f, change)
			if !strings.Contains(body, "bad_verification_code") || strings.Contains(body, "access_token") {
				t.Fatalf("%s: got %s, want an error and no token", name, body)
			}
		})
	}
	t.Run("a code used twice", func(t *testing.T) {
		code := approve(t, f)
		post := func() string {
			form := url.Values{"client_id": {cid}, "client_secret": {secret}, "redirect_uri": {redirect}, "code": {code}, "code_verifier": {"verifier"}}
			req, _ := http.NewRequest(http.MethodPost, f.URL()+"/login/oauth/access_token", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return string(b)
		}
		if first := post(); !strings.Contains(first, "access_token") {
			t.Fatalf("the first use of a code failed: %s", first)
		}
		if second := post(); strings.Contains(second, "access_token") {
			t.Fatalf("a code bought a second token: %s", second)
		}
	})
}

// /user answers 401 to a token it did not give out, and to no token at all.
func TestUserRefusesAnUnknownToken(t *testing.T) {
	f := New(cid, secret, redirect)
	defer f.Close()
	for _, auth := range []string{"", "Bearer gho_unknown", "Bearer "} {
		req, _ := http.NewRequest(http.MethodGet, f.URL()+"/user", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET /user with Authorization %q answered %d, want 401", auth, resp.StatusCode)
		}
	}
}
