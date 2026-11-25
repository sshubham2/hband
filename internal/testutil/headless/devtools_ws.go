package headless

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// A hand-written RFC 6455 client, just enough for the DevTools protocol: it sends
// no Origin header (Chrome refuses a DevTools handshake that carries one unless it
// is started with --remote-allow-origins), checks Sec-WebSocket-Accept, masks every
// frame it sends, and reads payload lengths of 7, 16 and 64 bits, messages split
// across continuation frames, and pings (answered with a pong) between them.

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type wsConn struct {
	conn net.Conn
	r    *bufio.Reader
	wmu  sync.Mutex
}

// dialWS performs the opening handshake with the DevTools endpoint at rawURL.
func dialWS(rawURL string) (*wsConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("tcp", u.Host, 10*time.Second)
	if err != nil {
		return nil, err
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(raw[:])
	path := u.RequestURI()
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", path, u.Host, key)
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, req); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	getReq, _ := http.NewRequest(http.MethodGet, rawURL, nil)
	resp, err := http.ReadResponse(br, getReq)
	if err != nil {
		conn.Close()
		return nil, err
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	want := base64.StdEncoding.EncodeToString(sum[:])
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Accept") != want {
		conn.Close()
		return nil, fmt.Errorf("websocket handshake refused: status %d, Sec-WebSocket-Accept %q, want %q", resp.StatusCode, resp.Header.Get("Sec-WebSocket-Accept"), want)
	}
	conn.SetDeadline(time.Time{})
	return &wsConn{conn: conn, r: br}, nil
}

// writeFrame sends one final, masked frame.
func (w *wsConn) writeFrame(op byte, payload []byte) error {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	return writeMaskedFrame(w.conn, op, payload)
}

func writeMaskedFrame(out io.Writer, op byte, payload []byte) error {
	head := []byte{0x80 | op}
	switch n := len(payload); {
	case n < 126:
		head = append(head, 0x80|byte(n))
	case n < 1<<16:
		head = append(head, 0x80|126, byte(n>>8), byte(n))
	default:
		var l [8]byte
		binary.BigEndian.PutUint64(l[:], uint64(n))
		head = append(append(head, 0x80|127), l[:]...)
	}
	var key [4]byte
	if _, err := rand.Read(key[:]); err != nil {
		return err
	}
	frame := append(head, key[:]...)
	for i, b := range payload {
		frame = append(frame, b^key[i%4])
	}
	_, err := out.Write(frame)
	return err
}

// writeText sends one text message.
func (w *wsConn) writeText(msg []byte) error { return w.writeFrame(0x1, msg) }

// readMessage returns the next text or binary message, whole. A ping met on the
// way is answered with a pong carrying the same payload; a close ends it with
// io.EOF.
func (w *wsConn) readMessage() ([]byte, error) { return readWSMessage(w.r, w.writeFrame) }

func readWSMessage(r io.Reader, write func(op byte, payload []byte) error) ([]byte, error) {
	var msg []byte
	for {
		var head [2]byte
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return nil, err
		}
		fin, op := head[0]&0x80 != 0, head[0]&0x0f
		masked := head[1]&0x80 != 0
		n := uint64(head[1] & 0x7f)
		switch n {
		case 126:
			var l [2]byte
			if _, err := io.ReadFull(r, l[:]); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(l[:]))
		case 127:
			var l [8]byte
			if _, err := io.ReadFull(r, l[:]); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(l[:])
		}
		if n > 1<<30 {
			return nil, errors.New("websocket frame too large")
		}
		var key [4]byte
		if masked {
			if _, err := io.ReadFull(r, key[:]); err != nil {
				return nil, err
			}
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= key[i%4]
			}
		}
		switch op {
		case 0x8:
			return nil, io.EOF
		case 0x9:
			if err := write(0xA, payload); err != nil {
				return nil, err
			}
		case 0xA:
		default: // text, binary, continuation
			msg = append(msg, payload...)
			if fin {
				return msg, nil
			}
		}
	}
}

func (w *wsConn) close() { w.conn.Close() }
