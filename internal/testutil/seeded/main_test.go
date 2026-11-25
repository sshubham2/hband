package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sshubham2/hband/internal/testutil/outside"
	"github.com/sshubham2/hband/internal/testutil/program"
)

// stopped is what recT.Fatalf panics with, as testing's Fatalf stops its caller.
type stopped struct{}

// recT is a program.T that records what it is told. Fatalf notes the message
// and stops the caller.
type recT struct {
	events   *[]string
	cleanups []func()
}

func newRecT(events *[]string) *recT { return &recT{events: events} }

func (r *recT) Helper()                         {}
func (r *recT) Logf(format string, args ...any) { r.note("log:" + fmt.Sprintf(format, args...)) }
func (r *recT) Cleanup(f func())                { r.cleanups = append(r.cleanups, f) }
func (r *recT) note(s string)                   { *r.events = append(*r.events, s) }
func (r *recT) Fatalf(format string, args ...any) {
	r.note("fatal:" + fmt.Sprintf(format, args...))
	panic(stopped{})
}

// runCleanups runs the cleanups newest first, as testing does.
func (r *recT) runCleanups() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}

// catch calls fn and reports whether it was stopped by a Fatalf.
func catch(fn func()) (fatal bool) {
	defer func() {
		if v := recover(); v != nil {
			if _, ok := v.(stopped); !ok {
				panic(v)
			}
			fatal = true
		}
	}()
	fn()
	return false
}

