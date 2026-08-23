package hub

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

// Listen opens the hub's one TCP port with the transport's TLS
// configuration.
//
// Separate from Serve so that a caller -- a test, or a `serve` that
// wants to report the address it actually got -- can see the listener.
func Listen(addr string, cert tls.Certificate, ca *x509.Certificate, denied *transport.Denylist) (net.Listener, error) {
	if addr == "" {
		addr = fmt.Sprintf(":%d", transport.DefaultPort)
	}
	ln, err := tls.Listen("tcp", addr, transport.ServerConfig(cert, ca, denied))
	if err != nil {
		return nil, fmt.Errorf("listening on %s: %w", addr, err)
	}
	return ln, nil
}

// Serve runs the control plane until the context ends.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler: s.Handler(),
		// SPEC 6.5's handshake timeout. There is deliberately no write
		// timeout: the subscribe stream is meant to stay open, and a
		// write deadline would cut it at a fixed age.
		ReadHeaderTimeout: transport.HandshakeTimeout,
		IdleTimeout:       transport.IdleStreamTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ErrorLog:          nil,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	err := srv.Serve(ln)

	// Serve returns the moment Shutdown closes the listener, while the
	// handlers it started are still running -- and the work the hub
	// started for itself, a queued job going out to a node that has
	// just reconnected, is not a request and is not drained by Shutdown
	// at all. A caller that waits for Serve is waiting to know both
	// have finished.
	//
	// Only on a context that has ended: a listener closed from under a
	// running hub leaves the subscribe streams open, and waiting for
	// those would never return.
	if ctx.Err() != nil {
		s.requests.wait()
		s.background.Wait()
	}

	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
