package reports

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sshubham2/hband/internal/store"
)

// The corpus is testdata/sweep_recovery.json: every way a restart can meet a
// set-aside file, as plantable states. Each row is run through the real Open.

type expectation struct {
	OpenErr bool   `json:"open_err"`
	OldKept bool   `json:"old_kept"`
	Cur     string `json:"cur"`
}

type corpusRow struct {
	Name        string       `json:"name"`
	WindowsOnly bool         `json:"windows_only"`
	Row         string       `json:"row"`
	Cur         string       `json:"cur"`
	Old         string       `json:"old"`
	DB          string       `json:"db"`
	Exit        string       `json:"exit"`
	Want        expectation  `json:"want"`
	Heal        string       `json:"heal"`
	AfterHeal   *expectation `json:"after_heal"`
}

type corpus struct {
	UnplantableExits map[string]string `json:"unplantable_exits"`
	Rows             []corpusRow       `json:"rows"`
}

func loadCorpus(t *testing.T) corpus {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "sweep_recovery.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c corpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("testdata/sweep_recovery.json: %v", err)
	}
	if len(c.Rows) == 0 {
		t.Fatal("the corpus has no rows")
	}
	return c
}

var (
	corpusRowBytes   = htmlOf(300, "row")
	corpusOtherBytes = htmlOf(400, "other")
)

// planted is a rig with one corpus row's files laid out the way a crash or a
// host fault leaves them.
type planted struct {
	r        *rig
	id       int64
	cur, old string
	held     *os.File
}

func plantRow(t *testing.T, c corpusRow) *planted {
	t.Helper()
	r := newRig(t)
	p := &planted{r: r, id: 7}
	if c.Row == "present" {
		p.id = r.mustSave(goodForm("crash", "Crash", "published", corpusRowBytes)).ID
	}
	p.cur, p.old = r.lib.path(p.id), r.lib.setAsidePath(p.id)

	must(t, os.RemoveAll(p.cur))
	switch c.Cur {
	case "absent":
	case "row-bytes":
		must(t, os.WriteFile(p.cur, corpusRowBytes, 0o600))
	case "other-bytes":
		must(t, os.WriteFile(p.cur, corpusOtherBytes, 0o600))
	case "directory":
		must(t, os.MkdirAll(filepath.Join(p.cur, "blocker"), 0o700))
	case "empty-directory":
		must(t, os.Mkdir(p.cur, 0o700))
	case "held-open":
		must(t, os.WriteFile(p.cur, corpusOtherBytes, 0o600))
		f, err := os.Open(p.cur)
		must(t, err)
		p.held = f
		t.Cleanup(func() { f.Close() })
	default:
		t.Fatalf("unknown cur state %q", c.Cur)
	}
	must(t, os.RemoveAll(p.old))
	switch c.Old {
	case "absent":
	case "row-bytes":
		must(t, os.WriteFile(p.old, corpusRowBytes, 0o600))
	case "other-bytes":
		must(t, os.WriteFile(p.old, corpusOtherBytes, 0o600))
	case "directory":
		must(t, os.MkdirAll(filepath.Join(p.old, "blocker"), 0o700))
	default:
		t.Fatalf("unknown old state %q", c.Old)
	}
	if c.DB == "down" {
		r.st.Close()
	}
	return p
}

// classify names what is at path: absent, directory, empty-directory,
// row-bytes or other-bytes.
func classify(t *testing.T, path string) string {
	t.Helper()
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "absent"
	}
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if fi.IsDir() {
		if es, _ := os.ReadDir(path); len(es) == 0 {
			return "empty-directory"
		}
		return "directory"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(b) == string(corpusRowBytes) {
		return "row-bytes"
	}
	return "other-bytes"
}

func (p *planted) open(db *store.Store) error {
	_, err := Open(Options{DB: db.DB(), DataDir: p.r.root, MaxUploadBytes: testCap, Now: p.r.clock.Now})
	return err
}

func (p *planted) check(t *testing.T, stage string, err error, want expectation) {
	t.Helper()
	if (err != nil) != want.OpenErr {
		t.Errorf("%s: Open error = %v, want an error: %v", stage, err, want.OpenErr)
	}
	if _, serr := os.Lstat(p.old); (serr == nil) != want.OldKept {
		t.Errorf("%s: set-aside file kept = %v, want %v", stage, serr == nil, want.OldKept)
	}
	if got := classify(t, p.cur); got != want.Cur {
		t.Errorf("%s: the file in place is %s, want %s", stage, got, want.Cur)
	}
}

func TestSweepRecoveryCorpus(t *testing.T) {
	c := loadCorpus(t)
	for _, row := range c.Rows {
		t.Run(row.Name, func(t *testing.T) {
			if row.WindowsOnly && runtime.GOOS != "windows" {
				t.Skip("this member only exists on Windows")
			}
			p := plantRow(t, row)
			beforeCur, beforeOld := classify(t, p.cur), classify(t, p.old)

			err := p.open(p.r.st)
			p.check(t, "first start", err, row.Want)

			// The invariant over every row: bytes the row describes survive.
			if row.Row == "present" && (beforeCur == "row-bytes" || beforeOld == "row-bytes") {
				if classify(t, p.cur) != "row-bytes" && classify(t, p.old) != "row-bytes" {
					t.Errorf("the bytes the row describes are gone (before: file in place %s, set aside %s)", beforeCur, beforeOld)
				}
			}

			if row.Heal == "" {
				return
			}
			// The fault goes away; the next start must finish the job.
			db := p.r.st
			switch row.Heal {
			case "remove-cur":
				must(t, os.RemoveAll(p.cur))
			case "release-hold":
				must(t, p.held.Close())
			case "reopen-db":
				st, err := store.Open(filepath.Join(p.r.root, "hband.db"))
				must(t, err)
				t.Cleanup(func() { st.Close() })
				db = st
			default:
				t.Fatalf("unknown heal %q", row.Heal)
			}
			p.check(t, "next start", p.open(db), *row.AfterHeal)
		})
	}
}
