package server

import (
	"net/http"

	"github.com/sshubham2/hband/internal/auth"
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
	}
}

func (a *app) adminHome(w http.ResponseWriter, r *http.Request, o auth.Owner) {
	data := pageData{Title: "Admin - hband", SignedIn: true, Viewer: auth.Viewer{GitHubID: o.GitHubID, Login: o.Login, IsOwner: true}}
	if v, ok := auth.ViewerFrom(r.Context()); ok {
		data.Viewer.AvatarURL = v.AvatarURL
	}
	a.render(w, http.StatusOK, "admin.html", data)
}
