// Package websocket is a minimal RFC 6455 server, written against the
// standard library because SPEC section 22.1 asks for exactly that and
// SPEC 4.2 admits no dependency to do it with.
//
// Deliberately small: a server that accepts an upgrade, sends text
// frames, answers a ping, and closes cleanly. It does not offer
// compression, it does not fragment what it writes, and it is not a
// client. Those are the parts of the protocol an event stream does not
// need, and each one is a place to be wrong.
package websocket

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// magic is RFC 6455's fixed GUID, concatenated with the client's key to
// prove the server understood the handshake rather than echoing it.
const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// The opcodes this build handles.
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// Close codes from RFC 6455 section 7.4.1.
const (
	CloseNormal        = 1000
	CloseGoingAway     = 1001
	CloseProtocolError = 1002
	CloseUnsupported   = 1003
	CloseTooLarge      = 1009
	CloseInternal      = 1011
)

// Conn is an accepted connection.
type Conn struct {
	conn net.Conn
	r    *bufio.Reader
	// MaxMessage bounds one incoming message. A frame header can claim
	// a payload of any size, and a server that allocated on that claim
	// would be one frame away from being out of memory.
	MaxMessage int64
	// WriteTimeout bounds a single write, so a peer that stops reading
	// does not hold the connection open for ever.
	WriteTimeout time.Duration
}

// Accept completes the handshake and takes over the connection.
//
// It hijacks, which is what a protocol switch means: after this the
// response writer is finished with and the bytes are ours.
func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, errors.New("this is not a websocket upgrade")
	}
	if !headerContains(r.Header.Get("Connection"), "upgrade") {
		return nil, errors.New("the upgrade request does not ask to upgrade the connection")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		return nil, errors.New("this build speaks websocket version 13")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errors.New("the upgrade request carries no key")
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("this connection cannot be upgraded")
	}
	conn, buf, err := hijacker.Hijack()
	if err != nil {
		return nil, err
	}

	accept := acceptKey(key)
	_, err = fmt.Fprintf(conn,
		"HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\n\r\n", accept)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &Conn{
		conn:         conn,
		r:            buf.Reader,
		MaxMessage:   1 << 20,
		WriteTimeout: 30 * time.Second,
	}, nil
}

// acceptKey is RFC 6455's proof: SHA-1 of the key and the fixed GUID,
// base64. SHA-1 is not a choice about hashing here -- the protocol
// specifies this exact construction and it carries no security weight.
func acceptKey(key string) string {
	sum := sha1.Sum([]byte(key + magic))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func headerContains(header, want string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), want) {
			return true
		}
	}
	return false
}

// Close sends a close frame and closes the connection.
func (c *Conn) Close(code int, reason string) error {
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload, uint16(code))
	copy(payload[2:], reason)
	_ = c.writeFrame(opClose, payload)
	return c.conn.Close()
}

// WriteText sends one text message as a single unfragmented frame.
func (c *Conn) WriteText(payload []byte) error {
	if !utf8.Valid(payload) {
		return errors.New("a text frame is UTF-8")
	}
	return c.writeFrame(opText, payload)
}

// writeFrame writes one frame. A server never masks, per RFC 6455
// section 5.1.
func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	if c.WriteTimeout > 0 {
		_ = c.conn.SetWriteDeadline(time.Now().Add(c.WriteTimeout))
	}
	header := make([]byte, 0, 10)
	// FIN set: this build never fragments what it sends.
	header = append(header, 0x80|opcode)
	switch n := len(payload); {
	case n < 126:
		header = append(header, byte(n))
	case n <= 0xFFFF:
		header = append(header, 126, byte(n>>8), byte(n))
	default:
		header = append(header, 127)
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(n))
		header = append(header, size[:]...)
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	_, err := c.conn.Write(payload)
	return err
}

// Message is one message read from the peer.
type Message struct {
	Opcode  byte
	Payload []byte
}

