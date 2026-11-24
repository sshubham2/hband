package server

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The class: a form handler whose every field is optional reads a body only if
// it happens to be a form of the kind the standard library parses. A body in any
// other encoding is not read at all, so it looks like "no field", which for the
// remove form is the valid reader-page case, and removal has no undo. Members of
// the class come from ordinary senders: a script posting JSON or text, a client
// that sends multipart, a request with no Content-Type, a body over the cap.
//
// The contract this corpus pins, for every encoding: a form is read, in either
// form encoding, and `from` is checked strictly; a body the handler cannot read
// as a form is refused unless it is empty; nothing over 1 KiB is read past, and
// nothing is changed by a refusal. An empty body that is not a form is the plain
// form; an empty multipart body, or one whose type does not parse, is refused.

// bodyRow is one request body. want is the status; admin is the landing of a 303.
type bodyRow struct {
	name, ctype, body string
	want              int
	admin             bool
}

const (
	urlenc  = "application/x-www-form-urlencoded"
	multipt = "multipart/form-data; boundary=b"
)

// part is one field of a multipart body whose boundary is b.
func part(name, value string) string {
	return "--b\r\nContent-Disposition: form-data; name=\"" + name + "\"\r\n\r\n" + value + "\r\n"
}

const endPart = "--b--\r\n"

func removeBodyRows() []bodyRow {
	pad := func(n int) string { return strings.Repeat("a", n) }
	return []bodyRow{
		// Urlencoded: the reader page's form, the admin page's, the aliases of the type, the cap.
		{"urlencoded, no field", urlenc, "", 303, false},
		{"urlencoded, from=admin", urlenc, "from=admin", 303, true},
		{"urlencoded alias: upper case with a charset", "Application/X-WWW-Form-Urlencoded; charset=UTF-8", "from=admin", 303, true},
		{"urlencoded, an unknown field", urlenc, "other=1", 303, false},
		{"urlencoded, another from", urlenc, "from=bogus", 400, false},
		{"urlencoded, an empty from", urlenc, "from=", 400, false},
		{"urlencoded, from repeated", urlenc, "from=admin&from=admin", 400, false},
		{"urlencoded, exactly the cap", urlenc, "pad=" + pad(1024-len("pad=")), 303, false},
		{"urlencoded, one byte over the cap", urlenc, "pad=" + pad(1024-len("pad=")+1), 400, false},
		{"urlencoded, 2 KiB", urlenc, "from=admin&pad=" + pad(2<<10), 400, false},
		// Multipart: read as a form, and checked the same way.
		{"multipart, from=admin", multipt, part("from", "admin") + endPart, 303, true},
		{"multipart alias: other case, quoted boundary", `Multipart/Form-Data; boundary="b"`, part("from", "admin") + endPart, 303, true},
		{"multipart, another from", multipt, part("from", "bogus") + endPart, 400, false},
		{"multipart, from repeated", multipt, part("from", "admin") + part("from", "admin") + endPart, 400, false},
		{"multipart, an unknown field", multipt, part("other", "1") + endPart, 303, false},
		{"multipart, no field at all", multipt, endPart, 303, false},
		{"multipart, a file part", multipt, "--b\r\nContent-Disposition: form-data; name=\"f\"; filename=\"f.txt\"\r\nContent-Type: text/plain\r\n\r\nx\r\n" + endPart, 400, false},
		{"multipart, no boundary parameter", "multipart/form-data", part("from", "admin") + endPart, 400, false},
		{"multipart, never closed", multipt, part("from", "admin"), 400, false},
		{"multipart, over the cap", multipt, part("pad", pad(2<<10)) + endPart, 400, false},
		// Encodings that are not forms: unread, so refused unless empty.
		{"text/plain, empty", "text/plain", "", 303, false},
		{"text/plain, from=admin", "text/plain", "from=admin", 400, false},
		{"text/plain with a charset, from=admin", "text/plain; charset=utf-8", "from=admin", 400, false},
		{"text/plain, 2 KiB", "text/plain", pad(2 << 10), 400, false},
		{"JSON, empty", "application/json", "", 303, false},
		{"JSON, an empty object", "application/json", "{}", 400, false},
		{"JSON, another from", "application/json", `{"from":"bogus"}`, 400, false},
		{"JSON, 2 KiB", "application/json", `{"pad":"` + pad(2<<10) + `"}`, 400, false},
		{"octet-stream, a byte", "application/octet-stream", "x", 400, false},
		{"no type, empty", "", "", 303, false},
		{"no type, from=admin", "", "from=admin", 400, false},
		{"no type, 2 KiB", "", pad(2 << 10), 400, false},
		{"a type that does not parse", ";;;", "x", 400, false},
	}
}

