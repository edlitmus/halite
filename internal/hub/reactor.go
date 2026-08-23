package hub

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/value"
)

// Reactor is the output side of the automation loop: it reads the bus
// and runs what the configuration says an event means.
//
// Salt's reactor is single-threaded and serialized, so a burst of
// events becomes a backlog and the backlog becomes an outage. It is the
// most common scaling failure in a Salt estate, and SPEC 18.2 is
// explicit about not repeating it: a worker pool, ordering only where
// ordering is needed, a bounded queue that drops the oldest and says
// so, and per-glob debounce, deduplication, and rate limiting.
type Reactor struct {
	Server  *Server
	Entries []ReactorEntry

	// Workers is the pool size, default 2 x NumCPU.
	Workers int
	// QueueDepth is the total bounded queue, default 10,000.
	QueueDepth int
	// MaxDepth breaks a causality chain longer than this, default 5.
	MaxDepth int
	// Timeout bounds rendering and dispatch for one reaction.
	Timeout time.Duration
	// OffsetFile is where the reactor remembers where it had read to,
	// so a restart is lossless rather than a gap nobody notices.
	OffsetFile string
	// Now is the clock, for the tests.
	Now func() time.Time

	workers []*reactorWorker
	limits  map[string]*reactorLimit
	mu      sync.Mutex
	// chains counts how many reactions a causality chain has caused.
	chains map[string]*chainCount

	// Handled is called after each event is processed, for a test that
	// needs to know the work is done. Nil in production.
	Handled func(tag string, results []ReactionResult)
}

func (r *Reactor) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reactor) workerCount() int {
	if r.Workers > 0 {
		return r.Workers
	}
	return 2 * runtime.NumCPU()
}

func (r *Reactor) queueDepth() int {
	if r.QueueDepth > 0 {
		return r.QueueDepth
	}
	return 10000
}

func (r *Reactor) maxDepth() int {
	if r.MaxDepth > 0 {
		return r.MaxDepth
	}
	return 5
}

func (r *Reactor) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return reactionTimeout
}

// chainCount is how many reactions one causality chain has caused, and
// when it was last seen.
type chainCount struct {
	n    int
	seen time.Time
}

// reactorLimit holds the per-glob controls: the token bucket, the
// deduplication window, and the debounce timer.
type reactorLimit struct {
	mu sync.Mutex
	// tokens and filled implement the bucket.
	tokens float64
	filled time.Time
	// seen is the deduplication window, keyed by the dedupe key.
	seen map[string]time.Time
	// pending is the debounce state, keyed by the dedupe key.
	pending map[string]*eventbus.Event
	timers  map[string]*time.Timer
}

