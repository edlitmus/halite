package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/fileperm"
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
	// Secrets are the values the hub decrypted compiling Pillar, as in
	// transport.PillarResponse. Absent from a hub older than the field,
	// and then every pillar value is treated as secret. DIVERGENCE 5.251.
	Secrets *[]string `json:"secrets,omitempty"`
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
	tree, err := applyOneshotContent(n, req)
	if err != nil {
		return oneshotFailure(req.JID, err)
	}
	// The inline tree is removed when this run is over, on every path
	// out of it. It used not to be: nothing removed it at all, so every
	// agentless `state.*` run left a `halite-oneshot-*` directory of the
	// hub's state and template files in the target's temporary directory
	// -- until reboot on a host that clears /tmp, and indefinitely on one
	// that does not. DIVERGENCE 5.290.
	//
	// The defer is the backstop for a panic. The explicit remove below,
	// before the frame is written, is the one that normally runs, and the
	// order is the point: a hub that has gone away leaves stdout a broken
	// pipe, a write to it kills this process with SIGPIPE, and a process
	// killed by a signal runs no defers.
	defer tree.remove(n)
	stopWatching := tree.removeOnSignal(n, oneshotSignals(), os.Exit)
	defer stopWatching()

	jid := req.JID
	if jid == "" {
		jid = string(newJobID())
	}
	// SPEC 25.6 does not reach agentless mode, and saying so is the
	// point.
	//
	// The job arrives on stdin from a caller that already has a shell on
	// this machine: `halite-hub ssh` pushed this binary here and is
	// running it. A hub that has been taken over and can do that can run
	// anything it likes without going through halite at all, so checking
	// a signature here would protect nothing -- and refusing the job
	// would break agentless mode on every machine configured for
	// signing, for no gain.
	//
	// What it must not do is be silent. A node whose configuration asks
	// for a control that does not apply here should say which and why,
	// once, rather than leaving an operator to assume it held.
	if n.signatureRequired.any() {
		if required, why := n.signatureRequired.requires(n, req.Fun); required {
			n.log.Warn("this node requires a signature for this function and agentless mode does not check one",
				"component", "jobsign", "fun", req.Fun, "class", why,
				"reason", "the caller already has a shell on this machine, so a signature here would establish nothing")
		}
	}
	ret := n.executeJob(&job.Job{
		JID:   job.ID(jid),
		Fun:   req.Fun,
		Arg:   req.Arg,
		Kwarg: req.Kwarg,
		Env:   req.Env,
	})
	// Nothing reads the tree after executeJob returns. It runs the job
	// synchronously, rendering and file sources included; cmd.script
	// copies its source to a file of its own before running it; and the
	// one thing that outlives a job, a `cmd.run` with `bg: true`, is
	// handed a command line, not a path into this tree.
	tree.remove(n)
	oneshotFrame(ret)
	if !ret.Success {
		return 1
	}
	return 0
}

// oneshotTempRoot is where the inline tree is made; empty is the
// platform's temporary directory. A variable so a test can watch the
// directory a run leaves behind without sharing os.TempDir with every
// other test in the package.
var oneshotTempRoot = ""

// inlineTree is the directory holding the files the hub sent for one
// agentless run. The zero value, and a nil one, hold no directory, and
// removing them does nothing -- a run that compiles no state has none.
type inlineTree struct {
	dir  string
	once sync.Once
}

// remove deletes the tree, once, however many paths reach it.
//
// A failure is reported rather than returned: the run's answer is
// already decided, and a tree that could not be removed is a thing the
// operator should hear about without it changing that answer.
func (t *inlineTree) remove(n *node) {
	if t == nil || t.dir == "" {
		return
	}
	t.once.Do(func() {
		if err := os.RemoveAll(t.dir); err != nil && n != nil && n.log != nil {
			n.log.Warn("the agentless run's inline state tree could not be removed",
				"path", t.dir, "error", err.Error())
		}
	})
}

