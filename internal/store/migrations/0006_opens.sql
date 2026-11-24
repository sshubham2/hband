-- Opens. One row is one visitor opening one report on one UTC day: the whole
-- row is the primary key, so "once per visitor per day" is a property of the
-- schema, and a report's times opened is the number of its rows. There is no
-- counter column to drift from the keys.
--
-- day is whole UTC days since 1970-01-01. visitor is the first 16 bytes of an
-- HMAC-SHA256 over the report, the day and the client address (an IPv4 address,
-- or an IPv6 /64), keyed by a random key that internal/opens draws at start and
-- never stores, so nothing in the database, a backup or the configuration can
-- turn a stored value back into an address.
--
-- Keyed by reports.id like votes (0003_votes.sql): the id never changes under a
-- replace, an unpublish or a republish, so a report keeps its opens through all
-- three, and deleting a report deletes its opens with it.
--
-- There is no backfill: opens made before this migration are not counted.
--
-- Only this table is created: nothing here alters users, sessions or reports.

CREATE TABLE report_opens (
    report_id INTEGER NOT NULL REFERENCES reports (id) ON DELETE CASCADE,
    day       INTEGER NOT NULL CHECK (day >= 0),
    visitor   BLOB    NOT NULL CHECK (length(visitor) = 16),
    PRIMARY KEY (report_id, day, visitor)
) STRICT, WITHOUT ROWID;
