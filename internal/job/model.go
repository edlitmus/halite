package job

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/jobsign"
)

// ReturnSchema is the version of the return shape. SPEC 9.4 freezes it,
// so a dashboard built on it keeps working.
const ReturnSchema = "halite.ret/1"

// JobSchema is the version of the job record's shape.
//
// The return's has been frozen since 9.4 and the record's had none at
// all, which meant a hub could not tell a record written by a newer hub
// from one of its own -- and would truncate it on the next write. See
// Job.Schema.
const JobSchema = "halite.job/1"

// SignedJobSchema is the shape of a record that carries SPEC 25.6's
// detached signature.
//
// A second version rather than a field added under the first, because
// the schema's job is to say what a build must understand to write the
// record back -- and a build that does not know about signatures would
// read a signed record, write it back, and drop the signature, which is
// exactly the truncation JobSchema exists to prevent. A node would then
// refuse the job as unsigned, which is loud; the record losing it
// silently is not.
//
// It is stamped per record rather than per build. An unsigned job's
// record is still `halite.job/1` and an older build can still write it,
// because an older build can represent it faithfully. The version
// describes the record, not who wrote it.
const SignedJobSchema = "halite.job/2"

// SchemaFor is the lowest schema that can represent this record.
func SchemaFor(j *Job) string {
	if j != nil && j.Signature != "" {
		return SignedJobSchema
	}
	return JobSchema
}

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
	// Batching: a batched job with nodes still to be delivered to.
	Batching State = "batching"
	// Aborted: a batch stopped by its safe limit, with nodes never
	// delivered to. Distinct from partial, which is nodes that did not
	// answer.
	Aborted State = "aborted"
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
	// OnBehalfOf is who asked the submitter to submit this: the
	// operator the API authenticated, when a job came through it.
	// Recorded for the audit and never used to authorize, so that
	// "who really asked for this" has an answer without becoming a
	// way to claim to be someone else.
	OnBehalfOf string `json:"on_behalf_of,omitempty"`
	// Correlation is the causality chain this job belongs to. It is
	// carried into every event the job produces, so that "what did this
	// cause" has an answer. SPEC 17.1.
	Correlation string `json:"correlation,omitempty"`
	// TraceParent is the W3C trace context this job runs under, as the
	// header spells it. SPEC 26.3.
	//
	// Deliberately not serialised. It is in-flight state rather than a
	// record: it reaches a node on the job message and is used to parent
	// that node's spans, and adding a field to the persisted record
	// would mean an older build silently dropping it on a rollback --
	// the defect 4.13 exists to have fixed once. A trace that has ended
	// is in the collector, and the identifier is on the job's log lines,
	// which is where an operator looks for it afterwards.
	TraceParent string `json:"-"`
	// Target and TargetKind are what the operator typed, kept so that
	// `jobs show` can say what was asked for and not only who answered.
	Target     string `json:"target,omitempty"`
	TargetKind string `json:"target_kind,omitempty"`
	// Nodes is the resolved set: who is expected to return.
	Nodes   []string `json:"nodes,omitempty"`
	Offline Offline  `json:"offline,omitempty"`
	State   State    `json:"state,omitempty"`
	Test    bool     `json:"test,omitempty"`

	// Batch is the batching of SPEC 9.3, and it is a property of the
	// job rather than of the client.
	//
	// In Salt `--batch` is implemented in the CLI, so closing the
	// terminal abandons the batch with half the estate updated and no
	// record of where it stopped. Here the hub owns it: the group has
	// its own record, it survives the operator going home, and
	// `halite-hub jobs resume` picks it up.
	Batch Batch `json:"batch,omitempty"`
	// Delivered is who the hub has attempted delivery to, which
	// differs from Nodes while a batch is in flight. It is the record
	// that makes resuming possible.
	//
	// An attempt, not an arrival: a node that was not connected when
	// its slice went out is still counted, or the batch would retry it
	// for ever and never advance. Who actually received it is the set
	// that returns, and Queued below is who is still owed one.
	Delivered []string `json:"delivered,omitempty"`
	// Queued is the spool of SPEC 9.5's `queue` policy: nodes that
	// were matched, were not connected, and are to be given the job
	// when they next appear -- if it has not expired by then.
	Queued []string `json:"queued,omitempty"`

	// Signature is SPEC 25.6's detached operator signature, base64 of
	// ASN.1 DER, empty on an unsigned job.
	//
	// On the record as well as on the wire because a queued or batched
	// job is delivered long after it was submitted, from the record: a
	// signature kept only in the dispatching call would be lost by
	// `jobs resume` and by SPEC 9.5's spool, and every node reached that
	// way would refuse a job that was properly signed.
	//
	// The hub stores it and relays it and can do nothing else with it.
	// It holds no signer key -- that is the point of the mechanism -- so
	// it cannot tell a valid signature from a forged one, and does not
	// try. The node decides.
	Signature string `json:"signature,omitempty"`

	// Schema is the version of this record's shape.
	//
	// It exists so that a hub can tell it is looking at a record a
	// newer hub wrote. Without it, it cannot: a record round-trips
	// through this struct, and `encoding/json` drops every field the
	// struct does not have -- measured at ten keys in and eight out.
	// So an older hub reading, changing and writing back a newer
	// record silently destroyed whatever the newer version had added,
	// with nothing anywhere recording that it happened. That is the
	// rollback case, and SPEC 31's "job cache format migration" row is
	// asking about exactly it. DIVERGENCE 4.13.
	//
	// Empty means a record written before this field existed, which is
	// this build's own shape and is stamped on the next write. Anything
	// else this build does not know is refused for *writing* and still
	// readable, because `jobs list` on a rolled-back hub should keep
	// working and truncating the record should not.
	Schema string `json:"schema,omitempty"`
}

