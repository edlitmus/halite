package builtin

import (
	"fmt"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The build-tool half of SPEC section 15.4: cargo, go, composer, cpan, and
// maven. A tree reaches for these to install a tool onto a node or to run
// a build in a checkout, so each carries `install`, a `list` where the
// tool can answer one, and `version`.

// ---- cargo ----

func registerCargo(r *Registries) {
	r.Exec.Add(
		langVersion("cargo", "cargo", []string{"--version"}, firstWordAfter("cargo")),
		exec.Module{
			Sig: signature.Signature{
				Module: "cargo", Function: "list",
				Doc:      "Return the crates installed by `cargo install`, as a mapping of name to version.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				res, err := langRun(c, "cargo", "", "install", "--list")
				if err != nil {
					return nil, err
				}
				return parseCargoList(res.Stdout), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "cargo", Function: "install",
				Doc: "Install one or more crates.",
				Params: []signature.Param{
					req("crates", signature.List, "Crate names."),
					opt("version", signature.String, "", "A version to pin all of them to."),
					opt("locked", signature.Bool, true, "Pass --locked, so the crate's own lock file decides its dependencies."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				argv := []string{"install"}
				if states.Bool(args, "locked", true) {
					argv = append(argv, "--locked")
				}
				if v := states.Str(args, "version", ""); v != "" {
					argv = append(argv, "--version", v)
				}
				argv = append(argv, states.Strings(args, "crates")...)
				res, err := langRun(c, "cargo", "", argv...)
				if err != nil {
					return nil, err
				}
				return strings.TrimSpace(res.Stderr), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "cargo", Function: "uninstall",
				Doc:      "Remove one or more crates.",
				Params:   []signature.Param{req("crates", signature.List, "Crate names.")},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				argv := append([]string{"uninstall"}, states.Strings(args, "crates")...)
				res, err := langRun(c, "cargo", "", argv...)
				if err != nil {
					return nil, err
				}
				return strings.TrimSpace(res.Stderr), nil
			},
		},
	)
}

// parseCargoList reads `cargo install --list`, whose output is a crate
// line "name v1.2.3:" followed by indented binary names.
func parseCargoList(out string) *value.Map {
	var pairs [][2]string
	for _, ln := range strings.Split(out, "\n") {
		if ln == "" || strings.HasPrefix(ln, " ") || strings.HasPrefix(ln, "\t") {
			continue
		}
		ln = strings.TrimSuffix(strings.TrimSpace(ln), ":")
		name, version, ok := strings.Cut(ln, " ")
		if !ok {
			continue
		}
		pairs = append(pairs, [2]string{name, strings.TrimPrefix(version, "v")})
	}
	return pkgList(pairs)
}

// ---- go ----

func registerGoTool(r *Registries) {
	r.Exec.Add(
		langVersion("go", "go", []string{"version"}, firstWordAfter("go version go")),
		exec.Module{
			Sig: signature.Signature{
				Module: "go", Function: "env",
				Doc: "Return the Go environment, or one variable of it.",
				Params: []signature.Param{
					opt("name", signature.String, "", "One variable to return instead of all of them."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if name := states.Str(args, "name", ""); name != "" {
					res, err := langRun(c, "go", "", "env", name)
					if err != nil {
						return nil, err
					}
					return strings.TrimSpace(res.Stdout), nil
				}
				res, err := langRun(c, "go", "", "env")
				if err != nil {
					return nil, err
				}
				return parseGoEnv(res.Stdout), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "go", Function: "install",
				Doc: "Install a command with `go install`.",
				Params: []signature.Param{
					req("pkgs", signature.List, "Package paths, such as `golang.org/x/tools/cmd/stringer@latest`."),
					opt("cwd", signature.Path, "", "A module directory to run in."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				argv := append([]string{"install"}, states.Strings(args, "pkgs")...)
				res, err := langRun(c, "go", states.Str(args, "cwd", ""), argv...)
				if err != nil {
					return nil, err
				}
				return strings.TrimSpace(res.Stderr), nil
			},
		},
	)
}

// parseGoEnv reads `go env`, whose lines are NAME='value' on unix.
func parseGoEnv(out string) *value.Map {
	m := value.NewMap(64)
	for _, ln := range strings.Split(out, "\n") {
		name, val, ok := strings.Cut(strings.TrimSpace(ln), "=")
		if !ok || name == "" {
			continue
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '\'' || val[0] == '"') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		m.Set(name, val)
	}
	return m
}

// ---- composer ----

func registerComposer(r *Registries) {
	r.Exec.Add(
		langVersion("composer", "composer", []string{"--version", "--no-ansi"}, firstLineOf),
		exec.Module{
			Sig: signature.Signature{
				Module: "composer", Function: "list",
				Doc: "Return a project's installed packages as a mapping of name to version.",
				Params: []signature.Param{
					req("dir", signature.Path, "The project directory."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				res, err := langRun(c, "composer", states.Str(args, "dir", ""),
					"show", "--format=json", "--no-ansi", "--no-interaction")
				if err != nil {
					return nil, err
				}
				return parseComposerShow(res.Stdout)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "composer", Function: "install",
				Doc: "Install a project's dependencies from its lock file.",
				Params: []signature.Param{
					req("dir", signature.Path, "The project directory."),
					opt("no_dev", signature.Bool, true, "Leave out the development dependencies."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				argv := []string{"install", "--no-ansi", "--no-interaction"}
				if states.Bool(args, "no_dev", true) {
					argv = append(argv, "--no-dev")
				}
				res, err := langRun(c, "composer", states.Str(args, "dir", ""), argv...)
				if err != nil {
					return nil, err
				}
				return strings.TrimSpace(res.Stderr), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "composer", Function: "require",
				Doc: "Add one or more packages to a project.",
				Params: []signature.Param{
					req("dir", signature.Path, "The project directory."),
					req("pkgs", signature.List, "Package specifiers, such as `monolog/monolog:^3.0`."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				argv := append([]string{"require", "--no-ansi", "--no-interaction"}, states.Strings(args, "pkgs")...)
				res, err := langRun(c, "composer", states.Str(args, "dir", ""), argv...)
				if err != nil {
					return nil, err
				}
				return strings.TrimSpace(res.Stderr), nil
			},
		},
	)
}

// parseComposerShow reads `composer show --format=json`, whose packages
// sit under an `installed` array.
func parseComposerShow(out string) (*value.Map, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return value.NewMap(0), nil
	}
	v, err := value.DecodeJSON([]byte(out))
	if err != nil {
		return nil, err
	}
	root, ok := v.(*value.Map)
	if !ok {
		return value.NewMap(0), nil
	}
	installed, ok := root.Get("installed")
	if !ok {
		return value.NewMap(0), nil
	}
	list, ok := installed.([]any)
	if !ok {
		return value.NewMap(0), nil
	}
	pairs := make([][2]string, 0, len(list))
	for _, item := range list {
		m, ok := item.(*value.Map)
		if !ok {
			continue
		}
		name, _ := m.Get("name")
		version, _ := m.Get("version")
		pairs = append(pairs, [2]string{value.KeyString(name), value.KeyString(version)})
	}
	return pkgList(pairs), nil
}

// ---- cpan ----

func registerCpan(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "cpan", Function: "version",
				Doc: "Return the CPAN client's version.",
				// Asked of perl rather than of `cpan -v`, which wants a
				// writable home directory to hold its configuration and
				// fails in the clean environment of SPEC section 25.4
				// with a permission error about `/CPAN`. Reading the
				// module's version needs no configuration at all.
				TestMode: signature.TestNotApplicable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				res, err := c.Run(exec.Command{
					Argv:           []string{"perl", "-MCPAN", "-e", "print $CPAN::VERSION"},
					IgnoreExitCode: true,
				})
				if err != nil || res.Code != 0 {
					return nil, fmt.Errorf("the CPAN client was not found on this node; the cpan module drives the system perl")
				}
				return strings.TrimSpace(res.Stdout), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "cpan", Function: "install",
				Doc: "Install one or more Perl modules.",
				Params: []signature.Param{
					req("modules", signature.List, "Module names, such as `JSON::PP`."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				argv := append([]string{"-i"}, states.Strings(args, "modules")...)
				res, err := langRun(c, "cpan", "", argv...)
				if err != nil {
					return nil, err
				}
				return strings.TrimSpace(res.Stdout), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "cpan", Function: "module_version",
				Doc: "Return the installed version of a Perl module, or the empty string. " +
					"`cpan.version` reports the tool's own version.",
				Params: []signature.Param{
					req("module", signature.String, "The module name."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				// perl answers this directly and cheaply; `cpan -D` starts
				// a session and reaches the network.
				mod := states.Str(args, "module", "")
				res, err := c.Run(exec.Command{
					Argv:           []string{"perl", "-M" + mod, "-e", "print $" + mod + "::VERSION"},
					IgnoreExitCode: true,
				})
				if err != nil || res.Code != 0 {
					return "", nil
				}
				return strings.TrimSpace(res.Stdout), nil
			},
		},
	)
}

// ---- maven ----

func registerMaven(r *Registries) {
	r.Exec.Add(
		langVersion("maven", "mvn", []string{"--version", "--batch-mode"}, firstWordAfter("Apache Maven")),
		exec.Module{
			Sig: signature.Signature{
				Module: "maven", Function: "run",
				Doc: "Run one or more Maven goals in a project.",
				Params: []signature.Param{
					req("dir", signature.Path, "The project directory."),
					req("goals", signature.List, "Goals, such as `clean` and `package`."),
					opt("properties", signature.Map, nil, "Properties passed as -Dname=value."),
				},
				Mutates:  true,
				TestMode: signature.TestUnreliable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				argv := []string{"--batch-mode"}
				if props := states.Mapping(args, "properties"); props != nil {
					for _, e := range props.Entries() {
						argv = append(argv, "-D"+value.KeyString(e.Key)+"="+value.KeyString(e.Val))
					}
				}
				argv = append(argv, states.Strings(args, "goals")...)
				res, err := langRun(c, "mvn", states.Str(args, "dir", ""), argv...)
				if err != nil {
					return nil, err
				}
				return strings.TrimSpace(res.Stdout), nil
			},
		},
	)
}
