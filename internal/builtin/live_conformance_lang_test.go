package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The six language-manager states: `gem`, `npm` and `pip`.
//
// # Why all six are live, including the four that are confined
//
// `npm.installed` takes a `dir` and `pip.installed` takes a `bin_env`, and
// with those set the effect lands inside a directory the case makes — which is
// the test that put the sixteen `file` states in the in-process list. They are
// live cases anyway, for a reason that is neither privilege nor blast radius:
// **installing a package needs the network.**
//
// A unit suite that cannot run without the internet cannot run on an
// aeroplane, in a locked-down builder, or in the sandbox the release job uses.
// `go test ./...` must not need a package registry to answer, so these go
// behind the live gate with the rest, where the machine is a CI runner that
// has a network by definition.
//
// It also means a failure here can be the network rather than the code, and
// the case cannot tell the two apart. Stated rather than worked around: there
// is no probe for "the registry was reachable", and inventing one would be a
// second thing to get wrong.
//
// # What each one is pointed at
//
//   - `gem` at the system gem path, because `gem.installed` has no directory
//     argument to redirect. A real machine change, removed again afterwards.
//   - `npm` at a project directory of the case's own, through `dir`, so
//     nothing reaches the global prefix.
//   - `pip` at a virtualenv the case builds, through `bin_env`. That is also
//     how these avoid PEP 668: Debian 12 and Ubuntu 24.04 mark the system
//     Python externally managed and refuse `pip install` into it outright. A
//     venv is what the refusal itself recommends, and is better than the flag
//     that overrides it.
//
// The packages are the smallest pure ones their ecosystems have, so that
// nothing here compiles: `colorize` (Ruby, no dependencies), `ms`
// (JavaScript, no dependencies) and `six` (Python, no dependencies). A case
// that needed a C toolchain would be testing the toolchain.
//
// DIVERGENCE 5.157.

const (
	conformanceGem = "colorize"
	conformanceNpm = "ms"
	conformancePip = "six"
)

// langRig is the directory one case works in, made on first use so that
// construction touches nothing.
type langRig struct {
	c   *hexec.Context
	r   *Registries
	dir string
}

func newLangRig() *langRig { return &langRig{c: liveRoot(), r: New()} }

func (l *langRig) workDir() (string, error) {
	if l.dir != "" {
		return l.dir, nil
	}
	d, err := os.MkdirTemp("", liveConformancePrefix+"-lang-")
	if err != nil {
		return "", err
	}
	l.dir = d
	return d, nil
}

// fresh empties the working directory, so a second Setup starts where the
// first did rather than on top of what the first phase left.
func (l *langRig) fresh() (string, error) {
	l.drop()
	return l.workDir()
}

func (l *langRig) drop() {
	if l.dir != "" {
		_ = os.RemoveAll(l.dir)
		l.dir = ""
	}
}

func (l *langRig) quiet(dir string, argv ...string) {
	_, _ = l.c.Run(hexec.Command{Argv: argv, Dir: dir, IgnoreExitCode: true})
}

func (l *langRig) run(dir string, argv ...string) error {
	res, err := l.c.Run(hexec.Command{Argv: argv, Dir: dir, IgnoreExitCode: true})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s exited %d: %s", strings.Join(argv, " "), res.Code,
			strings.TrimSpace(res.Stdout+res.Stderr))
	}
	return nil
}

func langCases() []liveCase {
	var cases []liveCase
	cases = append(cases, gemCases()...)
	cases = append(cases, npmCases()...)
	cases = append(cases, pipCases()...)
	return cases
}

// ---- gem ----
//
// The one with nowhere to be confined to: `gem.installed` takes only names, so
// the gem lands in the system path. That is why this pair, unlike the other
// four, would change a machine somebody was using.
func gemCases() []liveCase {
	rig := newLangRig()

	// `gem list --exact` names the versions installed and nothing else. The
	// tool's own answer rather than the module's.
	probe := func() (string, error) {
		res, err := rig.c.Run(hexec.Command{
			Argv:           []string{"gem", "list", "--exact", conformanceGem},
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", err
		}
		for _, line := range strings.Split(res.Stdout, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), conformanceGem) {
				return strings.TrimSpace(line), nil
			}
		}
		return "absent", nil
	}
	remove := func() {
		rig.quiet("", "gem", "uninstall", "--executables", "--all", conformanceGem)
	}
	install := func() error {
		_, err := rig.r.States.Call(rig.c, "gem.installed",
			value.MapOf("name", conformanceGem))
		return err
	}

	withGem := func(lc liveCase) liveCase {
		lc.needs = []string{"gem"}
		return lc
	}
	return []liveCase{
		withGem(liveCase{Conformance: states.Conformance{
			Name:    "gem.installed",
			Args:    value.MapOf("name", conformanceGem),
			Probe:   probe,
			Setup:   func() error { remove(); return nil },
			Cleanup: remove,
		}}),
		withGem(liveCase{Conformance: states.Conformance{
			Name:    "gem.removed",
			Args:    value.MapOf("name", conformanceGem),
			Probe:   probe,
			Setup:   func() error { remove(); return install() },
			Cleanup: remove,
		}}),
	}
}

