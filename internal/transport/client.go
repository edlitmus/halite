package transport

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// ErrPending is what a node gets while an operator has not yet decided.
// It is not a failure; the node waits and asks again.
var ErrPending = errors.New("the hub is holding this request for an operator")

// Client is a node's connection to its hub.
//
// It carries the node's certificate once it has one, and the CA it
// pinned at enrollment always: a node that would accept a new CA
// because it was offered one has no identity to defend.
type Client struct {
	// HubURL is the base, https://hub.example:4510.
	HubURL string
	// CA is what the hub is verified against.
	CA *x509.Certificate
	// Cert is the node's own, absent before enrollment.
	Cert *tls.Certificate
	// ServerName overrides the name checked in the hub's certificate,
	// for the case where the hub is reached by an address.
	ServerName string
	// Timeout applies to the short request/response endpoints and not
	// to the subscribe stream, which is meant to stay open.
	Timeout time.Duration

	http *http.Client
}

func (c *Client) serverName() (string, error) {
	if c.ServerName != "" {
		return c.ServerName, nil
	}
	u, err := url.Parse(c.HubURL)
	if err != nil {
		return "", fmt.Errorf("the hub address %q: %w", c.HubURL, err)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("the hub address %q names no host", c.HubURL)
	}
	return u.Hostname(), nil
}

func (c *Client) client() (*http.Client, error) {
	if c.http != nil {
		return c.http, nil
	}
	name, err := c.serverName()
	if err != nil {
		return nil, err
	}
	var cfg *tls.Config
	if c.Cert != nil {
		cfg = ClientConfig(*c.Cert, c.CA, name)
	} else {
		cfg = EnrollConfig(c.CA, name)
	}
	c.http = &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:     cfg,
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: HandshakeTimeout,
			// The subscribe stream is long-lived by design, so there
			// is no response header timeout here; the per-request
			// deadline comes from the context instead.
			IdleConnTimeout: IdleStreamTimeout,
		},
	}
	return c.http, nil
}

// Reset drops the cached HTTP client, so that a certificate collected
// or renewed takes effect on the next request.
func (c *Client) Reset() { c.http = nil }

func (c *Client) url(path string) string {
	return c.HubURL + path
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 30 * time.Second
}

// post sends a JSON body and decodes a JSON answer, turning the hub's
// error shape back into an error.
func (c *Client) post(ctx context.Context, path string, body, out any) (int, error) {
	client, err := c.client()
	if err != nil {
		return 0, err
	}
	raw, err := marshal(body)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(path), bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(res.Body, MaxRequestBody))
	if err != nil {
		return res.StatusCode, fmt.Errorf("%s: %w", path, err)
	}
	if res.StatusCode >= 400 {
		return res.StatusCode, decodeError(path, payload, res.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(payload, out); err != nil {
			return res.StatusCode, fmt.Errorf("%s answered with something that is not the expected JSON: %w", path, err)
		}
	}
	return res.StatusCode, nil
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeError(path string, payload []byte, status int) error {
	var e Error
	if err := json.Unmarshal(payload, &e); err == nil && e.Error != "" {
		return fmt.Errorf("%s: %s", path, e.Error)
	}
	return fmt.Errorf("%s: the hub answered %d", path, status)
}

// Health asks the one endpoint that needs no certificate.
func (c *Client) Health(ctx context.Context) (string, error) {
	client, err := c.client()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(PathHealth), nil)
	if err != nil {
		return "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4096))
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the hub answered %d", res.StatusCode)
	}
	return string(bytes.TrimSpace(body)), nil
}

// Enrollment is what a node gets back when it asks to join.
type Enrollment struct {
	NodeID      string
	Fingerprint string
	// CertPEM and CAPEM are empty while the request is pending.
	CertPEM []byte
	CAPEM   []byte
}