func TestCommentRemoveBodyShapes(t *testing.T) {
	h, owner, reader := commentWorld(t)
	for _, c := range removeBodyRows() {
		id := h.say(reader, "x", "a comment for "+c.name, 0)
		res := h.serve("POST", "/admin/comments/"+itoa(id)+"/remove", h.withSID(owner), withBody(c.ctype, []byte(c.body)))
		removed := h.commentCount("id = ? AND removed_at IS NOT NULL", id) == 1
		if res.Status != c.want {
			t.Errorf("%s: answered %d %q, want %d", c.name, res.Status, res.Body, c.want)
		}
		if removed != (c.want == http.StatusSeeOther) {
			t.Errorf("%s: removed = %v for a request that is answered %d", c.name, removed, c.want)
		}
		if c.want == http.StatusSeeOther {
			loc := "/reports/x#comments"
			if c.admin {
				loc = "/admin/reports/x#comments"
			}
			if res.Location() != loc {
				t.Errorf("%s: Location %q, want %q", c.name, res.Location(), loc)
			}
		} else if res.Location() != "" {
			t.Errorf("%s: a refusal with Location %q", c.name, res.Location())
		}
	}
	// The query string is not the form, whatever the body is: it neither lands the
	// owner in the admin page nor makes a refused body readable.
	id := h.say(reader, "x", "a comment for the query string", 0)
	res := h.serve("POST", "/admin/comments/"+itoa(id)+"/remove?from=admin", h.withSID(owner), withBody("text/plain", []byte("x")))
	if res.Status != http.StatusBadRequest {
		t.Errorf("a query-string from beside an unread body: %d, want 400", res.Status)
	}
}

// TestRemoveBodyShapesCoverEveryClass keeps the corpus honest about its own
// reach: it holds a refusal and an acceptance for each family of encoding, so a
// handler that stopped reading one family could not pass by accident.
func TestRemoveBodyShapesCoverEveryClass(t *testing.T) {
	families := map[string]*regexp.Regexp{
		"urlencoded":   regexp.MustCompile(`^urlencoded`),
		"multipart":    regexp.MustCompile(`^multipart`),
		"text/plain":   regexp.MustCompile(`^text/plain`),
		"JSON":         regexp.MustCompile(`^JSON`),
		"no type":      regexp.MustCompile(`^no type`),
		"other binary": regexp.MustCompile(`^octet-stream|^a type that`),
	}
	refused, accepted := map[string]bool{}, map[string]bool{}
	for _, c := range removeBodyRows() {
		for fam, re := range families {
			if re.MatchString(c.name) {
				if c.want == http.StatusBadRequest {
					refused[fam] = true
				} else {
					accepted[fam] = true
				}
			}
		}
	}
	for fam := range families {
		if !refused[fam] {
			t.Errorf("the corpus has no refused body of the family %q", fam)
		}
	}
	for _, fam := range []string{"urlencoded", "multipart", "text/plain", "JSON", "no type"} {
		if !accepted[fam] {
			t.Errorf("the corpus has no accepted (empty or well-formed) body of the family %q", fam)
		}
	}
}

// TestPostRoutesAreClassified is the census of the owner's POST routes: a route
// that reads a form must say how its fields work, so a new one cannot join the
// table without someone deciding what a body it does not read does. A route with
// a required field answers 400 to an unread body already (it finds no field); a
// route with no field reads nothing and cannot be steered by a body; a route
// whose every field is optional is the class, and is in the corpus above.
func TestPostRoutesAreClassified(t *testing.T) {
	classes := map[string]string{
		"/admin/reports":                  "required: the upload's file",
		"/admin/reports/{slug}/publish":   "no field",
		"/admin/reports/{slug}/unpublish": "no field",
		"/admin/reports/{slug}/delete":    "no field",
		"/admin/feedback/{n}/reply":       "required: body",
		"/admin/comments/{n}/remove":      "optional: from (removeBodyRows)",
	}
	var seen []string
	for _, rt := range adminRoutes(nil) {
		if rt.Method != "POST" {
			continue
		}
		seen = append(seen, rt.Pattern)
		if classes[rt.Pattern] == "" {
			t.Errorf("POST %s has no entry in the census: say whether its fields are required, absent or optional", rt.Pattern)
		}
	}
	sort.Strings(seen)
	for pattern := range classes {
		if i := sort.SearchStrings(seen, pattern); i >= len(seen) || seen[i] != pattern {
			t.Errorf("the census names POST %s, which is not an owner route any more", pattern)
		}
	}
}
