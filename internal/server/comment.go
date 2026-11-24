package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/sshubham2/hband/internal/auth"
	"github.com/sshubham2/hband/internal/comments"
	"github.com/sshubham2/hband/internal/reports"
)

// maxCommentBody caps the form a comment arrives in: a body of MaxRunes runes,
// four UTF-8 bytes each, every byte percent-encoded as three, plus the parent
// field, is 60 KB and a little; 64 KiB covers it.
const maxCommentBody = 64 << 10

// blockedRefusal is the answer of every public write (comment, reply, feedback,
// vote, withdrawal) to a blocked account: a 403 with this text and nothing stored.
const blockedRefusal = "Your account cannot take part here."

// postComment handles POST /reports/{slug}/comments. The form carries body, and
// parent (the id of the comment being answered) for a reply. The owner posts
// through the same form as any reader.
//
// The order is the vote handler's. The published report is looked up before
// anything is said about the visitor, so a draft, an unpublished, a deleted and
// an unknown slug answer the same 404 to everybody. A visitor who is not signed
// in is sent back to the report and nothing is parsed or written. Every answer
// that is not an error is a 303 to a page of the report (the report page, or the
// branch page that draws a deep reply), built from the stored slug and ids and
// never from the request.
func (a *app) postComment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	viewer, who := auth.Participant(r.Context())
	if errors.Is(who, auth.ErrIdentityUnavailable) {
		slog.Error("comment: identify the writer", "error", who)
		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
		return
	}

	rep, err := a.reports.PublishedBySlug(r.Context(), r.PathValue("slug"))
	switch {
	case errors.Is(err, reports.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		slog.Error("comment: look up the report", "error", err)
		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
		return
	}

	// A visitor who is not signed in is sent back to the report, where the page
	// says to sign in. Nothing is read from the request or written. (A form POST
	// answered by a redirect to the identity provider would be blocked by the
	// site's form-action policy, so the sign-in is not started from here.)
	if errors.Is(who, auth.ErrSignedOut) {
		auth.SeeOther(w, "/reports/"+rep.Slug+"#comments")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxCommentBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	// Only the body of the request counts: r.Form would also accept a value in
	// the query string, and a repeated field is ambiguous, not "the first one".
	bodies := r.PostForm["body"]
	if len(bodies) != 1 {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	var parent int64
	if values, present := r.PostForm["parent"]; present {
		var ok bool
		if parent, ok = canonicalID(values); !ok {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
	}

	id, err := a.comments.Post(r.Context(), rep.ID, viewer.GitHubID, parent, bodies[0])
	switch {
	case errors.Is(err, comments.ErrBadBody):
		http.Error(w, "Bad request", http.StatusBadRequest)
	case errors.Is(err, comments.ErrBlocked):
		// The writer's account is blocked: nothing was stored.
		http.Error(w, blockedRefusal, http.StatusForbidden)
	case errors.Is(err, comments.ErrNoTarget):
		// The report was unpublished or deleted since the lookup, or the parent
		// is missing, on another report, removed or written by a blocked account:
		// one answer, so a reply is no oracle for comments the reader cannot see.
		http.NotFound(w, r)
	case err != nil:
		slog.Error("comment: write", "error", err)
		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
	default:
		auth.SeeOther(w, a.landing(r.Context(), rep, id))
	}
}

// landing is where a reader is sent after a comment of theirs was stored: the
// page that draws it, at its own anchor. The report page draws the first
// threadDepthCap levels of a thread, and a branch page (branchRoot) every level
// below. The comment is looked up in the thread as readers see it, so the page
// is chosen by what the pages draw and never by a depth the writer reported.
//
// The write has already happened, so nothing here may turn it into an error: a
// thread that cannot be read sends the reader to the comments of the report,
// where the comment is drawn when the read works again. A comment that is not in
// the thread at all (its writer was blocked after the write: stored, never
// drawn) keeps the anchor on the report page, where there is nothing to scroll
// to. A writer who is blocked when it posts never gets here: Post refuses it.
func (a *app) landing(ctx context.Context, rep reports.Report, id int64) string {
	anchor := "#comment-" + strconv.FormatInt(id, 10)
	nodes, err := a.tree(ctx, rep.ID)
	if err != nil {
		slog.Error("comment: read the thread after a write", "error", err)
		return "/reports/" + rep.Slug + "#comments"
	}
	if root := branchRoot(comments.Find(nodes, id)); root != nil {
		return "/reports/" + rep.Slug + "/comments/" + strconv.FormatInt(root.ID, 10) + anchor
	}
	return "/reports/" + rep.Slug + anchor
}

// canonicalID reads the one value of a comment id field: a positive decimal
// number with no sign and no leading zero. strconv.ParseInt alone also takes
// "+5" and "007", which would be a second spelling of the same id.
func canonicalID(values []string) (int64, bool) {
	if len(values) != 1 {
		return 0, false
	}
	n, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != values[0] {
		return 0, false
	}
	return n, true
}
