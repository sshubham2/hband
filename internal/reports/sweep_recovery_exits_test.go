package reports

import (
	"os"
	"regexp"
	"runtime"
	"sort"
	"testing"
)

// Every row of the corpus names the exit recoverSetAside must take on it, and
// a file is kept (an error comes back) on exactly the rows that expect it kept.
func TestSweepRecoveryExits(t *testing.T) {
	c := loadCorpus(t)
	for _, row := range c.Rows {
		t.Run(row.Name, func(t *testing.T) {
			if row.WindowsOnly && runtime.GOOS != "windows" {
				t.Skip("this member only exists on Windows")
			}
			p := plantRow(t, row)
			got, err := p.r.lib.recoverSetAside(bg, p.id)
			if string(got) != row.Exit {
				t.Errorf("exit = %q, want %q (error: %v)", got, row.Exit, err)
			}
			if (err != nil) != row.Want.OldKept {
				t.Errorf("error = %v, want an error (keep the file): %v", err, row.Want.OldKept)
			}
		})
	}
}

// The census: every exit the code can take is either planted by a corpus row
// or named, with a reason, as not plantable. A new exit added to reports.go
// without a row fails here.
func TestSweepRecoveryCorpusCoversEveryExit(t *testing.T) {
	c := loadCorpus(t)
	src, err := os.ReadFile("reports.go")
	if err != nil {
		t.Fatal(err)
	}
	inCode := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s*rec[A-Za-z]+\s+recovery\s*=\s*"([a-z-]+)"`).FindAllStringSubmatch(string(src), -1) {
		inCode[m[1]] = true
	}
	if len(inCode) < 9 {
		t.Fatalf("found only %d exits in reports.go: the census would pass on nothing", len(inCode))
	}
	inCorpus := map[string]bool{}
	for _, r := range c.Rows {
		inCorpus[r.Exit] = true
	}
	for name := range c.UnplantableExits {
		if inCorpus[name] {
			t.Errorf("exit %q is listed as unplantable but a row plants it", name)
		}
		inCorpus[name] = true
	}
	var missing, unknown []string
	for name := range inCode {
		if !inCorpus[name] {
			missing = append(missing, name)
		}
	}
	for name := range inCorpus {
		if !inCode[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(unknown)
	if len(missing) > 0 {
		t.Errorf("exits of recoverSetAside with no corpus row: %v", missing)
	}
	if len(unknown) > 0 {
		t.Errorf("corpus names exits the code does not have: %v", unknown)
	}
}
