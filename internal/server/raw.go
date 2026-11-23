package server

import (
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
