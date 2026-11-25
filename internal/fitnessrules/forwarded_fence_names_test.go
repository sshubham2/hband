package fitnessrules

import "testing"

// TestForwardedFenceCatchesBareAndVersionedNames: X-Forwarded, Cf-Pseudo-IPv4 and
// CF-Connecting-IPv6 are client-identity headers: request-ip and django-ipware
// read X-Forwarded, request-ip reads Cf-Pseudo-IPv4, and Cloudflare adds both
// Cloudflare names. The rule that no Go file names a forwarded header other
// than X-Forwarded-For must fire on each of them, in either quote style, at an
// ordinary non-test file (internal/server/reader.go), while X-Forwarded-For
// stays quiet at internal/server/clientaddr.go.
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
