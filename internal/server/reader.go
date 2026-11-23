package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/sshubham2/hband/internal/auth"
	"github.com/sshubham2/hband/internal/reports"
)

// pageData feeds every site template.
type pageData struct {
	Title    string
	SignedIn bool
	Viewer   auth.Viewer
	// Next is the local path a signed-out reader is sent back to after signing
	// in; empty means the home page.
	Next string
}

// SignInNext is the value after ?next= in the header's sign-in anchor.
func (p pageData) SignInNext() string {
	if p.Next == "" {
		return "/"
	}
	return p.Next
}

// readerPage says which of the two reader pages is being drawn. Both are one
// template and one code path; the page only decides where sign-in returns to.
type readerPage int

const (
	homePage readerPage = iota
	reportPage
)

// readerState is what the lookup behind a reader page found.
type readerState int

const (
	// stateFound: a published report is shown.
	stateFound readerState = iota
	// stateEmpty: nothing is published yet (home page only).
	stateEmpty
	// stateUnavailable: the library could not answer, so the page says so and
	// shows no report. It is never confused with an empty library.
	stateUnavailable
)

// readerData feeds report.html.
type readerData struct {
	pageData
	Report      reports.Report
	HasReport   bool
	Unavailable bool
}

// home is the public landing page: the newest published report, laid out
// exactly as /reports/<slug> lays it out.
func (a *app) home(w http.ResponseWriter, r *http.Request) {
	rep, err := a.reports.Latest(r.Context())
	switch {
	case err == nil:
		a.renderReader(w, r, homePage, rep, stateFound)
	case errors.Is(err, reports.ErrNotFound):
		a.renderReader(w, r, homePage, reports.Report{}, stateEmpty)
	default:
		slog.Error("reader: look up the latest report", "error", err)
		a.renderReader(w, r, homePage, reports.Report{}, stateUnavailable)
	}
}

// readReport is the stable share URL. A draft, an unpublished, a deleted and
// an unknown slug all end in the same http.NotFound, written before any other
// header, so the page is no oracle for what exists.
func (a *app) readReport(w http.ResponseWriter, r *http.Request) {
	rep, err := a.reports.PublishedBySlug(r.Context(), r.PathValue("slug"))
	switch {
	case err == nil:
		a.renderReader(w, r, reportPage, rep, stateFound)
	case errors.Is(err, reports.ErrNotFound):
		http.NotFound(w, r)
	default:
		slog.Error("reader: look up a report", "error", err)
		a.renderReader(w, r, reportPage, reports.Report{}, stateUnavailable)
	}
}

// renderReader is the only path either reader page takes. It derives
// everything a page is made of, so the two handlers cannot compose it
// differently: the status from the state, the title, where sign-in returns to
// (from the stored slug, never from the request) and who is looking. A viewer
// whose session could not be resolved (the store is down) is anonymous.
func (a *app) renderReader(w http.ResponseWriter, r *http.Request, page readerPage, rep reports.Report, state readerState) {
	v, signedIn := auth.ViewerFrom(r.Context())
	d := readerData{pageData: pageData{Title: "hband", SignedIn: signedIn, Viewer: v}}
	status := http.StatusOK
	switch state {
	case stateFound:
		d.HasReport = true
		d.Report = rep
		d.Title = rep.Title + " - hband"
		if page == reportPage {
			d.Next = "/reports/" + rep.Slug
		}
	case stateUnavailable:
		d.Unavailable = true
		status = http.StatusServiceUnavailable
	}
	// The page is personalised (who is signed in, the owner's Admin link) and
	// changes whenever the author publishes: a shared cache may not keep it,
	// and a private one must revalidate.
	w.Header().Set("Cache-Control", "private, no-cache")
	a.render(w, status, "report.html", d)
}
