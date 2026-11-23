package auth

import (
	"net/http"

	"github.com/sshubham2/hband/internal/config"
)

// This file is the only place a cookie is built or written. A browser
// silently ignores a __Host- cookie, deletions included, that lacks Secure or
// Path=/, and only on https, so the attribute set is a property of these two
// functions rather than of each call site.

const (
	sidBase   = "hband_sid"
	oauthBase = "hband_oauth"

	sidMaxAge   = 30 * 24 * 60 * 60 // persistent: the session lasts 30 days
	oauthMaxAge = 600
)

// cookieName is the name used to write and to read a cookie: __Host- prefixed
// whenever the public scheme is https.
func cookieName(cfg config.Config, base string) string {
	if cfg.SecureCookies {
		return "__Host-" + base
	}
	return base
}

func buildCookie(cfg config.Config, base, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     cookieName(cfg, base),
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	}
}

func setCookie(w http.ResponseWriter, cfg config.Config, base, value string, maxAge int) {
	http.SetCookie(w, buildCookie(cfg, base, value, maxAge))
}

// clearCookie deletes a cookie with exactly the attributes it was set with.
func clearCookie(w http.ResponseWriter, cfg config.Config, base string) {
	http.SetCookie(w, buildCookie(cfg, base, "", -1))
}
