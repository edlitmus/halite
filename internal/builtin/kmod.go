package builtin

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The kmod module: kernel modules, loaded and persisted.
//
// SPEC names no `kmod` anywhere, and plan.md §6 carried that as a reason
// not to build it. The reason did not survive contact with a real tree:
// an estate's CIS controls unload the uncommon network protocols --
// `dccp`, `sctp`, `tipc`, `rds` -- with one `kmod.absent`, and not
// naming a module in a specification is no reason to leave a fleet's
// hardening uncompilable. Agreed on 2026-09-15; plan.md §2.6.
//
// Linux only. FreeBSD's kldload/kldunload is a different model with
// different names and a different persistence file, and inventing a
// mapping between them here would be this build deciding what a tree
// meant. It is declared for Linux and refused elsewhere by name.

// modulesConf is where a persisted module is written.
//
// Salt picks `/etc/modules-load.d/salt_managed.conf` when the systemd
// grain is present and `/etc/modules` otherwise, and this picks the same
// two places for the same reason -- systemd reads the drop-in directory
// and Debian's own init reads `/etc/modules`. The file is named for this
// build rather than for Salt: it records what halite persisted, and a
// file called `salt_managed.conf` written by something that is not Salt
// would be a lie to the next person reading the directory.
// The two paths are variables so a test can point them at a temporary
// directory, as HostsPath is for the same reason.
var (
	ModulesPath     = "/etc/modules"
	ModulesLoadPath = "/etc/modules-load.d/halite_managed.conf"
)

func modulesConf(c *exec.Context) string {
	if c != nil && c.Grains != nil {
		if v, ok := c.Grains.Get("systemd"); ok && v != nil {
			return ModulesLoadPath
		}
	}
	return ModulesPath
}

