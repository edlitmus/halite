package builtin

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// swapPlatforms are the platforms this module has been run against.
//
// OpenBSD and NetBSD are almost certainly fine — `swapctl` is their own
// name for the call, borrowed by FreeBSD's newer runtime — but nobody has
// run this against either, and a platform list is a claim about what was
// checked, not a guess about what probably works.
var swapPlatforms = []string{"linux", "freebsd"}

// registerSwap installs the `swap` module of SPEC 15.2's core execution
// modules: turning swap devices and files on and off.
//
// # Reading is not built here
//
// `mount.swaps` already answers "what swap is active right now" — it
// reads /proc/swaps where there is one and falls back to `swapctl -l` or
// `swapon -s` otherwise, keyed by device with its size and how much is
// used — and `activeSwaps` is the function behind it. A second reader in
// this module, parsing `swapinfo` or `swapctl -l` again under a different
// name, would be two parsers answering the same question with nothing to
// stop them drifting apart, which is the shape DIVERGENCE 5.31 is cited
// for elsewhere in this package. `swap.list` is a one-line call to
// `activeSwaps`, and `swap.on`/`swap.off` call it too, for their own "is
// this already the state I'm asked for" check. This is the `cron`/`at`
// instruction — reuse existing machinery deliberately rather than write a
// second copy of it — applied to a pair of modules that were never asked
// about it directly.
//
// A host with nothing configured for swap is not an error here, because
// it is not one in `activeSwaps`: an empty table is a real and common
// answer, and this module never invents a failure `mount.swaps` does not
// already report.
//
// # Enabling swap is a small, checkable command
//
// FreeBSD's `swapon(8)` is `swapon [-F fstab] -aLq | [-E] file ...` and
// Linux's is the same shape with one addition: `-p priority`, which
// biases the kernel's choice among several swap areas when more than one
// is active. That is one real difference between the platforms, not the
// three `hostname` turned out to have (plan.md §1.4), so `swapOnArgv` is
// a table keyed on GOOS with exactly that one branch — and a priority
// given on a platform with nothing to spend it on is refused by name
// rather than silently dropped. Silently dropping it would be the "eager
// work silences a node" shape: the caller would believe their priority
// took effect and nothing would ever tell them otherwise.
//
// `swapOffArgv` has no such branch. `swapoff <path>` is the whole of it
// on both platforms, and the table exists anyway so that fact is checked
// rather than assumed — the same reasoning `quotaSwitchArgv` gives for
// being one function rather than a platform switch hidden inside a
// bigger one.
//
// # No fstab, no persistence
//
// A line in /etc/fstab is what makes a swap device survive a reboot, and
// writing one is `mount`'s job: its `mounted` state already manages fstab
// entries of any type, `swap` included, the same way `quota.on`/
// `quota.off` reach only quotaon/quotaoff and leave the mount table to
// `mount`. This module reaches only the kernel's live state, through
// `swapon(8)`/`swapoff(8)`. A tree that wants swap on at boot writes both:
// a `mount.mounted` entry with `fstype: swap` for the table, and `swap.on`
// for the state the current boot is in.
func registerSwap(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "swap", Function: "list",
				Doc:       "Return the active swap devices and files, with their size and how much is used.",
				TestMode:  signature.TestNotApplicable,
				Platforms: swapPlatforms,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return activeSwaps(c)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "swap", Function: "on",
				Doc: "Enable a device or file as swap space.",
				Params: []signature.Param{
					req("name", signature.Path, "The device or file to enable as swap."),
					opt("priority", signature.Int, int64(-1),
						"Linux swap priority: higher is tried first. -1 lets the kernel choose. "+
							"FreeBSD has no such setting and refuses anything else."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  swapPlatforms,
				Section:    "15.2",
			},
			Fn: swapOnFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "swap", Function: "off",
				Doc: "Disable a device or file as swap space.",
				Params: []signature.Param{
					req("name", signature.Path, "The device or file to stop using as swap."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  swapPlatforms,
				Section:    "15.2",
			},
			Fn: swapOffFn,
		},
	)
}

// ---- the platform table ----

// swapOnArgv is the command that enables a swap device or file.
//
// See the package doc on registerSwap for why this is a table of one real
// difference rather than a runtime.GOOS branch inline: the difference is
// checkable from any host this way, and a decision buried in the
// function that also runs the command is checkable only on the platform
// that takes it.
func swapOnArgv(goos, path string, priority int64) ([]string, error) {
	switch goos {
	case "linux":
		if priority >= 0 {
			return []string{"swapon", "-p", strconv.FormatInt(priority, 10), path}, nil
		}
		return []string{"swapon", path}, nil
	case "freebsd":
		if priority >= 0 {
			return nil, fmt.Errorf(
				"a priority of %d was given, but FreeBSD's swapon has no -p; "+
					"it has nothing to bias its choice with", priority)
		}
		return []string{"swapon", path}, nil
	}
	return nil, fmt.Errorf("this build does not know how to enable swap on %s", goos)
}

