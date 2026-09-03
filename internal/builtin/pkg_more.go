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

// rfc822Blocks splits an apt- or dnf-style listing into blocks separated by
// a blank line, each a map of its `key: value` lines. The key is what
// precedes the first colon, trimmed, so a tool that pads its keys for
// alignment (`Repo-id      : baseos`) parses the same as one that does not.
// A value that itself contains `: ` keeps everything after the first colon.
func rfc822Blocks(s string) []map[string]string {
	var blocks []map[string]string
	cur := map[string]string{}
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, cur)
			cur = map[string]string{}
		}
	}
	for _, ln := range strings.Split(s, "\n") {
		if strings.TrimSpace(ln) == "" {
			flush()
			continue
		}
		key, val, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		cur[strings.TrimSpace(key)] = strings.TrimSpace(val)
	}
	flush()
	return blocks
}

// rpmShortName reduces an `rpm`/`dnf` NEVRA — `name-epoch:version-release.arch`
// — to the bare package name, the form a hold list is compared against. It
// strips a known architecture suffix and then the version and release, the
// same shape as the apk parser in pkg_providers.go.
func rpmShortName(nevra string) string {
	s := strings.TrimSpace(nevra)
	for _, arch := range []string{
		".x86_64", ".noarch", ".aarch64", ".i686", ".armv7hl", ".ppc64le", ".s390x",
	} {
		if strings.HasSuffix(s, arch) {
			s = strings.TrimSuffix(s, arch)
			break
		}
	}
	parts := strings.Split(s, "-")
	if len(parts) < 3 {
		return s
	}
	return strings.Join(parts[:len(parts)-2], "-")
}

// ---- apt: the optional interfaces ----
//
// dpkg and apt cover every one: `apt-mark` holds, `apt-get dist-upgrade`
// upgrades the world, `dpkg-query` maps packages to files, and
// `apt-get indextargets` is apt's own machine-readable account of its
// configured sources.

func (aptProvider) Hold(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"apt-mark", "hold", name}, Env: aptEnv()})
	return err
}

func (aptProvider) Unhold(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"apt-mark", "unhold", name}, Env: aptEnv()})
	return err
}

func (aptProvider) ListHolds(c *exec.Context) ([]string, error) {
	res, err := c.Run(exec.Command{Argv: []string{"apt-mark", "showhold"}, Env: aptEnv()})
	if err != nil {
		return nil, err
	}
	return sortedLines(res.Stdout), nil
}

func (p aptProvider) Upgrade(c *exec.Context, refresh bool) (*value.Map, error) {
	before, err := p.ListPkgs(c)
	if err != nil {
		return nil, err
	}
	if refresh {
		if _, err := c.Run(exec.Command{Argv: []string{"apt-get", "update", "-q"}, Env: aptEnv()}); err != nil {
			return nil, fmt.Errorf("apt-get update: %w", err)
		}
	}
	argv := []string{"apt-get", "dist-upgrade", "-y", "-q",
		"-o", "Dpkg::Options::=--force-confold",
		"-o", "Dpkg::Options::=--force-confdef"}
	if _, err := c.Run(exec.Command{Argv: argv, Env: aptEnv()}); err != nil {
		return nil, err
	}
	return pkgDelta(c, p, before)
}

func (aptProvider) ListUpgrades(c *exec.Context, refresh bool) (*value.Map, error) {
	if refresh {
		if _, err := c.Run(exec.Command{Argv: []string{"apt-get", "update", "-q"}, Env: aptEnv()}); err != nil {
			return nil, fmt.Errorf("apt-get update: %w", err)
		}
	}
	// A simulated dist-upgrade needs no privilege and prints one
	// `Inst name [oldver] (newver repo [arch])` line per upgradable package.
	res, err := c.Run(exec.Command{
		Argv: []string{"apt-get", "--simulate", "-q", "-o", "Debug::NoLocking=true", "dist-upgrade"},
		Env:  aptEnv(),
	})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(16)
	for _, ln := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(ln)
		if len(fields) < 4 || fields[0] != "Inst" {
			continue
		}
		out.Set(fields[1], strings.TrimPrefix(fields[3], "("))
	}
	return out, nil
}

func (aptProvider) FileList(c *exec.Context, name string) ([]string, error) {
	res, err := c.Run(exec.Command{Argv: []string{"dpkg-query", "-L", name}, Env: aptEnv()})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ln := range strings.Split(res.Stdout, "\n") {
		ln = strings.TrimSpace(ln)
		// dpkg lists the directories it owns and a `/.` root entry beside
		// the files; a caller asking what a package owns wants the paths.
		if !strings.HasPrefix(ln, "/") || ln == "/." {
			continue
		}
		out = append(out, ln)
	}
	sort.Strings(out)
	return out, nil
}

