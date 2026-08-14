package modules

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func init() {
	register("pip.installed", pipInstalled)
	register("pip.removed", pipRemoved)
	register("virtualenv.managed", virtualenvManaged)
}

// pipCommand is the pip to call. `bin_env` names a virtualenv (or the pip
// inside one), which is how a state installs into an application's
// environment rather than the system's.
func pipCommand(args map[string]any) ([]string, error) {
	binEnv := Str(args, "bin_env", "")
	if binEnv == "" {
		for _, candidate := range []string{"pip3", "pip"} {
			if has(candidate) {
				return []string{candidate}, nil
			}
		}
		return nil, fmt.Errorf("no pip found (install python3-pip, or set bin_env)")
	}
	if info, err := os.Stat(binEnv); err == nil && info.IsDir() {
		pip := filepath.Join(binEnv, "bin", "pip")
		if _, err := os.Stat(pip); err != nil {
			return nil, fmt.Errorf("%s is not a virtualenv (no %s)", binEnv, pip)
		}
		return []string{pip}, nil
	}
	if _, err := os.Stat(binEnv); err != nil {
		return nil, fmt.Errorf("bin_env %s does not exist", binEnv)
	}
	return []string{binEnv}, nil
}

// pipSpec is one requirement as a state names it: a name, and the version
// constraint attached to it if any.
type pipSpec struct {
	full string
	name string
}

// parsePipSpec splits "django==4.2" into the name pip lists it under and
// the whole spec to install.
func parsePipSpec(raw string) pipSpec {
	name := raw
	if i := strings.IndexAny(raw, "=<>!~[ ;"); i >= 0 {
		name = raw[:i]
	}
	return pipSpec{full: raw, name: strings.TrimSpace(name)}
}

// normalizePipName folds the spellings pip treats as one package: names
// are case-insensitive and `-`, `_`, `.` are equivalent.
func normalizePipName(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.NewReplacer("_", "-", ".", "-").Replace(lower)
}

// pipInstalledSet reads `pip freeze` into name -> version.
func pipInstalledSet(out string) map[string]string {
	installed := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		name, version, found := strings.Cut(line, "==")
		if !found {
			// Editable installs and direct URLs have no == form; record the
			// line under itself so a name match still works.
			installed[normalizePipName(line)] = ""
			continue
		}
		installed[normalizePipName(name)] = strings.TrimSpace(version)
	}
	return installed
}

// pipDrift reports which of the wanted specs are missing or at the wrong
// version.
func pipDrift(specs []pipSpec, installed map[string]string) []pipSpec {
	var missing []pipSpec
	for _, spec := range specs {
		version, present := installed[normalizePipName(spec.name)]
		if !present {
			missing = append(missing, spec)
			continue
		}
		// Only an exact pin can be checked without reimplementing PEP 440;
		// anything looser is left to pip, which is what Salt does too.
		if pinned, found := strings.CutPrefix(strings.TrimPrefix(spec.full, spec.name), "=="); found {
			if strings.TrimSpace(pinned) != version {
				missing = append(missing, spec)
			}
		}
	}
	return missing
}

// pipInstalled installs Python packages, into a virtualenv when the state
// names one.
//
//	app requirements:
//	  pip.installed:
//	    - bin_env: /opt/app/venv
//	    - requirements: /opt/app/requirements.txt
//
//	django:
//	  pip.installed:
//	    - name: django==4.2
func pipInstalled(c *Ctx, id string, args map[string]any) Result {
	pip, err := pipCommand(args)
	if err != nil {
		return resFail("%v", err)
	}
	requirements := Str(args, "requirements", "")
	upgrade := Bool(args, "upgrade", false)

	// A requirements file is pip's to read: halite does not parse it, so
	// the state cannot know what is missing without asking pip.
	if requirements != "" {
		if _, err := os.Stat(requirements); err != nil {
			return resFail("requirements %s: %v", requirements, err)
		}
		return pipRequirements(c, pip, requirements, upgrade)
	}

	var specs []pipSpec
	for _, raw := range pipNames(id, args) {
		specs = append(specs, parsePipSpec(raw))
	}
	if len(specs) == 0 {
		return resFail("pip.installed needs a package, pkgs, or requirements")
	}

	frozen := pipFreeze(pip)
	missing := pipDrift(specs, pipInstalledSet(frozen))
	if len(missing) == 0 && !upgrade {
		return resOK(fmt.Sprintf("all packages already installed (%s)", specNames(specs)))
	}
	if upgrade && len(missing) == 0 {
		// pip decides whether an upgrade is available, so the state has to
		// ask it and compare what it froze before and after.
		return pipRun(c, pip, specs, true, frozen)
	}
	return pipRun(c, pip, missing, upgrade, frozen)
}

// pipRun installs specs and reports what actually moved, by comparing what
// pip froze before and after.
func pipRun(c *Ctx, pip []string, specs []pipSpec, upgrade bool, before string) Result {
	if c.Test {
		return resWould(fmt.Sprintf("would install with pip: %s", specNames(specs)))
	}
	argv := append(append([]string{}, pip...), "install")
	if upgrade {
		argv = append(argv, "--upgrade")
	}
	for _, spec := range specs {
		argv = append(argv, spec.full)
	}
	if out, err := pkgRun(argv...); err != nil {
		return resFail("pip install: %v: %s", err, strings.TrimSpace(out))
	}
	after := pipFreeze(pip)
	changes := pipChanges(pipInstalledSet(before), pipInstalledSet(after))
	if len(changes) == 0 {
		return resOK(fmt.Sprintf("pip made no change (%s)", specNames(specs)))
	}
	return resChanged(fmt.Sprintf("installed with pip: %s", specNames(specs)), changes)
}

