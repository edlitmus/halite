package builtin

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// pkgProvider is one package manager. `pkg` is a virtual module: the
// provider is chosen from the node's grains, so an SLS file says
// `pkg.installed` and the right manager runs. SPEC section 15.2.
type pkgProvider interface {
	// Name is the provider's module name, such as "aptpkg".
	Name() string
	// Available reports whether this provider can run on this node.
	Available(c *exec.Context) bool
	// ListPkgs returns installed packages and their versions.
	ListPkgs(c *exec.Context) (*value.Map, error)
	// Install installs or upgrades the named packages. versions maps a
	// package name to a pinned version, and may be empty.
	Install(c *exec.Context, names []string, versions map[string]string, refresh bool) error
	// Remove removes the named packages.
	Remove(c *exec.Context, names []string, purge bool) error
	// LatestVersion reports the newest available version of a package, or
	// an empty string when it is already at the newest.
	LatestVersion(c *exec.Context, name string) (string, error)
	// RefreshDB updates the package metadata.
	RefreshDB(c *exec.Context) error
}

// providers is the registration list, searched in order.
var pkgProviders = []pkgProvider{
	aptProvider{},
	dnfProvider{binary: "dnf"},
	dnfProvider{binary: "yum"},
	pkgngProvider{},
	apkProvider{},
	brewProvider{},
	chocoProvider{},
}

