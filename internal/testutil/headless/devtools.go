package headless

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sshubham2/hband/internal/testutil/outside"
)

// The DevTools driver: a Chrome of the test's own, driven over the DevTools
// protocol by four verbs (Call, Viewport, Navigate, Eval). It exists because the
// command-line screenshot (Shot) cannot go below a window of about 492 CSS px and
// cannot click. It reads no environment: the browser is exactly the one Browser
// returns, which is CHROME_BIN and nothing else.

const (
	portWait    = 15 * time.Second
	callTimeout = 30 * time.Second
	closeWait   = 10 * time.Second
)

// Tab is one page of a headless Chrome started by Open.
type Tab struct {
	t   testing.TB
	ws  *wsConn
	cmd *exec.Cmd

	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcReply
	closed  chan struct{} // closed when the reader stops
	loads   chan struct{} // one value per Page.loadEventFired
}

type rpcReply struct {
	result json.RawMessage
	err    string
}

type rpcMessage struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Open starts the browser Browser(t) returns, with a fresh profile, and connects
// to its page. Where Browser skips the test (no usable CHROME_BIN, or Edge) the
// test has ended; with a recording TB, which Skip does not end, Open returns nil
// and has started nothing. The browser is closed when the test is done.
func Open(t testing.TB) *Tab {
	t.Helper()
	browser := Browser(t)
	if browser == "" {
		return nil
	}
	profile, err := os.MkdirTemp("", "hband-headless-profile-*")
	if err != nil {
		t.Fatalf("headless: profile folder: %v", err)
		return nil
	}
	t.Cleanup(func() { removeLoud(t, profile) })
	outside.MustBeOutside(t, profile)

	cmd := exec.Command(browser,
		"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--disable-extensions",
		"--no-proxy-server", "--remote-debugging-port=0", "--user-data-dir="+profile, "about:blank")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("headless: start the browser: %v", err)
		return nil
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	tab := &Tab{t: t, cmd: cmd, pending: map[int]chan rpcReply{}, closed: make(chan struct{}), loads: make(chan struct{}, 32)}
	t.Cleanup(func() { tab.shutdown(exited) })

	port, pageURL := "", ""
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: 5 * time.Second}
	for deadline := time.Now().Add(portWait); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		select {
		case <-exited:
			t.Fatalf("headless: the browser exited before it opened a DevTools port:\n%s", tail(stderr.String()))
			return nil
		default:
		}
		if port == "" {
			data, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
			if err != nil || !bytes.Contains(data, []byte("\n")) {
				continue
			}
			port = strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
		}
		resp, err := client.Get("http://127.0.0.1:" + port + "/json/list")
		if err != nil {
			continue
		}
		var targets []struct {
			Type string `json:"type"`
			WS   string `json:"webSocketDebuggerUrl"`
		}
		err = json.NewDecoder(resp.Body).Decode(&targets)
		resp.Body.Close()
		if err != nil {
			continue
		}
		for _, tg := range targets {
			if tg.Type == "page" && tg.WS != "" {
				pageURL = tg.WS
				break
			}
		}
		if pageURL != "" {
			break
		}
	}
	// ported:E42-318,E42-319,E42-321,E42-326
	if pageURL == "" {
		t.Fatalf("headless: no DevTools page target within %s (port %q)", portWait, port)
		return nil
	}
	ws, err := dialWS(pageURL)
	if err != nil {
		t.Fatalf("headless: DevTools connection: %v", err)
		return nil
	}
	tab.ws = ws
	go tab.read()
	tab.Call("Page.enable", nil)
	return tab
}

// read routes the browser's messages: replies to the call that waits for them,
// and the load events Navigate waits for.
func (tab *Tab) read() {
	defer close(tab.closed)
	for {
		raw, err := tab.ws.readMessage()
		if err != nil {
			return
		}
		var m rpcMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		switch {
		case m.ID != 0:
			tab.mu.Lock()
			ch := tab.pending[m.ID]
			delete(tab.pending, m.ID)
			tab.mu.Unlock()
			if ch != nil {
				r := rpcReply{result: m.Result}
				if m.Error != nil {
					r.err = m.Error.Message
				}
				ch <- r
			}
		case m.Method == "Page.loadEventFired":
			select {
			case tab.loads <- struct{}{}:
			default:
			}
		}
	}
}

// shutdown closes the browser, then kills it if it has not gone in closeWait.
func (tab *Tab) shutdown(exited chan struct{}) {
	if tab.ws != nil {
		if b, err := json.Marshal(map[string]any{"id": 1 << 20, "method": "Browser.close"}); err == nil {
			tab.ws.writeText(b)
		}
	}
	select {
	case <-exited:
	case <-time.After(closeWait):
		tab.cmd.Process.Kill()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
		}
	}
	if tab.ws != nil {
		tab.ws.close()
	}
}

