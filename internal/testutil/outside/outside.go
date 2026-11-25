// Package outside keeps a test's scratch folders out of the repository. A
// folder a test makes inside the module is a file left in the tree when the
// test ends, which the acceptance run fails on; so every helper that makes one
// (the program harness, the DevTools driver, the breaks test) asks here first.
//
// The module root is whatever `go env GOMOD` names, taken once. Both paths are
// made absolute and have their symbolic links and 8.3 short names resolved
// before they are compared, so C:\Users\SSHUB~1\... and C:\Users\sshub\... are
// the same place. This package reads no environment itself.
package outside

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// T is the part of testing.TB that MustBeOutside uses.
type T interface {
	Helper()
	Fatalf(format string, args ...any)
}

var (
	once sync.Once
	root string // "" when no module root can be found
)

func moduleRoot() string {
	once.Do(func() {
		goTool, err := exec.LookPath("go")
		if err != nil {
			return
		}
		out, err := exec.Command(goTool, "env", "GOMOD").Output()
		if err != nil {
			return
		}
		gomod := strings.TrimSpace(string(out))
		if gomod == "" || gomod == os.DevNull || gomod == "NUL" {
			return
		}
		root = resolve(filepath.Dir(gomod))
	})
	return root
}

// resolve makes p absolute and follows links; when the folder does not exist
// yet, its nearest existing parent is resolved and the rest appended.
func resolve(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	rest := ""
	for cur := abs; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// Inside reports whether dir is the module root or anything under it. It is
// false when no module root is known.
func Inside(dir string) bool {
	r := moduleRoot()
	if r == "" {
		return false
	}
	rel, err := filepath.Rel(r, resolve(dir))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// MustBeOutside fails the test when dir is inside the module.
func MustBeOutside(t T, dir string) {
	t.Helper()
	if Inside(dir) {
		t.Fatalf("the test folder %s is inside the repository (module root %s): a test must leave nothing in the tree; set TMP or TEMP to a folder outside it", dir, moduleRoot())
	}
}
