package program

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sshubham2/hband/internal/testutil/fakegithub"
	"github.com/sshubham2/hband/internal/testutil/outside"
)

// TestMain lets this test binary stand in for a program that exits at once:
// run with SC042_EXIT=<n> it does nothing but exit with that code.
func TestMain(m *testing.M) {
	if v := os.Getenv("SC042_EXIT"); v != "" {
		n, _ := strconv.Atoi(v)
		os.Exit(n)
	}
	os.Exit(m.Run())
}

type stop struct{}

// rec is a T that records Fatalf (and stops the caller, as Fatalf must) instead
// of failing the real test, and holds the cleanups until run says.
type rec struct {
	fatal    string
	logs     []string
	cleanups []func()
}

func (r *rec) Helper() {}
func (r *rec) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
	panic(stop{})
}
func (r *rec) Logf(format string, args ...any) { r.logs = append(r.logs, fmt.Sprintf(format, args...)) }
func (r *rec) Cleanup(f func())                { r.cleanups = append(r.cleanups, f) }

// run calls fn, absorbs the stop Fatalf makes, then runs the cleanups last
// registered first, and returns what Fatalf said ("" when it was not called).
func (r *rec) run(fn func()) string {
	func() {
		defer func() {
			if v := recover(); v != nil {
				if _, ok := v.(stop); !ok {
					panic(v)
				}
			}
		}()
		fn()
	}()
	r.finish()
	return r.fatal
}

func (r *rec) finish() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
	r.cleanups = nil
}

