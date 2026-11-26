// Package fakegithub is a stand-in for github.com's OAuth web flow, for tests
// only: it signs in whoever it is told to. Production code must never import it.
package fakegithub

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

// Choice is one account the picker offers: Label is the text of the link that
// signs in as User.
type Choice struct {
	Label string
	User  User
}

// grant is one authorization code. user is the account a picker link chose, or
// nil when the fake signs in whoever SetUser last named.
type grant struct {
	challenge string
	user      *User
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
	choices      []Choice
	codes        map[string]grant
	tokens       map[string]*User // nil value: the account SetUser names when /user is asked
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
		tokens:       map[string]*User{},
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

// OfferChoices makes the authorize endpoint a picker: a page of one link per
// choice, drawn with the choice's Label, instead of an approval of whoever
// SetUser named. The link a person (or a browser's click on its text) follows
// approves that choice's account and nobody else, so one running fake can sign a
// browser in as the owner and another as a reader. With no choices, which is
// how a fake starts, it approves at once as before.
func (f *Fake) OfferChoices(cs ...Choice) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.choices = append([]Choice(nil), cs...)
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
	var chosen *User
	if len(f.choices) > 0 && !f.sw.DenyAtAuthorize {
		pick := q.Get("pick")
		if pick == "" {
			f.picker(w, q)
			return
		}
		i, err := strconv.Atoi(pick)
		if err != nil || i < 0 || i >= len(f.choices) {
			http.Error(w, "unknown choice", http.StatusBadRequest)
			return
		}
		u := f.choices[i].User
		chosen = &u
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
		f.codes[code] = grant{challenge: challenge, user: chosen}
		v.Set("code", code)
	}
	back.RawQuery = v.Encode()
	w.Header().Set("Location", back.String())
	w.WriteHeader(http.StatusFound)
}

// pickerPage is the page OfferChoices draws. Each link is the authorize request
// the browser made, with the index of its choice added as pick.
var pickerPage = template.Must(template.New("picker").Parse(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Stand-in GitHub</title></head>
<body>
<h1>Stand-in GitHub</h1>
<p>A test double for GitHub's sign-in. Choose the account to sign in as.</p>
<ul>
{{range .}}<li><a href="{{.Href}}">{{.Label}}</a></li>
{{end}}</ul>
</body>
</html>
`))

// picker answers the authorize request q with a link for each choice. The caller
// holds f.mu.
func (f *Fake) picker(w http.ResponseWriter, q url.Values) {
	type link struct{ Href, Label string }
	links := make([]link, len(f.choices))
	for i, c := range f.choices {
		withPick := url.Values{}
		for k, vs := range q {
			withPick[k] = vs
		}
		withPick.Set("pick", strconv.Itoa(i))
		links[i] = link{Href: "/login/oauth/authorize?" + withPick.Encode(), Label: c.Label}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := pickerPage.Execute(w, links); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
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
	f.tokens[tok] = g.user
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
	picked, known := f.tokens[tok]
	if f.sw.User401 || !known {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	user := f.user
	if picked != nil {
		user = *picked
	}
	login, _ := json.Marshal(user.Login)
	avatar, _ := json.Marshal(user.AvatarURL)
	if f.sw.UserEmptyLogin {
		login = []byte(`""`)
	}
	id := fmt.Sprint(user.ID)
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
