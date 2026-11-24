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
// comment POST), each with a fault that breaks that step alone. Every row proves
// the fault is isolated (the tables the earlier steps read still read, the one
// that is broken does not, or the seam was reached), then says what the request
// answers. The census at the end fails when a step has no row, so a new step
// cannot be added without one.
//
// The seam, a.tree, is the only way to fail the thread read alone after the
// comments table has been used by the write that precedes it.
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
	const outage = "Reports cannot be shown right now"
	get := func(path string) func(*harness, string, string) result {
		return func(h *harness, reader, top string) result {
			return h.serve("GET", strings.ReplaceAll(path, "{top}", top), h.withSID(reader))
		}
	}
	post := func(h *harness, reader, top string) result {
		return h.postComment(reader, "x", commentForm("written while one step of the request fails", 0))
	}

	type row struct {
		handler, step string
		fault         func(h *harness, calls *int)
		broken        []string // tables that must no longer read once the fault is in
		healthy       []string // tables the steps before it read, which must still read
		seam          bool     // the thread read is the step that fails: it must have been called
		req           func(h *harness, reader, top string) result
		status        int
		loc, body     string // the Location of a 303, or text of a page
		added         int    // comments stored by the request
	}
	rows := []row{
		// POST /reports/{slug}/comments
		{"postComment", "identify the writer", drop("sessions"), []string{"sessions"}, []string{"reports", "comments"}, false, post, 503, "", "Service unavailable", 0},
		{"postComment", "look up the report", rename("reports"), []string{"reports"}, []string{"sessions", "comments"}, false, post, 503, "", "Service unavailable", 0},
		{"postComment", "write the comment", rename("comments"), []string{"comments"}, []string{"sessions", "reports"}, false, post, 503, "", "Service unavailable", 0},
		{"postComment", "read the thread after the write", treeFails, nil, []string{"sessions", "reports", "comments"}, true, post, 303, "/reports/x#comments", "", 1},
		// GET /reports/{slug}
		{"readReport", "identify the viewer", drop("sessions"), []string{"sessions"}, []string{"reports", "comments", "votes"}, false, get("/reports/x"), 200, "", "Sign in with GitHub", 0},
		{"readReport", "look up the report", rename("reports"), []string{"reports"}, []string{"sessions", "comments", "votes"}, false, get("/reports/x"), 503, "", outage, 0},
		{"readReport", "read the votes", rename("votes"), []string{"votes"}, []string{"sessions", "reports", "comments"}, false, get("/reports/x"), 503, "", outage, 0},
		{"readReport", "read the comments", rename("comments"), []string{"comments"}, []string{"sessions", "reports", "votes"}, false, get("/reports/x"), 503, "", outage, 0},
		{"readReport", "read the comments", treeFails, nil, []string{"sessions", "reports", "votes", "comments"}, true, get("/reports/x"), 503, "", outage, 0},
		// GET /
		{"home", "look up the latest report", rename("reports"), []string{"reports"}, []string{"sessions", "comments", "votes"}, false, get("/"), 503, "", outage, 0},
		{"home", "read the votes", rename("votes"), []string{"votes"}, []string{"sessions", "reports", "comments"}, false, get("/"), 503, "", outage, 0},
		{"home", "read the comments", rename("comments"), []string{"comments"}, []string{"sessions", "reports", "votes"}, false, get("/"), 503, "", outage, 0},
		{"home", "read the comments", treeFails, nil, []string{"sessions", "reports", "votes", "comments"}, true, get("/"), 503, "", outage, 0},
		// GET /reports/{slug}/comments/{id}
		{"readBranch", "identify the viewer", drop("sessions"), []string{"sessions"}, []string{"reports", "comments"}, false, get("/reports/x/comments/{top}"), 200, "", "Sign in with GitHub", 0},
		{"readBranch", "look up the report", rename("reports"), []string{"reports"}, []string{"sessions", "comments"}, false, get("/reports/x/comments/{top}"), 503, "", outage, 0},
		{"readBranch", "read the comments", rename("comments"), []string{"comments"}, []string{"sessions", "reports"}, false, get("/reports/x/comments/{top}"), 503, "", outage, 0},
		{"readBranch", "read the comments", treeFails, nil, []string{"sessions", "reports", "comments"}, true, get("/reports/x/comments/{top}"), 503, "", outage, 0},
	}

	// The census: every step a handler takes against the store has a row.
	steps := map[string][]string{
		"postComment": {"identify the writer", "look up the report", "write the comment", "read the thread after the write"},
		"readReport":  {"identify the viewer", "look up the report", "read the votes", "read the comments"},
		"home":        {"look up the latest report", "read the votes", "read the comments"},
		"readBranch":  {"identify the viewer", "look up the report", "read the comments"},
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
			h, _, reader := commentWorld(t)
			top := h.say(reader, "x", "a comment that is on the page", 0)
			topID := strconv.FormatInt(top, 10)
			// Control: with nothing broken the request does what the row's request is for.
			calls := 0
			if ctl := r.req(h, reader, topID); ctl.Status != 200 && ctl.Status != 303 {
				t.Fatalf("control: %d %q", ctl.Status, ctl.Body)
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

			res := r.req(h, reader, topID)
			if res.Status != r.status {
				t.Errorf("answered %d %q, want %d", res.Status, res.Body, r.status)
			}
			if r.loc != "" && res.Location() != r.loc {
				t.Errorf("Location %q, want %q", res.Location(), r.loc)
			}
			if r.status == http.StatusServiceUnavailable && res.Location() != "" {
				t.Errorf("a 503 with a Location %q", res.Location())
			}
			if r.body != "" && !strings.Contains(res.Body, r.body) {
				t.Errorf("the answer lacks %q:\n%s", r.body, res.Body)
			}
			if r.seam {
				if calls == 0 {
					t.Error("the seam was never reached: an earlier step ended the request")
				}
			} else if len(r.broken) == 0 {
				t.Error("a row that breaks no table must break the seam")
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
		})
	}
}
