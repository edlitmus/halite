package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// A connection the client gives up ends in a FIN, not an RST (issue 757).
//
// tls.Conn.Close writes close_notify and closes the descriptor without reading
// the peer's reply, and a Go server answers a close_notify with its own within
// a round trip. When that reply lands before the descriptor is closed the
// kernel finds unread bytes and resets the connection. Which end's idle timer
// fires first does not matter, so the test does not wait for one:
// CloseIdleConnections takes the same Close the idle reaper does.
//
// Ten connections, because one is a race. Without the drain every one of ten
// reset, in each of five runs on FreeBSD (DIVERGENCE 5.681), so ten FINs are
// not luck.
func TestAnAbandonedConnectionEndsInAFinNotAReset(t *testing.T) {
	skipWhereTheObserverIsBlind(t)
	ol := &endObserver{ends: make(chan string, 16)}
	f := newFixtureOn(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}), func(ln net.Listener) net.Listener { ol.Listener = ln; return ol })
	pair, _ := f.enrolledPair(t, "web1.example")

	ends := map[string]int{}
	for range 10 {
		client := &Client{HubURL: f.url, CA: f.ca.Cert, Cert: &pair, ServerName: "localhost", Timeout: 5 * time.Second}
		if _, err := client.Health(context.Background()); err != nil {
			t.Fatal(err)
		}
		client.CloseIdleConnections()
		select {
		case end := <-ol.ends:
			ends[end]++
		case <-time.After(5 * time.Second):
			t.Fatal("the server saw no end to the connection 5 s after the client closed it")
		}
	}
	if ends["FIN"] != 10 {
		t.Errorf("connection ends seen by the server: %v, want 10 FIN", ends)
	}
}

// The observer itself must be able to see a reset, or the test above passes
// on a probe that only ever says FIN. It is built the way FreeBSD resets: its
// tcp_disconnect discards unread data and sends a FIN, and the RST is its
// answer to bytes that arrive after the descriptor is closed -- "packets
// received after close" in netstat -s. So the client closes first and the
// server writes afterwards.
func TestTheEndObserverSeesAReset(t *testing.T) {
	skipWhereTheObserverIsBlind(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ol := &endObserver{Listener: ln, ends: make(chan string, 1)}
	defer ol.Close()
	clientClosed := make(chan struct{})
	go func() {
		c, err := ol.Accept()
		if err != nil {
			return
		}
		<-clientClosed
		time.Sleep(50 * time.Millisecond) // the client's descriptor is gone
		_, _ = c.Write([]byte("late"))
		_ = c.Close()
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	close(clientClosed)
	select {
	case end := <-ol.ends:
		if !strings.HasPrefix(end, "RST") {
			t.Fatalf("bytes sent after the client closed were seen as %s, want RST", end)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the observer saw no end at all")
	}
}

// skipWhereTheObserverIsBlind skips on macOS, where the observer has not been
// shown to see a reset, so a FIN there would prove nothing. In CI on the PR
// for issue 757, the control saw a FIN on macOS when it counted an aborted
// read, and again with SO_ERROR read before close; both made it pass on
// Linux and Windows (DIVERGENCE 5.681). The drain still runs on macOS in
// every test that dials through Client; only its FIN is not checked there.
func skipWhereTheObserverIsBlind(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("on darwin the end observer has not been shown to see a reset " +
			"(neither an aborted read nor SO_ERROR reported one in CI), " +
			"so a FIN here would prove nothing")
	}
}

// endObserver reports how each accepted connection's peer ended it. A
// connection's Close only half-closes, then watches the socket for its whole
// window, so the peer's FIN or RST lands on a live socket rather than on a
// closed one, where nothing can see it.
//
// It watches past the FIN on purpose. An RST here follows the peer's FIN --
// the peer closed, and then this end's close_notify reached it -- so a probe
// that stopped at the first EOF would report FIN for most resets. The verdict
// is RST if a reset arrives at any point in the window.
type endObserver struct {
	net.Listener
	ends chan string
}

func (l *endObserver) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &observedConn{TCPConn: c.(*net.TCPConn), ends: l.ends}, nil
}

// observeWindow is how long a closed connection is watched for a reset. The
// reset is the peer's answer to one segment, so it arrives within a round
// trip; on loopback that is microseconds.
const observeWindow = 100 * time.Millisecond

type observedConn struct {
	*net.TCPConn
	ends chan string
	once sync.Once
	mu   sync.Mutex
	fin  bool
	rst  bool
	// why is what the read that saw the reset returned, so a failure names
	// the platform's own word for it.
	why string
}

func (c *observedConn) Read(p []byte) (int, error) {
	n, err := c.TCPConn.Read(p)
	c.note(err)
	return n, err
}

func (c *observedConn) note(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case err == nil, errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, net.ErrClosed):
	case errors.Is(err, io.EOF):
		c.fin = true
	default:
		// Any other failure of a read is the connection aborted under it,
		// and the platforms do not agree on the word. ECONNRESET was all this
		// matched at first, and CI showed the control seeing no reset on
		// Linux, macOS or Windows (DIVERGENCE 5.681): a reset arriving after
		// this end's own FIN is reported differently there. Counting only
		// one errno made the observer blind to the very thing it watches
		// for, and the FIN tests pass on a blind observer.
		c.rst = true
		if c.why == "" {
			c.why = abortReason(err)
		}
	}
}

