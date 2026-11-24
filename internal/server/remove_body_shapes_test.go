package server

import (
	"net/http"
	"strings"
	"testing"
)

// The remove form's contract: a from other than exactly "admin", and a body over
// 1 KiB, are refused with 400 before anything is written, whatever the encoding.
func TestCommentRemoveRefusesBodiesItDoesNotRead(t *testing.T) {
	h, owner, reader := commentWorld(t)
	for _, c := range []struct{ name, ctype, body string }{
		{"multipart with another from", "multipart/form-data; boundary=b", "--b\r\nContent-Disposition: form-data; name=\"from\"\r\n\r\nbogus\r\n--b--\r\n"},
		{"a plain-text body over 1 KiB", "text/plain", strings.Repeat("a", 2<<10)},
		{"a body over 1 KiB with no type", "", strings.Repeat("a", 2<<10)},
	} {
		id := h.say(reader, "x", "a comment for "+c.name, 0)
		res := h.serve("POST", "/admin/comments/"+itoa(id)+"/remove", h.withSID(owner), withBody(c.ctype, []byte(c.body)))
		if res.Status != http.StatusBadRequest || res.Location() != "" {
			t.Errorf("%s: %d %q, want 400 and no Location", c.name, res.Status, res.Location())
		}
		if h.commentCount("id = ? AND removed_at IS NOT NULL", id) != 0 {
			t.Errorf("%s: the comment was removed by a request the contract refuses", c.name)
		}
	}
	// Control: the reader page's form, with no field, still removes.
	id := h.say(reader, "x", "a comment the plain form removes", 0)
	if res := h.serve("POST", "/admin/comments/"+itoa(id)+"/remove", h.withSID(owner), withBody(formType, nil)); res.Status != http.StatusSeeOther {
		t.Fatalf("control: %d %q", res.Status, res.Body)
	}
}
