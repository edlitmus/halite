package builtin

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// jailOnly is the platform restriction the jail module carries.
//
// Jails are FreeBSD's. DragonFly inherited an older form of them and
// spells the tools differently enough that a provider written here would
// be guessing at it, so it is left out rather than claimed.
var jailOnly = []string{"freebsd"}

// registerJail installs the jail module of SPEC 15.3's FreeBSD row, and
// the `jail.running` state.
//
// # It reads libxo's JSON, not the table
//
// `jls` prints a table by default and has printed a different one at
// different times: the columns depend on the flags, on whether the jail
// has an address, and on the version. `jls --libxo=json` prints the same
// facts in a structure that exists precisely so that a program does not
// have to guess at a spelling.
//
// That choice is a direct consequence of what `pf` cost. Its provider
// compared its own rendered rule against `pfctl -s rules` on the
// assumption that pf prints back what it was given; pf reprints from its
// parsed form, so no rule ever matched itself, and the test that should
// have caught it passed because its fixture was written in the module's
// own spelling. DIVERGENCE 5.31. Parsing a structured interface where
// the platform offers one removes most of that surface, and where this
// module still has to guess, `jail_test.go` says which assumption is
// invented and `TestJailReadsWhatARealJlsPrints` checks the rest on
// FreeBSD in CI.
//
// # What it does not do
//
// It does not create or destroy a jail. A jail's definition lives in
// `/etc/jail.conf` or `/etc/jail.conf.d`, which is a file, and a file is
// `file.managed`'s to own — a module that wrote jail.conf would own
// every jail on the host including the ones somebody else defined.
// `jail.running` starts and stops what is already defined, which is the
// part that is a state rather than a file.
func registerJail(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "jail", Function: "list",
				Doc:        "Return every running jail, by name.",
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  jailOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				jails, err := jailList(c)
				if err != nil {
					return nil, err
				}
				out := value.NewMap(len(jails))
				for _, name := range sortedJailNames(jails) {
					out.Set(name, jails[name].asMap())
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "jail", Function: "show",
				Doc: "Return one running jail's details, or nothing if it is not running.",
				Params: []signature.Param{
					req("name", signature.String, "The jail's name, as `jail.conf` names it."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  jailOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				jails, err := jailList(c)
				if err != nil {
					return nil, err
				}
				j, ok := jails[states.Str(args, "name", "")]
				if !ok {
					return nil, nil
				}
				return j.asMap(), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "jail", Function: "is_running",
				Doc: "Report whether a jail is running.",
				Params: []signature.Param{
					req("name", signature.String, "The jail's name."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  jailOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				jails, err := jailList(c)
				if err != nil {
					return nil, err
				}
				_, ok := jails[states.Str(args, "name", "")]
				return ok, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "jail", Function: "configured",
				Doc: "Return the jails `jail.conf` defines, running or not.",
				Params: []signature.Param{
					opt("config", signature.Path, "", "The configuration to read; defaults to /etc/jail.conf."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  jailOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				names, err := jailConfigured(c, states.Str(args, "config", ""))
				if err != nil {
					return nil, err
				}
				out := make([]any, len(names))
				for i, n := range names {
					out[i] = n
				}
				return out, nil
			},
		},
	)

	for _, m := range []struct{ fn, verb, doc string }{
		{"start", "-c", "Start a jail that `jail.conf` defines."},
		{"stop", "-r", "Stop a running jail."},
	} {
		fn, verb := m.fn, m.verb
		r.Exec.Add(exec.Module{
			Sig: signature.Signature{
				Module: "jail", Function: fn,
				Doc: m.doc,
				Params: []signature.Param{
					req("name", signature.String, "The jail's name."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  jailOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				name := strings.TrimSpace(states.Str(args, "name", ""))
				if name == "" {
					return nil, fmt.Errorf("jail.%s needs a jail name", fn)
				}
				if c.Test {
					return true, nil
				}
				return true, jailRun(c, verb, name)
			},
		})
	}

	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "jail", Function: "running",
			Doc: "Ensure a jail defined in `jail.conf` is running, or is not.",
			Params: []signature.Param{
				nameParam("The jail's name. Defaults to the state ID."),
				opt("running", signature.Bool, true, "Whether it should be running."),
			},
			Mutates:    true,
			TestMode:   signature.TestReliable,
			Privileges: []string{"root"},
			Platforms:  jailOnly,
			Section:    "15.5",
		},
		Fn: jailRunningState,
	})
}

// jailInfo is one running jail.
type jailInfo struct {
	JID      int
	Name     string
	Path     string
	Hostname string
	State    string
	// OSRelease is the userland version inside the jail, which is one of
	// the reasons somebody looks at a jail's details at all: a jail can
	// run an older FreeBSD than the host it is on.
	OSRelease string
}

func (j jailInfo) asMap() *value.Map {
	return value.MapOf(
		"jid", j.JID,
		"path", j.Path,
		"hostname", j.Hostname,
		"state", j.State,
		"osrelease", j.OSRelease,
	)
}

func sortedJailNames(jails map[string]jailInfo) []string {
	out := make([]string, 0, len(jails))
	for name := range jails {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func haveJail(c *exec.Context, tool string) error {
	if c.Which(tool) == "" {
		return fmt.Errorf("%s is not on this node; jails are FreeBSD's and this build "+
			"looks for the base system's tools", tool)
	}
	return nil
}

// jailList reads `jls --libxo=json`.
func jailList(c *exec.Context) (map[string]jailInfo, error) {
	if err := haveJail(c, "jls"); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"jls", "--libxo=json", "-v"},
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("jls: %s", firstLine(res.Stderr+res.Stdout))
	}
	return parseJls(res.Stdout)
}

// parseJls reads what `jls --libxo=json` prints.
//
// libxo wraps the list in a container whose name is the command's own:
//
//	{"__version":"1", "jail-information": {"jail": [ {...}, {...} ]}}
//
// The wrapper is looked for by name and then, failing that, by shape —
// any array of objects carrying a `jid`. Both, because the name is this
// module's one remaining guess about another program's output and the
// shape is not: a jail entry has a jid or it is not a jail entry. If
// libxo's container is ever spelled differently, the fallback keeps this
// working and `TestJailReadsWhatARealJlsPrints` on the FreeBSD runner is
// what would say so.
//
// An empty list is an answer. A host with no jails running is the
// ordinary case, and failing there would make `jail.list` fail on almost
// every FreeBSD machine.
func parseJls(stdout string) (map[string]jailInfo, error) {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return map[string]jailInfo{}, nil
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(stdout), &root); err != nil {
		return nil, fmt.Errorf("jls did not print JSON this build can read (%w). "+
			"It was asked for --libxo=json; a jls without libxo support would explain it", err)
	}
	entries := jailEntries(root)
	out := make(map[string]jailInfo, len(entries))
	for _, e := range entries {
		j := jailInfo{
			JID:      intOf(e["jid"]),
			Name:     stringOf(e["name"]),
			Path:     stringOf(e["path"]),
			Hostname: stringOf(e["host.hostname"]),
			State:    stringOf(e["state"]),
		}
		j.OSRelease = stringOf(e["osrelease"])
		if j.Hostname == "" {
			j.Hostname = stringOf(e["hostname"])
		}
		// A jail with no name is one started by `jail -c` without one,
		// which jls identifies by jid. Keying by the jid keeps it
		// visible rather than dropping it.
		key := j.Name
		if key == "" {
			key = fmt.Sprint(j.JID)
		}
		out[key] = j
	}
	return out, nil
}

// jailEntries finds the array of jails in libxo's envelope.
func jailEntries(root map[string]any) []map[string]any {
	if container, ok := root["jail-information"].(map[string]any); ok {
		if found := arrayOfJails(container["jail"]); found != nil {
			return found
		}
	}
	// By shape. Anything one level down that is an array of objects
	// with a jid is the list, whatever the container is called.
	for _, v := range root {
		if found := arrayOfJails(v); found != nil {
			return found
		}
		if nested, ok := v.(map[string]any); ok {
			for _, inner := range nested {
				if found := arrayOfJails(inner); found != nil {
					return found
				}
			}
		}
	}
	return nil
}

func arrayOfJails(v any) []map[string]any {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	var out []map[string]any
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil
		}
		if _, has := m["jid"]; !has {
			return nil
		}
		out = append(out, m)
	}
	return out
}