func testPort(t *testing.T) int {
	t.Helper()
	p, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var (
	branchRE  = regexp.MustCompile(`href="(/reports/e004-look/comments/[0-9]+)"`)
	countRE   = regexp.MustCompile(`class="comment-count">([0-9]+ comments?)<`)
	commentRE = regexp.MustCompile(`id="comment-([0-9]+)"`)
)

// page is one answer.
type page struct {
	status int
	body   string
}

func fetch(t *testing.T, addr, path string) page {
	t.Helper()
	c := &http.Client{
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       20 * time.Second,
	}
	resp, err := c.Get("http://" + addr + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return page{resp.StatusCode, string(b)}
}

// served is what a reader sees of the seeded product.
type served struct {
	ReportStatus int
	Continue     bool
	Branch       string
	BranchStatus int
	RawStatus    int
	RawBytes     int
	RawSum       [32]byte
	HomeFrame    bool
	Listed       bool
	Count        string
	IDs          []string // the comment ids of the report page and the branch page together
}

func gather(t *testing.T, addr string) served {
	t.Helper()
	var s served
	rep := fetch(t, addr, "/reports/e004-look")
	s.ReportStatus = rep.status
	s.Continue = strings.Contains(rep.body, "continue this thread")
	if m := branchRE.FindStringSubmatch(rep.body); m != nil {
		s.Branch = m[1]
		br := fetch(t, addr, m[1])
		s.BranchStatus = br.status
		for _, b := range []string{rep.body, br.body} {
			for _, id := range commentRE.FindAllStringSubmatch(b, -1) {
				s.IDs = append(s.IDs, id[1])
			}
		}
	}
	if m := countRE.FindStringSubmatch(rep.body); m != nil {
		s.Count = m[1]
	}
	raw := fetch(t, addr, "/raw/e004-look")
	s.RawStatus, s.RawBytes, s.RawSum = raw.status, len(raw.body), sha256.Sum256([]byte(raw.body))
	s.HomeFrame = strings.Contains(fetch(t, addr, "/").body, `src="/raw/e004-look"`)
	s.Listed = strings.Contains(fetch(t, addr, "/reports").body, `href="/reports/e004-look"`)
	set := map[string]bool{}
	var ids []string
	for _, id := range s.IDs {
		if !set[id] {
			set[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	s.IDs = ids
	return s
}

// wantSeeded fails t unless s is the whole nine-level thread.
func wantSeeded(t *testing.T, s served) {
	t.Helper()
	if s.ReportStatus != 200 || !s.Continue || s.Branch == "" {
		t.Errorf("report page: status %d, 'continue this thread' %v, branch link %q", s.ReportStatus, s.Continue, s.Branch)
	}
	if s.BranchStatus != 200 {
		t.Errorf("branch page %s answered %d", s.Branch, s.BranchStatus)
	}
	if s.RawStatus != 200 || s.RawBytes == 0 {
		t.Errorf("/raw/e004-look answered %d with %d bytes", s.RawStatus, s.RawBytes)
	}
	if !s.HomeFrame {
		t.Errorf("GET / does not show the report in its frame")
	}
	if !s.Listed {
		t.Errorf("/reports does not link e004-look")
	}
	if s.Count != "9 comments" {
		t.Errorf("the printed count reads %q, want 9 comments", s.Count)
	}
	if want := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}; !reflect.DeepEqual(s.IDs, want) {
		t.Errorf("comment ids across the report page and the branch page are %v, want %v", s.IDs, want)
	}
}

// TestPortOpensOnlyOnceTheThreadIsSeeded is the torn read of spike 208: a
// consumer that connects to the port the moment it opens (the engine's look, and
// line 7's first GET) must already get the nine-level thread.
func TestPortOpensOnlyOnceTheThreadIsSeeded(t *testing.T) {
	n := testPort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", n)

	type first struct {
		page page
		err  error
	}
	got := make(chan first, 1)
	quit := make(chan struct{})
	t.Cleanup(func() { close(quit) })
	go func() {
		for {
			select {
			case <-quit:
				return
			default:
			}
			conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
			if err != nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			// The very first connect asks at once, on that connection.
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(15 * time.Second))
			req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/reports/e004-look", nil)
			req.Header.Set("Connection", "close")
			if err := req.Write(conn); err != nil {
				got <- first{err: err}
				return
			}
			resp, err := http.ReadResponse(bufio.NewReader(conn), req)
			if err != nil {
				got <- first{err: err}
				return
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(resp.Body)
			got <- first{page{resp.StatusCode, string(b)}, err}
			return
		}
	}()

	var out bytes.Buffer
	run(t, n, &out)

	select {
	case f := <-got:
		if f.err != nil {
			t.Fatalf("the first connect to port %d: %v", n, f.err)
		}
		if f.page.status != 200 {
			t.Errorf("the first answer on port %d to GET /reports/e004-look was %d: the port opened before the product was seeded", n, f.page.status)
		}
		if !strings.Contains(f.page.body, "continue this thread") || !branchRE.MatchString(f.page.body) {
			t.Errorf("the first answer on port %d lacks the 'continue this thread' link and its branch: the port opened before the thread was seeded:\n%.600s", n, f.page.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("nothing connected to port %d", n)
	}

	wantSeeded(t, gather(t, addr))
	if want := fmt.Sprintf("serving http://127.0.0.1:%d/\n", n); out.String() != want {
		t.Errorf("the ready line is %q, want %q", out.String(), want)
	}
}

// TestSeededProgramOpensAKilledWritersDataFolder runs the cut-over step by
// step: a program is seeded and killed (Process.Kill, which is TerminateProcess
// on Windows, leaving its -wal and -shm files behind), then a program on another
// port opens the same data folder. It must serve exactly what the killed one
// served.
func TestSeededProgramOpensAKilledWritersDataFolder(t *testing.T) {
	bin := program.Build(t)
	data := t.TempDir()
	outside.MustBeOutside(t, data)
	env := map[string]string{"HBAND_DATA_DIR": data}
	p, n := testPort(t), testPort(t)
	for n == p {
		n = testPort(t)
	}

	var baseline served
	t.Run("staging", func(t *testing.T) {
		s := program.Start(t, bin, program.Options{Port: p, Env: env})
		program.DeepThread(t, s)
		baseline = gather(t, s.Base[len("http://"):])
		wantSeeded(t, baseline)
	})
	// The subtest's cleanups have killed the staging program and waited for it.
	if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p), time.Second); err == nil {
		conn.Close()
		t.Fatalf("the staging program still answers on port %d", p)
	}
	wal, err := os.Stat(filepath.Join(data, "hband.db-wal"))
	if _, shmErr := os.Stat(filepath.Join(data, "hband.db-shm")); err != nil || wal.Size() == 0 || shmErr != nil {
		t.Skipf("the killed program left no -wal and -shm behind (wal %v, shm %v): this run did not open a killed writer's folder", err, shmErr)
	}

	final := program.Start(t, bin, program.Options{Port: n, Env: env})
	got := gather(t, final.Addr)
	wantSeeded(t, got)
	if !reflect.DeepEqual(got, baseline) {
		t.Errorf("the program that opened the killed writer's data folder serves\n%+v\nwhat the killed one served was\n%+v", got, baseline)
	}
}

func TestScopeEndsOnlyItsOwnCleanupsNewestFirst(t *testing.T) {
	var events []string
	parent := newRecT(&events)
	parent.Cleanup(func() { events = append(events, "parent-cleanup") })
	s := newScope(parent)
	s.Cleanup(func() { events = append(events, "A") })
	s.Cleanup(func() { events = append(events, "B") })
	s.Cleanup(func() { events = append(events, "C") })
	s.Logf("staging %d", 1)

	s.end()
	if want := []string{"log:staging 1", "C", "B", "A"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("after end(): %v, want %v (the parent's cleanup must not run, the scope's run newest first)", events, want)
	}
	s.end()
	if len(events) != 4 {
		t.Errorf("a second end() ran something: %v", events)
	}
	parent.runCleanups()
	if events[len(events)-1] != "parent-cleanup" || len(events) != 5 {
		t.Errorf("the parent's own cleanup was lost: %v", events)
	}
}

func TestFatalfRunsCleanupsBeforeExit(t *testing.T) {
	t.Run("scope ends before the parent's Fatalf", func(t *testing.T) {
		var events []string
		parent := newRecT(&events)
		parent.Cleanup(func() { events = append(events, "parent-cleanup") })
		s := newScope(parent)
		s.Cleanup(func() { events = append(events, "remove-data") })
		s.Cleanup(func() { events = append(events, "kill") })
		if !catch(func() { s.Fatalf("boom %d", 7) }) {
			t.Fatal("scope.Fatalf returned")
		}
		if want := []string{"kill", "remove-data", "fatal:boom 7"}; !reflect.DeepEqual(events, want) {
			t.Errorf("%v, want %v", events, want)
		}
		if len(parent.cleanups) != 1 {
			t.Errorf("the scope touched the parent's cleanups")
		}
	})

	recordExit := func(t *testing.T, codes *[]int) {
		old := exit
		exit = func(code int) { *codes = append(*codes, code) }
		t.Cleanup(func() { exit = old })
	}

	t.Run("the process runs its cleanups newest first, once, then exits 1", func(t *testing.T) {
		var codes []int
		recordExit(t, &codes)
		var errw bytes.Buffer
		var events []string
		p := newProcessT(&errw)
		p.Cleanup(func() { events = append(events, "remove-bin") })
		p.Cleanup(func() { events = append(events, "kill") })
		p.Fatalf("boom %d", 7)
		p.runCleanups() // a later run of the list does nothing
		if want := []string{"kill", "remove-bin"}; !reflect.DeepEqual(events, want) {
			t.Errorf("cleanups ran %v, want %v", events, want)
		}
		if !reflect.DeepEqual(codes, []int{1}) {
			t.Errorf("exit codes %v, want [1]", codes)
		}
		if errw.String() != "seeded: boom 7\n" {
			t.Errorf("stderr holds %q", errw.String())
		}
	})

	t.Run("a Fatalf from inside a cleanup exits at once and does not run the list again", func(t *testing.T) {
		var codes []int
		recordExit(t, &codes)
		var errw bytes.Buffer
		runs := map[string]int{}
		p := newProcessT(&errw)
		p.Cleanup(func() { runs["first"]++ })
		p.Cleanup(func() { runs["failing"]++; p.Fatalf("again") })
		p.Cleanup(func() { runs["last"]++ })
		p.Fatalf("boom")
		for _, k := range []string{"first", "failing", "last"} {
			if runs[k] != 1 {
				t.Errorf("cleanup %s ran %d times, want once", k, runs[k])
			}
		}
		if !reflect.DeepEqual(codes, []int{1, 1}) {
			t.Errorf("exit codes %v, want the inner Fatalf's 1 and the outer's 1", codes)
		}
		if !strings.Contains(errw.String(), "seeded: again") {
			t.Errorf("stderr lacks the inner message: %q", errw.String())
		}
	})
}

func TestPortFlagRefusesBadValues(t *testing.T) {
	bad := [][]string{
		nil,
		{},
		{"-port", "0"},
		{"-port", "-1"},
		{"-port", "65536"},
		{"-port", "99999999999999999999"},
		{"-port", "abc"},
		{"-port", "80x"},
		{"-port", "0x1F90"},
		{"-port", "0o17"},
		{"-port", "0b1"},
		{"-port", "1_000"},
		{"-port", "+80"},
		{"-port", "٨٠"},
		{"-port", ""},
		{"-port"},
		{"-port", "8080", "extra"},
		{"8080"},
		{"-nonsense"},
	}
	for _, args := range bad {
		var w bytes.Buffer
		if got, err := parsePort(args, &w); err == nil {
			t.Errorf("parsePort(%q) = %d, want a refusal", args, got)
		} else if !strings.Contains(w.String(), "Usage") {
			t.Errorf("parsePort(%q) refused without the usage: %q", args, w.String())
		}
	}
	for _, c := range []struct {
		args []string
		want int
	}{
		{[]string{"-port", "1"}, 1},
		{[]string{"-port", "65535"}, 65535},
		{[]string{"-port=3000"}, 3000},
		{[]string{"--port", "4000"}, 4000},
		{[]string{"-port", "010"}, 10},
		{[]string{"-port", "08080"}, 8080},
	} {
		if got, err := parsePort(c.args, io.Discard); err != nil || got != c.want {
			t.Errorf("parsePort(%q) = %d, %v, want %d", c.args, got, err, c.want)
		}
	}

	// main exits 2 on a refusal and never serves.
	old, oldArgs, oldErr := exit, os.Args, os.Stderr
	t.Cleanup(func() { exit, os.Args, os.Stderr = old, oldArgs, oldErr })
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	os.Stderr = devnull
	var codes []int
	exit = func(code int) { codes = append(codes, code) }
	for _, args := range [][]string{{"seeded"}, {"seeded", "-port", "0"}, {"seeded", "-port", "65536"}, {"seeded", "-port", "abc"}} {
		codes = nil
		os.Args = args
		main()
		if !reflect.DeepEqual(codes, []int{2}) {
			t.Errorf("main with %q exited %v, want [2]", args[1:], codes)
		}
	}
}

func TestStandInPortIsReadFromItsURL(t *testing.T) {
	for _, c := range []struct {
		url  string
		want int
		fail bool
	}{
		{"http://127.0.0.1:54321", 54321, false},
		{"http://127.0.0.1:54321/", 54321, false},
		{"http://[::1]:80", 80, false},
		{"http://127.0.0.1", 0, true},
		{"http://[::1]", 0, true},
		{"", 0, true},
		{"http://127.0.0.1:five", 0, true},
		{"http://127.0.0.1:99999999999999999999", 0, true},
	} {
		got, err := portOf(c.url)
		if c.fail {
			if err == nil {
				t.Errorf("portOf(%q) = %d, want an error", c.url, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("portOf(%q) = %d, %v, want %d", c.url, got, err, c.want)
		}
	}
}

type bindRow struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Draws  []int  `json:"draws"`
	URL    string `json:"url"`
	Expect string `json:"expect"`
	Port   int    `json:"port"`
	Why    string `json:"why"`
}

// TestEphemeralBindsNeverReachTheServingPort feeds every row of
// testdata/ephemeral_binds.json: the ports the launcher draws itself before the
// serving program binds N. A staging port is never N, and a staging stand-in on N
// is refused, whatever shape its URL takes.
func TestEphemeralBindsNeverReachTheServingPort(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "ephemeral_binds.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Purpose string    `json:"purpose"`
		N       int       `json:"n"`
		Rows    []bindRow `json:"rows"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("corpus: %v", err)
	}
	if c.N <= 0 || c.Purpose == "" || len(c.Rows) < 10 {
		t.Fatalf("corpus: n %d, purpose %q, %d rows: it has been trimmed", c.N, c.Purpose, len(c.Rows))
	}
	oldPick := freePort
	t.Cleanup(func() { freePort = oldPick })

	ids := map[string]bool{}
	kinds := map[string]int{}
	for _, r := range c.Rows {
		if r.ID == "" || r.Why == "" || ids[r.ID] {
			t.Fatalf("corpus row %+v: it needs a unique id and a why", r)
		}
		ids[r.ID] = true
		kinds[r.Kind+"/"+r.Expect]++
		switch r.Kind {
		case "staging-port":
			next := 0
			freePort = func() (int, error) {
				if next >= len(r.Draws) {
					return 0, errors.New("the OS hands out no more ports")
				}
				next++
				return r.Draws[next-1], nil
			}
			got, err := stagingPort(c.N)
			switch r.Expect {
			case "port":
				if err != nil || got != r.Port {
					t.Errorf("%s: stagingPort = %d, %v, want %d (%s)", r.ID, got, err, r.Port, r.Why)
				}
			case "error":
				if err == nil {
					t.Errorf("%s: stagingPort = %d, want an error (%s)", r.ID, got, r.Why)
				}
			default:
				t.Fatalf("%s: expect %q", r.ID, r.Expect)
			}
		case "stand-in":
			var events []string
			rt := newRecT(&events)
			fatal := catch(func() { checkStandIn(rt, r.URL, c.N) })
			switch r.Expect {
			case "ok":
				if fatal {
					t.Errorf("%s: checkStandIn(%q) refused: %v (%s)", r.ID, r.URL, events, r.Why)
				}
			case "refuse":
				if !fatal {
					t.Errorf("%s: checkStandIn(%q) let it through (%s)", r.ID, r.URL, r.Why)
				}
			default:
				t.Fatalf("%s: expect %q", r.ID, r.Expect)
			}
		default:
			t.Fatalf("%s: kind %q", r.ID, r.Kind)
		}
	}
	for _, k := range []string{"staging-port/port", "staging-port/error", "stand-in/ok", "stand-in/refuse"} {
		if kinds[k] == 0 {
			t.Errorf("the corpus has no %s row", k)
		}
	}
}
