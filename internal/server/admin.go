package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/sshubham2/hband/internal/auth"
	"github.com/sshubham2/hband/internal/feedback"
	"github.com/sshubham2/hband/internal/reports"
	"github.com/sshubham2/hband/internal/votes"
)

// adminRoute is one owner-only page or action. Handlers are registered only
// from this table, each wrapped in auth.OwnerRoute, and the table is mounted
// only behind auth.RequireOwner. Tests enumerate it, so a new row joins the
// access matrix with no test edits.
type adminRoute struct {
	Method  string
	Pattern string
	Handler func(http.ResponseWriter, *http.Request, auth.Owner)
}

func adminRoutes(a *app) []adminRoute {
	return []adminRoute{
		// The landing page answers at the exact /admin and at /admin/.
		{Method: "GET", Pattern: "/admin", Handler: a.adminHome},
		{Method: "GET", Pattern: "/admin/{$}", Handler: a.adminHome},

		// Reports are keyed by slug. POST /admin/reports saves by slug: a new
		// slug creates a report, an existing one replaces it.
		{Method: "POST", Pattern: "/admin/reports", Handler: a.adminSave},
		{Method: "GET", Pattern: "/admin/reports/{slug}", Handler: a.adminReport},
		{Method: "GET", Pattern: "/admin/reports/{slug}/raw", Handler: a.adminRaw},
		{Method: "POST", Pattern: "/admin/reports/{slug}/publish", Handler: a.adminPublish},
		{Method: "POST", Pattern: "/admin/reports/{slug}/unpublish", Handler: a.adminUnpublish},
		{Method: "POST", Pattern: "/admin/reports/{slug}/delete", Handler: a.adminDelete},

		// Feedback is keyed by the message's row id. The owner answers a message
		// here; the reader it came from is the only other person who is shown it.
		{Method: "POST", Pattern: "/admin/feedback/{n}/reply", Handler: a.adminFeedbackReply},
	}
}

// uploadForm is what the upload form is filled with.
type uploadForm struct {
	Title, Slug, Status string
}

// adminData feeds the admin templates.
type adminData struct {
	pageData
	// Error is a refusal to show the owner, in plain words.
	Error   string
	Form    uploadForm
	Reports []reports.Report
	Report  reports.Report
	// Tally is the report's likes and dislikes, shown on its admin page only.
	Tally votes.Tally
	// Feedback is the private messages readers sent about the report, newest
	// first, each with its sender and the author's replies. It is shown on this
	// page only.
	Feedback []feedback.Message
}

func (a *app) adminPage(r *http.Request, o auth.Owner, title string) adminData {
	d := adminData{
		pageData: pageData{Title: title, SignedIn: true, Viewer: auth.Viewer{GitHubID: o.GitHubID, Login: o.Login, IsOwner: true}},
		Form:     uploadForm{Status: reports.StatusPublished},
	}
	if v, ok := auth.ViewerFrom(r.Context()); ok {
		d.Viewer.AvatarURL = v.AvatarURL
	}
	return d
}

func (a *app) adminHome(w http.ResponseWriter, r *http.Request, o auth.Owner) {
	a.renderLanding(w, r, o, http.StatusOK, "")
}