func registerKmod(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "kmod", Function: "available",
				Doc:      "Every kernel module this machine could load.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				mods, err := availableModules(c)
				if err != nil {
					return nil, err
				}
				return toAnyList(mods), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "kmod", Function: "check_available",
				Doc:      "Whether one module could be loaded.",
				Params:   []signature.Param{req("mod", signature.String, "The module name.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				mods, err := availableModules(c)
				if err != nil {
					return nil, err
				}
				return containsString(mods, normaliseModuleName(states.Str(args, "mod", ""))), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "kmod", Function: "lsmod",
				Doc:      "The loaded modules, with their size, use count, and dependants.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return lsmodEntries(c)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "kmod", Function: "mod_list",
				Doc: "The names of the loaded modules, or of the persisted ones.",
				Params: []signature.Param{
					opt("only_persist", signature.Bool, false, "List what is written to persist instead."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				mods, err := modList(c, states.Bool(args, "only_persist", false))
				if err != nil {
					return nil, err
				}
				return toAnyList(mods), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "kmod", Function: "is_loaded",
				Doc:      "Whether one module is loaded now.",
				Params:   []signature.Param{req("mod", signature.String, "The module name.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				mods, err := modList(c, false)
				if err != nil {
					return nil, err
				}
				return containsString(mods, normaliseModuleName(states.Str(args, "mod", ""))), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "kmod", Function: "load",
				Doc: "Load a module, and optionally write it where it will be loaded again at boot.",
				Params: []signature.Param{
					req("mod", signature.String, "The module name."),
					opt("persist", signature.Bool, false, "Also write it to the modules configuration."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				changed, err := loadModule(c, states.Str(args, "mod", ""), states.Bool(args, "persist", false))
				if err != nil {
					return nil, err
				}
				return toAnyList(changed), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "kmod", Function: "remove",
				Doc: "Unload a module, and optionally stop it being loaded at boot.",
				Params: []signature.Param{
					req("mod", signature.String, "The module name."),
					opt("persist", signature.Bool, false, "Also take it out of the modules configuration."),
					opt("comment", signature.Bool, true,
						"Comment the line out rather than delete it, so the record of what was there survives."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				changed, err := removeModule(c, states.Str(args, "mod", ""),
					states.Bool(args, "persist", false), states.Bool(args, "comment", true))
				if err != nil {
					return nil, err
				}
				return toAnyList(changed), nil
			},
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "kmod", Function: "present",
				Doc: "Ensure kernel modules are loaded.",
				Params: []signature.Param{
					nameParam("The module. A placeholder when `mods` is given."),
					opt("mods", signature.List, nil, "Several modules. When given, `name` is not used."),
					opt("persist", signature.Bool, false, "Also write them where they will be loaded at boot."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: kmodPresent,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "kmod", Function: "absent",
				Doc: "Ensure kernel modules are not loaded.",
				Params: []signature.Param{
					nameParam("The module. A placeholder when `mods` is given."),
					opt("mods", signature.List, nil, "Several modules. When given, `name` is not used."),
					opt("persist", signature.Bool, false, "Also stop them being loaded at boot."),
					opt("comment", signature.Bool, true,
						"Comment the line out rather than delete it, so the record of what was there survives."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: kmodAbsent,
		},
	)
}

// requestedModules reads Salt's two spellings. `mods` is the real
// argument and `name` is a placeholder when it is present -- the estate's
// own tree says exactly that in a comment beside the state:
//
//   - name: modules_to_unload   # not used, just a placeholder
//   - mods: [dccp, sctp, ...]
//
// `name` alone is still a module, because that is the single-module form
// Salt's own documentation shows.
func requestedModules(args *value.Map) []string {
	if mods := states.Strings(args, "mods"); len(mods) > 0 {
		out := make([]string, 0, len(mods))
		for _, m := range mods {
			if m = normaliseModuleName(m); m != "" {
				out = append(out, m)
			}
		}
		return out
	}
	if name := normaliseModuleName(states.Str(args, "name", "")); name != "" {
		return []string{name}
	}
	return nil
}

// normaliseModuleName strips the parameters a modules file may carry --
// `bonding mode=4 miimon=1000` names the module `bonding` -- and settles
// the hyphen-or-underscore question the kernel leaves open.
func normaliseModuleName(mod string) string {
	mod = strings.TrimSpace(mod)
	if i := strings.IndexAny(mod, " \t"); i >= 0 {
		mod = mod[:i]
	}
	return strings.ReplaceAll(mod, "-", "_")
}

func kmodUnsupported() (states.Result, bool) {
	if runtime.GOOS != "linux" {
		return states.False(fmt.Sprintf(
			"kmod manages Linux kernel modules and this node runs %s; "+
				"FreeBSD's kldload is a different model and this build does not map one onto the other.",
			runtime.GOOS)), true
	}
	return states.Result{}, false
}

func kmodPresent(c *exec.Context, args *value.Map) (states.Result, error) {
	if res, stop := kmodUnsupported(); stop {
		return res, nil
	}
	mods := requestedModules(args)
	if len(mods) == 0 {
		return states.False("This state needs a module name, or a list in `mods`."), nil
	}
	persist := states.Bool(args, "persist", false)

	loaded, err := modList(c, false)
	if err != nil {
		return states.False(fmt.Sprintf("the loaded modules could not be read: %v", err)), nil
	}
	if persist {
		// With persist, "present" means loaded *and* written down, so a
		// module that is loaded but not persisted still has work to do.
		persisted, err := modList(c, true)
		if err != nil {
			return states.False(fmt.Sprintf("%s could not be read: %v", modulesConf(c), err)), nil
		}
		loaded = intersect(loaded, persisted)
	}

	todo := subtract(mods, loaded)
	if len(todo) == 0 {
		return states.True(fmt.Sprintf("%s already loaded.", modulePhrase(mods))), nil
	}

	changes := value.NewMap(len(todo))
	for _, m := range todo {
		changes.Set(m, states.Change(nil, "loaded"))
	}
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be loaded.", modulePhrase(todo)), changes), nil
	}
	for _, m := range todo {
		if _, err := loadModule(c, m, persist); err != nil {
			return states.False(fmt.Sprintf("%s could not be loaded: %v", m, err)), nil
		}
	}
	return states.Changed(fmt.Sprintf("%s loaded.", modulePhrase(todo)), changes), nil
}

func kmodAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
	if res, stop := kmodUnsupported(); stop {
		return res, nil
	}
	mods := requestedModules(args)
	if len(mods) == 0 {
		return states.False("This state needs a module name, or a list in `mods`."), nil
	}
	persist := states.Bool(args, "persist", false)

	loaded, err := modList(c, false)
	if err != nil {
		return states.False(fmt.Sprintf("the loaded modules could not be read: %v", err)), nil
	}
	if persist {
		// With persist, a module counts as present if it is loaded *or*
		// written down: unloading it without taking it out of the file
		// passes a scan today and fails the same scan after a reboot,
		// which is the failure this control exists to prevent.
		persisted, err := modList(c, true)
		if err != nil {
			return states.False(fmt.Sprintf("%s could not be read: %v", modulesConf(c), err)), nil
		}
		loaded = union(loaded, persisted)
	}

	todo := intersect(mods, loaded)
	if len(todo) == 0 {
		return states.True(fmt.Sprintf("%s already absent.", modulePhrase(mods))), nil
	}

	changes := value.NewMap(len(todo))
	for _, m := range todo {
		changes.Set(m, states.Change("loaded", "removed"))
	}
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be removed.", modulePhrase(todo)), changes), nil
	}
	comment := states.Bool(args, "comment", true)
	for _, m := range todo {
		if _, err := removeModule(c, m, persist, comment); err != nil {
			return states.False(fmt.Sprintf("%s could not be removed: %v", m, err)), nil
		}
	}
	return states.Changed(fmt.Sprintf("%s removed.", modulePhrase(todo)), changes), nil
}

// modulePhrase names one module or several, so a comment reads as a
// sentence either way.
func modulePhrase(mods []string) string {
	if len(mods) == 1 {
		return "Kernel module " + mods[0] + " is"
	}
	return "Kernel modules " + strings.Join(mods, ", ") + " are"
}

// ---- the machine ----

// availableModules lists what this kernel could load, from the module
// directory for the running release: the built-in list, plus every .ko
// under it.
func availableModules(c *exec.Context) ([]string, error) {
	release, err := kernelRelease(c)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join("/lib/modules", release)
	seen := map[string]bool{}

	if f, err := os.Open(filepath.Join(dir, "modules.builtin")); err == nil {
		s := bufio.NewScanner(f)
		for s.Scan() {
			base := filepath.Base(strings.TrimSpace(s.Text()))
			seen[normaliseModuleName(strings.TrimSuffix(base, ".ko"))] = true
		}
		f.Close()
	}

	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil //nolint:nilerr // an unreadable subtree is not an answer, it is one fewer module
		}
		name := info.Name()
		if i := strings.Index(name, ".ko"); i > 0 {
			seen[normaliseModuleName(name[:i])] = true
		}
		return nil
	})

	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out, nil
}

// kernelRelease is the running kernel's release, which names the module
// directory. The grain already holds it, and asking the kernel a second
// time would be a second answer that could disagree with the first.
func kernelRelease(c *exec.Context) (string, error) {
	if c != nil && c.Grains != nil {
		if v, ok := c.Grains.Get("kernelrelease"); ok {
			if s := strings.TrimSpace(value.KeyString(v)); s != "" {
				return s, nil
			}
		}
	}
	body, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return "", fmt.Errorf("the kernel release could not be read: %w", err)
	}
	return strings.TrimSpace(string(body)), nil
}

// lsmodEntries reads /proc/modules rather than running lsmod.
//
// lsmod is a formatter over that file and nothing else, so parsing its
// columns would be parsing a rendering of something already available in
// a stable, documented format -- and it puts a binary between this and an
// answer the kernel is offering directly.
func lsmodEntries(c *exec.Context) (any, error) {
	f, err := os.Open("/proc/modules")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := []any{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 3 {
			continue
		}
		entry := value.MapOf(
			"module", fields[0],
			"size", fields[1],
			"depcount", fields[2],
		)
		deps := []any{}
		if len(fields) > 3 && fields[3] != "-" {
			for _, d := range strings.Split(strings.TrimSuffix(fields[3], ","), ",") {
				if d = strings.TrimSpace(d); d != "" {
					deps = append(deps, d)
				}
			}
		}
		entry.Set("deps", deps)
		out = append(out, entry)
	}
	return out, s.Err()
}

func modList(c *exec.Context, onlyPersist bool) ([]string, error) {
	if onlyPersist {
		return persistedModules(c)
	}
	entries, err := lsmodEntries(c)
	if err != nil {
		return nil, err
	}
	list, _ := entries.([]any)
	out := make([]string, 0, len(list))
	for _, e := range list {
		m, ok := e.(*value.Map)
		if !ok {
			continue
		}
		if name, ok := m.Get("module"); ok {
			out = append(out, value.KeyString(name))
		}
	}
	sort.Strings(out)
	return out, nil
}

// persistedModules reads the modules configuration. A commented line is
// not persisted, which is what makes `comment: True` a real removal.
func persistedModules(c *exec.Context) ([]string, error) {
	f, err := os.Open(modulesConf(c))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if name := normaliseModuleName(line); name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, s.Err()
}

func loadModule(c *exec.Context, mod string, persist bool) ([]string, error) {
	mod = normaliseModuleName(mod)
	if mod == "" {
		return nil, fmt.Errorf("kmod.load needs a module name")
	}
	before, err := modList(c, false)
	if err != nil {
		return nil, err
	}
	if c.Which("modprobe") == "" {
		return nil, fmt.Errorf("modprobe was not found on this node")
	}
	if c.Test {
		// The states above check test mode before they get here; the
		// execution functions `kmod.load` and `kmod.remove` did not, and
		// a dry run of either loaded or unloaded the module. Checked in
		// the shared helper so that both paths and any future caller get
		// it, and after the `modprobe` lookup so that a node without
		// modprobe still says so rather than predicting a change it
		// could not make.
		//
		// Found on the lab's Alpine and Ubuntu rows: a FreeBSD host
		// cannot reach this code at all, so the audit that caught it
		// could only catch it on Linux.
		return subtract([]string{mod}, before), nil
	}
	res, err := c.Run(exec.Command{Argv: []string{"modprobe", mod}, Env: exec.CleanEnv()})
	if err != nil {
		return nil, fmt.Errorf("modprobe %s: %w: %s", mod, err, firstLine(res.Stderr))
	}
	after, err := modList(c, false)
	if err != nil {
		return nil, err
	}
	changed := subtract(after, before)
	if persist {
		added, err := persistModule(c, mod)
		if err != nil {
			return nil, err
		}
		changed = union(changed, added)
	}
	return changed, nil
}

func removeModule(c *exec.Context, mod string, persist, comment bool) ([]string, error) {
	mod = normaliseModuleName(mod)
	if mod == "" {
		return nil, fmt.Errorf("kmod.remove needs a module name")
	}
	before, err := modList(c, false)
	if err != nil {
		return nil, err
	}
	var changed []string
	if containsString(before, mod) {
		// modprobe -r rather than rmmod, which Salt uses: rmmod refuses a
		// module with dependants and leaves them loaded, and a control
		// that unloads `sctp` means its dependants too.
		if c.Which("modprobe") == "" {
			return nil, fmt.Errorf("modprobe was not found on this node")
		}
		if c.Test {
			// See loadModule: the state form checked and the execution
			// form did not. The prediction is the module itself, because
			// what `modprobe -r` also unloads cannot be known without
			// running it -- and naming only what was asked for is the
			// honest half of that.
			return subtract([]string{mod}, nil), nil
		}
		res, err := c.Run(exec.Command{Argv: []string{"modprobe", "-r", mod}, Env: exec.CleanEnv()})
		if err != nil {
			return nil, fmt.Errorf("modprobe -r %s: %w: %s", mod, err, firstLine(res.Stderr))
		}
		after, err := modList(c, false)
		if err != nil {
			return nil, err
		}
		changed = subtract(before, after)
	}
	if persist {
		removed, err := unpersistModule(c, mod, comment)
		if err != nil {
			return nil, err
		}
		changed = union(changed, removed)
	}
	return changed, nil
}

// persistModule writes a module to the configuration, uncommenting the
// line if it is already there commented out.
func persistModule(c *exec.Context, mod string) ([]string, error) {
	conf := modulesConf(c)
	persisted, err := persistedModules(c)
	if err != nil {
		return nil, err
	}
	if containsString(persisted, mod) {
		return nil, nil
	}
	if c.Test {
		// Reached only from a caller that did not check, which is what
		// this pair of fixes is about -- but the write is here, so the
		// guard belongs here too rather than only at the callers.
		return []string{mod}, nil
	}
	lines, err := confLines(conf)
	if err != nil {
		return nil, err
	}
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") &&
			normaliseModuleName(strings.TrimLeft(strings.TrimSpace(line), "# \t")) == mod {
			lines[i] = mod
			return []string{mod}, writeConf(conf, lines)
		}
	}
	return []string{mod}, writeConf(conf, append(lines, mod))
}

// unpersistModule takes a module out of the configuration, by commenting
// its line or by deleting it.
func unpersistModule(c *exec.Context, mod string, comment bool) ([]string, error) {
	conf := modulesConf(c)
	lines, err := confLines(conf)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(lines))
	found := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || normaliseModuleName(trimmed) != mod {
			out = append(out, line)
			continue
		}
		found = true
		if comment {
			out = append(out, "# "+line)
		}
	}
	if !found {
		return nil, nil
	}
	if c.Test {
		// The same reasoning as persistModule: the write is here, so the
		// guard is here.
		return []string{mod}, nil
	}
	return []string{mod}, writeConf(conf, out)
}

func confLines(path string) ([]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	text := strings.TrimSuffix(string(body), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

func writeConf(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	return writeAtomic(path, []byte(body), 0o644)
}

// ---- small set helpers, order preserved from the first argument ----

func intersect(a, b []string) []string {
	var out []string
	for _, x := range a {
		if containsString(b, x) {
			out = append(out, x)
		}
	}
	return out
}

func subtract(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !containsString(b, x) {
			out = append(out, x)
		}
	}
	return out
}

func union(a, b []string) []string {
	out := append([]string{}, a...)
	for _, x := range b {
		if !containsString(out, x) {
			out = append(out, x)
		}
	}
	return out
}
