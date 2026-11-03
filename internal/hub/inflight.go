package hub

import (
	"errors"
	"net/http"
	"sync"

	"github.com/edlitmus/halite/internal/transport"
)

// inflight counts the requests a hub is serving, and refuses the ones
// that arrive once it has started to stop.
//
// `http.Server.Shutdown` drains requests too, but Serve returns the
// moment Shutdown closes the listener, so a caller that waits for Serve
// learns the hub has stopped while a handler is still writing a return
// into the job cache. Waiting for Shutdown itself would also wait for
// every connection to go quiet, which is a different and much slower
// question -- about a second, in a process that starts one hub per test.
//
// Counting alone is not enough, and was the whole of this until CI run
// 41682208877 showed why. Closing the listener stops new connections,
// not new requests: one net/http has already read off a connection
// that was open starts its handler whenever the scheduler gets to it,
// and a handler that had not reached `enter` when `wait` looked was not
// counted, so `wait` saw nothing running and Serve returned. That
// handler then wrote a node's return into a job cache the caller had
// been told was finished with. So `wait` also shuts the door: a request
// that reaches `enter` after it is answered 503, which a node treats as
// worth retrying (transport.Permanent), and runs nothing.
//
// This waits for the handlers and nothing else.
type inflight struct {
	mu   sync.Mutex
	n    int
	idle chan struct{}
	// closed is set by wait and never cleared: a Server is served once.
	closed bool

	// arrived runs at the top of every request, before it is counted,
	// which is the window a request net/http has already read can sit
	// in while the hub decides it has stopped. Tests only.
	arrived func(*http.Request)
}

var errStopping = errors.New("this hub is stopping; send it again to the hub that replaces it")

// enter counts a handler, or reports that the hub is stopping and the
// handler must not run.
func (f *inflight) enter() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	f.n++
	return true
}

func (f *inflight) leave() {
	f.mu.Lock()
	f.n--
	if f.n == 0 && f.idle != nil {
		close(f.idle)
		f.idle = nil
	}
	f.mu.Unlock()
}

// wait refuses every handler that has not yet started, and blocks
// until none is running.
//
// The refusal and the count are read under the one lock, which is what
// makes it sound: a handler either entered before wait looked, and is
// waited for, or enters after, and is refused. There is no third case.
func (f *inflight) wait() {
	f.mu.Lock()
	f.closed = true
	if f.n == 0 {
		f.mu.Unlock()
		return
	}
	ch := make(chan struct{})
	f.idle = ch
	f.mu.Unlock()
	<-ch
}

// track counts a handler for the whole time it runs.
func (f *inflight) track(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.arrived != nil {
			f.arrived(r)
		}
		if !f.enter() {
			// The connection is not worth keeping: nothing on it will
			// be served again.
			w.Header().Set("Connection", "close")
			transport.WriteError(w, http.StatusServiceUnavailable, transport.CodeInternal, errStopping)
			return
		}
		defer f.leave()
		next.ServeHTTP(w, r)
	})
}
