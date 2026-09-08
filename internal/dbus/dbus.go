// Package dbus is a direct implementation of the D-Bus wire protocol,
// enough of it to talk to systemd over the system bus.
//
// SPEC 15.2 says the `service` module's systemd provider speaks "over
// its D-Bus API where available, falling back to `systemctl`; the D-Bus
// client is a direct implementation of the wire protocol over a unix
// socket, since D-Bus marshalling is well-specified and small." This is
// that client.
//
// It is not a general D-Bus library. It marshals the handful of types a
// systemd `Manager` call needs -- strings, object paths, signatures,
// booleans, uint32, arrays of those, structs, and variants on the way
// back in -- authenticates with SASL EXTERNAL, and demultiplexes method
// returns from signals on one connection used one call at a time. It
// does not do the session bus, file-descriptor passing, property `Set`,
// or asynchronous dispatch, because nothing here needs them and a
// function for a capability nothing uses reads as an assurance it is not.
package dbus

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Conn is a connection to a message bus, authenticated and ready for
// method calls. It is not safe for concurrent use: the systemd binding
// drives one connection through one operation at a time.
type Conn struct {
	c      net.Conn
	r      *bufio.Reader
	serial uint32
	unique string
}

// DialSystemBus connects to the system bus named by
// $DBUS_SYSTEM_BUS_ADDRESS, or the well-known socket if that is unset.
func DialSystemBus() (*Conn, error) {
	addr := os.Getenv("DBUS_SYSTEM_BUS_ADDRESS")
	if addr == "" {
		addr = "unix:path=/run/dbus/system_bus_socket"
	}
	return Dial(addr)
}

// Dial connects to the bus at a D-Bus address and completes the
// authentication handshake. Only the `unix:` transport is understood;
// that is the only one a local system bus uses.
func Dial(address string) (*Conn, error) {
	network, addr, err := parseUnixAddress(address)
	if err != nil {
		return nil, err
	}
	c, err := net.Dial(network, addr)
	if err != nil {
		return nil, err
	}
	conn, err := Open(c)
	if err != nil {
		c.Close()
		return nil, err
	}
	return conn, nil
}

// Open runs the handshake over an established connection. It is the seam
// a test uses to drive the protocol over net.Pipe.
func Open(c net.Conn) (*Conn, error) {
	conn := &Conn{c: c, r: bufio.NewReader(c)}
	// A generous ceiling so the handshake and the early calls cannot
	// hang a run forever; the systemd binding tightens this per call.
	_ = c.SetDeadline(time.Now().Add(2 * time.Minute))
	if err := conn.auth(); err != nil {
		return nil, err
	}
	return conn, nil
}

// parseUnixAddress reduces a D-Bus address list to a net.Dial pair. A
// list is semicolon-separated; the first `unix:` entry wins.
func parseUnixAddress(list string) (network, addr string, err error) {
	for _, entry := range strings.Split(list, ";") {
		entry = strings.TrimSpace(entry)
		transport, rest, ok := strings.Cut(entry, ":")
		if !ok || transport != "unix" {
			continue
		}
		for _, kv := range strings.Split(rest, ",") {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			switch k {
			case "path":
				return "unix", v, nil
			case "abstract":
				// Linux abstract namespace: Go spells the leading NUL
				// as "@".
				return "unix", "@" + v, nil
			}
		}
	}
	return "", "", fmt.Errorf("dbus: no usable unix transport in %q", list)
}

// auth performs SASL EXTERNAL: a leading NUL byte, then the uid in hex,
// then BEGIN. EXTERNAL is what a local peer on a unix socket uses -- the
// kernel already told the bus who is connecting.
func (c *Conn) auth() error {
	uid := strconv.Itoa(os.Getuid())
	// The NUL is required before any auth traffic. The uid is sent as
	// the hex of its ASCII digits, which is what "%x" of a string is.
	if _, err := fmt.Fprintf(c.c, "\x00AUTH EXTERNAL %x\r\n", uid); err != nil {
		return err
	}
	line, err := c.readLine()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "OK ") {
		return fmt.Errorf("dbus: authentication refused: %q", line)
	}
	if _, err := c.c.Write([]byte("BEGIN\r\n")); err != nil {
		return err
	}
	return nil
}

func (c *Conn) readLine() (string, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// SetReadDeadline bounds how long the next ReadMessage may block. A zero
// time clears it.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.c.SetReadDeadline(t) }

// Close closes the underlying connection, which also unwinds any
// Subscribe the caller made.
func (c *Conn) Close() error { return c.c.Close() }
