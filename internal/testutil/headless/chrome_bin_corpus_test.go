package headless

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The corpus for the class "a CHROME_BIN spelling whose last text element is not
// the file the OS opens". Who produces its members: a person typing or pasting
// the path (a trailing separator-and-dot, a folder to go down and back up, a
// space or dot at the end, a quoted path, a relative path, any letter case) and
// the engine passing the value through. Every row is one spelling, and says what
// Browser must do with it:
//
//	edge    the OS would open Microsoft Edge: skip, with a message that says Edge
//	        and names CHROME_BIN, return no path (the quiet side of the rows below
//	        is "chrome", so a rule that refuses everything cannot pass)
//	chrome  the OS would open a Chrome: return the text exactly as written
//	skip    not a file: skip with a message that names CHROME_BIN, not Edge
//
// "windows_only" rows are spellings only Windows resolves to the file; the other
// rows run everywhere. Nothing is started: the files are empty.
//
// {dir} is replaced by a folder holding msedge.exe, chrome.exe,
// Edge/Application/chrome.exe and sub/; the test runs inside it, so relative
// rows mean those files.
type spellingRow struct {
	Name        string `json:"name"`
	Spelling    string `json:"spelling"`
	Want        string `json:"want"`
	WindowsOnly bool   `json:"windows_only"`
	Why         string `json:"why"`
}

func TestChromeBinSpellingsCorpus(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "chrome_bin_spellings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []spellingRow
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.Want]++
	}
	// A corpus with only one side would let a rule that refuses everything, or
	// nothing, pass.
	for _, want := range []string{"edge", "chrome", "skip"} {
		if counts[want] == 0 {
			t.Fatalf("the corpus has no %q rows", want)
		}
	}

	dir := t.TempDir()
	for _, f := range []string{"msedge.exe", "chrome.exe", filepath.Join("Edge", "Application", "chrome.exe")} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	for _, row := range rows {
		t.Run(row.Name, func(t *testing.T) {
			if row.WindowsOnly && runtime.GOOS != "windows" {
				t.Skip("only Windows resolves this spelling to the file")
			}
			spelling := strings.ReplaceAll(row.Spelling, "{dir}", dir)
			t.Setenv("CHROME_BIN", spelling)

			r := &recorder{TB: t}
			got := Browser(r)
			switch row.Want {
			case "edge":
				if !r.skipped || got != "" {
					t.Fatalf("CHROME_BIN=%q names Edge, yet Browser returned %q (skipped=%v)", spelling, got, r.skipped)
				}
				if !strings.Contains(r.msg, "CHROME_BIN") || !strings.Contains(r.msg, "Microsoft Edge") {
					t.Fatalf("CHROME_BIN=%q: the skip does not say it is Edge and name CHROME_BIN: %q", spelling, r.msg)
				}
			case "chrome":
				if r.skipped {
					t.Fatalf("CHROME_BIN=%q names a Chrome, yet Browser skipped: %s", spelling, r.msg)
				}
				if got != spelling {
					t.Fatalf("Browser = %q, want the CHROME_BIN text exactly: %q", got, spelling)
				}
			case "skip":
				if !r.skipped || got != "" {
					t.Fatalf("CHROME_BIN=%q is not a file, yet Browser returned %q (skipped=%v)", spelling, got, r.skipped)
				}
				if !strings.Contains(r.msg, "CHROME_BIN") || strings.Contains(r.msg, "Microsoft Edge") {
					t.Fatalf("CHROME_BIN=%q: the skip should name CHROME_BIN and not blame Edge: %q", spelling, r.msg)
				}
			default:
				t.Fatalf("unknown want %q", row.Want)
			}
		})
	}
}
