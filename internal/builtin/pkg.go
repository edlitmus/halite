package builtin

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"unicode"

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
//
// zypper is asked before dnf, and the order is load-bearing. openSUSE
// packages dnf -- Leap 16.0's repo-oss offers `dnf` 4.23.0, `dnf5` and
// `yum`, seen on the lab machine -- and a SUSE node that has installed one
// has rpm and dnf on the path, which is everything the dnf provider asks
// for. With dnf first, that node would be managed by a tool reading
// /etc/yum.repos.d, which its own package manager never writes. The
// reverse case is the one this order gets wrong: an EL node with a zypper
// binary installed would pick zypper. That was not measured on any
// machine, and is recorded rather than guarded because no grain is
// available to guard it with (see zypperProvider.Available).
// `TestZypperIsPickedBeforeDnf` holds the order.
var pkgProviders = []pkgProvider{
	aptProvider{},
	zypperProvider{},
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
	return nil, fmt.Errorf("no package manager was found on this node (%s); halite ships providers for apt, zypper, dnf, yum, pkgng, apk, mac_brew_pkg, and chocolatey", runtime.GOOS)
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
				name := states.Str(args, "name", "")
				if err := pkgValidateName(name); err != nil {
					return nil, err
				}
				p, err := pickPkgProvider(c)
				if err != nil {
					return nil, err
				}
				return p.LatestVersion(c, name)
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
				names, versions, err := packageSpecs(args)
				if err != nil {
					return nil, err
				}
				p, err := pickPkgProvider(c)
				if err != nil {
					return nil, err
				}
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
				names, _, err := packageSpecs(args)
				if err != nil {
					return nil, err
				}
				p, err := pickPkgProvider(c)
				if err != nil {
					return nil, err
				}
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
		opt("allow_updates", signature.Bool, false, "Treat a pinned version as a floor rather than an exact match, so a package updated outside halite is left alone."),
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
//
// Every name and every version it returns has been through
// pkgValidateName and pkgValidateVersion, and it is the only way the
// install, remove and purge paths read them -- so a spec that would be
// read as an option by the package manager is refused here, before any
// provider is asked anything. See pkgValidateName for why.
func packageSpecs(args *value.Map) ([]string, map[string]string, error) {
	names, versions := packageSpecsUnchecked(args)
	if err := pkgValidateNames(names); err != nil {
		return nil, nil, err
	}
	for _, n := range names {
		if v, ok := versions[n]; ok {
			if err := pkgValidateVersion(n, v); err != nil {
				return nil, nil, err
			}
		}
	}
	return names, versions, nil
}

func packageSpecsUnchecked(args *value.Map) ([]string, map[string]string) {
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
	allowUpdates := states.Bool(args, "allow_updates", false)
	names, versions, err := packageSpecs(args)
	if err != nil {
		return states.False(err.Error()), nil
	}
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
		case pinned && !versionSatisfies(c, currentVersion, want, allowUpdates):
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
	//
	// And a pin is checked against it, with the same versionSatisfies the
	// decision to install used. Without this, a provider that installed
	// some other version than the pin -- pkgng and apk both did, because
	// neither put the pin on the command line (issue 321, M-33) -- reported
	// success, and the next run saw the same mismatch and "changed" it
	// again, for ever. A state that cannot reach its pin has failed, and
	// says which version it got instead. The two checks being one function
	// is the point: an install that the next run would redo is exactly one
	// that this check refuses.
	after, err := p.ListPkgs(c)
	if err != nil {
		// Presence cannot be checked, so it is neither claimed nor denied.
		// The install reported success; the comment says the list that
		// would have confirmed it could not be read.
		return states.Changed(
			fmt.Sprintf("The following packages were installed: %s. (The package list could not be read back to confirm it: %v.)",
				states.SortedNames(missing), err), changes), nil
	}
	var unmet, absent []string
	for _, name := range missing {
		cur, _ := after.Get(name)
		old, _ := installed.Get(name)
		changes.Set(name, states.Change(value.KeyString(old), value.KeyString(cur)))
		if cur == nil {
			// Nothing was installed under this name, pinned or not. A
			// manager that exits 0 for a held, excluded or filtered
			// package, or one the repository no longer carries, lands
			// here, and the state used to call it Changed: the next run
			// found the package absent and changed it again, for ever
			// (issue 321, section 5).
			absent = append(absent, name)
			continue
		}
		if want, pinned := versions[name]; pinned {
			got := value.KeyString(cur)
			if !versionSatisfies(c, got, want, allowUpdates) {
				unmet = append(unmet, fmt.Sprintf("%s is pinned to %s and %s is installed", name, want, got))
			}
		}
	}
	if len(unmet) > 0 || len(absent) > 0 {
		sort.Strings(unmet)
		sort.Strings(absent)
		var parts []string
		if len(absent) > 0 {
			parts = append(parts, fmt.Sprintf("The package manager reported success but these packages are not installed afterwards: %s.", strings.Join(absent, ", ")))
		}
		if len(unmet) > 0 {
			parts = append(parts, fmt.Sprintf("The package manager did not install the pinned version: %s.", strings.Join(unmet, "; ")))
		}
		res := states.False(strings.Join(parts, " "))
		res.Changes = changes
		return res, nil
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
// versionSatisfies decides whether what is installed meets the pin.
//
// `allowUpdates` turns an exact pin into a floor, which is Salt's
// reading: it allows the package to be updated outside the configuration
// manager's control, so a node may carry a newer version than the
// repository offers without that forcing a re-installation. It
// is what a tree uses for a package that an agent updates itself —
// without it, every run after such an update sees a mismatch and
// reinstalls the older pinned version, fighting the agent once per
// highstate.
//
// A wildcard is already a floor of sorts and is unaffected: `1.2.*`
// means the prefix either way.
func versionSatisfies(c *exec.Context, installed, want string, allowUpdates bool) bool {
	if want == "" {
		return true
	}
	if strings.HasSuffix(want, "*") {
		return strings.HasPrefix(installed, strings.TrimSuffix(want, "*"))
	}
	if installed == want {
		return true
	}
	if rpmReleaseOmitted(c, installed, want) {
		return true
	}
	if allowUpdates {
		return installedIsAtLeast(c, installed, want)
	}
	return false
}

// rpmReleaseOmitted reports whether want names installed's version with
// its release left off, on a node whose packages are RPM's.
//
// rpm reports every package with a release (`1.8.0-10.el9`), and a tree
// pins the version it means (`1.8.0`). Compared exactly the pin was never
// met, so every run installed it again, and since the check after an
// install (M-33) failed every run (issue 321, M-34). Salt's rpm
// version_cmp ignores the release when either side has none, "if e.g. you
// are checking if a package version 3.2 is satisfied by 3.2-1" (Salt
// 3007.1, salt/modules/rpm_lowpkg.py); this is that. The epoch is still
// compared, as Salt compares it unless told ignore_epoch: `1.8.0` does
// not meet `2:1.8.0-10.el9`.
//
// Only for the RPM families by name. versionScheme gives an unknown
// family RPM's ordering, and Alpine's `2.3.2-r0` has the same shape, but
// nothing here showed apk or Salt taking `2.3.2` to mean it.
func rpmReleaseOmitted(c *exec.Context, installed, want string) bool {
	if c == nil || c.Grains == nil || strings.Contains(want, "-") {
		return false
	}
	family := ""
	if v, ok := c.Grains.Get("os_family"); ok {
		family = strings.ToLower(value.KeyString(v))
	}
	switch family {
	case "redhat", "rhel", "suse":
	default:
		return false
	}
	i := strings.LastIndex(installed, "-")
	return i > 0 && installed[:i] == want
}

// installedIsAtLeast orders two versions with the scheme this node's
// family uses, and refuses to guess.
//
// A comparison it cannot make is *not* satisfaction: answering "yes"
// there would leave a pinned package silently un-installed, and
// answering by string comparison would be worse still, since "1.10" sorts
// below "1.9". So an unorderable pair falls back to the exact match the
// caller already tried, which reinstalls -- the conservative half.
func installedIsAtLeast(c *exec.Context, installed, want string) bool {
	family := ""
	if c != nil && c.Grains != nil {
		if v, ok := c.Grains.Get("os_family"); ok {
			family = value.KeyString(v)
		}
	}
	scheme, err := versionScheme("auto", family)
	if err != nil {
		return false
	}
	switch scheme {
	case "debian":
		return CompareDebian(installed, want) >= 0
	case "rpm":
		return CompareRPM(installed, want) >= 0
	case "freebsd":
		got, err := compareFreeBSD(c, installed, want)
		if err != nil {
			return false
		}
		n, ok := got.(int64)
		return ok && n >= 0
	}
	return false
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
	names, _, err := packageSpecs(args)
	if err != nil {
		return states.False(err.Error()), nil
	}
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
	names, _, err := packageSpecs(args)
	if err != nil {
		return states.False(err.Error()), nil
	}
	if len(names) == 0 {
		return states.False("This state names no packages to bring up to date."), nil
	}

	installed, err := p.ListPkgs(c)
	if err != nil {
		return states.False(fmt.Sprintf("The installed package list could not be read: %v", err)), nil
	}

	// Two lists, because this state does two things and used to say it did
	// one. A package that is not installed has no current version, so the
	// comparison below is against the empty string and the state installs
	// it -- which is right, and is what an operator writing `pkg.latest`
	// for a new package expects. It then reported that the package had
	// been *upgraded*. See pkgLatestSentence.
	var toInstall, toUpgrade []string
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
		if present {
			toUpgrade = append(toUpgrade, name)
		} else {
			toInstall = append(toInstall, name)
		}
		changes.Set(name, states.Change(currentVersion, latest))
	}

	if len(toInstall)+len(toUpgrade) == 0 {
		return states.True(fmt.Sprintf("All of the requested packages are at their newest version: %s.", states.SortedNames(names))), nil
	}
	// One call for both: `Install` upgrades a package that is present and
	// installs one that is not, which is why the split above is about what
	// to *say* rather than about what to do.
	acting := append(append([]string{}, toInstall...), toUpgrade...)
	if c.Test {
		return states.WouldChange(
			pkgLatestSentence("would be installed", "would be upgraded", toInstall, toUpgrade), changes), nil
	}
	if err := p.Install(c, acting, nil, true); err != nil {
		return states.False(fmt.Sprintf("The packages could not be installed or upgraded: %v", err)), nil
	}
	return states.Changed(
		pkgLatestSentence("were installed", "were upgraded", toInstall, toUpgrade), changes), nil
}

// pkgLatestSentence says which of the two things `pkg.latest` is about to do,
// or has done.
//
// # Why one sentence was not enough
//
// It said "The following packages would be upgraded" for every package it
// acted on, including ones that were not installed and which it was about to
// install. The change set was right all along -- `{old: "", new: "1.2"}`
// names the absence -- so nothing that reads the changes could see the
// discrepancy, and only a person reading the comment could. That is the shape
// of defect this project keeps finding in prose rather than in code: the
// machine-readable half was correct, so no test disagreed.
//
// The phrases are passed in rather than derived from `c.Test`, so that the
// tense lives at the call site next to the result it describes and this
// function has no opinion about which run it is in.
func pkgLatestSentence(installPhrase, upgradePhrase string, toInstall, toUpgrade []string) string {
	switch {
	case len(toInstall) == 0:
		return fmt.Sprintf("The following packages %s: %s.",
			upgradePhrase, states.SortedNames(toUpgrade))
	case len(toUpgrade) == 0:
		return fmt.Sprintf("The following packages %s: %s.",
			installPhrase, states.SortedNames(toInstall))
	}
	return fmt.Sprintf("The following packages %s: %s; and these %s: %s.",
		installPhrase, states.SortedNames(toInstall),
		upgradePhrase, states.SortedNames(toUpgrade))
}

// pkgValidateName refuses a package name that a package manager would
// read as something other than a package name.
//
// # Why
//
// Every provider builds its argv as `tool verb [options] name...`, and a
// name comes from an SLS file, from pillar, or from a template rendered
// with either -- which is to say from whoever can write any of those, not
// from whoever wrote the provider. Nothing stood between that string and
// the argv. `pkgs: ["-oDpkg::Pre-Invoke::=touch /tmp/pwned", curl]` made
// the apt provider run
//
//	apt-get install -y -q -o ... -o ... "-oDpkg::Pre-Invoke::=touch /tmp/pwned" curl
//
// and apt-get parses that element as its own `-o`, which sets a dpkg hook
// that runs as root. dnf's `--setopt=` and `--installroot=` are the same
// door in another tool. There is no shell anywhere in that path; argv
// alone is enough, because the injection is into the package manager's
// option parser, not into a shell's.
//
// Two paths in this package already disagreed with this one about it:
// `dnf_module` refuses a leading `-` by regex for exactly this reason
// ("these specs arrive from pillar and templates"), and `chattr` puts
// `--` before its operand. The main pkg path did neither.
//
// # What is refused, and why only that
//
//   - The empty string: there is no package with no name, and an empty
//     element is not something any caller meant.
//   - A leading `-`: this is the defect. In the argv halite builds --
//     options first, operands after, no `--` -- such an element is read
//     by the tool's option parser before it could be read as a package,
//     so a name spelled that way could never have been installed as a
//     package by these providers anyway. Refusing it removes nothing
//     that worked.
//   - Whitespace, control characters (NUL included) and Unicode format
//     characters anywhere: no manager's name grammar contains them; in
//     argv they are not dangerous to the tool by themselves, but they are
//     what makes a refused name *look* like an accepted one in a log or a
//     state comment (a newline, a carriage return, a right-to-left
//     override), and NUL cannot be passed through execve at all.
//
// It deliberately does **not** impose one positive grammar, because the
// managers do not share one and a grammar strict enough for Debian would
// refuse legitimate names elsewhere: FreeBSD origins (`www/nginx`) and
// flavours (`py311-foo@py311`), Homebrew's `openssl@3` and tap paths,
// dnf's `name.arch`, globs and `@group`, apt's `name:arch`, Chocolatey's
// mixed case and dots. A narrower check per manager would be a claim
// about each manager's grammar that nobody here has measured; refusing
// the characters that are dangerous or that no manager can carry is a
// claim that needs no measurement.
//
// What this does not stop is a *legitimate-looking* operand that a
// manager treats specially -- dnf's local paths and URLs, apt's
// `name-` (remove) suffix, `name/release`. Those are package-manager
// features a tree may use on purpose, and they are not option
// injection.
//
// `dnf_module` calls this before its own narrower positive grammar for
// module specs, `dnfModuleSpec`, so that a module spec is refused for
// everything a package name is, whatever that grammar later admits.
func pkgValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("a package name is empty")
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("the package name %q begins with \"-\", which the package manager would read as an option rather than a package; refused", name)
	}
	if r, ok := pkgUnsafeRune(name); ok {
		return fmt.Errorf("the package name %q contains %U, and no package manager's names contain whitespace or control characters; refused", name, r)
	}
	return nil
}

