package beacon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// New builds the beacon registry this build ships. SPEC section 16.2.
func New() *Registry {
	r := NewRegistry()
	r.Add(
		Module{
			Name: "diskusage",
			Doc: "Fire when a filesystem is fuller than its threshold. Each key is a " +
				"mount point and its value a percentage, as `/: 85%`.",
			Fn: diskUsage,
		},
		Module{
			Name: "load",
			Doc: "Fire when the load average crosses a threshold. `1m`, `5m`, and `15m` " +
				"each take a comparison and a number, as `1m: ['>', 2.0]`.",
			Platforms: []string{"linux", "freebsd", "openbsd", "netbsd", "darwin"},
			Fn:        loadAverage,
		},
		Module{
			Name: "memusage",
			Doc:  "Fire when memory in use is above the percentage given as `percent`.",
			Fn:   memUsage,
		},
		Module{
			Name: "service",
			Doc: "Fire when a service's running state changes. Each key under `services` " +
				"is a service name.",
			Fn: serviceState,
		},
		Module{
			Name: "filechanges",
			Doc: "Fire when a watched file's digest, size, or mode changes, or when it " +
				"appears or is removed. `files` is a list of paths.",
			Fn: fileChanges,
		},
		Module{
			Name: "cert_info",
			Doc: "Fire when a certificate expires within `notify_days`. `files` is a " +
				"list of PEM certificate paths.",
			Fn: certInfo,
		},
		Module{
			Name: "status",
			Doc: "Emit selected status fields on every interval, for pull-style " +
				"monitoring. `functions` is a list of status module functions.",
			Fn: statusFields,
		},
	)
	registerPending(r)
	return r
}

// registerPending declares the rest of SPEC 16.2's inventory.
//
// Declared rather than omitted, for the reason the runners are: a name
// missing from the registry makes "not written yet" and "you have
// mistyped it" the same message, and a beacon is configured once and
// then trusted for years.
func registerPending(r *Registry) {
	pending := func(name, doc, when string, platforms ...string) Module {
		return Module{Name: name, Doc: doc, Platforms: platforms, Pending: when}
	}
	const syscalls = "the phase that admits golang.org/x/sys, which SPEC 4.2 records as an open question; `filechanges` polls today"
	const readers = "a later phase, with a portable reader for it"

	r.Add(
		pending("inotify", "Watch files and directories through inotify.", syscalls, "linux"),
		pending("fanotify", "Watch a whole mount, or permission events, through fanotify.", syscalls, "linux"),
		pending("watchdirs", "Watch directories through ReadDirectoryChangesW.", "phase 5, with Windows parity", "windows"),
		pending("fsevents", "Watch files through FSEvents.", "phase 5, with macOS parity", "darwin"),
		pending("swapusage", "Fire when swap in use is above a threshold.", readers),
		pending("cpuusage", "Fire when processor use is above a threshold.", readers),
		pending("network_info", "Fire on interface counter thresholds.", readers),
		pending("network_settings", "Fire when an interface attribute changes.", readers, "linux", "windows"),
		pending("proc", "Fire on a process appearing or disappearing.", readers),
		pending("ps", "Fire on a process crossing a resource threshold.", readers),
		pending("pkg", "Fire when package updates are available.", "phase 5, with the package provider matrix", "linux"),
		pending("journald", "Fire on a journal match.", "phase 5, with the systemd platform work", "linux"),
		pending("log", "Fire on a regular expression matching a log line.", readers),
		pending("wtmp", "Fire on a login record.", readers, "linux"),
		pending("btmp", "Fire on a failed-login record.", readers, "linux"),
		pending("eventlog", "Fire on a Windows Event Log subscription.", "phase 5, with Windows parity", "windows"),
		pending("sh", "Fire on a shell command being run.", readers, "linux"),
	)
}

// ---- the beacons ----

// diskUsage fires for each filesystem over its threshold.
func diskUsage(c *exec.Context, in *Instance) ([]Event, error) {
	usage, err := callMap(c, "disk.usage", value.NewMap(0))
	if err != nil {
		return nil, err
	}

	var out []Event
	for _, e := range in.Args.Entries() {
		mount := value.KeyString(e.Key)
		threshold, err := percent(e.Val)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", mount, err)
		}
		entry, ok := usage.Get(mount)
		if !ok {
			continue
		}
		stats, ok := entry.(*value.Map)
		if !ok {
			continue
		}
		used, ok := percentUsed(stats)
		if !ok {
			continue
		}
		if used < threshold {
			continue
		}
		out = append(out, Event{
			Suffix: pathSuffix(mount),
			Data: map[string]any{
				"mount": mount, "percent_used": used, "threshold": threshold,
			},
		})
	}
	sortEvents(out)
	return out, nil
}

