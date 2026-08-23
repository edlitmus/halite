package transport

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// The limits of SPEC 6.5. Each is enforced before allocation, which is
// the whole point of having them: Salt's absence of these is why one
// malformed return can exhaust a master. // lexicon:allow
//
// Only the limits something enforces are here. SPEC 6.5 names four more
// -- the return payload, the concurrent stream count, the connection
// count, and the decompression ratio -- and they arrive with the
// endpoints that need them. A constant that names a limit nothing
// applies reads like an assurance and is not one.
const (
	MaxRequestBody    = 64 << 20
	MaxSubscribeLine  = 1 << 20
	MaxGrainsPayload  = 1 << 20
	HandshakeTimeout  = 10 * time.Second
	IdleStreamTimeout = 90 * time.Second
)

// The endpoints of SPEC 6.2. Named so that the hub's routes and the
// node's requests cannot drift apart by a typo.
//
// Health, enroll, and subscribe are routed today. The rest are the
// remainder of phase 2 and are here as the one place the wire surface
// is written down; hub.Server.Handler is what says which exist.
const (
	PathHealth      = "/v1/health"
	PathEnroll      = "/v1/enroll"
	PathEnrollRenew = "/v1/enroll/renew"
	PathSubscribe   = "/v1/subscribe"
	PathReturn      = "/v1/return"
	PathEvent       = "/v1/event"
	PathPillar      = "/v1/pillar"
	PathFiles       = "/v1/files/"
	PathGrains      = "/v1/grains"
	PathJobs        = "/v1/jobs"
	PathEvents      = "/v1/events"
	PathJob         = "/v1/jobs/"
	PathMine        = "/v1/mine"
	PathRunners     = "/v1/runners"
)

// EnrollRequest is the body of a POST to /v1/enroll.
type EnrollRequest struct {
	// CSR is PEM. The node generates the key and keeps it.
	CSR string `json:"csr"`
	// Token is a bootstrap token secret, for the token mode of SPEC
	// 7.3. Empty in every other mode.
	Token string `json:"token,omitempty"`
}

// EnrollResponse is the answer.
//
// A pending request is a 202 with State pending, not an error: the node
// is expected to wait and ask again, and an operator has to look at it
// in between.
type EnrollResponse struct {
	NodeID string `json:"node_id"`
	State  string `json:"state"`
	// Cert and CA are PEM, present once the request is accepted.
	Cert string `json:"cert,omitempty"`
	CA   string `json:"ca,omitempty"`
	// Fingerprint is the public key digest an operator compares out of
	// band before accepting.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Message is for a person, and never carries a secret.
	Message string `json:"message,omitempty"`
}

// Message is one line of the /v1/subscribe stream: SPEC 6.2's NDJSON,
// one JSON object per line, no trailing state, so a truncated stream is
// unambiguous.
type Message struct {
	T string `json:"t"`
	// Ping.
	Seq int64 `json:"seq,omitempty"`
	// Job.
	JID     string         `json:"jid,omitempty"`
	Fun     string         `json:"fun,omitempty"`
	Arg     []string       `json:"arg,omitempty"`
	Kwarg   map[string]any `json:"kwarg,omitempty"`
	Env     string         `json:"env,omitempty"`
	Ret     string         `json:"ret,omitempty"`
	Expires string         `json:"expires,omitempty"`
	Nonce   string         `json:"nonce,omitempty"`
	// Event.
	Tag  string         `json:"tag,omitempty"`
	Data map[string]any `json:"data,omitempty"`
	// Revoke, quiesce, drain.
	Reason string `json:"reason,omitempty"`
	Final  bool   `json:"final,omitempty"`
}

// The message types of SPEC 6.2.
const (
	MsgJob     = "job"
	MsgPing    = "ping"
	MsgEvent   = "event"
	MsgRevoke  = "revoke"
	MsgReload  = "reload"
	MsgQuiesce = "quiesce"
	MsgDrain   = "drain"
	// MsgKill cancels a job a node has been given but has not
	// finished. SPEC 6.2 does not name it; without it `jobs kill` can
	// stop a job reaching the nodes it has not reached and can do
	// nothing about the ones it has, which is half a command.
	MsgKill = "kill"
)

