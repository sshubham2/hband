package server

import (
	"strings"
	"testing"
)

// A blocked account's reply on a report that is not published, to a parent that is
// visible (it is on the report's admin page), must not be described as a reply to a
// hidden comment: in the admin pages "hidden" means removed or written by a blocked
// account, and this parent is neither.
func TestBlockedPageUnpublishedReplyIsNotToAHiddenComment(t *testing.T) {
	h, owner, reader := commentWorld(t)
	h.mustUpload(owner, "gone", "Gone Report", "published", reportHTML(300, "gone"))
	bad := h.mint(8, "bad-eight")
	h.say(bad, "gone", "bad-eight writes top level", 0)
	parent := h.say(reader, "gone", "a visible comment by the reader", 0)
	h.say(bad, "gone", "bad-eight answers the reader", parent)
	h.mustAct(owner, "gone", "unpublish")
	h.mustBlock(owner, 8)

	admin := h.adminPageOf(owner, "gone")
	if !strings.Contains(admin, "a visible comment by the reader") {
		t.Fatalf("INVALID: the parent is not listed as visible on the report's admin page:\n%s", admin)
	}
	secs := blockedSections(h.page(owner, "/admin/blocked"))
	if len(secs) != 1 || secs[0].ID != 8 {
		t.Fatalf("sections %+v, want the one for account 8", secs)
	}
	e := entryWith(t, "the reply", secs[0].List, "bad-eight answers the reader")
	if strings.Contains(e, "hidden comment") {
		t.Errorf("a reply to a visible comment on an unpublished report is described as a reply to a hidden comment:\n%s", e)
	}
}
