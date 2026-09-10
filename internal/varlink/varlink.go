// Package varlink is a direct implementation of the client half of the
// Varlink wire protocol, enough of it to call systemd's varlink
// services over a unix socket.
//
// Varlink is systemd's IPC for the interfaces that post-date D-Bus —
// among them `io.systemd.Journal`, which `journald` uses for the
// journal's own rotate, flush and sync. SPEC section 15.3's journald
// row wants the journal reached "over a socket rather than by parsing
// `journalctl` output", and for the control verbs the varlink socket is
// exactly that.
//
// The protocol is small enough to implement rather than depend on: a
// request is one JSON object followed by a NUL byte, and a reply is one
// JSON object followed by a NUL byte. There is no framing to get wrong
// and no marshalling — which is the same argument `internal/dbus` makes
// for D-Bus, one layer simpler. Only the plain call/reply exchange is
// implemented; `more` (streaming) and `oneway` are not, because nothing
// here needs them, and a reply carrying `continues: true` is reported
// as an error rather than mishandled.
package varlink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// Error is a Varlink error reply: the interface-qualified error name and
// the parameters the service sent with it.
type Error struct {
	Name       string
	Parameters json.RawMessage
}

func (e *Error) Error() string {
	if len(e.Parameters) > 0 && string(e.Parameters) != "{}" && string(e.Parameters) != "null" {
		return fmt.Sprintf("varlink error %s %s", e.Name, e.Parameters)
	}
	return "varlink error " + e.Name
}

// Call sends one method call to the varlink service listening on
// socketPath and returns the `parameters` object of its reply.
//
// method is the fully-qualified name, such as
// `io.systemd.Journal.Rotate`. params is marshalled as the request's
// `parameters`; nil sends `{}`. A varlink error reply comes back as a
// *Error, so a caller can tell "the service said no" from "the socket
// was not there".
func Call(ctx context.Context, socketPath, method string, params any) (json.RawMessage, error) {
	if params == nil {
		params = struct{}{}
	}
	pj, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("varlink: encoding parameters: %w", err)
	}
	req, err := json.Marshal(request{Method: method, Parameters: pj})
	if err != nil {
		return nil, fmt.Errorf("varlink: encoding request: %w", err)
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("varlink: dialing %s: %w", socketPath, err)
	}
	defer conn.Close()

	deadline := time.Now().Add(30 * time.Second)
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	if _, err := conn.Write(append(req, 0)); err != nil {
		return nil, fmt.Errorf("varlink: writing request: %w", err)
	}

	reply, err := readFrame(conn)
	if err != nil {
		return nil, err
	}
	var r response
	if err := json.Unmarshal(reply, &r); err != nil {
		return nil, fmt.Errorf("varlink: reply did not parse: %w", err)
	}
	if r.Continues {
		return nil, errors.New("varlink: service sent a streaming reply, which this client does not read")
	}
	if r.Error != "" {
		return nil, &Error{Name: r.Error, Parameters: r.Parameters}
	}
	if r.Parameters == nil {
		return json.RawMessage("{}"), nil
	}
	return r.Parameters, nil
}

type request struct {
	Method     string          `json:"method"`
	Parameters json.RawMessage `json:"parameters"`
}

type response struct {
	Parameters json.RawMessage `json:"parameters,omitempty"`
	Error      string          `json:"error,omitempty"`
	Continues  bool            `json:"continues,omitempty"`
}

// readFrame reads bytes up to and not including the NUL terminator.
func readFrame(conn net.Conn) ([]byte, error) {
	var (
		buf [4096]byte
		out []byte
	)
	for {
		n, err := conn.Read(buf[:])
		if n > 0 {
			if i := indexNUL(buf[:n]); i >= 0 {
				out = append(out, buf[:i]...)
				return out, nil
			}
			out = append(out, buf[:n]...)
		}
		if err != nil {
			return nil, fmt.Errorf("varlink: reading reply: %w", err)
		}
		if len(out) > 8<<20 {
			return nil, errors.New("varlink: reply exceeded 8 MiB without a terminator")
		}
	}
}

func indexNUL(b []byte) int {
	for i, c := range b {
		if c == 0 {
			return i
		}
	}
	return -1
}
