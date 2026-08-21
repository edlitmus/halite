package builtin

import (
	"fmt"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The pip, npm, and gem states of SPEC section 15.5.
//
// All three have the same shape, and it is the shape the test-mode
// contract of section 11.6 needs: read what is installed, compare it with
// what the tree asked for, and either predict the difference or make it.
// Nothing here shells out before it knows there is something to do, so a
// test run costs one `list` and changes nothing.

// pkgSpec is one requested package, split into what to compare and what to
// pass to the tool.
type pkgSpec struct {
	Name    string
	Version string // empty means any version will do
	Raw     string
}

// parseSpec splits `requests==2.31.0`, `left-pad@1.3.0`, or a bare name.
// The separator differs per ecosystem, so the caller names it.
func parseSpec(raw, sep string) pkgSpec {
	if sep != "" {
		if name, version, ok := strings.Cut(raw, sep); ok {
			return pkgSpec{Name: name, Version: version, Raw: raw}
		}
	}
	return pkgSpec{Name: raw, Raw: raw}
}

// requestedSpecs reads a state's `name` and `pkgs` into specs.
func requestedSpecs(args *value.Map, sep string) []pkgSpec {
	raw := states.Strings(args, "pkgs")
	if len(raw) == 0 {
		if n := states.Str(args, "name", ""); n != "" {
			raw = []string{n}
		}
	}
	out := make([]pkgSpec, 0, len(raw))
	for _, r := range raw {
		out = append(out, parseSpec(r, sep))
	}
	return out
}

// langPresent is the body every `installed` state shares.
//
// installed reports what is on the node; install is what to run for the
// specs that are missing or at the wrong version.
func langPresent(
	c *exec.Context,
	what string,
	specs []pkgSpec,
	installed func() (*value.Map, error),
	install func([]string) error,
) (states.Result, error) {
	if len(specs) == 0 {
		return states.False(fmt.Sprintf("This state needs at least one %s to install.", what)), nil
	}
	have, err := installed()
	if err != nil {
		return states.False(fmt.Sprintf("The installed %ss could not be read: %v", what, err)), nil
	}

	changes := value.NewMap(len(specs))
	var missing, unknown []string
	for _, s := range specs {
		cur, ok := have.Get(s.Name)
		curStr := ""
		if ok {
			curStr = value.KeyString(cur)
		}
		switch {
		case !ok:
			changes.Set(s.Name, states.Change(nil, orAny(s.Version)))
			missing = append(missing, s.Raw)
		case s.Version == "":
			// Present, and the tree did not say which version.
		case curStr == "":
			// Present, but the tool did not report a version: npm omits
			// it for a package it cannot resolve. Reinstalling on every
			// run would never converge and would never say why, so the
			// state refuses and names the package instead.
			unknown = append(unknown, s.Name)
		case curStr != s.Version:
			changes.Set(s.Name, states.Change(curStr, s.Version))
			missing = append(missing, s.Raw)
		}
	}
	if len(unknown) > 0 {
		return states.False(fmt.Sprintf(
			"%s is installed but the tool did not report a version, so the pinned version cannot be checked. "+
				"Ask for the %s without a version, or repair the installation.",
			states.SortedNames(unknown), what)), nil
	}

	if changes.Len() == 0 {
		return states.True(fmt.Sprintf("%s is already installed.", namesOf(specs, what))), nil
	}
	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("%s would be installed.", states.SortedNames(missing)), changes), nil
	}
	if err := install(missing); err != nil {
		return states.False(fmt.Sprintf("Installing %s failed: %v", states.SortedNames(missing), err)), nil
	}
	return states.Changed(fmt.Sprintf("%s was installed.", states.SortedNames(missing)), changes), nil
}

// langAbsent is the body every `removed` state shares.
func langAbsent(
	c *exec.Context,
	what string,
	specs []pkgSpec,
	installed func() (*value.Map, error),
	remove func([]string) error,
) (states.Result, error) {
	if len(specs) == 0 {
		return states.False(fmt.Sprintf("This state needs at least one %s to remove.", what)), nil
	}
	have, err := installed()
	if err != nil {
		return states.False(fmt.Sprintf("The installed %ss could not be read: %v", what, err)), nil
	}

	changes := value.NewMap(len(specs))
	var present []string
	for _, s := range specs {
		if cur, ok := have.Get(s.Name); ok {
			changes.Set(s.Name, states.Change(value.KeyString(cur), nil))
			present = append(present, s.Name)
		}
	}

	if changes.Len() == 0 {
		return states.True(fmt.Sprintf("%s is not installed.", namesOf(specs, what))), nil
	}
	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("%s would be removed.", states.SortedNames(present)), changes), nil
	}
	if err := remove(present); err != nil {
		return states.False(fmt.Sprintf("Removing %s failed: %v", states.SortedNames(present), err)), nil
	}
	return states.Changed(fmt.Sprintf("%s was removed.", states.SortedNames(present)), changes), nil
}

func orAny(version string) any {
	if version == "" {
		return "installed"
	}
	return version
}

