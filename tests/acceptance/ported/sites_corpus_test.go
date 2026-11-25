package ported

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// siteRow is one spelling of an assertion site, or of something that is not one, in
// the Python the retired checks were written in: Sites must find exactly Sites lines.
type siteRow struct {
	Name  string `json:"name"`
	Src   string `json:"src"`
	Sites *[]int `json:"sites"` // the lines Sites must report; [] for a quiet row
	Why   string `json:"why"`   // who writes this spelling
}

// loadSiteSpellings reads testdata/site_spellings.json strictly: an unknown key, an
// empty corpus, a row with no source, no why or no sites, a duplicate name, a site
// past the end of its source, or an unexpanded placeholder is refused.
func loadSiteSpellings(t *testing.T) []siteRow {
	t.Helper()
	data, err := os.ReadFile("testdata/site_spellings.json")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("{{")) || bytes.Contains(data, []byte("${")) {
		t.Fatal("testdata/site_spellings.json holds an unexpanded placeholder")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var doc struct {
		Rows []siteRow `json:"rows"`
	}
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("testdata/site_spellings.json: %v", err)
	}
	if len(doc.Rows) == 0 {
		t.Fatal("testdata/site_spellings.json has no rows")
	}
	seen := map[string]bool{}
	for i, r := range doc.Rows {
		if r.Name == "" || seen[r.Name] || r.Src == "" || r.Why == "" || r.Sites == nil {
			t.Fatalf("row %d (%q): a row needs a unique name, a src, a why and its sites", i, r.Name)
		}
		lines := strings.Count(Canonical([]byte(r.Src)), "\n")
		for _, l := range *r.Sites {
			if l < 1 || l > lines {
				t.Fatalf("row %q: site %d is not a line of its %d-line source", r.Name, l, lines)
			}
		}
		seen[r.Name] = true
	}
	return doc.Rows
}

// Sites finds every spelling of an assertion site the corpus lists, and none of the
// spellings it pins quiet.
func TestSitesSeeEverySpelling(t *testing.T) {
	for _, r := range loadSiteSpellings(t) {
		t.Run(r.Name, func(t *testing.T) {
			got, want := fmt.Sprint(Sites(Canonical([]byte(r.Src)))), fmt.Sprint(*r.Sites)
			if got != want {
				t.Errorf("Sites = %s, want %s (%s) in:\n%s", got, want, r.Why, r.Src)
			}
		})
	}
}
