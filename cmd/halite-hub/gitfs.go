package main

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/gitfs"
	"github.com/edlitmus/halite/internal/value"
)

// gitBackend builds the git file server backend of SPEC 13.3, or nil
// when `fileserver_backend` does not name it.
//
// Checked at startup: a remote with an unusable URL, an unknown ref
// type, or signature verification with no keyring stops the hub here,
// where the operator who wrote it is looking.
func (h *hubContext) gitBackend() *gitfs.Backend {
	if !namesBackend(h.cfg, "git") && !namesBackend(h.cfg, "gitfs") {
		return nil
	}
	remotes := gitRemotes(h)
	if len(remotes) == 0 {
		cli.Fatalf("fileserver_backend names git and gitfs_remotes is empty")
	}
	cache := h.cfg.String("gitfs_cache_dir", "")
	if cache == "" {
		cache = filepath.Join(h.cfg.String("cache_dir", config.DefaultCacheDir), "gitfs")
	}
	backend, err := gitfs.New(gitfs.Options{
		Remotes:          remotes,
		CacheDir:         cache,
		Base:             h.cfg.String("gitfs_base", "main"),
		RefTypes:         splitList(h.cfg.String("gitfs_ref_types", "branches")),
		AllowEnvs:        h.cfg.StringSlice("gitfs_env_allowlist"),
		DenyEnvs:         h.cfg.StringSlice("gitfs_env_denylist"),
		VerifySignatures: h.cfg.Bool("gitfs_verify_signatures", false),
		Keyring:          h.cfg.String("gitfs_keyring", ""),
		Timeout:          h.cfg.Duration("gitfs_timeout", 5*time.Minute),
		Log: func(level, msg string, kv ...any) {
			if level == "warn" || level == "error" {
				h.log.Warn(msg, kv...)
				return
			}
			h.log.Info(msg, kv...)
		},
	})
	if err != nil {
		cli.Fatalf("gitfs: %v", err)
	}
	return backend
}

// namesBackend reports whether `fileserver_backend` lists one.
func namesBackend(cfg *config.Config, name string) bool {
	for _, b := range cfg.StringSlice("fileserver_backend") {
		if strings.TrimSpace(b) == name {
			return true
		}
	}
	return false
}

// gitRemotes reads `gitfs_remotes`, which is a list of URLs or of
// mappings with per-remote overrides.
//
// Both shapes, because SPEC 13.3 asks for per-remote configuration and
// most estates have one repository and want to write one line.
func gitRemotes(h *hubContext) []gitfs.Remote {
	raw, ok := h.cfg.Get("gitfs_remotes")
	if !ok || raw == nil {
		return nil
	}
	list, isList := raw.([]any)
	if !isList {
		cli.Fatalf("`gitfs_remotes` is a list of repositories, not %s", value.TypeName(raw))
	}
	defaultRoot := h.cfg.String("gitfs_root", "")

	var out []gitfs.Remote
	for _, item := range list {
		switch v := item.(type) {
		case string:
			out = append(out, gitfs.Remote{URL: v, Root: defaultRoot})
		case *value.Map:
			remote := gitfs.Remote{Root: defaultRoot}
			for _, e := range v.Entries() {
				key := value.KeyString(e.Key)
				switch key {
				case "url":
					remote.URL = value.KeyString(e.Val)
				case "name":
					remote.Name = value.KeyString(e.Val)
				case "root":
					remote.Root = value.KeyString(e.Val)
				case "base":
					remote.Base = value.KeyString(e.Val)
				case "ref_types":
					remote.RefTypes = stringsFrom(e.Val)
				case "insecure":
					remote.Insecure = value.Truthy(e.Val)
				default:
					// A misspelt per-remote key is a setting that does
					// nothing, which is what this project's whole
					// configuration handling exists to prevent.
					cli.Fatalf("`gitfs_remotes`: %q is not a per-remote setting; "+
						"use url, name, root, base, ref_types, or insecure", key)
				}
			}
			out = append(out, remote)
		default:
			cli.Fatalf("`gitfs_remotes`: a repository is a URL or a mapping, not %s",
				value.TypeName(item))
		}
	}
	return out
}

func stringsFrom(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, item := range t {
			if s := value.KeyString(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// updateGitfs fetches and republishes the served roots.
//
// The file server's whole search path is rebuilt rather than added to,
// because a branch that has gone away must stop being served. The
// configured `file_roots` go first, so a local directory still shadows
// a repository for the same path — which is the order
// `fileserver_backend` lists them in and the order an operator expects.
func updateGitfs(ctx context.Context, backend *gitfs.Backend, files *fileserver.Roots, local map[string][]string) error {
	if backend == nil || files == nil {
		return nil
	}
	err := backend.Update(ctx)

	combined := make(map[string][]string, len(local))
	for env, dirs := range local {
		combined[env] = append([]string(nil), dirs...)
	}
	for env, dirs := range backend.Roots() {
		combined[env] = append(combined[env], dirs...)
	}
	files.SetDirs(combined)
	return err
}
