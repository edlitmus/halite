package dbus

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// A method call marshals and comes back the same on decode. This is the
// property the rest of the client rests on: the systemd binding builds a
// Message, and what systemd parses has to be what the binding meant.
func TestMethodCallRoundTrips(t *testing.T) {
	in := &Message{
		Type:        TypeMethodCall,
		Serial:      7,
		Path:        "/org/freedesktop/systemd1",
		Interface:   "org.freedesktop.systemd1.Manager",
		Member:      "StartUnit",
		Destination: "org.freedesktop.systemd1",
		Signature:   "ss",
		Body:        []any{"cron.service", "replace"},
	}
	buf, err := encode(in)
	if err != nil {
		t.Fatal(err)
	}
	if buf[0] != 'l' || buf[1] != TypeMethodCall || buf[3] != 1 {
		t.Fatalf("fixed header wrong: % x", buf[:4])
	}
	if got := order.Uint32(buf[8:]); got != 7 {
		t.Errorf("serial on the wire = %d", got)
	}
	if len(buf)%8 != 0 {
		t.Errorf("a message is padded to 8 and this is %d bytes", len(buf))
	}

	out, err := decode(bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	if out.Path != in.Path || out.Interface != in.Interface ||
		out.Member != in.Member || out.Destination != in.Destination ||
		out.Signature != in.Signature {
		t.Errorf("header did not round-trip:\n in  %+v\n out %+v", in, out)
	}
	if len(out.Body) != 2 || out.Body[0] != "cron.service" || out.Body[1] != "replace" {
		t.Errorf("body did not round-trip: %#v", out.Body)
	}
}

// The Hello call, byte for byte where it is cheap to be sure. Empty
// body, four string-ish header fields; the header length and the total
// length are the two numbers a decoder on the far side reads first.
func TestHelloLayout(t *testing.T) {
	buf, err := encode(&Message{
		Type:        TypeMethodCall,
		Serial:      1,
		Path:        "/org/freedesktop/DBus",
		Interface:   "org.freedesktop.DBus",
		Member:      "Hello",
		Destination: "org.freedesktop.DBus",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := order.Uint32(buf[4:]); got != 0 {
		t.Errorf("body length = %d, want 0", got)
	}
	// Path(21) + Interface(20) + Member(5) + Destination(20), each field
	// aligned to 8, no trailing pad counted: 109 bytes of header fields.
	if got := order.Uint32(buf[12:]); got != 109 {
		t.Errorf("header field array length = %d, want 109", got)
	}
	if len(buf) != 128 {
		t.Errorf("whole Hello message = %d bytes, want 128", len(buf))
	}

	out, err := decode(bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	if out.Member != "Hello" || out.Destination != "org.freedesktop.DBus" {
		t.Errorf("decoded %+v", out)
	}
}

// Arrays of strings and booleans, which EnableUnitFiles needs.
func TestArrayAndBoolRoundTrip(t *testing.T) {
	in := &Message{
		Type:      TypeMethodCall,
		Serial:    3,
		Member:    "EnableUnitFiles",
		Signature: "asbb",
		Body:      []any{[]string{"a.service", "b.service"}, false, true},
	}
	buf, err := encode(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := decode(bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	arr, ok := out.Body[0].([]any)
	if !ok || len(arr) != 2 || arr[0] != "a.service" || arr[1] != "b.service" {
		t.Errorf("string array: %#v", out.Body[0])
	}
	if out.Body[1] != false || out.Body[2] != true {
		t.Errorf("bools: %#v %#v", out.Body[1], out.Body[2])
	}
}

// A property reply is a variant, and the client unwraps it: a caller
// asking for ActiveState wants "active", not a wrapper holding it. These
// bytes are a METHOD_RETURN of signature "v" carrying the string.
func TestVariantReplyUnwraps(t *testing.T) {
	var b []byte
	put := func(v ...byte) { b = append(b, v...) }
	// fixed header
	put('l', TypeMethodReturn, 0, 1)
	put(0x0f, 0, 0, 0) // body length 15
	put(2, 0, 0, 0)    // serial 2
	// header field array length 15
	put(0x0f, 0, 0, 0)
	// REPLY_SERIAL (code 5), variant sig "u", value 42
	put(5, 1, 'u', 0)
	put(42, 0, 0, 0)
	// SIGNATURE (code 8) is already 8-aligned here; variant sig "g",
	// value "v"
	put(8, 1, 'g', 0)
	put(1, 'v', 0)
	// pad header (31) to 32
	put(0)
	// body: variant("s", "active")
	put(1, 's', 0) // variant signature
	put(0)         // align the string to 4
	put(6, 0, 0, 0)
	put('a', 'c', 't', 'i', 'v', 'e', 0)

	m, err := decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != TypeMethodReturn || m.ReplySerial != 42 || m.Signature != "v" {
		t.Fatalf("header decoded wrong: %+v", m)
	}
	if len(m.Body) != 1 || m.Body[0] != "active" {
		t.Errorf("variant body = %#v, want [\"active\"]", m.Body)
	}
}

// An ERROR reply becomes an *Error naming the org.freedesktop.* name.
func TestCallTurnsErrorReplyIntoError(t *testing.T) {
	client, server := net.Pipe()
	go fakeBus(t, server, func(call *Message) *Message {
		return &Message{
			Type:        TypeError,
			ReplySerial: call.Serial,
			ErrorName:   "org.freedesktop.systemd1.NoSuchUnit",
			Signature:   "s",
			Body:        []any{"Unit missing.service not found."},
		}
	})

	c, err := Open(client)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.Call("org.freedesktop.systemd1", "/org/freedesktop/systemd1",
		"org.freedesktop.systemd1.Manager", "GetUnit", "s", "missing.service")
	de, ok := err.(*Error)
	if !ok {
		t.Fatalf("error is %T: %v", err, err)
	}
	if de.Name != "org.freedesktop.systemd1.NoSuchUnit" {
		t.Errorf("error name = %q", de.Name)
	}
}

// The whole handshake and one call, over a pipe.
func TestHandshakeAndCallOverPipe(t *testing.T) {
	client, server := net.Pipe()
	go fakeBus(t, server, func(call *Message) *Message {
		if call.Member != "GetUnitFileState" {
			t.Errorf("unexpected call %q", call.Member)
		}
		return &Message{
			Type:        TypeMethodReturn,
			ReplySerial: call.Serial,
			Signature:   "s",
			Body:        []any{"enabled"},
		}
	})

	c, err := Open(client)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	defer c.Close()

	out, err := c.Call("org.freedesktop.systemd1", "/org/freedesktop/systemd1",
		"org.freedesktop.systemd1.Manager", "GetUnitFileState", "s", "cron.service")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0] != "enabled" {
		t.Errorf("reply body = %#v", out)
	}
}

// fakeBus plays the server side: it completes the SASL EXTERNAL
// handshake, then answers each method call with reply(call).
func fakeBus(t *testing.T, conn net.Conn, reply func(*Message) *Message) {
	t.Helper()
	defer conn.Close()
	br := bufio.NewReader(conn)

	// Leading NUL, then the AUTH line.
	if _, err := br.ReadByte(); err != nil {
		return
	}
	if _, err := br.ReadString('\n'); err != nil {
		return
	}
	if _, err := io.WriteString(conn, "OK 0123456789abcdef0123456789abcdef\r\n"); err != nil {
		return
	}
	if _, err := br.ReadString('\n'); err != nil { // BEGIN
		return
	}

	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		m, err := decode(br)
		if err != nil {
			return
		}
		if m.Type != TypeMethodCall {
			continue
		}
		r := reply(m)
		buf, err := encode(r)
		if err != nil {
			t.Errorf("encoding reply: %v", err)
			return
		}
		if _, err := conn.Write(buf); err != nil {
			return
		}
	}
}