// SubscribeRequest is the body a node opens the stream with: its
// initial state, per SPEC 6.2.
type SubscribeRequest struct {
	NodeID string `json:"node_id"`
	// Grains is the node's fact set, already encoded.
	//
	// Raw JSON rather than a map, because SPEC 6.4 requires that a
	// 64-bit integer grain survive the round trip and re-encoding
	// through map[string]any would put it through float64.
	Grains json.RawMessage `json:"grains,omitempty"`
	// Version is the node's build, so a hub can say what it is talking
	// to without asking.
	Version string `json:"version,omitempty"`
}

// SubmitRequest is an operator asking for a job, at POST /v1/jobs.
//
// It carries no principal: who is asking comes from the certificate on
// the connection, never from the body.
type SubmitRequest struct {
	Target     string         `json:"target"`
	TargetKind string         `json:"target_kind,omitempty"`
	Fun        string         `json:"fun"`
	Arg        []string       `json:"arg,omitempty"`
	Kwarg      map[string]any `json:"kwarg,omitempty"`
	Env        string         `json:"env,omitempty"`
	Test       bool           `json:"test,omitempty"`
	Offline    string         `json:"offline,omitempty"`
	// TTLSeconds bounds how long the job may be run, per SPEC 6.3.
	TTLSeconds int `json:"ttl_seconds,omitempty"`

	// Batch is SPEC 9.3's `--batch`, as the operator wrote it: a count
	// or a percentage. The hub resolves it against the matched set,
	// because the operator does not know that number.
	Batch            string `json:"batch,omitempty"`
	BatchWaitSeconds int    `json:"batch_wait_seconds,omitempty"`
	BatchSafeLimit   int    `json:"batch_safe_limit,omitempty"`
	BatchTimeoutSecs int    `json:"batch_timeout_seconds,omitempty"`
	Subset           int    `json:"subset,omitempty"`
}

// SubmitResponse is the hub's acknowledgement: the jid and who is
// expected to answer, so that a caller which disconnects can still find
// out what happened.
type SubmitResponse struct {
	JID   string   `json:"jid"`
	Nodes []string `json:"nodes"`
	// Batch is the resolved batch size, zero for an unbatched job. The
	// operator wrote `25%`; this is what that came to.
	Batch int `json:"batch,omitempty"`
	// Absent lists matched nodes that were not connected, by name. A
	// count would send an operator looking for which.
	Absent []string `json:"absent,omitempty"`
}

// RunnerRequest is an operator asking the hub to run one of its own
// functions, at POST /v1/runners. SPEC section 19.2.
//
// There is no target: a runner runs on the hub, and the fact that some
// runners then reach the fleet is the runner's business, not the
// caller's.
type RunnerRequest struct {
	Fun   string         `json:"fun"`
	Arg   []string       `json:"arg,omitempty"`
	Kwarg map[string]any `json:"kwarg,omitempty"`
}

// RunnerResponse is what the runner returned.
//
// Success is the runner's own verdict and is carried in a 200: a runner
// that ran and reported a failure has answered the question, and only a
// refusal or an unknown name is an HTTP error. A caller that treats any
// 200 as success would report `jobs.exit_success` on a failed job as a
// success, which is the opposite of what it was asked.
type RunnerResponse struct {
	JID     string `json:"jid"`
	Fun     string `json:"fun"`
	Success bool   `json:"success"`
	// Return is already-encoded JSON, for the reason job.Return.Return
	// is: a runner answers with the ordered map of the nine-type model,
	// and re-encoding that through encoding/json marshals the struct.
	Return     json.RawMessage `json:"return,omitempty"`
	Error      string          `json:"error,omitempty"`
	DurationMS int64           `json:"duration_ms,omitempty"`
}

// GrainsRequest is PUT /v1/grains: a node pushing a refreshed fact
// set, per SPEC 6.2. The identity is the certificate's.
type GrainsRequest struct {
	Grains json.RawMessage `json:"grains"`
	// Version is the node's build, so a hub can say what it is talking
	// to without asking.
	Version string `json:"version,omitempty"`
}

