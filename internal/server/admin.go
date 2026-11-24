package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/sshubham2/hband/internal/auth"
	"github.com/sshubham2/hband/internal/comments"
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

		// Comments are keyed by their row id. The owner removes one here, at any
		// depth, from a reader page or from the report's admin page.
		{Method: "POST", Pattern: "/admin/comments/{n}/remove", Handler: a.adminCommentRemove},

		// Accounts are keyed by GitHub user id. The owner blocks one from a comment on
		// a report's admin page, reviews what a blocked account posted on the Blocked
		// users page, and unblocks it there. Neither POST has a field: the body is
		// never read, so no body can steer them.
		{Method: "POST", Pattern: "/admin/users/{u}/block", Handler: a.adminBlock},
		{Method: "POST", Pattern: "/admin/users/{u}/unblock", Handler: a.adminUnblock},
		{Method: "GET", Pattern: "/admin/blocked", Handler: a.adminBlocked},
	}
}

// maxRemoveBody caps the remove form, which carries at most one short field
// (from): 1 KiB is far over it.
const maxRemoveBody = 1 << 10

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
	// Moderation is every comment a reader can see under the report, at any depth,
	// in one flat list with a remove form on each and a Block form on each that the
	// author did not write. Drafts and unpublished reports list theirs. Removed text,
	// and the comments of blocked accounts, are not in it.
	Moderation []modItem
	// Blocked is every blocked account with what it posted, on the Blocked users
	// page only.
	Blocked []blockedView
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