// Run reads the bus and reacts until the context ends.
func (r *Reactor) Run(ctx context.Context) error {
	if r.Server == nil || r.Server.Events == nil {
		return fmt.Errorf("the reactor needs a hub with an event bus")
	}
	if len(r.Entries) == 0 {
		return nil
	}
	r.start(ctx)

	from := r.readOffset()
	tags := ReactorTags(r.Entries)
	bus := r.Server.Events
	for {
		wake := bus.Wait()
		events, next, err := bus.Read(from, tags, 500)
		if err != nil {
			// A bad offset must not wedge the reactor for ever: start
			// from what is there now and say so, because the
			// alternative is a reactor that has silently stopped.
			r.Server.warn("the reactor could not read from its offset; starting from the end",
				"offset", from, "error", err.Error())
			from = eventbus.Latest
			continue
		}
		for i := range events {
			r.Offer(&events[i])
		}
		if next != from {
			from = next
			r.writeOffset(from)
		}
		if len(events) > 0 {
			// More may be waiting; read again before sleeping.
			continue
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}

// start builds the worker pool.
func (r *Reactor) start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.workers != nil {
		return
	}
	r.limits = map[string]*reactorLimit{}
	r.chains = map[string]*chainCount{}
	n := r.workerCount()
	per := r.queueDepth() / n
	if per < 1 {
		per = 1
	}
	for i := 0; i < n; i++ {
		w := &reactorWorker{reactor: r, queue: newBoundedQueue(per)}
		r.workers = append(r.workers, w)
		go w.run(ctx)
	}
}

// Offer applies the SPEC 18.2 controls and hands the event to a worker.
func (r *Reactor) Offer(e *eventbus.Event) {
	// Every event the reactor acts on belongs to a chain, so that what
	// a reaction causes can be traced back and so that a loop is
	// countable. One that arrives without a chain starts its own,
	// named by where it sits on the log -- unique, and something an
	// operator can look up afterwards.
	if e.Correlation == "" {
		e.Correlation = e.Offset
	}
	if e.Correlation == "" {
		e.Correlation = string(r.Server.clock().Next())
	}
	for _, entry := range Matching(r.Entries, e.Tag) {
		r.offerTo(entry, e)
	}
}

func (r *Reactor) offerTo(entry ReactorEntry, e *eventbus.Event) {
	limit := r.limitFor(entry)
	key := dedupeKey(entry, e)

	if entry.RateLimit > 0 && !limit.allow(entry, r.now()) {
		r.Server.emitCorrelated("halite/reactor/throttled", e.Node, correlationOf(e), map[string]any{
			"tag": e.Tag, "reactor": entry.Tag, "rate_limit": entry.RateLimit,
		})
		return
	}
	if entry.DedupeWindow > 0 && limit.duplicate(key, entry.DedupeWindow, r.now()) {
		return
	}
	if entry.Debounce > 0 {
		limit.debounce(key, e, entry.Debounce, func(latest *eventbus.Event) {
			r.enqueue(entry, latest)
		})
		return
	}
	r.enqueue(entry, e)
}

// enqueue picks the worker and puts the event on its queue.
//
// Events in the same causality chain, or from the same node when the
// chain is unnamed, hash to a fixed worker so they are processed in
// order. Unrelated events proceed in parallel, which is the whole point
// of the pool.
func (r *Reactor) enqueue(entry ReactorEntry, e *eventbus.Event) {
	if r.overLength(e) {
		r.Server.warn("breaking a reactor causality chain",
			"tag", e.Tag, "correlation", correlationOf(e), "max_depth", r.maxDepth())
		r.Server.emitCorrelated("halite/reactor/loop", e.Node, correlationOf(e), map[string]any{
			"tag": e.Tag, "reactor": entry.Tag, "max_causality_depth": int64(r.maxDepth()),
		})
		return
	}
	key := e.Correlation
	if key == "" {
		key = e.Node
	}
	w := r.workers[hashIndex(key, len(r.workers))]
	if dropped := w.queue.push(reactorJob{entry: entry, event: e}); dropped > 0 {
		r.Server.warn("the reactor queue overflowed", "dropped", dropped, "tag", e.Tag)
		r.Server.emit("halite/reactor/overflow", "", map[string]any{
			"dropped": int64(dropped), "tag": e.Tag, "reactor": entry.Tag,
		})
	}
}

// overLength reports whether a chain has caused more reactions than it
// is allowed to. SPEC 16.3: a file that changes in a loop fires a
// beacon that fires a reactor that changes the file.
func (r *Reactor) overLength(e *eventbus.Event) bool {
	chain := e.Correlation
	if chain == "" {
		// An event with no chain starts one, and cannot be part of a
		// loop yet.
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	// Chains are forgotten after a while, or a busy estate would
	// accumulate one entry per job for ever.
	for k, c := range r.chains {
		if now.Sub(c.seen) > 10*time.Minute {
			delete(r.chains, k)
		}
	}
	c, ok := r.chains[chain]
	if !ok {
		c = &chainCount{}
		r.chains[chain] = c
	}
	c.n++
	c.seen = now
	return c.n > r.maxDepth()
}

func (r *Reactor) limitFor(entry ReactorEntry) *reactorLimit {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.limits[entry.Tag]
	if !ok {
		l = &reactorLimit{
			seen:    map[string]time.Time{},
			pending: map[string]*eventbus.Event{},
			timers:  map[string]*time.Timer{},
			tokens:  float64(entry.RateBurst),
			filled:  r.now(),
		}
		r.limits[entry.Tag] = l
	}
	return l
}

// allow is the token bucket.
func (l *reactorLimit) allow(entry ReactorEntry, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	burst := float64(entry.RateBurst)
	if burst < 1 {
		burst = 1
	}
	if l.filled.IsZero() {
		l.filled, l.tokens = now, burst
	}
	l.tokens += now.Sub(l.filled).Seconds() * entry.RateLimit
	if l.tokens > burst {
		l.tokens = burst
	}
	l.filled = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

// duplicate reports whether this key was seen inside the window.
func (l *reactorLimit) duplicate(key string, window time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, at := range l.seen {
		if now.Sub(at) > window {
			delete(l.seen, k)
		}
	}
	if at, ok := l.seen[key]; ok && now.Sub(at) <= window {
		return true
	}
	l.seen[key] = now
	return false
}

// debounce holds the latest event for a key and runs once the burst has
// stopped.
func (l *reactorLimit) debounce(key string, e *eventbus.Event, window time.Duration, run func(*eventbus.Event)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pending[key] = e
	if t, ok := l.timers[key]; ok {
		t.Reset(window)
		return
	}
	l.timers[key] = time.AfterFunc(window, func() {
		l.mu.Lock()
		latest := l.pending[key]
		delete(l.pending, key)
		delete(l.timers, key)
		l.mu.Unlock()
		if latest != nil {
			run(latest)
		}
	})
}

// dedupeKey is what "the same event twice" means for an entry.
func dedupeKey(entry ReactorEntry, e *eventbus.Event) string {
	if entry.DedupeKey == "" {
		return e.Tag
	}
	if v, ok := value.Traverse(payloadMap(e), entry.DedupeKey, ":"); ok {
		return e.Tag + "\x00" + value.KeyString(v)
	}
	return e.Tag
}

func payloadMap(e *eventbus.Event) *value.Map {
	out := value.NewMap(len(e.Data))
	for _, k := range sortedKeys(e.Data) {
		out.Set(k, e.Data[k])
	}
	return out
}

func hashIndex(key string, n int) int {
	if n <= 1 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() % uint32(n))
}

// reactorJob is one event matched to one entry.
type reactorJob struct {
	entry ReactorEntry
	event *eventbus.Event
}

// reactorWorker runs the reactions for the events hashed to it.
type reactorWorker struct {
	reactor *Reactor
	queue   *boundedQueue
}

func (w *reactorWorker) run(ctx context.Context) {
	for {
		job, ok := w.queue.pop(ctx)
		if !ok {
			return
		}
		w.reactor.react(ctx, job)
	}
}

// react renders and dispatches one event's reactions.
func (r *Reactor) react(ctx context.Context, j reactorJob) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()

	var all []ReactionResult
	for _, file := range j.entry.SLS {
		doc, err := r.Server.renderReaction(file, j.event)
		if err != nil {
			r.reportError(j, file, err)
			continue
		}
		reactions, err := parseReactions(doc, file)
		if err != nil {
			r.reportError(j, file, err)
			continue
		}
		results := r.Server.runReactions(ctx, j.entry, j.event, reactions)
		for _, res := range results {
			if res.Succeeded() {
				r.Server.info("reaction dispatched",
					"tag", j.event.Tag, "reaction", res.Reaction.ID,
					"kind", res.Reaction.Kind, "fun", res.Reaction.Fun, "jid", res.JID)
				continue
			}
			r.Server.warn("a reaction did not dispatch",
				"tag", j.event.Tag, "reaction", res.Reaction.ID, "error", res.Error)
			r.Server.emitCorrelated("halite/reactor/error", j.event.Node, correlationOf(j.event),
				map[string]any{
					"tag": j.event.Tag, "file": file,
					"reaction": res.Reaction.ID, "error": res.Error,
				})
		}
		all = append(all, results...)
	}
	if r.Handled != nil {
		r.Handled(j.event.Tag, all)
	}
}

// reportError says that a reaction failed to render or parse.
//
// Never silently, which is what Salt does: an event that should have
// caused something and did not is invisible there, and the event does
// not come again.
func (r *Reactor) reportError(j reactorJob, file string, err error) {
	r.Server.warn("a reaction could not be prepared",
		"tag", j.event.Tag, "file", file, "error", err.Error())
	r.Server.emitCorrelated("halite/reactor/error", j.event.Node, correlationOf(j.event),
		map[string]any{"tag": j.event.Tag, "file": file, "error": err.Error()})
	if r.Handled != nil {
		r.Handled(j.event.Tag, []ReactionResult{{Error: err.Error()}})
	}
}

// readOffset is where the reactor had got to.
func (r *Reactor) readOffset() string {
	if r.OffsetFile == "" {
		return eventbus.Latest
	}
	raw, err := os.ReadFile(filepath.Clean(r.OffsetFile))
	if err != nil {
		// No file is a first start, and starting at the end is right:
		// reacting to a month of history on first boot would be worse
		// than missing what happened while there was no reactor.
		return eventbus.Latest
	}
	offset := strings.TrimSpace(string(raw))
	if offset == "" {
		return eventbus.Latest
	}
	return offset
}

func (r *Reactor) writeOffset(offset string) {
	if r.OffsetFile == "" {
		return
	}
	if err := writeAtomic(r.OffsetFile, []byte(offset+"\n"), 0o600); err != nil {
		r.Server.warn("could not record the reactor's position",
			"file", r.OffsetFile, "error", err.Error())
	}
}

// boundedQueue is a ring that drops the oldest when it is full.
//
// A channel would block the reader instead, which turns a burst into
// backpressure on the bus reader and then into a backlog -- the failure
// SPEC 18.2 names. Dropping the oldest and reporting the count is the
// stated behaviour: loss is reported, never silent.
type boundedQueue struct {
	mu    sync.Mutex
	cond  *sync.Cond
	items []reactorJob
	limit int
}

func newBoundedQueue(limit int) *boundedQueue {
	q := &boundedQueue{limit: limit}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// push adds an item and reports how many were dropped to make room.
func (q *boundedQueue) push(j reactorJob) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	dropped := 0
	for len(q.items) >= q.limit {
		q.items = q.items[1:]
		dropped++
	}
	q.items = append(q.items, j)
	q.cond.Signal()
	return dropped
}

// pop blocks until an item is available or the context ends.
func (q *boundedQueue) pop(ctx context.Context) (reactorJob, bool) {
	stop := context.AfterFunc(ctx, func() {
		q.mu.Lock()
		q.cond.Broadcast()
		q.mu.Unlock()
	})
	defer stop()

	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 {
		if ctx.Err() != nil {
			return reactorJob{}, false
		}
		q.cond.Wait()
	}
	j := q.items[0]
	q.items = q.items[1:]
	return j, true
}

// Len reports the queue depth, for a metric and for a test.
func (q *boundedQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// ReactorSummary describes the configured entries, for `reactor.list`.
func ReactorSummary(entries []ReactorEntry) *value.Map {
	out := value.NewMap(len(entries))
	for _, e := range entries {
		item := value.NewMap(6)
		item.Set("sls", stringList(e.SLS))
		item.Set("principal", e.Principal)
		if e.Debounce > 0 {
			item.Set("debounce", e.Debounce.String())
		}
		if e.DedupeWindow > 0 {
			item.Set("dedupe_window", e.DedupeWindow.String())
			if e.DedupeKey != "" {
				item.Set("dedupe_key", e.DedupeKey)
			}
		}
		if e.RateLimit > 0 {
			item.Set("rate_limit", e.RateLimit)
		}
		out.Set(e.Tag, item)
	}
	return out
}
