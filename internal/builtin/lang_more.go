package builtin

import (
	"fmt"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// ---- virtualenv ----

func registerVirtualenv(r *Registries) {
	r.Exec.Add(
		langVersion("virtualenv", "virtualenv", []string{"--version"}, firstLineOf),
		exec.Module{
			Sig: signature.Signature{
				Module: "virtualenv", Function: "create",
				Doc: "Create a virtualenv, or report that one is already there.",
				Params: []signature.Param{
					req("path", signature.Path, "Where to create it."),
					opt("python", signature.Path, "", "The interpreter to build it from."),
					opt("system_site_packages", signature.Bool, false, "Give it the system packages."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				path := states.Str(args, "path", "")
				if path == "" {
					return nil, fmt.Errorf("virtualenv.create needs a path")
				}
				argv := []string{}
				if p := states.Str(args, "python", ""); p != "" {
					argv = append(argv, "--python", p)
				}
				if states.Bool(args, "system_site_packages", false) {
					argv = append(argv, "--system-site-packages")
				}
				argv = append(argv, path)
				res, err := langRun(c, "virtualenv", "", argv...)
				if err != nil {
					return nil, err
				}
				return strings.TrimSpace(res.Stdout), nil
			},
		},
	)
}

// ---- npm ----

func registerNpm(r *Registries) {
	r.Exec.Add(
		langVersion("npm", "npm", []string{"--version"}, firstLineOf),
		exec.Module{
			Sig: signature.Signature{
				Module: "npm", Function: "list",
				Doc: "Return the installed packages as a mapping of name to version.",
				Params: []signature.Param{
					opt("dir", signature.Path, "", "The project directory. Empty means the global install."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				dir := states.Str(args, "dir", "")
				argv := []string{"ls", "--json", "--depth=0"}
				if dir == "" {
					argv = append(argv, "--global")
				}
				// `npm ls` exits non-zero when the tree has any problem,
				// including one unrelated to the question asked, so the
				// JSON is read whatever the exit code says.
				res, _ := c.Run(exec.Command{
					Argv: append([]string{"npm"}, argv...), Dir: dir, IgnoreExitCode: true,
				})
				if c.Which("npm") == "" {
					return nil, fmt.Errorf("npm was not found on this node; the npm module drives the system binary")
				}
				return parseNpmList(res.Stdout)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "npm", Function: "install",
				Doc: "Install one or more packages.",
				Params: []signature.Param{
					req("pkgs", signature.List, "Package specifiers, such as `left-pad` or `left-pad@1.3.0`."),
					opt("dir", signature.Path, "", "The project directory. Empty installs globally."),
					opt("registry", signature.String, "", "An alternative registry."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return npmChange(c, args, "install")
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "npm", Function: "uninstall",
				Doc: "Remove one or more packages.",
				Params: []signature.Param{
					req("pkgs", signature.List, "Package names."),
					opt("dir", signature.Path, "", "The project directory. Empty removes globally."),
					opt("registry", signature.String, "", "An alternative registry."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return npmChange(c, args, "uninstall")
			},
		},
	)
}

func npmChange(c *exec.Context, args *value.Map, verb string) (any, error) {
	dir := states.Str(args, "dir", "")
	argv := []string{verb}
	if dir == "" {
		argv = append(argv, "--global")
	}
	if reg := states.Str(args, "registry", ""); reg != "" {
		argv = append(argv, "--registry", reg)
	}
	argv = append(argv, states.Strings(args, "pkgs")...)
	if c.Which("npm") == "" {
		return nil, fmt.Errorf("npm was not found on this node; the npm module drives the system binary")
	}
	res, err := c.Run(exec.Command{Argv: append([]string{"npm"}, argv...), Dir: dir})
	if err != nil {
		return nil, err
	}
	return strings.TrimSpace(res.Stdout), nil
}

// parseNpmList reads `npm ls --json`, whose packages sit under a
// `dependencies` object keyed by name.
func parseNpmList(out string) (*value.Map, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return value.NewMap(0), nil
	}
	v, err := value.DecodeJSON([]byte(out))
	if err != nil {
		return nil, fmt.Errorf("npm ls did not return JSON: %w", err)
	}
	root, ok := v.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("npm ls returned %s, expected an object", value.TypeName(v))
	}
	deps, ok := root.Get("dependencies")
	if !ok {
		return value.NewMap(0), nil
	}
	dm, ok := deps.(*value.Map)
	if !ok {
		return value.NewMap(0), nil
	}
	pairs := make([][2]string, 0, dm.Len())
	for _, e := range dm.Entries() {
		version := ""
		if sub, ok := e.Val.(*value.Map); ok {
			if v, ok := sub.Get("version"); ok {
				version = value.KeyString(v)
			}
		}
		pairs = append(pairs, [2]string{value.KeyString(e.Key), version})
	}
	return pkgList(pairs), nil
}

// ---- gem ----

func registerGem(r *Registries) {
	r.Exec.Add(
		langVersion("gem", "gem", []string{"--version"}, firstLineOf),
		exec.Module{
			Sig: signature.Signature{
				Module: "gem", Function: "list",
				Doc:      "Return the installed gems as a mapping of name to versions.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				res, err := langRun(c, "gem", "", "list", "--local")
				if err != nil {
					return nil, err
				}
				return parseGemList(res.Stdout), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "gem", Function: "install",
				Doc: "Install one or more gems.",
				Params: []signature.Param{
					req("gems", signature.List, "Gem names."),
					opt("version", signature.String, "", "A version to pin all of them to."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				argv := []string{"install", "--no-document"}
				if v := states.Str(args, "version", ""); v != "" {
					argv = append(argv, "--version", v)
				}
				argv = append(argv, states.Strings(args, "gems")...)
				res, err := langRun(c, "gem", "", argv...)
				if err != nil {
					return nil, err
				}
				return strings.TrimSpace(res.Stdout), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "gem", Function: "uninstall",
				Doc: "Remove one or more gems.",
				Params: []signature.Param{
					req("gems", signature.List, "Gem names."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				argv := append([]string{"uninstall", "--executables", "--all"}, states.Strings(args, "gems")...)
				res, err := langRun(c, "gem", "", argv...)
				if err != nil {
					return nil, err
				}
				return strings.TrimSpace(res.Stdout), nil
			},
		},
	)
}

// parseGemList reads `gem list --local`, whose lines are
// "name (1.2.3, 1.2.2)".
func parseGemList(out string) *value.Map {
	var pairs [][2]string
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "***") {
			continue
		}
		name, rest, ok := strings.Cut(ln, " (")
		if !ok {
			pairs = append(pairs, [2]string{ln, ""})
			continue
		}
		versions := strings.TrimSuffix(rest, ")")
		// The newest version comes first, which is the one a state that
		// asked for "installed" cares about.
		if i := strings.IndexByte(versions, ','); i > 0 {
			versions = versions[:i]
		}
		pairs = append(pairs, [2]string{strings.TrimSpace(name), strings.TrimSpace(versions)})
	}
	return pkgList(pairs)
}
