// Package config loads the HBAND_* environment into one validated Config.
//
// Load validates every variable and reports all the bad ones at once. It reads
// only HBAND_OWNER_GITHUB_ID, HBAND_GITHUB_CLIENT_ID, HBAND_GITHUB_CLIENT_SECRET,
// HBAND_BASE_URL, HBAND_ADDR, HBAND_DATA_DIR, HBAND_GITHUB_URL,
// HBAND_GITHUB_API_URL, HBAND_MAX_UPLOAD_BYTES and HBAND_TRUSTED_PROXIES; every
// other name is ignored.
package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	defaultAddr      = "127.0.0.1:8080"
	defaultDataDir   = "./data"
	defaultGitHubURL = "https://github.com"
	defaultGitHubAPI = "https://api.github.com"

	// defaultMaxUpload is 25 MiB; maxUploadLimit is the largest cap that may be
	// configured (200 MiB).
	defaultMaxUpload = 26214400
	maxUploadLimit   = 209715200

	// CallbackPath is where GitHub sends the browser back to.
	CallbackPath = "/auth/github/callback"
)

var (
	ownerIDPattern   = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	uploadCapPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
)

// GitHubEndpoints are the three URLs the sign-in flow talks to.
type GitHubEndpoints struct {
	AuthURL  string
	TokenURL string
	UserURL  string
}

// Config is the validated process configuration.
type Config struct {
	Addr    string
	DataDir string
	// MaxUploadBytes is the largest report file accepted, in bytes: at least 1
	// and at most 209715200 for a Config returned by Load.
	MaxUploadBytes int64

	// BaseURL is the public origin of the site, without a trailing slash.
	BaseURL string
	// Origin is scheme://host[:port] of BaseURL.
	Origin string
	// CallbackURL is the exact redirect_uri registered with the OAuth app.
	CallbackURL string
	// SecureCookies is on when the public scheme is https.
	SecureCookies bool

	// OwnerGitHubID is the GitHub numeric user ID pinned as the owner. It is
	// always > 0 for a Config returned by Load.
	OwnerGitHubID int64

	ClientID     string
	ClientSecret string

	GitHub GitHubEndpoints
	// GitHubURL and GitHubAPIURL are the validated bases the endpoints derive from.
	GitHubURL    string
	GitHubAPIURL string
	// GitHubOverridden is true when either base differs from the production default.
	GitHubOverridden bool

	// TrustedProxies are the addresses a request may come from for its
	// X-Forwarded-For header to be believed (HBAND_TRUSTED_PROXIES). Every prefix is
	// masked and is never /0. Nil when the variable is unset or empty: no peer is
	// trusted and the header is ignored.
	TrustedProxies []netip.Prefix
}

// Error lists every bad variable. It never carries a variable's value.
type Error struct {
	Problems []string
}

func (e *Error) Error() string {
	return "invalid configuration:\n  " + strings.Join(e.Problems, "\n  ")
}

// Load reads and validates the configuration through getenv.
func Load(getenv func(string) string) (Config, error) {
	var problems []string
	bad := func(name, why string) { problems = append(problems, name+": "+why) }

	var c Config

	// Owner: a strictly positive decimal int64, nothing else.
	const ownerVar = "HBAND_OWNER_GITHUB_ID"
	rawOwner := getenv(ownerVar)
	switch {
	case rawOwner == "":
		bad(ownerVar, "is required (the numeric GitHub user ID of the owner)")
	case !ownerIDPattern.MatchString(rawOwner):
		bad(ownerVar, "must be a positive decimal number such as 583231, not a login name")
	default:
		id, err := strconv.ParseInt(rawOwner, 10, 64)
		if err != nil || id <= 0 {
			bad(ownerVar, "must fit in a signed 64-bit integer")
		} else {
			c.OwnerGitHubID = id
		}
	}

	// OAuth app credentials. The secret's value is never echoed.
	c.ClientID = getenv("HBAND_GITHUB_CLIENT_ID")
	if c.ClientID == "" {
		bad("HBAND_GITHUB_CLIENT_ID", "is required")
	}
	c.ClientSecret = getenv("HBAND_GITHUB_CLIENT_SECRET")
	if c.ClientSecret == "" {
		bad("HBAND_GITHUB_CLIENT_SECRET", "is required")
	}

	// Public base URL.
	const baseVar = "HBAND_BASE_URL"
	if raw := getenv(baseVar); raw == "" {
		bad(baseVar, "is required, for example https://hband.example")
	} else if u, why := parseBase(raw); why != "" {
		bad(baseVar, why)
	} else {
		c.Origin = u.Scheme + "://" + u.Host
		c.BaseURL = c.Origin
		c.CallbackURL = c.Origin + CallbackPath
		c.SecureCookies = u.Scheme == "https"
	}

	// Listen address and data directory.
	c.Addr = getenv("HBAND_ADDR")
	if c.Addr == "" {
		c.Addr = defaultAddr
	} else if _, port, err := net.SplitHostPort(c.Addr); err != nil || port == "" {
		bad("HBAND_ADDR", "must be host:port, for example 127.0.0.1:8080")
	}
	c.DataDir = getenv("HBAND_DATA_DIR")
	if c.DataDir == "" {
		c.DataDir = defaultDataDir
	}

	// Upload cap: a positive decimal number of bytes, nothing else.
	const capVar = "HBAND_MAX_UPLOAD_BYTES"
	c.MaxUploadBytes = defaultMaxUpload
	if raw := getenv(capVar); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if !uploadCapPattern.MatchString(raw) || err != nil || n > maxUploadLimit {
			bad(capVar, "must be a positive decimal number of bytes, at most 209715200")
		} else {
			c.MaxUploadBytes = n
		}
	}

	// Identity provider endpoints. These are the finish line's test seam, so
	// they are only accepted as https or loopback http.
	c.GitHubURL = githubBase(getenv, "HBAND_GITHUB_URL", defaultGitHubURL, bad)
	c.GitHubAPIURL = githubBase(getenv, "HBAND_GITHUB_API_URL", defaultGitHubAPI, bad)
	c.GitHub = GitHubEndpoints{
		AuthURL:  c.GitHubURL + "/login/oauth/authorize",
		TokenURL: c.GitHubURL + "/login/oauth/access_token",
		UserURL:  c.GitHubAPIURL + "/user",
	}
	c.GitHubOverridden = c.GitHubURL != defaultGitHubURL || c.GitHubAPIURL != defaultGitHubAPI

	// Trusted proxies: unset trusts nobody. A set value is a comma-separated list
	// of addresses and CIDR prefixes and every item must be good, so a typo cannot
	// quietly trust fewer proxies than the operator meant (or, with /0, everyone).
	const proxiesVar = "HBAND_TRUSTED_PROXIES"
	if raw := getenv(proxiesVar); raw != "" {
		list, why := parseProxies(raw)
		if why != "" {
			bad(proxiesVar, why)
		} else {
			c.TrustedProxies = list
		}
	}

	if len(problems) > 0 {
		return Config{}, &Error{Problems: problems}
	}
	return c, nil
}

