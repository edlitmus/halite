package transport

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// ShortRequestLimit is how many short requests one Client keeps in
// flight to the hub at once: MaxConcurrentStreams less the one stream a
// node's subscribe holds. SPEC 6.5, and DIVERGENCE 5.665.3.
//
// Enforced here, by the client, because the hub's half of the limit
// cannot be relied on to protect anybody. A Go client on a cold
// connection has not yet read the hub's SETTINGS and assumes 100
// streams, so a burst of 65 or more starts before it knows the limit is
// 64. The hub resets the streams past 64, and once its SETTINGS ACK has
// landed it resets them with PROTOCOL_ERROR, which net/http does not
// retry: between one burst in fifty and one in five hundred lost a
// request outright (measured, 5.665.2 and 5.665.3).
// http.HTTP2Config.StrictMaxConcurrentRequests is no cure -- it keeps
// the loss, because it trusts the same assumed 100, and adds a stall of
// most of the burst in one to three bursts in a hundred. A request that
// is never issued cannot be reset, so the 64th waits here instead.
//
// The price is that the bound is per client, not per connection: a
// client at the limit no longer gets the second connection 5.639
// measured Go opening, and the API and a relay, which forward many
// callers through one Client, queue at 63 rather than spreading out.
const ShortRequestLimit = MaxConcurrentStreams - 1

// longLivedKey marks a request that is meant to stay open: the
// subscribe stream, and an event stream that follows the bus.
type longLivedKey struct{}

// longLived marks ctx's request as one that does not take a slot.
//
// Both kinds are kept out, for different reasons. The subscribe stream
// is the one a node cannot operate without, so a burst of returns and
// file fetches must never be able to make it wait (the hazard 748
// named before any of this was built); the one stream ShortRequestLimit
// leaves free is its room. A following event stream holds its stream
// for as long as an operator watches, and the API opens one through its
// single Client per watcher with no limit of its own: counted, 63
// watchers would have stopped every other request the API makes.
//
// What that leaves unbounded is a burst of long-lived streams on a cold
// connection, which no caller makes: a node opens one subscribe, and
// watchers arrive one operator at a time.
func longLived(ctx context.Context) context.Context {
	return context.WithValue(ctx, longLivedKey{}, true)
}

func isLongLived(ctx context.Context) bool {
	marked, _ := ctx.Value(longLivedKey{}).(bool)
	return marked
}

// streamLimited holds every short request to one of a Client's slots
// from before it is issued until its response body is finished with,
// which is when the HTTP/2 stream it used is.
type streamLimited struct {
	slots chan struct{}
	next  http.RoundTripper
}

// CloseIdleConnections passes through; see tracePropagating's.
func (s streamLimited) CloseIdleConnections() {
	if c, ok := s.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

func (s streamLimited) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	if isLongLived(ctx) {
		return s.next.RoundTrip(req)
	}
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		// Named, because the request never left this process: a
		// timeout reported as the hub's would send somebody to look
		// at the wrong machine.
		return nil, fmt.Errorf("waiting for one of this client's %d request slots: %w", cap(s.slots), ctx.Err())
	}
	release := sync.OnceFunc(func() { <-s.slots })
	res, err := s.next.RoundTrip(req)
	if err != nil {
		release()
		return nil, err
	}
	res.Body = &releasingBody{ReadCloser: res.Body, release: release}
	return res, nil
}

// releasingBody gives the slot back at the end of the body or at Close,
// whichever comes first. Every caller in this package closes, so Close
// is the guarantee; the end of the body is the earlier moment, and the
// stream really is finished then.
type releasingBody struct {
	io.ReadCloser
	release func()
}

func (b *releasingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.release()
	}
	return n, err
}

func (b *releasingBody) Close() error {
	err := b.ReadCloser.Close()
	b.release()
	return err
}
