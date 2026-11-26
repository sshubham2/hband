package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/sshubham2/hband/internal/auth"
	"github.com/sshubham2/hband/internal/comments"
	"github.com/sshubham2/hband/internal/feedback"
	"github.com/sshubham2/hband/internal/opens"
	"github.com/sshubham2/hband/internal/reports"
	"github.com/sshubham2/hband/internal/store"
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

// ThreadScript says whether the page loads the collapse script: only a page
// that draws a thread does, which readerData answers for itself. The shell's
// page-start reads it on every page.
func (p pageData) ThreadScript() bool { return false }

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
	// CommentCount is how many comments a reader sees under the report, at every
	// depth (comments.Visible): a removed comment is not counted, and the replies
	// beneath it are. The branch page draws only part of the thread and shows none.
	CommentCount int
	// OwnFeedback is the viewer's own private messages on this report with the
	// author's replies, oldest first. It is set only for a signed-in viewer who is
	// not the owner, read for that viewer's own id, so it is every other viewer's
	// zero value. The type has no row id to print.
	OwnFeedback []feedback.Sent
	// Author is the owner's account as sign-in last saw it, for the byline under
	// the title: login and avatar. It is filled only for a found report, and it is
	// the zero value when the owner has never signed in, when no owner is
	// configured, or when the read failed: the byline then starts at the date.
	Author store.User
}

// ThreadScript is true where a report is drawn (the found home and report pages
// and the branch page, which sets HasReport), and false on the empty and
// unavailable pages, which draw no thread.
func (d readerData) ThreadScript() bool { return d.HasReport }

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
		tv := threadViewer{SignedIn: signedIn, Owner: signedIn && v.IsOwner}
		views, visible, _, err := a.threadOf(r.Context(), rep, tv, 0)
		if err != nil {
			slog.Error("reader: read the comments", "error", err)
			state, rep = stateUnavailable, reports.Report{}
		} else {
			d.Thread, d.CommentCount = views, visible
		}
	}
	if state == stateFound && signedIn && !v.IsOwner {
		// The viewer's own messages and the author's replies to them, under the
		// feedback box. The key is the session's GitHub id and nothing from the
		// request, so it is the same on both pages and no reader is shown another's.
		// The owner reads feedback on the admin page, so the owner's pages are not
		// asked. A thread that cannot be read is never drawn as an empty one: the
		// page says it cannot be shown.
		own, err := a.feedback.SenderThreads(r.Context(), rep.ID, v.GitHubID)
		if err != nil {
			slog.Error("reader: read the viewer's own feedback", "error", err)
			state, rep = stateUnavailable, reports.Report{}
		} else {
			d.OwnFeedback = own
		}
	}
	if state == stateFound && a.cfg.OwnerGitHubID > 0 {
		// The author's name and avatar for the byline. Nothing else on the page
		// depends on it, so a read that fails is logged and the byline starts at the
		// date: the report is never made unavailable for it.
		if u, ok, err := a.store.User(r.Context(), a.cfg.OwnerGitHubID); err != nil {
			slog.Warn("reader: read the author", "error", err)
		} else if ok {
			d.Author = u
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
		// Every read the page is made of has answered, so this is a 200 for a found
		// report: the one place an open is counted.
		a.countOpen(r, rep.ID, v, signedIn)
	case stateUnavailable:
		d.Unavailable = true
		status = http.StatusServiceUnavailable
	}
	a.writeReader(w, status, "report.html", d)
}

// countOpen counts one open of the report for the visitor who asked for the page
// renderReader is about to answer 200. Only that answer counts: / (the newest
// report, drawn in full) and /reports/<slug>. HEAD, /raw/<slug> (the frame's own
// request), the branch page, the show-all page, admin pages, 404s and 503s never
// reach it or return before the write. Nothing is counted for:
//   - a request that is not a GET;
//   - the signed-in owner, whose own visits would be most of a small site's count;
//   - an obvious bot (opens.ObviousBot);
//   - a request whose client cannot be told (clientResolver.addr).
//
// The write is bounded at 2 s, and the count is a side effect of reading, not the
// reason for it: a write that fails is logged (without the address) and the page
// goes out as it would have.
func (a *app) countOpen(r *http.Request, reportID int64, v auth.Viewer, signedIn bool) {
	if r.Method != http.MethodGet || (signedIn && v.IsOwner) || opens.ObviousBot(r.UserAgent()) {
		return
	}
	addr, ok := a.client.addr(r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), openWriteLimit)
	defer cancel()
	if err := a.recordOpen(ctx, reportID, addr); err != nil {
		slog.Warn("reader: count an open", "error", err)
	}
}