// IsQueuedFor reports whether a node is owed this job.
func (j *Job) IsQueuedFor(nodeID string) bool {
	for _, id := range j.Queued {
		if id == nodeID {
			return true
		}
	}
	return false
}

// Dequeue removes a node from the spool.
func (j *Job) Dequeue(nodeID string) {
	out := j.Queued[:0]
	for _, id := range j.Queued {
		if id != nodeID {
			out = append(out, id)
		}
	}
	j.Queued = out
}

// Batch is the batching policy of SPEC 9.3.
type Batch struct {
	// Size is how many nodes run at once. Zero is all of them.
	Size int `json:"size,omitempty"`
	// Wait is the settle time between batches.
	Wait time.Duration `json:"wait,omitempty"`
	// SafeLimit aborts the run when more than this many nodes have
	// failed. Zero does not abort.
	SafeLimit int `json:"safe_limit,omitempty"`
	// Timeout is how long one batch waits for its returns before
	// moving on. Zero uses the job's own expiry.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// Batched reports whether this job is delivered in batches.
func (j *Job) Batched() bool { return j.Batch.Size > 0 && j.Batch.Size < len(j.Nodes) }

// Remaining lists the nodes a batched job has not been delivered to.
func (j *Job) Remaining() []string {
	sent := make(map[string]bool, len(j.Delivered))
	for _, id := range j.Delivered {
		sent[id] = true
	}
	var out []string
	for _, id := range j.Nodes {
		if !sent[id] {
			out = append(out, id)
		}
	}
	return out
}

// ParseBatchSize reads `25` or `25%` against a matched set.
//
// A percentage that rounds to zero becomes one: an operator who wrote
// `--batch 1%` against fifty nodes meant "a few at a time", not "none
// at a time", and a batch size of zero would mean the whole estate.
func ParseBatchSize(spec string, matched int) (int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, nil
	}
	if strings.HasSuffix(spec, "%") {
		var percent float64
		if _, err := fmt.Sscanf(strings.TrimSuffix(spec, "%"), "%g", &percent); err != nil {
			return 0, fmt.Errorf("--batch %q is not a number or a percentage", spec)
		}
		if percent <= 0 || percent > 100 {
			return 0, fmt.Errorf("--batch %q is not between 0%% and 100%%", spec)
		}
		size := int(float64(matched) * percent / 100)
		if size < 1 {
			size = 1
		}
		return size, nil
	}
	var size int
	if _, err := fmt.Sscanf(spec, "%d", &size); err != nil {
		return 0, fmt.Errorf("--batch %q is not a number or a percentage", spec)
	}
	if size < 1 {
		return 0, fmt.Errorf("--batch %q is not a positive number", spec)
	}
	return size, nil
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

// WireKwargs is the keyword arguments as a node will receive them.
//
// `test` is a field on the record and a keyword argument on the wire, and
// this is the one function that knows it. It used to be inline in the
// hub's messageFor, which was fine until a signature covered the
// arguments: an operator signing what they typed and a node verifying
// what arrived would disagree about exactly one key, on exactly the runs
// that set it, and every `--test` job would be refused as unsigned.
//
// The copy is not an optimisation. Setting `test` on the message used to
// write into the job's own map, so the record on disk grew an argument
// the operator never passed, and a resumed batch sent a different message
// from the first slice.
func WireKwargs(j *Job) map[string]any {
	kwargs := make(map[string]any, len(j.Kwarg)+1)
	for k, v := range j.Kwarg {
		kwargs[k] = v
	}
	if j.Test {
		kwargs["test"] = true
	}
	if len(kwargs) == 0 {
		return nil
	}
	return kwargs
}

// SigningPayload is what SPEC 25.6's signature covers.
//
// Built from the job by both ends -- the operator signs it before the
// hub has seen the job, and the node rebuilds it from the message it
// received -- so this function is the agreement between them. Anything
// it leaves out is something the hub can change on a signed job without
// the signature noticing.
func SigningPayload(j *Job) jobsign.Payload {
	return jobsign.Payload{
		JID:        string(j.JID),
		Target:     j.Target,
		TargetKind: j.TargetKind,
		Fun:        j.Fun,
		Arg:        j.Arg,
		Kwarg:      WireKwargs(j),
		Env:        j.Env,
		Expires:    j.Expires,
	}
}
