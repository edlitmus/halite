package job

import (
	"encoding/json"
	"fmt"
	"time"
)

// ReturnSchema is the version of the return shape. SPEC 9.4 freezes it,
// so a dashboard built on it keeps working.
const ReturnSchema = "halite.ret/1"

// Offline is the per-job policy of SPEC 9.5 for a node that is not
// connected.
type Offline string

const (
	// Skip reports the node as unresponsive and sends it nothing. The
	// default for an ad-hoc job.
	Skip Offline = "skip"
	// Queue spools the job for the node's next connection, bounded by
	// the job's expiry.
	Queue Offline = "queue"
	// Require fails the job outright if any matched node is absent.
	Require Offline = "require"
)

// ParseOffline reads the policy an operator asked for.
func ParseOffline(s string) (Offline, error) {
	switch Offline(s) {
	case Skip, Queue, Require:
		return Offline(s), nil
	case "":
		return Skip, nil
	}
	return "", fmt.Errorf("%q is not an offline policy; use %q, %q, or %q", s, Skip, Queue, Require)
}

// State is where a job has got to.
type State string

const (
	// Dispatched: written to the cache with its node set, and on its
	// way. SPEC 9.1 step 4 -- the expected respondents are recorded
	// before delivery, so a missing return is detectable rather than
	// invisible.
	Dispatched State = "dispatched"
	// Complete: every expected node returned.
	Complete State = "complete"
	// Partial: the gather window closed with returns outstanding.
	Partial State = "partial"
)

// Job is the record the hub keeps and the message the node receives.
type Job struct {
	JID   ID             `json:"jid"`
	Fun   string         `json:"fun"`
	Arg   []string       `json:"arg,omitempty"`
	Kwarg map[string]any `json:"kwarg,omitempty"`
	Env   string         `json:"env,omitempty"`
	Nonce string         `json:"nonce"`
	// Expires is absolute and RFC 3339. A node refuses a job past it.
	Expires time.Time `json:"expires"`
	Created time.Time `json:"created"`
	// Submitter is the authenticated principal that asked for this, for
	// the audit record. It is never the value of a field in a request
	// body.
	Submitter string `json:"submitter,omitempty"`
	// Target and TargetKind are what the operator typed, kept so that
	// `jobs show` can say what was asked for and not only who answered.
	Target     string `json:"target,omitempty"`
	TargetKind string `json:"target_kind,omitempty"`
	// Nodes is the resolved set: who is expected to return.
	Nodes   []string `json:"nodes,omitempty"`
	Offline Offline  `json:"offline,omitempty"`
	State   State    `json:"state,omitempty"`
	Test    bool     `json:"test,omitempty"`
}

// Expired reports whether the job may no longer be run.
func (j *Job) Expired(now time.Time) bool {
	return !j.Expires.IsZero() && now.After(j.Expires)
}

// Return is one node's answer, in the shape SPEC 9.4 freezes.
//
// The field names are Salt's, deliberately, so that an existing
// dashboard or returner keeps working.
type Return struct {
	JID     ID       `json:"jid"`
	NodeID  string   `json:"id"`
	Fun     string   `json:"fun"`
	FunArgs []string `json:"fun_args,omitempty"`
	Success bool     `json:"success"`
	RetCode int      `json:"retcode"`
	// Return is already-encoded JSON.
	//
	// Not `any`: a state result is an ordered map from the nine-type
	// model, and handing that to encoding/json marshals the struct
	// rather than the mapping -- a highstate came back as
	// `map[Pos:map[Col:0 File: Line:0]]`. Encoding once, on the node,
	// with the model's own codec also keeps SPEC 6.4's promise that a
	// 64-bit integer survives the round trip.
	Return      json.RawMessage `json:"return"`
	Out         string          `json:"out,omitempty"`
	StartTime   string          `json:"start_time"`
	DurationMS  int64           `json:"duration_ms"`
	NodeVersion string          `json:"node_version,omitempty"`
	Schema      string          `json:"schema"`
	// Chunk is the return's part number, for a result too large to send
	// in one request. Zero is the whole thing.
	Chunk int `json:"chunk,omitempty"`
}

// Key is what a return is idempotent by, per SPEC 6.2: the same node
// posting the same chunk of the same job twice is one return.
func (r *Return) Key() string {
	return fmt.Sprintf("%s/%s/%d", r.JID, r.NodeID, r.Chunk)
}
