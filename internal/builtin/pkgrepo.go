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

// pkgrepo, SPEC sections 15.2 and 15.5.
//
// A package repository is the one thing a tree has to declare before
// `pkg.installed` can work at all, and it existed here as neither an
// execution module nor a state — so an estate whose tree manages an apt
// source could not be migrated, whatever else was built. Five references
// in the estate's own tree.
//
// Virtual, the way `pkg` and `service` are: the SLS file says
// `pkgrepo.managed` and the provider for this node's package manager
// runs. The providers share `pkg`'s selection, so a node that installs
// with apt manages repositories with apt and there is no second way for
// the two to disagree about what this machine is.
//
// # What a repository is named
//
// Salt keys a Debian repository on the *line* — `deb http://… noble
// main` — because that is what ends up in sources.list, and then has to
// reconstruct which file it lives in. Here the name is the name: a short
// identifier that becomes `sources.list.d/<name>.list` or
// `yum.repos.d/<name>.repo`, with the line built from the fields. That
// is the difference between a state that can be read and one that has to
// be parsed.
//
// A tree migrating from Salt writes the line as the name, so a name that
// looks like one is taken apart and its pieces used, with the file named
// after the URI's host. `pkgrepo.managed` therefore accepts both spellings
// and neither is a special case at the provider.

// repoProvider is one package manager's repository configuration.
type repoProvider interface {
	// Name is the provider's module name, matching the pkg provider's.
	Name() string
	// Available reports whether this provider can run on this node.
	Available(c *exec.Context) bool
	// Get returns one repository's configuration, or nil when it is not
	// configured. A provider that cannot tell an absent repository from
	// an unreadable one returns an error instead.
	Get(c *exec.Context, name string) (*value.Map, error)
	// List returns every repository this provider manages, keyed by name.
	List(c *exec.Context) (*value.Map, error)
	// Matches reports whether the configured repository already is what
	// the declaration says, so that a state can decide without writing.
	//
	// The provider answers rather than the state comparing two mappings,
	// because only the provider knows which declared fields it stores. A
	// declaration carries `gpgcheck` for every platform and apt has no
	// such concept, so a comparison made outside the provider found a
	// difference on every run and the state never converged.
	Matches(c *exec.Context, name string, config *value.Map) (bool, error)
	// Set writes a repository, creating or replacing it. It reports
	// whether anything changed.
	Set(c *exec.Context, name string, config *value.Map) (changed bool, err error)
	// Delete removes a repository. Removing one that is not there is not
	// an error.
	Delete(c *exec.Context, name string) error
}

// repoProviders is the registration list, searched in order.
var repoProviders = []repoProvider{
	aptRepoProvider{},
	yumRepoProvider{},
	chocoRepoProvider{},
}

// pickRepoProvider chooses the provider for this node.
func pickRepoProvider(c *exec.Context) (repoProvider, error) {
	for _, p := range repoProviders {
		if p.Available(c) {
			return p, nil
		}
	}
	names := make([]string, 0, len(repoProviders))
	for _, p := range repoProviders {
		names = append(names, p.Name())
	}
	return nil, fmt.Errorf(
		"no package manager whose repositories this build can manage was found on this node (%s); "+
			"halite ships providers for %s",
		runtime.GOOS, strings.Join(names, ", "))
}

