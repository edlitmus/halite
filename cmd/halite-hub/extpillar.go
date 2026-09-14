package main

import (
	"fmt"
	"time"

	"github.com/edlitmus/halite/internal/awsauth"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/extpillar"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/value"
)

// extPillarSpec is one entry of `ext_pillar`, parsed.
//
// Parsing is separated from building so that it can be tested: building
// a source resolves credentials and ends in `cli.Fatalf`, and a test
// cannot follow it there.
type extPillarSpec struct {
	// Name is the source the entry named.
	Name string
	// Secrets is the aws_secrets_manager block, when that is the source.
	Secrets []extpillar.Secret
	// FailIgnore is this source's effective `ext_pillar_fail`.
	FailIgnore bool
}

// extPillarSources builds the external pillar sources of SPEC 12.7 from
// `ext_pillar`.
//
// The list keeps Salt's shape — a list of single-key mappings, the key
// naming the source — because that is what an existing Salt
// configuration holds and there is no value in churning it. What has
// changed is underneath: a source is compiled in rather than imported
// from a Python file on the file server, so a name this build does not
// know is refused at startup. Salt would have loaded whatever file
// happened to be there.
func extPillarSources(h *hubContext) []pillar.ExtSource {
	raw, ok := h.cfg.Get("ext_pillar")
	if !ok || raw == nil {
		return nil
	}
	specs, err := parseExtPillar(raw, h.cfg.String("ext_pillar_fail", "hard") == "ignore")
	if err != nil {
		cli.Fatalf("%v", err)
	}
	out := make([]pillar.ExtSource, 0, len(specs))
	for _, spec := range specs {
		switch spec.Name {
		case extpillar.AWSSecretsName:
			out = append(out, awsSecretsSource(h, spec))
		default:
			// Unreachable: parseExtPillar refuses an unknown name. Kept
			// so that adding a source to the parser and forgetting to
			// build it is a failure rather than a silent omission.
			cli.Fatalf("`ext_pillar`: %q parsed and has no builder", spec.Name)
		}
	}
	return out
}

// parseExtPillar reads the `ext_pillar` list.
func parseExtPillar(raw any, defaultFailIgnore bool) ([]extPillarSpec, error) {
	list, isList := raw.([]any)
	if !isList {
		return nil, fmt.Errorf("`ext_pillar` is a list of sources, not %s", value.TypeName(raw))
	}
	var out []extPillarSpec
	for _, item := range list {
		m, isMap := item.(*value.Map)
		if !isMap {
			return nil, fmt.Errorf("`ext_pillar`: an entry is a mapping naming one source, not %s; "+
				"a source that takes no configuration is written as `- %s: []`",
				value.TypeName(item), extpillar.AWSSecretsName)
		}
		for _, e := range m.Entries() {
			name := value.KeyString(e.Key)
			if name != extpillar.AWSSecretsName {
				return nil, fmt.Errorf("`ext_pillar`: %q is not an external pillar source this build "+
					"has. This build ships %s; the rest of Salt's are bridged or not built, and are "+
					"listed in docs/DIVERGENCE.md rather than being loaded from the file server",
					name, extpillar.AWSSecretsName)
			}
			secrets, failIgnore, err := awsSecretsBlock(e.Val, defaultFailIgnore)
			if err != nil {
				return nil, err
			}
			out = append(out, extPillarSpec{Name: name, Secrets: secrets, FailIgnore: failIgnore})
		}
	}
	return out, nil
}