func stringOf(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

// intOf reads a number libxo may have written as a number or a string.
//
// libxo emits a jid as a JSON number; some versions quote every value.
// Both are the same jid, and a module that reported 0 for one of them
// would be wrong about which jail it was looking at.
func intOf(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		n := 0
		if _, err := fmt.Sscanf(strings.TrimSpace(t), "%d", &n); err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// jailConfigured reads the names `jail.conf` defines.
//
// `jail -f <conf> -e ,` lists them without starting anything: `-e`
// prints the configured jails separated by the string given, and is the
// only way to ask the parser what it read rather than parsing jail.conf
// here. jail.conf has includes, variables and inheritance, and a second
// parser for it in this module would disagree with the real one
// eventually.
func jailConfigured(c *exec.Context, conf string) ([]string, error) {
	if err := haveJail(c, "jail"); err != nil {
		return nil, err
	}
	argv := []string{"jail"}
	if conf != "" {
		argv = append(argv, "-f", conf)
	}
	argv = append(argv, "-e", ",")
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("jail -e: %s", firstLine(res.Stderr+res.Stdout))
	}
	var out []string
	for _, name := range strings.Split(strings.TrimSpace(res.Stdout), ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// jailRun starts or stops a jail through `jail` itself.
//
// `jail -c <name>` and `jail -r <name>` rather than
// `service jail onestart <name>`: the service script is a wrapper around
// these that also honours `jail_enable` and `jail_list` in rc.conf, and
// a state that went through it would refuse to start a jail an operator
// had deliberately left out of `jail_list`. Which of the two an estate
// wants is a real question; starting the jail the state names is the
// answer that does what the state says.
func jailRun(c *exec.Context, verb, name string) error {
	if err := haveJail(c, "jail"); err != nil {
		return err
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"jail", verb, name},
		IgnoreExitCode: true,
	})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("jail %s %s: %s", verb, name, firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

// jailRunningState starts or stops a jail that jail.conf defines.
func jailRunningState(c *exec.Context, args *value.Map) (states.Result, error) {
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if name == "" {
		return states.False("This state needs a jail name."), nil
	}
	want := states.Bool(args, "running", true)

	jails, err := jailList(c)
	if err != nil {
		return states.False(fmt.Sprintf("the running jails could not be read: %v", err)), nil
	}
	_, running := jails[name]

	if running == want {
		if want {
			return states.True(fmt.Sprintf("%s is running.", name)), nil
		}
		return states.True(fmt.Sprintf("%s is not running.", name)), nil
	}

	if want {
		// A jail that jail.conf does not define cannot be started, and
		// saying so beats `jail -c` failing with its own wording: the
		// fix is a file, and naming the file is what an operator needs.
		configured, err := jailConfigured(c, "")
		if err == nil && !contains(configured, name) {
			return states.False(fmt.Sprintf(
				"%s is not defined in jail.conf, so there is nothing to start. This state "+
					"runs a jail that is already defined; the definition is a file, and "+
					"`file.managed` owns it. Defined now: %s",
				name, states.SortedNames(configured))), nil
		}
	}

	verb, didWhat := "-c", "started"
	if !want {
		verb, didWhat = "-r", "stopped"
	}
	changes := value.MapOf(name, states.Change(running, want))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be %s.", name, didWhat), changes), nil
	}
	if err := jailRun(c, verb, name); err != nil {
		return states.False(fmt.Sprintf("%s could not be %s: %v", name, didWhat, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s was %s.", name, didWhat), changes), nil
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
