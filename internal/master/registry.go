package master

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/sls"
	"github.com/edlitmus/halite/internal/transport"
)

// agentState is one enrolled host: what the control plane knows about it
// and the work waiting for it. Jobs queue per agent rather than in one
// fleet-wide list so a host that is down does not hold up the others.
type agentState struct {
	info   transport.Agent
	queue  []transport.Job
	notify chan struct{} // buffered(1): a nudge, never a payload
}

// jobState is a dispatched job and the answers that have come back.
type jobState struct {
	job       transport.Job
	expecting []string
	results   map[string]transport.JobResult
}

// registry holds all mutable control plane state behind one mutex. The
// fleet sizes this targets do not justify anything finer-grained.
type registry struct {
	mu     sync.Mutex
	agents map[string]*agentState
	jobs   map[string]*jobState

	// onlineAfter is how recently an agent must have polled to count as
	// online for targeting and listing.
	onlineAfter time.Duration

	// jobTTL bounds how stale queued work may be before it is dropped. An
	// agent that was down when a job was dispatched should come back to a
	// clean slate, not replay an operator's hours-old intent.
	jobTTL time.Duration
}

func newRegistry(onlineAfter, jobTTL time.Duration) *registry {
	return &registry{
		agents:      map[string]*agentState{},
		jobs:        map[string]*jobState{},
		onlineAfter: onlineAfter,
		jobTTL:      jobTTL,
	}
}

// touch records that an agent is present, creating its entry on first
// contact. Grains are replaced wholesale when supplied: they are facts
// about the host as it is now, not an accumulation.
func (r *registry) touch(id string, grains map[string]any, version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.agents[id]
	if !ok {
		state = &agentState{notify: make(chan struct{}, 1)}
		r.agents[id] = state
	}
	state.info.ID = id
	state.info.LastSeen = time.Now()
	if grains != nil {
		// The identity comes from the client certificate, so an agent
		// cannot target-spoof by reporting someone else's id grain.
		grains["id"] = id
		state.info.Grains = grains
	}
	if version != "" {
		state.info.Version = version
	}
}

// grainsOf returns an agent's last reported facts.
func (r *registry) grainsOf(id string) (map[string]any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.agents[id]
	if !ok || state.info.Grains == nil {
		return nil, false
	}
	return state.info.Grains, true
}

// list returns every known agent, newest contact first.
func (r *registry) list() []transport.Agent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]transport.Agent, 0, len(r.agents))
	for _, state := range r.agents {
		info := state.info
		info.Online = time.Since(info.LastSeen) < r.onlineAfter
		out = append(out, info)
	}
	return out
}

// take removes and returns the live work queued for an agent, discarding
// anything that has outlived jobTTL.
func (r *registry) take(id string) []transport.Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.agents[id]
	if !ok || len(state.queue) == 0 {
		return nil
	}
	queued := state.queue
	state.queue = nil

	var jobs []transport.Job
	for _, job := range queued {
		if time.Since(job.Created) < r.jobTTL {
			jobs = append(jobs, job)
		}
	}
	return jobs
}

// waiter returns the channel an agent's long poll blocks on.
func (r *registry) waiter(id string) <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.agents[id]
	if !ok {
		state = &agentState{notify: make(chan struct{}, 1)}
		state.info.ID = id
		r.agents[id] = state
	}
	return state.notify
}

// dispatch queues a job for every online agent the target matches, and
// returns the ids it was queued for.
func (r *registry) dispatch(job transport.Job) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var matched []string
	for id, state := range r.agents {
		if time.Since(state.info.LastSeen) >= r.onlineAfter {
			continue
		}
		if !sls.TargetMatch(job.Target, state.info.Grains) {
			continue
		}
		matched = append(matched, id)
		state.queue = append(state.queue, job)
		select {
		case state.notify <- struct{}{}:
		default: // a nudge is already pending; the poller will see the queue
		}
	}
	r.jobs[job.ID] = &jobState{
		job:       job,
		expecting: matched,
		results:   map[string]transport.JobResult{},
	}
	return matched
}

// record stores one agent's result, refusing results for jobs it was never
// sent — an agent may only answer what it was asked.
func (r *registry) record(res transport.JobResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.jobs[res.JobID]
	if !ok {
		return fmt.Errorf("unknown job %q", res.JobID)
	}
	for _, id := range state.expecting {
		if id == res.AgentID {
			state.results[res.AgentID] = res
			return nil
		}
	}
	return fmt.Errorf("job %q was not dispatched to %q", res.JobID, res.AgentID)
}

// jobInfo returns a job and its results so far.
func (r *registry) jobInfo(id string) (transport.JobInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.jobs[id]
	if !ok {
		return transport.JobInfo{}, false
	}
	info := transport.JobInfo{Job: state.job, Expecting: state.expecting}
	for _, agentID := range state.expecting {
		if res, ok := state.results[agentID]; ok {
			info.Results = append(info.Results, res)
		}
	}
	return info, true
}

// newJobID is time-ordered so that listing jobs sorts chronologically,
// with a random tail so two dispatches in the same microsecond differ.
func newJobID() string {
	var suffix [3]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		// A job id only has to be unique, not unpredictable; the timestamp
		// alone still is, in practice.
		return time.Now().UTC().Format("20060102150405.000000")
	}
	return time.Now().UTC().Format("20060102150405.000000") + "-" + hex.EncodeToString(suffix[:])
}