// oneshotSignals are the signals that end an agentless run and that the
// tree is removed for.
//
// SIGKILL is not among them because it cannot be: a run killed that way,
// or by the machine losing power, leaves its tree behind. So does one
// whose ssh session drops while it is still running -- sshd sends no
// signal to a command it started without a terminal, so the job runs to
// the end, and the tree is then removed before the write that would
// fail.
func oneshotSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}

// removeOnSignal removes the tree and exits when one of sigs arrives,
// and returns the function that stops watching.
//
// Exiting is what each of those signals did before anything caught it,
// so catching one changes what is left on disk and nothing else. The
// status is the shell's 128+n, which is what a caller saw from a process
// the signal killed. The job is abandoned mid-step either way; a state
// that reads the tree in the instant between the removal and the exit
// gets a missing file, and its failure is never reported, because
// nothing is written after this.
func (t *inlineTree) removeOnSignal(n *node, sigs []os.Signal, exit func(int)) func() {
	if t == nil || t.dir == "" {
		return func() {}
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, sigs...)
	return t.watch(n, ch, exit, func() { signal.Stop(ch) })
}

// watch is removeOnSignal's loop with the channel given, so a test can
// deliver a signal without sending one to the test binary.
func (t *inlineTree) watch(n *node, ch <-chan os.Signal, exit func(int), stop func()) func() {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case sig := <-ch:
			t.remove(n)
			code := 1
			if s, ok := sig.(syscall.Signal); ok {
				code = 128 + int(s)
			}
			exit(code)
		case <-done:
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			stop()
			close(done)
			wg.Wait()
		})
	}
}

// applyOneshotContent installs the pillar, grains, and files the hub
// sent, so the target compiles against them and needs no tree of its
// own. It returns the inline tree it made, if it made one, for the
// caller to remove when the run is over; on an error it has removed it
// already.
func applyOneshotContent(n *node, req OneshotRequest) (*inlineTree, error) {
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
			return nil, fmt.Errorf("the pillar is not readable: %w", err)
		}
		sent, ok := decoded.(*value.Map)
		if !ok {
			return nil, fmt.Errorf("the pillar is not a mapping")
		}
		pillar = sent
		// Seeded before anything runs. This path installed the pillar
		// the hub pushed and told the redactor nothing, so a decrypted
		// value was printable in an agentless run's output. DIVERGENCE
		// 5.251.
		n.seedPillarSecrets(pillar, req.Secrets)
	}
	n.hubPillar = func(string, bool) (*value.Map, error) { return pillar, nil }
	if len(req.Grains) > 0 {
		decoded, err := value.DecodeJSON(req.Grains)
		if err != nil {
			return nil, fmt.Errorf("the grains are not readable: %w", err)
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
		dir, err := os.MkdirTemp(oneshotTempRoot, "halite-oneshot-*")
		if err != nil {
			return nil, err
		}
		tree := &inlineTree{dir: dir}
		for rel, body := range req.Files {
			if err := writeInlineFile(dir, rel, body); err != nil {
				tree.remove(n)
				return nil, err
			}
		}
		// The tree the hub sent, and only it. A target in agentless
		// mode has no state tree of its own and must not fall back to
		// one that happens to be on the machine.
		n.files = fileserver.NewFetcher(fileserver.NewRoots(map[string][]string{
			n.env:  {dir},
			"base": {dir},
		}))
		return tree, nil
	}
	return nil, nil
}

// oneshotFrame writes a finished run's return. A variable so a test can
// look at the disk at the moment the frame is written, which is the
// moment a broken pipe would end the process.
var oneshotFrame = writeOneshot

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
	if err := fileperm.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	return fileperm.WriteFile(target, []byte(body), 0o600)
}

// needsInlineTree reports whether a function compiles state, and so
// must run against the tree the hub sent rather than anything local.
func needsInlineTree(fun string) bool {
	return strings.HasPrefix(fun, "state.")
}