func githubBase(getenv func(string) string, name, def string, bad func(string, string)) string {
	raw := getenv(name)
	if raw == "" {
		return def
	}
	u, why := parseBase(raw)
	if why != "" {
		bad(name, why)
		return def
	}
	return strings.TrimSuffix(u.Scheme+"://"+u.Host+u.EscapedPath(), "/")
}

// parseBase accepts an absolute https URL on any host, or an http URL on a
// loopback host, with no path beyond '/', no query, no fragment and no
// userinfo. The second result is a reason that never repeats the input.
func parseBase(raw string) (*url.URL, string) {
	if strings.ContainsAny(raw, "?#") {
		return nil, "must not carry a query or a fragment"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "is not a valid URL"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, "must be an absolute http or https URL"
	}
	if u.Host == "" || u.Opaque != "" {
		return nil, "must be an absolute URL with a host"
	}
	if u.User != nil {
		return nil, "must not carry user information"
	}
	if p := u.EscapedPath(); p != "" && p != "/" {
		return nil, "must not carry a path"
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return nil, "must be https unless the host is 127.0.0.1, localhost or [::1]"
	}
	return u, ""
}

// parseProxies reads a comma-separated list of IP addresses and CIDR prefixes.
// Each item is trimmed; an IPv4-mapped IPv6 form is read as the IPv4 one, a bare
// address becomes a /32 or /128 and a prefix is masked. The second result is a
// reason that never repeats the input. An empty item, a name that is not an
// address, a zone, a prefix that cannot be read and a prefix of length 0 (which
// would trust every peer) are refused.
func parseProxies(raw string) ([]netip.Prefix, string) {
	var out []netip.Prefix
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		switch {
		case item == "":
			return nil, "has an empty item (a stray comma or a blank list)"
		case strings.Contains(item, "%"):
			return nil, "must not carry an IPv6 zone"
		}
		var p netip.Prefix
		if strings.Contains(item, "/") {
			pp, err := netip.ParsePrefix(item)
			if err != nil {
				return nil, "has an item that is not an IP address or a CIDR prefix"
			}
			addr, bits := pp.Addr(), pp.Bits()
			if addr.Is4In6() {
				if bits < 96 {
					return nil, "has an IPv4-mapped prefix shorter than /96"
				}
				addr, bits = addr.Unmap(), bits-96
			}
			if bits == 0 {
				return nil, "must not trust every address (a /0 prefix)"
			}
			p = netip.PrefixFrom(addr, bits).Masked()
		} else {
			addr, err := netip.ParseAddr(item)
			if err != nil {
				return nil, "has an item that is not an IP address (names are not accepted)"
			}
			addr = addr.Unmap()
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		out = append(out, p)
	}
	return out, ""
}

func isLoopbackHost(h string) bool {
	return h == "127.0.0.1" || h == "::1" || strings.EqualFold(h, "localhost")
}

// String never includes the client secret.
func (c Config) String() string {
	return fmt.Sprintf("config{addr=%s base_url=%s owner_github_id=%d}", c.Addr, c.BaseURL, c.OwnerGitHubID)
}
