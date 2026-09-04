package bridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A process limit is enforced by the host, not by the extension.
//
// This is the part of SPEC 24.3 that the Windows side does better than
// the unix one. setrlimit cannot be applied to a child by its parent, so
// on unix the limits are handed to the extension in its environment and
// applied by the extension to itself — which does nothing for an
// extension that was not built with this package. A job object is set by
// the parent and enforced by the kernel, so it holds whatever the
// extension is.
//
// The extension here deliberately does not cooperate: it starts children
// until one is refused, and nothing in it reads the limit.
func TestAProcessLimitIsEnforcedByTheKernel(t *testing.T) {
	sandbox := DefaultSandbox()
	// The extension itself is one of them, so this permits three
	// children before the fourth is refused.
	sandbox.Processes = 4

	proc := startEcho(t, func(o *Options) {
		o.Sandbox = sandbox
		o.Env = []string{"SystemRoot=" + systemRootForTest()}
	})
	value, err := proc.Call(context.Background(), "spawn",
		nil, map[string]any{"count": 12}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Started int    `json:"started"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(value, &got); err != nil {
		t.Fatal(err)
	}
	if got.Started >= 12 {
		t.Errorf("the extension started %d children under a limit of %d; "+
			"the job object is not holding", got.Started, sandbox.Processes)
	}
	if got.Started == 0 {
		t.Errorf("the extension started none at all (%s), so this proves nothing about the limit", got.Error)
	}
}

// And the description says which mechanism is doing it, because an
// operator reading sys.list_extensions has to know whether a row is
// enforced by the kernel or honoured by the extension.
func TestTheDescriptionNamesTheJobObject(t *testing.T) {
	described := strings.Join(DefaultSandbox().Describe(), "; ")
	if !strings.Contains(described, "job object") {
		t.Errorf("the description does not name the mechanism: %s", described)
	}
	if !strings.Contains(described, "restricted token") {
		t.Errorf("the description does not say the privilege drop is absent: %s", described)
	}
	// RLIMIT_AS is not what bounds memory here, and naming it would be
	// telling an operator about a mechanism this platform does not have.
	if strings.Contains(described, "RLIMIT_AS") {
		t.Errorf("the description names a unix limit: %s", described)
	}

	// A limit with no counterpart here is said to be unenforced rather
	// than quietly dropped.
	s := DefaultSandbox()
	s.OpenFiles = 64
	if !strings.Contains(strings.Join(s.Describe(), "; "), "open files limit set but not enforced") {
		t.Errorf("an unenforceable limit is not reported: %v", s.Describe())
	}
}

// An extension asking to run as another account is refused rather than
// run as the agent's own, which would be more authority than the
// manifest asked for.
func TestRunningAsAnotherAccountIsRefusedRatherThanIgnored(t *testing.T) {
	sandbox := DefaultSandbox()
	sandbox.User = "halite-ext"
	_, err := startEchoErr(t, func(o *Options) { o.Sandbox = sandbox })
	if err == nil {
		t.Fatal("an extension asking for another account was started as the host's")
	}
	if !strings.Contains(err.Error(), "halite-ext") {
		t.Errorf("the refusal does not name the account: %v", err)
	}
	if !strings.Contains(err.Error(), "restricted token") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}
