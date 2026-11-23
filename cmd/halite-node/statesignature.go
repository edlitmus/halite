package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/jobsign"
	"github.com/edlitmus/halite/internal/treesign"
	"github.com/edlitmus/halite/internal/value"
)

// DefaultStateManifestMaxAge is how old a signed manifest may be before a
// node that requires one refuses it, unless `state_manifest_max_age` says
// otherwise.
//
// A freeze bound by default, because the maintainer asked for one (issue
// 764, proposal 10.3). Without it a compromised hub can keep serving the
// newest manifest a node has accepted and withhold its successor for ever,
// and the node cannot tell that from a quiet month.
//
// Thirty days, the figure the proposal suggested. Long enough that a CI
// job re-signing nightly, or weekly, has weeks of slack before a missed
// run stops anything, and that an ordinary hub outage -- a weekend, a
// hardware replacement -- never reaches it. Short enough that a frozen
// node is a month behind at most, which is also SPEC's job and event
// retention (720h), so the hub still holds the record of what changed. The
// cost is stated wherever the setting is: a hub outage longer than this
// stops state runs on every node that requires signatures, because the
// node applies only a manifest it can still accept, and the offline copy
// ages like any other.
const DefaultStateManifestMaxAge = 30 * 24 * time.Hour

// buildTreeGuard reads issue 764's settings. A requirement or a bound this
// build cannot read stops the node, for `require_job_signature`'s reason; a
// signer key that will not parse stops it too, for `job_signer_keys`'s. An
// empty key list does not: see fileserver.TreeGuard.accept.
func (n *node) buildTreeGuard() {
	mode, err := fileserver.ParseSignatureMode(n.cfg.Get("require_state_signature"))
	if err != nil {
		cli.Fatalf("%v", err)
	}
	if mode == fileserver.SignatureOff {
		return
	}
	var keys []jobsign.SignerKey
	for _, line := range n.cfg.StringSlice("state_signer_keys") {
		key, err := jobsign.ParseSignerKey(line)
		if err != nil {
			cli.Fatalf("state_signer_keys: %v", err)
		}
		keys = append(keys, key)
	}
	maxAge := DefaultStateManifestMaxAge
	if raw, ok := n.cfg.Get("state_manifest_max_age"); ok && raw != nil {
		maxAge, err = value.ParseDuration(raw)
		if err != nil || maxAge < 0 {
			cli.Fatalf("state_manifest_max_age: %v is not a duration; 0 turns the bound off", raw)
		}
	}
	stateDir := n.cfg.String("state_dir", config.DefaultStateDir)
	cacheDir := n.cfg.String("cache_dir", config.DefaultCacheDir)
	n.treeGuard = &fileserver.TreeGuard{
		Mode:    mode,
		Keys:    keys,
		MaxAge:  maxAge,
		Records: treesign.Store{Dir: filepath.Join(stateDir, "tree-revisions")},
		Objects: filepath.Join(cacheDir, "objects"),
		Warn:    n.log.With("component", "treesign").Warn,
		Info:    n.log.With("component", "treesign").Info,
		Refused: n.metrics.countTreeRefusal,
		Accepted: func(env string, issued time.Time) {
			n.metrics.treeRevision(env, issued)
		},
	}
	n.treeSignatureLocal = n.cfg.Bool("state_signature_local", true)
	n.metrics.declareTreeRefusals()
}

// localTree is this node's own roots for an environment, verified where
// `state_signature_local` says so.
func (n *node) localTree(env string) stateTree {
	f := fileserver.NewFetcher(fileserver.NewRoots(n.fileRootsFor(env)))
	if n.treeGuard.Active() && n.treeSignatureLocal {
		f.Guard = n.treeGuard
	}
	return f
}

// pinTreeForRun gives one job its own revision pins, so that every read
// of an environment in the job is answered from the one manifest accepted
// at its first read.
func pinTreeForRun(files stateTree) stateTree {
	switch t := files.(type) {
	case *fileserver.Remote:
		return t.ForRun()
	case *fileserver.Fetcher:
		return t.ForRun()
	}
	return files
}

// treeSignatureSummary is what the agent logs at startup.
func (n *node) treeSignatureSummary() string {
	if !n.treeGuard.Active() {
		return "no signed state content is required"
	}
	local := "the hub's tree only"
	if n.treeSignatureLocal {
		local = "the hub's tree and this node's own roots"
	}
	bound := "no age bound"
	if n.treeGuard.MaxAge > 0 {
		bound = "manifests at most " + n.treeGuard.MaxAge.String() + " old"
	}
	return fmt.Sprintf("signed state content is required (require_state_signature: %s) for %s, "+
		"from %d trusted key(s), %s; pillar is not covered", n.treeGuard.Mode, local,
		len(n.treeGuard.Keys), bound)
}