// swapOffArgv is the command that disables a swap device or file.
func swapOffArgv(goos, path string) ([]string, error) {
	switch goos {
	case "linux", "freebsd":
		return []string{"swapoff", path}, nil
	}
	return nil, fmt.Errorf("this build does not know how to disable swap on %s", goos)
}

// ---- reading, for the idempotency check ----

// swapPathActive reports whether `activeSwaps` already lists a path.
//
// It is a function of its own rather than inlined into `swapOnFn`, so
// that the "is this path already active" question can be answered from a
// map built by hand in a test, without going through a real read of
// /proc/swaps — which, on a machine that has one, `activeSwaps` reads
// directly from the filesystem and no test double can intercept.
func swapPathActive(active *value.Map, path string) bool {
	if active == nil {
		return false
	}
	_, ok := active.GetString(path)
	return ok
}

// ---- mutating ----

func swapOnFn(c *exec.Context, args *value.Map) (any, error) {
	path := strings.TrimSpace(states.Str(args, "name", ""))
	if path == "" {
		return nil, errors.New("a device or file must be named")
	}
	priority := states.Int(args, "priority", -1)

	argv, err := swapOnArgv(runtime.GOOS, path, priority)
	if err != nil {
		return nil, err
	}
	if c.Which(argv[0]) == "" {
		return nil, fmt.Errorf("this node has no `%s`", argv[0])
	}

	// A read that fails here is not fatal to the request: it costs the
	// "already on" answer, the way `quotaCurrent`'s failure does, and
	// `swapon` itself will say so if the device turns out to be active
	// already.
	if active, aerr := activeSwaps(c); aerr == nil && swapPathActive(active, path) {
		return swapMutateResult(c, false, fmt.Sprintf("%s is already swapped on.", path), nil), nil
	}

	change := value.MapOf(path, states.Change("off", "on"))
	if c.Test {
		return swapMutateResult(c, true, fmt.Sprintf("%s would be enabled as swap.", path), change), nil
	}
	if err := swapRun(c, argv); err != nil {
		return nil, err
	}
	return swapMutateResult(c, true, fmt.Sprintf("%s was enabled as swap.", path), change), nil
}

func swapOffFn(c *exec.Context, args *value.Map) (any, error) {
	path := strings.TrimSpace(states.Str(args, "name", ""))
	if path == "" {
		return nil, errors.New("a device or file must be named")
	}

	argv, err := swapOffArgv(runtime.GOOS, path)
	if err != nil {
		return nil, err
	}
	if c.Which(argv[0]) == "" {
		return nil, fmt.Errorf("this node has no `%s`", argv[0])
	}

	if active, aerr := activeSwaps(c); aerr == nil && !swapPathActive(active, path) {
		return swapMutateResult(c, false, fmt.Sprintf("%s is already off.", path), nil), nil
	}

	change := value.MapOf(path, states.Change("on", "off"))
	if c.Test {
		return swapMutateResult(c, true, fmt.Sprintf("%s would be disabled as swap.", path), change), nil
	}
	if err := swapRun(c, argv); err != nil {
		return nil, err
	}
	return swapMutateResult(c, true, fmt.Sprintf("%s was disabled as swap.", path), change), nil
}

// swapRun runs a mutating command and turns a non-zero exit into an
// error carrying the tool's own words.
func swapRun(c *exec.Context, argv []string) error {
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return fmt.Errorf("%s could not be run: %w", argv[0], err)
	}
	if res.Code != 0 {
		return fmt.Errorf("`%s` exited %d: %s", exec.Command{Argv: argv}.String(), res.Code,
			strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return nil
}

// swapMutateResult shapes a changed/comment/changes answer the way
// `udevMutateResult` does; copied rather than exported and shared,
// because the two modules have no other reason to depend on one another
// and a shared helper would be a coupling neither needs.
func swapMutateResult(c *exec.Context, changed bool, comment string, change *value.Map) *value.Map {
	out := value.NewMap(3)
	out.Set("changed", changed)
	if c.Test && changed {
		out.Set("comment", comment+" Nothing was changed: this was a test run.")
	} else {
		out.Set("comment", comment)
	}
	if change != nil {
		out.Set("changes", change)
	}
	return out
}