func registerPkgRepo(r *Registries) {
	repoName := req("name", signature.String,
		"The repository's short name, which becomes its file. A full Debian "+
			"source line is accepted too, for a tree migrating from Salt.")

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "pkgrepo", Function: "list_repos",
				Doc:      "Return every repository this node's package manager is configured with.",
				Returns:  "a mapping of name to configuration",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickRepoProvider(c)
				if err != nil {
					return nil, err
				}
				return p.List(c)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pkgrepo", Function: "get_repo",
				Doc:      "Return one repository's configuration, or nothing when it is not configured.",
				Params:   []signature.Param{repoName},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickRepoProvider(c)
				if err != nil {
					return nil, err
				}
				got, err := p.Get(c, states.Str(args, "name", ""))
				if err != nil {
					return nil, err
				}
				if got == nil {
					// An empty mapping rather than an error: "is it
					// configured" is a question a template asks, and a
					// failure would make the template have to guard.
					return value.NewMap(0), nil
				}
				return got, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pkgrepo", Function: "mod_repo",
				Doc:        "Write a repository, creating or replacing it.",
				Params:     append([]signature.Param{repoName}, repoFields()...),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickRepoProvider(c)
				if err != nil {
					return nil, err
				}
				if c.Test {
					return true, nil
				}
				name, config, err := repoFromArgs(args)
				if err != nil {
					return nil, err
				}
				changed, err := p.Set(c, name, config)
				if err != nil {
					return nil, err
				}
				if changed && states.Bool(args, "refresh", true) {
					if err := refreshAfterRepoChange(c); err != nil {
						return nil, err
					}
				}
				return changed, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pkgrepo", Function: "del_repo",
				Doc: "Remove a repository. Removing one that is not there is not an error.",
				Params: []signature.Param{
					repoName,
					opt("refresh", signature.Bool, true,
						"Refresh the package metadata afterwards."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickRepoProvider(c)
				if err != nil {
					return nil, err
				}
				if c.Test {
					return true, nil
				}
				name := repoKey(states.Str(args, "name", ""))
				if err := p.Delete(c, name); err != nil {
					return nil, err
				}
				if states.Bool(args, "refresh", true) {
					if err := refreshAfterRepoChange(c); err != nil {
						return nil, err
					}
				}
				return true, nil
			},
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "pkgrepo", Function: "managed",
				Doc: "Ensure a package repository is configured as declared.",
				Params: append([]signature.Param{
					nameParam("The repository. Defaults to the state ID."),
				}, repoFields()...),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: pkgrepoManaged,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "pkgrepo", Function: "absent",
				Doc: "Ensure a package repository is not configured.",
				Params: []signature.Param{
					nameParam("The repository. Defaults to the state ID."),
					opt("refresh", signature.Bool, true,
						"Refresh the package metadata after a removal."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: pkgrepoAbsent,
		},
	)
}

// repoFields are the arguments that describe a repository.
//
// One set for every provider, because a state file is written once and
// applied to whatever the node is. A field a provider has no use for is
// ignored by it rather than refused here: `dist` means nothing to a
// `.repo` file, and a tree that sets it for its Debian nodes should not
// fail on its RHEL ones.
func repoFields() []signature.Param {
	return []signature.Param{
		opt("humanname", signature.String, "",
			"What the package manager shows. RHEL requires one and takes the name when unset."),
		opt("baseurl", signature.String, "", "The repository's URL."),
		opt("dist", signature.String, "",
			"Debian: the suite or codename, as in `noble` or `stable`."),
		opt("comps", signature.List, nil,
			"Debian: the components, as in `main` and `universe`."),
		opt("type", signature.String, "deb",
			"Debian: `deb` for packages or `deb-src` for sources."),
		opt("architectures", signature.List, nil,
			"Debian: restrict the source to these architectures."),
		opt("signedby", signature.String, "",
			"Debian: the keyring file that signs this repository."),
		opt("gpgcheck", signature.Bool, true,
			"RHEL: verify package signatures. Leaving this on is the point of a repository "+
				"having a key at all."),
		opt("gpgkey", signature.String, "", "RHEL: the URL of the signing key."),
		opt("enabled", signature.Bool, true, "Whether the package manager uses it."),
		opt("priority", signature.Int, int64(0),
			"Chocolatey: lower numbers are consulted first. Zero leaves it unset."),
		opt("refresh", signature.Bool, true,
			"Refresh the package metadata after a change."),
	}
}

// repoKey normalises what a state named into the key a provider stores.
//
// A Salt tree names a Debian repository by its whole source line. That
// is a name nobody can put in a filename, so the host of its URI is used
// instead — `deb https://apt.example.com/ubuntu noble main` becomes
// `apt.example.com`, which is what an operator listing
// `/etc/apt/sources.list.d/` would want to see.
func repoKey(name string) string {
	trimmed := strings.TrimSpace(name)
	if !looksLikeSourceLine(trimmed) {
		return trimmed
	}
	fields := strings.Fields(trimmed)
	for _, f := range fields[1:] {
		if strings.Contains(f, "://") {
			_, rest, _ := strings.Cut(f, "://")
			host, _, _ := strings.Cut(rest, "/")
			if host != "" {
				return sanitiseRepoName(host)
			}
		}
	}
	return sanitiseRepoName(trimmed)
}

// looksLikeSourceLine reports whether a name is a Debian source line
// rather than a short name.
func looksLikeSourceLine(name string) bool {
	fields := strings.Fields(name)
	if len(fields) < 2 {
		return false
	}
	switch fields[0] {
	case "deb", "deb-src":
		return true
	}
	return false
}

// sanitiseRepoName makes a string usable as a filename.
func sanitiseRepoName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// repoFromArgs reads a declaration into the shape a provider takes,
// taking a Salt-style source line apart when that is what it was given.
func repoFromArgs(args *value.Map) (string, *value.Map, error) {
	raw := states.Str(args, "name", "")
	if strings.TrimSpace(raw) == "" {
		return "", nil, fmt.Errorf("pkgrepo needs a name")
	}

	config := value.NewMap(10)
	setIfGiven := func(key string, v any, given bool) {
		if given {
			config.Set(key, v)
		}
	}

	kind := states.Str(args, "type", "deb")
	baseurl := states.Str(args, "baseurl", "")
	dist := states.Str(args, "dist", "")
	comps := states.Strings(args, "comps")

	if looksLikeSourceLine(raw) {
		// `deb [arch=amd64] https://… noble main universe`. The fields a
		// tree stated explicitly win over the ones parsed out of the
		// line, because a state that says both means the explicit one.
		parsedType, parsedURL, parsedDist, parsedComps, parsedOpts := parseSourceLine(raw)
		if kind == "deb" && parsedType != "" {
			kind = parsedType
		}
		if baseurl == "" {
			baseurl = parsedURL
		}
		if dist == "" {
			dist = parsedDist
		}
		if len(comps) == 0 {
			comps = parsedComps
		}
		for k, v := range parsedOpts {
			if _, stated := args.Get(k); !stated {
				config.Set(k, v)
			}
		}
	}

	config.Set("type", kind)
	config.Set("baseurl", baseurl)
	config.Set("dist", dist)
	if len(comps) > 0 {
		config.Set("comps", stringsToAny(comps))
	}
	config.Set("enabled", states.Bool(args, "enabled", true))
	config.Set("gpgcheck", states.Bool(args, "gpgcheck", true))

	_, hasHuman := args.Get("humanname")
	setIfGiven("humanname", states.Str(args, "humanname", ""), hasHuman)
	_, hasKey := args.Get("gpgkey")
	setIfGiven("gpgkey", states.Str(args, "gpgkey", ""), hasKey)
	_, hasSigned := args.Get("signedby")
	setIfGiven("signedby", states.Str(args, "signedby", ""), hasSigned)
	if arches := states.Strings(args, "architectures"); len(arches) > 0 {
		config.Set("architectures", stringsToAny(arches))
	}
	if p := states.Int(args, "priority", 0); p != 0 {
		config.Set("priority", p)
	}

	if baseurl == "" {
		return "", nil, fmt.Errorf(
			"pkgrepo %q needs a baseurl, or a name that is a full source line", raw)
	}
	return repoKey(raw), config, nil
}

// parseSourceLine takes a Debian source line apart.
func parseSourceLine(line string) (kind, url, dist string, comps []string, opts map[string]any) {
	opts = map[string]any{}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", "", "", nil, opts
	}
	kind = fields[0]
	rest := fields[1:]

	// `[arch=amd64 signed-by=/usr/share/keyrings/x.gpg]`, which may be
	// several fields once split on spaces.
	if len(rest) > 0 && strings.HasPrefix(rest[0], "[") {
		var inside []string
		for len(rest) > 0 {
			f := rest[0]
			rest = rest[1:]
			inside = append(inside, strings.Trim(f, "[]"))
			if strings.HasSuffix(f, "]") {
				break
			}
		}
		for _, opt := range inside {
			k, v, found := strings.Cut(opt, "=")
			if !found {
				continue
			}
			switch k {
			case "arch":
				opts["architectures"] = stringsToAny(strings.Split(v, ","))
			case "signed-by":
				opts["signedby"] = v
			case "trusted":
				opts["trusted"] = v == "yes"
			}
		}
	}
	if len(rest) > 0 {
		url = rest[0]
		rest = rest[1:]
	}
	if len(rest) > 0 {
		dist = rest[0]
		comps = rest[1:]
	}
	return kind, url, dist, comps, opts
}

