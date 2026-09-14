package awsauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TokenTTL is how long an IMDSv2 session token is asked for.
//
// Five minutes, and refreshed a minute early. A metadata walk makes one
// request per node of the tree, and taking a fresh token for each of
// them turns a hundred reads into two hundred.
const TokenTTL = 5 * time.Minute

// IMDS reads the EC2 instance metadata service.
//
// IMDSv2 only, for the reason SPEC 14.1 gives and `fetchInstanceCredentials`
// repeats: IMDSv1 is a plain GET on a link-local address that any process
// on the instance — and any server-side request forgery in an application
// on it — can make. There is no fallback, because a fallback is the
// hardening given away for a convenience nobody asked for.
//
// One of these is shared by the credential provider and by cloud grain
// collection, so that the token exchange exists once.
type IMDS struct {
	// Address overrides the link-local endpoint. Empty is IMDSAddress.
	Address string
	// Client reaches the service. Nil takes one with a short timeout: a
	// machine that is not on EC2 does not answer at all, and every
	// caller waits for that.
	Client *http.Client
	// Now is the clock, for the tests.
	Now func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

func (m *IMDS) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *IMDS) address() string {
	if m.Address != "" {
		return strings.TrimSuffix(m.Address, "/")
	}
	return IMDSAddress
}

func (m *IMDS) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return &http.Client{Timeout: 5 * time.Second}
}

// Token returns a session token, taking a new one when the cached one is
// close to expiring.
func (m *IMDS) Token(ctx context.Context) (string, error) {
	m.mu.Lock()
	if m.token != "" && m.now().Add(time.Minute).Before(m.expires) {
		defer m.mu.Unlock()
		return m.token, nil
	}
	m.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, m.address()+"/latest/api/token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds",
		fmt.Sprintf("%d", int(TokenTTL.Seconds())))
	res, err := m.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("the instance metadata service did not answer: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the instance metadata service answered %d to a token request; "+
			"IMDSv1 is not used", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 4096))
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(body))
	if token == "" {
		return "", fmt.Errorf("the instance metadata service returned an empty token")
	}

	m.mu.Lock()
	m.token = token
	m.expires = m.now().Add(TokenTTL)
	m.mu.Unlock()
	return token, nil
}

// ErrNotFound is what Get reports for a 404.
//
// A metadata walk asks for paths that do not exist on every instance
// shape — no IAM role, no spot section, no IPv6 — and a missing one is
// an absent grain rather than a collection failure.
var ErrNotFound = fmt.Errorf("not present in the instance metadata")

// Get reads one metadata path, such as `latest/meta-data/instance-id`.
//
// A leading slash is accepted. The body is bounded by MaxMetadataBody:
// the service is reached over plain HTTP on a link-local address, which
// is not a reason to read an unbounded response.
func (m *IMDS) Get(ctx context.Context, path string) ([]byte, error) {
	token, err := m.Token(ctx)
	if err != nil {
		return nil, err
	}
	endpoint := m.address() + "/" + strings.TrimPrefix(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-aws-ec2-metadata-token", token)
	res, err := m.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxMetadataBody))
	if err != nil {
		return nil, err
	}
	switch res.StatusCode {
	case http.StatusOK:
		return body, nil
	case http.StatusNotFound:
		return nil, fmt.Errorf("%s: %w", path, ErrNotFound)
	default:
		return nil, fmt.Errorf("%s answered %d", endpoint, res.StatusCode)
	}
}