func (aptProvider) OwnerOf(c *exec.Context, path string) (string, error) {
	res, err := c.Run(exec.Command{
		Argv: []string{"dpkg-query", "-S", path}, Env: aptEnv(), IgnoreExitCode: true,
	})
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		// A path no package owns is a normal answer: a tree asks this to
		// decide whether it may manage the file.
		return "", nil
	}
	// `pkg: /path`, or `pkg1, pkg2: /path` when the path is diverted; the
	// first package named owns it.
	pkgs, _, ok := strings.Cut(firstLine(res.Stdout), ": ")
	if !ok {
		return "", nil
	}
	first, _, _ := strings.Cut(pkgs, ",")
	return strings.TrimSpace(first), nil
}

func (aptProvider) ListRepos(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{Argv: []string{"apt-get", "indextargets"}, Env: aptEnv()})
	if err != nil {
		return nil, err
	}
	type acc struct {
		m     *value.Map
		comps []string
	}
	var order []string
	seen := map[string]*acc{}
	for _, b := range rfc822Blocks(res.Stdout) {
		// indextargets also carries translation, DEP-11 icon, and
		// command-not-found targets; only the package indexes are repos.
		if b["Created-By"] != "Packages" {
			continue
		}
		uri := b["Repo-URI"]
		if uri == "" {
			uri = b["Base-URI"]
		}
		dist := b["Codename"]
		if dist == "" {
			dist = b["Suite"]
		}
		key := "deb " + uri + " " + dist
		a := seen[key]
		if a == nil {
			m := value.NewMap(6)
			m.Set("type", "deb")
			m.Set("uri", uri)
			m.Set("dist", dist)
			m.Set("architecture", b["Architecture"])
			m.Set("trusted", b["Trusted"] == "yes")
			a = &acc{m: m}
			seen[key] = a
			order = append(order, key)
		}
		if comp := b["Component"]; comp != "" && !containsString(a.comps, comp) {
			a.comps = append(a.comps, comp)
		}
	}
	out := value.NewMap(len(order))
	for _, key := range order {
		a := seen[key]
		sort.Strings(a.comps)
		comps := make([]any, len(a.comps))
		for i, comp := range a.comps {
			comps[i] = comp
		}
		a.m.Set("comps", comps)
		out.Set(key, a.m)
	}
	return out, nil
}

// ---- dnf and yum: the optional interfaces ----
//
// Holding is the `versionlock` plugin, which is not always installed — the
// call fails naming it, rather than silently doing nothing. Everything else
// is `rpm` and the manager's own `repolist`.

func (p dnfProvider) Hold(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{p.binary, "versionlock", "add", name}})
	return err
}

func (p dnfProvider) Unhold(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{p.binary, "versionlock", "delete", name}})
	return err
}

func (p dnfProvider) ListHolds(c *exec.Context) ([]string, error) {
	res, err := c.Run(exec.Command{
		Argv: []string{p.binary, "versionlock", "list", "--quiet"}, IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ln := range strings.Split(res.Stdout, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") || strings.HasPrefix(ln, "Last metadata") {
			continue
		}
		out = append(out, rpmShortName(ln))
	}
	sort.Strings(out)
	return out, nil
}

func (p dnfProvider) Upgrade(c *exec.Context, refresh bool) (*value.Map, error) {
	before, err := p.ListPkgs(c)
	if err != nil {
		return nil, err
	}
	argv := []string{p.binary, "upgrade", "-y", "-q"}
	if !refresh {
		argv = append(argv, "-C")
	}
	if _, err := c.Run(exec.Command{Argv: argv}); err != nil {
		return nil, err
	}
	return pkgDelta(c, p, before)
}

func (p dnfProvider) ListUpgrades(c *exec.Context, refresh bool) (*value.Map, error) {
	argv := []string{p.binary, "--quiet", "list", "--upgrades"}
	if !refresh {
		argv = append(argv, "-C")
	}
	// `list --upgrades` exits 100 when there is anything to report, which
	// is an answer rather than a failure.
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(16)
	for _, ln := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(ln)
		if len(fields) != 3 || !strings.Contains(fields[0], ".") {
			continue
		}
		name, _, _ := strings.Cut(fields[0], ".")
		out.Set(name, strings.TrimPrefix(fields[1], "0:"))
	}
	return out, nil
}