func namesOf(specs []pkgSpec, what string) string {
	names := make([]string, 0, len(specs))
	for _, s := range specs {
		names = append(names, s.Name)
	}
	if len(names) == 1 {
		return strings.Title(what) + " " + names[0] // lexicon:allow
	}
	return states.SortedNames(names)
}

// langStateParams is the argument set all six of these states share.
func langStateParams(what string, extra ...signature.Param) []signature.Param {
	base := []signature.Param{
		req("name", signature.String, "A single "+what+", when `pkgs` is not given."),
		opt("pkgs", signature.List, nil, "Several "+what+"s, each optionally carrying a version."),
	}
	return append(base, extra...)
}

func registerLangStates(r *Registries) {
	pipInstalled := func(c *exec.Context, args *value.Map) (*value.Map, error) {
		res, err := pipRun(c, args, "list", "--format=json")
		if err != nil {
			return nil, err
		}
		return parsePipList(res.Stdout)
	}

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "pip", Function: "installed",
				Doc:      "Install Python packages.",
				Params:   langStateParams("package", opt("bin_env", signature.Path, "", "A virtualenv directory or a pip binary.")),
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return langPresent(c, "package", requestedSpecs(args, "=="),
					func() (*value.Map, error) { return pipInstalled(c, args) },
					func(pkgs []string) error {
						_, err := pipRun(c, args, append([]string{"install"}, pkgs...)...)
						return err
					})
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "pip", Function: "removed",
				Doc:      "Remove Python packages.",
				Params:   langStateParams("package", opt("bin_env", signature.Path, "", "A virtualenv directory or a pip binary.")),
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return langAbsent(c, "package", requestedSpecs(args, "=="),
					func() (*value.Map, error) { return pipInstalled(c, args) },
					func(pkgs []string) error {
						_, err := pipRun(c, args, append([]string{"uninstall", "--yes"}, pkgs...)...)
						return err
					})
			},
		},

		states.Module{
			Sig: signature.Signature{
				Module: "npm", Function: "installed",
				Doc:      "Install npm packages.",
				Params:   langStateParams("package", opt("dir", signature.Path, "", "The project directory. Empty installs globally.")),
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return langPresent(c, "package", requestedSpecs(args, "@"),
					func() (*value.Map, error) { return npmInstalled(c, args) },
					func(pkgs []string) error { return npmRun(c, args, "install", pkgs) })
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "npm", Function: "removed",
				Doc:      "Remove npm packages.",
				Params:   langStateParams("package", opt("dir", signature.Path, "", "The project directory. Empty removes globally.")),
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return langAbsent(c, "package", requestedSpecs(args, "@"),
					func() (*value.Map, error) { return npmInstalled(c, args) },
					func(pkgs []string) error { return npmRun(c, args, "uninstall", pkgs) })
			},
		},

		states.Module{
			Sig: signature.Signature{
				Module: "gem", Function: "installed",
				Doc:      "Install Ruby gems.",
				Params:   langStateParams("gem"),
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return langPresent(c, "gem", requestedSpecs(args, ":"),
					func() (*value.Map, error) { return gemInstalled(c) },
					func(gems []string) error {
						_, err := langRun(c, "gem", "", append([]string{"install", "--no-document"}, gems...)...)
						return err
					})
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "gem", Function: "removed",
				Doc:      "Remove Ruby gems.",
				Params:   langStateParams("gem"),
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return langAbsent(c, "gem", requestedSpecs(args, ":"),
					func() (*value.Map, error) { return gemInstalled(c) },
					func(gems []string) error {
						_, err := langRun(c, "gem", "", append([]string{"uninstall", "--executables", "--all"}, gems...)...)
						return err
					})
			},
		},
	)
}

func npmInstalled(c *exec.Context, args *value.Map) (*value.Map, error) {
	if c.Which("npm") == "" {
		return nil, fmt.Errorf("npm was not found on this node")
	}
	dir := states.Str(args, "dir", "")
	argv := []string{"ls", "--json", "--depth=0"}
	if dir == "" {
		argv = append(argv, "--global")
	}
	res, _ := c.Run(exec.Command{
		Argv: append([]string{"npm"}, argv...), Dir: dir, IgnoreExitCode: true,
	})
	return parseNpmList(res.Stdout)
}

func npmRun(c *exec.Context, args *value.Map, verb string, pkgs []string) error {
	dir := states.Str(args, "dir", "")
	argv := []string{verb}
	if dir == "" {
		argv = append(argv, "--global")
	}
	argv = append(argv, pkgs...)
	if c.Which("npm") == "" {
		return fmt.Errorf("npm was not found on this node")
	}
	_, err := c.Run(exec.Command{Argv: append([]string{"npm"}, argv...), Dir: dir})
	return err
}

func gemInstalled(c *exec.Context) (*value.Map, error) {
	res, err := langRun(c, "gem", "", "list", "--local")
	if err != nil {
		return nil, err
	}
	return parseGemList(res.Stdout), nil
}