// ---- npm ----
//
// `dir` makes this a local install. The probe reads the filesystem rather than
// `npm ls`: that prints a tree whose shape and whose `problems` array differ
// between npm versions, and what is actually being asked is whether the
// package arrived. A directory under `node_modules` is that, and it is stable
// across versions.
func npmCases() []liveCase {
	rig := newLangRig()
	args := value.MapOf("name", conformanceNpm)

	probe := func() (string, error) {
		if rig.dir == "" {
			return "no project", nil
		}
		marker := filepath.Join(rig.dir, "node_modules", conformanceNpm, "package.json")
		if _, err := os.Stat(marker); err == nil {
			return "installed", nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		return "absent", nil
	}
	// A project directory carrying a package.json, so npm treats it as a
	// project rather than walking upward looking for one -- which on a CI
	// runner could find the checkout's own.
	project := func() (string, error) {
		dir, err := rig.fresh()
		if err != nil {
			return "", err
		}
		manifest := `{"name":"halite-conformance","version":"1.0.0","private":true}`
		if err := os.WriteFile(filepath.Join(dir, "package.json"),
			[]byte(manifest+"\n"), 0o644); err != nil {
			return "", err
		}
		args.Set("dir", dir)
		return dir, nil
	}

	withNpm := func(lc liveCase) liveCase {
		lc.needs = []string{"npm"}
		return lc
	}
	return []liveCase{
		withNpm(liveCase{Conformance: states.Conformance{
			Name:  "npm.installed",
			Args:  args,
			Probe: probe,
			Setup: func() error {
				_, err := project()
				return err
			},
			Cleanup: rig.drop,
		}}),
		withNpm(liveCase{Conformance: states.Conformance{
			Name:  "npm.removed",
			Args:  args,
			Probe: probe,
			Setup: func() error {
				dir, err := project()
				if err != nil {
					return err
				}
				return rig.run(dir, "npm", "install", "--no-audit", "--no-fund",
					conformanceNpm)
			},
			Cleanup: rig.drop,
		}}),
	}
}

// ---- pip ----
//
// `bin_env` points at a virtualenv the case builds. `pipRun` turns a directory
// into `<dir>/bin/pip`, an absolute path, and `langRun` looks that up with
// `exec.LookPath` -- which resolves a name containing a separator directly
// rather than searching PATH, so the venv's own pip is what runs. Checked
// before writing this: had it searched PATH instead, the whole `bin_env`
// argument would be unusable and that would have been the finding.
func pipCases() []liveCase {
	rig := newLangRig()
	args := value.MapOf("name", conformancePip)

	pipBin := func() string {
		if rig.dir == "" {
			return ""
		}
		return filepath.Join(rig.dir, "venv", "bin", "pip")
	}
	// `pip show` exits non-zero for a package that is not installed, which is
	// the question, and prints the version when it is.
	probe := func() (string, error) {
		bin := pipBin()
		if bin == "" {
			return "no venv", nil
		}
		res, err := rig.c.Run(hexec.Command{
			Argv:           []string{bin, "show", conformancePip},
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", err
		}
		if res.Code != 0 {
			return "absent", nil
		}
		for _, line := range strings.Split(res.Stdout, "\n") {
			if strings.HasPrefix(line, "Version:") {
				return strings.TrimSpace(line), nil
			}
		}
		return "installed", nil
	}
	venv := func() (string, error) {
		dir, err := rig.fresh()
		if err != nil {
			return "", err
		}
		env := filepath.Join(dir, "venv")
		if err := rig.run("", "python3", "-m", "venv", env); err != nil {
			return "", err
		}
		args.Set("bin_env", env)
		return env, nil
	}

	withPip := func(lc liveCase) liveCase {
		lc.needs = []string{"python3"}
		return lc
	}
	return []liveCase{
		withPip(liveCase{Conformance: states.Conformance{
			Name:  "pip.installed",
			Args:  args,
			Probe: probe,
			Setup: func() error {
				_, err := venv()
				return err
			},
			Cleanup: rig.drop,
		}}),
		withPip(liveCase{Conformance: states.Conformance{
			Name:  "pip.removed",
			Args:  args,
			Probe: probe,
			Setup: func() error {
				env, err := venv()
				if err != nil {
					return err
				}
				return rig.run("", filepath.Join(env, "bin", "pip"),
					"install", "--quiet", conformancePip)
			},
			Cleanup: rig.drop,
		}}),
	}
}
