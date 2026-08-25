package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/version"
)

// The frames that separate this program's answer from anything else on
// the target's stdout.
//
// SPEC 21.1 asks for a framed delimiter, and the reason is concrete: a
// login banner, a motd, a sudo lecture, and a shell profile that echoes
// something all arrive on the same stream. Without a frame the caller
// is parsing "welcome to prod" as JSON.
const (
	OneshotBegin = "--- halite-oneshot-begin ---"
	OneshotEnd   = "--- halite-oneshot-end ---"
)

// OneshotRequest is the job an agentless run sends on stdin.
type OneshotRequest struct {
	// Protocol lets the pushed binary refuse a caller it does not
	// understand, which matters because the binary is cached on the
	// target and may be older than the hub that finds it.
	Protocol int            `json:"protocol"`
	JID      string         `json:"jid"`
	NodeID   string         `json:"node_id"`
	Fun      string         `json:"fun"`
	Arg      []string       `json:"arg,omitempty"`
	Kwarg    map[string]any `json:"kwarg,omitempty"`
	Env      string         `json:"env,omitempty"`
	Test     bool           `json:"test,omitempty"`
	// Pillar is compiled on the hub and sent inline, so the target
	// needs no pillar tree and no access to one.
	Pillar json.RawMessage `json:"pillar,omitempty"`
	// Files are the state tree entries this job needs, sent inline for
	// a small payload. SPEC 21.1.
	Files map[string]string `json:"files,omitempty"`
	// Grains the roster attached, merged under what the target
	// reports about itself.
	Grains json.RawMessage `json:"grains,omitempty"`
	// Timeout bounds the run on the target.
	Timeout float64 `json:"timeout_seconds,omitempty"`
}

// OneshotProtocol is the version this build speaks.
const OneshotProtocol = 1

// runOneshot is `halite-node oneshot`, the mode an agentless run
// executes in on the target.
//
// It is not in the usage text. A person has no reason to run it: it
// reads a job on stdin and writes a framed return on stdout, and it
// exists because `halite-hub ssh` pushes this binary and needs
// something to invoke.
func runOneshot(args *cli.Args) int {
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<20))
	if err != nil {
		return oneshotFailure("", fmt.Errorf("reading the job: %w", err))
	}
	var req OneshotRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return oneshotFailure("", fmt.Errorf("the job is not readable: %w", err))
	}
	if req.Protocol != 0 && req.Protocol != OneshotProtocol {
		// The cached binary is older or newer than the caller. Said
		// plainly, because the fix is `--clean` and nobody would guess
		// that from a decoding error.
		return oneshotFailure(req.JID, fmt.Errorf(
			"this binary speaks oneshot protocol %d and the caller speaks %d; "+
				"run with --clean to replace the cached binary",
			OneshotProtocol, req.Protocol))
	}
	if req.Fun == "" {
		return oneshotFailure(req.JID, fmt.Errorf("the job names no function"))
	}

	n := setup(args)
	if req.NodeID != "" {
		n.nodeID = req.NodeID
	}
	if req.Env != "" {
		n.env, n.pillarEnv = req.Env, req.Env
	}
	n.test = req.Test
	if err := applyOneshotContent(n, req); err != nil {
		return oneshotFailure(req.JID, err)
	}

	jid := req.JID
	if jid == "" {
		jid = string(newJobID())
	}
	ret := n.executeJob(&job.Job{
		JID:   job.ID(jid),
		Fun:   req.Fun,
		Arg:   req.Arg,
		Kwarg: req.Kwarg,
		Env:   req.Env,
	})
	writeOneshot(ret)
	if !ret.Success {
		return 1
	}
	return 0
}