// Enroll submits a certificate request.
//
// It returns ErrPending, with the fingerprint filled in, while an
// operator has not decided. The fingerprint is what the operator
// compares out of band, so it is printed on the node as well as on the
// hub: comparing two things that came from the same place proves
// nothing.
func (c *Client) Enroll(ctx context.Context, key crypto.Signer, nodeID, token string) (*Enrollment, error) {
	csrDER, err := pki.NewNodeCSR(key, nodeID)
	if err != nil {
		return nil, err
	}
	csrPEM := pki.EncodeCSR(csrDER)
	fingerprint, err := pki.FingerprintKey(key.Public())
	if err != nil {
		return nil, err
	}

	var res EnrollResponse
	status, err := c.post(ctx, PathEnroll, EnrollRequest{CSR: string(csrPEM), Token: token}, &res)
	if err != nil {
		return nil, err
	}
	out := &Enrollment{NodeID: res.NodeID, Fingerprint: res.Fingerprint}
	if out.Fingerprint == "" {
		out.Fingerprint = fingerprint
	}
	if status == http.StatusAccepted || res.State == "pending" {
		return out, ErrPending
	}
	if res.Cert == "" {
		return out, fmt.Errorf("the hub accepted the request and sent no certificate")
	}
	out.CertPEM = []byte(res.Cert)
	out.CAPEM = []byte(res.CA)
	return out, nil
}

// Renew presents a new request on an authenticated connection. SPEC
// 7.4: no operator, no token.
func (c *Client) Renew(ctx context.Context, key crypto.Signer, nodeID string) (*Enrollment, error) {
	if c.Cert == nil {
		return nil, fmt.Errorf("renewal needs the certificate being renewed")
	}
	csrDER, err := pki.NewNodeCSR(key, nodeID)
	if err != nil {
		return nil, err
	}
	var res EnrollResponse
	if _, err := c.post(ctx, PathEnrollRenew, EnrollRequest{CSR: string(pki.EncodeCSR(csrDER))}, &res); err != nil {
		return nil, err
	}
	if res.Cert == "" {
		return nil, fmt.Errorf("the hub renewed the certificate and sent none")
	}
	return &Enrollment{
		NodeID:      res.NodeID,
		Fingerprint: res.Fingerprint,
		CertPEM:     []byte(res.Cert),
		CAPEM:       []byte(res.CA),
	}, nil
}

// Subscribe opens the long-lived stream and calls onMessage for every
// line until the context ends or the hub closes it.
//
// The stream is unterminated NDJSON by design (SPEC 6.2), so a clean
// end and a truncated one are the same thing at this level: the caller
// reconnects either way.
func (c *Client) Subscribe(ctx context.Context, req SubscribeRequest, onMessage func(Message) error) error {
	client, err := c.client()
	if err != nil {
		return err
	}
	raw, err := marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(PathSubscribe), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		return decodeError(PathSubscribe, payload, res.StatusCode)
	}

	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), MaxSubscribeLine)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			return fmt.Errorf("the hub sent a line that is not a message: %w", err)
		}
		if err := onMessage(msg); err != nil {
			return err
		}
		if msg.Final {
			return nil
		}
	}
	return scanner.Err()
}

// Return posts one job return to the hub. SPEC 6.2: one request per
// return, idempotent by (jid, node_id, chunk), so a retry after a lost
// acknowledgement is safe.
func (c *Client) Return(ctx context.Context, ret any) error {
	_, err := c.post(ctx, PathReturn, ret, nil)
	return err
}

