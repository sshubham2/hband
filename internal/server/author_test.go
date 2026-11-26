package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/sshubham2/hband/internal/comments"
	"github.com/sshubham2/hband/internal/store"
)

// A report page leads with the writing: under the title a byline names the author
// and the date. The author is the owner's account as sign-in last saw it, so the
// name and avatar are read from the users row of the configured owner. Nothing else
// on the page depends on that read, so a missing row, no configured owner or a
// read that fails leave the byline starting at the date, and the page is still a
// 200 with the report on it.
func TestReportPageNamesTheAuthor(t *testing.T) {
	h := newHarness(t, false)
	const avatar = "https://avatars.githubusercontent.com/u/42?v=4"
	owner := h.mintUser(store.User{GitHubID: testOwnerID, Login: "the-<b>owner", AvatarURL: avatar})
	h.mustUpload(owner, "q3", "Q3 review", "published", reportHTML(300, "q3"))

	byline := func(path string, opts ...func(*http.Request)) string {
		t.Helper()
		res := h.serve("GET", path, opts...)
		if res.Status != 200 {
			t.Fatalf("GET %s: %d", path, res.Status)
		}
		if !strings.Contains(res.Body, "<h1>Q3 review</h1>") {
			t.Fatalf("GET %s: the report is not on the page:\n%s", path, res.Body)
		}
		start := strings.Index(res.Body, `<p class="meta">`)
		if start < 0 {
			t.Fatalf("GET %s: no byline:\n%s", path, res.Body)
		}
		end := strings.Index(res.Body[start:], "</p>")
		return res.Body[start : start+end]
	}

	// The owner has signed in once: the byline names them, with their avatar, on the
	// home page and on the report page, whoever is looking. The login is escaped.
	const author = `<span class="author"><img class="avatar" src="` + avatar + `" alt="" width="24" height="24"> the-&lt;b&gt;owner</span> &middot; Published `
	for _, path := range []string{"/", "/reports/q3"} {
		for who, opts := range map[string][]func(*http.Request){"anonymous": nil, "the owner": {h.withSID(owner)}} {
			if got := byline(path, opts...); !strings.HasPrefix(got, `<p class="meta">`+author) {
				t.Errorf("GET %s as %s: the byline is\n%s\nwant it to begin with the author, then the date:\n%s", path, who, got, author)
			}
		}
	}

	// An owner with no avatar is named without an image.
	h.mintUser(store.User{GitHubID: testOwnerID, Login: "the-owner"})
	if got := byline("/reports/q3"); !strings.HasPrefix(got, `<p class="meta"><span class="author">the-owner</span> &middot; Published `) {
		t.Errorf("an owner without an avatar: the byline is\n%s", got)
	}

	// Nobody to name: an owner who never signed in, and no owner at all. The byline
	// starts at the date, and there is no author element.
	for name, id := range map[string]int64{"an owner who never signed in": 99, "no configured owner": 0} {
		h.rebuild(id)
		if got := byline("/reports/q3"); !strings.HasPrefix(got, `<p class="meta">Published `) || strings.Contains(got, `class="author"`) {
			t.Errorf("%s: the byline is\n%s\nwant it to start at the date, with no author", name, got)
		}
	}

	// The users read fails and nothing else does: the thread read is replaced by an
	// empty one and the column the byline reads is renamed away. The page is still
	// the report, a 200, with no author.
	h.rebuild(testOwnerID)
	h.app.tree = func(context.Context, int64) ([]*comments.Node, error) { return nil, nil }
	if _, err := h.store.DB().Exec(`ALTER TABLE users RENAME COLUMN avatar_url TO avatar_gone`); err != nil {
		t.Fatalf("break the users read: %v", err)
	}
	if got := byline("/reports/q3"); !strings.HasPrefix(got, `<p class="meta">Published `) || strings.Contains(got, `class="author"`) {
		t.Errorf("the users read fails: the byline is\n%s\nwant it to start at the date, with no author", got)
	}
}
