//go:build sc042breaks

// Package breaks shows that the acceptance tests guard what they claim to: for a
// sample of shipped behaviours, at least one for each of E-001 to E-006, it breaks
// the behaviour in the built program and requires the epic's acceptance test to
// fail, where the same test passes against the program as it is.
//
// A break is a file added to package main of ./cmd/hband by a Go overlay in GOFLAGS,
// which the acceptance test's own build takes from the environment; nothing is
// written into the repository. The broken program registers a SQLite function,
// sc042_fired, that writes a "fired" mark the moment it runs, and as soon as its
// database has the table it adds one trigger that breaks the behaviour, calls that
// function, and writes an "installed" mark. So each broken run is one of:
//
//   - never installed: the break was never met (no table, or the trigger refused);
//   - installed but never fired: the test never made the write the break is on;
//   - fired, and the test passed: the test missed the break;
//   - fired, and the test failed: the test guards the behaviour.
//
// Only the last passes. The build tag keeps this out of go test ./...:
//
//	go test -count=1 -tags sc042breaks -timeout 40m -run ^TestBrokenProgramsFailTheirAcceptanceTests$ ./tests/acceptance/breaks
package breaks

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sshubham2/hband/internal/testutil/outside"
)

// brokenSource is the file the overlay adds to package main of ./cmd/hband.
const brokenSource = `package main

// SL-015: a deliberately broken program, added only through a Go overlay
// (GOFLAGS=-overlay) and never written into the repository.

import (
	"database/sql"
	"database/sql/driver"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
)

const (
	sc042Marks = @MARKS@
	sc042Name  = @NAME@
	sc042Table = @TABLE@
	sc042DDL   = @DDL@
)

func init() {
	// Every connection the store opens after this has sc042_fired, so a trigger can
	// note, inside the very statement that meets the break, that it was met.
	sqlite.MustRegisterScalarFunction("sc042_fired", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		os.WriteFile(filepath.Join(sc042Marks, sc042Name+"-fired-"+strconv.Itoa(os.Getpid())), []byte("fired"), 0o644)
		return nil, nil
	})
	go sc042Break()
}

func sc042Break() {
	defer func() { _ = recover() }()
	dir := os.Getenv("HBAND_DATA_DIR")
	if dir == "" {
		dir = "data"
	}
	path, err := filepath.Abs(filepath.Join(dir, "hband.db"))
	if err != nil {
		return
	}
	p := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	dsn := (&url.URL{Scheme: "file", Path: p, RawQuery: "mode=rw&_pragma=busy_timeout(10000)"}).String()
	owner := os.Getenv("HBAND_OWNER_GITHUB_ID")
	if _, err := strconv.ParseInt(owner, 10, 64); err != nil {
		owner = "0"
	}
	ddl := strings.ReplaceAll(sc042DDL, "{owner}", owner)
	for end := time.Now().Add(3 * time.Minute); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			continue
		}
		var n int
		if db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", sc042Table).Scan(&n) == nil && n == 1 {
			if _, err := db.Exec(ddl); err == nil {
				db.Close()
				os.WriteFile(filepath.Join(sc042Marks, sc042Name+"-installed-"+strconv.Itoa(os.Getpid())), []byte(path), 0o644)
				return
			}
		}
		db.Close()
	}
}
`

