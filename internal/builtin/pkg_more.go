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

// The rest of the pkg module's execution functions, SPEC section 15.2.
//
// The ones a tree reaches for between `pkg.installed` states: purge and
// upgrade, the hold that stops an upgrade, the queries that say what a
// package owns and what owns a file, and the repository listing. Each sits
// behind an optional interface rather than in pkgProvider, because they
// are not all universal — apk has no hold in the dpkg sense, and pkgng's
// idea of a repository is a file rather than a line in sources.list.
//
// A provider that cannot answer says so, naming itself, rather than
// returning an empty answer that reads as "there are none".

// pkgHolder is a provider that can pin a package against upgrade.
type pkgHolder interface {
	Hold(c *exec.Context, name string) error
	Unhold(c *exec.Context, name string) error
	ListHolds(c *exec.Context) ([]string, error)
}

// pkgUpgrader is a provider that can upgrade everything at once and say
// what is upgradable.
type pkgUpgrader interface {
	Upgrade(c *exec.Context, refresh bool) (*value.Map, error)
	ListUpgrades(c *exec.Context, refresh bool) (*value.Map, error)
}

// pkgOwner is a provider that can map between packages and the files
// they own.
type pkgOwner interface {
	FileList(c *exec.Context, name string) ([]string, error)
	OwnerOf(c *exec.Context, path string) (string, error)
}

// pkgRepos is a provider that can list its configured repositories.
type pkgRepos interface {
	ListRepos(c *exec.Context) (*value.Map, error)
}

