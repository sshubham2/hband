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
	"github.com/sshubham2/hband/internal/config"
	"github.com/sshubham2/hband/internal/reports"
	"github.com/sshubham2/hband/internal/store"
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
	pages   *template.Template
}

// New builds the whole handler. The chain from outside in: security headers,
// cross-origin protection (trusted origin = the base URL's origin), identity,
// then the mux. Scheme and host come only from cfg; no handler reads them
// from the request. lib is the opened report library (it has already swept
// the data directory).
func New(cfg config.Config, st *store.Store, lib *reports.Library, clock func() time.Time) http.Handler {
	a := &app{
		cfg:     cfg,
		store:   st,
		reports: lib,
		auth:    auth.New(cfg, st, clock),
		pages:   template.Must(template.ParseFS(templateFS, "templates/*.html")),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.home)
	// The share URL, and the isolated path its iframe loads. Neither is under
	// /admin, and /raw reads no identity.
	mux.HandleFunc("GET /reports/{slug}", a.readReport)
	mux.HandleFunc("GET /raw/{slug}", a.readRaw)
	mux.HandleFunc("GET /healthz", a.healthz)
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embedded above
	}
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
