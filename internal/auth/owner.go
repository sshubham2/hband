package auth

import (
	"bytes"
	"context"
	"embed"
	"html/template"
	"net/http"
	"net/url"
)

//go:embed templates/forbidden.html
var forbiddenFS embed.FS

var forbiddenPage = template.Must(template.ParseFS(forbiddenFS, "templates/forbidden.html"))

// IsOwner is the only allow rule in the program: the pinned owner ID must be
// positive and equal to the signed-in GitHub ID. It is recomputed on every
// request from in-memory configuration.
func IsOwner(ownerID, githubID int64) bool {
	return ownerID > 0 && githubID == ownerID
}

// Owner is what RequireOwner attaches to the request context when it lets the
// owner through. Only this package can attach it (ownerKey is unexported); an
// Owner value on its own proves nothing.
type Owner struct {
	GitHubID int64
	Login    string
}

type ownerKey struct{}

// OwnerFrom returns the proof RequireOwner attached, if any.
func OwnerFrom(ctx context.Context) (Owner, bool) {
	o, ok := ctx.Value(ownerKey{}).(Owner)
	return o, ok
}

const loginPath = "/auth/github/login"

// RequireOwner is the gate in front of every admin page. It sets
// Cache-Control: no-store first, then:
//
//   - a store error while identifying the visitor: 503;
//   - anonymous GET or HEAD: 303 to sign-in, carrying where they were going;
//   - anonymous with any other method: 403;
//   - a signed-in non-owner: 403 on a page with no admin content;
//   - the owner: the Owner proof goes into the context and h is called.
func RequireOwner(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ctx := r.Context()
		if identifyErrFrom(ctx) != nil {
			http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
			return
		}
		v, ok := ViewerFrom(ctx)
		if !ok {
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				SeeOther(w, loginTarget(r))
				return
			}
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if !v.IsOwner {
			var page bytes.Buffer
			if err := forbiddenPage.Execute(&page, struct{ Login string }{v.Login}); err != nil {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			w.Write(page.Bytes())
			return
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(ctx, ownerKey{}, Owner{GitHubID: v.GitHubID, Login: v.Login})))
	})
}

// loginTarget is where an anonymous admin visitor is sent. The next
// parameter is passed only when the request URI is itself a local path and
// the escaped sign-in URL still fits the redirect cap; otherwise the plain
// sign-in URL is used, so the answer is always the sign-in page.
func loginTarget(r *http.Request) string {
	uri := r.URL.RequestURI()
	if _, ok := LocalPath(uri); !ok {
		return loginPath
	}
	target := loginPath + "?next=" + url.QueryEscape(uri)
	if _, ok := LocalPath(target); !ok {
		return loginPath
	}
	return target
}

// OwnerRoute is the second interlock: a handler reached without the gate's
// proof in its context answers 403 and is never called. A Viewer, even one
// with IsOwner set, is not proof.
func OwnerRoute(fn func(http.ResponseWriter, *http.Request, Owner)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o, ok := OwnerFrom(r.Context())
		if !ok {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		fn(w, r, o)
	})
}
