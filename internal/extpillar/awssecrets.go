package extpillar

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/awsauth"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/value"
)

// AWSSecretsName is what the `ext_pillar` list calls this source.
const AWSSecretsName = "aws_secrets_manager"

// DefaultSecretsRoot is the pillar key everything lands under.
//
// The same key Salt's `aws_secrets_manager.py` used, so that
// `pillar.get('aws_secrets:database:password')` in an existing tree
// resolves without being rewritten.
const DefaultSecretsRoot = "aws_secrets"

// DefaultCacheTTL matches the Salt module's 300 seconds.
const DefaultCacheTTL = 5 * time.Minute

// Secret is one secret to fetch and where to put it.
type Secret struct {
	// Key is the pillar key under the root. Dots nest it, so
	// `base.ubuntu_pro` lands at `aws_secrets:base:ubuntu_pro`.
	Key string
	// SecretID is a name or an ARN. The service takes either.
	SecretID string
	// Region overrides the region the ARN or the configuration implies.
	Region string
	// Stage selects a version other than AWSCURRENT.
	Stage string
}

// AWSSecretsOptions configure the source.
type AWSSecretsOptions struct {
	// Secrets are what the hub's own configuration names.
	Secrets []Secret
	// Root is the pillar key to nest everything under. Empty is
	// DefaultSecretsRoot.
	Root string
	// Region is the fallback when a secret is named rather than given
	// as an ARN and the node has no `region` grain.
	Region string
	// Partition is `aws`, `aws-us-gov`, or `aws-cn`, for secrets whose
	// ARN does not say.
	Partition string
	// Endpoint overrides the host, for a compatible service.
	Endpoint string
	// Provider resolves credentials.
	Provider *awsauth.Provider
	Client   *http.Client
	Timeout  time.Duration
	// CacheTTL is how long a fetched value is reused. Zero is
	// DefaultCacheTTL; negative disables the cache.
	CacheTTL time.Duration

	// NodeGrain is a grain naming further secrets the node itself wants,
	// in the shape of the configured list. Empty disables it.
	//
	// This is Salt parity: a shared-state tree renders the hub's
	// `ext_pillar` block from a per-node list. It is worth being plain
	// about what it means — a node controls its own grains, so enabling
	// this lets any node ask the hub to fetch any secret the hub's
	// credentials can read. NodeGrainAllow is how that is bounded.
	NodeGrain string
	// NodeGrainAllow are glob patterns a node-named secret ID must
	// match. Empty allows any, which is the Salt behaviour.
	//
	// They bound PillarList too, though a list that came from the tree
	// is already the hub's own.
	NodeGrainAllow []string

	// PillarList is a key in the pillar compiled so far naming further
	// secrets, in the same shape as the configured list. Empty disables
	// it.
	//
	// The other half of Salt parity, and the better half: the Python
	// module read `aws_secrets_ext_pillar` out of the pillar, which
	// comes from the tree and which a node cannot write. Prefer it to
	// NodeGrain wherever the choice is open — it selects secrets per
	// node exactly as well, through the pillar top file, and a node has
	// no say in what it asks for.
	PillarList string

	// FailIgnore is `ext_pillar_fail: ignore` for this source.
	FailIgnore bool
	// OnSecret receives every value fetched, for the redactor of SPEC
	// 26.1, so that a secret cannot reach a log by being interpolated
	// into a message.
	OnSecret func(string)
	Log      func(level, msg string, kv ...any)
	Now      func() time.Time
}

// AWSSecrets fetches secrets from AWS Secrets Manager into pillar.
//
// A compiled-in replacement for the `aws_secrets_manager.py` external
// pillar: the same `aws_secrets` root, the same dotted-key nesting, the
// same automatic JSON parsing, and the same cache.
//
// SPEC 12.7's table does not name this source. It is here because a
// tree being migrated depends on it, which the table was written
// without knowing. DIVERGENCE 5.78 records that.
type AWSSecrets struct {
	opts   AWSSecretsOptions
	client *secretsClient

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	value any
	at    time.Time
}

// NewAWSSecrets builds the source, refusing a configuration that cannot
// work.
//
// Refused at construction rather than at the first compilation: a hub
// that starts and then fails every node's pillar is a worse way to
// learn that a secret has no key than a hub that does not start.
func NewAWSSecrets(opts AWSSecretsOptions) (*AWSSecrets, error) {
	for i, s := range opts.Secrets {
		if s.Key == "" {
			return nil, fmt.Errorf("%s: secret %d has no `name`", AWSSecretsName, i+1)
		}
		if s.SecretID == "" {
			return nil, fmt.Errorf("%s: the secret %q has no `secret_id`", AWSSecretsName, s.Key)
		}
	}
	for _, p := range opts.NodeGrainAllow {
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("%s: %q is not a usable pattern: %v", AWSSecretsName, p, err)
		}
	}
	if opts.Provider == nil {
		return nil, fmt.Errorf("%s: no credential provider", AWSSecretsName)
	}
	return &AWSSecrets{
		opts:  opts,
		cache: map[string]cacheEntry{},
		client: &secretsClient{
			Provider:  opts.Provider,
			Client:    opts.Client,
			Partition: opts.Partition,
			Endpoint:  opts.Endpoint,
			Timeout:   opts.Timeout,
			Now:       opts.Now,
		},
	}, nil
}

