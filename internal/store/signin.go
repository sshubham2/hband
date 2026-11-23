package store

import (
	"context"
	"fmt"
	"time"
)

// User is a GitHub account as sign-in last saw it.
type User struct {
	GitHubID  int64
	Login     string
	AvatarURL string
}

// SignInArgs is everything one sign-in writes.
type SignInArgs struct {
	User User
	// OldTokenHash is the session the browser arrived with, if any. It is
	// deleted so that a sign-in never keeps a pre-existing session alive.
	OldTokenHash string
	NewTokenHash string
	ExpiresAt    time.Time
	Now          time.Time
}

// SignIn records a sign-in in ONE transaction: it upserts the user, deletes
// expired sessions, deletes the incoming session and inserts the new one. It
// returns nil, or an error with nothing written.
func (s *Store) SignIn(ctx context.Context, a SignInArgs) error {
	if a.User.GitHubID <= 0 || a.NewTokenHash == "" {
		return fmt.Errorf("sign in: refusing an incomplete record")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sign in: %w", err)
	}
	defer tx.Rollback()

	now := unix(a.Now)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO users (github_id, login, avatar_url, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (github_id) DO UPDATE SET
    login = excluded.login,
    avatar_url = excluded.avatar_url,
    updated_at = excluded.updated_at`,
		a.User.GitHubID, a.User.Login, a.User.AvatarURL, now, now); err != nil {
		return fmt.Errorf("sign in: save user: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now); err != nil {
		return fmt.Errorf("sign in: sweep expired sessions: %w", err)
	}
	if a.OldTokenHash != "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, a.OldTokenHash); err != nil {
			return fmt.Errorf("sign in: drop incoming session: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO sessions (token_hash, github_id, created_at, expires_at)
VALUES (?, ?, ?, ?)`,
		a.NewTokenHash, a.User.GitHubID, now, unix(a.ExpiresAt)); err != nil {
		return fmt.Errorf("sign in: save session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sign in: commit: %w", err)
	}
	return nil
}
