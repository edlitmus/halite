package main

import (
	"path/filepath"
	"time"

	"github.com/edlitmus/halite/internal/awsauth"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/s3fs"
	"github.com/edlitmus/halite/internal/value"
)

// s3Backend builds the S3 file server backend of SPEC 13.4, or nil when
// `fileserver_backend` does not name it.
func (h *hubContext) s3Backend() *s3fs.Backend {
	if !namesBackend(h.cfg, "s3") && !namesBackend(h.cfg, "s3fs") {
		return nil
	}
	buckets := s3Buckets(h)
	if len(buckets) == 0 {
		cli.Fatalf("fileserver_backend names s3 and s3_buckets is empty")
	}
	cache := h.cfg.String("s3_cache_dir", "")
	if cache == "" {
		cache = filepath.Join(h.cfg.String("cache_dir", config.DefaultCacheDir), "s3fs")
	}
	backend, err := s3fs.New(s3fs.Options{
		Buckets:   buckets,
		CacheDir:  cache,
		Provider:  s3Provider(h),
		Partition: h.cfg.String("s3_partition", "aws"),
		DualStack: h.cfg.Bool("s3_dualstack", false),
		AllowEnvs: h.cfg.StringSlice("s3_env_allowlist"),
		DenyEnvs:  h.cfg.StringSlice("s3_env_denylist"),
		Timeout:   h.cfg.Duration("s3_timeout", 2*time.Minute),
		Log: func(level, msg string, kv ...any) {
			if level == "warn" || level == "error" {
				h.log.Warn(msg, kv...)
				return
			}
			h.log.Info(msg, kv...)
		},
	})
	if err != nil {
		cli.Fatalf("s3fs: %v", err)
	}
	return backend
}

// s3Provider is the credential chain of SPEC 13.4.
func s3Provider(h *hubContext) *awsauth.Provider {
	secret := h.cfg.String("s3_secret_access_key", "")
	if path := h.cfg.String("s3_secret_access_key_file", ""); path != "" {
		read, err := config.ReadSecretFile(path)
		if err != nil {
			cli.Fatalf("s3fs: %v", err)
		}
		secret = read
	}
	return &awsauth.Provider{
		Explicit: awsauth.Credentials{
			AccessKeyID:     h.cfg.String("s3_access_key_id", ""),
			SecretAccessKey: secret,
		},
		Partition:            h.cfg.String("s3_partition", "aws"),
		Region:               h.cfg.String("s3_region", "us-east-1"),
		RoleARN:              h.cfg.String("s3_role_arn", ""),
		RoleSession:          h.cfg.String("s3_role_session", "halite"),
		WebIdentityTokenFile: h.cfg.String("s3_web_identity_token_file", ""),
	}
}

// s3Buckets reads `s3_buckets`, a list of names or of mappings.
func s3Buckets(h *hubContext) []s3fs.Bucket {
	raw, ok := h.cfg.Get("s3_buckets")
	if !ok || raw == nil {
		return nil
	}
	list, isList := raw.([]any)
	if !isList {
		cli.Fatalf("`s3_buckets` is a list of buckets, not %s", value.TypeName(raw))
	}
	defaults := s3fs.Bucket{
		Region:    h.cfg.String("s3_region", "us-east-1"),
		Endpoint:  h.cfg.String("s3_endpoint", ""),
		PathStyle: h.cfg.Bool("s3_path_style", false),
	}

	var out []s3fs.Bucket
	for _, item := range list {
		switch v := item.(type) {
		case string:
			bucket := defaults
			bucket.Name = v
			out = append(out, bucket)
		case *value.Map:
			bucket := defaults
			for _, e := range v.Entries() {
				key := value.KeyString(e.Key)
				switch key {
				case "name":
					bucket.Name = value.KeyString(e.Val)
				case "region":
					bucket.Region = value.KeyString(e.Val)
				case "prefix":
					bucket.Prefix = value.KeyString(e.Val)
				case "env":
					bucket.Env = value.KeyString(e.Val)
				case "endpoint":
					bucket.Endpoint = value.KeyString(e.Val)
				case "path_style":
					bucket.PathStyle = value.Truthy(e.Val)
				default:
					// A misspelt per-bucket key is a setting that does
					// nothing, which this project's configuration
					// handling exists to prevent.
					cli.Fatalf("`s3_buckets`: %q is not a per-bucket setting; "+
						"use name, region, prefix, env, endpoint, or path_style", key)
				}
			}
			out = append(out, bucket)
		default:
			cli.Fatalf("`s3_buckets`: a bucket is a name or a mapping, not %s", value.TypeName(item))
		}
	}
	return out
}
