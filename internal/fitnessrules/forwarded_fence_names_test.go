package fitnessrules

import "testing"

// TestForwardedFenceCatchesBareAndVersionedNames: X-Forwarded, Cf-Pseudo-IPv4 and
// CF-Connecting-IPv6 are client-identity headers that real-IP libraries read
// (request-ip, ipware). The rule that no Go file names a forwarded header other
// than X-Forwarded-For must fire on each of them outside test files.
func TestForwardedFenceCatchesBareAndVersionedNames(t *testing.T) {
	rules := loadRules(t, repoRoot(t))
	narrow, ok := rules["no Go file names a forwarded header other than X-Forwarded-For"]
	if !ok {
		t.Fatal("fitness.yaml has no rule named 'no Go file names a forwarded header other than X-Forwarded-For'")
	}
	for _, name := range []string{"X-Forwarded", "Cf-Pseudo-IPv4", "CF-Connecting-IPv6"} {
		for _, quoted := range []string{`"` + name + `"`, "`" + name + "`"} {
			text := "v := r.Header.Get(" + quoted + ")"
			if fired, _ := narrow.fires("internal/server/reader.go", text); !fired {
				t.Errorf("%s names a client-identity header and the rule stays quiet", text)
			}
		}
	}
	if fired, _ := narrow.fires("internal/server/clientaddr.go", `lines := r.Header.Values("X-Forwarded-For")`); fired {
		t.Error("X-Forwarded-For inside clientaddr.go must stay quiet")
	}
}