// Name identifies the source in a diagnostic.
func (a *AWSSecrets) Name() string { return AWSSecretsName }

// FailSoft reports `ext_pillar_fail: ignore`.
func (a *AWSSecrets) FailSoft() bool { return a.opts.FailIgnore }

func (a *AWSSecrets) now() time.Time {
	if a.opts.Now != nil {
		return a.opts.Now()
	}
	return time.Now()
}

func (a *AWSSecrets) log(level, msg string, kv ...any) {
	if a.opts.Log != nil {
		a.opts.Log(level, msg, kv...)
	}
}

func (a *AWSSecrets) root() string {
	if a.opts.Root != "" {
		return a.opts.Root
	}
	return DefaultSecretsRoot
}

// Pillar fetches every configured secret and returns them nested under
// the root key.
//
// A secret that cannot be fetched fails the whole source rather than
// being dropped, because the compiler's contract is that a pillar is
// complete or it is an error. Salt logged and carried on, which is how
// a state comes to apply with an empty password.
func (a *AWSSecrets) Pillar(ctx context.Context, req pillar.ExtRequest) (*value.Map, error) {
	secrets, err := a.secretsFor(req)
	if err != nil {
		return nil, err
	}
	if len(secrets) == 0 {
		return nil, nil
	}

	flat := map[string]any{}
	var keys []string
	for _, s := range secrets {
		region := a.regionFor(s, req)
		if region == "" {
			return nil, fmt.Errorf("the secret %q names neither a region nor an ARN carrying one, "+
				"and neither `aws_secrets_region` nor this node's `region` grain is set", s.Key)
		}
		val, err := a.fetch(ctx, s, region)
		if err != nil {
			return nil, fmt.Errorf("fetching %q for the pillar key %q in %s: %w",
				s.SecretID, s.Key, region, err)
		}
		if _, dup := flat[s.Key]; dup {
			return nil, fmt.Errorf("two secrets both claim the pillar key %q", s.Key)
		}
		flat[s.Key] = val
		keys = append(keys, s.Key)
	}

	a.log("debug", "external pillar fetched secrets",
		"source", AWSSecretsName, "node_id", req.NodeID, "count", len(keys))

	out := value.NewMap(1)
	out.Set(a.root(), nest(flat, keys))
	return out, nil
}

