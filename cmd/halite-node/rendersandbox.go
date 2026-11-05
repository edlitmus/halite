package main

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/edlitmus/halite/internal/cli"
	hlog "github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/rendersandbox"
)

// The two halves of SPEC section 25.4's render sandbox that belong to
// the node rather than to the package.
//
// `renderEngine` is what a run compiles with, and `runRenderSandbox` is
// what the child process runs. They are in one file because they are one
// arrangement: the parent re-executes this binary with the subcommand
// below, which is why the child is guaranteed to be the same build and
// why the protocol between them is versioned rather than negotiated.

// renderSandboxCommand is the subcommand the child is started with. Not
// in the usage text, for the reason `oneshot` is not: a person has no
// reason to run it, and it speaks a framed protocol on stdin.
const renderSandboxCommand = "render-sandbox"

// runRenderSandbox is the child. It loads no configuration, opens no
// socket, reads no key, and answers render requests until its parent
// closes the pipe.
func runRenderSandbox(_ *cli.Args) int {
	if err := rendersandbox.Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "halite-node render-sandbox: %v\n", err)
		return 1
	}
	return 0
}

// sandboxHolder is the render sandbox one node and every copy of it
// share. See the `sandbox` field on node.
type sandboxHolder struct {
	mu sync.Mutex
	sb *rendersandbox.Sandbox
}

// renderEngine returns the engine this node's compilations use.
//
// Nil when `render_sandbox` is off, which is the default and which means
// rendering happens in this process exactly as it did before this
// existed. The sandbox is built once per node and reused, because a
// highstate renders fifty files and a process per file is the cost that
// would make an operator turn it off.
//
// Once per node means once for the agent and all its jobs. Each job runs
// on a copy of the node, and when the sandbox was a field of the node, a
// copy that found it nil built its own: its child was never closed, and
// lived until the garbage collector finalised its pipes. Whether the agent
// had built one before the first job decided whether the jobs shared it or
// each leaked one. The holder is a pointer the copies share, so there is
// one child and runConnect closes it. DIVERGENCE 5.592.
func (n *node) renderEngine() render.Engine {
	if !n.cfg.Bool("render_sandbox", false) {
		return nil
	}
	h := n.sandbox
	if h == nil {
		// A node built without setup, which only a test does. It gets a
		// holder of its own, which its copies then share.
		h = &sandboxHolder{}
		n.sandbox = h
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sb != nil {
		return h.sb
	}

	h.sb = rendersandbox.New(rendersandbox.Config{
		Args:  []string{renderSandboxCommand},
		User:  n.cfg.String("render_sandbox_user", ""),
		Group: n.cfg.String("render_sandbox_group", ""),
		// The child's diagnostics are the node's: a render that failed
		// inside the sandbox must not be silent because the message went
		// to a pipe nobody read.
		Stderr: &childLog{log: n.log},
	})

	// Said once, at the point the decision takes effect, and in the
	// terms of what is actually applied on this machine. An operator who
	// turns this on is entitled to know that the identity was not
	// dropped because the node is not root, rather than to assume it
	// was.
	for _, line := range h.sb.Describe() {
		n.log.Info("render sandbox: "+line, "component", "render")
	}
	// A control that was asked for and is not applied is a warning, not
	// a line in a description an operator has to go looking for.
	for _, line := range h.sb.Unenforced() {
		n.log.Warn("render sandbox: "+line, "component", "render")
	}
	return h.sb
}

// childLog turns the child's standard error into the node's log, a line
// at a time. Without it a child that failed to start says why into a
// pipe nobody reads, which is the shape of silence this project keeps
// finding in its own retry paths.
type childLog struct{ log *hlog.Logger }

func (w *childLog) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			w.log.Warn("render sandbox child: "+line, "component", "render")
		}
	}
	return len(p), nil
}

// closeRenderSandbox ends the child, if one was started. It ends the one
// every copy of this node shares, so only the owner of the node calls it:
// runConnect when the agent stops, never a job.
func (n *node) closeRenderSandbox() {
	h := n.sandbox
	if h == nil {
		return
	}
	h.mu.Lock()
	sb := h.sb
	h.sb = nil
	h.mu.Unlock()
	if sb == nil {
		return
	}
	if err := sb.Close(); err != nil {
		n.log.Debug(fmt.Sprintf("render sandbox: %v", err), "component", "render")
	}
}
