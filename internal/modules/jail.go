package modules

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func init() {
	register("jail.present", jailPresent)
	register("jail.absent", jailAbsent)
	register("jail.running", jailRunning)
	register("jail.stopped", jailStopped)
}

// jailConfDir is where a per-jail configuration lives. rc.d/jail reads
// /etc/jail.conf.d/<name>.conf for a named jail, and jail.conf pulls the
// same file in when it has the matching .include — so writing one file per
// jail works either way, and leaves /etc/jail.conf, with the operator's
// global settings in it, alone.
const jailConfDir = "/etc/jail.conf.d"

// jailDefaults are the parameters a jail needs to be a running system
// rather than an empty namespace, in the order they are written. Every one
// of them can be overridden through `params`, including to nothing.
var jailDefaults = []struct{ key, value string }{
	{"exec.start", "/bin/sh /etc/rc"},
	{"exec.stop", "/bin/sh /etc/rc.shutdown"},
	{"mount.devfs", "true"},
}

// jailPresent writes a jail's configuration.
//
//	www:
//	  jail.present:
//	    - path: /usr/local/jails/www
//	    - hostname: www.example.com
//	    - ip4_addr: 10.0.0.10
//	    - interface: em0
//	    - params:
//	        allow.raw_sockets: true
//	        devfs_ruleset: "4"
//
// It writes the configuration and nothing else: the jail's filesystem is
// the operator's — a base tarball, a ZFS clone, or `file.recurse` — and
// starting it is `jail.running`, so a reconfigured jail can `watch` this
// state and restart on the change.
func jailPresent(c *Ctx, id string, args map[string]any) Result {
	if err := jailsSupported(); err != nil {
		return resFail("%v", err)
	}
	name := Str(args, "name", id)
	if Str(args, "path", "") == "" {
		return resFail("jail.present needs a path (the jail's root directory)")
	}
	body, err := renderJailConf(name, args)
	if err != nil {
		return resFail("%v", err)
	}

	// /etc/jail.conf.d is halite's to create: the file is useless without
	// it, and editPath only makes directories when told to.
	path := jailConfPath(name, args)
	writeArgs := withoutArg(args, "params")
	writeArgs["makedirs"] = "true"
	result := editPath(c, path, writeArgs, func(current []byte, _ bool) ([]byte, string, error) {
		if string(current) == body {
			return current, "", nil
		}
		return []byte(body), "configuration written", nil
	})
	if !result.Ok || !result.Changed {
		return result
	}
	if jailIsRunning(name) {
		// The file is what the next start reads; the running jail is still
		// on the old configuration until something restarts it.
		result.Comment += " (jail is running: restart it to apply)"
	}
	if err := setJailBootValue(c, name, args, Bool(args, "boot", false)); err != nil {
		return resFail("%v", err)
	}
	return result
}

// jailAbsent removes a jail's configuration, stopping it first.
//
// The filesystem is left alone: a jail root is somebody's data, and a
// state that deleted it would be the most expensive kind of surprise.
func jailAbsent(c *Ctx, id string, args map[string]any) Result {
	if err := jailsSupported(); err != nil {
		return resFail("%v", err)
	}
	name := Str(args, "name", id)
	path := jailConfPath(name, args)

	_, statErr := os.Stat(path)
	configured := statErr == nil
	running := jailIsRunning(name)
	if !configured && !running {
		return resOK(fmt.Sprintf("jail %s is absent", name))
	}
	if c.Test {
		return resWould(fmt.Sprintf("jail %s would be stopped and its configuration removed", name))
	}

	changes := map[string]string{}
	if running {
		if err := jailStop(name); err != nil {
			return resFail("%v", err)
		}
		changes[name] = "stopped"
	}
	if configured {
		if err := os.Remove(path); err != nil {
			return resFail("remove %s: %v", path, err)
		}
		changes[path] = "removed"
	}
	if err := setJailBootValue(c, name, args, false); err != nil {
		return resFail("%v", err)
	}
	return resChanged(fmt.Sprintf("jail %s removed (its filesystem is untouched)", name), changes)
}

