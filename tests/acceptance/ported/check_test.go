package ported

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// moduleRoot is the repository root: this package is tests/acceptance/ported.
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("no go.mod at %s: %v", root, err)
	}
	return root
}

// TestPortedAssertionsHaveAGoTest is the record that lets the Python be deleted:
// every assertion site of every retired source has a row, every row's Go check is
// where it says (a ported:<ID> tag on the statement that makes it), every tag has a
// row, and every source's text is the edition the rows were made from.
func TestPortedAssertionsHaveAGoTest(t *testing.T) {
	for _, p := range Check(moduleRoot(t), "tests/acceptance/ported/testdata", sources, rows) {
		t.Error(p)
	}
}

// A small world of its own: one retired Python source, one Go body, and the Go
// files that port them, in a temporary repository.
const (
	pySource = `"""a retired check"""
import sys


class GitHub:
    def do_POST(self):
        return self.answer(400, '{"error": "bad_verification_code"}')


def check(ok, why):  # the helper: each call of it is a site
    if not ok:
        raise AssertionError(why)


def main():
    # assert in a comment is not a site
    check(1 == 1, "one is one")
    assert 2 == 2, "two is two"
    return 0


if __name__ == "__main__":
    sys.exit(main())
`
	goPort = `package x

import "testing"

func TestX(t *testing.T) {
	// ported:P-17
	if 1 != 1 {
		t.Fatalf("one is one")
	}
	// ported:P-18
	if 2 != 2 {
		t.Fatalf("two is two")
	}
}
`
	goBody = "package y\n\nimport \"testing\"\n\nfunc TestY(t *testing.T) {}\n"
)

