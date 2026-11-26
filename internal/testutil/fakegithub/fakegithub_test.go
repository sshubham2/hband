package fakegithub

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
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

// userFor runs the whole exchange for the authorize request q (as a browser's
// click on a picker link would make it) and returns what /user says.
func userFor(t *testing.T, f *Fake, authorizeURL string) (id, login string) {
	t.Helper()
	resp, err := noFollow().Get(authorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back, _ := url.Parse(resp.Header.Get("Location"))
	code := back.Query().Get("code")
	if resp.StatusCode != http.StatusFound || code == "" {
		t.Fatalf("%s answered %d %q, want a 302 back carrying a code", authorizeURL, resp.StatusCode, resp.Header.Get("Location"))
	}
	form := url.Values{"client_id": {cid}, "client_secret": {secret}, "redirect_uri": {redirect}, "code": {code}, "code_verifier": {"verifier"}}
	req, _ := http.NewRequest(http.MethodPost, f.URL()+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	tr, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(tr.Body).Decode(&tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("no token: %v", err)
	}
	ureq, _ := http.NewRequest(http.MethodGet, f.URL()+"/user", nil)
	ureq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	ur, err := http.DefaultClient.Do(ureq)
	if err != nil {
		t.Fatal(err)
	}
	defer ur.Body.Close()
	var who struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := json.NewDecoder(ur.Body).Decode(&who); err != nil {
		t.Fatal(err)
	}
	return strconv.FormatInt(who.ID, 10), who.Login
}

var pickLinkRE = regexp.MustCompile(`<a href="([^"]+)">([^<]+)</a>`)

// With choices offered, the authorize endpoint is a page of links, and the link
// followed decides who is signed in: not SetUser, and not the other choice. With
// none offered it approves at once as it always did.
func TestOfferChoicesMakesTheAuthorizePageAPicker(t *testing.T) {
	f := New(cid, secret, redirect)
	defer f.Close()
	query := authQuery(nil).Encode()

	// No choices: an immediate 302, whoever SetUser named.
	if id, login := userFor(t, f, f.URL()+"/login/oauth/authorize?"+query); id != "42" || login != "octocat" {
		t.Fatalf("without choices the fake signed in %s/%s, want 42/octocat", id, login)
	}

	f.OfferChoices(
		Choice{Label: "Continue as the owner", User: User{ID: 4242, Login: "report-author", AvatarURL: "https://avatars.example/u/4242"}},
		Choice{Label: "Continue as a <reader> & more", User: User{ID: 42, Login: "octocat"}},
	)
	resp, err := noFollow().Get(f.URL() + "/login/oauth/authorize?" + query)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("the picker answered %d %q, want a 200 page", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	links := pickLinkRE.FindAllStringSubmatch(string(body), -1)
	if len(links) != 2 || links[0][2] != "Continue as the owner" || links[1][2] != "Continue as a &lt;reader&gt; &amp; more" {
		t.Fatalf("the picker's links are %q, want the two labels, the second escaped", links)
	}
	for i, want := range [][2]string{{"4242", "report-author"}, {"42", "octocat"}} {
		href := html.UnescapeString(links[i][1])
		f.SetUser(User{ID: 7, Login: "someone-else"}) // a picked link ignores SetUser
		if id, login := userFor(t, f, f.URL()+href); id != want[0] || login != want[1] {
			t.Errorf("link %d signed in %s/%s, want %s/%s", i, id, login, want[0], want[1])
		}
	}

	// A pick that is not a choice is refused, and a request that was refused
	// before the picker is still refused with choices on offer.
	for _, pick := range []string{"2", "-1", "x"} {
		q := authQuery(func(q url.Values) { q.Set("pick", pick) })
		r, err := noFollow().Get(f.URL() + "/login/oauth/authorize?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusBadRequest {
			t.Errorf("pick=%s answered %d, want 400", pick, r.StatusCode)
		}
	}
	bad := authQuery(func(q url.Values) { q.Set("client_id", "other") })
	r, err := noFollow().Get(f.URL() + "/login/oauth/authorize?" + bad.Encode())
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Errorf("a bad client_id with choices on offer answered %d, want 400", r.StatusCode)
	}

	// Taking the choices away restores the immediate approval.
	f.OfferChoices()
	f.SetUser(User{ID: 9, Login: "ninth"})
	if id, login := userFor(t, f, f.URL()+"/login/oauth/authorize?"+query); id != "9" || login != "ninth" {
		t.Fatalf("after OfferChoices() the fake signed in %s/%s, want 9/ninth", id, login)
	}
}
