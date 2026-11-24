package config

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

const marker = "SECRET-MARKER-9f3a"

func env(over map[string]string) func(string) string {
	base := map[string]string{
		"HBAND_OWNER_GITHUB_ID":      "4242",
		"HBAND_GITHUB_CLIENT_ID":     "cid",
		"HBAND_GITHUB_CLIENT_SECRET": marker,
		"HBAND_BASE_URL":             "https://hband.example",
	}
	for k, v := range over {
		if v == "\x00unset" {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
	return func(k string) string { return base[k] }
}

func problemNames(t *testing.T, err error) string {
	t.Helper()
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("error is %T, want *config.Error: %v", err, err)
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("error text leaks the client secret: %v", err)
	}
	return err.Error()
}

func TestLoadOwnerIDGrammar(t *testing.T) {
	refused := []string{"", "0", "-1", "+5", " 5", "5 ", "05", "octocat", "@octocat", "a@b.c", "1e5", "12.0", "9223372036854775808",
		"5\n", "0x10", "٣", "99999999999999999999", "1_000"}
	for _, v := range refused {
		t.Run("refuse "+strings.ReplaceAll(v, "\n", `\n`), func(t *testing.T) {
			_, err := Load(env(map[string]string{"HBAND_OWNER_GITHUB_ID": v}))
			if err == nil {
				t.Fatalf("owner %q accepted", v)
			}
			if got := problemNames(t, err); !strings.Contains(got, "HBAND_OWNER_GITHUB_ID") {
				t.Fatalf("error does not name the variable: %v", got)
			}
		})
	}
	accepted := map[string]int64{"1": 1, "9223372036854775807": 9223372036854775807, "4242": 4242}
	for v, want := range accepted {
		cfg, err := Load(env(map[string]string{"HBAND_OWNER_GITHUB_ID": v}))
		if err != nil {
			t.Fatalf("owner %q refused: %v", v, err)
		}
		if cfg.OwnerGitHubID != want {
			t.Fatalf("owner %q parsed as %d", v, cfg.OwnerGitHubID)
		}
	}
	// Positive control: with the owner removed the same environment fails,
	// so the accepted rows above are not passing on a lenient loader.
	if _, err := Load(env(map[string]string{"HBAND_OWNER_GITHUB_ID": "\x00unset"})); err == nil {
		t.Fatal("missing owner accepted")
	}
}

func TestLoadReportsEveryBadVariable(t *testing.T) {
	_, err := Load(func(string) string { return "" })
	got := problemNames(t, err)
	for _, name := range []string{"HBAND_OWNER_GITHUB_ID", "HBAND_GITHUB_CLIENT_ID", "HBAND_GITHUB_CLIENT_SECRET", "HBAND_BASE_URL"} {
		if !strings.Contains(got, name) {
			t.Errorf("error does not name %s: %s", name, got)
		}
	}
}

func TestLoadBaseURL(t *testing.T) {
	ok := []struct {
		raw, origin string
		secure      bool
	}{
		{"https://hband.example", "https://hband.example", true},
		{"https://hband.example/", "https://hband.example", true},
		{"https://hband.example:8443", "https://hband.example:8443", true},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080", false},
		{"http://localhost", "http://localhost", false},
		{"http://localhost:3000/", "http://localhost:3000", false},
		{"http://[::1]:8080", "http://[::1]:8080", false},
		{"https://hband.test", "https://hband.test", true},
	}
	for _, c := range ok {
		cfg, err := Load(env(map[string]string{"HBAND_BASE_URL": c.raw}))
		if err != nil {
			t.Errorf("%q refused: %v", c.raw, err)
			continue
		}
		if cfg.Origin != c.origin || cfg.SecureCookies != c.secure || cfg.CallbackURL != c.origin+"/auth/github/callback" {
			t.Errorf("%q: origin %q secure %v callback %q", c.raw, cfg.Origin, cfg.SecureCookies, cfg.CallbackURL)
		}
	}
	refused := []string{
		"http://hband.example", "http://10.0.0.5", "https://hband.example/app", "https://hband.example?x=1",
		"https://hband.example/#frag", "https://user:pw@hband.example", "https://user@hband.example",
		"//hband.example", "hband.example", "/relative", "ftp://hband.example", "https:hband.example",
		"https://", "", "https://hband.example/%2F", "https://hband.example/?",
	}
	for _, raw := range refused {
		_, err := Load(env(map[string]string{"HBAND_BASE_URL": raw}))
		if err == nil {
			t.Errorf("%q accepted", raw)
			continue
		}
		if got := problemNames(t, err); !strings.Contains(got, "HBAND_BASE_URL") {
			t.Errorf("%q: error does not name HBAND_BASE_URL: %s", raw, got)
		}
		if strings.Contains(err.Error(), "user:pw") {
			t.Errorf("%q: error echoes userinfo", raw)
		}
	}
}

func TestLoadGitHubEndpoints(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHub.AuthURL != "https://github.com/login/oauth/authorize" ||
		cfg.GitHub.TokenURL != "https://github.com/login/oauth/access_token" ||
		cfg.GitHub.UserURL != "https://api.github.com/user" {
		t.Fatalf("defaults wrong: %+v", cfg.GitHub)
	}
	if cfg.GitHubOverridden {
		t.Fatal("defaults reported as an override")
	}

	cfg, err = Load(env(map[string]string{
		"HBAND_GITHUB_URL":     "http://127.0.0.1:9000/",
		"HBAND_GITHUB_API_URL": "http://localhost:9001",
	}))
	if err != nil {
		t.Fatalf("loopback override refused: %v", err)
	}
	if cfg.GitHub.AuthURL != "http://127.0.0.1:9000/login/oauth/authorize" ||
		cfg.GitHub.TokenURL != "http://127.0.0.1:9000/login/oauth/access_token" ||
		cfg.GitHub.UserURL != "http://localhost:9001/user" || !cfg.GitHubOverridden {
		t.Fatalf("override wrong: %+v overridden=%v", cfg.GitHub, cfg.GitHubOverridden)
	}

	if cfg, err = Load(env(map[string]string{"HBAND_GITHUB_URL": "https://ghe.example"})); err != nil || !cfg.GitHubOverridden ||
		cfg.GitHub.UserURL != "https://api.github.com/user" {
		t.Fatalf("https override: cfg=%+v err=%v", cfg.GitHub, err)
	}

	for _, name := range []string{"HBAND_GITHUB_URL", "HBAND_GITHUB_API_URL"} {
		for _, raw := range []string{"http://evil.example", "https://github.com/login", "https://github.com?x=1", "https://github.com#f",
			"https://u:p@github.com", "/relative", "github.com", "http://10.0.0.1:80"} {
			_, err := Load(env(map[string]string{name: raw}))
			if err == nil {
				t.Errorf("%s=%q accepted", name, raw)
				continue
			}
			if got := problemNames(t, err); !strings.Contains(got, name) {
				t.Errorf("%s=%q: error does not name the variable: %s", name, raw, got)
			}
		}
	}
}

func TestLoadIgnoresUnknownVariables(t *testing.T) {
	asked := map[string]bool{}
	base := env(map[string]string{"HBAND_TRUSTED_PROXIES": "127.0.0.1", "HBAND_SOMETHING_ELSE": "x"})
	cfg, err := Load(func(k string) string { asked[k] = true; return base(k) })
	if err != nil {
		t.Fatalf("HBAND_TRUSTED_PROXIES caused an error: %v", err)
	}
	if cfg.Addr != "127.0.0.1:8080" {
		t.Fatalf("unset HBAND_ADDR gave %q, want 127.0.0.1:8080", cfg.Addr)
	}
	if cfg.DataDir != "./data" {
		t.Fatalf("unset HBAND_DATA_DIR gave %q", cfg.DataDir)
	}
	// HBAND_TRUSTED_PROXIES is read now (SL-013); HBAND_SOMETHING_ELSE is the name
	// that stays ignored.
	if want := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}; !slices.Equal(cfg.TrustedProxies, want) {
		t.Fatalf("HBAND_TRUSTED_PROXIES=127.0.0.1 gave %v, want %v", cfg.TrustedProxies, want)
	}
	for k := range asked {
		switch k {
		case "HBAND_OWNER_GITHUB_ID", "HBAND_GITHUB_CLIENT_ID", "HBAND_GITHUB_CLIENT_SECRET", "HBAND_BASE_URL",
			"HBAND_ADDR", "HBAND_DATA_DIR", "HBAND_GITHUB_URL", "HBAND_GITHUB_API_URL", "HBAND_MAX_UPLOAD_BYTES",
			"HBAND_TRUSTED_PROXIES":
		default:
			t.Errorf("Load read %s, which is not one of its variables", k)
		}
	}
	if cfg, err = Load(env(map[string]string{"HBAND_ADDR": "127.0.0.1:55555"})); err != nil || cfg.Addr != "127.0.0.1:55555" {
		t.Fatalf("HBAND_ADDR not honoured: %q %v", cfg.Addr, err)
	}
	if _, err = Load(env(map[string]string{"HBAND_ADDR": "not-an-address"})); err == nil {
		t.Fatal("a malformed HBAND_ADDR was accepted")
	}
}

