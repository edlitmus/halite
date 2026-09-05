package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Pool holds running extension processes.
//
// SPEC 24.2: concurrency is by process pool rather than in-process
// threading. Each process answers one call at a time, and a caller that
// finds none free starts one up to the bound — so a slow extension
// costs latency and a bounded amount of memory, rather than an
// unbounded number of processes or a queue behind one.
type Pool struct {
	opts Options
	// Size is how many processes may exist. Zero takes DefaultPoolSize.
	Size int

	mu    sync.Mutex
	idle  []*Process
	live  int
	info  Info
	known bool
	// waiters are callers waiting for a process to come free.
	waiters []chan *Process
	closed  bool
}

// DefaultPoolSize is small on purpose. An extension is somebody else's
// code and each copy is a process; an estate that needs more says so.
const DefaultPoolSize = 4

// NewPool answers with a pool that has started nothing.
func NewPool(opts Options, size int) *Pool {
	if size <= 0 {
		size = DefaultPoolSize
	}
	return &Pool{opts: opts, Size: size}
}

// Info is what the extension declared, from the first process started.
//
// It needs one to have run: the declarations come from the handshake,
// and a pool that has started nothing has nothing to report. Call
// `Warm` first when the answer is needed before any work.
func (p *Pool) Info() (Info, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.info, p.known
}

// Warm starts one process, so the handshake has happened and the
// extension's declarations are known.
func (p *Pool) Warm(ctx context.Context) error {
	proc, err := p.acquire(ctx)
	if err != nil {
		return err
	}
	p.release(proc)
	return nil
}

// Call runs one function on a free process.
func (p *Pool) Call(ctx context.Context, function string, args, kwargs any, callCtx *CallContext) (json.RawMessage, error) {
	proc, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	value, err := proc.Call(ctx, function, args, kwargs, callCtx)
	p.release(proc)
	return value, err
}

// acquire takes a free process, starts one, or waits for one.
func (p *Pool) acquire(ctx context.Context) (*Process, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("this extension pool is closed")
	}
	if n := len(p.idle); n > 0 {
		proc := p.idle[n-1]
		p.idle = p.idle[:n-1]
		p.mu.Unlock()
		return proc, nil
	}
	if p.live < p.Size {
		p.live++
		p.mu.Unlock()
		proc, err := Start(ctx, p.opts)
		if err != nil {
			p.mu.Lock()
			p.live--
			p.mu.Unlock()
			return nil, err
		}
		p.mu.Lock()
		if !p.known {
			p.info, p.known = proc.Info(), true
		}
		p.mu.Unlock()
		return proc, nil
	}
	// Every process is busy. Wait for one, bounded by the caller's
	// context and by the call timeout: a caller blocked for ever on a
	// pool of hung extensions is the hang this design exists to
	// prevent, one level up.
	wait := make(chan *Process, 1)
	p.waiters = append(p.waiters, wait)
	p.mu.Unlock()

	timeout := p.opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case proc := <-wait:
		if proc == nil {
			return nil, fmt.Errorf("this extension pool is closed")
		}
		return proc, nil
	case <-timer.C:
		p.forget(wait)
		return nil, fmt.Errorf("%w: every %s process was busy for %s", ErrTimeout, p.opts.Path, timeout)
	case <-ctx.Done():
		p.forget(wait)
		return nil, ctx.Err()
	}
}

// release returns a process, or replaces it if it died.
//
// A process killed for a protocol violation or a timeout is not reused:
// it is gone, and the next caller starts a fresh one. That is the point
// of the pool being processes — recovery is a fork, not a reset.
func (p *Pool) release(proc *Process) {
	if dead, _ := proc.Dead(); dead {
		p.mu.Lock()
		p.live--
		p.mu.Unlock()
		p.wake(nil)
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		proc.Close()
		return
	}
	if len(p.waiters) > 0 {
		wait := p.waiters[0]
		p.waiters = p.waiters[1:]
		p.mu.Unlock()
		wait <- proc
		return
	}
	p.idle = append(p.idle, proc)
	p.mu.Unlock()
}

// wake gives a waiting caller the chance to start a process of its own,
// after one died.
func (p *Pool) wake(proc *Process) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.waiters) == 0 {
		return
	}
	wait := p.waiters[0]
	p.waiters = p.waiters[1:]
	// A nil hands the waiter nothing and lets it time out into starting
	// one; closing the channel would look like a closed pool.
	select {
	case wait <- proc:
	default:
	}
}

func (p *Pool) forget(wait chan *Process) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, w := range p.waiters {
		if w == wait {
			p.waiters = append(p.waiters[:i], p.waiters[i+1:]...)
			return
		}
	}
}

// Close stops every process.
func (p *Pool) Close() {
	p.mu.Lock()
	p.closed = true
	idle := p.idle
	p.idle = nil
	waiters := p.waiters
	p.waiters = nil
	p.mu.Unlock()

	for _, w := range waiters {
		w <- nil
	}
	for _, proc := range idle {
		proc.Close()
	}
}
