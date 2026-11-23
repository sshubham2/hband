package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sshubham2/hband/internal/store"
	"github.com/sshubham2/hband/internal/testutil/fakegithub"
)

const secretMarker = "SECRET-MARKER-7c1e42"

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func baseEnv(dataDir, addr string) map[string]string {
	return map[string]string{
		"HBAND_ADDR":                 addr,
		"HBAND_DATA_DIR":             dataDir,
		"HBAND_BASE_URL":             "https://hband.test",
		"HBAND_OWNER_GITHUB_ID":      "4242",
		"HBAND_GITHUB_CLIENT_ID":     "cid",
		"HBAND_GITHUB_CLIENT_SECRET": secretMarker,
	}
}

func mapGetenv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestRealMainRefusesBadConfig(t *testing.T) {
	bad := map[string]func(map[string]string){
		"no owner":        func(m map[string]string) { delete(m, "HBAND_OWNER_GITHUB_ID") },
		"login as owner":  func(m map[string]string) { m["HBAND_OWNER_GITHUB_ID"] = "octocat" },
		"http base":       func(m map[string]string) { m["HBAND_BASE_URL"] = "http://hband.example" },
		"remote http gh":  func(m map[string]string) { m["HBAND_GITHUB_URL"] = "http://evil.example" },
		"remote http api": func(m map[string]string) { m["HBAND_GITHUB_API_URL"] = "http://evil.example" },
	}
	wantName := map[string]string{
		"no owner": "HBAND_OWNER_GITHUB_ID", "login as owner": "HBAND_OWNER_GITHUB_ID", "http base": "HBAND_BASE_URL",
		"remote http gh": "HBAND_GITHUB_URL", "remote http api": "HBAND_GITHUB_API_URL",
	}
	for name, mutate := range bad {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			env := baseEnv(dir, "127.0.0.1:0")
			mutate(env)
			var out syncBuffer
			code := realMain(context.Background(), mapGetenv(env), &out)
			if code != 2 {
				t.Fatalf("exit code %d, want 2: %s", code, out.String())
			}
			if !strings.Contains(out.String(), wantName[name]) {
				t.Fatalf("the refusal does not name %s: %s", wantName[name], out.String())
			}
			if strings.Contains(out.String(), secretMarker) {
				t.Fatal("the client secret was printed")
			}
			if _, err := os.Stat(dir); err == nil {
				t.Fatal("the data directory was created before the configuration was accepted")
			}
		})
	}

	t.Run("a database from a newer binary", func(t *testing.T) {
		dir := t.TempDir()
		st, err := store.Open(filepath.Join(dir, "hband.db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB().Exec("PRAGMA user_version = 99"); err != nil {
			t.Fatal(err)
		}
		st.Close()
		var out syncBuffer
		code := realMain(context.Background(), mapGetenv(baseEnv(dir, "127.0.0.1:0")), &out)
		if code != 1 {
			t.Fatalf("exit code %d, want 1: %s", code, out.String())
		}
		if strings.Contains(out.String(), "listening") || strings.Contains(out.String(), secretMarker) {
			t.Fatalf("unexpected output: %s", out.String())
		}
	})

	t.Run("a good start and a clean shutdown", func(t *testing.T) {
		dir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var out syncBuffer
		done := make(chan int, 1)
		go func() { done <- realMain(ctx, mapGetenv(baseEnv(dir, "127.0.0.1:0")), &out) }()
		addr := waitForListening(t, &out, done)
		resp, err := http.Get("http://" + addr + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("/healthz: %d", resp.StatusCode)
		}
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Fatalf("exit code %d after shutdown", code)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("realMain did not return after the context ended")
		}
		for _, leak := range []string{secretMarker} {
			if strings.Contains(out.String(), leak) {
				t.Fatalf("the log contains %q", leak)
			}
		}
	})
}

var listeningRE = regexp.MustCompile(`listening addr=(\S+)`)

