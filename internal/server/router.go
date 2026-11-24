// Package server builds the HTTP handler: security headers, cross-origin
// protection, identity, and the routes.
package server

import (
	"bytes"
	"context"
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/sshubham2/hband/internal/auth"
	"github.com/sshubham2/hband/internal/comments"
	"github.com/sshubham2/hband/internal/config"
	"github.com/sshubham2/hband/internal/feedback"
	"github.com/sshubham2/hband/internal/reports"
	"github.com/sshubham2/hband/internal/store"
	"github.com/sshubham2/hband/internal/votes"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// siteCSP is the policy of every site page. It differs from the policy on
// report bytes, which is set only where those bytes are served.
const siteCSP = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' https://avatars.githubusercontent.com; frame-src 'self'; " +
	"object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

type app struct {
	cfg     config.Config
	store   *store.Store
	reports *reports.Library
	auth    *auth.Auth
	votes   *votes.Book
	// comments is the public comment tree, on the same pool as votes.
	comments *comments.Book
	// feedback is the private messages readers send the author, on the same pool.
	feedback *feedback.Book
	// tree reads a report's pruned comment forest. Every page and handler that
	// needs the thread reads it here and nowhere else (renderReader, readBranch
	// and the redirect after a comment), and it is a field only so that a test can
	// make that one read fail while every other lookup works.
	tree  func(ctx context.Context, reportID int64) ([]*comments.Node, error)
	pages *template.Template
}

// New builds the whole handler. The chain from outside in: security headers,
// cross-origin protection (trusted origin = the base URL's origin), identity,
// then the mux. Scheme and host come only from cfg; no handler reads them
// from the request. lib is the opened report library (it has already swept
// the data directory).
func New(cfg config.Config, st *store.Store, lib *reports.Library, clock func() time.Time) http.Handler {
	return newApp(cfg, st, lib, clock).handler()
}

// newApp builds the app New serves. It is split from handler so that a test can
// keep the *app and change a seam (tree) before it serves a request.
func newApp(cfg config.Config, st *store.Store, lib *reports.Library, clock func() time.Time) *app {
	a := &app{
		cfg:      cfg,
		store:    st,
		reports:  lib,
		auth:     auth.New(cfg, st, clock),
		votes:    votes.New(st.DB(), clock),
		comments: comments.New(st.DB(), clock),
		feedback: feedback.New(st.DB(), clock),
		pages:    template.Must(template.ParseFS(templateFS, "templates/*.html")),
	}
	a.tree = a.comments.Tree
	return a
}

// handler is the whole chain around the routes, as New describes it.
func (a *app) handler() http.Handler {
	cfg := a.cfg
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.home)
	// The share URL, and the isolated path its iframe loads. Neither is under
	// /admin. Identify still runs in front of /raw as on every route, but
	// readRaw never reads the viewer it resolves.
	mux.HandleFunc("GET /reports/{slug}", a.readReport)
	// One branch of a deep thread on a page of its own: where a "continue this
	// thread" link goes, and where a reply deeper than the report page draws is
	// answered with a 303.
	mux.HandleFunc("GET /reports/{slug}/comments/{id}", a.readBranch)
	// The public write a reader uses to vote: a signed-in reader sets their
	// vote. It is behind cross-origin protection and Identify like every route.
	// The comment and feedback writes below and POST /auth/logout, registered by
	// a.auth.Routes below, are the other public POSTs.
	mux.HandleFunc("POST /reports/{slug}/vote", a.vote)
	// The public write a signed-in reader (the owner included) uses to comment
	// or reply, behind the same cross-origin protection and Identify.
	mux.HandleFunc("POST /reports/{slug}/comments", a.postComment)
	// The third public reader POST: a signed-in reader (the owner included) sends
	// the author a private message, which only the owner's admin page lists.
	mux.HandleFunc("POST /reports/{slug}/feedback", a.postFeedback)
	mux.HandleFunc("GET /raw/{slug}", a.readRaw)
	mux.HandleFunc("GET /healthz", a.healthz)
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embedded above
	}
	// The site's own script (thread.js) is served from here under script-src
	// 'self'; report bytes never are (they come from /raw with a policy that
	// forbids every script).
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticSub)))
	a.auth.Routes(mux)

	// Admin handlers exist only as adminRoutes rows, each wrapped in
	// OwnerRoute, and the table is mounted behind the gate exactly twice:
	// the exact /admin (the landing page answers 200 itself, with no
	// redirect) and the /admin/ subtree.
	adminMux := http.NewServeMux()
	for _, rt := range adminRoutes(a) {
		adminMux.Handle(rt.Method+" "+rt.Pattern, auth.OwnerRoute(rt.Handler))
	}
	mux.Handle("/admin", auth.RequireOwner(adminMux))
	mux.Handle("/admin/", auth.RequireOwner(adminMux))

	cop := http.NewCrossOriginProtection()
	if err := cop.AddTrustedOrigin(cfg.Origin); err != nil {
		slog.Error("cross-origin protection: trusted origin refused", "error", err)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "misconfigured", http.StatusInternalServerError)
		})
	}
	return securityHeaders(cop.Handler(a.auth.Identify(mux)))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", siteCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// render executes a page into a buffer first, so a template error becomes a
// 500 instead of a half-written page.
func (a *app) render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := a.pages.ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("render page", "page", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func (a *app) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	w.Header().Set("Cache-Control", "no-store")
	if err := a.store.Ping(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("ok"))
}
