package modules

import (
	"fmt"
	"strings"
)

func init() {
	register("alternatives.install", alternativesInstall)
	register("alternatives.remove", alternativesRemove)
	register("alternatives.set", alternativesSet)
}

// alternativesTool is the command this distribution calls the alternatives
// system: Debian ships update-alternatives, RHEL ships alternatives.
func alternativesTool() (string, error) {
	for _, name := range []string{"update-alternatives", "alternatives"} {
		if has(name) {
			return name, nil
		}
	}
	return "", fmt.Errorf("no alternatives command found (update-alternatives or alternatives)")
}

// alternativesInstall registers a candidate for a link group.
//
//	editor:
//	  alternatives.install:
//	    - link: /usr/bin/editor
//	    - path: /usr/bin/vim
//	    - priority: 100
func alternativesInstall(c *Ctx, id string, args map[string]any) Result {
	tool, err := alternativesTool()
	if err != nil {
		return resFail("%v", err)
	}
	name := Str(args, "name", id)
	link := Str(args, "link", "")
	path := Str(args, "path", "")
	priority := Str(args, "priority", "")
	if link == "" || path == "" || priority == "" {
		return resFail("alternatives.install needs link, path, and priority")
	}

	query, _ := pkgQuery(tool, "--query", name)
	if alternativeHasPath(query, path) {
		return resOK(fmt.Sprintf("%s already offers %s", name, path))
	}
	if c.Test {
		return resWould(fmt.Sprintf("%s would offer %s at priority %s", name, path, priority))
	}
	if _, err := pkgRun(tool, "--install", link, name, path, priority); err != nil {
		return resFail("%s --install: %v", tool, err)
	}
	return resChanged(fmt.Sprintf("%s now offers %s", name, path),
		map[string]string{name: "installed " + path})
}

// alternativesRemove withdraws a candidate from a link group.
func alternativesRemove(c *Ctx, id string, args map[string]any) Result {
	tool, err := alternativesTool()
	if err != nil {
		return resFail("%v", err)
	}
	name := Str(args, "name", id)
	path := Str(args, "path", "")
	if path == "" {
		return resFail("alternatives.remove needs a path")
	}

	query, _ := pkgQuery(tool, "--query", name)
	if !alternativeHasPath(query, path) {
		return resOK(fmt.Sprintf("%s does not offer %s", name, path))
	}
	if c.Test {
		return resWould(fmt.Sprintf("%s would stop offering %s", name, path))
	}
	if _, err := pkgRun(tool, "--remove", name, path); err != nil {
		return resFail("%s --remove: %v", tool, err)
	}
	return resChanged(fmt.Sprintf("%s no longer offers %s", name, path),
		map[string]string{name: "removed " + path})
}

// alternativesSet chooses which candidate a link group points at, which
// also takes the group out of automatic mode.
func alternativesSet(c *Ctx, id string, args map[string]any) Result {
	tool, err := alternativesTool()
	if err != nil {
		return resFail("%v", err)
	}
	name := Str(args, "name", id)
	path := Str(args, "path", "")
	if path == "" {
		return resFail("alternatives.set needs a path")
	}

	query, _ := pkgQuery(tool, "--query", name)
	if alternativeCurrent(query) == path {
		return resOK(fmt.Sprintf("%s already points at %s", name, path))
	}
	if !alternativeHasPath(query, path) {
		return resFail("%s does not offer %s; install it first", name, path)
	}
	if c.Test {
		return resWould(fmt.Sprintf("%s would point at %s", name, path))
	}
	if _, err := pkgRun(tool, "--set", name, path); err != nil {
		return resFail("%s --set: %v", tool, err)
	}
	return resChanged(fmt.Sprintf("%s points at %s", name, path),
		map[string]string{name: "set to " + path})
}

// alternativeCurrent reads the selected path out of `--query` output:
//
//	Name: editor
//	Link: /usr/bin/editor
//	Status: manual
//	Best: /usr/bin/vim
//	Value: /usr/bin/vim
func alternativeCurrent(query string) string {
	return alternativeField(query, "Value:")
}

// alternativeHasPath reports whether a candidate is registered, reading the
// `Alternative:` lines the query prints for each one.
func alternativeHasPath(query, path string) bool {
	for _, line := range strings.Split(query, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "Alternative:" {
			continue
		}
		if fields[1] == path {
			return true
		}
	}
	return false
}

func alternativeField(query, field string) string {
	for _, line := range strings.Split(query, "\n") {
		if rest, found := strings.CutPrefix(strings.TrimSpace(line), field); found {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}
