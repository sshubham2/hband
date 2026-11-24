package server

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// press finds the first form on page whose action matches the pattern, and sends
// it the way a browser does: its method, its action, and the hidden fields it
// carries. It fails the test unless a form matches.
func (h *harness) press(t *testing.T, sid, page, actionPattern string) result {
	t.Helper()
	form := regexp.MustCompile(`(?s)<form method="post" action="(` + actionPattern + `)">(.*?)</form>`)
	m := form.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no form matches %s, in:\n%s", actionPattern, page)
	}
	var fields []string
	for _, in := range regexp.MustCompile(`<input type="hidden" name="(\w+)" value="(\w*)">`).FindAllStringSubmatch(m[2], -1) {
		fields = append(fields, in[1]+"="+in[2])
	}
	return h.serve("POST", m[1], h.withSID(sid), withBody(formType, []byte(strings.Join(fields, "&"))))
}

// TestUnblockRestores drives the author's real controls from first to last: a
// block from a comment's own Block form, a removal from the Blocked users page
// while the account is blocked, and an Unblock from that page. The account's
// comments and the counts come back, except the comments removed in between, which
// stay removed (a SQL-deleted block row could not show that: lesson #977), and the
// account takes part again.
func TestUnblockRestores(t *testing.T) {
	h, owner, reader := commentWorld(t)
	h.mustUpload(owner, "z", "Report Z", "published", reportHTML(300, "z"))
	bad := h.mint(8, "bad-eight")
	other := h.mint(9, "other-nine")

	ra := h.say(reader, "x", "visible by the reader", 0)
	xa := h.say(bad, "x", "bad-eight lone top level", 0)
	xr := h.say(bad, "x", "bad-eight reply to the reader", ra)
	xt := h.say(bad, "x", "bad-eight top level with a reply", 0)
	rr := h.say(reader, "x", "reader answers bad-eight", xt)
	xz := h.say(bad, "z", "bad-eight on z", 0)
	h.say(other, "z", "other on z", 0)
	h.mustVote(bad, "x", "up")
	h.mustVote(other, "x", "down")

	count := func(slug string) int {
		t.Helper()
		n, _ := countOn(t, h.page("", "/reports/"+slug))
		return n
	}
	// drawn maps each comment a public page draws to whether it is drawn as itself
	// (false: as the placeholder).
	drawn := func(slug string) map[int64]bool {
		t.Helper()
		out := map[int64]bool{}
		for _, a := range parseThread(t, h.page("", "/reports/"+slug)) {
			out[a.ID] = !strings.Contains(a.Own, "comment removed")
		}
		return out
	}
	sameDrawn := func(when, slug string, want map[int64]bool) {
		t.Helper()
		got := drawn(slug)
		if len(got) != len(want) {
			t.Errorf("%s: %s draws %v, want %v", when, slug, got, want)
			return
		}
		for id, self := range want {
			if s, ok := got[id]; !ok || s != self {
				t.Errorf("%s: %s draws %v, want %v", when, slug, got, want)
				return
			}
		}
	}
	tallyIs := func(when string, likes, dislikes int) {
		t.Helper()
		got, err := h.app.votes.OwnerTally(bg, h.reportOf("x"))
		if err != nil || got.Likes != likes || got.Dislikes != dislikes {
			t.Errorf("%s: tally %+v (%v), want %d likes and %d dislikes", when, got, err, likes, dislikes)
		}
	}
	if count("x") != 5 || count("z") != 2 {
		t.Fatalf("INVALID: the starting counts are %d and %d, want 5 and 2", count("x"), count("z"))
	}
	tallyIs("before the block", 1, 1)
	likes := likesLine(t, h.page("", "/reports/x"))

	// Block: pressed on the Block form of one of the account's comments on the report's
	// admin page.
	res := h.press(t, owner, h.adminPageOf(owner, "x"), `/admin/users/8/block`)
	if res.Status != http.StatusSeeOther || res.Location() != "/admin/blocked#blocked-8" {
		t.Fatalf("the Block form: %d %q, want a 303 to /admin/blocked#blocked-8", res.Status, res.Location())
	}
	if count("x") != 2 || count("z") != 1 {
		t.Errorf("after the block: counts %d and %d, want 2 and 1", count("x"), count("z"))
	}
	sameDrawn("after the block", "x", map[int64]bool{ra: true, xt: false, rr: true})
	sameDrawn("after the block", "z", map[int64]bool{h.lastComment("z"): true})
	if got := likesLine(t, h.page("", "/reports/x")); got != likes {
		t.Errorf("blocking changed the like line: %q, was %q", got, likes)
	}
	tallyIs("after the block", 1, 1)

	// Remove, from the Blocked users page, a blocked comment nothing draws (xa) and one
	// held as a placeholder (xt), each by the form the page draws for it.
	for _, id := range []int64{xa, xt} {
		page := h.page(owner, "/admin/blocked")
		res := h.press(t, owner, page, `/admin/comments/`+itoa(id)+`/remove`)
		if res.Status != http.StatusSeeOther || res.Location() != "/admin/blocked" {
			t.Fatalf("removing comment %d from the Blocked users page: %d %q, want a 303 back to it", id, res.Status, res.Location())
		}
	}
	page := h.page(owner, "/admin/blocked")
	for _, s := range []string{"bad-eight lone top level", "bad-eight top level with a reply"} {
		if strings.Contains(page, s) {
			t.Errorf("the text of a removed comment is on the Blocked users page: %q", s)
		}
	}
	if !strings.Contains(page, "bad-eight reply to the reader") || strings.Contains(page, `/admin/comments/`+itoa(xa)+`/remove`) {
		t.Errorf("the page should still list the comments that are not removed, and give the removed ones no form:\n%s", page)
	}
	if count("x") != 2 {
		t.Errorf("removing blocked comments changed the public count: %d, want 2", count("x"))
	}

	// Unblock: pressed on the page's own Unblock form.
	res = h.press(t, owner, page, `/admin/users/8/unblock`)
	if res.Status != http.StatusSeeOther || res.Location() != "/admin/blocked" {
		t.Fatalf("the Unblock form: %d %q, want a 303 to /admin/blocked", res.Status, res.Location())
	}
	if _, ok := h.blockedAt(8); ok {
		t.Fatal("the account is still blocked")
	}
	if got := h.page(owner, "/admin/blocked"); !strings.Contains(got, "Nobody is blocked.") {
		t.Errorf("the Blocked users page after the unblock:\n%s", got)
	}
	// The comments that were not removed are back, in their places; the removed ones are
	// not: the lone one is gone and the one with a reply is a placeholder again.
	if count("x") != 3 || count("z") != 2 {
		t.Errorf("after the unblock: counts %d and %d, want 3 and 2", count("x"), count("z"))
	}
	sameDrawn("after the unblock", "x", map[int64]bool{ra: true, xr: true, xt: false, rr: true})
	if got := drawn("z"); !got[xz] || len(got) != 2 {
		t.Errorf("after the unblock: z draws %v, want its two comments with %d among them", got, xz)
	}
	for _, s := range []string{"bad-eight lone top level", "bad-eight top level with a reply"} {
		for _, p := range []string{"/", "/reports/x"} {
			if strings.Contains(h.page("", p), s) || strings.Contains(h.page(owner, p), s) {
				t.Errorf("GET %s shows %q, which was removed while its writer was blocked", p, s)
			}
		}
	}
	if sql := h.commentCount(visibleWhere); sql != 5 {
		t.Errorf("the database counts %d visible comments, want 5 (the page counts agree with it)", sql)
	}
	if got := likesLine(t, h.page("", "/reports/x")); got != likes {
		t.Errorf("unblocking changed the like line: %q, was %q", got, likes)
	}
	tallyIs("after the unblock", 1, 1)

	// It takes part again: it comments, sends feedback, votes and withdraws.
	h.say(bad, "x", "bad-eight is back", ra)
	h.sendFeedback(bad, "x", "bad-eight is back, privately")
	h.mustVote(bad, "x", "down")
	tallyIs("after it voted down again", 0, 2)
	h.mustVote(bad, "x", "none")
	tallyIs("after it withdrew", 0, 1)
	if !strings.Contains(h.adminPageOf(owner, "x"), "bad-eight is back, privately") {
		t.Error("the unblocked account's message did not arrive")
	}

	// Blocking it again does not undo the removals, and unblocking it once more
	// leaves them as they were.
	h.mustBlock(owner, 8)
	h.mustUnblock(owner, 8)
	sameDrawn("after a second block and unblock", "x", map[int64]bool{ra: true, xr: true, xt: false, rr: true, h.lastComment("x"): true})
}

