package websocket

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// RFC 6455's handshake proof: SHA-1 of the key and the fixed GUID. The
// example is the one in section 1.3, so this checks the construction
// against the specification rather than against itself.
func TestTheHandshakeProofMatchesTheSpecification(t *testing.T) {
	if got := acceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Errorf("acceptKey = %q", got)
	}
}

// dial completes a handshake against a server and returns the raw
// connection, so the framing can be exercised as a client would.
func dial(t *testing.T, handler func(*Conn)) net.Conn {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Accept(w, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		handler(conn)
	}))
	t.Cleanup(ts.Close)

	addr := strings.TrimPrefix(ts.URL, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	_, err = conn.Write([]byte(
		"GET /ws HTTP/1.1\r\n" +
			"Host: " + addr + "\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n" +
			"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
			"Sec-WebSocket-Version: 13\r\n\r\n"))
	if err != nil {
		t.Fatal(err)
	}

	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("the handshake answered %d", res.StatusCode)
	}
	if res.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Errorf("the proof is %q", res.Header.Get("Sec-WebSocket-Accept"))
	}
	return &bufferedConn{Conn: conn, r: br}
}

// bufferedConn keeps the reader the handshake left bytes in.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// maskedFrame builds a client frame, which is always masked.
func maskedFrame(opcode byte, payload []byte) []byte {
	var buf bytes.Buffer
	buf.WriteByte(0x80 | opcode)
	mask := []byte{0x01, 0x02, 0x03, 0x04}
	switch n := len(payload); {
	case n < 126:
		buf.WriteByte(0x80 | byte(n))
	default:
		buf.WriteByte(0x80 | 126)
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(n))
		buf.Write(ext[:])
	}
	buf.Write(mask)
	for i, b := range payload {
		buf.WriteByte(b ^ mask[i%4])
	}
	return buf.Bytes()
}

// readFrameFrom reads one server frame off the wire.
func readFrameFrom(t *testing.T, conn net.Conn) (opcode byte, payload []byte) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	head := make([]byte, 2)
	if _, err := readFull(conn, head); err != nil {
		t.Fatal(err)
	}
	opcode = head[0] & 0x0f
	length := int(head[1] & 0x7f)
	if head[1]&0x80 != 0 {
		t.Fatal("the server masked a frame, which a server must not do")
	}
	if length == 126 {
		ext := make([]byte, 2)
		if _, err := readFull(conn, ext); err != nil {
			t.Fatal(err)
		}
		length = int(binary.BigEndian.Uint16(ext))
	}
	payload = make([]byte, length)
	if length > 0 {
		if _, err := readFull(conn, payload); err != nil {
			t.Fatal(err)
		}
	}
	return opcode, payload
}

func readFull(conn net.Conn, p []byte) (int, error) {
	read := 0
	for read < len(p) {
		n, err := conn.Read(p[read:])
		read += n
		if err != nil {
			return read, err
		}
	}
	return read, nil
}

// A text message goes out unmasked and unfragmented, which is what a
// server sends.
func TestAServerSendsUnmaskedText(t *testing.T) {
	conn := dial(t, func(c *Conn) {
		_ = c.WriteText([]byte("hello"))
		time.Sleep(200 * time.Millisecond)
	})
	opcode, payload := readFrameFrom(t, conn)
	if opcode != opText {
		t.Errorf("the opcode is %#x", opcode)
	}
	if string(payload) != "hello" {
		t.Errorf("the payload is %q", payload)
	}
}

// A payload over 125 bytes uses the extended length, which is the case
// every real event hits.
func TestALongMessageUsesTheExtendedLength(t *testing.T) {
	long := strings.Repeat("x", 400)
	conn := dial(t, func(c *Conn) {
		_ = c.WriteText([]byte(long))
		time.Sleep(200 * time.Millisecond)
	})
	opcode, payload := readFrameFrom(t, conn)
	if opcode != opText || string(payload) != long {
		t.Errorf("a long message came back as %d bytes", len(payload))
	}
}