// pipRequirements installs from a requirements file. pip is the only thing
// that knows whether the file is satisfied, so the state asks it and
// reports the difference.
func pipRequirements(c *Ctx, pip []string, path string, upgrade bool) Result {
	before := pipFreeze(pip)
	if c.Test {
		// --dry-run is pip 22.2 and later; where it is missing the state
		// says what it would do rather than guessing at the outcome.
		argv := append(append([]string{}, pip...), "install", "--dry-run", "-r", path)
		if out, err := pkgRun(argv...); err == nil {
			if strings.Contains(out, "Would install") {
				return resWould(fmt.Sprintf("%s: %s", path, firstLineContaining(out, "Would install")))
			}
			return resOK(fmt.Sprintf("%s is satisfied", path))
		}
		return resWould(fmt.Sprintf("would install from %s", path))
	}

	argv := append(append([]string{}, pip...), "install")
	if upgrade {
		argv = append(argv, "--upgrade")
	}
	argv = append(argv, "-r", path)
	if out, err := pkgRun(argv...); err != nil {
		return resFail("pip install -r %s: %v: %s", path, err, strings.TrimSpace(out))
	}
	after := pipFreeze(pip)
	changes := pipChanges(pipInstalledSet(before), pipInstalledSet(after))
	if len(changes) == 0 {
		return resOK(fmt.Sprintf("%s is satisfied", path))
	}
	return resChanged(fmt.Sprintf("installed from %s", path), changes)
}

// pipRemoved uninstalls Python packages.
func pipRemoved(c *Ctx, id string, args map[string]any) Result {
	pip, err := pipCommand(args)
	if err != nil {
		return resFail("%v", err)
	}
	names := pipNames(id, args)
	if len(names) == 0 {
		return resFail("pip.removed needs a package or pkgs")
	}
	installed := pipInstalledSet(pipFreeze(pip))

	var present []string
	for _, raw := range names {
		if _, ok := installed[normalizePipName(parsePipSpec(raw).name)]; ok {
			present = append(present, parsePipSpec(raw).name)
		}
	}
	if len(present) == 0 {
		return resOK(fmt.Sprintf("all packages already absent (%s)", strings.Join(names, ", ")))
	}
	if c.Test {
		return resWould(fmt.Sprintf("would uninstall with pip: %s", strings.Join(present, ", ")))
	}
	argv := append(append(append([]string{}, pip...), "uninstall", "-y"), present...)
	if out, err := pkgRun(argv...); err != nil {
		return resFail("pip uninstall: %v: %s", err, strings.TrimSpace(out))
	}
	changes := map[string]string{}
	for _, name := range present {
		changes[name] = "uninstalled"
	}
	return resChanged(fmt.Sprintf("uninstalled with pip: %s", strings.Join(present, ", ")), changes)
}

// virtualenvManaged creates a Python virtual environment, and installs a
// requirements file into it when the state names one.
//
//	/opt/app/venv:
//	  virtualenv.managed:
//	    - requirements: /opt/app/requirements.txt
func virtualenvManaged(c *Ctx, id string, args map[string]any) Result {
	path := Str(args, "name", id)
	python := Str(args, "python", "python3")
	requirements := Str(args, "requirements", "")

	pip := filepath.Join(path, "bin", "pip")
	_, err := os.Stat(pip)
	exists := err == nil

	if !exists {
		if c.Test {
			return resWould(fmt.Sprintf("virtualenv %s would be created", path))
		}
		if !has(python) {
			return resFail("%s not found", python)
		}
		if out, err := pkgRun(python, "-m", "venv", path); err != nil {
			return resFail("%s -m venv %s: %v: %s", python, path, err, strings.TrimSpace(out))
		}
	}
	if requirements == "" {
		if exists {
			return resOK(fmt.Sprintf("virtualenv %s exists", path))
		}
		return resChanged(fmt.Sprintf("virtualenv %s created", path),
			map[string]string{path: "created"})
	}

	// The environment is there; the requirements are pip.installed's job,
	// against this environment's own pip.
	inner := map[string]any{"requirements": requirements, "bin_env": path}
	res := pipInstalled(c, id, inner)
	if !exists && res.Ok {
		res.Changed = true
		if res.Changes == nil {
			res.Changes = map[string]string{}
		}
		res.Changes[path] = "created"
		res.Comment = fmt.Sprintf("virtualenv %s created; %s", path, res.Comment)
	}
	return res
}

// pipNames is the packages a state names, by `pkgs` or by `name`.
func pipNames(id string, args map[string]any) []string {
	if pkgs := List(args, "pkgs"); len(pkgs) > 0 {
		return pkgs
	}
	if name := Str(args, "name", id); name != "" {
		return []string{name}
	}
	return nil
}

func specNames(specs []pipSpec) string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.full)
	}
	return strings.Join(names, ", ")
}

// pipChanges is what moved between two freezes.
func pipChanges(before, after map[string]string) map[string]string {
	changes := map[string]string{}
	for name, version := range after {
		old, existed := before[name]
		switch {
		case !existed:
			changes[name] = "installed " + version
		case old != version:
			changes[name] = old + " -> " + version
		}
	}
	for name := range before {
		if _, still := after[name]; !still {
			changes[name] = "uninstalled"
		}
	}
	return changes
}

// pipFreeze asks pip what is installed. A pip that cannot answer leaves
// the set empty, which reads as "nothing installed" — the state then tries
// to install and reports pip's own error.
func pipFreeze(pip []string) string {
	out, _ := pkgQuery(append(pip, "freeze")...)
	return out
}

func firstLineContaining(out, want string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, want) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
