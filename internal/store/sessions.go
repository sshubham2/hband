package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Session is a live session joined to its user.
type Session struct {
	User      User
	ExpiresAt time.Time
}

// LookupSession finds the session stored under tokenHash. Only rows with
// expires_at > now count; ok is false for an unknown or expired session.
func (s *Store) LookupSession(ctx context.Context, tokenHash string, now time.Time) (sess Session, ok bool, err error) {
	var exp int64
	err = s.db.QueryRowContext(ctx, `
SELECT s.github_id, u.login, u.avatar_url, s.expires_at
FROM sessions s JOIN users u ON u.github_id = s.github_id
WHERE s.token_hash = ? AND s.expires_at > ?`, tokenHash, unix(now)).
		Scan(&sess.User.GitHubID, &sess.User.Login, &sess.User.AvatarURL, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("look up session: %w", err)
	}
	sess.ExpiresAt = time.Unix(exp, 0)
	return sess, true, nil
}

// DeleteSession removes the session stored under tokenHash. Deleting a
// session that does not exist is not an error.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}