// The upload cap is a positive decimal number of bytes, at most 200 MiB, and
// 25 MiB when the variable is unset (the shipped default).
func TestLoadUploadCap(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxUploadBytes != 26214400 {
		t.Fatalf("unset HBAND_MAX_UPLOAD_BYTES gave %d, want 26214400", cfg.MaxUploadBytes)
	}
	if cfg, err = Load(env(map[string]string{"HBAND_MAX_UPLOAD_BYTES": ""})); err != nil || cfg.MaxUploadBytes != 26214400 {
		t.Fatalf("empty HBAND_MAX_UPLOAD_BYTES: %d %v", cfg.MaxUploadBytes, err)
	}

	accepted := map[string]int64{"1": 1, "65536": 65536, "26214400": 26214400, "209715200": 209715200}
	for v, want := range accepted {
		cfg, err := Load(env(map[string]string{"HBAND_MAX_UPLOAD_BYTES": v}))
		if err != nil || cfg.MaxUploadBytes != want {
			t.Errorf("%q: got %d, %v; want %d", v, cfg.MaxUploadBytes, err, want)
		}
	}
	refused := []string{"0", "-1", "+5", "05", " 5", "5 ", "5\n", "1e6", "25MB", "nonsense", "0x10", "1_000", "12.0", "٣",
		"209715201", "9223372036854775808", "99999999999999999999"}
	for _, v := range refused {
		_, err := Load(env(map[string]string{"HBAND_MAX_UPLOAD_BYTES": v}))
		if err == nil {
			t.Errorf("%q accepted", v)
			continue
		}
		if got := problemNames(t, err); !strings.Contains(got, "HBAND_MAX_UPLOAD_BYTES") {
			t.Errorf("%q: error does not name the variable: %s", v, got)
		}
	}
}

