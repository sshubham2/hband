package headless

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// serverFrame builds one unmasked server frame; lenMode forces how the length is
// written: 7, 16 or 64 bits (0 picks the shortest).
func serverFrame(op byte, fin bool, payload []byte, lenMode int) []byte {
	b0 := op
	if fin {
		b0 |= 0x80
	}
	out := []byte{b0}
	n := len(payload)
	switch {
	case lenMode == 64 || (lenMode == 0 && n >= 1<<16):
		var l [8]byte
		binary.BigEndian.PutUint64(l[:], uint64(n))
		out = append(append(out, 127), l[:]...)
	case lenMode == 16 || (lenMode == 0 && n >= 126):
		out = append(out, 126, byte(n>>8), byte(n))
	default:
		out = append(out, byte(n))
	}
	return append(out, payload...)
}

// The reader copes with every shape a browser's reply can take: payload lengths
// of 7, 16 and 64 bits; one message split across continuation frames with a ping
// between them (answered with a masked pong that carries the ping's payload); and
// the client's own frames are masked.
func TestDevToolsFrames(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 70000)
	mid := bytes.Repeat([]byte("m"), 300)
	cases := []struct {
		name string
		in   []byte
		want []byte
		pong string
	}{
		{"7-bit length", serverFrame(0x1, true, []byte(`{"id":1}`), 0), []byte(`{"id":1}`), ""},
		{"16-bit length", serverFrame(0x1, true, mid, 0), mid, ""},
		{"64-bit length, as a payload that needs it", serverFrame(0x1, true, big, 0), big, ""},
		{"64-bit length, written for a short payload", serverFrame(0x1, true, []byte("short"), 64), []byte("short"), ""},
		{"continuation frames with a ping between", bytes.Join([][]byte{
			serverFrame(0x1, false, []byte(`{"id":`), 0),
			serverFrame(0x9, true, []byte("are-you-there"), 0),
			serverFrame(0x0, false, []byte(`7,"res`), 0),
			serverFrame(0x0, true, []byte(`ult":{}}`), 0),
		}, nil), []byte(`{"id":7,"result":{}}`), "are-you-there"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clientSide, serverSide := net.Pipe()
			defer clientSide.Close()
			defer serverSide.Close()
			go func() { serverSide.Write(c.in) }()
			wrote := make(chan []byte, 4)
			go func() { // what the client writes back, frame by frame
				for {
					var head [2]byte
					if _, err := io.ReadFull(serverSide, head[:]); err != nil {
						return
					}
					n := int(head[1] & 0x7f)
					var key [4]byte
					io.ReadFull(serverSide, key[:])
					payload := make([]byte, n)
					io.ReadFull(serverSide, payload)
					for i := range payload {
						payload[i] ^= key[i%4]
					}
					if head[1]&0x80 == 0 {
						payload = []byte("UNMASKED")
					}
					wrote <- append([]byte{head[0] & 0x0f}, payload...)
				}
			}()
			got, err := readWSMessage(clientSide, func(op byte, payload []byte) error { return writeMaskedFrame(clientSide, op, payload) })
			if err != nil {
				t.Fatalf("readWSMessage: %v", err)
			}
			if !bytes.Equal(got, c.want) {
				t.Fatalf("read %d bytes, want %d: %.60q", len(got), len(c.want), got)
			}
			if c.pong != "" {
				select {
				case w := <-wrote:
					if w[0] != 0xA || string(w[1:]) != c.pong {
						t.Fatalf("answered a ping with op %#x payload %q, want a masked pong of %q", w[0], w[1:], c.pong)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("a ping was not answered")
				}
			}
		})
	}
	t.Run("a client frame is masked", func(t *testing.T) {
		var buf bytes.Buffer
		if err := writeMaskedFrame(&buf, 0x1, []byte(`{"id":1}`)); err != nil {
			t.Fatal(err)
		}
		b := buf.Bytes()
		if b[0] != 0x81 || b[1]&0x80 == 0 || int(b[1]&0x7f) != 8 {
			t.Fatalf("frame header % x: want a final text frame with the mask bit and length 8", b[:2])
		}
		var plain [8]byte
		for i := range plain {
			plain[i] = b[6+i] ^ b[2+i%4]
		}
		if string(plain[:]) != `{"id":1}` {
			t.Fatalf("unmasked payload %q", plain[:])
		}
	})
}

