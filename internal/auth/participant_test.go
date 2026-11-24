package auth

import (
	"context"
	"errors"
	"testing"
)

// Participant answers all three outcomes of "who is this" from one call: a
// signed-in viewer, an anonymous visitor, and a lookup that failed.
func TestParticipant(t *testing.T) {
	bg := context.Background()
	cause := errors.New("database is closed")

	v := Viewer{GitHubID: 7, Login: "reader"}
	got, err := Participant(context.WithValue(bg, viewerKey{}, v))
	if err != nil || got != v {
		t.Errorf("signed in: %+v, %v", got, err)
	}

	got, err = Participant(bg)
	if !errors.Is(err, ErrSignedOut) || errors.Is(err, ErrIdentityUnavailable) || got != (Viewer{}) {
		t.Errorf("anonymous: %+v, %v", got, err)
	}

	got, err = Participant(context.WithValue(bg, identifyErrKey{}, cause))
	if !errors.Is(err, ErrIdentityUnavailable) || !errors.Is(err, cause) || errors.Is(err, ErrSignedOut) || got != (Viewer{}) {
		t.Errorf("lookup failed: %+v, %v", got, err)
	}

	// A failed lookup attaches no viewer, but if both were somehow present the
	// outage wins: nobody may act on an identity that could not be confirmed.
	both := context.WithValue(context.WithValue(bg, viewerKey{}, v), identifyErrKey{}, cause)
	if _, err := Participant(both); !errors.Is(err, ErrIdentityUnavailable) {
		t.Errorf("viewer and a failed lookup together: %v", err)
	}
}
