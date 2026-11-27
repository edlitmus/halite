package transport

import (
	"context"
	"io"
	"net"
	"sync"
	"time"
)

// dialDraining dials the hub with the dialer net/http would have used, and
// hands back a connection whose Close ends it with a FIN (issue 757).
//
// tls.Conn.Close writes close_notify and closes the descriptor straight away,
// without reading what the peer says back -- and a Go server answers a
// close_notify with its own within a round trip. That answer reaches a socket
// that no longer exists, and the kernel replies with an RST: on FreeBSD,
// "packets received after close" in netstat -s, and a "Spurious RST" line in
// the hub's log for every connection reaped. Nothing is lost by it, since both
// ends had already decided to discard the connection, but every idle
// connection ends the abortive way and the hub never sees a clean shutdown.
//
// Making the node's idle timeout shorter than the hub's, so that the node
// closes first, does not help, and that was measured rather than assumed
// (DIVERGENCE 5.681): the hub's close_notify is a reply to the node's, not
// its own timer, so the node that closes first is the one that resets. What
// helps is keeping the descriptor until the reply has come.
func dialDraining(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	tcp, ok := c.(*net.TCPConn)
	if !ok {
		return c, nil
	}
	return &drainingConn{TCPConn: tcp}, nil
}

// drainingConn's Close sends a FIN, discards whatever the peer still sends,
// and closes the descriptor when the peer's FIN arrives or HandshakeTimeout
// passes. Close itself returns at once: the TLS layer that calls it and the
// idle reaper behind that are not held up by the peer.
//
// HandshakeTimeout is the bound because it is the longest this transport
// already waits for one round trip, and the reply is one round trip away. A
// peer that does not answer in that time leaves the connection to end as it
// did before this existed, which is no worse.
type drainingConn struct {
	*net.TCPConn
	once sync.Once
}

func (c *drainingConn) Close() error {
	err := net.ErrClosed
	c.once.Do(func() {
		if err = c.TCPConn.CloseWrite(); err != nil {
			err = c.TCPConn.Close()
			return
		}
		go c.drain()
	})
	return err
}

// DrainingListener hands back the connections ln accepts as drainingConns,
// so that the hub ends them the way dialDraining makes a node end its own.
//
// The hub needs it for a reason of its own (issue 875). TLS 1.3 lets a client
// finish its half of the handshake before the server has checked the client's
// certificate, and a Go client then writes at once: HTTP/2's preface,
// SETTINGS and WINDOW_UPDATE. When the hub refuses the certificate it sends
// the alert and closes with those bytes still unread, and a socket closed with
// unread data ends with an RST, not a FIN. On Windows the RST discards the
// alert the client has not yet read ("An existing connection was forcibly
// closed"); on unix the client's next write fails with EPIPE. Either way the
// node is left with an error that says nothing about its certificate, so
// CertificateRefused cannot see a revocation and Unreachable calls it an
// outage. Measured with the test fixture on Windows 11: 297 resets in 4,500
// refused handshakes, 286 of them in the expired case; with the hub's
// connections drained, none (DIVERGENCE 5.755).
//
// Wrapping below TLS, rather than in front of net/http, is what makes it
// apply to the handshake failures: net/http closes those itself, through
// tls.Conn.Close, which closes the connection underneath -- this one.
//
// The cost is a goroutine and a descriptor for up to HandshakeTimeout after
// each close, until the peer's FIN. A peer could already hold a descriptor
// that long by never finishing its handshake, so this at most doubles what
// one connection can hold, and the connection cap counts streams, not
// sockets (internal/hub/connlimit.go).
func DrainingListener(ln net.Listener) net.Listener { return drainingListener{ln} }

type drainingListener struct{ net.Listener }

func (l drainingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if tcp, ok := c.(*net.TCPConn); ok {
		return &drainingConn{TCPConn: tcp}, nil
	}
	return c, nil
}

func (c *drainingConn) drain() {
	_ = c.TCPConn.SetReadDeadline(time.Now().Add(HandshakeTimeout))
	_, _ = io.Copy(io.Discard, c.TCPConn)
	_ = c.TCPConn.Close()
}
