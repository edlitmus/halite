package awsauth

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MaxMetadataBody bounds what the metadata service or STS may return.
//
// Both are reached over plain HTTP on a link-local address, or over TLS
// to an endpoint the configuration names. Neither is a reason to read
// an unbounded body.
const MaxMetadataBody = 1 << 20

// IMDSAddress is the EC2 instance metadata service.
const IMDSAddress = "http://169.254.169.254"

// ECSAddress is where a task's credentials live.
const ECSAddress = "http://169.254.170.2"

// Provider resolves credentials.
//
// The order is SPEC 13.4's: explicit configuration, environment
// variables, the instance metadata service with IMDSv2, and the web
// identity token file for IRSA. Each is tried in turn and the first
// that answers wins, so an operator who sets a key in the configuration
// is never surprised by an instance role.
type Provider struct {
	// Explicit is what the configuration named. Used first when it is
	// complete.
	Explicit Credentials
	// Client reaches the metadata service and STS. Nil takes one with a
	// short timeout: a metadata service that is not there must fail
	// fast, because every request waits for it.
	Client *http.Client
	// Partition is `aws`, `aws-us-gov`, or `aws-cn`. It decides the STS
	// endpoint, so an ARN is never constructed against the wrong one.
	Partition string
	// Region is where STS is called.
	Region string
	// RoleARN, when set, is assumed after the base credentials
	// resolve. SPEC 13.4's two signed STS calls.
	RoleARN     string
	RoleSession string
	// WebIdentityTokenFile is IRSA's. When it and RoleARN are both set,
	// `AssumeRoleWithWebIdentity` is used and no base credentials are
	// needed.
	WebIdentityTokenFile string
	// Now is the clock, for the tests.
	Now func() time.Time
	// Environ reads the environment, for the tests.
	Environ func(string) string

	mu     sync.Mutex
	cached Credentials
}

func (p *Provider) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Provider) env(name string) string {
	if p.Environ != nil {
		return p.Environ(name)
	}
	return os.Getenv(name)
}

func (p *Provider) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	// Short, and deliberately. A machine that is not on EC2 has no
	// metadata service, and the link-local address does not answer;
	// every request would otherwise wait for a connection that cannot
	// be made.
	return &http.Client{Timeout: 5 * time.Second}
}

// Retrieve answers with usable credentials, refreshing when the cached
// ones are close to expiring.
func (p *Provider) Retrieve(ctx context.Context) (Credentials, error) {
	p.mu.Lock()
	if p.cached.Valid(p.now()) {
		defer p.mu.Unlock()
		return p.cached, nil
	}
	p.mu.Unlock()

	creds, err := p.resolve(ctx)
	if err != nil {
		return Credentials{}, err
	}
	p.mu.Lock()
	p.cached = creds
	p.mu.Unlock()
	return creds, nil
}

func (p *Provider) resolve(ctx context.Context) (Credentials, error) {
	// IRSA first when it is configured, because a pod that has a web
	// identity token has it *instead* of an instance role, and falling
	// through to the node's role would give it the wrong identity.
	if p.WebIdentityTokenFile != "" && p.RoleARN != "" {
		return p.assumeWithWebIdentity(ctx)
	}

	base, err := p.baseCredentials(ctx)
	if err != nil {
		return Credentials{}, err
	}
	if p.RoleARN == "" {
		return base, nil
	}
	return p.assumeRole(ctx, base)
}

// baseCredentials is the chain before any role is assumed.
func (p *Provider) baseCredentials(ctx context.Context) (Credentials, error) {
	if p.Explicit.AccessKeyID != "" && p.Explicit.SecretAccessKey != "" {
		return p.Explicit, nil
	}
	if id := p.env("AWS_ACCESS_KEY_ID"); id != "" {
		return Credentials{
			AccessKeyID:     id,
			SecretAccessKey: p.env("AWS_SECRET_ACCESS_KEY"),
			SessionToken:    p.env("AWS_SESSION_TOKEN"),
		}, nil
	}
	// A task's credentials, which ECS and Fargate supply.
	if uri := p.env("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"); uri != "" {
		return p.fetchContainerCredentials(ctx, ECSAddress+uri)
	}
	if uri := p.env("AWS_CONTAINER_CREDENTIALS_FULL_URI"); uri != "" {
		return p.fetchContainerCredentials(ctx, uri)
	}
	return p.fetchInstanceCredentials(ctx)
}

