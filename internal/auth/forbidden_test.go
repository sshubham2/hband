package auth

import (
	"bytes"
	"html"
	"strings"
	"testing"
)

// TestForbiddenPageTakesTheShell executes the Not allowed page the way RequireOwner
// does and reads what the shell must give it: the title, the one stylesheet link,
// the brand-only header, and none of the admin marker or a script.
func TestForbiddenPageTakesTheShell(t *testing.T) {
	for _, login := range []string{"", "octo<b>&cat"} {
		var page bytes.Buffer
		if err := forbiddenPage.Execute(&page, forbiddenData{Title: forbiddenTitle, Login: login}); err != nil {
			t.Fatalf("login %q: %v", login, err)
		}
		body := page.String()
		for _, want := range []string{
			`<title>Not allowed - hband</title>`,
			`<link rel="stylesheet" href="/static/site.css">`,
			`This GitHub account cannot open admin pages.`,
			`<header class="site-header">`,
			`<a class="brand" href="/">hband</a>`,
			`<a href="/">Back to the site</a>`,
			`<meta name="viewport" content="width=device-width, initial-scale=1">`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("login %q: the page lacks %q:\n%s", login, want, body)
			}
		}
		if n := strings.Count(body, `href="/static/site.css"`); n != 1 {
			t.Errorf("login %q: %d stylesheet links, want 1", login, n)
		}
		if strings.Contains(body, "data-hband-admin") {
			t.Errorf("login %q: the Not allowed page carries the admin marker", login)
		}
		if strings.Contains(strings.ToLower(body), "<script") {
			t.Errorf("login %q: the Not allowed page loads a script", login)
		}
		if strings.Contains(body, "<nav>") || strings.Contains(body, "Signed in as") {
			t.Errorf("login %q: the Not allowed page's header is more than the brand", login)
		}
		if login == "" {
			if strings.Contains(body, "You are signed in as") || strings.Contains(body, "Sign out") {
				t.Errorf("an empty login still gets the signed-in sentence or the Sign out button:\n%s", body)
			}
			continue
		}
		if !strings.Contains(body, "You are signed in as @"+html.EscapeString(login)+".") || strings.Contains(body, "octo<b>") {
			t.Errorf("the login is not escaped in the page:\n%s", body)
		}
		if !strings.Contains(body, `<form method="post" action="/auth/logout"><button type="submit">Sign out</button></form>`) {
			t.Errorf("a signed-in non-owner is offered no Sign out:\n%s", body)
		}
	}
}