// renderLanding draws the report list and the upload form, with a message
// when an action was refused.
func (a *app) renderLanding(w http.ResponseWriter, r *http.Request, o auth.Owner, status int, msg string) {
	list, err := a.reports.ListAll(r.Context())
	if err != nil {
		slog.Error("admin: list reports", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	d := a.adminPage(r, o, "Admin - hband")
	d.Reports, d.Error = list, msg
	a.render(w, status, "admin.html", d)
}

// adminSave handles the upload form. Every refusal says why and leaves the
// stored reports exactly as they were.
func (a *app) adminSave(w http.ResponseWriter, r *http.Request, o auth.Owner) {
	rep, err := a.reports.SaveUpload(w, r)
	var refusal *reports.Refusal
	switch {
	case errors.As(err, &refusal):
		a.renderLanding(w, r, o, refusal.Status, refusal.Message)
	case errors.Is(err, reports.ErrBusy):
		a.renderLanding(w, r, o, http.StatusConflict, "That report is being read right now, so it could not be replaced. Nothing changed; try again in a moment.")
	case err != nil:
		slog.Error("admin: save report", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	default:
		auth.SeeOther(w, "/admin/reports/"+rep.Slug)
	}
}

// adminReport is the edit page: the like and dislike counts, the private
// feedback readers sent with the author's replies and a reply form on each
// message, the sandboxed preview, the publish state, the replace
// form with the slug filled in, and delete.
func (a *app) adminReport(w http.ResponseWriter, r *http.Request, o auth.Owner) {
	rep, err := a.reports.BySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, reports.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("admin: look up report", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	tally, err := a.votes.OwnerTally(r.Context(), rep.ID)
	if err != nil {
		slog.Error("admin: read the votes", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The messages are read the way the tally is: a list that cannot be read is
	// an error page, never an empty list standing in for it. Drafts and
	// unpublished reports still list theirs.
	inbox, err := a.feedback.OwnerInbox(r.Context(), rep.ID)
	if err != nil {
		slog.Error("admin: read the feedback", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	d := a.adminPage(r, o, rep.Title+" - hband admin")
	d.Report = rep
	d.Tally = tally
	d.Feedback = inbox
	d.Form = uploadForm{Title: rep.Title, Slug: rep.Slug, Status: rep.Status}
	a.render(w, http.StatusOK, "admin_report.html", d)
}

// adminFeedbackReply handles POST /admin/feedback/{n}/reply: the owner answers
// one feedback message. The form carries exactly one field, body.
//
// The order is the comment handler's, target-first: the message id is checked
// and the message looked up before the body is read, so a malformed or unknown
// id is the same 404 whatever the body is, and the owner's POST to the access
// matrix's placeholder id ("x") is a 404. A reply is stored whatever the
// report's publish state; the sender is shown it when the report is published.
// The answer is a 303 back to that message on its report's admin page, built from
// the stored slug and the parsed id, never from the request.
func (a *app) adminFeedbackReply(w http.ResponseWriter, r *http.Request, o auth.Owner) {
	n, ok := canonicalID([]string{r.PathValue("n")})
	if !ok {
		http.NotFound(w, r)
		return
	}
	slug, err := a.feedback.OwnerReplyTarget(r.Context(), n)
	switch {
	case errors.Is(err, feedback.ErrNoTarget):
		http.NotFound(w, r)
		return
	case err != nil:
		slog.Error("admin: look up the feedback to answer", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxCommentBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	// Only the body of the request counts, and exactly one value of it: a field in
	// the query string is not the form, and a repeated one is ambiguous.
	bodies := r.PostForm["body"]
	if len(bodies) != 1 {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	err = a.feedback.OwnerReply(r.Context(), n, bodies[0])
	switch {
	case errors.Is(err, feedback.ErrBadBody):
		http.Error(w, "Bad request", http.StatusBadRequest)
	case errors.Is(err, feedback.ErrNoTarget):
		// The message was deleted with its report since the lookup.
		http.NotFound(w, r)
	case err != nil:
		slog.Error("admin: write the reply", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	default:
		auth.SeeOther(w, "/admin/reports/"+slug+"#feedback-"+strconv.FormatInt(n, 10))
	}
}

// adminRaw is the owner's preview, drafts included. It stays behind the gate
// and is never cached.
func (a *app) adminRaw(w http.ResponseWriter, r *http.Request, o auth.Owner) {
	rep, f, err := a.reports.OpenRaw(r.Context(), r.PathValue("slug"), false)
	if errors.Is(err, reports.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("admin: open report file", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	a.serveRaw(w, r, rep, f, "no-store")
}

func (a *app) adminPublish(w http.ResponseWriter, r *http.Request, o auth.Owner) {
	a.adminAction(w, r, a.reports.Publish, true)
}

func (a *app) adminUnpublish(w http.ResponseWriter, r *http.Request, o auth.Owner) {
	a.adminAction(w, r, a.reports.Unpublish, true)
}

// adminDelete removes the row and the file, then returns to the list.
func (a *app) adminDelete(w http.ResponseWriter, r *http.Request, o auth.Owner) {
	a.adminAction(w, r, a.reports.Delete, false)
}

// adminAction runs one lifecycle action on the slug in the path and sends the
// owner back to the report (or, once it is gone, to the list).
func (a *app) adminAction(w http.ResponseWriter, r *http.Request, act func(ctx context.Context, slug string) error, backToReport bool) {
	slug := r.PathValue("slug")
	err := act(r.Context(), slug)
	switch {
	case errors.Is(err, reports.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		slog.Error("admin: report action", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	case backToReport:
		auth.SeeOther(w, "/admin/reports/"+slug)
	default:
		auth.SeeOther(w, "/admin")
	}
}
