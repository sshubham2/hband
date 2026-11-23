package auth

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/sshubham2/hband/internal/config"
)

// maxLocalPath bounds a redirect target, whole, as it is written.
const maxLocalPath = 512

// LocalPath reports whether s may be sent as a Location on this site. It
// accepts s only if all of these hold:
//
//   - 1 to 512 bytes, every byte in 0x21..0x7E (so no tab, newline, space,
//     DEL, NUL or raw non-ASCII: browsers strip tab and newline, which can
//     turn a slash, tab, slash into two slashes);
//   - no backslash and no case-insensitive %5c anywhere (a browser reads a
//     backslash in an http(s) URL as a slash);
//   - s[0] is '/', and s[1], when present, is neither '/' nor a backslash;
//   - url.Parse gives no scheme, host, opaque part or user information.
//
// It checks the exact string that will go out; nothing may rewrite the value
// afterwards. Otherwise it returns ("/", false).
func LocalPath(s string) (string, bool) {
	if s == "" || len(s) > maxLocalPath {
		return "/", false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return "/", false
		}
	}
	if strings.ContainsRune(s, '\\') || strings.Contains(strings.ToLower(s), "%5c") {
		return "/", false
	}
	if s[0] != '/' || (len(s) > 1 && s[1] == '/') {
		return "/", false
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.User != nil {
		return "/", false
	}
	return s, true
}

// SeeOther answers 303 to target, which must pass LocalPath (anything else
// becomes "/"). The accepted string is written verbatim: http.Redirect would
// run path.Clean over it and could turn a validated value into an off-site
// one. This is the only 303 in the program.
func SeeOther(w http.ResponseWriter, target string) {
	safe, _ := LocalPath(target)
	w.Header().Set("Location", safe)
	w.WriteHeader(http.StatusSeeOther)
}

// ToProvider answers 302 to the identity provider's authorize URL. It
// refuses anything that does not start with the configured authorize
// endpoint, so it cannot be used to redirect elsewhere. This is the only
// absolute redirect in the program.
func ToProvider(w http.ResponseWriter, cfg config.Config, target string) {
	if !strings.HasPrefix(target, cfg.GitHub.AuthURL+"?") {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusFound)
}
