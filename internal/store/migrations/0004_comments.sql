-- Comments, and the accounts the author has blocked. Times are unix seconds.
--
-- A comment belongs to one report and may answer one comment of the same
-- report, to any depth. The parent is a composite key: the pair (report_id,
-- parent_id) must name a row of this table, which UNIQUE (report_id, id) makes
-- possible. A NULL parent_id is a top-level comment and is not checked. So a
-- reply can never attach to a comment of another report, or to no comment at
-- all, whoever writes the row. (Measured under the pinned SQLite driver: a
-- cross-report or missing parent is refused with FOREIGN KEY constraint failed,
-- code 787, and a NULL parent inserts.)
--
-- The self-reference carries no action clause, which is NO ACTION: deleting a
-- comment that still has replies fails, so a comment is never removed that way;
-- it is hidden by setting removed_at, and its replies keep their place.
-- Deleting a report cascades through report_id and removes every comment of the
-- report in one statement, whatever the depth (measured at 1,500 and 10,000
-- levels). A self-referencing ON DELETE CASCADE is not used: SQLite stops a
-- cascade through parent links past its trigger depth of 1000.
-- If NO ACTION ever has to change, the one fallback is DEFERRABLE INITIALLY
-- DEFERRED on that key. It was measured too, and passes the same deletes, but
-- inside an explicit transaction it accepts a cross-report row until COMMIT, so
-- the refusal tests would have to move to the commit. Deleting the deepest
-- comments first is ruled out: internal/reports may not name this table (the
-- fitness rule that only internal/comments writes SQL on it).
--
-- github_id references users with no action clause either: a reader's row
-- cannot be deleted while it owns comments, and no program path deletes one.
-- The body's length and character rules live in Go (internal/comments), not
-- here, so changing them needs no table rebuild.
--
-- AUTOINCREMENT, so an id is never handed out twice and a parent's id is always
-- smaller than the id of its reply.

CREATE TABLE comments (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    report_id  INTEGER NOT NULL REFERENCES reports (id) ON DELETE CASCADE,
    parent_id  INTEGER,
    github_id  INTEGER NOT NULL REFERENCES users (github_id),
    body       TEXT    NOT NULL CHECK (length(body) > 0),
    created_at INTEGER NOT NULL,
    removed_at INTEGER,
    UNIQUE (report_id, id),
    FOREIGN KEY (report_id, parent_id) REFERENCES comments (report_id, id)
) STRICT;

CREATE INDEX comments_children ON comments (report_id, parent_id);

-- A block is a fact about a GitHub account, not about a row of users: the
-- account id outlives any users row, so there is deliberately no foreign key
-- here. (With NO ACTION a blocked reader's row could never be deleted; with
-- CASCADE deleting it would quietly unblock the account at its next sign-in.)
-- A row means the account is blocked and deleting it unblocks. Nothing derived
-- is stored: the comments of a blocked account are hidden at read time.

CREATE TABLE blocked_accounts (
    github_id  INTEGER PRIMARY KEY CHECK (github_id > 0),
    blocked_at INTEGER NOT NULL
) STRICT;
