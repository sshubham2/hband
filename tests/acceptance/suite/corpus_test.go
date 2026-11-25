package suite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// corpusRow plants go test -json events into a healthy run of one mode and says
// whether the judge of that mode must refuse the run (fire) or accept it (quiet).
type corpusRow struct {
	Name   string   `json:"name"`
	Mode   string   `json:"mode"`   // "without" (CHROME_BIN unset) or "chrome"
	Expect string   `json:"expect"` // "fire" or "quiet"
	Want   string   `json:"want"`   // fire: text one of the judge's problems must hold
	Allow  []string `json:"allow"`  // without: test names added to mayAlsoSkip for this row
	Why    string   `json:"why"`    // who produces this shape
	Plant  []event  `json:"plant"`
}

// loadCorpus reads testdata/skip_events.json strictly: an unknown key, an empty
// corpus, a row with no plant, an unknown mode or expectation, a fire row with no
// Want, a quiet row with one, a duplicate name or an unexpanded placeholder is refused.
func loadCorpus(t *testing.T) []corpusRow {
	t.Helper()
	data, err := os.ReadFile("testdata/skip_events.json")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("{{")) || bytes.Contains(data, []byte("${")) {
		t.Fatal("testdata/skip_events.json holds an unexpanded placeholder")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var doc struct {
		Rows []corpusRow `json:"rows"`
	}
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("testdata/skip_events.json: %v", err)
	}
	if len(doc.Rows) == 0 {
		t.Fatal("testdata/skip_events.json has no rows")
	}
	seen := map[string]bool{}
	for i, r := range doc.Rows {
		switch {
		case r.Name == "" || seen[r.Name]:
			t.Fatalf("row %d: no name, or the name %q is used twice", i, r.Name)
		case r.Mode != "without" && r.Mode != "chrome":
			t.Fatalf("row %q: mode %q is neither without nor chrome", r.Name, r.Mode)
		case r.Expect != "fire" && r.Expect != "quiet":
			t.Fatalf("row %q: expect %q is neither fire nor quiet", r.Name, r.Expect)
		case (r.Expect == "fire") != (r.Want != ""):
			t.Fatalf("row %q: a fire row needs a want and a quiet row has none", r.Name)
		case r.Why == "" || len(r.Plant) == 0:
			t.Fatalf("row %q: no why, or nothing planted", r.Name)
		case len(r.Allow) > 0 && r.Mode != "without":
			t.Fatalf("row %q: allow applies only to the without mode", r.Name)
		}
		for _, e := range r.Plant {
			if e.Package == "" || e.Test == "" || !contains([]string{"output", "pass", "fail", "skip"}, e.Action) {
				t.Fatalf("row %q: the planted event %+v needs a Package, a Test and an Action of output, pass, fail or skip", r.Name, e)
			}
		}
		seen[r.Name] = true
	}
	return doc.Rows
}

// healthy is a run the judge of mode accepts: sixteen acceptance tests pass, and the
// browser tests skip saying CHROME_BIN is not set (without) or pass (chrome).
func healthy(mode string) []event {
	var ev []event
	for i := 0; i < 16; i++ {
		ev = append(ev, event{Action: "pass", Package: "example/acceptance", Test: fmt.Sprintf("TestE00%dHealthy%02d", i%6+1, i)})
	}
	for _, n := range browserTests {
		if mode == "without" {
			ev = append(ev,
				event{Action: "output", Package: "example/browser", Test: n, Output: "    x_test.go:9: CHROME_BIN is not set: set it to the path of a Chrome executable (not Edge) to run this check\n"},
				event{Action: "skip", Package: "example/browser", Test: n})
		} else {
			ev = append(ev, event{Action: "pass", Package: "example/browser", Test: n})
		}
	}
	return append(ev, event{Action: "pass", Package: "example/other", Test: "TestOther"})
}

func stream(t *testing.T, events []event) []byte {
	var b bytes.Buffer
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func judge(mode string, r result, allow []string) []string {
	if mode == "chrome" {
		p, _ := judgeChrome(r)
		return p
	}
	merged := map[string]string{}
	for k, v := range mayAlsoSkip {
		merged[k] = v
	}
	for _, n := range allow {
		merged[n] = "allowed by the corpus row"
	}
	p, _ := judgeWithout(r, merged)
	return p
}

// The whole-suite judges read every verdict go test reports, a subtest's as much as
// a top-level test's: each row of testdata/skip_events.json plants events into a
// healthy run and says whether the judge must refuse it.
func TestSuiteChecksReadEverySkip(t *testing.T) {
	for _, mode := range []string{"without", "chrome"} {
		if p := judge(mode, parse(stream(t, healthy(mode))), nil); len(p) != 0 {
			t.Fatalf("the healthy %s run is refused: %v", mode, p)
		}
	}
	for _, row := range loadCorpus(t) {
		t.Run(row.Name, func(t *testing.T) {
			got := judge(row.Mode, parse(stream(t, append(healthy(row.Mode), row.Plant...))), row.Allow)
			if row.Expect == "quiet" {
				if len(got) != 0 {
					t.Errorf("refused a run it must accept (%s): %v", row.Why, got)
				}
				return
			}
			for _, p := range got {
				if strings.Contains(p, row.Want) {
					return
				}
			}
			t.Errorf("accepted a run it must refuse with %q (%s); its problems: %v", row.Want, row.Why, got)
		})
	}
}
