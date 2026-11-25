// Command seeded serves the seeded product for a look: the real ./cmd/hband,
// started through internal/testutil/program with a stand-in GitHub, holding the
// nine-level thread of program.DeepThread, at http://127.0.0.1:<port>/.
//
//	go run ./internal/testutil/seeded -port <port>
//
// Run from the repository's root. It is what the engine's app command starts
// (E-009 finish line 7), and it replaces the retired Python launcher.
//
// The port is bound only by a program whose data is already complete. A product
// started on <port> straight away would answer there about a second before
// DeepThread has finished seeding it, and a consumer that reads the first answer
// as "ready" (the engine's look connects to the port and goes on) would read an
// empty product. So the seed is staged: a first program, on a port of its own,
// is seeded and then stopped, and the program that serves <port> is started over
// the same data folder. Killing a program that has written (so its -wal and -shm
// files are left behind) is safe: SQLite recovers them when the next program
// opens the folder.
//
// Once it is serving it prints one line, "serving http://127.0.0.1:<port>/", and
// blocks. Ctrl+C stops the program and removes the folders. When the job it runs
// in is ended instead (TerminateJobObject, as the engine stops its app) nothing
// runs after it, so the folders under the temp folder stay; nothing sweeps them,
// because a sweep by name could remove the folders of another run still using
// them.
//
// It imports only internal/testutil, like every helper there, so it links none
// of the product.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"

	"github.com/sshubham2/hband/internal/testutil/outside"
	"github.com/sshubham2/hband/internal/testutil/program"
)

// exit ends the process. A variable so a test can record the code instead.
var exit = os.Exit

func main() {
	port, err := parsePort(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			exit(0)
			return
		}
		exit(2)
		return
	}
	root := newProcessT(os.Stderr)
	run(root, port, os.Stdout)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
	root.runCleanups()
	exit(0)
}

// parsePort reads -port. A missing flag, a value that is not a whole number, one
// at or below 0 or above 65535, and any argument that is not a flag are
// refused with the usage on w: 0 would reach program.Start as "pick a port" and
// serve where nobody looks.
func parsePort(args []string, w io.Writer) (int, error) {
	fs := flag.NewFlagSet("seeded", flag.ContinueOnError)
	fs.SetOutput(w)
	port := 0
	// The value is read as text and parsed as decimal: fs.Int would take it as
	// strconv.ParseInt(s, 0, ...) does, so 010 would be port 8, 0x1F90 port 8080
	// and 1_000 port 1000, none of them the port the caller wrote.
	fs.Func("port", "the loopback port to serve the seeded product on (1-65535)", func(s string) error {
		n, err := decimal(s)
		if err != nil {
			return err
		}
		port = n
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return 0, err
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(w, "seeded: unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return 0, errors.New("unexpected argument")
	}
	if port <= 0 || port > 65535 {
		fmt.Fprintf(w, "seeded: -port must be a number from 1 to 65535, not %d\n", port)
		fs.Usage()
		return 0, errors.New("bad port")
	}
	return port, nil
}

// decimal reads s as a non-negative decimal number: ASCII digits only, so no
// sign, base prefix or digit separator.
func decimal(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty: want a decimal number")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%q is not a decimal number", s)
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is out of range", s)
	}
	return n, nil
}

// portOf is the port in a URL such as a stand-in's http://127.0.0.1:54321.
func portOf(raw string) (int, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, err
	}
	p := u.Port()
	if p == "" {
		return 0, fmt.Errorf("%q names no port", raw)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return 0, fmt.Errorf("%q: %v", raw, err)
	}
	return n, nil
}

// freePort asks the OS for a free loopback port. A variable so a test can hand
// back a given one.
var freePort = func() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// stagingPort is a free loopback port that is not n.
func stagingPort(n int) (int, error) {
	for i := 0; i < 5; i++ {
		p, err := freePort()
		if err != nil {
			return 0, err
		}
		if p != n {
			return p, nil
		}
	}
	return 0, fmt.Errorf("the OS handed out port %d five times in a row", n)
}

