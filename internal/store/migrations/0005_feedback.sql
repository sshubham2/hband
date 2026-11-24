-- Private feedback from a signed-in reader to the report's author, and the
-- author's private replies to it. Times are unix seconds.
--
-- Feedback is its own table and is never a flag on comments: nothing that reads
-- the public thread can see a private message, and a separate AUTOINCREMENT
-- counter means the ids of public comments cannot reveal how much feedback
-- exists.
--
-- feedback.report_id and feedback.github_id carry the foreign-key clauses of
-- 0004_comments.sql word for word: the report clause cascades, so deleting a
-- report deletes its feedback in the same statement; the user clause has no
-- action, so a reader's row cannot be deleted while it owns feedback, and no
-- program path deletes one. (Measured under the pinned SQLite driver with these
-- clauses: a populated version 4 database migrates with an empty
-- foreign_key_check; replacing a report and re-running the sign-in upsert for
-- the same GitHub account keep every feedback row; deleting the report returns
-- no error and removes its feedback.)
--
-- feedback_replies are written only by the author's reply route. A reply hangs
-- off the message it answers and goes with it: deleting a report cascades
-- through feedback to its replies, two hops of ON DELETE CASCADE. There is no
-- column saying who wrote a reply, because every reply is the pinned owner's by
-- construction.
--
-- AUTOINCREMENT, so an id is never handed out twice: the reply route is keyed
-- by the id of the message, and a reused id would let a reply form left open in
-- an old tab land on another reader's message.
--
-- The body's length and character rules live in Go (internal/comments.Normalize,
-- shared with comments), not here, so changing them needs no table rebuild. The
-- column is called github_id so that a later join against blocked_accounts reads
-- the way comments' does.

CREATE TABLE feedback (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    report_id  INTEGER NOT NULL REFERENCES reports (id) ON DELETE CASCADE,
    github_id  INTEGER NOT NULL REFERENCES users (github_id),
    body       TEXT    NOT NULL CHECK (length(body) > 0),
    created_at INTEGER NOT NULL
) STRICT;

CREATE INDEX feedback_report ON feedback (report_id, id);

CREATE TABLE feedback_replies (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    feedback_id INTEGER NOT NULL REFERENCES feedback (id) ON DELETE CASCADE,
    body        TEXT    NOT NULL CHECK (length(body) > 0),
    created_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX feedback_replies_by_feedback ON feedback_replies (feedback_id, id);
