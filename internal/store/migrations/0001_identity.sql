-- Identity and sessions. Times are unix seconds.
-- Who may reach the admin pages is decided from the pinned GitHub user ID in
-- the process configuration, never from anything stored here.

CREATE TABLE users (
    github_id  INTEGER PRIMARY KEY CHECK (github_id > 0),
    login      TEXT    NOT NULL,
    avatar_url TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE sessions (
    token_hash TEXT    PRIMARY KEY,
    github_id  INTEGER NOT NULL REFERENCES users (github_id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
) STRICT;

CREATE INDEX sessions_expires_at ON sessions (expires_at);