func stringsToAny(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// refreshAfterRepoChange updates the package metadata through the `pkg`
// provider, so that a `pkg.installed` after a `pkgrepo.managed` in the
// same run can see what the new repository offers.
//
// Through `pkg` rather than through the repository provider, because
// refreshing is a package-manager operation and there is already one
// implementation of it per manager. A node with no package provider is
// not an error here: the repository was still written, and saying the
// whole state failed because the metadata could not be refreshed would
// be reporting the wrong thing.
func refreshAfterRepoChange(c *exec.Context) error {
	p, err := pickPkgProvider(c)
	if err != nil {
		return nil
	}
	return p.RefreshDB(c)
}

// pkgrepoManaged ensures a repository is configured as declared.
func pkgrepoManaged(c *exec.Context, args *value.Map) (states.Result, error) {
	p, err := pickRepoProvider(c)
	if err != nil {
		return states.False(err.Error()), nil
	}
	name, declared, err := repoFromArgs(args)
	if err != nil {
		return states.False(err.Error()), nil
	}

	matches, err := p.Matches(c, name, declared)
	if err != nil {
		return states.False(fmt.Sprintf("The repository %s could not be read: %v", name, err)), nil
	}
	if matches {
		return states.True(fmt.Sprintf("%s is already configured as declared.", name)), nil
	}
	current, err := p.Get(c, name)
	if err != nil {
		return states.False(fmt.Sprintf("The repository %s could not be read: %v", name, err)), nil
	}

	changes := value.NewMap(1)
	changes.Set("repo", states.Change(repoWas(current), "as declared"))
	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("%s would be configured for %s.", name, p.Name()), changes), nil
	}
	if _, err := p.Set(c, name, declared); err != nil {
		return states.False(fmt.Sprintf("%s could not be configured: %v", name, err)), nil
	}
	if states.Bool(args, "refresh", true) {
		if err := refreshAfterRepoChange(c); err != nil {
			// The repository is written; only the metadata refresh
			// failed, and saying the state failed would be reporting the
			// wrong thing. A warning carries it without the lie.
			res := states.Changed(fmt.Sprintf("%s was configured.", name), changes)
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("the package metadata could not be refreshed: %v", err))
			return res, nil
		}
	}
	return states.Changed(fmt.Sprintf("%s was configured.", name), changes), nil
}

func repoWas(current *value.Map) string {
	if current == nil {
		return "absent"
	}
	return "configured differently"
}

// pkgrepoAbsent ensures a repository is not configured.
func pkgrepoAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
	p, err := pickRepoProvider(c)
	if err != nil {
		return states.False(err.Error()), nil
	}
	name := repoKey(states.Str(args, "name", ""))
	if name == "" {
		return states.False("pkgrepo.absent needs a name."), nil
	}

	current, err := p.Get(c, name)
	if err != nil {
		return states.False(fmt.Sprintf("The repository %s could not be read: %v", name, err)), nil
	}
	if current == nil {
		return states.True(fmt.Sprintf("%s is not configured.", name)), nil
	}

	changes := value.NewMap(1)
	changes.Set("repo", states.Change("configured", "absent"))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be removed.", name), changes), nil
	}
	if err := p.Delete(c, name); err != nil {
		return states.False(fmt.Sprintf("%s could not be removed: %v", name, err)), nil
	}
	if states.Bool(args, "refresh", true) {
		_ = refreshAfterRepoChange(c)
	}
	return states.Changed(fmt.Sprintf("%s was removed.", name), changes), nil
}
