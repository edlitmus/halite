package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/edlitmus/halite/internal/grains"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// runtimeFile is the name a node writes its own changes to.
//
// One file, always the same one, and never the operator's: a package
// manager owns `beacons.d/10-base.yaml` and this owns
// `beacons.d/99-runtime.yaml`. The number puts it last, so a change
// made at runtime beats the file it was made against — which is the
// order the person making it expects.
const runtimeFile = "99-runtime.yaml"

// saveRuntimeConfig writes a subsystem's running configuration, so that
// a change made with `beacons.add` or `schedule.add` survives a
// restart.
func (n *node) saveRuntimeConfig(kind string, running *value.Map) (string, error) {
	dir, err := n.runtimeDir(kind)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}

	// The definitions bare, with no `beacons:` above them: a fragment
	// in this directory is a mapping of names, and the directory
	// already says what kind they are.
	encoded := yaml.Encode(running, yaml.EncodeOptions{Indent: 2})
	header := "# Written by halite-node. Runtime changes made with " +
		kind + ".add and its neighbours.\n" +
		"# Edit the files beside this one instead; this is rewritten on every save.\n"

	path := filepath.Join(dir, runtimeFile)
	if err := writeFileAtomic(path, []byte(header+encoded), 0o644); err != nil {
		return "", err
	}
	n.log.Info("runtime configuration saved", "kind", kind, "path", path)
	return path, nil
}

// reloadRuntimeConfig re-reads a subsystem from disk, discarding
// runtime changes that were never saved.
func (n *node) reloadRuntimeConfig(kind string) error {
	switch kind {
	case "schedule":
		if n.reloadSchedule == nil {
			return fmt.Errorf("this node is not running a schedule")
		}
		return n.reloadSchedule()
	case "grains":
		// Re-collect rather than merge the one value in: a grain set by
		// a state has to end up where a collected one would, and the
		// merge order between the static file, `grains.d`, and the
		// configuration is the collector's to decide, not this
		// function's.
		fresh, warnings := grains.Collect(grains.Options{
			NodeID:     n.nodeID,
			StaticFile: n.root + "/grains",
			GrainsDir:  n.root + "/grains.d",
			Extra:      n.cfg.Map("grains"),
			Cloud:      n.cfg.Bool("cloud_grains", false),
		})
		for _, w := range warnings {
			n.log.Warn(w.String(), "component", "grains")
		}
		n.grains = fresh
		return nil
	}
	return fmt.Errorf("%s cannot be re-read", kind)
}

func (n *node) runtimeDir(kind string) (string, error) {
	switch kind {
	case "beacons":
		return n.beaconDir(), nil
	case "schedule":
		return n.scheduleDir(), nil
	case "grains":
		// The node already merges `grains.d/`, so a grain set by a state
		// lands where one set by a package or by hand does, and the
		// numbering puts the runtime file last for the same reason it
		// does for beacons.
		return filepath.Join(n.root, "grains.d"), nil
	}
	return "", fmt.Errorf("%s has no configuration directory", kind)
}

// writeFileAtomic writes through a temporary file in the same
// directory, so that a node interrupted mid-write leaves the previous
// configuration rather than half of the new one.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".halite-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// loadRuntimeConfig reads back what saveRuntimeConfig last wrote, so a
// change to one setting keeps the others in the same file.
//
// An absent file is an empty mapping rather than an error: the first
// grain a node sets for itself is written into a directory that may not
// exist yet, and refusing there would make the first run the only one
// that fails.
func (n *node) loadRuntimeConfig(kind string) (*value.Map, error) {
	dir, err := n.runtimeDir(kind)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, runtimeFile))
	if os.IsNotExist(err) {
		return value.NewMap(0), nil
	}
	if err != nil {
		return nil, err
	}
	parsed, _, err := yaml.Parse(raw, yaml.DefaultOptions(filepath.Join(dir, runtimeFile)))
	if err != nil {
		return nil, err
	}
	m, ok := parsed.(*value.Map)
	if !ok {
		return value.NewMap(0), nil
	}
	return m, nil
}