// abortReason is the errno a read failed with, or its error when there is
// none, without the addresses that would make every connection's differ.
func abortReason(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return fmt.Sprintf("%v, errno %d", errno, uintptr(errno))
	}
	return fmt.Sprintf("%T", err)
}

func (c *observedConn) Close() error {
	c.once.Do(func() {
		_ = c.TCPConn.CloseWrite()
		go func() {
			deadline := time.Now().Add(observeWindow)
			buf := make([]byte, 4096)
			for time.Now().Before(deadline) {
				_ = c.TCPConn.SetReadDeadline(deadline)
				_, err := c.TCPConn.Read(buf)
				c.note(err)
				if errors.Is(err, io.EOF) {
					time.Sleep(5 * time.Millisecond) // an RST may still come
				} else if err != nil {
					break
				}
			}
			// A reset a read never reported may still be waiting on the
			// socket; see pendingSocketError.
			if err := pendingSocketError(c.TCPConn); err != nil {
				c.note(err)
			}
			_ = c.TCPConn.Close()
			c.mu.Lock()
			defer c.mu.Unlock()
			switch {
			case c.rst:
				c.ends <- "RST (" + c.why + ")"
			case c.fin:
				c.ends <- "FIN"
			default:
				c.ends <- "neither"
			}
		}()
	})
	return nil
}

// The same holds when the hub's idle timeout is the one that fires, which with
// both ends on IdleStreamTimeout it is about as often as the node's. The node
// is still the end that closes: an HTTP/2 server reaps an idle connection with
// a GOAWAY and waits, and a client with no stream open closes on receiving it.
// So the client's drain covers this case too, and nothing is needed on the
// hub -- which a server-side drain was tried for, and could not be shown to
// change: its test passed with it removed (DIVERGENCE 5.681).
func TestAConnectionTheHubReapsEndsInAFinNotAReset(t *testing.T) {
	skipWhereTheObserverIsBlind(t)
	f := newFixture(t, nil) // for its CA and a node certificate only
	pair, _ := f.enrolledPair(t, "web1.example")
	hubKey, _ := pki.GenerateKey(pki.ECDSAP256)
	hubDER, err := f.ca.IssueHub(hubKey, []string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, _ := pki.EncodeKey(hubKey)
	cert, err := tls.X509KeyPair(pki.EncodeCert(hubDER), keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ol := &endObserver{Listener: tcp, ends: make(chan string, 16)}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }),
		// The hub's timer, made short so that it fires first; the client's
		// stays IdleStreamTimeout.
		IdleTimeout: 50 * time.Millisecond,
	}
	go srv.Serve(tls.NewListener(ol, ServerConfig(cert, f.ca.Cert, NewDenylist())))
	t.Cleanup(func() { srv.Close() })

	counts := map[string]int{}
	for range 10 {
		client := &Client{HubURL: "https://" + tcp.Addr().String(), CA: f.ca.Cert, Cert: &pair, ServerName: "localhost", Timeout: 5 * time.Second}
		if _, err := client.Health(context.Background()); err != nil {
			t.Fatal(err)
		}
		select {
		case end := <-ol.ends:
			counts[end]++
		case <-time.After(5 * time.Second):
			t.Fatal("the hub saw no end to the connection 5 s after its idle timeout")
		}
	}
	if counts["FIN"] != 10 {
		t.Errorf("connection ends seen by the hub: %v, want 10 FIN", counts)
	}
}
