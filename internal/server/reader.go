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
	"github.com/sshubham2/hband/internal/votes"
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
	// Votes is the like count and the viewer's own vote for the report shown.
	Votes votes.PublicView
	// Thread is the report's comments, pruned for readers and ready to draw.
	Thread []commentView
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
	if state == stateFound {
		// The like count and the viewer's own vote. Only a PublicView reaches a
		// reader page: it has no dislike count to show, to anyone. A count that
		// cannot be read is never replaced by a zero: the page says it cannot be
		// shown.
		pub, err := a.votes.Public(r.Context(), rep.ID, v.GitHubID)
		if err != nil {
			slog.Error("reader: read the votes", "error", err)
			state, rep = stateUnavailable, reports.Report{}
		} else {
			d.Votes = pub
		}
	}
	if state == stateFound {
		// The thread under the report, the same on both pages: nothing in it
		// depends on which page asked. A thread that cannot be read is never
		// drawn as an empty one: the page says it cannot be shown.
		views, _, err := a.threadOf(r.Context(), rep, signedIn, 0)
		if err != nil {
			slog.Error("reader: read the comments", "error", err)
			state, rep = stateUnavailable, reports.Report{}
		} else {
			d.Thread = views
		}
	}
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
	a.writeReader(w, status, "report.html", d)
}

// threadOf is the only place a reader page's thread is read and shaped: the
// forest through a.tree, the owner's id from the configuration and the depth
// cap are supplied here once, for the report pages and the branch page alike.
// branch 0 draws the whole thread. Any other id draws that one comment as the
// root of the page, with what lies below it, and found is false when the pruned
// forest has no such comment (it never existed, belongs to another report, or has
// nothing visible in it).
func (a *app) threadOf(ctx context.Context, rep reports.Report, signedIn bool, branch int64) (views []commentView, found bool, err error) {
	nodes, err := a.tree(ctx, rep.ID)
	if err != nil {
		return nil, false, err
	}
	if branch != 0 {
		path := comments.Find(nodes, branch)
		if path == nil {
			return nil, false, nil
		}
		// The comment itself, not the top-level comment above it: the page begins
		// where the "continue this thread" link was.
		nodes = path[len(path)-1:]
	}
	return buildThread(nodes, rep.Slug, signedIn, a.cfg.OwnerGitHubID, threadDepthCap), true, nil
}

// writeReader sends a reader page. Every reader page is personalised (who is
// signed in, the owner's Admin link, a reply form) and changes whenever the
// author publishes or a reader writes: a shared cache may not keep it, and a
// private one must revalidate.
func (a *app) writeReader(w http.ResponseWriter, status int, page string, d readerData) {
	w.Header().Set("Cache-Control", "private, no-cache")
	a.render(w, status, page, d)
}

// readBranch is one branch of a deep thread on a page of its own:
// GET /reports/<slug>/comments/<id>, where a "continue this thread" link goes. It
// is answered like the report page it belongs to. A draft, an unpublished, a
// deleted and an unknown slug, an id that is not a canonical positive number, and
// an id that is not in the visible thread (unknown, on another report, or pruned)
// all end in the same http.NotFound. A lookup that fails is a 503 page, the one
// the report page draws, never a 404: an outage is not an absence.
func (a *app) readBranch(w http.ResponseWriter, r *http.Request) {
	rep, err := a.reports.PublishedBySlug(r.Context(), r.PathValue("slug"))
	switch {
	case errors.Is(err, reports.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		slog.Error("reader: look up a report", "error", err)
		a.renderReader(w, r, reportPage, reports.Report{}, stateUnavailable)
		return
	}
	id, ok := canonicalID([]string{r.PathValue("id")})
	if !ok {
		http.NotFound(w, r)
		return
	}
	v, signedIn := auth.ViewerFrom(r.Context())
	views, found, err := a.threadOf(r.Context(), rep, signedIn, id)
	switch {
	case err != nil:
		slog.Error("reader: read the comments", "error", err)
		a.renderReader(w, r, reportPage, reports.Report{}, stateUnavailable)
		return
	case !found:
		http.NotFound(w, r)
		return
	}
	d := readerData{
		pageData:  pageData{Title: rep.Title + " - hband", SignedIn: signedIn, Viewer: v, Next: "/reports/" + rep.Slug + "/comments/" + strconv.FormatInt(id, 10)},
		Report:    rep,
		HasReport: true,
		Thread:    views,
	}
	a.writeReader(w, http.StatusOK, "branch.html", d)
}
