// Package orchplan is issue 765's signed orchestration plan: the file an
// operator writes off the hub, holding every fleet step of one
// orchestration as a complete SPEC 25.6 signed job, so that a node which
// requires signed jobs can run an orchestration the hub could not have
// authorised by itself.
//
// # What a plan authorises, and what it does not
//
// Each step's signature is the authority, and it is the job signature a
// node already checks: the job id, target and target kind, function,
// arguments, environment and expiry. Nodes never see the plan, and the
// plan as a whole carries no signature, because the only party that
// would check one is the hub, which is the party it would have to be
// protected from.
//
// So a plan authorises a set of jobs, not their order. A hub holding a
// plan can send its steps in another order, skip one, or send none; each
// one it sends is still exactly what was signed. Batch, subset, safe
// limit, `tolerate_failures` and `run_parallel` are outside the signature
// and are controls an honest hub honours, not authorisations. The
// hub-side steps (`salt.runner`, `salt.wheel`, `salt.wait_for_event`) are
// listed so that an honest hub can refuse an orchestration that changed,
// and nothing makes a compromised one honour the list.
//
// # The format
//
// JSON of schema `halite.orch-plan/1`, strictly parsed: an unknown field
// or schema is refused, for the reason treesign gives -- a field this
// build does not understand could be one that changes what the plan
// means. Numbers inside keyword arguments are kept as `json.Number`, so
// the payload rebuilt here is the one a node rebuilds from the wire.
package orchplan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/jobsign"
)

// Schema is the plan's format, and the only one this build reads.
const Schema = "halite.orch-plan/1"

// MaxBytes bounds what a hub reads as a plan. Sixteen MiB, which is a
// few thousand steps of ordinary size; a plan arrives as a runner
// argument, and the request body is already bounded above it.
const MaxBytes = 16 << 20

// Plan is one signed orchestration.
type Plan struct {
	Schema string   `json:"schema"`
	SLS    []string `json:"sls"`
	Env    string   `json:"env"`
	Test   bool     `json:"test"`
	// PillarDigest is the digest the hub records for a run's override
	// (`none` for no override), so that a plan for one override cannot be
	// run with another.
	PillarDigest string `json:"pillar_digest"`
	// TreeRevision is the issue 764 manifest id (treesign.ID) of the
	// checkout the plan was compiled from, when that checkout carried
	// one, and empty when it did not.
	TreeRevision string `json:"tree_revision,omitempty"`
	// ResumeOf and ResumeFrom make this a resume plan: it signs the
	// steps from ResumeFrom onwards, for `orch resume ResumeOf`.
	ResumeOf   string `json:"resume_of,omitempty"`
	ResumeFrom string `json:"resume_from,omitempty"`
	Created    string `json:"created"`
	// Expires is every step's expiry: one window for the whole run.
	Expires  string    `json:"expires"`
	Steps    []Step    `json:"steps"`
	HubSteps []HubStep `json:"hub_steps"`

	created time.Time
	expires time.Time
}

// Step is one fleet step and the signed job it sends.
type Step struct {
	// ID is the state ID, which is what the hub matches the step by.
	ID string `json:"id"`
	// Step is the step function, such as `salt.state`.
	Step      string `json:"step"`
	Job       Job    `json:"job"`
	Signature string `json:"signature"`
}

// Job is job.SigningPayload's field set and nothing else, so that a plan
// cannot carry something a node does not check.
type Job struct {
	JID        string         `json:"jid"`
	Target     string         `json:"target"`
	TargetKind string         `json:"target_kind"`
	Fun        string         `json:"fun"`
	Arg        []string       `json:"arg,omitempty"`
	Kwarg      map[string]any `json:"kwarg,omitempty"`
	Env        string         `json:"env"`
	Expires    string         `json:"expires"`
}

// HubStep is a step the hub runs itself. Informational: see the package
// comment.
type HubStep struct {
	ID   string `json:"id"`
	Step string `json:"step"`
	Name string `json:"name,omitempty"`
}

// CreatedAt and ExpiresAt are the parsed times.
func (p *Plan) CreatedAt() time.Time { return p.created }
func (p *Plan) ExpiresAt() time.Time { return p.expires }

// Step finds a fleet step by its state ID.
func (p *Plan) Step(id string) (Step, bool) {
	for _, s := range p.Steps {
		if s.ID == id {
			return s, true
		}
	}
	return Step{}, false
}

// FromJob is the plan's form of a job record's signed fields.
func FromJob(j *job.Job) Job {
	payload := job.SigningPayload(j)
	return Job{
		JID:        payload.JID,
		Target:     payload.Target,
		TargetKind: payload.TargetKind,
		Fun:        payload.Fun,
		Arg:        payload.Arg,
		Kwarg:      payload.Kwarg,
		Env:        payload.Env,
		Expires:    FormatTime(payload.Expires),
	}
}