// run builds the program, seeds a data folder through a staging program on a
// port of its own, stops that program, starts the one that serves n over the
// same folder, and writes "serving http://127.0.0.1:<n>/" to out once it
// answers. Every failure is t.Fatalf. What it starts belongs to t's cleanups.
func run(t program.T, n int, out io.Writer) *program.Program {
	t.Helper()
	bin := program.Build(t)

	data, err := os.MkdirTemp("", "hband-seeded-data-*")
	if err != nil {
		t.Fatalf("data folder: %v", err)
		return nil
	}
	t.Cleanup(func() { removeAll(t, data) })
	outside.MustBeOutside(t, data)
	env := map[string]string{"HBAND_DATA_DIR": data}

	stage := newScope(t)
	p, err := stagingPort(n)
	if err != nil {
		stage.Fatalf("staging port: %v", err)
		return nil
	}
	s := program.Start(stage, bin, program.Options{Port: p, Env: env})
	checkStandIn(stage, s.Fake.URL(), n)
	program.DeepThread(stage, s)
	stage.end()

	final := program.Start(t, bin, program.Options{Port: n, Env: env})
	fmt.Fprintf(out, "serving http://127.0.0.1:%d/\n", n)
	return final
}

// checkStandIn fails t when the stand-in GitHub at raw sits on port n. It was
// bound before the staging program, from the same pool of ports n came from, and
// one sitting on n would answer there: a consumer would read it as the product.
// A stand-in whose port cannot be read is refused too, so a misread URL never
// lets a collision through.
func checkStandIn(t program.T, raw string, n int) {
	t.Helper()
	sp, err := portOf(raw)
	if err != nil {
		t.Fatalf("cannot read the staging stand-in's port: %v", err)
		return
	}
	if sp == n {
		t.Fatalf("the staging stand-in GitHub took port %d, the port to serve on; run it again", n)
	}
}

// removeAll deletes a folder with a few retries (a process can hold a file for a
// moment after it is gone) and logs, never fails, when it cannot.
func removeAll(t program.T, dir string) {
	var err error
	for i := 0; i < 10; i++ {
		if err = os.RemoveAll(dir); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("left %s behind: %v", dir, err)
}

// scope is a program.T over a parent: it keeps its own cleanups, which end()
// runs newest first and once, leaving the parent's alone. Fatalf ends the scope
// before it hands over to the parent, so a staging program that failed never
// outlives the failure.
type scope struct {
	parent program.T

	mu       sync.Mutex
	cleanups []func()
	ended    bool
}

func newScope(parent program.T) *scope { return &scope{parent: parent} }

func (s *scope) Helper() {}

func (s *scope) Logf(format string, args ...any) { s.parent.Logf(format, args...) }

func (s *scope) Cleanup(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanups = append(s.cleanups, f)
}

func (s *scope) Fatalf(format string, args ...any) {
	s.end()
	s.parent.Fatalf(format, args...)
}

func (s *scope) end() {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	list := s.cleanups
	s.cleanups = nil
	s.mu.Unlock()
	for i := len(list) - 1; i >= 0; i-- {
		list[i]()
	}
}

// processT is the program.T of the whole process. os.Exit ends no child
// process, so Fatalf runs every cleanup (the kill of the program it started
// among them) before it exits.
type processT struct {
	errw io.Writer

	mu       sync.Mutex
	cleanups []func()
	ran      bool
}

func newProcessT(errw io.Writer) *processT { return &processT{errw: errw} }

func (p *processT) Helper() {}

func (p *processT) Logf(format string, args ...any) {
	fmt.Fprintf(p.errw, "%s\n", fmt.Sprintf(format, args...))
}

func (p *processT) Cleanup(f func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanups = append(p.cleanups, f)
}

// Fatalf prints the message, runs the cleanups and exits 1. Called from inside
// a cleanup it exits at once: the list is not run again.
func (p *processT) Fatalf(format string, args ...any) {
	fmt.Fprintf(p.errw, "seeded: %s\n", fmt.Sprintf(format, args...))
	p.runCleanups()
	exit(1)
}

// runCleanups runs the cleanups newest first, once.
func (p *processT) runCleanups() {
	p.mu.Lock()
	if p.ran {
		p.mu.Unlock()
		return
	}
	p.ran = true
	list := p.cleanups
	p.cleanups = nil
	p.mu.Unlock()
	for i := len(list) - 1; i >= 0; i-- {
		list[i]()
	}
}