func waitForListening(t *testing.T, out *syncBuffer, exited <-chan int) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if m := listeningRE.FindStringSubmatch(out.String()); m != nil {
			return m[1]
		}
		select {
		case code := <-exited:
			t.Fatalf("the process exited with %d before listening: %s", code, out.String())
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("no 'listening addr=' within 30s: %s", out.String())
	return ""
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "hband")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build (CGO_ENABLED=0): %v\n%s", err, out)
	}
	return bin
}

type proc struct {
	cmd    *exec.Cmd
	out    *syncBuffer
	exited chan error
}

// startProc runs the binary with exactly the given HBAND_* environment.
func startProc(t *testing.T, bin string, env map[string]string) *proc {
	t.Helper()
	cmd := exec.Command(bin)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HBAND_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	p := &proc{cmd: cmd, out: &syncBuffer{}, exited: make(chan error, 1)}
	cmd.Stdout, cmd.Stderr = p.out, p.out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	go func() { p.exited <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			<-p.exited
		}
	})
	return p
}

// waitExit returns the exit code, failing if the process outlives the limit.
func (p *proc) waitExit(t *testing.T, limit time.Duration) int {
	t.Helper()
	select {
	case <-p.exited:
		return p.cmd.ProcessState.ExitCode()
	case <-time.After(limit):
		t.Fatalf("the process did not exit within %s: %s", limit, p.out.String())
		return -1
	}
}

func (p *proc) waitListening(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if listeningRE.MatchString(p.out.String()) {
			return
		}
		select {
		case err := <-p.exited:
			t.Fatalf("the process exited (%v) before listening: %s", err, p.out.String())
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("no 'listening addr=' within 30s: %s", p.out.String())
}

// rewrite sends requests for https://hband.test to the real listener, the way
// the finish-line harness does, and leaves every other host alone. The cookie
// jar still sees the original https URL, because the client consults it
// before the transport runs.
type rewrite struct{ addr string }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "hband.test" {
		return http.DefaultTransport.RoundTrip(req)
	}
	c := req.Clone(req.Context())
	c.URL.Scheme, c.URL.Host = "http", r.addr
	resp, err := http.DefaultTransport.RoundTrip(c)
	if resp != nil {
		resp.Request = req
	}
	return resp, err
}

func browser(addr string, follow bool) *http.Client {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Transport: rewrite{addr}, Jar: jar, Timeout: 20 * time.Second}
	if !follow {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return c
}

func fetch(t *testing.T, c *http.Client, rawURL string) (int, http.Header, string) {
	t.Helper()
	resp, err := c.Get(rawURL)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(body)
}