// Call sends one DevTools command and returns its result. It fails the test on a
// protocol error, and when there is no reply in 30 s; the message names the method.
func (tab *Tab) Call(method string, params any) json.RawMessage {
	tab.t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	tab.mu.Lock()
	tab.nextID++
	id := tab.nextID
	ch := make(chan rpcReply, 1)
	tab.pending[id] = ch
	tab.mu.Unlock()
	msg, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		tab.t.Fatalf("headless: %s: %v", method, err)
		return nil
	}
	if err := tab.ws.writeText(msg); err != nil {
		tab.t.Fatalf("headless: %s: %v", method, err)
		return nil
	}
	select {
	case r := <-ch:
		// ported:E42-362
		if r.err != "" {
			tab.t.Fatalf("headless: %s: %s", method, r.err)
			return nil
		}
		return r.result
	case <-tab.closed:
		// ported:E42-338
		tab.t.Fatalf("headless: %s: the browser closed DevTools", method)
	case <-time.After(callTimeout):
		tab.t.Fatalf("headless: %s: no reply within %s", method, callTimeout)
	}
	return nil
}

// Viewport emulates a screen of w by h CSS px, below the window floor of the
// command line: a mobile one (device scale factor 2) or a desktop one (1).
func (tab *Tab) Viewport(w, h int, mobile bool) {
	tab.t.Helper()
	scale := 1
	if mobile {
		scale = 2
	}
	tab.Call("Emulation.setDeviceMetricsOverride", map[string]any{"width": w, "height": h, "deviceScaleFactor": scale, "mobile": mobile})
}

// drainLoads forgets the load events that are already waiting, so that the next
// wait is for a load that has not happened yet.
func (tab *Tab) drainLoads() {
	for drained := false; !drained; {
		select {
		case <-tab.loads:
		default:
			drained = true
		}
	}
}

// Submit runs expr, which must start a navigation of the page (a form's submit(),
// say), and waits for the document it lands on to load. It is how a test drives a
// form POST with its redirects: polling with Eval across the navigation would race
// the old document's teardown and fail on a destroyed execution context. The
// expression is evaluated before the wait begins, so it must not itself wait.
func (tab *Tab) Submit(expr string) {
	tab.t.Helper()
	tab.drainLoads()
	tab.Eval(expr, nil)
	select {
	case <-tab.loads:
	case <-tab.closed:
		tab.t.Fatalf("headless: the browser closed DevTools before the page the form led to loaded")
	case <-time.After(callTimeout):
		tab.t.Fatalf("headless: the page the form led to did not finish loading within %s", callTimeout)
	}
}

// Navigate loads url and waits for its load event.
func (tab *Tab) Navigate(url string) {
	tab.t.Helper()
	tab.drainLoads()
	var res struct {
		ErrorText string `json:"errorText"`
	}
	// ported:E42-378
	if err := json.Unmarshal(tab.Call("Page.navigate", map[string]any{"url": url}), &res); err != nil {
		tab.t.Fatalf("headless: Page.navigate %s: %v", url, err)
		return
	}
	if res.ErrorText != "" {
		tab.t.Fatalf("headless: Page.navigate %s: %s", url, res.ErrorText)
		return
	}
	select {
	case <-tab.loads:
	case <-tab.closed:
		// ported:E42-380,E42-383
		tab.t.Fatalf("headless: %s: the browser closed DevTools before it loaded", url)
	case <-time.After(callTimeout):
		tab.t.Fatalf("headless: %s did not finish loading within %s", url, callTimeout)
	}
}

// Eval runs expr in the page (a promise is awaited) and unmarshals its value into
// out, when out is not nil. It fails the test when the page's JavaScript throws.
func (tab *Tab) Eval(expr string, out any) {
	tab.t.Helper()
	var res struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	// ported:E42-373
	raw := tab.Call("Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true})
	if err := json.Unmarshal(raw, &res); err != nil {
		tab.t.Fatalf("headless: Runtime.evaluate: %v", err)
		return
	}
	// ported:E42-374
	if res.Exception != nil {
		tab.t.Fatalf("headless: the page's JavaScript failed: %s %s", res.Exception.Text, res.Exception.Exception.Description)
		return
	}
	if out != nil {
		if err := json.Unmarshal(res.Result.Value, out); err != nil {
			tab.t.Fatalf("headless: Runtime.evaluate result %s into %T: %v", strconv.Quote(string(res.Result.Value)), out, err)
		}
	}
}