func registerPkgMore(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "purge",
				Doc: "Remove packages and their configuration.",
				Params: []signature.Param{
					req("pkgs", signature.List, "Package names."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickPkgProvider(c)
				if err != nil {
					return nil, err
				}
				names := states.Strings(args, "pkgs")
				before, err := p.ListPkgs(c)
				if err != nil {
					return nil, err
				}
				if err := p.Remove(c, names, true); err != nil {
					return nil, err
				}
				return pkgDelta(c, p, before)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "upgrade",
				Doc: "Upgrade every package, returning what changed.",
				Params: []signature.Param{
					opt("refresh", signature.Bool, true, "Refresh the package metadata first."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, u, err := pickUpgrader(c)
				if err != nil {
					return nil, err
				}
				_ = p
				return u.Upgrade(c, states.Bool(args, "refresh", true))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "list_upgrades",
				Doc: "Return the packages with a newer version available, as a mapping of name to that version.",
				Params: []signature.Param{
					opt("refresh", signature.Bool, true, "Refresh the package metadata first."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				_, u, err := pickUpgrader(c)
				if err != nil {
					return nil, err
				}
				return u.ListUpgrades(c, states.Bool(args, "refresh", true))
			},
		},

		holdModule("hold", "Pin a package so an upgrade leaves it alone.",
			func(h pkgHolder, c *exec.Context, name string) error { return h.Hold(c, name) }),
		holdModule("unhold", "Release a pinned package.",
			func(h pkgHolder, c *exec.Context, name string) error { return h.Unhold(c, name) }),
		exec.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "list_holds",
				Doc:      "Return the pinned packages.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				h, err := pickHolder(c)
				if err != nil {
					return nil, err
				}
				names, err := h.ListHolds(c)
				if err != nil {
					return nil, err
				}
				sort.Strings(names)
				out := make([]any, len(names))
				for i, n := range names {
					out[i] = n
				}
				return out, nil
			},
		},

		exec.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "file_list",
				Doc: "Return the files a package owns.",
				Params: []signature.Param{
					req("name", signature.String, "The package."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				o, err := pickOwner(c)
				if err != nil {
					return nil, err
				}
				files, err := o.FileList(c, states.Str(args, "name", ""))
				if err != nil {
					return nil, err
				}
				sort.Strings(files)
				out := make([]any, len(files))
				for i, f := range files {
					out[i] = f
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "owner",
				Doc: "Return the package that owns a path, or the empty string.",
				Params: []signature.Param{
					req("path", signature.Path, "The path."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				o, err := pickOwner(c)
				if err != nil {
					return nil, err
				}
				return o.OwnerOf(c, states.Str(args, "path", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "available_version",
				Doc: "Return the newest available version of a package, or the empty string when it is " +
					"already at the newest. Salt's other name for latest_version, and trees use both.",
				Params: []signature.Param{
					req("name", signature.String, "The package."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickPkgProvider(c)
				if err != nil {
					return nil, err
				}
				return p.LatestVersion(c, states.Str(args, "name", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "list_repos",
				Doc:      "Return the configured repositories.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickPkgProvider(c)
				if err != nil {
					return nil, err
				}
				rp, ok := p.(pkgRepos)
				if !ok {
					return nil, fmt.Errorf("the %s provider cannot list repositories", p.Name())
				}
				return rp.ListRepos(c)
			},
		},
	)
}

func holdModule(name, doc string, run func(pkgHolder, *exec.Context, string) error) exec.Module {
	return exec.Module{
		Sig: signature.Signature{
			Module: "pkg", Function: name,
			Doc: doc,
			Params: []signature.Param{
				req("name", signature.String, "The package."),
			},
			Mutates:  true,
			TestMode: signature.TestReliable,
			Section:  "15.2",
		},
		Fn: func(c *exec.Context, args *value.Map) (any, error) {
			h, err := pickHolder(c)
			if err != nil {
				return nil, err
			}
			if err := run(h, c, states.Str(args, "name", "")); err != nil {
				return nil, err
			}
			return true, nil
		},
	}
}

func pickHolder(c *exec.Context) (pkgHolder, error) {
	p, err := pickPkgProvider(c)
	if err != nil {
		return nil, err
	}
	h, ok := p.(pkgHolder)
	if !ok {
		return nil, fmt.Errorf("the %s provider cannot hold a package against upgrade", p.Name())
	}
	return h, nil
}

func pickUpgrader(c *exec.Context) (pkgProvider, pkgUpgrader, error) {
	p, err := pickPkgProvider(c)
	if err != nil {
		return nil, nil, err
	}
	u, ok := p.(pkgUpgrader)
	if !ok {
		return nil, nil, fmt.Errorf("the %s provider cannot upgrade every package at once", p.Name())
	}
	return p, u, nil
}

func pickOwner(c *exec.Context) (pkgOwner, error) {
	p, err := pickPkgProvider(c)
	if err != nil {
		return nil, err
	}
	o, ok := p.(pkgOwner)
	if !ok {
		return nil, fmt.Errorf("the %s provider cannot map packages to the files they own", p.Name())
	}
	return o, nil
}

// pkgDelta reports what a change did, by comparing the package list
// before it with the list after. It is how every mutating pkg function
// answers, so a state's `changes` is what actually happened rather than
// what was asked for.
func pkgDelta(c *exec.Context, p pkgProvider, before *value.Map) (*value.Map, error) {
	after, err := p.ListPkgs(c)
	if err != nil {
		return nil, err
	}
	changes := value.NewMap(8)
	for _, e := range before.Entries() {
		name := value.KeyString(e.Key)
		if now, ok := after.Get(name); !ok {
			changes.Set(name, states.Change(e.Val, nil))
		} else if value.KeyString(now) != value.KeyString(e.Val) {
			changes.Set(name, states.Change(e.Val, now))
		}
	}
	for _, e := range after.Entries() {
		if name := value.KeyString(e.Key); !before.Has(name) {
			changes.Set(name, states.Change(nil, e.Val))
		}
	}
	return changes, nil
}

// ---- pkgng, the provider this host runs ----

func (pkgngProvider) Hold(c *exec.Context, name string) error {
	// pkg's lock is its hold: a locked package is skipped by upgrade and
	// cannot be removed until it is unlocked.
	_, err := c.Run(exec.Command{Argv: []string{"pkg", "lock", "--yes", name}})
	return err
}

func (pkgngProvider) Unhold(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"pkg", "unlock", "--yes", name}})
	return err
}

func (pkgngProvider) ListHolds(c *exec.Context) ([]string, error) {
	res, err := c.Run(exec.Command{
		Argv: []string{"pkg", "lock", "--list", "--quiet"}, IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ln := range strings.Split(res.Stdout, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			// The listing is `name-version`; a hold is on the name.
			if i := strings.LastIndexByte(ln, '-'); i > 0 {
				ln = ln[:i]
			}
			out = append(out, ln)
		}
	}
	return out, nil
}

func (p pkgngProvider) Upgrade(c *exec.Context, refresh bool) (*value.Map, error) {
	before, err := p.ListPkgs(c)
	if err != nil {
		return nil, err
	}
	argv := []string{"pkg", "upgrade", "--yes"}
	if !refresh {
		argv = append(argv, "--no-repo-update")
	}
	if _, err := c.Run(exec.Command{Argv: argv}); err != nil {
		return nil, err
	}
	return pkgDelta(c, p, before)
}

func (pkgngProvider) ListUpgrades(c *exec.Context, refresh bool) (*value.Map, error) {
	argv := []string{"pkg", "upgrade", "--dry-run", "--quiet"}
	if !refresh {
		argv = append(argv, "--no-repo-update")
	}
	// A dry run exits non-zero when there is nothing to do, which is an
	// answer rather than a failure.
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(8)
	for _, ln := range strings.Split(res.Stdout, "\n") {
		ln = strings.TrimSpace(ln)
		// The upgrade lines read `name: 1.0 -> 1.1`.
		name, rest, ok := strings.Cut(ln, ": ")
		if !ok || !strings.Contains(rest, "->") {
			continue
		}
		_, to, _ := strings.Cut(rest, "->")
		out.Set(strings.TrimSpace(name), strings.TrimSpace(to))
	}
	return out, nil
}

func (pkgngProvider) FileList(c *exec.Context, name string) ([]string, error) {
	res, err := c.Run(exec.Command{Argv: []string{"pkg", "list", name}})
	if err != nil {
		return nil, err
	}
	return sortedLines(res.Stdout), nil
}

func (pkgngProvider) OwnerOf(c *exec.Context, path string) (string, error) {
	res, err := c.Run(exec.Command{
		Argv: []string{"pkg", "which", "--quiet", "--origin", path}, IgnoreExitCode: true,
	})
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		// A path no package owns is a normal answer, not an error: a tree
		// asks this to decide whether it may manage the file.
		return "", nil
	}
	return strings.TrimSpace(res.Stdout), nil
}

func (pkgngProvider) ListRepos(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{Argv: []string{"pkg", "-vv"}, IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(4)
	var current string
	for _, ln := range strings.Split(res.Stdout, "\n") {
		trimmed := strings.TrimSpace(ln)
		// A repository block opens with `name: {` at one level of indent.
		if name, rest, ok := strings.Cut(trimmed, ": "); ok && strings.TrimSpace(rest) == "{" &&
			strings.HasPrefix(ln, "  ") && !strings.HasPrefix(ln, "    ") {
			current = name
			out.Set(current, value.NewMap(4))
			continue
		}
		if current == "" {
			continue
		}
		if key, val, ok := strings.Cut(trimmed, ": "); ok {
			val = strings.TrimSuffix(strings.TrimSpace(val), ",")
			val = strings.Trim(val, `"`)
			if m, ok := mustMap(out, current); ok {
				m.Set(strings.TrimSpace(key), val)
			}
		}
		if trimmed == "}" {
			current = ""
		}
	}
	return out, nil
}

func mustMap(m *value.Map, key string) (*value.Map, bool) {
	v, ok := m.Get(key)
	if !ok {
		return nil, false
	}
	sub, ok := v.(*value.Map)
	return sub, ok
}