// secretsFor is the configured list, plus what the tree asked for, plus
// what the node asked for.
//
// In that order, so that a hub reading both sees the configured list
// first and the node's own last — and so that the duplicate-key check
// in Pillar names the later one.
func (a *AWSSecrets) secretsFor(req pillar.ExtRequest) ([]Secret, error) {
	out := append([]Secret{}, a.opts.Secrets...)

	if a.opts.PillarList != "" && req.Pillar != nil {
		found, err := a.listFrom(req.Pillar, a.opts.PillarList, "pillar key")
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	if a.opts.NodeGrain != "" && req.Grains != nil {
		found, err := a.listFrom(req.Grains, a.opts.NodeGrain, "grain")
		if err != nil {
			return nil, err
		}
		// Only these are checked against the allow list. A list that
		// came from the tree is the hub's own writing; one that came
		// from a grain is the node's.
		for _, s := range found {
			if err := a.allowed(s.SecretID); err != nil {
				return nil, err
			}
		}
		out = append(out, found...)
	}
	return out, nil
}

// listFrom reads a list of secret specifications out of a map.
func (a *AWSSecrets) listFrom(from *value.Map, key, kind string) ([]Secret, error) {
	raw, ok := value.Traverse(from, key, ":")
	if !ok || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("the %q %s is %s, not a list of secrets",
			key, kind, value.TypeName(raw))
	}
	out := make([]Secret, 0, len(list))
	for i, item := range list {
		m, ok := item.(*value.Map)
		if !ok {
			return nil, fmt.Errorf("entry %d of the %q %s is %s, not a mapping",
				i+1, key, kind, value.TypeName(item))
		}
		s, err := SecretFromMap(m)
		if err != nil {
			return nil, fmt.Errorf("entry %d of the %q %s: %w", i+1, key, kind, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// allowed checks a node-named secret against the patterns.
//
// Refused loudly rather than skipped. A node that asks for a secret it
// may not have is either misconfigured or probing, and both are worth
// seeing; the failure is confined to that node's own pillar.
func (a *AWSSecrets) allowed(secretID string) error {
	if len(a.opts.NodeGrainAllow) == 0 {
		return nil
	}
	for _, pattern := range a.opts.NodeGrainAllow {
		if ok, err := path.Match(pattern, secretID); err == nil && ok {
			return nil
		}
	}
	return fmt.Errorf("this node asked for the secret %q through its %q grain, and no pattern in "+
		"`aws_secrets_node_grain_allow` permits it", secretID, a.opts.NodeGrain)
}

// regionFor decides which region a secret is read from.
//
// Explicit configuration first, so an operator who sets one is never
// surprised; then the ARN, which carries the truth when it is given;
// then the node's own `region` grain, which is what the Salt module
// reached for; then the hub's default.
func (a *AWSSecrets) regionFor(s Secret, req pillar.ExtRequest) string {
	if s.Region != "" {
		return s.Region
	}
	if r := regionFromARN(s.SecretID); r != "" {
		return r
	}
	if req.Grains != nil {
		if r, ok := req.Grains.Get("region"); ok {
			if str, ok := r.(string); ok && str != "" {
				return str
			}
		}
	}
	return a.opts.Region
}

// fetch reads one secret, through the cache.
func (a *AWSSecrets) fetch(ctx context.Context, s Secret, region string) (any, error) {
	key := region + "|" + s.SecretID + "|" + s.Stage
	ttl := a.opts.CacheTTL
	if ttl == 0 {
		ttl = DefaultCacheTTL
	}
	if ttl > 0 {
		a.mu.Lock()
		entry, ok := a.cache[key]
		a.mu.Unlock()
		if ok && a.now().Sub(entry.at) < ttl {
			return entry.value, nil
		}
	}

	// The partition an ARN names wins over the configured one, so a
	// GovCloud ARN is signed against a GovCloud endpoint in a hub that
	// also reads commercial secrets.
	client := a.client
	if p := partitionFromARN(s.SecretID); p != "" && p != a.opts.Partition && a.opts.Endpoint == "" {
		clone := *a.client
		clone.Partition = p
		client = &clone
	}

	text, err := client.GetSecretValue(ctx, s.SecretID, region, s.Stage)
	if err != nil {
		return nil, err
	}
	if a.opts.OnSecret != nil {
		a.opts.OnSecret(text)
	}

	parsed := parseSecret(text, a.opts.OnSecret)
	if ttl > 0 {
		a.mu.Lock()
		a.cache[key] = cacheEntry{value: parsed, at: a.now()}
		a.mu.Unlock()
	}
	return parsed, nil
}

// parseSecret turns a secret string into a pillar value.
//
// A JSON object becomes a map, so `pillar.get('aws_secrets:db:password')`
// reaches into it; anything else stays the string it is. This is the
// Salt module's behaviour, and it is what every tree that stores a
// credential pair as JSON depends on.
func parseSecret(text string, onSecret func(string)) any {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return text
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return text
	}
	out := value.NewMap(len(decoded))
	names := make([]string, 0, len(decoded))
	for k := range decoded {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		v := decoded[k]
		// Every leaf of a JSON secret is a secret, so each one is
		// offered to the redactor rather than only the envelope.
		if s, ok := v.(string); ok && onSecret != nil {
			onSecret(s)
		}
		out.Set(k, v)
	}
	return out
}

// nest expands dotted keys into a tree.
//
// `base.ubuntu_pro` becomes `base: {ubuntu_pro: ...}`, which is how the
// Salt module let one pillar key be assembled from several secrets.
func nest(flat map[string]any, order []string) *value.Map {
	root := value.NewMap(len(flat))
	for _, key := range order {
		parts := strings.Split(key, ".")
		cur := root
		for _, part := range parts[:len(parts)-1] {
			next, ok := cur.Get(part)
			asMap, isMap := next.(*value.Map)
			if !ok || !isMap {
				asMap = value.NewMap(4)
				cur.Set(part, asMap)
			}
			cur = asMap
		}
		cur.Set(parts[len(parts)-1], flat[key])
	}
	return root
}

// SecretFromMap reads one secret specification.
//
// The Salt module accepted `name` and `pillar_key` for the same thing,
// and both spellings are in this estate's trees, so both are read here.
func SecretFromMap(m *value.Map) (Secret, error) {
	var s Secret
	for _, e := range m.Entries() {
		switch key := value.KeyString(e.Key); key {
		case "name", "pillar_key":
			s.Key = value.KeyString(e.Val)
		case "secret_id", "secret_name":
			s.SecretID = value.KeyString(e.Val)
		case "region":
			s.Region = value.KeyString(e.Val)
		case "version_stage", "stage":
			s.Stage = value.KeyString(e.Val)
		default:
			// A misspelt key is a setting that does nothing, which is
			// the failure this project's configuration handling exists
			// to prevent.
			return Secret{}, fmt.Errorf("%q is not a per-secret setting; use name (or pillar_key), "+
				"secret_id (or secret_name), region, or version_stage", key)
		}
	}
	if s.Key == "" {
		return Secret{}, fmt.Errorf("a secret needs a `name`")
	}
	if s.SecretID == "" {
		return Secret{}, fmt.Errorf("the secret %q needs a `secret_id`", s.Key)
	}
	return s, nil
}
