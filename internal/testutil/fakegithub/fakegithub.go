// Package fakegithub is a stand-in for github.com's OAuth web flow, for tests
// only: it signs in whoever it is told to. Production code must never import it.
package fakegithub

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
)

// User is the GitHub account the fake signs in.
type User struct {
	ID        int64
	Login     string
	AvatarURL string
}

// Switches are the failures the fake can be told to produce.
type Switches struct {
	DenyAtAuthorize bool // authorize answers error=access_denied
	WrongVerifier   bool // the stored challenge never matches the app's verifier
	TokenError200   bool // token endpoint: 200 with error=bad_verification_code
	Token500        bool // token endpoint: HTTP 500
	User401         bool // /user answers 401
	UserMissingID   bool // /user has no id
	UserIDZero      bool // id is 0
	UserIDString    bool // id is "42"
	UserIDFloat     bool // id is 42.0
	UserEmptyLogin  bool // login is ""
}

type grant struct {
	challenge string
}

// Fake is a running fake GitHub. Point both HBAND_GITHUB_URL and
// HBAND_GITHUB_API_URL (or Config.GitHub) at URL().
type Fake struct {
	srv *httptest.Server

	mu           sync.Mutex
	clientID     string
	clientSecret string
	redirectURI  string
	user         User
	sw           Switches
	codes        map[string]grant
	tokens       map[string]bool
	tokenCalls   int
}

// New starts a fake that accepts the given OAuth app credentials and exactly
// the given redirect_uri. Close it when done.
func New(clientID, clientSecret, redirectURI string) *Fake {
	f := &Fake{
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURI:  redirectURI,
		user:         User{ID: 42, Login: "octocat", AvatarURL: "https://avatars.githubusercontent.com/u/42"},
		codes:        map[string]grant{},
		tokens:       map[string]bool{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/authorize", f.authorize)
	mux.HandleFunc("/login/oauth/access_token", f.accessToken)
	mux.HandleFunc("/user", f.userInfo)
	f.srv = httptest.NewServer(mux)
	return f
}

// URL is the fake's base URL (http://127.0.0.1:port).
func (f *Fake) URL() string { return f.srv.URL }

// Close stops the fake.
func (f *Fake) Close() { f.srv.Close() }

// SetUser chooses the account the next sign-ins return.
func (f *Fake) SetUser(u User) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.user = u
}

// SetRedirectURI changes the only redirect_uri the fake accepts.
func (f *Fake) SetRedirectURI(uri string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.redirectURI = uri
}

// Configure changes the failure switches.
func (f *Fake) Configure(fn func(*Switches)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.sw)
}

// TokenCalls is how many times the token endpoint was called.
func (f *Fake) TokenCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenCalls
}

func randomString() string {
	var b [18]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func (f *Fake) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case q.Get("client_id") != f.clientID:
		http.Error(w, "unknown client_id", http.StatusBadRequest)
		return
	case q.Get("redirect_uri") != f.redirectURI:
		http.Error(w, "redirect_uri mismatch", http.StatusBadRequest)
		return
	case q.Get("response_type") != "code", q.Get("state") == "":
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	case q.Get("code_challenge_method") != "S256", q.Get("code_challenge") == "":
		http.Error(w, "PKCE S256 required", http.StatusBadRequest)
		return
	case q.Get("scope") != "":
		http.Error(w, "the app must ask for no scopes", http.StatusBadRequest)
		return
	}
	back, err := url.Parse(f.redirectURI)
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusInternalServerError)
		return
	}
	v := back.Query()
	v.Set("state", q.Get("state"))
	if f.sw.DenyAtAuthorize {
		v.Set("error", "access_denied")
	} else {
		code := randomString()
		challenge := q.Get("code_challenge")
		if f.sw.WrongVerifier {
			challenge = "not-the-challenge"
		}
		f.codes[code] = grant{challenge: challenge}
		v.Set("code", code)
	}
	back.RawQuery = v.Encode()
	w.Header().Set("Location", back.String())
	w.WriteHeader(http.StatusFound)
}

func (f *Fake) tokenError(w http.ResponseWriter, r *http.Request) {
	// Like GitHub, a failed exchange is still a 200 carrying an error.
	if strings.Contains(r.Header.Get("Accept"), "json") {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"error":"bad_verification_code","error_description":"The code passed is incorrect or expired."}`)
		return
	}
	w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
	fmt.Fprint(w, "error=bad_verification_code&error_description=The+code+passed+is+incorrect+or+expired.")
}

func (f *Fake) accessToken(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenCalls++
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if f.sw.Token500 {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if f.sw.TokenError200 {
		f.tokenError(w, r)
		return
	}
	g, ok := f.codes[r.PostForm.Get("code")]
	delete(f.codes, r.PostForm.Get("code")) // single use
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	verifierOK := base64.RawURLEncoding.EncodeToString(sum[:]) == g.challenge
	if !ok || !verifierOK ||
		r.PostForm.Get("client_id") != f.clientID ||
		r.PostForm.Get("client_secret") != f.clientSecret ||
		r.PostForm.Get("redirect_uri") != f.redirectURI {
		f.tokenError(w, r)
		return
	}
	tok := "gho_" + randomString()
	f.tokens[tok] = true
	if strings.Contains(r.Header.Get("Accept"), "json") {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"bearer","scope":""}`, tok)
		return
	}
	w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
	fmt.Fprintf(w, "access_token=%s&token_type=bearer&scope=", url.QueryEscape(tok))
}

func (f *Fake) userInfo(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if f.sw.User401 || !f.tokens[tok] {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	login, _ := json.Marshal(f.user.Login)
	avatar, _ := json.Marshal(f.user.AvatarURL)
	if f.sw.UserEmptyLogin {
		login = []byte(`""`)
	}
	id := fmt.Sprint(f.user.ID)
	switch {
	case f.sw.UserIDZero:
		id = "0"
	case f.sw.UserIDString:
		id = fmt.Sprintf("%q", fmt.Sprint(f.user.ID))
	case f.sw.UserIDFloat:
		id = fmt.Sprintf("%d.0", f.user.ID)
	}
	w.Header().Set("Content-Type", "application/json")
	if f.sw.UserMissingID {
		fmt.Fprintf(w, `{"login":%s,"avatar_url":%s}`, login, avatar)
		return
	}
	fmt.Fprintf(w, `{"id":%s,"login":%s,"avatar_url":%s}`, id, login, avatar)
}