// KillResponse says what a kill reached.
type KillResponse struct {
	JID string `json:"jid"`
	// Told is the nodes that were sent the cancellation.
	Told []string `json:"told,omitempty"`
	// Unqueued is the nodes that had not received the job yet and now
	// never will.
	Unqueued []string `json:"unqueued,omitempty"`
}

// ResumeResponse is the answer to POST /v1/jobs/{jid}/resume.
type ResumeResponse struct {
	JID       string   `json:"jid"`
	Remaining []string `json:"remaining,omitempty"`
}

// JobStatus is the answer to GET /v1/jobs/{jid}.
type JobStatus struct {
	JID    string   `json:"jid"`
	Fun    string   `json:"fun"`
	Target string   `json:"target,omitempty"`
	State  string   `json:"state,omitempty"`
	Nodes  []string `json:"nodes,omitempty"`
	// Delivered is who the job has reached, which lags Nodes while a
	// batch is in flight.
	Delivered []string          `json:"delivered,omitempty"`
	Missing   []string          `json:"missing,omitempty"`
	Returns   []json.RawMessage `json:"returns,omitempty"`
}

// PillarRequest is POST /v1/pillar: a node asking for its own pillar.
//
// The grains go up because the top file targets on them; the identity
// does not, because the certificate carries it.
type PillarRequest struct {
	NodeID string          `json:"node_id,omitempty"`
	Env    string          `json:"env,omitempty"`
	Grains json.RawMessage `json:"grains,omitempty"`
}

// PillarResponse is the compiled pillar, already encoded.
type PillarResponse struct {
	NodeID string          `json:"node_id"`
	Env    string          `json:"env"`
	SLS    []string        `json:"sls,omitempty"`
	Pillar json.RawMessage `json:"pillar"`
}

// EventRequest is POST /v1/event: a node putting something on the
// hub's bus. The hub namespaces the tag under the node, so a node
// cannot forge an event that looks like the hub's own.
type EventRequest struct {
	Tag         string          `json:"tag"`
	Data        json.RawMessage `json:"data,omitempty"`
	Correlation string          `json:"correlation,omitempty"`
}

// EventResponse says where the record landed.
type EventResponse struct {
	Tag    string `json:"tag"`
	Offset string `json:"offset"`
}

// Error is the shape of every failure the control plane returns, so
// that a node can tell a refusal from a network fault without parsing
// prose.
type Error struct {
	Error string `json:"error"`
	// Code is a stable token: a hub's wording may improve, and a node's
	// behaviour must not change when it does.
	Code string `json:"code,omitempty"`
}

// The error codes a node acts on.
const (
	CodePending   = "pending"
	CodeRefused   = "refused"
	CodeMalformed = "malformed"
	CodeInternal  = "internal"
)

// WriteJSON sends a value with the canonical settings of SPEC 6.4:
// HTML escaping off, because this is not a browser and a `&` in a
// command line should survive.
func WriteJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// WriteError sends a failure in the one shape.
func WriteError(w http.ResponseWriter, status int, code string, err error) {
	_ = WriteJSON(w, status, Error{Error: err.Error(), Code: code})
}

// ReadJSON decodes a request body under a limit, refusing anything
// after the first value so that a request cannot smuggle a second one.
func ReadJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("the request body: %w", err)
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return fmt.Errorf("the request body carries more than one JSON value")
	}
	return nil
}

// PeerNodeID is the identity the connection authenticated as.
//
// It reads the certificate the TLS layer verified, never a field in the
// body: a node says who it is by holding a key, not by claiming a name.
func PeerNodeID(r *http.Request) (string, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return "", fmt.Errorf("the request carries no client certificate")
	}
	return pki.NodeIDFromCert(r.TLS.PeerCertificates[0])
}

// PeerCert is the verified client certificate, for the renewal path
// that needs its serial.
func PeerCert(r *http.Request) (*x509.Certificate, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return nil, fmt.Errorf("the request carries no client certificate")
	}
	return r.TLS.PeerCertificates[0], nil
}
