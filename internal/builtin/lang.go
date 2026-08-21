package builtin

import (
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The language and runtime modules of SPEC section 15.4.
//
// Each wraps a system binary and parses its machine-readable output. No
// language runtime is embedded, and none of these links a library: the
// node inherits the operating system's patching cadence for the tool,
// which is the same argument section 4.2 makes for driving `git` rather
// than linking libgit2.
//
// They share a shape, because a tree uses them the same way: `install`,
// `remove`, `list`, and `version`. Depth beyond that is recorded as a gap
// rather than guessed at.

// langRun runs a language tool.
//
// A missing tool is an error naming the tool and the module, not a
// non-zero exit the caller has to interpret: `pip.install` on a node with
// no pip is a mistake in the tree, and it should read like one.
func langRun(c *exec.Context, tool, cwd string, argv ...string) (exec.Result, error) {
	if c.Which(tool) == "" {
		return exec.Result{}, fmt.Errorf("%s was not found on this node; the %s module drives the system binary",
			tool, tool)
	}
	return c.Run(exec.Command{
		Argv: append([]string{tool}, argv...),
		Dir:  cwd,
	})
}

// langVersion is the `version` function every one of these modules has.
func langVersion(module, tool string, argv []string, clean func(string) string) exec.Module {
	return exec.Module{
		Sig: signature.Signature{
			Module: module, Function: "version",
			Doc:      "Return the system " + tool + "'s version.",
			TestMode: signature.TestNotApplicable,
			Section:  "15.4",
		},
		Fn: func(c *exec.Context, args *value.Map) (any, error) {
			res, err := langRun(c, tool, "", argv...)
			if err != nil {
				return nil, err
			}
			out := strings.TrimSpace(res.Stdout)
			if out == "" {
				out = strings.TrimSpace(res.Stderr)
			}
			if clean != nil {
				return clean(out), nil
			}
			return out, nil
		},
	}
}

// firstWordAfter pulls the version out of a line such as
// "pip 24.0 from /usr/lib/python3/pip (python 3.11)".
func firstWordAfter(prefix string) func(string) string {
	return func(s string) string {
		s = firstLineOf(s)
		s = strings.TrimSpace(strings.TrimPrefix(s, prefix))
		if i := strings.IndexByte(s, ' '); i > 0 {
			s = s[:i]
		}
		return s
	}
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// pkgList is the shape every one of these `list` functions returns: an
// ordered mapping of package name to version, so a state can compare it
// against what a tree asked for without parsing anything twice.
func pkgList(pairs [][2]string) *value.Map {
	sort.Slice(pairs, func(i, j int) bool { return pairs[i][0] < pairs[j][0] })
	m := value.NewMap(len(pairs))
	for _, p := range pairs {
		m.Set(p[0], p[1])
	}
	return m
}

// ---- pip ----

func registerPip(r *Registries) {
	r.Exec.Add(
		langVersion("pip", "pip", []string{"--version"}, firstWordAfter("pip")),
		exec.Module{
			Sig: signature.Signature{
				Module: "pip", Function: "list",
				Doc: "Return the installed packages as a mapping of name to version.",
				Params: []signature.Param{
					opt("bin_env", signature.Path, "", "A virtualenv directory or a pip binary to use instead of the system pip."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				res, err := pipRun(c, args, "list", "--format=json")
				if err != nil {
					return nil, err
				}
				return parsePipList(res.Stdout)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pip", Function: "install",
				Doc: "Install one or more packages.",
				Params: []signature.Param{
					req("pkgs", signature.List, "Package specifiers, such as `requests` or `requests==2.31.0`."),
					opt("bin_env", signature.Path, "", "A virtualenv directory or a pip binary."),
					opt("upgrade", signature.Bool, false, "Pass --upgrade."),
					opt("index_url", signature.String, "", "An alternative index."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				argv := []string{"install"}
				if states.Bool(args, "upgrade", false) {
					argv = append(argv, "--upgrade")
				}
				if u := states.Str(args, "index_url", ""); u != "" {
					argv = append(argv, "--index-url", u)
				}
				argv = append(argv, states.Strings(args, "pkgs")...)
				res, err := pipRun(c, args, argv...)
				if err != nil {
					return nil, err
				}
				return strings.TrimSpace(res.Stdout), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pip", Function: "uninstall",
				Doc: "Remove one or more packages.",
				Params: []signature.Param{
					req("pkgs", signature.List, "Package names."),
					opt("bin_env", signature.Path, "", "A virtualenv directory or a pip binary."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				argv := append([]string{"uninstall", "--yes"}, states.Strings(args, "pkgs")...)
				res, err := pipRun(c, args, argv...)
				if err != nil {
					return nil, err
				}
				return strings.TrimSpace(res.Stdout), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pip", Function: "freeze",
				Doc: "Return the installed packages in requirements.txt form.",
				Params: []signature.Param{
					opt("bin_env", signature.Path, "", "A virtualenv directory or a pip binary."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				res, err := pipRun(c, args, "freeze")
				if err != nil {
					return nil, err
				}
				return linesOf(res.Stdout), nil
			},
		},
	)
}

// pipRun locates pip, honouring bin_env the way Salt trees expect: a
// directory is a virtualenv and its own pip is used, a file is a pip
// binary, and nothing means the system pip.
func pipRun(c *exec.Context, args *value.Map, argv ...string) (exec.Result, error) {
	tool := "pip"
	if env := states.Str(args, "bin_env", ""); env != "" {
		tool = env
		if !strings.HasSuffix(env, "pip") && !strings.HasSuffix(env, "pip3") {
			tool = strings.TrimRight(env, "/") + "/bin/pip"
		}
	}
	return langRun(c, tool, "", argv...)
}

// parsePipList reads `pip list --format=json`.
func parsePipList(out string) (*value.Map, error) {
	v, err := value.DecodeJSON([]byte(strings.TrimSpace(out)))
	if err != nil {
		return nil, fmt.Errorf("pip list did not return JSON: %w", err)
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("pip list returned %s, expected a list", value.TypeName(v))
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

func linesOf(s string) []any {
	var out []any
	for _, ln := range strings.Split(strings.TrimSpace(s), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
	}
	if out == nil {
		return []any{}
	}
	return out
}
