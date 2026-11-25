// Package program builds the real ./cmd/hband and starts it for an acceptance
// test: on its own address, with a throwaway configuration and database, and the
// fakegithub stand-in for GitHub. A test then talks to it only over HTTP, through
// a Visitor.
//
// It is test-only and imports no package of the product, so linking it into an
// acceptance test links none of the product either. It reads the environment only
// to pass it on to the go tool and to the program it starts (every inherited
// HBAND_* name is dropped first), and it never reads CHROME_BIN or any other
// browser setting: a browser is the business of internal/testutil/headless.
//
// Every folder it makes (the binary's, the program's data folder and working
// folder) comes from os.MkdirTemp and is checked to lie outside the repository,
// so a finished test leaves nothing in the tree.
package program

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sshubham2/hband/internal/testutil/fakegithub"
	"github.com/sshubham2/hband/internal/testutil/outside"
)

// OwnerID is the GitHub user ID the started program pins as its owner.
const OwnerID int64 = 4242

const (
	testSecret = "hband-test-secret"
	publicBase = "https://hband.test"
	publicHost = "hband.test"
)

// T is the part of testing.TB the harness uses. Fatalf must not return.
type T interface {
	Helper()
	Fatalf(format string, args ...any)
	Logf(format string, args ...any)
	Cleanup(func())
}

var listeningRE = regexp.MustCompile(`listening addr=(\S+)`)

// Build makes the program with `go build ./cmd/hband` and returns the path of
// the binary. The environment is passed through untouched (CGO_ENABLED=0 is
// added), so a GOFLAGS carrying -overlay reaches the build.
func Build(t T) string {
	t.Helper()
	goTool, err := exec.LookPath("go")
	// ported:E3-43,E41-39,E42-41,E5-41,E61-41,E62-42
	if err != nil {
		t.Fatalf("program: no go tool on PATH to build ./cmd/hband with: %v", err)
		return ""
	}
	dir, err := os.MkdirTemp("", "hband-acceptance-bin-*")
	if err != nil {
		t.Fatalf("program: binary folder: %v", err)
		return ""
	}
	t.Cleanup(func() { removeLoud(t, dir) })
	outside.MustBeOutside(t, dir)
	bin := filepath.Join(dir, "hband"+exeSuffix())
	cmd := exec.Command(goTool, "build", "-buildvcs=false", "-o", bin, "github.com/sshubham2/hband/cmd/hband")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	// ported:E3-46,E41-42,E42-44,E5-44,E61-44,E62-45,SS-75
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("program: `go build ./cmd/hband` failed: %v\n%s", err, out)
		return ""
	}
	return bin
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// Options say how Start runs the program.
type Options struct {
	// Loopback runs it on http://127.0.0.1:<port>, a base a real browser can
	// use (the session cookie is then the plain one, not __Host-). The port is
	// picked, and the start retried (at most 3 attempts in all) when the
	// program exits with code 1, the code of a failed listen, before it logs
	// that it is listening.
	Loopback bool
	// Port, when above 0, is the fixed port; it implies Loopback and is never
	// retried.
	Port int
	// Env overrides or adds HBAND_* variables of the program's environment.
	Env map[string]string
}

// Program is a running program.
type Program struct {
	Base    string // the public base URL the program is configured with
	Addr    string // host:port it listens on
	Fake    *fakegithub.Fake
	OwnerID int64

	loopback bool
	log      *syncBuf
}