// IsText reports whether this is a text message.
func (m Message) IsText() bool { return m.Opcode == opText }

// Read reads one whole message, answering a ping and honouring a close
// as it goes.
//
// Fragments are reassembled, because a client may send them even though
// this build does not: refusing what the protocol permits would be a
// server that works with some clients.
func (c *Conn) Read() (Message, error) {
	var assembled []byte
	var messageOp byte

	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return Message{}, err
		}

		switch opcode {
		case opPing:
			// Answered with the same payload, as RFC 6455 requires,
			// and it does not interrupt a message being assembled.
			if err := c.writeFrame(opPong, payload); err != nil {
				return Message{}, err
			}
			continue
		case opPong:
			continue
		case opClose:
			code := CloseNormal
			if len(payload) >= 2 {
				code = int(binary.BigEndian.Uint16(payload))
			}
			return Message{Opcode: opClose}, &CloseError{Code: code}
		case opContinuation:
			if messageOp == 0 {
				return Message{}, errors.New("a continuation frame began a message")
			}
		case opText, opBinary:
			if messageOp != 0 {
				return Message{}, errors.New("a new message began before the last one finished")
			}
			messageOp = opcode
		default:
			return Message{}, fmt.Errorf("opcode %#x is not one this build handles", opcode)
		}

		assembled = append(assembled, payload...)
		if int64(len(assembled)) > c.MaxMessage {
			_ = c.Close(CloseTooLarge, "message too large")
			return Message{}, errors.New("the message is larger than this build accepts")
		}
		if fin {
			return Message{Opcode: messageOp, Payload: assembled}, nil
		}
	}
}

// CloseError is what Read returns when the peer closed.
type CloseError struct{ Code int }

func (e *CloseError) Error() string {
	return fmt.Sprintf("the peer closed the connection (%d)", e.Code)
}

// IsClose reports whether an error is a peer close.
func IsClose(err error) bool {
	var closed *CloseError
	return errors.As(err, &closed)
}

// readFrame reads one frame header and its payload.
func (c *Conn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var head [2]byte
	if _, err := io.ReadFull(c.r, head[:]); err != nil {
		return false, 0, nil, err
	}
	fin = head[0]&0x80 != 0
	if head[0]&0x70 != 0 {
		// Reserved bits set means an extension was negotiated, and
		// none was. Refusing beats reading the frame as though the
		// bits meant nothing.
		return false, 0, nil, errors.New("a reserved frame bit is set and no extension was negotiated")
	}
	opcode = head[0] & 0x0f
	masked := head[1]&0x80 != 0
	length := int64(head[1] & 0x7f)

	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.r, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.r, ext[:]); err != nil {
			return false, 0, nil, err
		}
		size := binary.BigEndian.Uint64(ext[:])
		if size > uint64(c.MaxMessage) {
			// Refused on the claim, before anything is allocated: a
			// header saying eight exabytes must not become an
			// allocation of eight exabytes.
			return false, 0, nil, errors.New("the frame claims to be larger than this build accepts")
		}
		length = int64(size)
	}
	if length > c.MaxMessage {
		return false, 0, nil, errors.New("the frame is larger than this build accepts")
	}
	if !masked {
		// RFC 6455 section 5.1: every frame from a client is masked,
		// and a server that accepts an unmasked one is one an
		// intermediary can be confused by.
		return false, 0, nil, errors.New("a frame from the client was not masked")
	}

	var mask [4]byte
	if _, err := io.ReadFull(c.r, mask[:]); err != nil {
		return false, 0, nil, err
	}
	payload = make([]byte, length)
	if _, err := io.ReadFull(c.r, payload); err != nil {
		return false, 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return fin, opcode, payload, nil
}

// Ping sends a ping, which is how an idle stream stays open through an
// intermediary that closes quiet connections.
func (c *Conn) Ping() error { return c.writeFrame(opPing, nil) }