// A ping is answered with the same payload, which is how an idle stream
// stays open.
func TestAPingIsAnsweredWithItsPayload(t *testing.T) {
	got := make(chan struct{})
	conn := dial(t, func(c *Conn) {
		// One read is enough: the pong is sent inside it.
		go func() { _, _ = c.Read(); close(got) }()
		time.Sleep(500 * time.Millisecond)
	})
	if _, err := conn.Write(maskedFrame(opPing, []byte("beat"))); err != nil {
		t.Fatal(err)
	}
	opcode, payload := readFrameFrom(t, conn)
	if opcode != opPong || string(payload) != "beat" {
		t.Errorf("the answer is %#x %q", opcode, payload)
	}
}

// A close from the peer is reported as one rather than as a broken
// connection, so a caller can tell a clean end from a fault.
func TestACloseIsReportedAsOne(t *testing.T) {
	result := make(chan error, 1)
	conn := dial(t, func(c *Conn) {
		_, err := c.Read()
		result <- err
	})
	payload := make([]byte, 2)
	binary.BigEndian.PutUint16(payload, CloseNormal)
	if _, err := conn.Write(maskedFrame(opClose, payload)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !IsClose(err) {
			t.Errorf("a close came back as %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the close was never noticed")
	}
}

// RFC 6455 section 5.1: every frame from a client is masked. A server
// that accepts an unmasked one is one an intermediary can be confused
// by.
func TestAnUnmaskedClientFrameIsRefused(t *testing.T) {
	result := make(chan error, 1)
	conn := dial(t, func(c *Conn) {
		_, err := c.Read()
		result <- err
	})
	// FIN + text, length 1, no mask bit.
	if _, err := conn.Write([]byte{0x81, 0x01, 'x'}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "masked") {
			t.Errorf("an unmasked frame came back as %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an unmasked frame was accepted")
	}
}

// A header claiming an enormous payload must be refused on the claim,
// before anything is allocated.
func TestAnEnormousClaimIsRefusedBeforeAllocating(t *testing.T) {
	result := make(chan error, 1)
	conn := dial(t, func(c *Conn) {
		c.MaxMessage = 1024
		_, err := c.Read()
		result <- err
	})
	// FIN + text, 64-bit length of eight exabytes, masked.
	frame := []byte{0x81, 0xFF}
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], 1<<62)
	frame = append(frame, size[:]...)
	frame = append(frame, 0x01, 0x02, 0x03, 0x04)
	if _, err := conn.Write(frame); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "larger than") {
			t.Errorf("an enormous claim came back as %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an enormous claim was accepted")
	}
}

// Fragments are reassembled, because a client may send them even though
// this build does not: refusing what the protocol permits would be a
// server that works with some clients.
func TestFragmentsAreReassembled(t *testing.T) {
	result := make(chan Message, 1)
	conn := dial(t, func(c *Conn) {
		msg, err := c.Read()
		if err == nil {
			result <- msg
		}
		time.Sleep(200 * time.Millisecond)
	})

	// A text frame with FIN clear, then a continuation with FIN set.
	first := maskedFrame(opText, []byte("hel"))
	first[0] &^= 0x80
	if _, err := conn.Write(first); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(maskedFrame(opContinuation, []byte("lo"))); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-result:
		if !msg.IsText() || string(msg.Payload) != "hello" {
			t.Errorf("the message came back as %q", msg.Payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the fragments were never reassembled")
	}
}

// An upgrade that is not one is refused rather than half-accepted.
func TestABadUpgradeIsRefused(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := Accept(w, r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
	}))
	t.Cleanup(ts.Close)

	cases := map[string]map[string]string{
		"no upgrade header": {"Connection": "Upgrade", "Sec-WebSocket-Version": "13"},
		"the wrong version": {"Upgrade": "websocket", "Connection": "Upgrade",
			"Sec-WebSocket-Version": "8", "Sec-WebSocket-Key": "x"},
		"no key": {"Upgrade": "websocket", "Connection": "Upgrade",
			"Sec-WebSocket-Version": "13"},
	}
	for name, headers := range cases {
		req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s answered %d", name, res.StatusCode)
		}
	}
}