// percentUsed reads the used percentage a `disk.usage` entry reports,
// under either the percentage it gives or the byte counts.
func percentUsed(stats *value.Map) (float64, bool) {
	if v, ok := stats.Get("capacity"); ok {
		if n, err := percent(v); err == nil {
			return n, true
		}
	}
	used, uok := numberOf(stats, "used")
	avail, aok := numberOf(stats, "available")
	if uok && aok && used+avail > 0 {
		return used / (used + avail) * 100, true
	}
	return 0, false
}

// loadAverage fires when a load average crosses its threshold.
func loadAverage(c *exec.Context, in *Instance) ([]Event, error) {
	load, err := callMap(c, "status.loadavg", value.NewMap(0))
	if err != nil {
		return nil, err
	}

	var out []Event
	for _, window := range []string{"1m", "5m", "15m"} {
		spec, ok := in.Arg(window)
		if !ok {
			continue
		}
		op, want, err := comparison(spec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", window, err)
		}
		got, ok := numberOf(load, window)
		if !ok {
			// The module names its windows differently on some
			// platforms; try Salt's other spelling before giving up.
			got, ok = numberOf(load, strings.TrimSuffix(window, "m")+"-min")
		}
		if !ok {
			continue
		}
		if !compare(op, got, want) {
			continue
		}
		out = append(out, Event{
			Suffix: window,
			Data: map[string]any{
				"window": window, "load": got, "comparison": op, "threshold": want,
			},
		})
	}
	return out, nil
}

// memUsage fires when memory in use is above the percentage asked for.
func memUsage(c *exec.Context, in *Instance) ([]Event, error) {
	spec, ok := in.Arg("percent")
	if !ok {
		return nil, fmt.Errorf("the memusage beacon needs `percent`")
	}
	threshold, err := percent(spec)
	if err != nil {
		return nil, err
	}

	mem, err := callMap(c, "status.meminfo", value.NewMap(0))
	if err != nil {
		return nil, err
	}
	total, tok := firstNumber(mem, "total", "MemTotal", "mem_total")
	avail, aok := firstNumber(mem, "available", "MemAvailable", "free", "MemFree", "mem_free")
	if !tok || !aok || total <= 0 {
		return nil, fmt.Errorf("status.meminfo did not report a total and an available figure")
	}
	used := (total - avail) / total * 100
	if used < threshold {
		return nil, nil
	}
	return []Event{{
		Data: map[string]any{"percent_used": used, "threshold": threshold},
	}}, nil
}

// serviceState fires when a service's running state changes.
func serviceState(c *exec.Context, in *Instance) ([]Event, error) {
	services, ok := in.Arg("services")
	if !ok {
		return nil, fmt.Errorf("the service beacon needs `services`")
	}
	list, ok := services.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("`services` is a mapping of service name to its settings")
	}

	var out []Event
	for _, e := range list.Entries() {
		name := value.KeyString(e.Key)
		args := value.NewMap(1)
		args.Set("name", name)
		running, err := c.Call("service.status", args)
		if err != nil {
			out = append(out, Event{
				Suffix: name,
				Data:   map[string]any{"service": name, "error": err.Error()},
			})
			continue
		}
		out = append(out, Event{
			Suffix: name,
			Data:   map[string]any{"service": name, "running": value.Truthy(running)},
		})
	}
	return out, nil
}

// fileChanges fires when a watched file's digest, size, or mode moves.
//
// The portable beacon of SPEC 16.2: polling on hash and metadata, for
// the platforms with no native notifier and for this build, which has
// no inotify.
func fileChanges(c *exec.Context, in *Instance) ([]Event, error) {
	paths, err := stringList(in, "files")
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, path := range paths {
		data := map[string]any{"path": path}
		info, err := os.Stat(filepath.Clean(path))
		switch {
		case os.IsNotExist(err):
			data["exists"] = false
		case err != nil:
			data["error"] = err.Error()
		default:
			data["exists"] = true
			data["size"] = info.Size()
			data["mode"] = fmt.Sprintf("%04o", info.Mode().Perm())
			args := value.NewMap(1)
			args.Set("path", path)
			if digest, err := c.Call("file.get_hash", args); err == nil {
				data["hash"] = value.KeyString(digest)
			}
		}
		out = append(out, Event{Suffix: pathSuffix(path), Data: data})
	}
	return out, nil
}