// awsSecretsBlock reads the source's own configuration: a list of
// secrets, optionally with a `fail:` setting among them.
func awsSecretsBlock(block any, failIgnore bool) ([]extpillar.Secret, bool, error) {
	if block == nil {
		return nil, failIgnore, nil
	}
	list, ok := block.([]any)
	if !ok {
		return nil, false, fmt.Errorf("`ext_pillar`: %s takes a list of secrets, not %s",
			extpillar.AWSSecretsName, value.TypeName(block))
	}
	var out []extpillar.Secret
	for i, item := range list {
		m, ok := item.(*value.Map)
		if !ok {
			return nil, false, fmt.Errorf("`ext_pillar`: %s entry %d is %s, not a mapping",
				extpillar.AWSSecretsName, i+1, value.TypeName(item))
		}
		// `- fail: ignore` among the secrets is the per-source spelling
		// of ext_pillar_fail that SPEC 12.7 calls for.
		if m.Len() == 1 {
			if v, ok := m.Get("fail"); ok {
				switch value.KeyString(v) {
				case "ignore":
					failIgnore = true
				case "hard":
					failIgnore = false
				default:
					return nil, false, fmt.Errorf("`ext_pillar`: %s: `fail` is hard or ignore, not %q",
						extpillar.AWSSecretsName, value.KeyString(v))
				}
				continue
			}
		}
		s, err := extpillar.SecretFromMap(m)
		if err != nil {
			return nil, false, fmt.Errorf("`ext_pillar`: %s entry %d: %v",
				extpillar.AWSSecretsName, i+1, err)
		}
		out = append(out, s)
	}
	return out, failIgnore, nil
}

// awsSecretsSource builds the Secrets Manager source.
func awsSecretsSource(h *hubContext, spec extPillarSpec) pillar.ExtSource {
	secret := h.cfg.String("aws_secrets_secret_access_key", "")
	if path := h.cfg.String("aws_secrets_secret_access_key_file", ""); path != "" {
		read, err := config.ReadSecretFile(path)
		if err != nil {
			cli.Fatalf("%s: %v", extpillar.AWSSecretsName, err)
		}
		secret = read
	}
	partition := h.cfg.String("aws_secrets_partition", "aws")
	region := h.cfg.String("aws_secrets_region", "")
	grain := h.cfg.String("aws_secrets_node_grain", "")
	allow := h.cfg.StringSlice("aws_secrets_node_grain_allow")

	source, err := extpillar.NewAWSSecrets(extpillar.AWSSecretsOptions{
		Secrets:   spec.Secrets,
		Root:      h.cfg.String("aws_secrets_pillar_key", extpillar.DefaultSecretsRoot),
		Region:    region,
		Partition: partition,
		Endpoint:  h.cfg.String("aws_secrets_endpoint", ""),
		Provider: &awsauth.Provider{
			Explicit: awsauth.Credentials{
				AccessKeyID:     h.cfg.String("aws_secrets_access_key_id", ""),
				SecretAccessKey: secret,
			},
			Partition:            partition,
			Region:               region,
			RoleARN:              h.cfg.String("aws_secrets_role_arn", ""),
			RoleSession:          h.cfg.String("aws_secrets_role_session", "halite"),
			WebIdentityTokenFile: h.cfg.String("aws_secrets_web_identity_token_file", ""),
		},
		Timeout:        h.cfg.Duration("aws_secrets_timeout", 30*time.Second),
		CacheTTL:       h.cfg.Duration("aws_secrets_cache_ttl", extpillar.DefaultCacheTTL),
		PillarList:     h.cfg.String("aws_secrets_pillar_list", ""),
		NodeGrain:      grain,
		NodeGrainAllow: allow,
		FailIgnore:     spec.FailIgnore,
		Log: func(level, msg string, kv ...any) {
			if level == "warn" || level == "error" {
				h.log.Warn(msg, kv...)
				return
			}
			h.log.Debug(msg, kv...)
		},
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}

	// Said once, at startup, rather than left for an operator to work
	// out from the fact that a node can name its own secrets. The
	// warning is the point: this is the one place where a node's own
	// input selects what the hub fetches.
	if grain != "" {
		if len(allow) == 0 {
			h.log.Warn("nodes may name their own secrets and nothing bounds which; "+
				"a node controls its own grains, so this hub will fetch any secret its credentials "+
				"can read on any node's say-so. Set aws_secrets_node_grain_allow.",
				"setting", "aws_secrets_node_grain", "grain", grain, "section", "12.7")
		} else {
			h.log.Info("nodes may name their own secrets, bounded by pattern",
				"setting", "aws_secrets_node_grain", "grain", grain,
				"patterns", len(allow), "section", "12.7")
		}
	}
	return source
}