// applyOneshotContent installs the pillar, grains, and files the hub
// sent, so the target compiles against them and needs no tree of its
// own.
func applyOneshotContent(n *node, req OneshotRequest) error {
	// The pillar is whatever the hub sent, and *only* that — an empty
	// one when it sent none.
	//
	// Never the target's own tree. SPEC 21.1 compiles pillar on the hub
	// and sends it inline, and a target that fell back to a local
	// pillar tree would compile against whatever happens to be on the
	// machine: on a machine that used to run Salt, that is the old
	// estate's pillar, half of it encrypted to keys this process does
	// not have. Found exactly that way.
	pillar := value.NewMap(0)
	if len(req.Pillar) > 0 {
		decoded, err := value.DecodeJSON(req.Pillar)
		if err != nil {
			return fmt.Errorf("the pillar is not readable: %w", err)
		}
		sent, ok := decoded.(*value.Map)
		if !ok {
			return fmt.Errorf("the pillar is not a mapping")
		}
		pillar = sent
	}
	n.hubPillar = func(string) (*value.Map, error) { return pillar, nil }
	if len(req.Grains) > 0 {
		decoded, err := value.DecodeJSON(req.Grains)
		if err != nil {
			return fmt.Errorf("the grains are not readable: %w", err)
		}
		if extra, ok := decoded.(*value.Map); ok && n.grains != nil {
			// Under what the target reports about itself: a roster
			// saying `os: Debian` about a machine that is FreeBSD
			// should not make it apply the Debian branch.
			merged := value.Merge(extra, n.grains, value.MergeOpts{Strategy: value.Recurse})
			if m, ok := merged.(*value.Map); ok {
				n.grains = m
			}
		}
	}
	// The tree is what the hub sent, and only that. An agentless target
	// has no state tree of its own, and one that fell back to a local
	// directory would apply whatever a previous configuration system
	// left there.
	if req.Files != nil || needsInlineTree(req.Fun) {
		dir, err := os.MkdirTemp("", "halite-oneshot-*")
		if err != nil {
			return err
		}
		for rel, body := range req.Files {
			if err := writeInlineFile(dir, rel, body); err != nil {
				return err
			}
		}
		// The tree the hub sent, and only it. A target in agentless
		// mode has no state tree of its own and must not fall back to
		// one that happens to be on the machine.
		n.files = fileserver.NewFetcher(fileserver.NewRoots(map[string][]string{
			n.env:  {dir},
			"base": {dir},
		}))
	}
	return nil
}

// writeOneshot frames the return.
func writeOneshot(ret *job.Return) {
	encoded, err := json.Marshal(ret)
	if err != nil {
		encoded = []byte(fmt.Sprintf(`{"success":false,"return":%q}`, err.Error()))
	}
	fmt.Println()
	fmt.Println(OneshotBegin)
	fmt.Println(string(encoded))
	fmt.Println(OneshotEnd)
}

// oneshotFailure answers with a framed failure, so the caller gets a
// diagnosis rather than a target that said nothing.
func oneshotFailure(jid string, err error) int {
	writeOneshot(&job.Return{
		JID: job.ID(jid), Success: false, RetCode: 1,
		Return:      json.RawMessage(fmt.Sprintf("%q", err.Error())),
		NodeVersion: version.String(),
		Schema:      job.ReturnSchema,
		StartTime:   time.Now().UTC().Format(time.RFC3339Nano),
	})
	return 1
}

// writeInlineFile writes one file the hub sent, refusing a path that
// would leave the temporary tree.
//
// The paths come from the hub, which is trusted — and a check that
// costs nothing is a check worth having on anything that writes to
// disk from a message.
func writeInlineFile(dir, rel, body string) error {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return fmt.Errorf("%q is not a path inside the tree", rel)
	}
	for _, segment := range strings.Split(filepath.ToSlash(clean), "/") {
		if segment == ".." {
			return fmt.Errorf("%q leaves the tree", rel)
		}
	}
	target := filepath.Join(dir, clean)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	return os.WriteFile(target, []byte(body), 0o600)
}

// needsInlineTree reports whether a function compiles state, and so
// must run against the tree the hub sent rather than anything local.
func needsInlineTree(fun string) bool {
	return strings.HasPrefix(fun, "state.")
}
