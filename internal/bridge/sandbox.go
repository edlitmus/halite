package bridge

import (
	"fmt"
	"os/exec"
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
	var out []string
	if !limitsSupported() {
		return []string{"resource limits: not enforced on this platform"}
	}
	if s.MemoryBytes > 0 {
		out = append(out, fmt.Sprintf("address space %d MiB", s.MemoryBytes>>20))
	} else {
		out = append(out, "address space unbounded (RLIMIT_AS kills a garbage-collected runtime)")
	}
	if s.CPUSeconds > 0 {
		out = append(out, fmt.Sprintf("cpu %ds", s.CPUSeconds))
	}
	if s.OpenFiles > 0 {
		out = append(out, fmt.Sprintf("open files %d", s.OpenFiles))
	}
	if s.Processes > 0 {
		out = append(out, fmt.Sprintf("processes %d", s.Processes))
	}
	return out
}

// apply configures the command. A nil sandbox applies nothing, which is
// what a development build does.
func (s *Sandbox) apply(cmd *exec.Cmd) error {
	if s == nil {
		return nil
	}
	return s.applyPlatform(cmd)
}