// pkgValidateVersion is pkgValidateName for a pinned version.
//
// A version reaches argv in two shapes. Glued to its name -- apt's and
// zypper's `name=version`, dnf's `name-version` -- it cannot become an
// option, because the element starts with the (already validated) name.
// As an element of its own -- Chocolatey's `--version <v>`, and
// `pkg version -t <a> <b>` on FreeBSD -- it can, and there it is read
// as an option before it could be read as a version. The rule is the
// same as for names, and is applied everywhere rather than only where it
// happens to matter today:
// the shape is the provider's choice, and a provider can change it
// without anybody revisiting this.
func pkgValidateVersion(name, version string) error {
	if version == "" {
		return nil
	}
	if strings.HasPrefix(version, "-") {
		return fmt.Errorf("the version %q pinned for %s begins with \"-\", which the package manager would read as an option; refused", version, name)
	}
	if r, ok := pkgUnsafeRune(version); ok {
		return fmt.Errorf("the version %q pinned for %s contains %U, and no version scheme contains whitespace or control characters; refused", version, name, r)
	}
	return nil
}

// pkgValidateNames applies pkgValidateName to each of names.
func pkgValidateNames(names []string) error {
	for _, n := range names {
		if err := pkgValidateName(n); err != nil {
			return err
		}
	}
	return nil
}

// pkgUnsafeRune finds the first rune no package name or version may hold.
// Invalid UTF-8 decodes as U+FFFD, which is neither space nor control,
// and is let through for the same reason non-ASCII letters are: no
// measurement says every manager refuses it.
func pkgUnsafeRune(s string) (rune, bool) {
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return r, true
		}
	}
	return 0, false
}
