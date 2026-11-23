package auth

import (
	"crypto/subtle"
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"

	"golang.org/x/oauth2"

	"github.com/sshubham2/hband/internal/store"
)

const stateLen = 43

// Routes registers the sign-in and sign-out routes. The login and callback
// routes answer GET and HEAD only; a mux answers 405 for any other method.
func (a *Auth) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/github/login", a.Login)
	mux.HandleFunc("GET /auth/github/callback", a.Callback)
	mux.HandleFunc("POST /auth/logout", a.Logout)
}

// Login starts a sign-in. The OAuth state, the PKCE verifier and where to
// return to travel together in one short-lived cookie, so no server-side
// state is kept for a flow that may never finish.
func (a *Auth) Login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	next, _ := LocalPath(r.URL.Query().Get("next"))
	state, err := newToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	verifier := oauth2.GenerateVerifier()
	value := state + "." + verifier + "." + base64.RawURLEncoding.EncodeToString([]byte(next))
	setCookie(w, a.cfg, oauthBase, value, oauthMaxAge)
	ToProvider(w, a.cfg, a.oauthConfig().AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)))
}

// Callback finishes a sign-in. The OAuth cookie is cleared on every outcome.
// The GitHub ID comes only from the server-to-server call, and the session
// cookie is issued only after the transaction that stores the session commits.
func (a *Auth) Callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var cookieValue string
	if c, err := r.Cookie(cookieName(a.cfg, oauthBase)); err == nil {
		cookieValue = c.Value
	}
	clearCookie(w, a.cfg, oauthBase)

	state, verifier, next, ok := parseOAuthCookie(cookieValue)
	q := r.URL.Query()
	if !ok || subtle.ConstantTimeCompare([]byte(state), []byte(q.Get("state"))) != 1 {
		http.Error(w, "Sign-in was not started from this browser", http.StatusBadRequest)
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		http.Error(w, "Sign-in was not completed", http.StatusBadRequest)
		return
	}

	token, err := a.exchange(r.Context(), q.Get("code"), verifier)
	if err != nil {
		slog.Warn("sign-in refused", "step", "token", "reason", err.Error())
		http.Error(w, "GitHub did not complete the sign-in", http.StatusBadGateway)
		return
	}
	user, err := a.fetchUser(r.Context(), token)
	if err != nil {
		slog.Warn("sign-in refused", "step", "user", "reason", err.Error())
		http.Error(w, "GitHub did not say who you are", http.StatusBadGateway)
		return
	}
	next, _ = LocalPath(next)

	session, err := newToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var oldHash string
	if c, err := r.Cookie(cookieName(a.cfg, sidBase)); err == nil && tokenPattern.MatchString(c.Value) {
		oldHash = hashToken(c.Value)
	}
	now := a.now()
	if err := a.store.SignIn(r.Context(), store.SignInArgs{
		User:         user,
		OldTokenHash: oldHash,
		NewTokenHash: hashToken(session),
		ExpiresAt:    now.Add(sessionTTL),
		Now:          now,
	}); err != nil {
		slog.Error("sign-in could not be saved", "error", err)
		http.Error(w, "Sign-in could not be saved, try again", http.StatusServiceUnavailable)
		return
	}
	setCookie(w, a.cfg, sidBase, session, sidMaxAge)
	SeeOther(w, next)
}

// parseOAuthCookie splits the OAuth cookie: state, verifier and the
// base64url of next, joined by dots. A cookie that does not split into
// exactly three parts, or whose state is not 43 characters, is refused.
func parseOAuthCookie(v string) (state, verifier, next string, ok bool) {
	parts := strings.Split(v, ".")
	if len(parts) != 3 || len(parts[0]) != stateLen {
		return "", "", "", false
	}
	if len(parts[1]) < 43 || len(parts[1]) > 128 {
		return "", "", "", false
	}
	next = "/"
	if nb, err := base64.RawURLEncoding.DecodeString(parts[2]); err == nil {
		next = string(nb)
	}
	return parts[0], parts[1], next, true
}

// Logout ends the session. When the row cannot be deleted it answers 503 and
// leaves the cookie alone, so it never claims a sign-out that did not happen.
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	c, err := r.Cookie(cookieName(a.cfg, sidBase))
	if err != nil {
		SeeOther(w, "/")
		return
	}
	if tokenPattern.MatchString(c.Value) {
		if err := a.store.DeleteSession(r.Context(), hashToken(c.Value)); err != nil {
			slog.Error("sign-out could not be saved", "error", err)
			http.Error(w, "Sign-out could not be completed, try again", http.StatusServiceUnavailable)
			return
		}
	}
	clearCookie(w, a.cfg, sidBase)
	SeeOther(w, "/")
}
