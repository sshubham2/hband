-- Votes. Times are unix seconds. One row is one reader's current vote on one
-- report: the primary key makes "one vote per reader per report" a property of
-- the schema, a changed vote is an update of the row, and a withdrawn vote is
-- no row at all.
--
-- Keyed by reports.id, which never changes under a replace, an unpublish or a
-- republish (so votes stay with their report) and is never handed out twice
-- (so a vote never attaches to another one). Deleting a report, or a reader's
-- account, removes the votes with it.
--
-- Only this table is created: nothing here alters users, sessions or reports.

CREATE TABLE votes (
    report_id  INTEGER NOT NULL REFERENCES reports (id) ON DELETE CASCADE,
    github_id  INTEGER NOT NULL REFERENCES users (github_id) ON DELETE CASCADE,
    kind       TEXT    NOT NULL CHECK (kind IN ('up', 'down')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (report_id, github_id)
) STRICT;