// Payload is what the step's signature covers, rebuilt from the plan.
//
// Kwarg is used as it stands, `test` included when the step was planned
// in test mode: it is job.WireKwargs's output already, because FromJob
// took it from job.SigningPayload.
func (j Job) Payload() (jobsign.Payload, error) {
	expires, err := parseTime(j.Expires)
	if err != nil {
		return jobsign.Payload{}, fmt.Errorf("job %s's expiry: %w", j.JID, err)
	}
	return jobsign.Payload{
		JID:        j.JID,
		Target:     j.Target,
		TargetKind: j.TargetKind,
		Fun:        j.Fun,
		Arg:        j.Arg,
		Kwarg:      j.Kwarg,
		Env:        j.Env,
		Expires:    expires,
	}, nil
}

// Digest identifies a plan by its exact bytes, for the run's record and
// the hub's log.
func Digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Encode writes a plan the way Parse reads it.
func Encode(p *Plan) ([]byte, error) {
	p.Schema = Schema
	if p.Steps == nil {
		p.Steps = []Step{}
	}
	if p.HubSteps == nil {
		p.HubSteps = []HubStep{}
	}
	out, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// Parse reads a plan strictly.
//
// It checks the plan's shape and nothing about whether it is right: that
// every step is well formed, every job id is valid and used once, every
// state ID once, and every job's expiry the plan's. Whether the jobs are
// the ones this orchestration compiles to is the hub's pre-flight, and
// whether the signatures verify is each node's.
func Parse(raw []byte) (*Plan, error) {
	if len(raw) > MaxBytes {
		return nil, fmt.Errorf("the plan is %d bytes, over the %d a hub reads", len(raw), MaxBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var p Plan
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("the plan is not a %s document: %w", Schema, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("the plan has content after its document")
	}
	if p.Schema != Schema {
		return nil, fmt.Errorf("the plan's schema is %q and this build reads %q", p.Schema, Schema)
	}
	if len(p.SLS) == 0 {
		return nil, errors.New("the plan names no orchestration SLS")
	}
	if p.Env == "" {
		return nil, errors.New("the plan names no environment")
	}
	if p.PillarDigest == "" {
		return nil, errors.New("the plan carries no pillar_digest")
	}
	if (p.ResumeOf == "") != (p.ResumeFrom == "") {
		return nil, errors.New("a resume plan names both resume_of and resume_from, or neither")
	}
	if p.ResumeOf != "" && !job.ID(p.ResumeOf).Valid() {
		return nil, fmt.Errorf("the plan resumes %q, which is not a job identifier", p.ResumeOf)
	}
	var err error
	if p.created, err = parseTime(p.Created); err != nil {
		return nil, fmt.Errorf("the plan's created time: %w", err)
	}
	if p.expires, err = parseTime(p.Expires); err != nil {
		return nil, fmt.Errorf("the plan's expiry: %w", err)
	}
	if !p.expires.After(p.created) {
		return nil, errors.New("the plan expires before it was created")
	}
	if p.Steps == nil || p.HubSteps == nil {
		return nil, errors.New("the plan has no steps or no hub_steps list")
	}
	ids := map[string]bool{}
	jids := map[string]bool{}
	for _, s := range p.Steps {
		if s.ID == "" || s.Step == "" {
			return nil, errors.New("a plan step has no id or no step function")
		}
		if ids[s.ID] {
			return nil, fmt.Errorf("the plan names step %q twice", s.ID)
		}
		ids[s.ID] = true
		if !job.ID(s.Job.JID).Valid() {
			return nil, fmt.Errorf("step %s: %q is not a job identifier", s.ID, s.Job.JID)
		}
		if jids[s.Job.JID] {
			return nil, fmt.Errorf("step %s: job %s is used twice in the plan", s.ID, s.Job.JID)
		}
		jids[s.Job.JID] = true
		if s.Job.Target == "" || s.Job.TargetKind == "" || s.Job.Fun == "" {
			return nil, fmt.Errorf("step %s: the job has no target, target kind or function", s.ID)
		}
		if s.Job.Expires != p.Expires {
			return nil, fmt.Errorf("step %s: the job expires at %s and the plan at %s; a plan has one window",
				s.ID, s.Job.Expires, p.Expires)
		}
		if s.Signature == "" {
			return nil, fmt.Errorf("step %s carries no signature", s.ID)
		}
	}
	for _, h := range p.HubSteps {
		if h.ID == "" || h.Step == "" {
			return nil, errors.New("a hub step has no id or no step function")
		}
	}
	return &p, nil
}

// FormatTime is the plan's time form: RFC 3339 in UTC with nanoseconds,
// as the job signature's own expiry is written.
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not an RFC 3339 time", s)
	}
	return t, nil
}