// The breaks. The gate, likes, placeholder and blocked triggers are E-009 line 5's
// own; opens, flatten and feedback add E-002, E-004 and E-005. Each trigger calls
// sc042_fired() first, in the statement that meets it.
var breaks = []struct{ name, behaviour, table, ddl, pkg, test string }{
	{"gate", "the owner-only admin gate (every new session is made the pinned owner's)", "sessions",
		"CREATE TRIGGER sc042_gate AFTER INSERT ON sessions BEGIN SELECT sc042_fired(); UPDATE sessions SET github_id = {owner} WHERE token_hash = NEW.token_hash; END",
		"e001_1_owner_only_admin", "TestE001OwnerOnlyAdmin"},
	{"opens", "views counted once a day per visitor (every open vanishes as it is counted)", "report_opens",
		"CREATE TRIGGER sc042_opens AFTER INSERT ON report_opens BEGIN SELECT sc042_fired(); DELETE FROM report_opens WHERE report_id = NEW.report_id AND day = NEW.day AND visitor = NEW.visitor; END",
		"e002_3_opens_counted_once", "TestE002OpensCountedOnce"},
	{"likes", "the public like count (a vote vanishes as it is cast)", "votes",
		"CREATE TRIGGER sc042_likes AFTER INSERT ON votes BEGIN SELECT sc042_fired(); DELETE FROM votes WHERE report_id = NEW.report_id AND github_id = NEW.github_id; END",
		"e003_1_sign_in_and_vote", "TestE003SignInAndVote"},
	{"flatten", "reply nesting (every reply is made a top-level comment)", "comments",
		"CREATE TRIGGER sc042_flatten AFTER INSERT ON comments WHEN NEW.parent_id IS NOT NULL BEGIN SELECT sc042_fired(); UPDATE comments SET parent_id = NULL WHERE id = NEW.id; END",
		"e004_1_comments_and_replies", "TestE004CommentsAndReplies"},
	{"feedback", "private feedback reaching the owner (every message vanishes as it is sent)", "feedback",
		"CREATE TRIGGER sc042_feedback AFTER INSERT ON feedback BEGIN SELECT sc042_fired(); DELETE FROM feedback WHERE id = NEW.id; END",
		"e005_1_private_feedback", "TestE005PrivateFeedback"},
	{"placeholder", "the 'comment removed' placeholder (a removal is undone, so the comment shows as before)", "comments",
		"CREATE TRIGGER sc042_placeholder AFTER UPDATE OF removed_at ON comments WHEN NEW.removed_at IS NOT NULL BEGIN SELECT sc042_fired(); UPDATE comments SET removed_at = NULL WHERE id = NEW.id; END",
		"e006_1_removal_and_placeholders", "TestE006RemovalAndPlaceholders"},
	{"blocked", "a blocked account's hidden comments (a block is undone as it is made)", "blocked_accounts",
		"CREATE TRIGGER sc042_blocked AFTER INSERT ON blocked_accounts BEGIN SELECT sc042_fired(); DELETE FROM blocked_accounts WHERE github_id = NEW.github_id; END",
		"e006_2_blocking_and_unblocking", "TestE006BlockingAndUnblocking"},
}

func goTool(t *testing.T) string {
	t.Helper()
	g, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("no go tool on PATH: %v", err)
	}
	return g
}

func moduleRoot(t *testing.T, gocmd string) string {
	t.Helper()
	out, err := exec.Command(gocmd, "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	return filepath.Dir(strings.TrimSpace(string(out)))
}

// baseGoFlags is the inherited GOFLAGS without a -tags (this test's own tag must not
// reach the acceptance packages) and without an -overlay.
func baseGoFlags() string {
	var keep []string
	for _, f := range strings.Fields(os.Getenv("GOFLAGS")) {
		if strings.HasPrefix(f, "-tags") || strings.HasPrefix(f, "--tags") || strings.HasPrefix(f, "-overlay") || strings.HasPrefix(f, "--overlay") {
			continue
		}
		keep = append(keep, f)
	}
	return strings.Join(keep, " ")
}

// childEnv is this process's environment with GOFLAGS set to flags.
func childEnv(flags string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "GOFLAGS=") {
			env = append(env, kv)
		}
	}
	return append(env, "GOFLAGS="+flags)
}

func run(t *testing.T, gocmd, root string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(gocmd, args...)
	cmd.Dir, cmd.Env = root, env
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("go %s: %v", strings.Join(args, " "), err)
	}
	return string(out), code
}

func tail(s string) string {
	if len(s) > 3000 {
		return "..." + s[len(s)-3000:]
	}
	return s
}

