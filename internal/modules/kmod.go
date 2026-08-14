package modules

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func init() {
	register("kmod.present", kmodPresent)
	register("kmod.absent", kmodAbsent)
}

// kmodBackend is one platform's kernel module commands.
type kmodBackend struct {
	name        string
	loaded      func(module string) bool
	load        func(module string) error
	unload      func(module string) error
	persistFile string // where a module is listed to load at boot
	persistLine func(module string) string
}

// detectKmodBackend picks the module tooling for this platform. Kernel
// modules are the least portable thing in the module set: Linux and
// FreeBSD have them, and nothing else here does.
func detectKmodBackend() (*kmodBackend, error) {
	switch runtime.GOOS {
	case "linux":
		return &kmodBackend{
			name: "modprobe",
			loaded: func(module string) bool {
				return kmodListed(readFile("/proc/modules"), module, 0)
			},
			load:        func(module string) error { _, err := pkgRun("modprobe", module); return err },
			unload:      func(module string) error { _, err := pkgRun("modprobe", "-r", module); return err },
			persistFile: "/etc/modules-load.d/halite.conf",
			persistLine: func(module string) string { return module },
		}, nil
	case "freebsd":
		return &kmodBackend{
			name: "kldload",
			loaded: func(module string) bool {
				_, ok := pkgQuery("kldstat", "-q", "-n", module)
				return ok
			},
			load:        func(module string) error { _, err := pkgRun("kldload", module); return err },
			unload:      func(module string) error { _, err := pkgRun("kldunload", module); return err },
			persistFile: "/boot/loader.conf",
			persistLine: func(module string) string { return module + `_load="YES"` },
		}, nil
	}
	return nil, fmt.Errorf("kernel modules are not implemented on %s", runtime.GOOS)
}

// kmodListed reports whether a module appears in the given field of any
// line — /proc/modules lists the name first.
func kmodListed(content, module string, field int) bool {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) > field && normalizeKmod(fields[field]) == normalizeKmod(module) {
			return true
		}
	}
	return false
}

// normalizeKmod folds the two spellings of a module name: /proc/modules
// prints underscores where modprobe accepts dashes.
func normalizeKmod(name string) string {
	return strings.ReplaceAll(strings.TrimSuffix(name, ".ko"), "-", "_")
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// kmodPresent loads a kernel module, and with `persist` also arranges for
// it to load at boot.
//
//	nfs:
//	  kmod.present:
//	    - persist: true
func kmodPresent(c *Ctx, id string, args map[string]any) Result {
	return kmodState(c, id, args, true)
}

// kmodAbsent unloads a kernel module and, with `persist`, stops it loading
// at boot.
func kmodAbsent(c *Ctx, id string, args map[string]any) Result {
	return kmodState(c, id, args, false)
}

func kmodState(c *Ctx, id string, args map[string]any, want bool) Result {
	be, err := detectKmodBackend()
	if err != nil {
		return resFail("%v", err)
	}
	module := Str(args, "name", id)
	persist := Bool(args, "persist", false)

	loadedNow := be.loaded(module)
	persistPath := be.persistFile
	if override := Str(args, "config", ""); override != "" {
		persistPath = override
	}
	persistedNow := persist && kmodPersisted(readFile(persistPath), be.persistLine(module))

	needLoad := loadedNow != want
	needPersist := persist && persistedNow != want
	if !needLoad && !needPersist {
		return resOK(fmt.Sprintf("module %s is %s", module, kmodStateWord(want)))
	}
	if c.Test {
		return resWould(fmt.Sprintf("module %s would be %s", module, kmodStateWord(want)))
	}

	changes := map[string]string{}
	if needLoad {
		action := be.load
		if !want {
			action = be.unload
		}
		if err := action(module); err != nil {
			return resFail("%s %s: %v", be.name, module, err)
		}
		changes[module] = kmodStateWord(want)
	}
	if needPersist {
		updated, err := kmodPersistFile(persistPath, be.persistLine(module), want)
		if err != nil {
			return resFail("%v", err)
		}
		changes[persistPath] = updated
	}
	return resChanged(fmt.Sprintf("module %s is now %s", module, kmodStateWord(want)), changes)
}

func kmodStateWord(loaded bool) string {
	if loaded {
		return "loaded"
	}
	return "unloaded"
}

// kmodPersisted reports whether the boot configuration already carries the
// line for a module.
func kmodPersisted(content, line string) bool {
	for _, existing := range strings.Split(content, "\n") {
		if strings.TrimSpace(existing) == line {
			return true
		}
	}
	return false
}

// kmodPersistFile adds or removes a module's boot line, leaving the rest of
// the file alone — /boot/loader.conf in particular belongs to the host, not
// to halite.
func kmodPersistFile(path, line string, want bool) (string, error) {
	lines := splitLines([]byte(readFile(path)))
	if want {
		if kmodPersisted(strings.Join(lines, "\n"), line) {
			return "unchanged", nil
		}
		lines = append(lines, line)
	} else {
		kept := make([]string, 0, len(lines))
		for _, existing := range lines {
			if strings.TrimSpace(existing) != line {
				kept = append(kept, existing)
			}
		}
		if len(kept) == len(lines) {
			return "unchanged", nil
		}
		lines = kept
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := atomicWrite(path, joinLines(lines), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	if want {
		return "added", nil
	}
	return "removed", nil
}