func moduleRootOf(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

// Build must hand the environment to the go tool as it is: a GOFLAGS carrying an
// -overlay is how the breaks test, and the epic's own checks, compile a broken
// behaviour into the program.
func TestBuildTakesGOFLAGSFromTheEnvironment(t *testing.T) {
	dir, err := os.MkdirTemp("", "sc042-overlay-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if strings.ContainsAny(dir, " \t") {
		t.Skip("the temporary folder's path has a space, which a GOFLAGS -overlay cannot carry")
	}
	src := filepath.Join(dir, "exit7.go")
	if err := os.WriteFile(src, []byte("package main\n\nimport \"os\"\n\nfunc init() { os.Exit(7) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay, _ := json.Marshal(map[string]map[string]string{"Replace": {
		filepath.Join(moduleRootOf(t), "cmd", "hband", "sc042_exit7.go"): src,
	}})
	jsonPath := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(jsonPath, overlay, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFLAGS", strings.TrimSpace(os.Getenv("GOFLAGS")+" -overlay="+jsonPath))

	bin := Build(t)
	err = exec.Command(bin).Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 7 {
		t.Fatalf("the built program did not carry the overlay (want exit 7): %v", err)
	}
}

// A program that exits before it logs 'listening addr=' fails Start's caller,
// naming the exit code, instead of leaving it to wait or to carry on.
func TestStartFailsWhenTheProgramExitsBeforeListening(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := &rec{}
	msg := r.run(func() { Start(r, exe, Options{Env: map[string]string{"SC042_EXIT": "3"}}) })
	if !strings.Contains(msg, "code 3") || !strings.Contains(msg, "listening addr=") {
		t.Fatalf("Start did not fail naming exit code 3 before 'listening addr=': %q", msg)
	}
}

// On a loopback-http base the port is in the base URL, so it is picked first; when
// it is taken by the time the program binds it, the start is tried again on a new
// one, and the stand-in GitHub follows to the new redirect address.
func TestLoopbackStartRetriesOnATakenPort(t *testing.T) {
	bin := Build(t)
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	takenPort := held.Addr().(*net.TCPAddr).Port
	old := pickPort
	defer func() { pickPort = old }()
	calls := 0
	pickPort = func() (int, error) {
		calls++
		if calls == 1 {
			return takenPort, nil
		}
		return old()
	}
	p := Start(t, bin, Options{Loopback: true})
	if calls < 2 {
		t.Fatalf("pickPort was called %d time(s): the taken port was never retried", calls)
	}
	if strings.HasSuffix(p.Base, ":"+strconv.Itoa(takenPort)) {
		t.Fatalf("the program is still on the taken port: %s", p.Base)
	}
	v := p.SignIn(t, fakegithub.User{ID: 77, Login: "retry-reader"}) // the fake's redirect_uri followed the new port
	if got := v.Get(t, "/"); got.Status != 200 {
		t.Fatalf("GET / answered %d", got.Status)
	}
}

// Nothing the harness does depends on the default address or on a proxy: with
// 127.0.0.1:8080 held and every proxy variable pointing at a closed port, the
// program still builds, starts, signs a visitor in (hband's token exchange with
// the stand-in included) and serves /healthz.
func TestStartIgnoresTheDefaultPortAndProxies(t *testing.T) {
	if l, err := net.Listen("tcp", "127.0.0.1:8080"); err == nil {
		defer l.Close()
	}
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + closed.Addr().String()
	closed.Close()
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		t.Setenv(name, dead)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	bin := Build(t)
	p := Start(t, bin, Options{})
	v := p.SignIn(t, fakegithub.User{ID: 78, Login: "proxy-reader"})
	if got := v.Get(t, "/healthz"); got.Status != 200 {
		t.Fatalf("GET /healthz answered %d", got.Status)
	}
	if p.Addr == "127.0.0.1:8080" {
		t.Fatalf("the program listened on the default address")
	}
}

// A finished test leaves nothing in the repository: the binary, the data folder
// and the working folder are made outside it, and a temporary folder that is
// inside it is refused rather than used.
func TestStartLeavesNothingInTheRepository(t *testing.T) {
	root := moduleRootOf(t)
	snap := func() map[string]bool {
		seen := map[string]bool{}
		for _, top := range []string{"cmd", "internal", "tests"} {
			filepath.WalkDir(filepath.Join(root, top), func(path string, d os.DirEntry, err error) error {
				if err == nil {
					seen[path] = true
				}
				return nil
			})
		}
		return seen
	}
	before := snap()
	t.Run("one build, start, sign-in and publish", func(t *testing.T) {
		p := Start(t, Build(t), Options{})
		if outside.Inside(os.TempDir()) {
			t.Skip("the temporary folder is inside the repository")
		}
		p.Owner(t).Publish(t, "leave-nothing", "Leave Nothing", "<!doctype html><title>x</title><p>x</p>")
	})
	for path := range snap() {
		if !before[path] {
			t.Errorf("a finished test left %s in the repository", path)
		}
	}

	inside, err := os.MkdirTemp(root, "sc042-inside-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(inside)
	t.Setenv("TMP", inside)
	t.Setenv("TEMP", inside)
	t.Setenv("TMPDIR", inside)
	r := &rec{}
	msg := r.run(func() { Build(r) })
	if !strings.Contains(msg, "inside the repository") {
		t.Fatalf("Build used a folder inside the repository: %q", msg)
	}
	r = &rec{}
	msg = r.run(func() { Start(r, os.Args[0], Options{}) })
	if !strings.Contains(msg, "inside the repository") {
		t.Fatalf("Start used a folder inside the repository: %q", msg)
	}
}

// An anonymous visitor who asks for an owner-only page is shown the redirect to
// sign in, and holds no cookie afterwards; following it would sign them in as
// whoever the stand-in GitHub approves, so GetFollow refuses to.
func TestAnonymousVisitorIsNotSignedInByAGatedPage(t *testing.T) {
	p := Start(t, Build(t), Options{})
	anon := p.Anonymous()
	got := anon.Get(t, "/admin")
	if got.Status != 303 || !strings.HasPrefix(got.Location, "/auth/github/login?next=") {
		t.Fatalf("GET /admin answered %d %q, want 303 to /auth/github/login?next=", got.Status, got.Location)
	}
	base, _ := url.Parse(p.Base)
	if n := len(anon.jar.Cookies(base)); n != 0 {
		t.Fatalf("the jar holds %d cookie(s) after a refused GET /admin", n)
	}
	r := &rec{}
	msg := r.run(func() { p.Anonymous().GetFollow(r, "/admin") })
	if !strings.Contains(msg, "sign-in route") {
		t.Fatalf("GetFollow did not stop at the sign-in hop: %q", msg)
	}
}