func (dnfProvider) FileList(c *exec.Context, name string) ([]string, error) {
	res, err := c.Run(exec.Command{Argv: []string{"rpm", "-ql", name}})
	if err != nil {
		return nil, err
	}
	lines := sortedLines(res.Stdout)
	if len(lines) == 1 && lines[0] == "(contains no files)" {
		return nil, nil
	}
	return lines, nil
}

func (dnfProvider) OwnerOf(c *exec.Context, path string) (string, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"rpm", "-qf", "--queryformat", "%{NAME}", path},
		IgnoreExitCode: true,
	})
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", nil
	}
	return strings.TrimSpace(firstLine(res.Stdout)), nil
}

func (p dnfProvider) ListRepos(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{p.binary, "--quiet", "repolist", "--all", "--verbose"},
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(8)
	var cur *value.Map
	for _, b := range rfc822Blocks(res.Stdout) {
		id := b["Repo-id"]
		if id == "" {
			continue
		}
		// `-v` writes `baseos` or, on some versions, `baseos                baseos`.
		id, _, _ = strings.Cut(id, " ")
		cur = value.NewMap(6)
		cur.Set("name", b["Repo-name"])
		cur.Set("enabled", b["Repo-status"] == "enabled")
		if v := b["Repo-baseurl"]; v != "" {
			cur.Set("baseurl", v)
		}
		if v := b["Repo-metalink"]; v != "" {
			cur.Set("metalink", v)
		}
		if v := b["Repo-mirrors"]; v != "" {
			cur.Set("mirrors", v)
		}
		out.Set(id, cur)
	}
	return out, nil
}

// ---- apk: the optional interfaces ----
//
// apk can upgrade the world and answer what owns a file. It has no hold in
// the dpkg sense and no command that lists its repositories, so it
// implements neither pkgHolder nor pkgRepos — a caller asking for those
// gets the "the apkpkg provider cannot ..." refusal, not an empty answer.

func (p apkProvider) Upgrade(c *exec.Context, refresh bool) (*value.Map, error) {
	before, err := p.ListPkgs(c)
	if err != nil {
		return nil, err
	}
	argv := []string{"apk", "upgrade", "--no-progress"}
	if refresh {
		argv = append(argv, "--update-cache")
	}
	if _, err := c.Run(exec.Command{Argv: argv}); err != nil {
		return nil, err
	}
	return pkgDelta(c, p, before)
}

func (apkProvider) ListUpgrades(c *exec.Context, refresh bool) (*value.Map, error) {
	if refresh {
		if _, err := c.Run(exec.Command{Argv: []string{"apk", "update", "--no-progress"}}); err != nil {
			return nil, err
		}
	}
	res, err := c.Run(exec.Command{
		Argv: []string{"apk", "version", "-l", "<"}, IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(16)
	for _, ln := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(ln)
		// `pkgname-1.0-r0 < 1.1-r0`; the header line is `Installed:`.
		if len(fields) < 3 || fields[1] != "<" {
			continue
		}
		parts := strings.Split(fields[0], "-")
		if len(parts) < 3 {
			continue
		}
		out.Set(strings.Join(parts[:len(parts)-2], "-"), fields[2])
	}
	return out, nil
}

func (apkProvider) FileList(c *exec.Context, name string) ([]string, error) {
	res, err := c.Run(exec.Command{
		Argv: []string{"apk", "info", "-L", name}, IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ln := range strings.Split(res.Stdout, "\n") {
		ln = strings.TrimSpace(ln)
		// The first line is `pkg-ver contains:`; the rest are paths with no
		// leading slash.
		if ln == "" || strings.HasSuffix(ln, "contains:") {
			continue
		}
		out = append(out, "/"+ln)
	}
	sort.Strings(out)
	return out, nil
}

func (apkProvider) OwnerOf(c *exec.Context, path string) (string, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"apk", "info", "-W", strings.TrimPrefix(path, "/")},
		IgnoreExitCode: true,
	})
	if err != nil {
		return "", err
	}
	// `<path> is owned by <pkg-ver>`, or `<path> is not owned by any package`.
	const marker = " is owned by "
	line := firstLine(res.Stdout)
	i := strings.Index(line, marker)
	if i < 0 {
		return "", nil
	}
	owner := strings.TrimSpace(line[i+len(marker):])
	parts := strings.Split(owner, "-")
	if len(parts) < 3 {
		return owner, nil
	}
	return strings.Join(parts[:len(parts)-2], "-"), nil
}
