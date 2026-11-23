package server

import (
	"net/http"

	"github.com/sshubham2/hband/internal/auth"
)

// pageData feeds every site template.
type pageData struct {
	Title    string
	SignedIn bool
	Viewer   auth.Viewer
}

// home is the public landing page. A visitor whose session could not be
// resolved (the store is down) is rendered as anonymous.
func (a *app) home(w http.ResponseWriter, r *http.Request) {
	v, ok := auth.ViewerFrom(r.Context())
	a.render(w, http.StatusOK, "home.html", pageData{Title: "hband", SignedIn: ok, Viewer: v})
}
