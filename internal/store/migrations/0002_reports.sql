-- Reports. Times are unix seconds. A report is one self-contained HTML file
-- kept at <data>/reports/<id>.html; this row is its identity.
--
-- AUTOINCREMENT, so an id is never handed out twice, even after the newest
-- report is deleted: later votes and comments reference reports.id and must
-- never attach to a different report.
-- published_at is set the first time a report is published and is never moved
-- by a replace, an unpublish or a republish.

CREATE TABLE reports (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    slug         TEXT    NOT NULL UNIQUE,
    title        TEXT    NOT NULL,
    status       TEXT    NOT NULL CHECK (status IN ('draft', 'published')),
    published_at INTEGER NULL,
    size_bytes   INTEGER NOT NULL,
    sha256       TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;

CREATE INDEX reports_status_published ON reports (status, published_at, id);
