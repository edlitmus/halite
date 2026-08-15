// Package transport carries halite's control plane traffic: mTLS over
// HTTP/2, JSON bodies, no custom framing and no custom crypto. Every
// endpoint except enrollment requires a client certificate issued by the
// fleet CA, and the caller's role comes from that certificate rather than
// from anything it says about itself.
package transport

import "time"

// API paths. Only PathEnroll is reachable without a client certificate.
const (
	PathEnroll    = "/v1/enroll"
	PathRenew     = "/v1/renew"
	PathHello     = "/v1/hello"
	PathJobs      = "/v1/jobs"
	PathResults   = "/v1/results"
	PathPillar    = "/v1/pillar"
	PathStateTree = "/v1/statetree"

	PathEvents      = "/v1/events"
	PathMine        = "/v1/mine"
	PathDispatch    = "/v1/dispatch"
	PathAgents      = "/v1/agents"
	PathOrchestrate = "/v1/orchestrate"
	PathOrchInfo    = "/v1/orch/" // + orchestration id
	PathJobInfo     = "/v1/job/"  // + job id
)

// DefaultPort is the control plane's listening port.
const DefaultPort = 4506

// MaxBodyBytes caps request bodies. Enrollment is reachable before
// authentication, so every body is bounded.
const MaxBodyBytes = 8 << 20

// EnrollRequest is an agent asking the CA to sign its certificate request.
type EnrollRequest struct {
	ID  string `json:"id"`
	CSR string `json:"csr"` // PEM
}

// EnrollResponse reports where the request stands. Cert is set only once
// an operator has accepted the request.
type EnrollResponse struct {
	State string `json:"state"` // pending, accepted, rejected
	Cert  string `json:"cert,omitempty"`
}

// RenewRequest is an agent asking for a fresh certificate before its
// current one expires. There is no id in it: the control plane takes that
// from the certificate the agent is connecting with, which is the whole
// reason renewal needs no operator.
type RenewRequest struct {
	CSR string `json:"csr"` // PEM, for the key the agent already holds
}

// RenewResponse carries the reissued certificate.
type RenewResponse struct {
	Cert string `json:"cert"`
}

// HelloRequest registers an agent and its facts with the control plane.
// Targeting runs against these grains, so they are refreshed on every
// reconnect.
type HelloRequest struct {
	Grains  map[string]any `json:"grains"`
	Version string         `json:"version"`
}

// Agent is what the control plane knows about one enrolled host.
type Agent struct {
	ID       string         `json:"id"`
	Grains   map[string]any `json:"grains,omitempty"`
	Version  string         `json:"version,omitempty"`
	LastSeen time.Time      `json:"last_seen"`
	Online   bool           `json:"online"`
}

// Job kinds. Anything else is rejected by the agent.
const (
	KindHighstate = "state.highstate"
	KindApply     = "state.apply"
	KindCall      = "call"
	KindGrains    = "grains"
	KindPillar    = "pillar"
)

// Job is one unit of work dispatched to a set of agents.
type Job struct {
	ID      string            `json:"id"`
	Kind    string            `json:"kind"`
	Target  string            `json:"target"`
	SLS     []string          `json:"sls,omitempty"`  // for state.apply
	Fn      string            `json:"fn,omitempty"`   // for call
	Args    map[string]string `json:"args,omitempty"` // for call
	Test    bool              `json:"test,omitempty"`
	Created time.Time         `json:"created"`
}

// StateOutcome mirrors one line of `halite apply` output.
type StateOutcome struct {
	ID       string            `json:"id"`
	Function string            `json:"function"`
	Ok       bool              `json:"result"`
	Changed  bool              `json:"changed"`
	Comment  string            `json:"comment"`
	Changes  map[string]string `json:"changes,omitempty"`
}

// JobResult is one agent's answer to one job.
type JobResult struct {
	JobID     string         `json:"job_id"`
	AgentID   string         `json:"agent_id"`
	Ok        bool           `json:"result"`
	Error     string         `json:"error,omitempty"`
	States    []StateOutcome `json:"states,omitempty"`
	Data      map[string]any `json:"data,omitempty"` // grains and pillar jobs
	Succeeded int            `json:"succeeded"`
	Failed    int            `json:"failed"`
	Changed   int            `json:"changed"`
	Finished  time.Time      `json:"finished"`
	Duration  time.Duration  `json:"duration_ns"`
}

// DispatchRequest is an operator asking for work to be run.
type DispatchRequest struct {
	Target string            `json:"target"`
	Kind   string            `json:"kind"`
	SLS    []string          `json:"sls,omitempty"`
	Fn     string            `json:"fn,omitempty"`
	Args   map[string]string `json:"args,omitempty"`
	Test   bool              `json:"test,omitempty"`
}

// DispatchResponse names the job and the agents it was queued for.
type DispatchResponse struct {
	JobID  string   `json:"job_id"`
	Agents []string `json:"agents"`
}

// JobInfo is a job plus whatever results have come back so far.
type JobInfo struct {
	Job       Job         `json:"job"`
	Expecting []string    `json:"expecting"`
	Results   []JobResult `json:"results"`
}

// Event is one thing that happened, as it crosses the wire. It mirrors
// internal/event.Event; agents post their own, operators stream them.
type Event struct {
	ID     string         `json:"id"`
	Tag    string         `json:"tag"`
	Time   time.Time      `json:"time"`
	Source string         `json:"source"`
	Data   map[string]any `json:"data,omitempty"`
}

// MineReport is an agent publishing one function's output.
type MineReport struct {
	Function string         `json:"function"`
	Data     map[string]any `json:"data"`
}

// MineEntry is one agent's latest value for one function.
type MineEntry struct {
	Data    map[string]any `json:"data"`
	Updated time.Time      `json:"updated"`
}

// Mine is what the control plane holds: function -> agent -> value. States
// read it as {{ .Mine.<function>.<agent> }}.
type Mine map[string]map[string]MineEntry

// OrchestrateRequest starts an orchestration by name.
type OrchestrateRequest struct {
	Name string `json:"name"`
	Test bool   `json:"test,omitempty"`
}

// OrchestrateResponse names the run that started.
type OrchestrateResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Steps int    `json:"steps"`
}

// OrchStep is one step's outcome, as the control plane reports it.
type OrchStep struct {
	ID      string      `json:"id"`
	Ok      bool        `json:"result"`
	Changed bool        `json:"changed"`
	Comment string      `json:"comment"`
	JobID   string      `json:"job_id,omitempty"`
	Agents  []string    `json:"agents,omitempty"`
	Results []JobResult `json:"results,omitempty"`
}

// OrchInfo is an orchestration and its outcome so far.
type OrchInfo struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	By       string     `json:"by"`
	Started  time.Time  `json:"started"`
	Finished time.Time  `json:"finished,omitempty"`
	Running  bool       `json:"running"`
	Steps    []OrchStep `json:"steps,omitempty"`
}

// ErrorResponse is the body of every non-2xx reply.
type ErrorResponse struct {
	Error string `json:"error"`
}