// Submit asks the hub to run a job. The caller is an operator, and the
// hub decides who that is from the certificate on the connection.
func (c *Client) Submit(ctx context.Context, req SubmitRequest) (*SubmitResponse, error) {
	var res SubmitResponse
	if _, err := c.post(ctx, PathJobs, req, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Runner calls a hub runner, per SPEC section 19.2.
func (c *Client) Runner(ctx context.Context, req RunnerRequest) (*RunnerResponse, error) {
	var res RunnerResponse
	if _, err := c.post(ctx, PathRunners, req, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// JobStatus reads what has come back for a job so far.
func (c *Client) JobStatus(ctx context.Context, jid string) (*JobStatus, error) {
	client, err := c.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(PathJob+jid), nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(res.Body, MaxRequestBody))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		return nil, decodeError(PathJob+jid, payload, res.StatusCode)
	}
	var out JobStatus
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("the hub's answer for %s is not readable: %w", jid, err)
	}
	return &out, nil
}

// FileManifest is a subtree's contents on the hub, as JSON: the node
// decodes it with the fileserver's own type.
func (c *Client) FileManifest(ctx context.Context, env, prefix string) ([]byte, error) {
	path := PathFiles + url.PathEscape(env)
	if prefix != "" {
		path += "?prefix=" + url.QueryEscape(prefix)
	}
	body, _, _, err := c.get(ctx, path, "")
	return body, err
}

// FetchFile reads one file from the hub.
//
// etag is what the node already has, and an empty answer with
// notModified set means the cached copy is current. The digest the hub
// publishes comes back so that the node can check what it wrote.
func (c *Client) FetchFile(ctx context.Context, env, path, etag string) (body []byte, digest string, notModified bool, err error) {
	escaped := PathFiles + url.PathEscape(env)
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		escaped += "/" + url.PathEscape(segment)
	}
	return c.get(ctx, escaped, etag)
}

func (c *Client) get(ctx context.Context, path, etag string) ([]byte, string, bool, error) {
	client, err := c.client()
	if err != nil {
		return nil, "", false, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path), nil)
	if err != nil {
		return nil, "", false, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", `"`+etag+`"`)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	res, err := client.Do(req)
	if err != nil {
		return nil, "", false, fmt.Errorf("%s: %w", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotModified {
		return nil, res.Header.Get("X-Halite-Hash"), true, nil
	}
	payload, err := io.ReadAll(io.LimitReader(res.Body, MaxRequestBody))
	if err != nil {
		return nil, "", false, fmt.Errorf("%s: %w", path, err)
	}
	if res.StatusCode >= 400 {
		return nil, "", false, decodeError(path, payload, res.StatusCode)
	}
	return payload, res.Header.Get("X-Halite-Hash"), false, nil
}

// Pillar asks the hub to compile this node's pillar.
func (c *Client) Pillar(ctx context.Context, req PillarRequest) (*PillarResponse, error) {
	var res PillarResponse
	if _, err := c.post(ctx, PathPillar, req, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// ResumeJob asks the hub to pick up a batch it was part way through.
func (c *Client) ResumeJob(ctx context.Context, jid string) (*ResumeResponse, error) {
	var res ResumeResponse
	if _, err := c.post(ctx, PathJob+jid+"/resume", struct{}{}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// SendEvent puts something on the hub's bus. The hub namespaces the
// tag under this node.
func (c *Client) SendEvent(ctx context.Context, req EventRequest) (*EventResponse, error) {
	var res EventResponse
	if _, err := c.post(ctx, PathEvent, req, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// FollowEvents streams the bus, calling onEvent for each record until
// the context ends. A non-following read returns when the log is
// exhausted.
func (c *Client) FollowEvents(ctx context.Context, tags []string, from string, follow bool, limit int, onEvent func(json.RawMessage) error) error {
	client, err := c.client()
	if err != nil {
		return err
	}
	query := url.Values{}
	for _, tag := range tags {
		query.Add("tag", tag)
	}
	if from != "" {
		query.Set("from", from)
	}
	if follow {
		query.Set("follow", "true")
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(PathEvents+"?"+query.Encode()), nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		return decodeError(PathEvents, payload, res.StatusCode)
	}
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), MaxSubscribeLine)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := onEvent(append(json.RawMessage(nil), line...)); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// KillJob stops a job that has not finished.
func (c *Client) KillJob(ctx context.Context, jid string) (*KillResponse, error) {
	var res KillResponse
	if _, err := c.post(ctx, PathJob+jid+"/kill", struct{}{}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// PushGrains sends a refreshed fact set to the hub.
func (c *Client) PushGrains(ctx context.Context, req GrainsRequest) error {
	client, err := c.client()
	if err != nil {
		return err
	}
	raw, err := marshal(req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, c.url(PathGrains), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("%s: %w", PathGrains, err)
	}
	defer res.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode >= 400 {
		return decodeError(PathGrains, payload, res.StatusCode)
	}
	return nil
}
