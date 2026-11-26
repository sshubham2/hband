package main

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sshubham2/hband/internal/testutil/program"
)

var linkRE = regexp.MustCompile(`<a href="([^"]+)">([^<]+)</a>`)

// follow follows redirects the way a browser does, on any host: the sign-in goes
// from the program to the stand-in GitHub and back.
func follow(t *testing.T, v *program.Visitor, pg program.Page) program.Page {
	t.Helper()
	for i := 0; i < 10 && pg.Status >= 300 && pg.Status < 400 && pg.Location != ""; i++ {
		base, err := url.Parse(pg.URL)
		if err != nil {
			t.Fatal(err)
		}
		next, err := base.Parse(pg.Location)
		if err != nil {
			t.Fatal(err)
		}
		pg = v.Get(t, next.String())
	}
	return pg
}

// picker asks the program to sign v in and returns the stand-in's page, where a
// browser would be left.
func picker(t *testing.T, v *program.Visitor, start string) program.Page {
	t.Helper()
	return follow(t, v, v.Get(t, start))
}

// signInBy signs a new visitor in the way the look does: it starts a sign-in, and
// presses the link with the given text on the stand-in's page.
func signInBy(t *testing.T, p *program.Program, label string) (*program.Visitor, program.Page) {
	t.Helper()
	v := p.Anonymous()
	pg := picker(t, v, "/auth/github/login")
	if pg.Status != http.StatusOK {
		t.Fatalf("the sign-in ended on %s with %d, want the stand-in's page of links:\n%.300s", pg.URL, pg.Status, pg.Body)
	}
	var labels []string
	for _, m := range linkRE.FindAllStringSubmatch(pg.Body, -1) {
		labels = append(labels, html.UnescapeString(m[2]))
		if html.UnescapeString(m[2]) == label {
			base, _ := url.Parse(pg.URL)
			href, err := base.Parse(html.UnescapeString(m[1]))
			if err != nil {
				t.Fatal(err)
			}
			return v, follow(t, v, v.Get(t, href.String()))
		}
	}
	t.Fatalf("the stand-in's page has no link %q; it has %q", label, labels)
	return nil, program.Page{}
}

func mustGet(t *testing.T, v *program.Visitor, path string, want int) string {
	t.Helper()
	pg := v.Get(t, path)
	if pg.Status != want {
		t.Fatalf("GET %s answered %d, want %d:\n%.400s", path, pg.Status, want, pg.Body)
	}
	return pg.Body
}

func wantAll(t *testing.T, where, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("%s lacks %q", where, w)
		}
	}
}

func wantNone(t *testing.T, where, body string, not ...string) {
	t.Helper()
	for _, w := range not {
		if strings.Contains(body, w) {
			t.Errorf("%s holds %q", where, w)
		}
	}
}