// Open defers entirely to Browser's skip: where CHROME_BIN gives no usable
// browser it starts nothing and returns no Tab, and the skip names CHROME_BIN.
func TestOpenSkipsWhereBrowserSkips(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name  string
		bin   string
		unset bool
	}{
		{name: "unset", unset: true},
		{name: "empty", bin: ""},
		{name: "a folder", bin: dir},
		{name: "a file called msedge.exe", bin: fileIn(t, "msedge.exe")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setEnv(t, "CHROME_BIN", c.bin, c.unset)
			r := &recorder{TB: t}
			if tab := Open(r); tab != nil {
				t.Fatal("Open returned a Tab where Browser skips")
			}
			if !r.skipped {
				t.Fatal("Open did not skip")
			}
			if !contains(r.msg, "CHROME_BIN") {
				t.Fatalf("the skip message does not name CHROME_BIN: %q", r.msg)
			}
		})
	}
}

func contains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }

// Emulation takes: a page that fits reads the emulated width below the command
// line's window floor, a page with a wider element reads more (so the
// measurement can fail), and a button wired by a script from the site's own path,
// under a CSP that allows only 'self', can be clicked and read back.
func TestTabEmulatesNarrowViewport(t *testing.T) {
	tab := Open(t)
	mux := http.NewServeMux()
	page := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "script-src 'self'")
		io := `<!doctype html><meta name="viewport" content="width=device-width, initial-scale=1"><body style="margin:0">` + body
		w.Write([]byte(io))
	}
	mux.HandleFunc("/fit", func(w http.ResponseWriter, r *http.Request) { page(w, `<p>it fits</p>`) })
	mux.HandleFunc("/wide", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<div style="width:600px;height:20px;background:#ccc">wide</div>`)
	})
	mux.HandleFunc("/toggle", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<button id="b" aria-expanded="true">Hide</button><div id="r">replies</div><script src="/t.js"></script>`)
	})
	mux.HandleFunc("/t.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		w.Write([]byte(`document.getElementById("b").addEventListener("click", function () {
  var r = document.getElementById("r"); r.hidden = !r.hidden;
  this.setAttribute("aria-expanded", r.hidden ? "false" : "true");
});`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	width := func(path string) float64 {
		tab.Navigate(srv.URL + path)
		var w float64
		tab.Eval(`document.documentElement.scrollWidth`, &w)
		return w
	}
	for _, w := range []int{360, 400} {
		tab.Viewport(w, 780, true)
		if got := width("/fit"); got != float64(w) {
			t.Fatalf("a page that fits reads scrollWidth %v at an emulated %d px", got, w)
		}
		if got := width("/wide"); got <= float64(w) {
			t.Fatalf("a 600 px element reads scrollWidth %v at an emulated %d px: the measurement cannot fail", got, w)
		}
	}
	tab.Viewport(400, 860, true)
	tab.Navigate(srv.URL + "/toggle")
	read := func() [2]any {
		var s [2]any
		tab.Eval(`(() => { const r = document.getElementById("r"), b = document.getElementById("b"); return [r.hidden, b.getAttribute("aria-expanded")]; })()`, &s)
		return s
	}
	click := func() {
		tab.Eval(`(async () => { document.getElementById("b").click(); await new Promise(r => setTimeout(r, 100)); })()`, nil)
	}
	if got := read(); got != [2]any{false, "true"} {
		t.Fatalf("before a click: %v", got)
	}
	if click(); read() != [2]any{true, "false"} {
		t.Fatalf("after one click: %v", read())
	}
	if click(); read() != [2]any{false, "true"} {
		t.Fatalf("after two clicks: %v", read())
	}
}