// adminReport is the edit page: the like and dislike counts, every comment a reader can see under the report with a remove form on each
// and a Block form on each that is not the author's, the private
// feedback readers sent with the author's replies (marked when the sender is blocked) and a reply form on each
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
	// The comments are read the same way: through the one a.tree seam, and an error
	// page when they cannot be, never an empty list standing in for them.
	nodes, err := a.tree(r.Context(), rep.ID)
	if err != nil {
		slog.Error("admin: read the comments", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	d := a.adminPage(r, o, rep.Title+" - hband admin")
	d.Report = rep
	d.Tally = tally
	d.Feedback = inbox
	d.Moderation = moderationList(nodes, a.cfg.OwnerGitHubID)
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

// adminCommentRemove handles POST /admin/comments/{n}/remove: the owner removes
// one comment, at any depth, from the reader pages, the admin page or the Blocked
// users page. The only field is the optional from, and its only values are
// "admin" and "blocked".
//
// The order is adminFeedbackReply's, target-first: the id is checked and the
// comment looked up before the body is read, so a malformed or unknown id is the
// same 404 whatever the body is, and the owner's POST to the access matrix's
// placeholder id ("x") is a 404. The write is one statement keyed by the id, so
// it reaches a comment of a draft or unpublished report, and one that is already
// hidden by a block. Removing a removed comment is a success.
//
// It answers a 303 built from the stored slug and nothing from the request: to the
// Blocked users page when from=blocked (whatever the report's state: that page
// lists comments of drafts and unpublished reports too), to the admin page of the
// report when from=admin, or when the report has no reader page to land on (a
// draft or an unpublished one), and otherwise to the comments of its reader page.
// A reader-page form carries no field, so the owner's pages stay one set of bytes
// whichever page draws them. Any other from, or a repeated one, is a 400 before
// anything is written. The Owner proof comes from OwnerRoute; the handler asks
// nothing else about the viewer.
func (a *app) adminCommentRemove(w http.ResponseWriter, r *http.Request, o auth.Owner) {
	n, ok := canonicalID([]string{r.PathValue("n")})
	if !ok {
		http.NotFound(w, r)
		return
	}
	slug, published, err := a.comments.RemoveTarget(r.Context(), n)
	switch {
	case errors.Is(err, comments.ErrNoTarget):
		http.NotFound(w, r)
		return
	case err != nil:
		slog.Error("admin: look up the comment to remove", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRemoveBody)
	from, ok := removeFrom(r)
	if !ok {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	toAdmin, toBlocked := false, false
	switch {
	case len(from) == 0:
	case len(from) == 1 && from[0] == "admin":
		toAdmin = true
	case len(from) == 1 && from[0] == "blocked":
		toBlocked = true
	default:
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	err = a.comments.Remove(r.Context(), n)
	switch {
	case errors.Is(err, comments.ErrNoTarget):
		// The comment was deleted with its report since the lookup.
		http.NotFound(w, r)
	case err != nil:
		slog.Error("admin: remove the comment", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	case toBlocked:
		auth.SeeOther(w, "/admin/blocked")
	case toAdmin || !published:
		auth.SeeOther(w, "/admin/reports/"+slug+"#comments")
	default:
		auth.SeeOther(w, "/reports/"+slug+"#comments")
	}
}

// removeFrom reads the one optional field of the remove form, from, out of a body
// that r.Body has already capped, and reports whether the request was readable.
// Every field of the form is optional, so a body the handler did not read would
// look exactly like "no field", which is the valid reader-page case, and a removal
// has no undo. So no encoding may go unread:
//
//   - a form, urlencoded or multipart, is parsed, and only its own fields count (a
//     field in the query string is not the form, a repeated one is ambiguous, and a
//     file part is not a field this form has);
//   - whatever else is in the body is read to its end through the cap, so a body over
//     the cap is refused whatever its type;
//   - a body that is not a form is refused unless it is empty: an empty body that is
//     not a form is the plain form with no field; an empty multipart body is
//     malformed multipart, and a Content-Type that does not parse is refused like
//     any unreadable body.
func removeFrom(r *http.Request) (from []string, ok bool) {
	// ParseForm reads a urlencoded body and says so when it is malformed or over the cap;
	// ParseMultipartForm swallows that error for a body that is not multipart, so it is read
	// first and its error is not left to the one after it.
	if err := r.ParseForm(); err != nil {
		return nil, false
	}
	if err := r.ParseMultipartForm(maxRemoveBody); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		return nil, false
	}
	if r.MultipartForm != nil && len(r.MultipartForm.File) > 0 {
		return nil, false
	}
	rest, err := io.Copy(io.Discard, r.Body)
	if err != nil {
		return nil, false
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if rest > 0 && media != "application/x-www-form-urlencoded" && media != "multipart/form-data" {
		return nil, false
	}
	return r.PostForm["from"], true
}

// adminBlock handles POST /admin/users/{u}/block: the owner blocks one GitHub
// account. The account need not have signed in here: a block is a fact about an
// id.
//
// The route has no field, so the body is never read: whatever it is, it cannot
// steer the block, and nothing in a request but the path's id reaches the store.
// The id is checked first (a malformed one is a 404, which is also what the access
// matrix's placeholder id "x" gets). The author's own account is refused by the
// store (comments.ErrOwnerBlock), not here, so no caller can forget it; it is a
// 400. The answer is a 303 to that account's section of the Blocked users page,
// built from the parsed id alone. Blocking an account that is blocked already is
// the same success and keeps the first block time.
func (a *app) adminBlock(w http.ResponseWriter, r *http.Request, o auth.Owner) {
	u, ok := canonicalID([]string{r.PathValue("u")})
	if !ok {
		http.NotFound(w, r)
		return
	}
	err := a.comments.Block(r.Context(), u)
	switch {
	case errors.Is(err, comments.ErrOwnerBlock):
		http.Error(w, "The author cannot be blocked.", http.StatusBadRequest)
	case err != nil:
		slog.Error("admin: block the account", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	default:
		auth.SeeOther(w, "/admin/blocked#blocked-"+strconv.FormatInt(u, 10))
	}
}

// adminUnblock handles POST /admin/users/{u}/unblock: the owner lifts a block. It
// reads no body either, and answers a 303 to the Blocked users page. Unblocking an
// account that is not blocked is the same success. What it brings back is the
// account's comments and its right to take part; a comment removed while its
// writer was blocked stays removed.
func (a *app) adminUnblock(w http.ResponseWriter, r *http.Request, o auth.Owner) {
	u, ok := canonicalID([]string{r.PathValue("u")})
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := a.comments.Unblock(r.Context(), u); err != nil {
		slog.Error("admin: unblock the account", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	auth.SeeOther(w, "/admin/blocked")
}

// blockedView is one blocked account as the Blocked users page draws it.
type blockedView struct {
	GitHubID  int64
	Login     string
	AvatarURL string
	BlockedAt time.Time
	Comments  []blockedComment
	Feedback  []blockedFeedback
}

// blockedComment is one comment of a blocked account. A removed one has no text.
type blockedComment struct {
	Body    string
	Removed bool
	Posted  time.Time
	// ID is the comment's id, for its remove form.
	ID    int64
	Title string
	// ReportHref is the report's reader page, or its admin page when it has none.
	ReportHref string
	// Reply is set for a reply. ParentHref is where the comment it answered is
	// drawn, and is empty when that comment is not drawn anywhere (it is hidden with
	// nothing visible below it, or the report has no reader page).
	Reply      bool
	ParentHref string
}

// blockedFeedback is one private message a blocked account sent.
type blockedFeedback struct {
	Body  string
	When  time.Time
	Title string
	// Href is the message on its report's admin page, where the author answers it.
	Href string
}

// adminBlocked handles GET /admin/blocked: every blocked account with everything
// it posted, so the author can review it, remove what should stay hidden, and
// unblock. A read that fails is an error page, never an empty list standing in for
// it. Nothing is limited: like the feedback inbox and the moderation list, the page
// grows with what was posted.
//
// A reply's link to the comment it answered is built the way landing builds one:
// from the thread as readers see it (a.tree, one read per published report that
// has a reply on the page), through comments.Find and branchRoot, so a parent
// below the depth cap links to its branch page. A parent the thread does not draw
// has no link, and a report with no reader page links to its admin page.
func (a *app) adminBlocked(w http.ResponseWriter, r *http.Request, o auth.Owner) {
	accounts, err := a.comments.Blocked(r.Context())
	if err != nil {
		slog.Error("admin: read the blocked accounts", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	forests := map[int64][]*comments.Node{}
	view := make([]blockedView, 0, len(accounts))
	for _, acct := range accounts {
		v := blockedView{GitHubID: acct.GitHubID, Login: acct.Login, AvatarURL: acct.AvatarURL, BlockedAt: acct.BlockedAt}
		for _, c := range acct.Comments {
			bc := blockedComment{ID: c.ID, Body: c.Body, Removed: c.Removed, Posted: c.Posted, Title: c.Title,
				ReportHref: "/admin/reports/" + c.Slug, Reply: c.ParentID != 0}
			if c.Published {
				bc.ReportHref = "/reports/" + c.Slug
			}
			if bc.Reply && c.Published {
				forest, seen := forests[c.ReportID]
				if !seen {
					if forest, err = a.tree(r.Context(), c.ReportID); err != nil {
						slog.Error("admin: read the comments of a blocked account's report", "error", err)
						http.Error(w, "internal error", http.StatusInternalServerError)
						return
					}
					forests[c.ReportID] = forest
				}
				if path := comments.Find(forest, c.ParentID); path != nil {
					bc.ParentHref = "/reports/" + c.Slug
					if root := branchRoot(path); root != nil {
						bc.ParentHref += "/comments/" + strconv.FormatInt(root.ID, 10)
					}
					bc.ParentHref += "#comment-" + strconv.FormatInt(c.ParentID, 10)
				}
			}
			v.Comments = append(v.Comments, bc)
		}
		sent, err := a.feedback.OwnerInboxBySender(r.Context(), acct.GitHubID)
		if err != nil {
			slog.Error("admin: read a blocked account's feedback", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, m := range sent {
			v.Feedback = append(v.Feedback, blockedFeedback{Body: m.Body, When: m.When, Title: m.Title,
				Href: "/admin/reports/" + m.Slug + "#feedback-" + strconv.FormatInt(m.ID, 10)})
		}
		view = append(view, v)
	}
	d := a.adminPage(r, o, "Blocked users - hband admin")
	d.Blocked = view
	a.render(w, http.StatusOK, "admin_blocked.html", d)
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
