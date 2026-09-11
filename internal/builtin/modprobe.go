package builtin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// ProcModulesPath is the kernel's list of loaded modules. A variable so
// a test can point it at a fixture.
var ProcModulesPath = "/proc/modules"

// ModulesLoadDir is where a module name written here is loaded by
// systemd-modules-load.service at boot. A variable so a test can
// redirect it.
var ModulesLoadDir = "/etc/modules-load.d"

// ModProbeDir holds the denylist and options files modprobe itself
// reads. A variable so a test can redirect it.
var ModProbeDir = "/etc/modprobe.d"

// registerModprobe installs the `modprobe` module of SPEC 15.3's Common
// Linux row.
//
// # Reading needs no root; /proc/modules is a fixed table
//
// `list` reads /proc/modules directly rather than shelling to `lsmod`,
// which only reformats the same file for a person. Its columns are
// fixed and documented in `Documentation/filesystems/proc.rst`: name,
// size, use count, a comma list of dependent modules or `-`, load
// state, and address. `info` parses `modinfo`, whose `label:` lines are
// a closed vocabulary the way `mdadm --detail`'s are; a `parm:` or
// `alias:` line can repeat, so those come back as lists rather than
// being overwritten by the last one.
//
// # Persistence is two files, because the kernel keeps two separate
// # ideas of "this module matters"
//
// Loading a module now is `modprobe`; loading it at every boot is a
// name in `/etc/modules-load.d/<name>.conf`, which is
// systemd-modules-load.service's own input format — one module per
// line, nothing else. Refusing to load one at all is a *different*
// file, `/etc/modprobe.d/denylist-<name>.conf`, because modprobe reads
// the directive that does that — still spelled the old way in every
// modprobe.conf(5) — from every `.conf` in that directory regardless of
// name. `allowlist` removes only the file this module would have
// written; a stock denylist file such as Debian's own firewire one is
// left alone, because deleting a file this module did not create is
// not what an operator asking to allow one module back means.
//
// # There is deliberately no `modprobe` state
//
// SPEC 15.5 names one for none of `pam`, `journald`, `mdadm` or this,
// and Salt's own `kmod` state is one of the "modules SPEC never
// planned for" that plan.md §6 leaves as a decision for a person rather
// than something to build unasked.
func registerModprobe(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "modprobe", Function: "list",
				Doc:       "Return every loaded module, its size, use count and what depends on it.",
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				mods, err := modprobeReadModules()
				if err != nil {
					return nil, err
				}
				return modprobeListValue(mods), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "modprobe", Function: "is_loaded",
				Doc: "Report whether a module is currently loaded.",
				Params: []signature.Param{
					req("name", signature.String, "The module's name."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				name := strings.TrimSpace(states.Str(args, "name", ""))
				mods, err := modprobeReadModules()
				if err != nil {
					return nil, err
				}
				_, ok := mods[name]
				return ok, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "modprobe", Function: "info",
				Doc: "Return a module's metadata: version, description, license, dependencies, aliases and parameters.",
				Params: []signature.Param{
					req("name", signature.String, "The module's name."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: modprobeInfoFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "modprobe", Function: "is_denylisted",
				Doc: "Report whether a module is denylisted, and which file says so.",
				Params: []signature.Param{
					req("name", signature.String, "The module's name."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				name := strings.TrimSpace(states.Str(args, "name", ""))
				if name == "" {
					return nil, errors.New("a module must be named")
				}
				file, ok := modprobeDenylistedIn(name)
				out := value.NewMap(2)
				out.Set("denylisted", ok)
				out.Set("file", nilIfEmpty(file))
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "modprobe", Function: "load",
				Doc: "Load a module, if it is not already loaded.",
				Params: []signature.Param{
					req("name", signature.String, "The module's name."),
					opt("params", signature.Map, nil, "Module parameters, such as {debug: 1}."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: modprobeLoadFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "modprobe", Function: "remove",
				Doc: "Unload a module, if it is loaded. Refuses one that is still in use.",
				Params: []signature.Param{
					req("name", signature.String, "The module's name."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: modprobeRemoveFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "modprobe", Function: "persist_load",
				Doc: "Load a module at every boot, through systemd-modules-load.service.",
				Params: []signature.Param{
					req("name", signature.String, "The module's name."),
					opt("params", signature.Map, nil, "Parameters to give the module whenever it loads, written to modprobe.d."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: modprobePersistLoadFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "modprobe", Function: "persist_remove",
				Doc: "Stop loading a module at boot. Does not unload it now.",
				Params: []signature.Param{
					req("name", signature.String, "The module's name."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: modprobePersistRemoveFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "modprobe", Function: "denylist",
				Doc: "Refuse to auto-load a module. Does not unload it if it is already loaded.",
				Params: []signature.Param{
					req("name", signature.String, "The module's name."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: modprobeDenylistFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "modprobe", Function: "allowlist",
				Doc: "Remove the denylist entry this module wrote. Leaves any other file's denylist rule alone.",
				Params: []signature.Param{
					req("name", signature.String, "The module's name."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: modprobeAllowlistFn,
		},
	)
}

// ---- reading ----

// modprobeModule is one row of /proc/modules.
type modprobeModule struct {
	Name     string
	SizeB    int64
	UseCount int64
	UsedBy   []string
	State    string
	Address  string
}

// modprobeReadModules parses /proc/modules.
//
//	raid1 61440 0 - Live 0x0000000000000000
//	xt_conntrack 12288 2 xt_tcpudp,ip6table_filter Live 0x0000000000000000
//
// Six whitespace-separated fields, always present; the fourth is `-`
// for a module nothing depends on, or a comma list with no spaces
// otherwise.
func modprobeReadModules() (map[string]modprobeModule, error) {
	data, err := os.ReadFile(ProcModulesPath)
	if err != nil {
		return nil, fmt.Errorf("%s could not be read: %w", ProcModulesPath, err)
	}
	out := map[string]modprobeModule{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		m := modprobeModule{Name: f[0], State: f[4], Address: f[5]}
		m.SizeB, _ = strconv.ParseInt(f[1], 10, 64)
		m.UseCount, _ = strconv.ParseInt(f[2], 10, 64)
		if f[3] != "-" {
			m.UsedBy = strings.Split(f[3], ",")
		}
		out[m.Name] = m
	}
	return out, nil
}

func modprobeListValue(mods map[string]modprobeModule) []any {
	names := make([]string, 0, len(mods))
	for n := range mods {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]any, len(names))
	for i, n := range names {
		m := mods[n]
		row := value.NewMap(6)
		row.Set("name", m.Name)
		row.Set("size", m.SizeB)
		row.Set("use_count", m.UseCount)
		usedBy := make([]any, len(m.UsedBy))
		for j, u := range m.UsedBy {
			usedBy[j] = u
		}
		row.Set("used_by", usedBy)
		row.Set("state", m.State)
		row.Set("address", m.Address)
		out[i] = row
	}
	return out
}

func modprobeToolPresent(c *exec.Context) error {
	if c.Which("modprobe") == "" {
		return errors.New(
			"this node has no `modprobe`; it is part of kmod, which every Linux system with loadable " +
				"module support ships")
	}
	return nil
}

// modprobeInfoFn parses `modinfo`.
//
// Each line is `label:\s*value`; `alias` and `parm` commonly repeat, one
// per line, and are collected as lists rather than each overwriting the
// last. A continuation line -- `signature:`'s hex dump wraps onto lines
// with no label of their own -- carries nothing this module reports and
// is skipped, rather than guessed at.
func modprobeInfoFn(c *exec.Context, args *value.Map) (any, error) {
	if err := modprobeToolPresent(c); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if name == "" {
		return nil, errors.New("a module must be named")
	}
	res, err := c.Run(exec.Command{Argv: []string{"modinfo", name}, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("modinfo could not be run: %w", err)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("%s: %s", name, strings.TrimSpace(firstLine(res.Stderr+res.Stdout)))
	}
	return modprobeParseInfo(res.Stdout), nil
}

func modprobeParseInfo(out string) *value.Map {
	multi := map[string]bool{"alias": true, "parm": true, "depends": true, "firmware": true}
	scalars := value.NewMap(8)
	lists := map[string][]any{}
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok || strings.ContainsAny(key, " \t") {
			continue // a continuation line, not `label: value`
		}
		val = strings.TrimSpace(val)
		if key == "depends" {
			if val == "" {
				lists[key] = []any{}
			} else {
				for _, d := range strings.Split(val, ",") {
					lists[key] = append(lists[key], strings.TrimSpace(d))
				}
			}
			continue
		}
		if multi[key] {
			if val != "" {
				lists[key] = append(lists[key], val)
			}
			continue
		}
		scalars.Set(key, val)
	}
	for k, v := range lists {
		scalars.Set(k, v)
	}
	return scalars
}

// modprobeDenylistedIn sweeps ModProbeDir for the directive that keeps a
// module from auto-loading and reports which file has it. The directive
// itself is still spelled the old way in every modprobe.conf(5) this
// build will meet, which is why the line below quotes it rather than
// this module's own name for the idea.
func modprobeDenylistedIn(name string) (file string, found bool) {
	entries, err := os.ReadDir(ModProbeDir)
	if err != nil {
		return "", false
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".conf") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, fname := range names {
		path := filepath.Join(ModProbeDir, fname)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if i := strings.IndexByte(line, '#'); i >= 0 {
				line = strings.TrimSpace(line[:i])
			}
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "blacklist" && fields[1] == name { // lexicon:allow — modprobe.conf(5)'s own directive name
				return path, true
			}
		}
	}
	return "", false
}

// ---- writing: load/remove ----

func modprobeMutateResult(c *exec.Context, changed bool, comment string, change *value.Map) *value.Map {
	out := value.NewMap(3)
	out.Set("changed", changed)
	if c.Test && changed {
		out.Set("comment", comment+" Nothing was changed: this was a test run.")
	} else {
		out.Set("comment", comment)
	}
	if change != nil {
		out.Set("changes", change)
	}
	return out
}

// modprobeParamArgs renders a params mapping as `key=value` tokens, in
// sorted key order so the command this builds is the same on every run.
func modprobeParamArgs(args *value.Map, key string) []string {
	m := states.Mapping(args, key)
	if m == nil {
		return nil
	}
	keys := make([]string, 0, m.Len())
	for _, e := range m.Entries() {
		keys = append(keys, value.KeyString(e.Key))
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		v, _ := m.GetString(k)
		out = append(out, fmt.Sprintf("%s=%s", k, value.KeyString(v)))
	}
	return out
}

func modprobeLoadFn(c *exec.Context, args *value.Map) (any, error) {
	if err := modprobeToolPresent(c); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if name == "" {
		return nil, errors.New("a module must be named")
	}
	mods, err := modprobeReadModules()
	if err != nil {
		return nil, err
	}
	if _, ok := mods[name]; ok {
		return modprobeMutateResult(c, false, fmt.Sprintf("%s is already loaded.", name), nil), nil
	}
	params := modprobeParamArgs(args, "params")
	argv := append([]string{"modprobe", name}, params...)
	change := value.MapOf(name, states.Change("not loaded", "loaded"))
	if c.Test {
		return modprobeMutateResult(c, true, fmt.Sprintf("%s would be loaded.", name), change), nil
	}
	if err := modprobeRun(c, argv); err != nil {
		return nil, err
	}
	return modprobeMutateResult(c, true, fmt.Sprintf("%s was loaded.", name), change), nil
}

func modprobeRemoveFn(c *exec.Context, args *value.Map) (any, error) {
	if err := modprobeToolPresent(c); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if name == "" {
		return nil, errors.New("a module must be named")
	}
	mods, err := modprobeReadModules()
	if err != nil {
		return nil, err
	}
	m, ok := mods[name]
	if !ok {
		return modprobeMutateResult(c, false, fmt.Sprintf("%s is not loaded.", name), nil), nil
	}
	if len(m.UsedBy) > 0 {
		return nil, fmt.Errorf("%s is still in use by %s", name, strings.Join(m.UsedBy, ", "))
	}
	change := value.MapOf(name, states.Change("loaded", "not loaded"))
	if c.Test {
		return modprobeMutateResult(c, true, fmt.Sprintf("%s would be unloaded.", name), change), nil
	}
	if err := modprobeRun(c, []string{"modprobe", "-r", name}); err != nil {
		return nil, err
	}
	return modprobeMutateResult(c, true, fmt.Sprintf("%s was unloaded.", name), change), nil
}

func modprobeRun(c *exec.Context, argv []string) error {
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return fmt.Errorf("%s could not be run: %w", argv[0], err)
	}
	if res.Code != 0 {
		return fmt.Errorf("`%s` exited %d: %s", exec.Command{Argv: argv}.String(), res.Code,
			strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return nil
}

// ---- writing: persistence ----

func modprobeLoadFilePath(name string) string {
	return filepath.Join(ModulesLoadDir, name+".conf")
}

func modprobeOptionsFilePath(name string) string {
	return filepath.Join(ModProbeDir, name+"-options.conf")
}

func modprobeDenylistFilePath(name string) string {
	return filepath.Join(ModProbeDir, "denylist-"+name+".conf")
}

func modprobePersistLoadFn(c *exec.Context, args *value.Map) (any, error) {
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if name == "" {
		return nil, errors.New("a module must be named")
	}
	loadPath := modprobeLoadFilePath(name)
	loadBody := name + "\n"
	loadChanged, err := modprobeWriteIfDifferent(c, loadPath, loadBody, 0o644)
	if err != nil {
		return nil, err
	}

	optPath := modprobeOptionsFilePath(name)
	params := modprobeParamArgs(args, "params")
	var optChanged bool
	if len(params) > 0 {
		optBody := fmt.Sprintf("options %s %s\n", name, strings.Join(params, " "))
		optChanged, err = modprobeWriteIfDifferent(c, optPath, optBody, 0o644)
		if err != nil {
			return nil, err
		}
	}

	if !loadChanged && !optChanged {
		return modprobeMutateResult(c, false, fmt.Sprintf("%s already loads at boot.", name), nil), nil
	}
	change := value.NewMap(2)
	change.Set(loadPath, states.Change(nil, name))
	if len(params) > 0 {
		change.Set(optPath, states.Change(nil, strings.Join(params, " ")))
	}
	verb := "would load"
	if !c.Test {
		verb = "will load"
	}
	return modprobeMutateResult(c, true, fmt.Sprintf("%s %s at every boot.", name, verb), change), nil
}

// modprobeWriteIfDifferent writes a file if its content differs,
// honouring test mode, and reports whether it changed.
func modprobeWriteIfDifferent(c *exec.Context, path, body string, mode os.FileMode) (bool, error) {
	existing, _ := os.ReadFile(path)
	if string(existing) == body {
		return false, nil
	}
	if c.Test {
		return true, nil
	}
	if err := atomicfile.Write(path, []byte(body), mode); err != nil {
		return false, fmt.Errorf("%s could not be written: %w", path, err)
	}
	return true, nil
}

func modprobePersistRemoveFn(c *exec.Context, args *value.Map) (any, error) {
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if name == "" {
		return nil, errors.New("a module must be named")
	}
	path := modprobeLoadFilePath(name)
	if _, err := os.Stat(path); err != nil {
		return modprobeMutateResult(c, false, fmt.Sprintf("%s does not load at boot.", name), nil), nil
	}
	change := value.MapOf(path, states.Change(name, nil))
	if c.Test {
		return modprobeMutateResult(c, true, fmt.Sprintf("%s would stop loading at boot.", name), change), nil
	}
	if err := os.Remove(path); err != nil {
		return nil, fmt.Errorf("%s could not be removed: %w", path, err)
	}
	return modprobeMutateResult(c, true, fmt.Sprintf("%s no longer loads at boot.", name), change), nil
}

// modprobeDenylistFn writes a file whose one directive is spelled the
// way modprobe.conf(5) has always spelled it. Everything about this
// function's own name and reporting uses this project's word for the
// idea; only the line handed to modprobe cannot.
func modprobeDenylistFn(c *exec.Context, args *value.Map) (any, error) {
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if name == "" {
		return nil, errors.New("a module must be named")
	}
	if file, ok := modprobeDenylistedIn(name); ok {
		return modprobeMutateResult(c, false, fmt.Sprintf("%s is already denylisted, in %s.", name, file), nil), nil
	}
	path := modprobeDenylistFilePath(name)
	body := fmt.Sprintf("blacklist %s\n", name) // lexicon:allow — modprobe.conf(5)'s own directive name
	change := value.MapOf(path, states.Change(nil, "denylisted"))
	if c.Test {
		return modprobeMutateResult(c, true, fmt.Sprintf("%s would be denylisted.", name), change), nil
	}
	if err := atomicfile.Write(path, []byte(body), 0o644); err != nil {
		return nil, fmt.Errorf("%s could not be written: %w", path, err)
	}
	return modprobeMutateResult(c, true, fmt.Sprintf("%s was denylisted.", name), change), nil
}

func modprobeAllowlistFn(c *exec.Context, args *value.Map) (any, error) {
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if name == "" {
		return nil, errors.New("a module must be named")
	}
	path := modprobeDenylistFilePath(name)
	if _, err := os.Stat(path); err != nil {
		if file, ok := modprobeDenylistedIn(name); ok {
			return nil, fmt.Errorf(
				"%s is denylisted in %s, which this module did not write; remove that line by hand or with file.managed", name, file)
		}
		return modprobeMutateResult(c, false, fmt.Sprintf("%s is not denylisted.", name), nil), nil
	}
	change := value.MapOf(path, states.Change("denylisted", nil))
	if c.Test {
		return modprobeMutateResult(c, true, fmt.Sprintf("%s would be allowed to load again.", name), change), nil
	}
	if err := os.Remove(path); err != nil {
		return nil, fmt.Errorf("%s could not be removed: %w", path, err)
	}
	return modprobeMutateResult(c, true, fmt.Sprintf("%s was allowed to load again.", name), change), nil
}
