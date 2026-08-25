package extension

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Store is the extension cache: `<dir>/<name>/<version>`.
//
// SPEC 24.4 puts it at `/var/lib/halite/ext/<name>/<version>`, and the
// layout is the point rather than the path: an extension is identified
// by name and version everywhere — in a pin, in the cache, and in
// `sys.list_extensions` — so the three cannot disagree about which
// bundle is meant.
type Store struct {
	// Dir is the cache root.
	Dir string
	// Options are how every bundle in it is verified.
	Options LoadOptions
	// Pins fixes each extension by name. An extension with no pin is
	// loaded at whatever version the cache holds, which an estate
	// should not do and which is permitted so a pin can be added after
	// the extension works.
	Pins map[string]Pin
}

// Installed is one bundle in the cache, loaded or refused.
//
// A refusal is carried rather than dropped: an operator running
// `sys.list_extensions` after a failed highstate needs to see that the
// extension is there and why it did not load, not an empty list.
type Installed struct {
	Name    string
	Version string
	Dir     string
	// Bundle is nil when the bundle was refused.
	Bundle *Bundle
	// Err is why it was refused.
	Err error
}

// Load reads every bundle in the cache.
//
// Every version present, not only the pinned one: a node that has
// fetched a new version and pinned the old one should be able to say
// so. The caller picks which to use with `Usable`.
func (s *Store) Load() ([]Installed, error) {
	if s.Dir == "" {
		return nil, nil
	}
	names, err := subdirectories(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			// No cache is not an error: a node with no extensions is
			// the normal case and the common one.
			return nil, nil
		}
		return nil, err
	}

	var out []Installed
	for _, name := range names {
		versions, err := subdirectories(filepath.Join(s.Dir, name))
		if err != nil {
			return nil, err
		}
		for _, version := range versions {
			dir := filepath.Join(s.Dir, name, version)
			opts := s.Options
			opts.Pin = s.Pins[name]
			bundle, err := Load(dir, opts)
			out = append(out, Installed{
				Name: name, Version: version, Dir: dir,
				Bundle: bundle, Err: err,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version < out[j].Version
	})
	return out, nil
}

// Usable picks one bundle per extension name.
//
// The pinned version when there is a pin, and the only loadable one
// otherwise. Two loadable versions with no pin is refused rather than
// guessed at: "the newest" needs a version ordering this project does
// not have, and picking wrong means running code the estate did not
// choose.
func (s *Store) Usable(installed []Installed) (map[string]*Bundle, []error) {
	byName := map[string][]Installed{}
	for _, item := range installed {
		byName[item.Name] = append(byName[item.Name], item)
	}

	out := map[string]*Bundle{}
	var problems []error
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		var loaded []Installed
		for _, item := range byName[name] {
			if item.Bundle != nil {
				loaded = append(loaded, item)
			}
		}
		switch len(loaded) {
		case 0:
			for _, item := range byName[name] {
				problems = append(problems, item.Err)
			}
		case 1:
			out[name] = loaded[0].Bundle
		default:
			versions := make([]string, 0, len(loaded))
			for _, item := range loaded {
				versions = append(versions, item.Version)
			}
			problems = append(problems, fmt.Errorf(
				"%s is installed at %s and nothing says which to use; pin it",
				name, strings.Join(versions, " and ")))
		}
	}
	return out, problems
}

func subdirectories(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}