// pickPkgProvider chooses the provider for this node.
func pickPkgProvider(c *exec.Context) (pkgProvider, error) {
	for _, p := range pkgProviders {
		if p.Available(c) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("no package manager was found on this node (%s); halite ships providers for apt, dnf, yum, pkgng, apk, mac_brew_pkg, and chocolatey", runtime.GOOS)
}

func registerPkg(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "list_pkgs",
				Doc:      "Return the installed packages and their versions.",
				Returns:  "a mapping of package name to version",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickPkgProvider(c)
				if err != nil {
					return nil, err
				}
				return p.ListPkgs(c)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "version",
				Doc:      "Return the installed version of a package, or an empty string.",
				Params:   []signature.Param{req("name", signature.String, "The package.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickPkgProvider(c)
				if err != nil {
					return nil, err
				}
				installed, err := p.ListPkgs(c)
				if err != nil {
					return nil, err
				}
				v, _ := installed.Get(states.Str(args, "name", ""))
				if v == nil {
					return "", nil
				}
				return v, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "latest_version",
				Doc:      "Return the newest available version of a package.",
				Params:   []signature.Param{req("name", signature.String, "The package.")},
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
				Module: "pkg", Function: "refresh_db",
				Doc:      "Update the package manager's metadata.",
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickPkgProvider(c)
				if err != nil {
					return nil, err
				}
				if c.Test {
					return true, nil
				}
				return true, p.RefreshDB(c)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "install",
				Doc: "Install packages.",
				Params: []signature.Param{
					opt("name", signature.String, "", "A single package."),
					opt("pkgs", signature.List, nil, "Several packages."),
					opt("version", signature.String, "", "A version to pin."),
					opt("refresh", signature.Bool, false, "Refresh the package metadata first."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickPkgProvider(c)
				if err != nil {
					return nil, err
				}
				names, versions := packageSpecs(args)
				if c.Test {
					return names, nil
				}
				return true, p.Install(c, names, versions, states.Bool(args, "refresh", false))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "remove",
				Doc: "Remove packages.",
				Params: []signature.Param{
					opt("name", signature.String, "", "A single package."),
					opt("pkgs", signature.List, nil, "Several packages."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickPkgProvider(c)
				if err != nil {
					return nil, err
				}
				names, _ := packageSpecs(args)
				if c.Test {
					return names, nil
				}
				return true, p.Remove(c, names, false)
			},
		},
	)

	installedParams := []signature.Param{
		nameParam("The package. Defaults to the state ID."),
		opt("pkgs", signature.List, nil, "Several packages, optionally with pinned versions."),
		opt("version", signature.String, "", "A version to pin."),
		opt("refresh", signature.Bool, false, "Refresh the package metadata before installing."),
	}

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "installed",
				Doc:        "Ensure packages are installed, optionally at a pinned version.",
				Params:     installedParams,
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: pkgInstalled,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "removed",
				Doc: "Ensure packages are not installed.",
				Params: []signature.Param{
					nameParam("The package. Defaults to the state ID."),
					opt("pkgs", signature.List, nil, "Several packages."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: pkgRemoved,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "purged",
				Doc: "Ensure packages are not installed, and take their " +
					"configuration with them.",
				Params: []signature.Param{
					nameParam("The package. Defaults to the state ID."),
					opt("pkgs", signature.List, nil, "Several packages."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: pkgPurged,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "latest",
				Doc:        "Ensure packages are at their newest available version.",
				Params:     installedParams,
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: pkgLatest,
		},
	)
}

// packageSpecs reads the name, pkgs, and version arguments into a name
// list and a pin map. `pkgs` accepts both a plain list and Salt's
// list-of-single-key-mappings form that carries versions.
func packageSpecs(args *value.Map) ([]string, map[string]string) {
	versions := map[string]string{}
	var names []string

	if v, ok := args.Get("pkgs"); ok && v != nil {
		if list, ok := v.([]any); ok {
			for _, item := range list {
				switch t := item.(type) {
				case string:
					names = append(names, t)
				case *value.Map:
					for _, e := range t.Entries() {
						name := value.KeyString(e.Key)
						names = append(names, name)
						if e.Val != nil {
							versions[name] = value.KeyString(e.Val)
						}
					}
				}
			}
		}
	}

	if len(names) == 0 {
		if name := states.Str(args, "name", ""); name != "" {
			names = append(names, name)
			if v := states.Str(args, "version", ""); v != "" {
				versions[name] = v
			}
		}
	} else if v := states.Str(args, "version", ""); v != "" {
		for _, n := range names {
			if _, pinned := versions[n]; !pinned {
				versions[n] = v
			}
		}
	}
	return names, versions
}

func pkgInstalled(c *exec.Context, args *value.Map) (states.Result, error) {
	p, err := pickPkgProvider(c)
	if err != nil {
		return states.False(fmt.Sprintf("No package provider is available: %v", err)), nil
	}
	names, versions := packageSpecs(args)
	if len(names) == 0 {
		return states.False("This state names no packages to install."), nil
	}

	installed, err := p.ListPkgs(c)
	if err != nil {
		return states.False(fmt.Sprintf("The installed package list could not be read: %v", err)), nil
	}

	var missing []string
	changes := value.NewMap(len(names))
	for _, name := range names {
		cur, present := installed.Get(name)
		currentVersion := ""
		if present {
			currentVersion = value.KeyString(cur)
		}
		want, pinned := versions[name]
		switch {
		case !present:
			missing = append(missing, name)
			changes.Set(name, states.Change("", displayVersion(want)))
		case pinned && !versionSatisfies(currentVersion, want):
			missing = append(missing, name)
			changes.Set(name, states.Change(currentVersion, want))
		}
	}

	if len(missing) == 0 {
		return states.True(fmt.Sprintf("All of the requested packages are installed: %s.", states.SortedNames(names))), nil
	}
	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("The following packages would be installed: %s.", states.SortedNames(missing)), changes), nil
	}

	if err := p.Install(c, missing, versions, states.Bool(args, "refresh", false)); err != nil {
		return states.False(fmt.Sprintf("The packages could not be installed: %v", err)), nil
	}

	// The change set is rebuilt from what is actually installed now, so a
	// package manager that pulled a different version than asked reports
	// the version it really installed.
	after, err := p.ListPkgs(c)
	if err == nil {
		for _, name := range missing {
			cur, _ := after.Get(name)
			old, _ := installed.Get(name)
			changes.Set(name, states.Change(value.KeyString(old), value.KeyString(cur)))
		}
	}
	return states.Changed(
		fmt.Sprintf("The following packages were installed: %s.", states.SortedNames(missing)), changes), nil
}

func displayVersion(v string) string {
	if v == "" {
		return "installed"
	}
	return v
}

// versionSatisfies compares an installed version against a requested one.
// A trailing `*` is a prefix match, which is how a Salt tree pins a minor
// series.
func versionSatisfies(installed, want string) bool {
	if want == "" {
		return true
	}
	if strings.HasSuffix(want, "*") {
		return strings.HasPrefix(installed, strings.TrimSuffix(want, "*"))
	}
	return installed == want
}

func pkgRemoved(c *exec.Context, args *value.Map) (states.Result, error) {
	return pkgRemoveOrPurge(c, args, false)
}

// pkgPurged is pkg.removed that also takes the configuration with it.
//
// The provider interface has carried a `purge` flag since it was
// written; only the state was missing, so a tree that purged had to be
// rewritten to merely remove — which leaves the configuration behind and
// is a different outcome, not a smaller one.
func pkgPurged(c *exec.Context, args *value.Map) (states.Result, error) {
	return pkgRemoveOrPurge(c, args, true)
}

func pkgRemoveOrPurge(c *exec.Context, args *value.Map, purge bool) (states.Result, error) {
	verbed := "removed"
	if purge {
		verbed = "purged"
	}
	p, err := pickPkgProvider(c)
	if err != nil {
		return states.False(fmt.Sprintf("No package provider is available: %v", err)), nil
	}
	names, _ := packageSpecs(args)
	if len(names) == 0 {
		return states.False(fmt.Sprintf("This state names no packages to be %s.", verbed)), nil
	}

	installed, err := p.ListPkgs(c)
	if err != nil {
		return states.False(fmt.Sprintf("The installed package list could not be read: %v", err)), nil
	}

	var present []string
	changes := value.NewMap(len(names))
	for _, name := range names {
		if cur, ok := installed.Get(name); ok {
			present = append(present, name)
			changes.Set(name, states.Change(value.KeyString(cur), ""))
		}
	}
	if len(present) == 0 {
		return states.True(fmt.Sprintf("None of the requested packages are installed: %s.", states.SortedNames(names))), nil
	}
	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("The following packages would be %s: %s.", verbed, states.SortedNames(present)), changes), nil
	}
	if err := p.Remove(c, present, purge); err != nil {
		return states.False(fmt.Sprintf("The packages could not be %s: %v", verbed, err)), nil
	}
	return states.Changed(
		fmt.Sprintf("The following packages were %s: %s.", verbed, states.SortedNames(present)), changes), nil
}

func pkgLatest(c *exec.Context, args *value.Map) (states.Result, error) {
	p, err := pickPkgProvider(c)
	if err != nil {
		return states.False(fmt.Sprintf("No package provider is available: %v", err)), nil
	}
	names, _ := packageSpecs(args)
	if len(names) == 0 {
		return states.False("This state names no packages to upgrade."), nil
	}

	installed, err := p.ListPkgs(c)
	if err != nil {
		return states.False(fmt.Sprintf("The installed package list could not be read: %v", err)), nil
	}

	var outdated []string
	changes := value.NewMap(len(names))
	for _, name := range names {
		latest, err := p.LatestVersion(c, name)
		if err != nil {
			return states.False(fmt.Sprintf("The newest version of %s could not be determined: %v", name, err)), nil
		}
		cur, present := installed.Get(name)
		currentVersion := ""
		if present {
			currentVersion = value.KeyString(cur)
		}
		if latest == "" || latest == currentVersion {
			continue
		}
		outdated = append(outdated, name)
		changes.Set(name, states.Change(currentVersion, latest))
	}

	if len(outdated) == 0 {
		return states.True(fmt.Sprintf("All of the requested packages are at their newest version: %s.", states.SortedNames(names))), nil
	}
	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("The following packages would be upgraded: %s.", states.SortedNames(outdated)), changes), nil
	}
	if err := p.Install(c, outdated, nil, true); err != nil {
		return states.False(fmt.Sprintf("The packages could not be upgraded: %v", err)), nil
	}
	return states.Changed(
		fmt.Sprintf("The following packages were upgraded: %s.", states.SortedNames(outdated)), changes), nil
}