// lastComment is the id of the newest comment on the report.
func (h *harness) lastComment(slug string) int64 {
	h.t.Helper()
	var id int64
	err := h.store.DB().QueryRow(`SELECT max(id) FROM comments WHERE report_id = ?`, h.reportOf(slug)).Scan(&id)
	if err != nil {
		h.t.Fatalf("the newest comment on %s: %v", slug, err)
	}
	return id
}

// TestBlockControls: the Block form on the report's admin page, and what the page
// says about blocked accounts. The form is on each comment not written by the
// author, closed by default, and carries no field; a feedback message is marked
// only when its sender is blocked; a reply under a hidden comment says "hidden".
func TestBlockControls(t *testing.T) {
	h, owner, reader := commentWorld(t)
	bad := h.mint(8, "bad-eight")
	top := h.say(reader, "x", "a comment by the reader", 0)
	mine := h.say(owner, "x", "a comment by the owner", 0)
	bt := h.say(bad, "x", "a comment by bad-eight", 0)
	reply := h.say(reader, "x", "the reader answers bad-eight", bt)
	h.sendFeedback(reader, "x", "a note from the reader")
	h.sendFeedback(bad, "x", "a note from bad-eight")

	articles := func() map[int64]string {
		t.Helper()
		out := map[int64]string{}
		page := h.adminPageOf(owner, "x")
		for _, m := range regexp.MustCompile(`(?s)<article class="mod">(.*?)</article>`).FindAllStringSubmatch(page, -1) {
			for _, id := range []int64{top, mine, bt, reply} {
				if regexp.MustCompile(`/admin/comments/` + itoa(id) + `/remove"`).MatchString(m[1]) {
					out[id] = m[1]
				}
			}
		}
		return out
	}
	blockForm := func(id int64, who string) *regexp.Regexp {
		return regexp.MustCompile(`<details class="block">\s*<summary>Block</summary>\s*<form method="post" action="/admin/users/` + itoa(id) + `/block">\s*<button type="submit">Block @` + who + `</button>\s*</form>\s*</details>`)
	}
	arts := articles()
	if len(arts) != 4 {
		t.Fatalf("the admin page lists %d of the 4 comments", len(arts))
	}
	for id, who := range map[int64]string{top: "reader", bt: "bad-eight", reply: "reader"} {
		own := map[int64]int64{top: 7, bt: 8, reply: 7}[id]
		if !blockForm(own, who).MatchString(arts[id]) {
			t.Errorf("comment %d has no closed Block form for @%s:\n%s", id, who, arts[id])
		}
	}
	if strings.Contains(arts[mine], "/block") || strings.Contains(arts[mine], ">Block") {
		t.Errorf("the author's own comment carries a Block form:\n%s", arts[mine])
	}
	// The Block control carries no field and is closed by default.
	for _, id := range []int64{top, bt, reply} {
		d := regexp.MustCompile(`(?s)<details class="block">.*?</details>`).FindString(arts[id])
		if strings.Contains(d, "<input") || strings.Contains(d, " open") {
			t.Errorf("the Block control carries a field or is open by default:\n%s", d)
		}
	}
	// Reader pages show nothing of it.
	for _, p := range []string{"/", "/reports/x"} {
		for _, sid := range []string{"", reader, owner} {
			if page := h.page(sid, p); strings.Contains(page, "/admin/users/") || strings.Contains(page, `class="block"`) {
				t.Errorf("GET %s shows a block control or an account id", p)
			}
		}
	}

	// Feedback: unmarked while nobody is blocked, then marked for the blocked sender only.
	markedRE := regexp.MustCompile(`(?i)\bblocked\b`)
	for _, it := range inboxOf(h.adminPageOf(owner, "x")) {
		if markedRE.MatchString(it.Own) {
			t.Errorf("feedback %d is marked before anyone is blocked:\n%s", it.ID, it.Own)
		}
	}
	// Block from the form, then look again.
	if res := h.press(t, owner, h.adminPageOf(owner, "x"), `/admin/users/8/block`); res.Status != http.StatusSeeOther {
		t.Fatalf("blocking from the form: %d", res.Status)
	}
	inbox := inboxOf(h.adminPageOf(owner, "x"))
	if len(inbox) != 2 {
		t.Fatalf("%d messages after the block, want both kept", len(inbox))
	}
	for _, it := range inbox {
		isBad := strings.Contains(it.Own, "a note from bad-eight")
		if got := markedRE.MatchString(it.Own); got != isBad {
			t.Errorf("feedback %d: marked = %v for a sender who is blocked = %v:\n%s", it.ID, got, isBad, it.Own)
		}
		if isBad && !strings.Contains(it.Own, `<span class="badge">blocked</span>`) {
			t.Errorf("the mark is not the badge:\n%s", it.Own)
		}
	}
	// The blocked account's comment is off the list, and the reader's reply under it
	// now says its parent is hidden (not removed: unblocking brings it back).
	arts = articles()
	if _, ok := arts[bt]; ok {
		t.Error("a blocked account's comment is still on the moderation list")
	}
	if !strings.Contains(arts[reply], "in reply to a hidden comment") || strings.Contains(arts[reply], "removed comment") {
		t.Errorf("the reply under a blocked comment:\n%s", arts[reply])
	}
	// The word "blocked" is nowhere else in a feedback article of a sender who is not.
	for _, it := range inbox {
		if !strings.Contains(it.Own, "a note from bad-eight") && markedRE.MatchString(it.Own) {
			t.Errorf("an unblocked sender's message is marked:\n%s", it.Own)
		}
	}
}
