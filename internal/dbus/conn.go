package dbus

import "fmt"

// Error is a D-Bus error reply: the `org.freedesktop.*` error name and,
// where the peer sent one, a human message.
type Error struct {
	Name string
	Body string
}

func (e *Error) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("%s: %s", e.Name, e.Body)
	}
	return e.Name
}

// SendCall writes a METHOD_CALL and returns its serial. The caller reads
// replies and signals with ReadMessage and matches on ReplySerial. This
// is the primitive the systemd binding needs so it can watch for a
// JobRemoved signal that may arrive before the method return.
func (c *Conn) SendCall(dest, path, iface, member, sig string, args ...any) (uint32, error) {
	c.serial++
	m := &Message{
		Type:        TypeMethodCall,
		Serial:      c.serial,
		Path:        path,
		Interface:   iface,
		Member:      member,
		Destination: dest,
		Signature:   sig,
		Body:        args,
	}
	buf, err := encode(m)
	if err != nil {
		return 0, err
	}
	if _, err := c.c.Write(buf); err != nil {
		return 0, err
	}
	return c.serial, nil
}

// ReadMessage reads the next message off the bus, whatever its type.
func (c *Conn) ReadMessage() (*Message, error) {
	return decode(c.r)
}

// Call issues a method call and returns the body of its reply, turning
// an ERROR reply into an *Error. Signals that arrive before the matching
// reply are discarded: a bare Call has no interest in them.
func (c *Conn) Call(dest, path, iface, member, sig string, args ...any) ([]any, error) {
	serial, err := c.SendCall(dest, path, iface, member, sig, args...)
	if err != nil {
		return nil, err
	}
	for {
		m, err := c.ReadMessage()
		if err != nil {
			return nil, err
		}
		if m.ReplySerial != serial {
			continue
		}
		if m.Type == TypeError {
			body := ""
			if len(m.Body) > 0 {
				body, _ = m.Body[0].(string)
			}
			return nil, &Error{Name: m.ErrorName, Body: body}
		}
		return m.Body, nil
	}
}

// Hello is the mandatory first call: the bus assigns this connection its
// unique name and will not route anything until it has been made.
func (c *Conn) Hello() (string, error) {
	out, err := c.Call("org.freedesktop.DBus", "/org/freedesktop/DBus",
		"org.freedesktop.DBus", "Hello", "")
	if err != nil {
		return "", err
	}
	name, _ := out[0].(string)
	c.unique = name
	return name, nil
}

// AddMatch installs a match rule so the bus routes matching signals to
// this connection. Unicast method returns arrive without one; broadcast
// signals do not.
func (c *Conn) AddMatch(rule string) error {
	_, err := c.Call("org.freedesktop.DBus", "/org/freedesktop/DBus",
		"org.freedesktop.DBus", "AddMatch", "s", rule)
	return err
}
