package awsauth

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
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
	// ContainerCredentialsFullURI and ContainerAuthorizationTokenFile
	// are EKS Pod Identity's, named in configuration rather than read
	// from AWS_CONTAINER_CREDENTIALS_FULL_URI and
	// AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE.
	//
	// They exist for a process that is not given the pod's environment.
	// An extension is started with PATH and HOME and nothing else
	// (internal/extension), so aws_secrets_manager in a pod never sees
	// the two variables EKS sets, and reading them from the environment
	// alone would leave Pod Identity unreachable from the one component
	// most likely to want it. Each is used only when it is set; empty
	// falls back to the environment.
	ContainerCredentialsFullURI     string
	ContainerAuthorizationTokenFile string
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
	// Configuration before environment, as for the key above.
	if p.ContainerCredentialsFullURI != "" {
		return p.fetchFullURICredentials(ctx, p.ContainerCredentialsFullURI)
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
		return p.fetchFullURICredentials(ctx, uri)
	}
	return p.fetchInstanceCredentials(ctx)
}

// EKSPodIdentityAddress and EKSPodIdentityAddressV6 are where the EKS
// Pod Identity agent answers, on every node that runs it.
const (
	EKSPodIdentityAddress   = "169.254.170.23"
	EKSPodIdentityAddressV6 = "fd00:ec2::23"
)

// fetchFullURICredentials is the full-URI form of the container
// credential endpoint: EKS Pod Identity, and anything else that hands a
// process a URL and a token.
//
// Two things the first cut did not do (issue 613, item 1):
//
// It sent no Authorization header. Pod Identity's agent refuses a
// request without the pod's token, so the path EKS documents could not
// work -- the request carried `Authorization=""` against a stand-in.
// The token is read here, on every fetch and not once at start, because
// EKS rotates the file under a running pod; a token read at start is a
// pod that loses its credentials when the first one expires.
//
// And it fetched whatever URL it was given, over plain HTTP. The URL
// comes from the environment, and the answer is a key: a full URI
// pointing at an arbitrary host would send the pod's token there and
// take credentials back from it. The AWS SDKs allow plain HTTP only to
// a loopback address or to the ECS and EKS agents' link-local ones, and
// so does this; HTTPS is allowed to any host, because there the host
// has to prove who it is.
func (p *Provider) fetchFullURICredentials(ctx context.Context, uri string) (Credentials, error) {
	if err := checkFullURI(uri); err != nil {
		return Credentials{}, err
	}
	token, err := p.containerAuthorizationToken()
	if err != nil {
		return Credentials{}, err
	}
	var headers map[string]string
	if token != "" {
		headers = map[string]string{"Authorization": token}
	}
	body, err := p.get(ctx, uri, headers)
	if err != nil {
		return Credentials{}, err
	}
	var parsed containerCredentials
	if err := decodeJSON(body, &parsed); err != nil {
		return Credentials{}, err
	}
	return parsed.toCredentials()
}

// containerAuthorizationToken is the token a full-URI request carries.
//
// A file before a value, configuration before environment: the file is
// what EKS sets and what it rotates, and a value in the environment is
// the older ECS-on-anywhere form. Nothing set is no token, which is not
// an error here -- the endpoint will say so if it wanted one, and a
// local endpoint for a test may not.
func (p *Provider) containerAuthorizationToken() (string, error) {
	file := p.ContainerAuthorizationTokenFile
	if file == "" {
		file = p.env("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE")
	}
	token := ""
	if file != "" {
		raw, err := os.ReadFile(filepath.Clean(file))
		if err != nil {
			return "", fmt.Errorf("reading the container authorization token: %w", err)
		}
		token = strings.TrimSpace(string(raw))
	} else {
		token = strings.TrimSpace(p.env("AWS_CONTAINER_AUTHORIZATION_TOKEN"))
	}
	// A header value cannot carry a line break, and Go's client refuses
	// one with a message about an invalid header field that says nothing
	// about where the value came from. Say it here instead.
	if strings.ContainsAny(token, "\r\n") {
		return "", fmt.Errorf("the container authorization token has a line break in it")
	}
	return token, nil
}

// checkFullURI refuses a full URI that would send a token to a host
// that has not proved who it is.
func checkFullURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("the container credentials URI is not a URL: %w", err)
	}
	switch u.Scheme {
	case "https":
		if u.Hostname() == "" {
			return fmt.Errorf("the container credentials URI %q names no host", raw)
		}
		return nil
	case "http":
	default:
		return fmt.Errorf("the container credentials URI %q is neither http nor https", raw)
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	// A literal address only. A name would have to be resolved and every
	// answer checked, and the answer can change between the check and the
	// connection; the agents this exists for are reached by address.
	ip := net.ParseIP(host)
	if ip != nil && (ip.IsLoopback() ||
		ip.Equal(net.ParseIP(ECSAddress[len("http://"):])) ||
		ip.Equal(net.ParseIP(EKSPodIdentityAddress)) ||
		ip.Equal(net.ParseIP(EKSPodIdentityAddressV6))) {
		return nil
	}
	return fmt.Errorf("the container credentials URI %q is plain http to a host that is not "+
		"loopback, %s, %s or %s; use https, or one of those addresses",
		raw, ECSAddress[len("http://"):], EKSPodIdentityAddress, EKSPodIdentityAddressV6)
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
	md := &IMDS{Client: p.client(), Now: p.Now}
	role, err := md.Get(ctx, "latest/meta-data/iam/security-credentials/")
	if err != nil {
		return Credentials{}, err
	}
	name := strings.TrimSpace(strings.Split(string(role), "\n")[0])
	if name == "" {
		return Credentials{}, fmt.Errorf("this instance has no role")
	}
	body, err := md.Get(ctx, "latest/meta-data/iam/security-credentials/"+url.PathEscape(name))
	if err != nil {
		return Credentials{}, err
	}
	var parsed containerCredentials
	if err := decodeJSON(body, &parsed); err != nil {
		return Credentials{}, err
	}
	return parsed.toCredentials()
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
//
// The region goes through ServiceURL, which refuses anything that is
// not a region name before it becomes part of a host. Here the region
// is the operator's configuration rather than a node's, so this is the
// cheaper half of DIVERGENCE 5.324 -- but a signed STS call is the one
// that carries the long-lived key's signature, and the place a region
// becomes a host is where it is checked, whoever supplied it. An
// unknown partition keeps its old meaning (the commercial suffix), so
// the other callers of this provider see no change in what they reach.
func (p *Provider) stsEndpoint() (string, error) {
	region := p.Region
	if region == "" {
		region = "us-east-1"
	}
	suffix := "amazonaws.com"
	if p.Partition == "aws-cn" {
		suffix = "amazonaws.com.cn"
	}
	u, err := ServiceURL("sts", region, suffix)
	if err != nil {
		return "", fmt.Errorf("the sts endpoint: %w", err)
	}
	return u.String(), nil
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

	endpoint, err := p.stsEndpoint()
	if err != nil {
		return Credentials{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
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

	endpoint, err := p.stsEndpoint()
	if err != nil {
		return Credentials{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
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
