package main

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/awsauth"
)

// DefaultRoot is the pillar key everything lands under.
//
// The same key Salt's `aws_secrets_manager.py` used, so that
// `pillar.get('aws_secrets:database:password')` in an existing tree
// resolves without being rewritten.
const DefaultRoot = "aws_secrets"

// DefaultCacheTTL matches the Salt module's 300 seconds.
const DefaultCacheTTL = 5 * time.Minute

// Secret is one secret to fetch and where to put it.
type Secret struct {
	// Key is the pillar key under the root. Dots nest it, so
	// `base.ubuntu_pro` lands at `aws_secrets:base:ubuntu_pro`.
	Key string
	// SecretID is a name or an ARN. The service takes either.
	SecretID string
	// Region overrides the region the ARN or the block implies.
	Region string
	// Stage selects a version other than AWSCURRENT.
	Stage string
}

// UnmarshalJSON reads one secret specification.
//
// Salt's module accepted `name` and `pillar_key` for the same thing,
// and `secret_id` and `secret_name`, and both spellings are in real
// trees, so both are read. A key that is none of them is refused: a
// misspelt setting that does nothing is the failure this project's
// configuration handling exists to prevent, and an extension is not
// exempt from that because its configuration arrives over a pipe.
func (s *Secret) UnmarshalJSON(b []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("a secret is a mapping: %w", err)
	}
	for k, v := range raw {
		text, _ := v.(string)
		switch k {
		case "name", "pillar_key":
			s.Key = text
		case "secret_id", "secret_name":
			s.SecretID = text
		case "region":
			s.Region = text
		case "version_stage", "stage":
			s.Stage = text
		default:
			return fmt.Errorf("%q is not a per-secret setting; use name (or pillar_key), "+
				"secret_id (or secret_name), region, or version_stage", k)
		}
	}
	if s.Key == "" {
		return fmt.Errorf("a secret needs a `name`")
	}
	if s.SecretID == "" {
		return fmt.Errorf("the secret %q needs a `secret_id`", s.Key)
	}
	return nil
}

// Config is this source's `ext_pillar` block.
type Config struct {
	// Secrets is what the hub's own configuration names.
	Secrets []Secret `json:"secrets"`
	// Root is the pillar key to nest everything under.
	Root string `json:"pillar_key"`
	// Region is the fallback when a secret is named rather than given
	// as an ARN and the node has no `region` grain.
	Region string `json:"region"`
	// Partition is `aws`, `aws-us-gov`, or `aws-cn`, for secrets whose
	// ARN does not say.
	Partition string `json:"partition"`
	// Endpoint overrides the host, for a compatible service.
	Endpoint string `json:"endpoint"`
	// CacheTTL is how long a fetched value is reused. Zero is
	// DefaultCacheTTL; negative disables the cache.
	CacheTTL Duration `json:"cache_ttl"`
	// Timeout bounds the whole call.
	Timeout Duration `json:"timeout"`

	// PillarList is a key in the pillar the tree produced naming
	// further secrets. Hub-controlled, and the one to prefer.
	PillarList string `json:"pillar_list"`
	// NodeGrain is a grain naming further secrets the node wants.
	//
	// Salt parity, and worth being plain about: a node controls its own
	// grains, so this lets any node ask the hub to fetch any secret the
	// hub's credentials can read. NodeGrainAllow bounds it.
	NodeGrain string `json:"node_grain"`
	// NodeGrainAllow are glob patterns a node-named secret ID must
	// match. Empty allows any, which is the Salt behaviour.
	NodeGrainAllow []string `json:"node_grain_allow"`

	// Credentials. The instance role needs none of these.
	AccessKeyID          string `json:"access_key_id"`
	SecretAccessKeyFile  string `json:"secret_access_key_file"`
	RoleARN              string `json:"role_arn"`
	RoleSession          string `json:"role_session"`
	WebIdentityTokenFile string `json:"web_identity_token_file"`
}

// Duration reads Go's duration spelling out of JSON, so a block can say
// `cache_ttl: 5m` rather than a number whose unit the reader guesses.
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var text string
	if err := json.Unmarshal(b, &text); err == nil {
		parsed, err := time.ParseDuration(text)
		if err != nil {
			return fmt.Errorf("%q is not a duration: %w", text, err)
		}
		*d = Duration(parsed)
		return nil
	}
	var seconds float64
	if err := json.Unmarshal(b, &seconds); err != nil {
		return fmt.Errorf("a duration is `5m` or a number of seconds")
	}
	*d = Duration(time.Duration(seconds * float64(time.Second)))
	return nil
}

