package main

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"

	"github.com/sshubham2/hband/internal/testutil/fakegithub"
	"github.com/sshubham2/hband/internal/testutil/program"
)

// What a look can reach beyond the reader pages. program.DeepThread seeds the
// published report and its nine-level thread; seedScreens adds, through the
// product's own routes, what the owner's screens and the signed-in reader's page
// need to show:
//
//   - a draft report next to the published one (/admin lists both statuses, and
//     the draft's page offers Publish);
//   - a private message from the reader the look signs in as (octocat), with the
//     author's reply, so the reader's page draws its own messages and the reply,
//     and the published report's admin page draws an inbox card with a reply;
//   - an account (noisy-reader) that commented twice, had one comment removed,
//     sent a private message, and was then blocked, so the admin page draws the
//     'blocked' badge and the Blocked users page draws an account with both lists.
//
// The blocked account's comments are hidden from every reader and every count, so
// the public thread stays the nine-level one the launcher's tests pin.

const (
	// ownerChoice and readerChoice are the texts of the two links the stand-in
	// GitHub shows when a browser signs in. The look presses them by their text,
	// so they are the launcher's contract with the screens it is photographed
	// through.
	ownerChoice  = "Continue as the owner"
	readerChoice = "Continue as a reader"

	draftSlug = "e004-draft"

	standInClientID = "seeded-look"
	standInSecret   = "seeded-look-secret"
)

// ownerUser is the account the owner choice signs in: the one the product pins as
// its owner, under the login program.DeepThread's owner already has. Its avatar is
// on a host the site's CSP refuses, which draws the plain circle and never leaves
// this machine.
var ownerUser = fakegithub.User{ID: program.OwnerID, Login: "report-author", AvatarURL: "https://avatars.example/u/4242?v=4"}

// readerUser is the account the reader choice signs in, and the one a fresh
// stand-in signs in by default: a plain reader, not the owner.
var readerUser = fakegithub.User{ID: 42, Login: "octocat", AvatarURL: "https://avatars.githubusercontent.com/u/42"}

// noisyUser is the account that is blocked.
var noisyUser = fakegithub.User{ID: 8803, Login: "noisy-reader"}

// choices are what the serving program's stand-in GitHub offers.
func choices() []fakegithub.Choice {
	return []fakegithub.Choice{
		{Label: ownerChoice, User: ownerUser},
		{Label: readerChoice, User: readerUser},
	}
}

var (
	replyFormRE = regexp.MustCompile(`action="/admin/feedback/([0-9]+)/reply"`)
	commentIDRE = regexp.MustCompile(`#comment-([0-9]+)$`)
)

// seedScreens seeds the product p (the staging program, with th already posted)
// as the comment above says. Every step is checked: a refusal stops the launcher
// naming the step, so a look never photographs a half-seeded product.
func seedScreens(t program.T, p *program.Program, th program.Thread) {
	t.Helper()
	owner := th.Owner

	got := owner.Upload(t, "/admin/reports",
		map[string]string{"title": "Draft Notes", "slug": draftSlug, "status": "draft"},
		"file", draftSlug+".html",
		[]byte("<!doctype html><html><head><title>Draft Notes</title></head><body><h1>Draft Notes</h1><p>Not published yet.</p></body></html>"))
	mustSeeOther(t, "uploading the draft report", got)

	reader := p.SignIn(t, readerUser)
	mustSeeOther(t, "the reader's private message",
		reader.Post(t, "/reports/"+th.Slug+"/feedback", url.Values{"body": {"The five-level cap reads well on a phone. Is it deliberate, and does the thread continue anywhere?"}}))
	// The message is the only one so far, so the page has exactly one reply form.
	inbox := owner.Get(t, "/admin/reports/"+th.Slug)
	m := replyFormRE.FindAllStringSubmatch(inbox.Body, -1)
	if inbox.Status != http.StatusOK || len(m) != 1 {
		t.Fatalf("seeded: the owner's page for %s answered %d with %d reply forms, want 200 and 1", th.Slug, inbox.Status, len(m))
		return
	}
	mustSeeOther(t, "the author's reply",
		owner.Post(t, "/admin/feedback/"+m[0][1]+"/reply", url.Values{"body": {"Yes: past the fifth level a reply continues on its own page, behind 'continue this thread'."}}))

	noisy := p.SignIn(t, noisyUser)
	var ids []string
	for _, text := range []string{
		"First! Come and read my other reports too, they are much better than this one.",
		"Second comment, because the first one deserved company.",
	} {
		noisy.GetFollow(t, "/reports/"+th.Slug)
		landed := noisy.PostFollow(t, "/reports/"+th.Slug+"/comments", url.Values{"body": {text}})
		id := commentIDRE.FindStringSubmatch(landed.URL)
		if id == nil {
			t.Fatalf("seeded: the blocked account's comment landed on %s, not on a #comment-<id>", landed.URL)
			return
		}
		ids = append(ids, id[1])
	}
	mustSeeOther(t, "the blocked account's private message",
		noisy.Post(t, "/reports/"+th.Slug+"/feedback", url.Values{"body": {"Please link my profile from every report."}}))
	mustSeeOther(t, "removing the blocked account's second comment",
		owner.Post(t, "/admin/comments/"+ids[1]+"/remove", url.Values{}))
	mustSeeOther(t, "blocking the account",
		owner.Post(t, fmt.Sprintf("/admin/users/%d/block", noisyUser.ID), url.Values{}))
}

// mustSeeOther fails t unless a seeding step was answered with a 303.
func mustSeeOther(t program.T, step string, got program.Page) {
	t.Helper()
	if got.Status != http.StatusSeeOther {
		t.Fatalf("seeded: %s answered %d, want 303: %.300s", step, got.Status, got.Body)
	}
}