// jailRunning ensures a jail is running, restarting it when a watched
// state — usually its own jail.present — reported a change.
func jailRunning(c *Ctx, id string, args map[string]any) Result {
	if err := jailsSupported(); err != nil {
		return resFail("%v", err)
	}
	name := Str(args, "name", id)
	running := jailIsRunning(name)
	restart := Bool(args, "__watch_changed", false)

	if running && !restart {
		return resOK(fmt.Sprintf("jail %s is running", name))
	}
	if c.Test {
		if running {
			return resWould(fmt.Sprintf("jail %s would be restarted (watch)", name))
		}
		return resWould(fmt.Sprintf("jail %s would be started", name))
	}
	if running {
		if err := jailStop(name); err != nil {
			return resFail("%v", err)
		}
		if err := jailStart(name); err != nil {
			return resFail("%v", err)
		}
		return resChanged(fmt.Sprintf("jail %s restarted", name),
			map[string]string{name: "restarted (watch)"})
	}
	if err := jailStart(name); err != nil {
		return resFail("%v", err)
	}
	return resChanged(fmt.Sprintf("jail %s started", name), map[string]string{name: "started"})
}

// jailStopped ensures a jail is not running. Its configuration stays.
func jailStopped(c *Ctx, id string, args map[string]any) Result {
	if err := jailsSupported(); err != nil {
		return resFail("%v", err)
	}
	name := Str(args, "name", id)
	if !jailIsRunning(name) {
		return resOK(fmt.Sprintf("jail %s is not running", name))
	}
	if c.Test {
		return resWould(fmt.Sprintf("jail %s would be stopped", name))
	}
	if err := jailStop(name); err != nil {
		return resFail("%v", err)
	}
	return resChanged(fmt.Sprintf("jail %s stopped", name), map[string]string{name: "stopped"})
}

func jailsSupported() error {
	if runtime.GOOS != "freebsd" {
		return fmt.Errorf("jails are FreeBSD only")
	}
	return nil
}

func jailConfPath(name string, args map[string]any) string {
	if path := Str(args, "config", ""); path != "" {
		return path
	}
	return filepath.Join(jailConfDir, name+".conf")
}

// jailIsRunning asks jls, which answers for the jail by name.
func jailIsRunning(name string) bool {
	_, ok := pkgQuery("jls", "-j", name, "jid")
	return ok
}

// jailStart and jailStop go through rc.d/jail rather than jail(8), so a
// jail halite starts is started the same way the host starts it at boot —
// including which configuration file rc decides to read.
func jailStart(name string) error {
	if _, err := pkgRun("service", "jail", "start", name); err != nil {
		return fmt.Errorf("start jail %s: %w", name, err)
	}
	return nil
}

func jailStop(name string) error {
	if _, err := pkgRun("service", "jail", "stop", name); err != nil {
		return fmt.Errorf("stop jail %s: %w", name, err)
	}
	return nil
}

// renderJailConf builds the configuration block. The named arguments come
// first in a fixed order and the rest sorted, so the file is byte-stable
// across runs — a block that reordered itself would report a change every
// time.
func renderJailConf(name string, args map[string]any) (string, error) {
	if strings.ContainsAny(name, " \t{}\"';") {
		return "", fmt.Errorf("jail name %q is not a name jail.conf can hold", name)
	}
	params, err := jailParams(name, args)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("# managed by halite\n")
	fmt.Fprintf(&b, "%s {\n", name)
	for _, key := range params.order {
		value := params.values[key]
		if value == "" {
			continue // an override to nothing drops the parameter
		}
		if value == boolFlag {
			fmt.Fprintf(&b, "\t%s;\n", key)
			continue
		}
		fmt.Fprintf(&b, "\t%s = %s;\n", key, value)
	}
	b.WriteString("}\n")
	return b.String(), nil
}

