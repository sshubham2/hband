// Package config loads the HBAND_* environment into one validated Config.
//
// Load validates every variable and reports all the bad ones at once. It reads
// only the names listed in knownVariables; every other name (including
// HBAND_TRUSTED_PROXIES, which is left for a later block) is ignored.
package config

import (
	"fmt"
	"net"
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

	// CallbackPath is where GitHub sends the browser back to.
	CallbackPath = "/auth/github/callback"
)

var ownerIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)

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

func isLoopbackHost(h string) bool {
	return h == "127.0.0.1" || h == "::1" || strings.EqualFold(h, "localhost")
}

// String never includes the client secret.
func (c Config) String() string {
	return fmt.Sprintf("config{addr=%s base_url=%s owner_github_id=%d}", c.Addr, c.BaseURL, c.OwnerGitHubID)
}
