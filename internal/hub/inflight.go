package hub

import (
	"net/http"
	"sync"
)

// inflight counts the requests a hub is serving.
//
// `http.Server.Shutdown` drains requests too, but Serve returns the
// moment Shutdown closes the listener, so a caller that waits for Serve
// learns the hub has stopped while a handler is still writing a return
// into the job cache. Waiting for Shutdown itself would also wait for
// every connection to go quiet, which is a different and much slower
// question -- about a second, in a process that starts one hub per test.
//
// This waits for the handlers and nothing else.
type inflight struct {
	mu   sync.Mutex
	n    int
	idle chan struct{}
}

func (f *inflight) enter() {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
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

// wait blocks until no handler is running.
func (f *inflight) wait() {
	f.mu.Lock()
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
		f.enter()
		defer f.leave()
		next.ServeHTTP(w, r)
	})
}