// TestSeededProductOffersTheOwnerAndTheReader is the screens the look photographs
// beyond the reader pages, reached the way the look reaches them: by pressing
// the two links on the stand-in GitHub's page. The owner link opens /admin, the
// published report's admin page, the draft's and Blocked users, each with what it
// is there to show; the reader link opens the Not allowed page at /admin and the
// reader's own message with its reply; and nobody is signed in by merely asking.
func TestSeededProductOffersTheOwnerAndTheReader(t *testing.T) {
	n := testPort(t)
	var out strings.Builder
	p := run(t, n, &out)
	addr := fmt.Sprintf("127.0.0.1:%d", n)

	// The public thread is untouched by what was added: nine visible comments.
	wantSeeded(t, gather(t, addr))

	t.Run("nobody is signed in by asking", func(t *testing.T) {
		v := p.Anonymous()
		pg := picker(t, v, "/admin")
		if pg.Status != http.StatusOK || !strings.Contains(pg.Body, ownerChoice) || !strings.Contains(pg.Body, readerChoice) {
			t.Fatalf("an anonymous GET /admin ended on %s with %d, want the stand-in's page with both links:\n%.300s", pg.URL, pg.Status, pg.Body)
		}
		if again := v.Get(t, "/admin"); again.Status != http.StatusSeeOther {
			t.Errorf("after reaching the stand-in's page GET /admin answered %d, want 303: the visitor was signed in without choosing", again.Status)
		}
	})

	t.Run("the owner", func(t *testing.T) {
		v, landed := signInBy(t, p, ownerChoice)
		if landed.Status != http.StatusOK || !strings.Contains(landed.Body, "Signed in as @report-author") || !strings.Contains(landed.Body, `<a href="/admin">Admin</a>`) {
			t.Fatalf("the owner link landed on %s with %d, want the home page as the owner:\n%.300s", landed.URL, landed.Status, landed.Body)
		}

		list := mustGet(t, v, "/admin", http.StatusOK)
		wantAll(t, "/admin", list, "data-hband-admin", `class="pill pill-published"`, `class="pill pill-draft"`, "Draft Notes", "Look Notes", `href="#upload"`)

		rep := mustGet(t, v, "/admin/reports/e004-look", http.StatusOK)
		wantAll(t, "/admin/reports/e004-look", rep,
			`class="badge">blocked<`,                     // the blocked sender's card
			`class="feedback-reply"`,                     // the author's reply under octocat's message
			"deliberate, and does the thread",            // octocat's message
			"Please link my profile",                     // the blocked account's message
			"Remove for everyone", "Block @plain-reader", // the two-step controls
			`<button type="submit">Unpublish</button>`)
		wantNone(t, "/admin/reports/e004-look", rep, "Come and read my other reports") // the blocked comment is hidden

		draft := mustGet(t, v, "/admin/reports/"+draftSlug, http.StatusOK)
		wantAll(t, "/admin/reports/"+draftSlug, draft, `class="primary">Publish<`, `class="pill pill-draft"`)

		blocked := mustGet(t, v, "/admin/blocked", http.StatusOK)
		wantAll(t, "/admin/blocked", blocked,
			`id="blocked-8803"`, "noisy-reader", "Unblock",
			"Come and read my other reports",            // a comment it posted
			`<span class="removed">removed</span>`,      // and one the owner removed
			"Remove for everyone",                       // the two-step control on the first
			"Please link my profile from every report.") // what it sent
	})

	t.Run("a reader", func(t *testing.T) {
		v, landed := signInBy(t, p, readerChoice)
		if landed.Status != http.StatusOK || !strings.Contains(landed.Body, "Signed in as @octocat") || strings.Contains(landed.Body, `href="/admin">Admin<`) {
			t.Fatalf("the reader link landed on %s with %d, want the home page as octocat, with no Admin link:\n%.300s", landed.URL, landed.Status, landed.Body)
		}
		pg := v.Get(t, "/admin")
		if pg.Status != http.StatusForbidden {
			t.Fatalf("GET /admin as a reader answered %d, want 403", pg.Status)
		}
		wantAll(t, "the Not allowed page", pg.Body, "This GitHub account cannot open admin pages.", "octocat", `action="/auth/logout"`)
		wantNone(t, "the Not allowed page", pg.Body, "data-hband-admin")

		own := mustGet(t, v, "/reports/e004-look", http.StatusOK)
		wantAll(t, "the reader's report page", own,
			"deliberate, and does the thread", // its own message
			`class="feedback-reply"`,          // and the author's reply
			"continues on its own page")
		wantNone(t, "the reader's report page", own,
			"Please link my profile",         // nobody else's message
			"Come and read my other reports") // nor a blocked comment
	})

	// An anonymous reader sees neither message, and the thread is as seeded.
	anon := p.Anonymous()
	pub := mustGet(t, anon, "/reports/e004-look", http.StatusOK)
	wantNone(t, "the anonymous report page", pub, "deliberate, and does the thread", "Please link my profile", "Come and read my other reports", `class="feedback-reply"`)
	if want := fmt.Sprintf("serving http://127.0.0.1:%d/\n", n); out.String() != want {
		t.Errorf("the ready line is %q, want %q", out.String(), want)
	}
}
