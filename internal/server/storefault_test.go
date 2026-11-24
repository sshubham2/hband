package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/sshubham2/hband/internal/comments"
)

// TestStoreFaultSteps is the corpus of one class of defect: a step of a handler
// that reads or writes the store has a failure path nobody can reach, because
// something earlier in the same request fails first (or the one fault a test can
// make breaks two steps at once). Such a path ships untested. The class is bounded
// by who produces its members: every store-touching step of the handlers that draw
// or answer a thread (the home page, the report page, the branch page and the
// comment POST) and of the two owner handlers that read or change one (the
// owner's report page, which lists the thread, and the comment removal), each with
// a fault that breaks that step alone. Every row proves the fault is isolated
// (the tables the earlier steps read still read, the one that is broken does not,
// the seam was reached, or, for a fault in a write, the write fails while every
// table still reads), then says what the request answers. The census at the end
// fails when a step has no row, so a new step cannot be added without one.
//
// The seam, a.tree, is the only way to fail the thread read alone after the
// comments table has been used by the write that precedes it.
//
// A write fault is the one fault a table rename cannot make: the lookup that
// precedes a write reads the same tables the write changes. The row installs a
// trigger that aborts an update of removed_at, then proves it is isolated by the
// reads that still work and by an update of its own that fails.
func TestStoreFaultSteps(t *testing.T) {
	treeFails := func(h *harness, calls *int) {
		h.app.tree = func(context.Context, int64) ([]*comments.Node, error) {
			*calls++
			return nil, errors.New("injected thread read failure")
		}
	}
	rename := func(table string) func(*harness, *int) {
		return func(h *harness, _ *int) { h.exec(`ALTER TABLE ` + table + ` RENAME TO ` + table + `_gone`) }
	}
	drop := func(table string) func(*harness, *int) {
		return func(h *harness, _ *int) { h.exec(`DROP TABLE ` + table) }
	}
	// removalFails aborts any update of removed_at: the comment can still be looked
	// up, and only the write of the removal fails.
	removalFails := func(h *harness, _ *int) {
		h.exec(`CREATE TRIGGER removal_fails BEFORE UPDATE OF removed_at ON comments BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	}

	// world is what a row's request is aimed at. The control request and the faulted
	// one are two requests, so an admin removal must not be aimed at the comment the
	// control already removed: target is top for the control and spare for the
	// request under the fault.
	type world struct {
		owner, reader string
		top, spare    string
		target        string
	}

	const outage = "Reports cannot be shown right now"
	const internal = "internal error"
	get := func(path string) func(*harness, world) result {
		return func(h *harness, w world) result {
			return h.serve("GET", strings.ReplaceAll(path, "{top}", w.top), h.withSID(w.reader))
		}
	}
	// counted is a signed-in reader's visit that the open counter would count: a
	// browser's User-Agent, from a visitor address a trusted proxy forwards.
	counted := func(path string) func(*harness, world) result {
		return func(h *harness, w world) result {
			h.trustPeer()
			return h.visit(path, "198.51.100.5", h.withSID(w.reader))
		}
	}
	post := func(h *harness, w world) result {
		return h.postComment(w.reader, "x", commentForm("written while one step of the request fails", 0))
	}
	adminGet := func(h *harness, w world) result {
		return h.serve("GET", "/admin/reports/x", h.withSID(w.owner))
	}
	remove := func(h *harness, w world) result {
		return h.serve("POST", "/admin/comments/"+w.target+"/remove", h.withSID(w.owner), withBody(formType, nil))
	}

	type row struct {
		handler, step string
		fault         func(h *harness, calls *int)
		broken        []string // tables that must no longer read once the fault is in
		healthy       []string // tables the steps before it read, which must still read
		seam          bool     // the thread read is the step that fails: it must have been called
		req           func(h *harness, w world) result
		status        int
		loc, body     string // the Location of a 303, or text of a page
		added         int    // comments stored by the request
		writeFault    bool   // the fault breaks a write and no read: the write must fail with the fault's own message
	}
	rows := []row{
		// POST /reports/{slug}/comments
		{"postComment", "identify the writer", drop("sessions"), []string{"sessions"}, []string{"reports", "comments"}, false, post, 503, "", "Service unavailable", 0, false},
		{"postComment", "look up the report", rename("reports"), []string{"reports"}, []string{"sessions", "comments"}, false, post, 503, "", "Service unavailable", 0, false},
		{"postComment", "write the comment", rename("comments"), []string{"comments"}, []string{"sessions", "reports"}, false, post, 503, "", "Service unavailable", 0, false},
		{"postComment", "read the thread after the write", treeFails, nil, []string{"sessions", "reports", "comments"}, true, post, 303, "/reports/x#comments", "", 1, false},
		// GET /reports/{slug}
		{"readReport", "identify the viewer", drop("sessions"), []string{"sessions"}, []string{"reports", "comments", "votes"}, false, get("/reports/x"), 200, "", "Sign in with GitHub", 0, false},
		{"readReport", "look up the report", rename("reports"), []string{"reports"}, []string{"sessions", "comments", "votes"}, false, get("/reports/x"), 503, "", outage, 0, false},
		{"readReport", "read the votes", rename("votes"), []string{"votes"}, []string{"sessions", "reports", "comments"}, false, get("/reports/x"), 503, "", outage, 0, false},
		{"readReport", "read the comments", rename("comments"), []string{"comments"}, []string{"sessions", "reports", "votes"}, false, get("/reports/x"), 503, "", outage, 0, false},
		{"readReport", "read the comments", treeFails, nil, []string{"sessions", "reports", "votes", "comments"}, true, get("/reports/x"), 503, "", outage, 0, false},
		// GET /
		{"home", "look up the latest report", rename("reports"), []string{"reports"}, []string{"sessions", "comments", "votes"}, false, get("/"), 503, "", outage, 0, false},
		{"home", "read the votes", rename("votes"), []string{"votes"}, []string{"sessions", "reports", "comments"}, false, get("/"), 503, "", outage, 0, false},
		{"home", "read the comments", rename("comments"), []string{"comments"}, []string{"sessions", "reports", "votes"}, false, get("/"), 503, "", outage, 0, false},
		{"home", "read the comments", treeFails, nil, []string{"sessions", "reports", "votes", "comments"}, true, get("/"), 503, "", outage, 0, false},
		// The open is counted at the end of a found report's page, after every read the
		// page is made of. A failure of that write alone must leave the page as it was.
		{"readReport", "write the open", rename("report_opens"), []string{"report_opens"}, []string{"sessions", "reports", "votes", "comments"}, false, counted("/reports/x"), 200, "", "Report X", 0, false},
		{"home", "write the open", rename("report_opens"), []string{"report_opens"}, []string{"sessions", "reports", "votes", "comments"}, false, counted("/"), 200, "", "Report X", 0, false},
		// GET /reports, the show-all page: four whole-table reads, each of which, failing
		// alone, makes the whole page an outage with no row and no zero in its place.
		{"showAll", "look up the reports", rename("reports"), []string{"reports"}, []string{"sessions", "votes", "comments", "report_opens"}, false, get("/reports"), 503, "", outage, 0, false},
		{"showAll", "read the likes", rename("votes"), []string{"votes"}, []string{"sessions", "reports", "comments", "report_opens"}, false, get("/reports"), 503, "", outage, 0, false},
		{"showAll", "read the visible comments", rename("comments"), []string{"comments"}, []string{"sessions", "reports", "votes", "report_opens"}, false, get("/reports"), 503, "", outage, 0, false},
		{"showAll", "read the opens", rename("report_opens"), []string{"report_opens"}, []string{"sessions", "reports", "votes", "comments"}, false, get("/reports"), 503, "", outage, 0, false},
		// GET /reports/{slug}/comments/{id}
		{"readBranch", "identify the viewer", drop("sessions"), []string{"sessions"}, []string{"reports", "comments"}, false, get("/reports/x/comments/{top}"), 200, "", "Sign in with GitHub", 0, false},
		{"readBranch", "look up the report", rename("reports"), []string{"reports"}, []string{"sessions", "comments"}, false, get("/reports/x/comments/{top}"), 503, "", outage, 0, false},
		{"readBranch", "read the comments", rename("comments"), []string{"comments"}, []string{"sessions", "reports"}, false, get("/reports/x/comments/{top}"), 503, "", outage, 0, false},
		{"readBranch", "read the comments", treeFails, nil, []string{"sessions", "reports", "comments"}, true, get("/reports/x/comments/{top}"), 503, "", outage, 0, false},
		// POST /admin/comments/{n}/remove, as the owner. A refused removal answers an
		// error and no Location, and the comment it was aimed at stays visible.
		{"adminCommentRemove", "identify the owner", drop("sessions"), []string{"sessions"}, []string{"reports", "comments"}, false, remove, 503, "", "", 0, false},
		{"adminCommentRemove", "look up the comment", rename("reports"), []string{"reports"}, []string{"sessions", "comments"}, false, remove, 500, "", internal, 0, false},
		{"adminCommentRemove", "remove the comment", removalFails, nil, []string{"sessions", "reports", "comments"}, false, remove, 500, "", internal, 0, true},
		// GET /admin/reports/{slug}, as the owner. The inbox is two reads, the messages
		// and then their replies, and the report holds one message so that both run.
		{"adminReport", "identify the owner", drop("sessions"), []string{"sessions"}, []string{"reports", "votes", "feedback", "feedback_replies", "comments"}, false, adminGet, 503, "", "", 0, false},
		{"adminReport", "look up the report", rename("reports"), []string{"reports"}, []string{"sessions", "votes", "feedback", "feedback_replies", "comments"}, false, adminGet, 500, "", internal, 0, false},
		{"adminReport", "read the votes", rename("votes"), []string{"votes"}, []string{"sessions", "reports", "feedback", "feedback_replies", "comments"}, false, adminGet, 500, "", internal, 0, false},
		{"adminReport", "read the feedback", rename("feedback"), []string{"feedback"}, []string{"sessions", "reports", "votes", "feedback_replies", "comments"}, false, adminGet, 500, "", internal, 0, false},
		{"adminReport", "read the feedback replies", rename("feedback_replies"), []string{"feedback_replies"}, []string{"sessions", "reports", "votes", "feedback", "comments"}, false, adminGet, 500, "", internal, 0, false},
		{"adminReport", "read the comments", treeFails, nil, []string{"sessions", "reports", "votes", "feedback", "feedback_replies", "comments"}, true, adminGet, 500, "", internal, 0, false},
	}

	// The census: every step a handler takes against the store has a row.
	steps := map[string][]string{
		"postComment":        {"identify the writer", "look up the report", "write the comment", "read the thread after the write"},
		"readReport":         {"identify the viewer", "look up the report", "read the votes", "read the comments", "write the open"},
		"home":               {"look up the latest report", "read the votes", "read the comments", "write the open"},
		"showAll":            {"look up the reports", "read the likes", "read the visible comments", "read the opens"},
		"readBranch":         {"identify the viewer", "look up the report", "read the comments"},
		"adminCommentRemove": {"identify the owner", "look up the comment", "remove the comment"},
		"adminReport":        {"identify the owner", "look up the report", "read the votes", "read the feedback", "read the feedback replies", "read the comments"},
	}
	have := map[string]bool{}
	for _, r := range rows {
		have[r.handler+": "+r.step] = true
	}
	var missing []string
	for handler, list := range steps {
		for _, step := range list {
			if !have[handler+": "+step] {
				missing = append(missing, handler+": "+step)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("steps with no row that fails them alone: %v", missing)
	}

	for i, r := range rows {
		t.Run(fmt.Sprintf("%02d %s: %s", i, r.handler, r.step), func(t *testing.T) {
			h, owner, reader := commentWorld(t)
			top := h.say(reader, "x", "a comment that is on the page", 0)
			spare := h.say(reader, "x", "a second comment, which an admin removal under the fault is aimed at", 0)
			// One message, so the owner's inbox has replies to read.
			h.sendFeedback(reader, "x", "a private message to the author")
			w := world{owner: owner, reader: reader, top: strconv.FormatInt(top, 10), spare: strconv.FormatInt(spare, 10)}
			w.target = w.top
			// Control: with nothing broken the request does what the row's request is for.
			calls := 0
			if ctl := r.req(h, w); ctl.Status != 200 && ctl.Status != 303 {
				t.Fatalf("control: %d %q", ctl.Status, ctl.Body)
			}
			if r.handler == "adminCommentRemove" {
				if h.commentCount("id = ? AND removed_at IS NOT NULL", top) != 1 {
					t.Fatal("control: the removal did not remove the comment it was aimed at")
				}
				if h.commentCount("id = ? AND removed_at IS NULL", spare) != 1 {
					t.Fatal("control: the removal reached the comment it was not aimed at")
				}
				w.target = w.spare
			}
			before := h.commentCount("1 = 1")

			r.fault(h, &calls)
			readable := func(table string) bool {
				_, err := h.store.DB().Exec(`SELECT count(*) FROM ` + table)
				return err == nil
			}
			for _, table := range r.healthy {
				if !readable(table) {
					t.Fatalf("the fault is not isolated: %s no longer reads, so an earlier step would fail first", table)
				}
			}
			for _, table := range r.broken {
				if readable(table) {
					t.Fatalf("the fault did not break %s", table)
				}
			}
			if r.writeFault {
				// Readability cannot show a fault in a write: an update of its own must fail.
				_, err := h.store.DB().Exec(`UPDATE comments SET removed_at = removed_at WHERE id = ?`, spare)
				if err == nil || !strings.Contains(err.Error(), "injected") {
					t.Fatalf("the fault did not break the write: %v", err)
				}
			}

			res := r.req(h, w)
			if res.Status != r.status {
				t.Errorf("answered %d %q, want %d", res.Status, res.Body, r.status)
			}
			if r.loc != "" && res.Location() != r.loc {
				t.Errorf("Location %q, want %q", res.Location(), r.loc)
			}
			if r.status >= http.StatusInternalServerError && res.Location() != "" {
				t.Errorf("a %d with a Location %q", r.status, res.Location())
			}
			if r.body != "" && !strings.Contains(res.Body, r.body) {
				t.Errorf("the answer lacks %q:\n%s", r.body, res.Body)
			}
			if r.seam {
				if calls == 0 {
					t.Error("the seam was never reached: an earlier step ended the request")
				}
			} else if len(r.broken) == 0 && !r.writeFault {
				t.Error("a row that breaks no table must break the seam or the write")
			}
			if r.added > 0 {
				if n := h.commentCount("1 = 1"); n != before+r.added {
					t.Errorf("%d comments stored by the request, want %d", n-before, r.added)
				}
			} else if r.handler == "postComment" && readable("comments") {
				if n := h.commentCount("1 = 1"); n != before {
					t.Errorf("%d comments stored by a request that was refused", n-before)
				}
			}
			if r.handler == "adminCommentRemove" && readable("comments") {
				// A removal that failed must not look like one that worked: the comment
				// it was aimed at is still visible.
				if h.commentCount("id = ? AND removed_at IS NULL", spare) != 1 {
					t.Error("the comment the failed removal was aimed at has been removed")
				}
			}
		})
	}
}
