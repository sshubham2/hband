package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/sshubham2/hband/internal/auth"
	"github.com/sshubham2/hband/internal/reports"
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

// adminReport is the edit page: the sandboxed preview, the publish state, the
// replace form with the slug filled in, and delete.
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
	d := a.adminPage(r, o, rep.Title+" - hband admin")
	d.Report = rep
	d.Form = uploadForm{Title: rep.Title, Slug: rep.Slug, Status: rep.Status}
	a.render(w, http.StatusOK, "admin_report.html", d)
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