func TestBrokenProgramsFailTheirAcceptanceTests(t *testing.T) {
	gocmd := goTool(t)
	root := moduleRoot(t, gocmd)
	flags := baseGoFlags()

	// The same acceptance tests, against the program as it is, pass.
	var pkgs, names []string
	for _, b := range breaks {
		pkgs = append(pkgs, "./tests/acceptance/"+b.pkg)
		names = append(names, b.test)
	}
	args := append([]string{"test", "-count=1", "-v", "-run", "^(" + strings.Join(names, "|") + ")$"}, pkgs...)
	out, code := run(t, gocmd, root, childEnv(flags), args...)
	if code != 0 {
		t.Fatalf("the acceptance tests do not pass against the program as it is (exit %d):\n%s", code, tail(out))
	}
	for _, n := range names {
		if !strings.Contains(out, "--- PASS: "+n) {
			t.Fatalf("%s did not pass against the program as it is:\n%s", n, tail(out))
		}
	}

	for _, b := range breaks {
		t.Run(b.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "sc042-break-*")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			outside.MustBeOutside(t, dir)
			if strings.ContainsAny(dir, " \t") {
				t.Fatalf("the temporary folder %s has a space, which a GOFLAGS -overlay cannot carry", dir)
			}
			marks := filepath.Join(dir, "marks")
			if err := os.Mkdir(marks, 0o755); err != nil {
				t.Fatal(err)
			}
			src := strings.NewReplacer("@MARKS@", strconv.Quote(marks), "@NAME@", strconv.Quote(b.name),
				"@TABLE@", strconv.Quote(b.table), "@DDL@", strconv.Quote(b.ddl)).Replace(brokenSource)
			srcPath := filepath.Join(dir, "break.go")
			if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			overlay, _ := json.Marshal(map[string]map[string]string{"Replace": {
				filepath.Join(root, "cmd", "hband", "sc042_break_"+b.name+".go"): srcPath,
			}})
			jsonPath := filepath.Join(dir, "overlay.json")
			if err := os.WriteFile(jsonPath, overlay, 0o644); err != nil {
				t.Fatal(err)
			}
			broken := strings.TrimSpace(flags + " -overlay=" + jsonPath)
			if out, code := run(t, gocmd, root, childEnv(broken), "build", "-o", filepath.Join(dir, "hband"+exe()), "./cmd/hband"); code != 0 {
				t.Fatalf("the broken program does not compile:\n%s", tail(out))
			}

			out, code := run(t, gocmd, root, childEnv(broken), "test", "-count=1", "-v", "-run", "^"+b.test+"$", "./tests/acceptance/"+b.pkg)
			installed, fired := marked(marks, b.name+"-installed-"), marked(marks, b.name+"-fired-")
			failed := code == 1 && strings.Contains(out, "--- FAIL: "+b.test)
			switch {
			case !installed:
				t.Fatalf("%s: the break was never installed (no %s table met): it says nothing about %s (exit %d):\n%s", b.behaviour, b.table, b.test, code, tail(out))
			case !fired:
				t.Fatalf("%s: installed but never fired: %s never made the write the break is on (exit %d):\n%s", b.behaviour, b.test, code, tail(out))
			case !failed:
				t.Fatalf("%s: the break fired and %s did not fail (exit %d): it does not guard the behaviour:\n%s", b.behaviour, b.test, code, tail(out))
			}
			t.Logf("%s: broken, %s fails: %s", b.behaviour, b.test, firstFailure(out))
		})
	}
}

func marked(dir, prefix string) bool {
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if strings.HasPrefix(e.Name(), prefix) {
			return true
		}
	}
	return false
}

func firstFailure(out string) string {
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.Contains(l, "_test.go:") {
			return l
		}
	}
	return fmt.Sprintf("(%d bytes of output)", len(out))
}

func exe() string {
	if filepath.Separator == '\\' {
		return ".exe"
	}
	return ""
}
