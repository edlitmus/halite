package bridge

import (
	"github.com/edlitmus/halite/ext"

	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Sandbox is what an extension process is confined by.
//
// SPEC 24.3 gives a table of controls per platform, and the honest
// summary of what this build enforces is in `Describe`: an operator can
// read what is actually applied on the machine in front of them rather
// than what the specification hopes for. A control that is not
// available says so; it is never silently skipped.
type Sandbox struct {
	// User and Group are the account to drop to. Empty runs as the
	// host's own identity, which is what an extension declaring `root`
	// gets and what every other extension must not.
	User  string
	Group string

	// Network permits outbound connections. Denied by default, and
	// granted only to an extension that declared it.
	Network bool

	// MemoryBytes is RLIMIT_AS, which SPEC 24.3 names. Zero leaves it
	// alone, and zero is the default — see DefaultSandbox.
	MemoryBytes uint64
	// CPUSeconds, OpenFiles, and Processes are the other limits of
	// SPEC 24.3. Zero leaves each alone.
	CPUSeconds uint64
	OpenFiles  uint64
	Processes  uint64
}

// DefaultSandbox is what an extension gets when it declares nothing.
//
// Bounded rather than generous: an extension is somebody else's code
// running on a managed node, and the limits are what stop a bug in it
// from being an outage on the machine. An extension that needs more
// says so, and the declaration is visible.
//
// MemoryBytes is deliberately unset. RLIMIT_AS bounds *virtual* address
// space, and a runtime with a garbage collector reserves far more of
// that than it ever commits: a Go extension under a 512 MiB RLIMIT_AS —
// which reads as generous — dies with "out of memory" after allocating
// about 160 MiB. That was measured on this build's own test extension,
// and a default that kills extensions intermittently is worse than no
// default. An operator who wants the limit sets it, with a number that
// suits the language the extension is written in.
//
// `Processes` is kept, and **applies only to an extension with an
// account of its own**. RLIMIT_NPROC is per real UID: for an extension
// sharing the agent's identity the number bounds the whole account
// rather than the extension, and what that produced was the same
// intermittent death the paragraph above describes -- for weeks, in
// this project's own end-to-end test. `limitEnvironment` carries the
// measurement. DIVERGENCE 5.125.
func DefaultSandbox() *Sandbox {
	return &Sandbox{
		CPUSeconds: 60,
		OpenFiles:  256,
		Processes:  32,
	}
}

// From builds a sandbox for what an extension declared.
//
// Nothing is granted that was not declared. A declaration this build
// does not understand is refused rather than ignored: an extension
// asking for something the host cannot enforce must not run as though
// it had been granted.
func From(declares []string, runAs, runAsGroup string) (*Sandbox, error) {
	s := DefaultSandbox()
	s.User, s.Group = runAs, runAsGroup
	for _, declared := range declares {
		switch strings.TrimSpace(declared) {
		case "":
		case "network":
			s.Network = true
		case "root":
			// The host does not drop identity for it. Whether the host
			// is itself root is the operator's business; what matters
			// here is that the extension said so and it is visible.
			s.User, s.Group = "", ""
		default:
			return nil, fmt.Errorf("%q is not something an extension can declare; "+
				"this build understands `root` and `network`", declared)
		}
	}
	return s, nil
}

// Describe says what this sandbox actually enforces on this platform.
//
// SPEC 24.3's table is aspirational across five operating systems, and
// an operator needs to know which rows are real on the machine in front
// of them. `sys.list_extensions` shows this.
func (s *Sandbox) Describe() []string {
	if s == nil {
		return []string{"none: this extension runs with the host's own identity and limits"}
	}
	out := []string{"process boundary"}
	if s.User != "" {
		out = append(out, "runs as "+s.User)
	} else {
		out = append(out, "runs as the host's identity")
	}
	if s.Network {
		out = append(out, "network permitted")
	} else {
		out = append(out, "network "+networkEnforcement())
	}
	out = append(out, s.describeLimits()...)
	out = append(out, sandboxPlatformNotes()...)
	return out
}

func (s *Sandbox) describeLimits() []string {
	sup := ext.Limits()
	if !sup.Memory && !sup.CPU && !sup.OpenFiles && !sup.Processes {
		return []string{"resource limits: not enforced on this platform"}
	}
	var out []string
	if sup.Memory {
		if s.MemoryBytes > 0 {
			out = append(out, fmt.Sprintf("%s %d MiB", sup.MemoryLabel, s.MemoryBytes>>20))
		} else {
			out = append(out, sup.MemoryUnbounded)
		}
	}
	if sup.CPU && s.CPUSeconds > 0 {
		out = append(out, fmt.Sprintf("cpu %ds", s.CPUSeconds))
	}
	if sup.OpenFiles && s.OpenFiles > 0 {
		out = append(out, fmt.Sprintf("open files %d", s.OpenFiles))
	}
	if sup.Processes && s.Processes > 0 {
		if s.User != "" {
			out = append(out, fmt.Sprintf("processes %d for the account %s, which anything else running as it shares", s.Processes, s.User))
		} else {
			// Said out loud for the same reason the unenforced limits
			// below are: an operator who reads "processes 32" and gets
			// no limit at all has been told something untrue.
			out = append(out, fmt.Sprintf("processes %d not applied: the limit is per account and this extension runs as the host's identity", s.Processes))
		}
	}
	// A limit that was asked for and cannot be enforced here is said so
	// out loud. Silently dropping it is how an operator comes to believe
	// an extension is bounded when it is not.
	for _, unenforced := range []struct {
		set  bool
		name string
	}{
		{!sup.Memory && s.MemoryBytes > 0, "memory"},
		{!sup.CPU && s.CPUSeconds > 0, "cpu"},
		{!sup.OpenFiles && s.OpenFiles > 0, "open files"},
		{!sup.Processes && s.Processes > 0, "processes"},
	} {
		if unenforced.set {
			out = append(out, unenforced.name+" limit set but not enforced on this platform")
		}
	}
	return out
}

// limitEnvironment carries the sandbox's decisions to a child that
// knows how to apply them to itself.
//
// Platform-neutral, and it used to be in the unix file. That left an
// extension on Windows never told the network was denied — the one part
// of the sandbox that does not need a kernel mechanism, and the part
// every extension built with this package honours.
func (s *Sandbox) limitEnvironment() []string {
	var out []string
	if s.MemoryBytes > 0 {
		out = append(out, "HALITE_EXT_RLIMIT_AS="+strconv.FormatUint(s.MemoryBytes, 10))
	}
	if s.CPUSeconds > 0 {
		out = append(out, "HALITE_EXT_RLIMIT_CPU="+strconv.FormatUint(s.CPUSeconds, 10))
	}
	if s.OpenFiles > 0 {
		out = append(out, "HALITE_EXT_RLIMIT_NOFILE="+strconv.FormatUint(s.OpenFiles, 10))
	}
	// **`RLIMIT_NPROC` is per real UID, not per process**, and that is
	// the whole of why it is conditional here. The kernel counts every
	// process -- on Linux, every *thread* -- that account already has
	// anywhere on the machine, so a limit of 32 handed to an extension
	// sharing the agent's identity is not "this extension may have 32
	// threads". It is "this account may have 32 threads in total", and
	// the extension is the one that finds out, by dying at whatever
	// point the rest of the machine happens to have reached.
	//
	// That is not a hypothesis. `internal/extpillar`'s end-to-end test
	// flaked for weeks with "the extension exited without answering",
	// and once the stderr tail kept both ends of a Go fatal (5.104) the
	// next occurrence said what it was:
	//
	//	runtime: failed to create new OS thread (have 5 already; errno=11)
	//	runtime: may need to increase max user processes (ulimit -u)
	//	fatal error: newosproc
	//
	// Five threads, against a limit of 32, on a CI runner whose account
	// was already near its own cap. Intermittent exactly as a shared
	// budget would be.
	//
	// So it is set only where it means what it says: when the extension
	// has an account of its own. `DefaultSandbox`'s comment already
	// made this argument about `RLIMIT_AS` -- "a default that kills
	// extensions intermittently is worse than no default" -- and this
	// is the same sentence about a different limit. DIVERGENCE 5.125.
	if s.Processes > 0 && s.User != "" {
		out = append(out, "HALITE_EXT_RLIMIT_NPROC="+strconv.FormatUint(s.Processes, 10))
	}
	if !s.Network {
		out = append(out, "HALITE_EXT_NETWORK=deny")
	}
	return out
}

// apply configures the command. A nil sandbox applies nothing, which is
// what a development build does.
//
// It returns a hook to run once the child exists and one to run when it
// is gone, because a job object can only be assigned to a process that
// has started. On unix both are empty.
func (s *Sandbox) apply(cmd *exec.Cmd) (afterStart func() error, cleanup func(), err error) {
	nothing := func() {}
	if s == nil {
		return func() error { return nil }, nothing, nil
	}
	cmd.Env = append(cmd.Env, s.limitEnvironment()...)
	return s.applyPlatform(cmd)
}