// Log is what the program has written so far.
func (p *Program) Log() string { return p.log.String() }

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// pickPort asks the OS for a free loopback port. A package variable so a test
// can hand back a taken one first.
var pickPort = func() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Start runs bin (from Build) and returns once it logs 'listening addr=' and
// answers GET /healthz with 200. A program that exits first fails t, naming its
// exit code and the end of its log.
func Start(t T, bin string, o Options) *Program {
	t.Helper()
	work, err := os.MkdirTemp("", "hband-acceptance-work-*")
	if err != nil {
		t.Fatalf("program: working folder: %v", err)
		return nil
	}
	t.Cleanup(func() { removeLoud(t, work) })
	outside.MustBeOutside(t, work)
	data, err := os.MkdirTemp("", "hband-acceptance-data-*")
	if err != nil {
		t.Fatalf("program: data folder: %v", err)
		return nil
	}
	t.Cleanup(func() { removeLoud(t, data) })
	outside.MustBeOutside(t, data)

	fake := fakegithub.New("cid", testSecret, publicBase+"/auth/github/callback")
	t.Cleanup(fake.Close)

	loop := o.Loopback || o.Port > 0
	attempts := 1
	if loop && o.Port == 0 {
		attempts = 3
	}
	for attempt := 1; ; attempt++ {
		base, addr := publicBase, "127.0.0.1:0"
		if loop {
			port := o.Port
			if port == 0 {
				var err error
				if port, err = pickPort(); err != nil {
					t.Fatalf("program: no free port: %v", err)
					return nil
				}
			}
			base, addr = fmt.Sprintf("http://127.0.0.1:%d", port), fmt.Sprintf("127.0.0.1:%d", port)
		}
		fake.SetRedirectURI(base + "/auth/github/callback")
		env := childEnv(map[string]string{
			"HBAND_ADDR":                 addr,
			"HBAND_BASE_URL":             base,
			"HBAND_DATA_DIR":             data,
			"HBAND_OWNER_GITHUB_ID":      fmt.Sprint(OwnerID),
			"HBAND_GITHUB_CLIENT_ID":     "cid",
			"HBAND_GITHUB_CLIENT_SECRET": testSecret,
			"HBAND_GITHUB_URL":           fake.URL(),
			"HBAND_GITHUB_API_URL":       fake.URL(),
			"HBAND_MAX_UPLOAD_BYTES":     "26214400",
			"HBAND_TRUSTED_PROXIES":      "127.0.0.1",
		}, o.Env)
		log := &syncBuf{}
		listening, exited, code := launch(t, bin, work, env, log)
		if listening != "" {
			p := &Program{Base: base, Addr: listening, Fake: fake, OwnerID: OwnerID, loopback: loop, log: log}
			p.healthy(t)
			return p
		}
		if exited && code == 1 && attempt < attempts {
			t.Logf("program: attempt %d exited with code 1 before listening (port taken?); trying again:\n%s", attempt, tailOf(log.String()))
			continue
		}
		// ported:E3-196,E41-192,E42-194,E5-194,E61-194,E62-195
		if exited {
			t.Fatalf("program: the program exited with code %d before it logged 'listening addr=':\n%s", code, tailOf(log.String()))
		} else {
			// ported:E3-202,E41-198,E42-200,E5-200,E61-200,E62-201
			t.Fatalf("program: no 'listening addr=' within 30s:\n%s", tailOf(log.String()))
		}
		return nil
	}
}

// launch starts the binary and waits for it to log where it listens. It returns
// the address, or "" with whether the program exited (and its exit code).
func launch(t T, bin, dir string, env []string, log *syncBuf) (listening string, exited bool, code int) {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatalf("program: start %s: %v", bin, err)
		return "", true, -1
	}
	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if m := listeningRE.FindStringSubmatch(log.String()); m != nil {
			return m[1], false, 0
		}
		select {
		case <-done:
			if m := listeningRE.FindStringSubmatch(log.String()); m != nil {
				return m[1], false, 0
			}
			return "", true, cmd.ProcessState.ExitCode()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return "", false, 0
}

// childEnv is the environment of the program: what this process has, less every
// HBAND_* name, plus set, plus extra.
func childEnv(set, extra map[string]string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name := kv
		if i := strings.IndexByte(kv, '='); i > 0 {
			name = kv[:i]
		}
		if !strings.HasPrefix(strings.ToUpper(name), "HBAND_") {
			env = append(env, kv)
		}
	}
	for k, v := range set {
		if _, over := extra[k]; !over {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// healthy requires GET /healthz to answer 200: the program is serving, not
// merely bound.
func (p *Program) healthy(t T) {
	t.Helper()
	u, err := url.Parse(p.Base)
	if err != nil {
		t.Fatalf("program: base %q: %v", p.Base, err)
		return
	}
	req, _ := http.NewRequest(http.MethodGet, "http://"+p.Addr+"/healthz", nil)
	req.Host = u.Host
	c := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: 30 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("program: GET /healthz: %v\n%s", err, tailOf(p.Log()))
		return
	}
	resp.Body.Close()
	// ported:SS-90
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("program: GET /healthz answered %d, want 200\n%s", resp.StatusCode, tailOf(p.Log()))
	}
}

// removeLoud deletes a folder with a few retries (a process can hold a file for
// a moment after it is gone) and logs, never fails, when it cannot.
func removeLoud(t T, dir string) {
	var err error
	for i := 0; i < 10; i++ {
		if err = os.RemoveAll(dir); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("program: left %s behind: %v", dir, err)
}

func tailOf(s string) string {
	const max = 2000
	if len(s) > max {
		return "..." + s[len(s)-max:]
	}
	return s
}
