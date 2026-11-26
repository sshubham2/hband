package store

import (
	"testing"
	"time"
)

// User reads the account sign-in last saw: the login and the avatar the report
// page's byline shows, as the newest sign-in left them, and a plain "no" for an
// account that has never signed in.
func TestUserReadsWhatSignInLastSaw(t *testing.T) {
	s, _ := openTemp(t)
	now := time.Unix(1_800_000_000, 0)

	if u, ok, err := s.User(ctx, 7); err != nil || ok || u != (User{}) {
		t.Fatalf("User of an account that never signed in = %+v, %v, %v; want the zero User, false, nil", u, ok, err)
	}

	first := SignInArgs{User: User{GitHubID: 7, Login: "seven", AvatarURL: "https://avatars.example/u/7"}, NewTokenHash: "h1", ExpiresAt: now.Add(time.Hour), Now: now}
	if err := s.SignIn(ctx, first); err != nil {
		t.Fatal(err)
	}
	u, ok, err := s.User(ctx, 7)
	if err != nil || !ok || u != first.User {
		t.Fatalf("User(7) = %+v, %v, %v; want %+v, true, nil", u, ok, err, first.User)
	}

	// A later sign-in under a new login and avatar is what the next read returns.
	second := SignInArgs{User: User{GitHubID: 7, Login: "seven-renamed", AvatarURL: "https://avatars.example/u/7?v=2"}, NewTokenHash: "h2", ExpiresAt: now.Add(2 * time.Hour), Now: now.Add(time.Minute)}
	if err := s.SignIn(ctx, second); err != nil {
		t.Fatal(err)
	}
	if u, ok, err := s.User(ctx, 7); err != nil || !ok || u != second.User {
		t.Fatalf("User(7) after a second sign-in = %+v, %v, %v; want %+v, true, nil", u, ok, err, second.User)
	}
	if _, ok, _ := s.User(ctx, 8); ok {
		t.Fatal("User(8) found an account nobody signed in as")
	}
}