func TestBinary(t *testing.T) {
	bin := buildBinary(t)

	t.Run("the finish line's environment shape", func(t *testing.T) {
		fake := fakegithub.New("cid", secretMarker, "https://hband.test/auth/github/callback")
		defer fake.Close()
		addr := freeAddr(t)
		env := baseEnv(t.TempDir(), addr)
		env["HBAND_GITHUB_URL"] = fake.URL()
		env["HBAND_GITHUB_API_URL"] = fake.URL()
		env["HBAND_TRUSTED_PROXIES"] = "127.0.0.1"
		p := startProc(t, bin, env)
		p.waitListening(t)

		// The owner signs in and reaches the admin page.
		fake.SetUser(fakegithub.User{ID: 4242, Login: "the-owner", AvatarURL: "https://avatars.githubusercontent.com/u/4242"})
		owner := browser(addr, true)
		status, _, body := fetch(t, owner, "https://hband.test/auth/github/login?next="+url.QueryEscape("/admin"))
		if status != 200 || !strings.Contains(body, "data-hband-admin") || !strings.Contains(body, "@the-owner") {
			t.Fatalf("owner at /admin: %d %s", status, body)
		}
		nf := browser(addr, false)
		nf.Jar = owner.Jar
		if status, _, body := fetch(t, nf, "https://hband.test/admin"); status != 200 || !strings.Contains(body, "data-hband-admin") {
			t.Fatalf("owner's exact GET /admin without following redirects: %d", status)
		}

		// Another GitHub account is refused.
		fake.SetUser(fakegithub.User{ID: 4343, Login: "someone-else"})
		other := browser(addr, true)
		status, _, body = fetch(t, other, "https://hband.test/auth/github/login?next="+url.QueryEscape("/admin"))
		if status != 403 || strings.Contains(body, "data-hband-admin") {
			t.Fatalf("another GitHub account at /admin: %d %s", status, body)
		}

		// An anonymous visitor is sent to sign in.
		status, hdr, body := fetch(t, browser(addr, false), "https://hband.test/admin")
		if status != 303 || !strings.HasPrefix(hdr.Get("Location"), "/auth/github/login?next=") || strings.Contains(body, "data-hband-admin") {
			t.Fatalf("anonymous at /admin: %d %q", status, hdr.Get("Location"))
		}

		log := p.out.String()
		if !strings.Contains(log, "overridden") || !strings.Contains(log, "owner_github_id=4242") {
			t.Fatalf("the startup log should state the override and the owner: %s", log)
		}
		if strings.Contains(log, secretMarker) {
			t.Fatal("the client secret reached the log")
		}
	})

	t.Run("without overrides sign-in goes to github.com", func(t *testing.T) {
		addr := freeAddr(t)
		env := baseEnv(t.TempDir(), addr)
		// Names the program does not read are ignored, whatever they hold.
		env["HBAND_GITHUB_AUTH_URL"] = "http://evil.example"
		env["HBAND_GITHUB_TOKEN_URL"] = "http://evil.example"
		p := startProc(t, bin, env)
		p.waitListening(t)

		c := browser(addr, false)
		if status, _, body := fetch(t, c, "http://"+addr+"/healthz"); status != 200 || body != "ok" {
			t.Fatalf("/healthz: %d %q", status, body)
		}
		status, hdr, _ := fetch(t, c, "http://"+addr+"/auth/github/login")
		if status != 302 {
			t.Fatalf("login: %d", status)
		}
		loc, err := url.Parse(hdr.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		q := loc.Query()
		if loc.Scheme != "https" || loc.Host != "github.com" || loc.Path != "/login/oauth/authorize" {
			t.Fatalf("login redirected to %s", hdr.Get("Location"))
		}
		if q.Get("client_id") != "cid" || q.Get("redirect_uri") != "https://hband.test/auth/github/callback" ||
			q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || len(q.Get("state")) != 43 ||
			q.Has("scope") || q.Get("response_type") != "code" {
			t.Fatalf("authorize parameters wrong: %v", q)
		}
		if strings.Contains(p.out.String(), "overridden") {
			t.Fatalf("an override was reported without one: %s", p.out.String())
		}
	})

	t.Run("a remote http identity provider is refused", func(t *testing.T) {
		for _, name := range []string{"HBAND_GITHUB_URL", "HBAND_GITHUB_API_URL"} {
			env := baseEnv(t.TempDir(), freeAddr(t))
			env[name] = "http://evil.example"
			p := startProc(t, bin, env)
			if code := p.waitExit(t, 10*time.Second); code != 2 {
				t.Fatalf("%s: exit code %d, want 2: %s", name, code, p.out.String())
			}
			out := p.out.String()
			if !strings.Contains(out, name) || strings.Contains(out, secretMarker) || strings.Contains(out, "listening") {
				t.Fatalf("%s: unexpected output: %s", name, out)
			}
		}
	})

	t.Run("no owner ID", func(t *testing.T) {
		for _, owner := range []string{"", "octocat"} {
			env := baseEnv(t.TempDir(), freeAddr(t))
			if owner == "" {
				delete(env, "HBAND_OWNER_GITHUB_ID")
			} else {
				env["HBAND_OWNER_GITHUB_ID"] = owner
			}
			start := time.Now()
			p := startProc(t, bin, env)
			code := p.waitExit(t, 10*time.Second)
			out := p.out.String()
			if code != 2 || time.Since(start) > 10*time.Second {
				t.Fatalf("owner %q: exit code %d after %s: %s", owner, code, time.Since(start), out)
			}
			if !strings.Contains(out, "HBAND_OWNER_GITHUB_ID") || strings.Contains(out, secretMarker) || strings.Contains(out, "listening") {
				t.Fatalf("owner %q: unexpected output: %s", owner, out)
			}
		}
	})
}
