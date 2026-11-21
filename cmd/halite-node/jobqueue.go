package main

import (
	"context"
	"errors"

	"github.com/edlitmus/halite/internal/job"
)

// enqueue puts a job at the back of the queue, or refuses it when the
// queue is full. It does no checking of the job itself; Offer and the
// schedule have each done theirs.
func (e *executor) enqueue(p *pending) error {
	e.mu.Lock()
	if len(e.waiting) >= e.depth {
		e.mu.Unlock()
		return ErrQueueFull
	}
	e.waiting = append(e.waiting, p)
	e.mu.Unlock()
	e.signal()
	return nil
}

// signal wakes Run without ever blocking: one pending wake-up is as good
// as many, because Run looks at the whole queue each time.
func (e *executor) signal() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Depth is how many jobs are waiting, for a gauge. A running job is not
// waiting.
func (e *executor) Depth() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.waiting)
}

// Run works the queue until done closes.
//
// Each job runs on a goroutine of its own, so that Run is free to look
// at the queue again when one finishes. A job that is running when done
// closes is not interrupted, for the reason Cancel gives.
func (e *executor) Run(done <-chan struct{}) {
	for {
		for _, p := range e.startable() {
			go e.run(p)
		}
		select {
		case <-done:
			return
		case <-e.wake:
		}
	}
}

// startable takes from the front of the queue every job that may start
// now, and counts them as running.
func (e *executor) startable() []*pending {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*pending
	for len(e.waiting) > 0 && e.running == 0 {
		p := e.waiting[0]
		e.waiting = e.waiting[1:]
		e.running++
		out = append(out, p)
	}
	return out
}

// run executes one job and hands its return on, then frees its place.
//
// The return is handed on before the place is freed, as it was when the
// queue had one worker: the next job does not start until this one's
// return is on its way.
func (e *executor) run(p *pending) {
	j := p.job
	e.node.log.Info("running a job", "jid", string(j.JID), "fun", j.Fun)
	ret := e.node.executeJob(j)
	e.node.log.Info("job finished",
		"jid", string(j.JID), "fun", j.Fun,
		"success", ret.Success, "retcode", ret.RetCode, "duration_ms", ret.DurationMS)
	if !p.local {
		// Before the return goes to the hub, so that the node's account
		// of the outcome exists whether or not the hub ever receives one.
		e.node.recordJobResult(ret)
	}
	p.finish(ret)

	e.mu.Lock()
	e.running--
	e.mu.Unlock()
	e.signal()
}

// Cancel drops a job from the queue if it has not started.
//
// One already running is left alone: a state run interrupted halfway
// leaves a machine in neither the old state nor the new one, which is
// worse than finishing something an operator changed their mind about.
// The hub's `jobs kill` says so in its output.
//
// The channel this replaced was drained and refilled to take one job
// out, while Run read the same channel, and a refill that found it full
// again dropped a job without a word. Under the mutex there is no window.
func (e *executor) Cancel(id job.ID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, p := range e.waiting {
		if p.job.JID == id {
			e.waiting = append(e.waiting[:i], e.waiting[i+1:]...)
			return true
		}
	}
	return false
}

// errNotRunningJobs is a scheduled job on a node with no executor.
var errNotRunningJobs = errors.New("this node is not running jobs")

// runLocal runs a job this node raised itself through the same queue as
// the hub's, and waits for its return.
//
// It waits for as long as the job is queued and running, which is what
// the schedule's `maxrunning` counts. A job still queued when ctx ends is
// left to run, because a job that has been queued is one the node has
// agreed to do; ctx ending means the node is stopping, and so is Run.
func (n *node) runLocal(ctx context.Context, j *job.Job) (*job.Return, error) {
	if n.executor == nil {
		return nil, errNotRunningJobs
	}
	result := make(chan *job.Return, 1)
	finish := func(ret *job.Return) { result <- ret }
	if err := n.executor.enqueue(&pending{job: j, finish: finish, local: true}); err != nil {
		return nil, err
	}
	select {
	case ret := <-result:
		return ret, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
