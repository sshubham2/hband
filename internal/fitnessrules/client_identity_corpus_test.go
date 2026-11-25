package fitnessrules

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// narrowRuleName is the rule that keeps every Go file but a _test.go file from
// naming a forwarded or client-identity header other than X-Forwarded-For.
const narrowRuleName = "no Go file names a forwarded header other than X-Forwarded-For"

type headerRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Producer string `json:"producer"`
	Expect   string `json:"expect"`
}

type headerCorpus struct {
	Purpose   string      `json:"purpose"`
	BoundedBy string      `json:"bounded_by"`
	Rows      []headerRow `json:"rows"`
}

// loadHeaderCorpus reads testdata/client_identity_headers.json and refuses a
// row that would test nothing: an unknown key, an empty field, a name that is
// not one bare header name (a quote, a space or a placeholder left in it), an
// expect other than fire, quiet or hole, a repeated id or a name repeated in the
// same letter case. It also refuses a corpus whose purpose or bounded_by is empty.
func loadHeaderCorpus(t *testing.T) headerCorpus {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "client_identity_headers.json"))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c headerCorpus
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("client_identity_headers.json: %v", err)
	}
	if c.Purpose == "" || c.BoundedBy == "" {
		t.Fatal("client_identity_headers.json: purpose and bounded_by must both be written")
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for _, r := range c.Rows {
		if r.ID == "" || r.Name == "" || r.Producer == "" {
			t.Fatalf("row %+v: id, name and producer must all be set", r)
		}
		if strings.ContainsAny(r.Name, "\"`' \t\r\n{}%$") {
			t.Fatalf("%s: name %q is not one bare header name", r.ID, r.Name)
		}
		switch r.Expect {
		case "fire", "quiet", "hole":
		default:
			t.Fatalf("%s: expect must be fire, quiet or hole, got %q", r.ID, r.Expect)
		}
		if ids[r.ID] {
			t.Fatalf("row id %q is used twice", r.ID)
		}
		ids[r.ID] = true
		if names[r.Name] {
			t.Fatalf("%s: name %q is listed twice", r.ID, r.Name)
		}
		names[r.Name] = true
	}
	return c
}

// TestClientIdentityHeaderCensus runs every name of the census through the
// narrow forwarded-header rule, in both quote styles and three contexts, at the
// resolver's own file and at an ordinary one. Every name that must fire is also
// run at a _test.go path, where the rule must stay quiet. It then fails if the
// census has lost a name it must hold or has fewer rows of a kind than it had.
func TestClientIdentityHeaderCensus(t *testing.T) {
	rules := loadRules(t, repoRoot(t))
	narrow, ok := rules[narrowRuleName]
	if !ok {
		t.Fatalf("fitness.yaml has no rule named %q", narrowRuleName)
	}
	c := loadHeaderCorpus(t)

	contexts := []string{
		"v := r.Header.Get(%s)",
		"var copied = []string{\"Accept\", %s, \"Origin\"}",
		"const clientHeader = %s",
	}
	quotes := []string{`"`, "`"}
	paths := []string{"internal/server/reader.go", "internal/server/clientaddr.go"}

	counts := map[string]int{}
	have := map[string]bool{}
	for _, r := range c.Rows {
		counts[r.Expect]++
		have[r.Name] = true
		for _, q := range quotes {
			for _, ctx := range contexts {
				text := strings.Replace(ctx, "%s", q+r.Name+q, 1)
				for _, p := range paths {
					fired, skipped := narrow.fires(p, text)
					if len(skipped) > 0 {
						t.Fatalf("a pattern of the rule does not compile in Go's regexp: %v", skipped)
					}
					switch r.Expect {
					case "fire":
						if !fired {
							t.Errorf("%s [%s]: the rule stays quiet on %s at %s", r.ID, r.Producer, text, p)
						}
					case "quiet":
						if fired {
							t.Errorf("%s [%s]: the rule fires on %s at %s", r.ID, r.Producer, text, p)
						}
					case "hole":
						if fired {
							t.Errorf("%s [%s]: the pinned hole now fires on %s at %s: flip the row to fire", r.ID, r.Producer, text, p)
						}
					}
				}
				if r.Expect == "fire" {
					if fired, _ := narrow.fires("internal/server/opens_test.go", text); fired {
						t.Errorf("%s: a _test.go file may send any header, and the rule fires on %s", r.ID, text)
					}
				}
			}
		}
	}

	// A trimmed census proves less than it did.
	for _, name := range []string{"X-Forwarded", "Cf-Pseudo-IPv4", "CF-Connecting-IPv6", "X-Azure-SocketIP", "X-Forwarded-For"} {
		if !have[name] {
			t.Errorf("the census no longer holds %s", name)
		}
	}
	if counts["fire"] < 30 || counts["quiet"] < 10 || counts["hole"] < 5 {
		t.Errorf("the census holds %d fire, %d quiet and %d hole rows; it has been trimmed", counts["fire"], counts["quiet"], counts["hole"])
	}
}
