package server

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/sshubham2/hband/internal/reports"
)

// rawCSP is the policy on report bytes. The report is sandboxed with no
// allow-scripts and no allow-same-origin, scripts are blocked outright, and it
// may use only inline styles and data: images and fonts. frame-ancestors is
// the site's own origin, so only this site can frame it.
func (a *app) rawCSP() string {
	return "sandbox allow-popups allow-popups-to-escape-sandbox; default-src 'none'; script-src 'none'; " +
		"style-src 'unsafe-inline'; img-src data:; font-src data:; form-action 'none'; base-uri 'none'; " +
		"frame-ancestors " + a.cfg.Origin
}

// readRaw is the public byte path, the only address the reader pages frame.
// It serves a report only while it is published, never reads the viewer that
// Identify resolved, and sets no cookie. Every report that is not public (a draft, an unpublished,
// a deleted, an unknown or a malformed slug) is the same http.NotFound,
// written before any report header, so it keeps the site policy and gives
// nothing away. A published row whose file is gone is not hidden: it is
// logged and answered 500, so corruption stays visible and no bytes are sent.
//
// Known limit on a Windows host: a slow reader holds the file open, and a
// replace that finds it open is refused with ErrBusy (admin: 409, nothing
// changed). The production host is a Linux container, where it is not.
func (a *app) readRaw(w http.ResponseWriter, r *http.Request) {
	rep, f, err := a.reports.OpenRaw(r.Context(), r.PathValue("slug"), true)
	if errors.Is(err, reports.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("reader: open report file", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	a.serveRaw(w, r, rep, f, "no-cache")
}

// serveRaw is the only code that writes report bytes. It replaces the site
// CSP (and the gate's headers) with the report policy and answers the file
// with http.ServeContent, which handles HEAD, ETag and Range. cache is the
// Cache-Control value: "no-cache" for a public report, "no-store" for the
// gated preview, which may be a draft. It closes f before it returns.
func (a *app) serveRaw(w http.ResponseWriter, r *http.Request, rep reports.Report, f *os.File, cache string) {
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", a.rawCSP())
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("ETag", `"`+rep.SHA256+`"`)
	h.Set("Cache-Control", cache)
	// No modification time: the ETag is the validator.
	http.ServeContent(w, r, "", time.Time{}, f)
}