// containerCredentials is what the ECS credential endpoint and IMDS
// both answer with.
type containerCredentials struct {
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	Token           string `json:"Token"`
	Expiration      string `json:"Expiration"`
	Code            string `json:"Code"`
	Message         string `json:"Message"`
}

func (c containerCredentials) toCredentials() (Credentials, error) {
	if c.Code != "" && c.Code != "Success" {
		return Credentials{}, fmt.Errorf("the credential endpoint answered %s: %s", c.Code, c.Message)
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return Credentials{}, fmt.Errorf("the credential endpoint returned no key")
	}
	creds := Credentials{
		AccessKeyID:     c.AccessKeyID,
		SecretAccessKey: c.SecretAccessKey,
		SessionToken:    c.Token,
	}
	if c.Expiration != "" {
		expires, err := time.Parse(time.RFC3339, c.Expiration)
		if err != nil {
			return Credentials{}, fmt.Errorf("the credential expiry %q is not readable", c.Expiration)
		}
		creds.Expires = expires
	}
	return creds, nil
}

func (p *Provider) fetchContainerCredentials(ctx context.Context, endpoint string) (Credentials, error) {
	body, err := p.get(ctx, endpoint, nil)
	if err != nil {
		return Credentials{}, err
	}
	var parsed containerCredentials
	if err := decodeJSON(body, &parsed); err != nil {
		return Credentials{}, err
	}
	return parsed.toCredentials()
}

// fetchInstanceCredentials reads an EC2 instance role through IMDSv2.
//
// IMDSv2 only. IMDSv1 is a plain GET on a link-local address, which any
// process on the instance — and any server-side request forgery in an
// application on it — can make. The token exchange is what makes that
// harder, and falling back to v1 when v2 does not answer would give the
// hardening away for a convenience nobody asked for.
func (p *Provider) fetchInstanceCredentials(ctx context.Context) (Credentials, error) {
	token, err := p.imdsToken(ctx)
	if err != nil {
		return Credentials{}, err
	}
	headers := map[string]string{"X-aws-ec2-metadata-token": token}

	role, err := p.get(ctx, IMDSAddress+"/latest/meta-data/iam/security-credentials/", headers)
	if err != nil {
		return Credentials{}, err
	}
	name := strings.TrimSpace(strings.Split(string(role), "\n")[0])
	if name == "" {
		return Credentials{}, fmt.Errorf("this instance has no role")
	}
	body, err := p.get(ctx, IMDSAddress+"/latest/meta-data/iam/security-credentials/"+url.PathEscape(name), headers)
	if err != nil {
		return Credentials{}, err
	}
	var parsed containerCredentials
	if err := decodeJSON(body, &parsed); err != nil {
		return Credentials{}, err
	}
	return parsed.toCredentials()
}

func (p *Provider) imdsToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, IMDSAddress+"/latest/api/token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "300")
	res, err := p.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("the instance metadata service did not answer: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the instance metadata service answered %d to a token request; "+
			"IMDSv1 is not used", res.StatusCode)
	}
	token, err := io.ReadAll(io.LimitReader(res.Body, 4096))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(token)), nil
}

func (p *Provider) get(ctx context.Context, endpoint string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := p.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxMetadataBody))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d", endpoint, res.StatusCode)
	}
	return body, nil
}

// stsEndpoint is the STS host for a partition and region.
//
// Constructed from the partition rather than hardcoded to `aws`, as
// SPEC 13.4 requires: an ARN or an endpoint built for the commercial
// partition is wrong in GovCloud and in China, and wrong in a way that
// fails at the worst time.
func (p *Provider) stsEndpoint() string {
	region := p.Region
	if region == "" {
		region = "us-east-1"
	}
	switch p.Partition {
	case "aws-cn":
		return "https://sts." + region + ".amazonaws.com.cn"
	case "aws-us-gov":
		return "https://sts." + region + ".amazonaws.com"
	default:
		return "https://sts." + region + ".amazonaws.com"
	}
}

