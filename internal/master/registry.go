package master

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/orch"
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
	// reactor records that this job was the reactor's own work, so the
	// events it produces can be marked and not fed back to the reactor.
	reactor bool
}

// registry holds all mutable control plane state behind one mutex. The
// fleet sizes this targets do not justify anything finer-grained.
type registry struct {
	mu     sync.Mutex
	agents map[string]*agentState
	jobs   map[string]*jobState
	// mine is function -> agent -> latest value.
	mine map[string]map[string]transport.MineEntry
	// orchestrations are runs started on this control plane.
	orchestrations map[string]*transport.OrchInfo

	// onlineAfter is how recently an agent must have polled to count as
	// online for targeting and listing.
	onlineAfter time.Duration

	// jobTTL bounds how stale queued work may be before it is dropped. An
	// agent that was down when a job was dispatched should come back to a
	// clean slate, not replay an operator's hours-old intent.
	jobTTL time.Duration
}

// maxJobs and maxOrchestrations bound the finished-work records a
// long-lived master keeps for `halite run`/`halite orchestrate` queries. A
// reactor dispatching around the clock must not grow the control plane
// without bound; the oldest records are evicted first.
const (
	maxJobs           = 4096
	maxOrchestrations = 512
)

// mineTTL is how long after an agent's last contact its mine entries are
// still served. Facts stay valid while the host keeps checking in, however
// rarely it republishes; a host that has vanished must not keep feeding its
// address into other hosts' templates forever.
const mineTTL = time.Hour

func newRegistry(onlineAfter, jobTTL time.Duration) *registry {
	return &registry{
		agents:         map[string]*agentState{},
		jobs:           map[string]*jobState{},
		mine:           map[string]map[string]transport.MineEntry{},
		orchestrations: map[string]*transport.OrchInfo{},
		onlineAfter:    onlineAfter,
		jobTTL:         jobTTL,
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
func (r *registry) dispatch(job transport.Job, byReactor bool) []string {
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
		reactor:   byReactor,
	}
	r.pruneJobsLocked()
	return matched
}

// pruneJobsLocked evicts the oldest job records once the map exceeds
// maxJobs. Job IDs sort chronologically, so the oldest are the smallest.
// The caller must hold r.mu.
func (r *registry) pruneJobsLocked() {
	if len(r.jobs) <= maxJobs {
		return
	}
	ids := make([]string, 0, len(r.jobs))
	for id := range r.jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids[:len(ids)-maxJobs] {
		delete(r.jobs, id)
	}
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

// jobOf returns the job a result answers and whether the reactor caused
// it, for returners and for marking the events it produces.
func (r *registry) jobOf(id string) (job transport.Job, byReactor, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, found := r.jobs[id]
	if !found {
		return transport.Job{}, false, false
	}
	return state.job, state.reactor, true
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

// storeMine records one agent's latest value for one function, replacing
// whatever was there: the mine holds current facts, not a history.
func (r *registry) storeMine(agentID, function string, data map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	byAgent, ok := r.mine[function]
	if !ok {
		byAgent = map[string]transport.MineEntry{}
		r.mine[function] = byAgent
	}
	byAgent[agentID] = transport.MineEntry{Data: data, Updated: time.Now().UTC()}
}

// readMine returns the mine, optionally narrowed to one function and to
// agents matching a target pattern.
func (r *registry) readMine(function, target string) transport.Mine {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := transport.Mine{}
	for fn, byAgent := range r.mine {
		if function != "" && fn != function {
			continue
		}
		for agentID, entry := range byAgent {
			// Serve facts only for hosts still checking in: a decommissioned
			// agent's entries expire rather than persisting forever.
			state, known := r.agents[agentID]
			if !known || time.Since(state.info.LastSeen) > mineTTL {
				continue
			}
			if target != "" && target != "*" {
				if !sls.TargetMatch(target, state.info.Grains) {
					continue
				}
			}
			if out[fn] == nil {
				out[fn] = map[string]transport.MineEntry{}
			}
			out[fn][agentID] = entry
		}
	}
	return out
}

// startOrchestration records a run as in flight.
func (r *registry) startOrchestration(id, name, by string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.orchestrations[id] = &transport.OrchInfo{
		ID: id, Name: name, By: by, Started: time.Now().UTC(), Running: true,
	}
	r.pruneOrchestrationsLocked()
}

// pruneOrchestrationsLocked evicts the oldest finished runs once the map
// exceeds maxOrchestrations. Runs still in flight are never evicted. The
// caller must hold r.mu.
func (r *registry) pruneOrchestrationsLocked() {
	if len(r.orchestrations) <= maxOrchestrations {
		return
	}
	var ids []string
	for id, info := range r.orchestrations {
		if !info.Running {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	excess := len(r.orchestrations) - maxOrchestrations
	if excess > len(ids) {
		excess = len(ids)
	}
	for _, id := range ids[:excess] {
		delete(r.orchestrations, id)
	}
}

// finishOrchestration records the outcome of a completed run.
func (r *registry) finishOrchestration(id string, steps []orch.StepOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info, ok := r.orchestrations[id]
	if !ok {
		return
	}
	info.Running = false
	info.Finished = time.Now().UTC()
	info.Steps = make([]transport.OrchStep, 0, len(steps))
	for _, step := range steps {
		info.Steps = append(info.Steps, transport.OrchStep{
			ID: step.ID, Ok: step.Ok, Changed: step.Changed, Comment: step.Comment,
			JobID: step.JobID, Agents: step.Agents, Results: step.Results,
		})
	}
}

// orchestration returns a run by id.
func (r *registry) orchestration(id string) (transport.OrchInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info, ok := r.orchestrations[id]
	if !ok {
		return transport.OrchInfo{}, false
	}
	return *info, true
}

var (
	jobIDMu      sync.Mutex
	lastJobStamp string
	jobSeq       int
)

// newJobID is strictly increasing — a UTC microsecond timestamp plus a
// fixed-width sequence number for dispatches sharing a microsecond — so
// listing jobs sorts chronologically as plain strings.
func newJobID() string {
	jobIDMu.Lock()
	defer jobIDMu.Unlock()
	stamp := time.Now().UTC().Format("20060102150405.000000")
	if stamp <= lastJobStamp {
		stamp = lastJobStamp
		jobSeq++
	} else {
		lastJobStamp = stamp
		jobSeq = 0
	}
	return fmt.Sprintf("%s-%06x", stamp, jobSeq)
}