func world(t *testing.T) (root string, srcs []Source, rws []Row) {
	t.Helper()
	root = t.TempDir()
	write := func(rel, text string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("snap/p.txt", pySource)
	write("tests/acceptance/x/x_test.go", goPort)
	write("tests/acceptance/y/y_test.go", goBody)
	write("internal/testutil/fakegithub/f_test.go", "package fakegithub\n\nimport \"testing\"\n\nfunc TestRefuses(t *testing.T) {}\n")
	srcs = []Source{
		{Name: "p.py", Edition: EditionOf([]byte(pySource)), Snapshot: "p.txt", FakeSide: [2]int{5, 8}},
		{Name: "y.go", Edition: EditionOf([]byte(goBody)), File: "tests/acceptance/y/y_test.go"},
	}
	rws = []Row{
		{ID: "P-7", Source: "p.py", At: 7, Kind: Helper, By: "internal/testutil/fakegithub.TestRefuses", Via: "tests/acceptance/x.TestX", Why: "the stand-in refuses an unknown code"},
		{ID: "P-12", Source: "p.py", At: 12, Kind: Superseded, Why: "check()'s raise; each call has its row"},
		{ID: "P-17", Source: "p.py", At: 17, Kind: Ported, By: "tests/acceptance/x.TestX"},
		{ID: "P-18", Source: "p.py", At: 18, Kind: Ported, By: "tests/acceptance/x.TestX"},
		{ID: "P-23", Source: "p.py", At: 23, Kind: Superseded, Why: "the exit status"},
		{ID: "Y", Source: "y.go", Kind: Verbatim, By: "tests/acceptance/y.TestY"},
	}
	return root, srcs, rws
}

func without(rws []Row, id string) []Row {
	var out []Row
	for _, r := range rws {
		if r.ID != id {
			out = append(out, r)
		}
	}
	return out
}

func changed(rws []Row, id string, fn func(*Row)) []Row {
	out := append([]Row(nil), rws...)
	for i := range out {
		if out[i].ID == id {
			fn(&out[i])
		}
	}
	return out
}

// The checker refuses every way a table can fail to account for a source.
func TestPortedCheckerRefusesGaps(t *testing.T) {
	root, srcs, rws := world(t)
	if got := Check(root, "snap", srcs, rws); len(got) != 0 {
		t.Fatalf("the sound table is refused: %v", got)
	}
	if got := fmt.Sprint(Sites(Canonical([]byte(pySource)))); got != "[12 17 18 23]" {
		t.Fatalf("Sites found %s in the small source, want [12 17 18 23]", got)
	}
	cases := []struct {
		name string
		srcs []Source
		rows []Row
		want string
	}{
		{"a site with no row", srcs, without(rws, "P-18"), "line 18 is an assertion site and has no row"},
		{"a row for a line the source does not have", srcs, append(append([]Row(nil), rws...), Row{ID: "P-99", Source: "p.py", At: 99, Kind: Superseded, Why: "x"}), "line 99 is not in p.py"},
		{"a Ported row whose function holds no tag", srcs, changed(rws, "P-17", func(r *Row) { r.ID = "P-17b" }), "holds no ported:P-17b tag"},
		{"a stray tag", srcs, without(rws, "P-17"), "the tag ported:P-17 (tests/acceptance/x/x_test.go:6) has no row"},
		{"a Ported row naming no function", srcs, changed(rws, "P-17", func(r *Row) { r.By = "tests/acceptance/x.TestGone" }), "tests/acceptance/x.TestGone is no function"},
		{"an Amended row with no Why", srcs, changed(rws, "P-23", func(r *Row) { r.Kind, r.Why = Amended, "" }), "row P-23 is Amended and has no Why"},
		{"a Superseded row with no Why", srcs, changed(rws, "P-23", func(r *Row) { r.Why = "" }), "row P-23 is Superseded and has no Why"},
		{"a Superseded row that names a By", srcs, changed(rws, "P-23", func(r *Row) { r.By = "tests/acceptance/x.TestX" }), "row P-23 is Superseded and names a By"},
		{"a Verbatim body whose sha256 is not its Edition", []Source{srcs[0], {Name: "y.go", Edition: EditionOf([]byte(goBody + "// edited\n")), File: srcs[1].File}}, rws, "source y.go: tests/acceptance/y/y_test.go has the sha256"},
		{"a snapshot whose sha256 is not its Edition", []Source{{Name: "p.py", Edition: EditionOf([]byte("other")), Snapshot: "p.txt", FakeSide: srcs[0].FakeSide}, srcs[1]}, rws, "source p.py: snap/p.txt has the sha256"},
		{"a Go body with no Verbatim row", srcs, without(rws, "Y"), "source y.go is a Go body and has no Verbatim row"},
		{"a stand-in GitHub check mapped to a reason", srcs, changed(rws, "P-7", func(r *Row) { r.Kind, r.By, r.Via = Amended, "", "" }), "row P-7 is a stand-in GitHub check"},
		{"a stand-in GitHub check with no acceptance test", srcs, changed(rws, "P-7", func(r *Row) { r.Via = "" }), "row P-7 is a stand-in GitHub check"},
		{"a row id used twice", srcs, append(append([]Row(nil), rws...), Row{ID: "P-12", Source: "p.py", At: 1, Kind: Superseded, Why: "x"}), "row id P-12 is used twice"},
		{"two rows for one line", srcs, append(append([]Row(nil), rws...), Row{ID: "P-12b", Source: "p.py", At: 12, Kind: Superseded, Why: "x"}), "p.py line 12 has two rows"},
		{"a row of an undeclared source", srcs, append(append([]Row(nil), rws...), Row{ID: "Q", Source: "q.py", At: 1, Kind: Superseded, Why: "x"}), "names the undeclared source q.py"},
		{"a Helper row naming no helper test", srcs, changed(rws, "P-7", func(r *Row) { r.By = "internal/testutil/fakegithub.TestGone" }), "Helper By \"internal/testutil/fakegithub.TestGone\" is not an existing test"},
		{"a Verbatim row for a Python source", srcs, changed(rws, "P-23", func(r *Row) { r.Kind, r.By, r.Why = Verbatim, "tests/acceptance/x.TestX", "" }), "only a Go body can be Verbatim"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Check(root, "snap", c.srcs, c.rows)
			for _, p := range got {
				if strings.Contains(p, c.want) {
					return
				}
			}
			t.Fatalf("not refused: want a problem containing %q, got %q", c.want, got)
		})
	}
	t.Run("a snapshot checked out with CRLF line ends and a byte-order mark is the same edition", func(t *testing.T) {
		p := filepath.Join(root, "snap", "p.txt")
		crlf := "\uFEFF" + strings.ReplaceAll(pySource, "\n", "\r\n") + "\r\n"
		if err := os.WriteFile(p, []byte(crlf), 0o644); err != nil {
			t.Fatal(err)
		}
		defer os.WriteFile(p, []byte(pySource), 0o644)
		if got := Check(root, "snap", srcs, rws); len(got) != 0 {
			t.Fatalf("a CRLF checkout of the snapshot is refused (git's core.autocrlf makes one on Windows): %v", got)
		}
	})
	t.Run("a tag that is not on a statement that checks", func(t *testing.T) {
		loose := strings.Replace(goPort, "\t// ported:P-18\n", "\t// ported:P-18\n\tx := 1\n\t_ = x\n", 1)
		if err := os.WriteFile(filepath.Join(root, "tests", "acceptance", "x", "x_test.go"), []byte(loose), 0o644); err != nil {
			t.Fatal(err)
		}
		defer os.WriteFile(filepath.Join(root, "tests", "acceptance", "x", "x_test.go"), []byte(goPort), 0o644)
		for _, p := range Check(root, "snap", srcs, rws) {
			if strings.Contains(p, "ported:P-18") && strings.Contains(p, "not on or just above a statement that checks") {
				return
			}
		}
		t.Fatal("a tag two statements above its check was accepted")
	})
}

// The acceptance tests and the test helpers link none of the product: what they
// import, test imports included, is the standard library, internal/testutil and
// tests/acceptance, and nothing else. This holds by construction, whatever the
// spelling of an import, where the regular expressions of the fitness rules are
// tripwires.
func TestAcceptanceLinksNoneOfTheProduct(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("no go tool on PATH: %v", err)
	}
	gomod, err := exec.Command(goTool, "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	cmd := exec.Command(goTool, "list", "-deps", "-test", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./tests/acceptance/...", "./internal/testutil/...")
	cmd.Dir = filepath.Dir(strings.TrimSpace(string(gomod)))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	const module = "github.com/sshubham2/hband/"
	seen := 0
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		seen++
		if i := strings.Index(p, " ["); i >= 0 { // a package as compiled for a test: "x [x.test]"
			p = p[:i]
		}
		p = strings.TrimSuffix(strings.TrimSuffix(p, ".test"), "_test")
		if strings.HasPrefix(p, module+"internal/testutil/") || strings.HasPrefix(p, module+"tests/acceptance/") {
			continue
		}
		t.Errorf("an acceptance test or a test helper links %s: it must reach the program only over HTTP", p)
	}
	if seen == 0 {
		t.Fatal("go list named no package: the check is vacuous")
	}
}