// openWriteLimit bounds the write that counts an open.
const openWriteLimit = 2 * time.Second

// showRow is one report on the show-all page.
type showRow struct {
	Slug        string
	Title       string
	PublishedAt time.Time
	// HasDate is false only for a published report whose publish date is unset.
	HasDate bool
	Likes   int
	// Comments is the whole-thread visible count, the number the report page prints.
	Comments int
	Views    int
}

// showData feeds all_reports.html.
type showData struct {
	pageData
	Rows        []showRow
	Unavailable bool
}

// showAll is the public list of every published report, newest first, each with
// its likes, its visible comments and its times opened (views). Anyone may read
// it, and the rows are the same for an anonymous visitor, a reader and the owner:
// no dislike count is read, and no open is counted for the page itself. The
// numbers come from four whole-table reads, each in the package that owns its
// table. They are four snapshots, not one. If any of the four fails the page says
// it cannot be shown, with no rows and a 503: a count that could not be read is
// never drawn as a zero.
func (a *app) showAll(w http.ResponseWriter, r *http.Request) {
	v, signedIn := auth.ViewerFrom(r.Context())
	d := showData{pageData: pageData{Title: "All reports - hband", SignedIn: signedIn, Viewer: v, Next: "/reports"}}
	status := http.StatusOK
	rows, err := a.showRows(r.Context())
	if err != nil {
		slog.Error("reader: read the show-all page", "error", err)
		d.Unavailable = true
		status = http.StatusServiceUnavailable
	} else {
		d.Rows = rows
	}
	a.writeReader(w, status, "all_reports.html", d)
}

// showRows reads the four tables the show-all page is made of. A report missing
// from a count's map has none of that count, which is a true zero: the read
// itself answered.
func (a *app) showRows(ctx context.Context) ([]showRow, error) {
	list, err := a.reports.ListPublished(ctx)
	if err != nil {
		return nil, err
	}
	likes, err := a.votes.PublicLikes(ctx)
	if err != nil {
		return nil, err
	}
	visible, err := a.comments.VisibleCounts(ctx)
	if err != nil {
		return nil, err
	}
	views, err := a.opens.Counts(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]showRow, 0, len(list))
	for _, rep := range list {
		rows = append(rows, showRow{
			Slug: rep.Slug, Title: rep.Title,
			PublishedAt: rep.PublishedAt, HasDate: rep.HasPublishDate(),
			Likes: likes[rep.ID], Comments: visible[rep.ID], Views: views[rep.ID],
		})
	}
	return rows, nil
}

// threadOf is the only place a reader page's thread is read and shaped: the
// forest through a.tree, the owner's id from the configuration and the depth
// cap are supplied here once, for the report pages and the branch page alike.
// branch 0 draws the whole thread. Any other id draws that one comment as the
// root of the page, with what lies below it, and found is false when the pruned
// forest has no such comment (it never existed, belongs to another report, or has
// nothing visible in it).
//
// visible is the public comment count of the whole report, taken from the whole
// forest before a branch narrows it, so the count and the thread come from one
// read and cannot disagree. A caller that draws only a branch does not show it.
func (a *app) threadOf(ctx context.Context, rep reports.Report, tv threadViewer, branch int64) (views []commentView, visible int, found bool, err error) {
	nodes, err := a.tree(ctx, rep.ID)
	if err != nil {
		return nil, 0, false, err
	}
	visible = comments.Visible(nodes)
	if branch != 0 {
		path := comments.Find(nodes, branch)
		if path == nil {
			return nil, visible, false, nil
		}
		// The comment itself, not the top-level comment above it: the page begins
		// where the "continue this thread" link was.
		nodes = path[len(path)-1:]
	}
	return buildThread(nodes, rep.Slug, tv, a.cfg.OwnerGitHubID, threadDepthCap), visible, true, nil
}

// writeReader sends a reader page. Every reader page is personalised (who is
// signed in, the owner's Admin link, a reply form) and changes whenever the
// author publishes or a reader writes: a shared cache may not keep it, and a
// private one must revalidate.
func (a *app) writeReader(w http.ResponseWriter, status int, page string, d any) {
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
	tv := threadViewer{SignedIn: signedIn, Owner: signedIn && v.IsOwner}
	views, _, found, err := a.threadOf(r.Context(), rep, tv, id)
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