func TestConfigStringHidesSecret(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cfg.String(), marker) {
		t.Fatal("String() leaks the client secret")
	}
}

// HBAND_TRUSTED_PROXIES: unset (or empty) trusts nobody, a list of addresses and
// CIDR prefixes is read into masked prefixes, and every malformed item stops
// startup with an error that names the variable and never repeats its value.
func TestTrustedProxies(t *testing.T) {
	const name = "HBAND_TRUSTED_PROXIES"
	p := func(ss ...string) []netip.Prefix {
		var out []netip.Prefix
		for _, s := range ss {
			out = append(out, netip.MustParsePrefix(s))
		}
		return out
	}
	t.Run("unset and empty trust nobody", func(t *testing.T) {
		for _, set := range []map[string]string{{name: "\x00unset"}, {name: ""}} {
			cfg, err := Load(env(set))
			if err != nil {
				t.Fatalf("%v: %v", set, err)
			}
			if cfg.TrustedProxies != nil {
				t.Errorf("%v trusts %v, want nobody", set, cfg.TrustedProxies)
			}
		}
	})
	t.Run("accepted lists", func(t *testing.T) {
		for raw, want := range map[string][]netip.Prefix{
			"127.0.0.1":                          p("127.0.0.1/32"),
			"::1":                                p("::1/128"),
			"10.0.0.0/8":                         p("10.0.0.0/8"),
			"10.1.2.3/24":                        p("10.1.2.0/24"),
			" 10.0.0.1 , fd00::/8 ":              p("10.0.0.1/32", "fd00::/8"),
			"192.168.0.0/16,2001:db8::5,::1/128": p("192.168.0.0/16", "2001:db8::5/128", "::1/128"),
			"::ffff:10.0.0.1":                    p("10.0.0.1/32"),
			"::ffff:10.0.0.0/104":                p("10.0.0.0/8"),
			"0.0.0.0/1":                          p("0.0.0.0/1"),
		} {
			cfg, err := Load(env(map[string]string{name: raw}))
			if err != nil {
				t.Errorf("%q refused: %v", raw, err)
				continue
			}
			if !slices.Equal(cfg.TrustedProxies, want) {
				t.Errorf("%q gave %v, want %v", raw, cfg.TrustedProxies, want)
			}
		}
	})
	t.Run("malformed values are refused by name", func(t *testing.T) {
		for _, raw := range []string{
			"proxy.example", // a hostname
			"localhost",     // a hostname
			"10.0.0.1,",     // a stray comma
			",10.0.0.1",     // another
			"10.0.0.1,,10.0.0.2",
			" ", // a blank list
			",",
			"10.0.0.0/33", // a prefix too long
			"2001:db8::/129",
			"10.0.0.0/abc",
			"10.0.0.0/",
			"10.0.0.1/24/8",
			"fe80::1%eth0", // a zone
			"fe80::%eth0/64",
			"10.0.0.0/0", // would trust every peer
			"::/0",
			"::ffff:0.0.0.0/96",
			"::ffff:10.0.0.0/64",
			"300.1.1.1",
			"10.0.0.1 10.0.0.2",
			"10.0.0.1;10.0.0.2",
			"10.0.0.1, bad.example.test",
		} {
			_, err := Load(env(map[string]string{name: raw}))
			if err == nil {
				t.Errorf("%s=%q accepted", name, raw)
				continue
			}
			got := problemNames(t, err)
			if !strings.Contains(got, name) {
				t.Errorf("%s=%q: error does not name the variable: %s", name, raw, got)
			}
			if v := strings.TrimSpace(raw); len(v) > 2 && strings.Contains(got, v) {
				t.Errorf("%s=%q: the error repeats the value: %s", name, raw, got)
			}
		}
	})
	t.Run("a bad value is reported with the others", func(t *testing.T) {
		_, err := Load(env(map[string]string{name: "proxy.example", "HBAND_OWNER_GITHUB_ID": "octocat"}))
		if got := problemNames(t, err); !strings.Contains(got, name) || !strings.Contains(got, "HBAND_OWNER_GITHUB_ID") {
			t.Errorf("Load did not report both problems at once: %s", got)
		}
	})
}
