package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/sshubham2/hband/internal/auth"
	"github.com/sshubham2/hband/internal/feedback"
	"github.com/sshubham2/hband/internal/reports"
)

// postFeedback handles POST /reports/{slug}/feedback: a signed-in reader (the
// owner included) sends the author a private message. The form carries exactly
// one field, body.
//
// The order is the comment handler's. The published report is looked up before
// anything is said about the visitor, so a draft, an unpublished, a deleted and
// an unknown slug answer the same 404 to everybody. A visitor who is not signed
// in is sent back to the report and nothing is parsed or written. Every answer
// that is not an error is a 303 to the report, built from the stored slug and a
// fixed fragment, never from the request and never from the stored message: no
// row id reaches the sender, because ids are shared with every other reader's
// messages. A successful send lands on #feedback-sent, which the page uses to
// show "Sent to the author."; a visitor who is not signed in lands on #feedback.
func (a *app) postFeedback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	viewer, who := auth.Participant(r.Context())
	if errors.Is(who, auth.ErrIdentityUnavailable) {
		slog.Error("feedback: identify the sender", "error", who)
		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
		return
	}

	rep, err := a.reports.PublishedBySlug(r.Context(), r.PathValue("slug"))
	switch {
	case errors.Is(err, reports.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		slog.Error("feedback: look up the report", "error", err)
		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
		return
	}

	// A visitor who is not signed in is sent back to the report, where the page
	// says to sign in. Nothing is read from the request or written. (A form POST
	// answered by a redirect to the identity provider would be blocked by the
	// site's form-action policy, so the sign-in is not started from here.)
	if errors.Is(who, auth.ErrSignedOut) {
		auth.SeeOther(w, "/reports/"+rep.Slug+"#feedback")
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

	err = a.feedback.Send(r.Context(), rep.ID, viewer.GitHubID, bodies[0])
	switch {
	case errors.Is(err, feedback.ErrBadBody):
		http.Error(w, "Bad request", http.StatusBadRequest)
	case errors.Is(err, feedback.ErrBlocked):
		// The sender's account is blocked: nothing was stored.
		http.Error(w, blockedRefusal, http.StatusForbidden)
	case errors.Is(err, feedback.ErrNoTarget):
		// The report was unpublished or deleted since the lookup: the same answer
		// as for a report that was never there.
		http.NotFound(w, r)
	case err != nil:
		slog.Error("feedback: write", "error", err)
		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
	default:
		auth.SeeOther(w, "/reports/"+rep.Slug+"#feedback-sent")
	}
}
