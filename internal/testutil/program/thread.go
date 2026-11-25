package program

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/sshubham2/hband/internal/testutil/fakegithub"
)

// Thread is the look's nine-level thread (the signed-in look of the E-004 line 2
// check, run_finish_e004_2.py's seed), posted through the real handlers.
type Thread struct {
	Slug     string
	IDs      [9]string // the id of the comment at each level, level 1 first
	Branch   string    // /reports/<slug>/comments/<id of level 5>
	Landings [9]string // the URL each post landed on
	Owner    *Visitor
	Who      *Visitor // the account whose login is 39 characters long
	Reader   *Visitor
}

var landedRE = regexp.MustCompile(`#comment-(\d+)$`)

// DeepThread publishes the report 'e004-look' and posts a chain of nine
// comments: the owner at level 5 (the Author badge), a 39-character login with
// an unbroken 120-character body at levels 4 and 9, and a plain reader
// elsewhere. Level 9 is the cut comment of the branch page rooted at level 5.
func DeepThread(t T, p *Program) Thread {
	t.Helper()
	th := Thread{Slug: "e004-look"}
	th.Owner = p.Owner(t)
	th.Owner.Publish(t, th.Slug, "Look Notes",
		"<!doctype html><html><head><title>Look Notes</title></head><body><h1>Look Notes</h1></body></html>")
	longLogin := strings.Repeat("w", 39) // the most GitHub allows, with no hyphen to break at and the widest letter
	// ported:RE-108
	if len(longLogin) != 39 {
		t.Fatalf("the long login is %d characters, not 39", len(longLogin))
	}
	th.Who = p.SignIn(t, fakegithub.User{ID: 8801, Login: longLogin})
	th.Reader = p.SignIn(t, fakegithub.User{ID: 8802, Login: "plain-reader"})
	parent := ""
	for level := 1; level <= 9; level++ {
		poster := th.Reader
		switch {
		case level == 5:
			poster = th.Owner
		case level == 4 || level == 9:
			poster = th.Who
		}
		text := "level " + string(rune('0'+level)) + " text"
		if level == 4 || level == 9 {
			text = strings.Repeat("UNBROKEN", 15)
		}
		from := "/reports/" + th.Slug
		if level > 5 {
			from = th.Branch
		}
		poster.GetFollow(t, from)
		form := url.Values{"body": {text}}
		if parent != "" {
			form.Set("parent", parent)
		}
		got := poster.PostFollow(t, "/reports/"+th.Slug+"/comments", form)
		m := landedRE.FindStringSubmatch(got.URL)
		// ported:RE-121
		if m == nil {
			t.Fatalf("level %d: landed on %s", level, got.URL)
			return th
		}
		parent = m[1]
		th.IDs[level-1], th.Landings[level-1] = parent, got.URL
		if level == 5 {
			th.Branch = "/reports/" + th.Slug + "/comments/" + parent
		}
		// ported:RE-127
		if level > 5 && !strings.HasSuffix(strings.SplitN(got.URL, "#", 2)[0], th.Branch) {
			t.Fatalf("level %d landed on %s, not on %s", level, got.URL, th.Branch)
			return th
		}
	}
	return th
}