// boolFlag marks a parameter written bare, the jail.conf spelling for a
// boolean that is on.
const boolFlag = "\x00flag"

// jailParamSet is the parameters in the order they will be written.
type jailParamSet struct {
	order  []string
	values map[string]string
}

func (s *jailParamSet) set(key, value string) {
	if _, seen := s.values[key]; !seen {
		s.order = append(s.order, key)
	}
	s.values[key] = value
}

// jailParams assembles the block: the named arguments, then halite's
// defaults for what a jail needs to boot, then whatever `params` says —
// which wins over both, including by setting a parameter to nothing.
func jailParams(name string, args map[string]any) (*jailParamSet, error) {
	set := &jailParamSet{values: map[string]string{}}
	named := []struct{ key, arg string }{
		{"path", "path"},
		{"host.hostname", "hostname"},
		{"ip4.addr", "ip4_addr"},
		{"ip6.addr", "ip6_addr"},
		{"interface", "interface"},
	}
	for _, n := range named {
		value := Str(args, n.arg, "")
		if n.arg == "hostname" && value == "" {
			value = name
		}
		if value == "" {
			continue
		}
		set.set(n.key, quoteJailValue(value))
	}
	for _, d := range jailDefaults {
		if _, taken := set.values[d.key]; !taken {
			set.set(d.key, jailValue(d.value))
		}
	}

	extra, err := jailExtraParams(args)
	if err != nil {
		return nil, err
	}
	for _, key := range extra.order {
		set.set(key, extra.values[key])
	}
	return set, nil
}

// jailExtraParams reads the `params` mapping, sorted so the file is stable.
func jailExtraParams(args map[string]any) (*jailParamSet, error) {
	set := &jailParamSet{values: map[string]string{}}
	params, declared := Map(args, "params")
	if !declared {
		return set, nil
	}
	if params == nil {
		return nil, fmt.Errorf("params must be a mapping of jail parameters")
	}
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		switch value := params[key].(type) {
		case []any:
			parts := make([]string, 0, len(value))
			for _, item := range value {
				parts = append(parts, quoteJailValue(fmt.Sprintf("%v", item)))
			}
			set.set(key, strings.Join(parts, ", "))
		default:
			set.set(key, jailValue(fmt.Sprintf("%v", value)))
		}
	}
	return set, nil
}

// jailValue turns a scalar into what jail.conf should hold: `true` is a
// bare flag, an empty value drops the parameter, anything else is quoted.
//
// There is no translation for switching a boolean off, because jail.conf
// spells that by prefixing the last component with "no"
// (`allow.nomount.devfs`) and guessing where that prefix goes is how a
// state writes a file that means something other than what it says. Write
// the negated name as a parameter instead.
func jailValue(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "on":
		return boolFlag
	case "":
		return ""
	}
	return quoteJailValue(value)
}

func quoteJailValue(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

// setJailBootValue writes jail_list as a whole rather than relying on
// sysrc's += and -=, so the result is the list halite computed.
func setJailBootValue(c *Ctx, name string, args map[string]any, want bool) error {
	if _, declared := args["boot"]; !declared {
		return nil
	}
	current, _ := pkgQuery("sysrc", "-n", "jail_list")
	listed := strings.Fields(current)
	updated := make([]string, 0, len(listed)+1)
	found := false
	for _, entry := range listed {
		if entry == name {
			found = true
			if !want {
				continue
			}
		}
		updated = append(updated, entry)
	}
	if want && !found {
		updated = append(updated, name)
	}
	if want == found {
		return nil
	}
	if c.Test {
		return nil
	}
	if _, err := pkgRun("sysrc", "jail_list="+strings.Join(updated, " ")); err != nil {
		return fmt.Errorf("sysrc jail_list: %w", err)
	}
	return nil
}
