package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The program run returns names, as its Fake, the stand-in GitHub that program
// actually signs in through, so a caller's p.Fake (SignIn, Approve, TokenCalls)
// talks to the same stand-in the program redirects to.
func TestRunReturnsTheStandInItsProgramUses(t *testing.T) {
	p := run(t, testPort(t), io.Discard)
	start := p.Anonymous().Get(t, "/auth/github/login")
	if start.Status < 300 || start.Status >= 400 {
		t.Fatalf("GET /auth/github/login answered %d, want a redirect", start.Status)
	}
	if !strings.HasPrefix(start.Location, p.Fake.URL()+"/login/oauth/authorize") {
		t.Fatalf("the serving program sends sign-in to %q, but the Program run returned names its stand-in as %s", start.Location, p.Fake.URL())
	}
	_ = http.StatusOK
}