// parseConfig reads the block, refusing a key it does not know.
func parseConfig(raw json.RawMessage) (*Config, error) {
	cfg := &Config{}
	if len(raw) == 0 || string(raw) == "null" {
		return cfg, nil
	}

	// Salt writes most external pillar blocks as a bare list of
	// entries, and this one is usually written that way too. Both are
	// read: a list is the secrets, a mapping is the whole block.
	if trimmed := strings.TrimSpace(string(raw)); strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal(raw, &cfg.Secrets); err != nil {
			return nil, err
		}
		return cfg, nil
	}

	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("the aws_secrets_manager block: %w", err)
	}
	for _, p := range cfg.NodeGrainAllow {
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("%q is not a usable pattern: %v", p, err)
		}
	}
	return cfg, nil
}

func (c *Config) root() string {
	if c.Root != "" {
		return c.Root
	}
	return DefaultRoot
}

func (c *Config) cacheTTL() time.Duration {
	if c.CacheTTL == 0 {
		return DefaultCacheTTL
	}
	return time.Duration(c.CacheTTL)
}

func (c *Config) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 30 * time.Second
	}
	return time.Duration(c.Timeout)
}

// provider is the credential chain of SPEC 13.4.
//
// The instance role is the end of it and needs nothing configured,
// which is the arrangement to prefer: a static key in a configuration
// file is a secret guarding the other secrets.
func (c *Config) provider() *awsauth.Provider {
	return &awsauth.Provider{
		Explicit: awsauth.Credentials{
			AccessKeyID:     c.AccessKeyID,
			SecretAccessKey: readSecretFile(c.SecretAccessKeyFile),
		},
		Partition:            c.Partition,
		Region:               c.Region,
		RoleARN:              c.RoleARN,
		RoleSession:          c.RoleSession,
		WebIdentityTokenFile: c.WebIdentityTokenFile,
	}
}

// regionFor decides which region a secret is read from.
//
// Explicit configuration first, so an operator who sets one is never
// surprised; then the ARN, which carries the truth when it is given;
// then the node's own `region` grain, which is what the Salt module
// reached for; then the block's default.
func (c *Config) regionFor(s Secret, grains map[string]any) string {
	if s.Region != "" {
		return s.Region
	}
	if r := regionFromARN(s.SecretID); r != "" {
		return r
	}
	if r, ok := grains["region"].(string); ok && r != "" {
		return r
	}
	return c.Region
}

// secretsFor is the configured list, plus what the tree asked for, plus
// what the node asked for, in that order.
func (c *Config) secretsFor(grains, treePillar map[string]any) ([]Secret, error) {
	out := append([]Secret{}, c.Secrets...)

	if c.PillarList != "" {
		found, err := listFrom(treePillar, c.PillarList, "pillar key")
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	if c.NodeGrain != "" {
		found, err := listFrom(grains, c.NodeGrain, "grain")
		if err != nil {
			return nil, err
		}
		// Only these are checked against the allow list. A list that
		// came from the tree is the hub's own writing; one that came
		// from a grain is the node's.
		for _, s := range found {
			if err := c.allowed(s.SecretID); err != nil {
				return nil, err
			}
		}
		out = append(out, found...)
	}
	return out, nil
}

// allowed checks a node-named secret against the patterns.
//
// Refused loudly rather than skipped. A node that asks for a secret it
// may not have is either misconfigured or probing, and both are worth
// seeing; the failure is confined to that node's own pillar.
func (c *Config) allowed(secretID string) error {
	if len(c.NodeGrainAllow) == 0 {
		return nil
	}
	for _, pattern := range c.NodeGrainAllow {
		if ok, err := path.Match(pattern, secretID); err == nil && ok {
			return nil
		}
	}
	return fmt.Errorf("this node asked for the secret %q through its %q grain, and no pattern in "+
		"`node_grain_allow` permits it", secretID, c.NodeGrain)
}

// listFrom reads a list of secret specifications out of a decoded map.
func listFrom(from map[string]any, key, kind string) ([]Secret, error) {
	raw, ok := traverse(from, key)
	if !ok || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("the %q %s is not a list of secrets", key, kind)
	}
	out := make([]Secret, 0, len(list))
	for i, item := range list {
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("entry %d of the %q %s: %w", i+1, key, kind, err)
		}
		var s Secret
		if err := json.Unmarshal(encoded, &s); err != nil {
			return nil, fmt.Errorf("entry %d of the %q %s: %w", i+1, key, kind, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// traverse walks a colon-delimited path, so a list can live somewhere
// other than the top level.
func traverse(from map[string]any, key string) (any, bool) {
	var cur any = from
	for _, part := range strings.Split(key, ":") {
		node, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = node[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// nest expands dotted keys into a tree.
//
// `base.ubuntu_pro` becomes `base: {ubuntu_pro: ...}`, which is how the
// Salt module let one pillar key be assembled from several secrets.
func nest(flat map[string]any, order []string) map[string]any {
	root := map[string]any{}
	for _, key := range order {
		parts := strings.Split(key, ".")
		cur := root
		for _, part := range parts[:len(parts)-1] {
			next, ok := cur[part].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[part] = next
			}
			cur = next
		}
		cur[parts[len(parts)-1]] = flat[key]
	}
	return root
}
