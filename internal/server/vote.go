package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/sshubham2/hband/internal/auth"
	"github.com/sshubham2/hband/internal/reports"
	"github.com/sshubham2/hband/internal/votes"
)

// maxVoteBody caps the form a vote arrives in: one field of a few bytes.
const maxVoteBody = 1 << 10

// vote handles POST /reports/{slug}/vote. The form carries exactly one
// vote field, the reader's wanted state: up, down or none. Sending the same
// state again changes nothing, so a double click, a retried request and a
// back-button resubmit all end where the first one did.
//
// The order matters. The published report is looked up before anything is
// said about the visitor, so a draft, an unpublished, a deleted and an
// unknown slug answer the same 404 to everybody. Every answer that is not an
// error is the same 303 to the report, built from the stored slug and never
// from the request, so the response says nothing about what was cast.
func (a *app) vote(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	viewer, who := auth.Participant(r.Context())
	if errors.Is(who, auth.ErrIdentityUnavailable) {
		slog.Error("vote: identify the voter", "error", who)
		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
		return
	}

	rep, err := a.reports.PublishedBySlug(r.Context(), r.PathValue("slug"))
	switch {
	case errors.Is(err, reports.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		slog.Error("vote: look up the report", "error", err)
		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
		return
	}
	back := "/reports/" + rep.Slug + "#votes"

	// A visitor who is not signed in is sent back to the report, where the
	// header's sign-in anchor returns them to it. Nothing is written. (A form
	// POST answered by a redirect to the identity provider would be blocked by
	// the site's form-action policy, so the sign-in is not started from here.)
	if errors.Is(who, auth.ErrSignedOut) {
		auth.SeeOther(w, back)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxVoteBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	// Only the body counts: r.Form would also accept a value in the query
	// string, and a repeated field is ambiguous, not "the first one".
	values := r.PostForm["vote"]
	if len(values) != 1 {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	switch values[0] {
	case "up":
		err = a.votes.Cast(r.Context(), rep.ID, viewer.GitHubID, votes.Up)
	case "down":
		err = a.votes.Cast(r.Context(), rep.ID, viewer.GitHubID, votes.Down)
	case "none":
		err = a.votes.Withdraw(r.Context(), rep.ID, viewer.GitHubID)
	default:
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	switch {
	case errors.Is(err, votes.ErrNotOpen):
		// Unpublished or deleted between the lookup and the write.
		http.NotFound(w, r)
	case err != nil:
		slog.Error("vote: write", "error", err)
		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
	default:
		auth.SeeOther(w, back)
	}
}