// assumeRoleResult is the shape both STS calls answer with.
type assumeRoleResult struct {
	Credentials struct {
		AccessKeyID     string `xml:"AccessKeyId"`
		SecretAccessKey string `xml:"SecretAccessKey"`
		SessionToken    string `xml:"SessionToken"`
		Expiration      string `xml:"Expiration"`
	} `xml:"AssumeRoleResult>Credentials"`
	WebIdentity struct {
		AccessKeyID     string `xml:"AccessKeyId"`
		SecretAccessKey string `xml:"SecretAccessKey"`
		SessionToken    string `xml:"SessionToken"`
		Expiration      string `xml:"Expiration"`
	} `xml:"AssumeRoleWithWebIdentityResult>Credentials"`
	Error struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
}

func (r assumeRoleResult) credentials() (Credentials, error) {
	if r.Error.Code != "" {
		return Credentials{}, fmt.Errorf("sts: %s: %s", r.Error.Code, r.Error.Message)
	}
	id, secret, token, expiry := r.Credentials.AccessKeyID, r.Credentials.SecretAccessKey,
		r.Credentials.SessionToken, r.Credentials.Expiration
	if id == "" {
		id, secret, token, expiry = r.WebIdentity.AccessKeyID, r.WebIdentity.SecretAccessKey,
			r.WebIdentity.SessionToken, r.WebIdentity.Expiration
	}
	if id == "" || secret == "" {
		return Credentials{}, fmt.Errorf("sts returned no credentials")
	}
	creds := Credentials{AccessKeyID: id, SecretAccessKey: secret, SessionToken: token}
	if expiry != "" {
		parsed, err := time.Parse(time.RFC3339, expiry)
		if err != nil {
			return Credentials{}, fmt.Errorf("the sts expiry %q is not readable", expiry)
		}
		creds.Expires = parsed
	}
	return creds, nil
}

// assumeRole is SPEC 13.4's signed STS call.
func (p *Provider) assumeRole(ctx context.Context, base Credentials) (Credentials, error) {
	form := url.Values{}
	form.Set("Action", "AssumeRole")
	form.Set("Version", "2011-06-15")
	form.Set("RoleArn", p.RoleARN)
	form.Set("RoleSessionName", p.sessionName())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.stsEndpoint(),
		strings.NewReader(form.Encode()))
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	signer := Signer{Region: p.regionOrDefault(), Service: "sts"}
	if err := signer.Sign(req, base, HexSHA256([]byte(form.Encode())), p.now()); err != nil {
		return Credentials{}, err
	}
	return p.callSTS(ctx, req)
}

// assumeWithWebIdentity is IRSA's call, which carries the token instead
// of a signature.
func (p *Provider) assumeWithWebIdentity(ctx context.Context) (Credentials, error) {
	token, err := os.ReadFile(filepath.Clean(p.WebIdentityTokenFile))
	if err != nil {
		return Credentials{}, fmt.Errorf("reading the web identity token: %w", err)
	}
	form := url.Values{}
	form.Set("Action", "AssumeRoleWithWebIdentity")
	form.Set("Version", "2011-06-15")
	form.Set("RoleArn", p.RoleARN)
	form.Set("RoleSessionName", p.sessionName())
	form.Set("WebIdentityToken", strings.TrimSpace(string(token)))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.stsEndpoint(),
		strings.NewReader(form.Encode()))
	if err != nil {
		return Credentials{}, err
	}
	// Unsigned on purpose: the web identity token is the assertion, and
	// there are no credentials yet to sign with. That is what makes
	// IRSA work in a pod with no key material at all.
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return p.callSTS(ctx, req)
}

func (p *Provider) callSTS(ctx context.Context, req *http.Request) (Credentials, error) {
	res, err := p.client().Do(req)
	if err != nil {
		return Credentials{}, fmt.Errorf("reaching sts: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxMetadataBody))
	if err != nil {
		return Credentials{}, err
	}
	var parsed assumeRoleResult
	if err := xml.Unmarshal(body, &parsed); err != nil {
		return Credentials{}, fmt.Errorf("sts answered %d with something that is not a response",
			res.StatusCode)
	}
	return parsed.credentials()
}

func (p *Provider) sessionName() string {
	if p.RoleSession != "" {
		return p.RoleSession
	}
	return "halite"
}

func (p *Provider) regionOrDefault() string {
	if p.Region != "" {
		return p.Region
	}
	return "us-east-1"
}

// decodeJSON is here rather than inline so the two credential endpoints
// parse identically.
func decodeJSON(body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("the credential endpoint answered with something that is not readable: %w", err)
	}
	return nil
}