// certInfo fires for a certificate expiring inside the window.
func certInfo(c *exec.Context, in *Instance) ([]Event, error) {
	paths, err := stringList(in, "files")
	if err != nil {
		return nil, err
	}
	days := int64(30)
	if v, ok := in.Arg("notify_days"); ok {
		if n, err := asFloat(v); err == nil {
			days = int64(n)
		}
	}

	var out []Event
	for _, path := range paths {
		args := value.NewMap(2)
		args.Set("path", path)
		args.Set("days", days)
		expiring, err := c.Call("x509.expires", args)
		if err != nil {
			out = append(out, Event{
				Suffix: pathSuffix(path),
				Data:   map[string]any{"path": path, "error": err.Error()},
			})
			continue
		}
		if !value.Truthy(expiring) {
			continue
		}
		out = append(out, Event{
			Suffix: pathSuffix(path),
			Data:   map[string]any{"path": path, "expires_within_days": days},
		})
	}
	return out, nil
}

// statusFields emits the status functions asked for, every interval.
func statusFields(c *exec.Context, in *Instance) ([]Event, error) {
	names, err := stringList(in, "functions")
	if err != nil {
		return nil, err
	}
	data := map[string]any{}
	for _, name := range names {
		fun := name
		if !strings.Contains(fun, ".") {
			fun = "status." + fun
		}
		got, err := c.Call(fun, value.NewMap(0))
		if err != nil {
			data[name] = map[string]any{"error": err.Error()}
			continue
		}
		data[name] = got
	}
	if len(data) == 0 {
		return nil, nil
	}
	return []Event{{Data: data}}, nil
}

// ---- helpers ----

func callMap(c *exec.Context, fun string, args *value.Map) (*value.Map, error) {
	got, err := c.Call(fun, args)
	if err != nil {
		return nil, err
	}
	m, ok := got.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("%s returned %s rather than a mapping", fun, value.TypeName(got))
	}
	return m, nil
}

func stringList(in *Instance, name string) ([]string, error) {
	raw, ok := in.Arg(name)
	if !ok {
		return nil, fmt.Errorf("the %s beacon needs `%s`", in.Name, name)
	}
	switch t := raw.(type) {
	case string:
		return []string{t}, nil
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			out = append(out, value.KeyString(item))
		}
		return out, nil
	case *value.Map:
		// A mapping is accepted for the same reason Salt accepts one:
		// a per-file configuration whose keys are the files.
		out := make([]string, 0, t.Len())
		for _, e := range t.Entries() {
			out = append(out, value.KeyString(e.Key))
		}
		return out, nil
	}
	return nil, fmt.Errorf("`%s` is a list of strings, not %s", name, value.TypeName(raw))
}

// pathSuffix turns a path into the part of a tag that follows the
// beacon's name.
//
// The leading slash goes, so that `/var/log` becomes `var/log` and a
// reactor can glob on the segments. The root filesystem would leave
// nothing at all, and a beacon whose tag ends at its own name cannot be
// matched by `diskusage/**` -- so it is called `root`.
func pathSuffix(path string) string {
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" {
		return "root"
	}
	return trimmed
}

// percent reads `85%`, `85`, or `85.5`.
func percent(v any) (float64, error) {
	switch t := v.(type) {
	case int64:
		return float64(t), nil
	case float64:
		return t, nil
	case string:
		n, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(t), "%"), 64)
		if err != nil {
			return 0, fmt.Errorf("%q is not a percentage", t)
		}
		return n, nil
	}
	return 0, fmt.Errorf("%s is not a percentage", value.TypeName(v))
}

// comparison reads `['>', 2.0]`, `'> 2.0'`, or a bare number, which
// means "at or above".
func comparison(v any) (string, float64, error) {
	switch t := v.(type) {
	case []any:
		if len(t) != 2 {
			return "", 0, fmt.Errorf("a threshold is a comparison and a number")
		}
		n, err := asFloat(t[1])
		if err != nil {
			return "", 0, err
		}
		return value.KeyString(t[0]), n, nil
	case string:
		fields := strings.Fields(t)
		if len(fields) == 2 {
			n, err := strconv.ParseFloat(fields[1], 64)
			if err != nil {
				return "", 0, fmt.Errorf("%q is not a number", fields[1])
			}
			return fields[0], n, nil
		}
	}
	n, err := asFloat(v)
	if err != nil {
		return "", 0, err
	}
	return ">=", n, nil
}

func compare(op string, got, want float64) bool {
	switch op {
	case ">":
		return got > want
	case ">=":
		return got >= want
	case "<":
		return got < want
	case "<=":
		return got <= want
	case "==", "=":
		return got == want
	case "!=":
		return got != want
	}
	return false
}

func numberOf(m *value.Map, key string) (float64, bool) {
	v, ok := m.Get(key)
	if !ok {
		return 0, false
	}
	n, err := asFloat(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

func firstNumber(m *value.Map, keys ...string) (float64, bool) {
	for _, k := range keys {
		if n, ok := numberOf(m, k); ok {
			return n, true
		}
	}
	return 0, false
}

func sortEvents(events []Event) {
	sort.Slice(events, func(i, j int) bool { return events[i].Suffix < events[j].Suffix })
}
