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
	PathMine        = "/v1/mine"
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
