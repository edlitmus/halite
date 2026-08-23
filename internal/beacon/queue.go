package beacon

import (
	"context"
	"sync"
	"time"
)

// queued is one event waiting to be sent.
type queued struct {
	tag  string
	data map[string]any
	// key is what two events have to share to be the same event: the
	// beacon and the digest of what it said.
	key string
	at  time.Time
	// window is how long this event waits for a twin before it goes.
	window time.Duration
	// count is how many events this one stands for.
	count int
}

// ready reports whether the coalescing window has closed.
func (q queued) ready(now time.Time) bool { return now.Sub(q.at) >= q.window }

// coalescingQueue is the bounded queue of SPEC 16.3, with the
// coalescing of identical events built into the push.
//
// Bounded and dropping the oldest, rather than a channel that blocks:
// blocking would push the backpressure into the beacon's poll loop and
// then into the node, and a watcher that stops watching because it is
// busy is the failure mode this exists to avoid. Loss is reported.
type coalescingQueue struct {
	mu    sync.Mutex
	cond  *sync.Cond
	items []queued
	limit int
}

func newCoalescingQueue(limit int) *coalescingQueue {
	if limit < 1 {
		limit = DefaultQueueDepth
	}
	q := &coalescingQueue{limit: limit}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// push adds an event, collapsing it into an identical one that is still
// inside its window, and reports how many were dropped to make room.
func (q *coalescingQueue) push(item queued) int {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i := range q.items {
		if q.items[i].key != item.key {
			continue
		}
		if item.at.Sub(q.items[i].at) > q.items[i].window {
			continue
		}
		// Same tag, same significant payload, inside the window: one
		// event carrying a count.
		q.items[i].count++
		q.cond.Broadcast()
		return 0
	}

	dropped := 0
	for len(q.items) >= q.limit {
		q.items = q.items[1:]
		dropped++
	}
	item.count = 1
	q.items = append(q.items, item)
	q.cond.Broadcast()
	return dropped
}

// pop blocks until an event's window has closed, or the context ends.
func (q *coalescingQueue) pop(ctx context.Context, now func() time.Time) (queued, bool) {
	stop := context.AfterFunc(ctx, func() {
		q.mu.Lock()
		q.cond.Broadcast()
		q.mu.Unlock()
	})
	defer stop()

	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if ctx.Err() != nil {
			return queued{}, false
		}
		if len(q.items) > 0 && q.items[0].ready(now()) {
			item := q.items[0]
			q.items = q.items[1:]
			return item, true
		}
		q.cond.Wait()
	}
}

// wake releases a waiter whose head-of-queue window has closed.
//
// The head becomes ready with the clock rather than with an event, so
// nothing signals the condition when a window closes. One waker
// running for the engine's lifetime does it, rather than a goroutine
// per wait.
func (q *coalescingQueue) wake(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			q.mu.Lock()
			q.cond.Broadcast()
			q.mu.Unlock()
		case <-ctx.Done():
			return
		}
	}
}

// Len reports the depth, for a metric and for a test.
func (q *coalescingQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}
