package main

import (
	"fmt"
	"os"
	"path/filepath"

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
	}
	return fmt.Errorf("%s cannot be re-read", kind)
}

func (n *node) runtimeDir(kind string) (string, error) {
	switch kind {
	case "beacons":
		return n.beaconDir(), nil
	case "schedule":
		return n.scheduleDir(), nil
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
