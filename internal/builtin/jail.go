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
				Module: "jail", Function: "show_config",
				Doc: "Return the parameters `jail.conf` gives one jail, running or not.",
				Params: []signature.Param{
					req("name", signature.String, "The jail's name."),
					opt("config", signature.Path, "", "The configuration to read; defaults to /etc/jail.conf."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  jailOnly,
				Section:    "15.3",
			},
			// This reads `jail -e` rather than parsing jail.conf, for
			// the reason the whole module does: jail.conf has includes,
			// variables and inheritance, and a jail's real parameters
			// are what `jail` resolves them to. The values here are the
			// ones the jail would actually be created with, including
			// everything it inherits from the global block, which is
			// what an operator is asking when they ask what a jail is
			// configured as.
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				name := strings.TrimSpace(states.Str(args, "name", ""))
				if name == "" {
					return nil, fmt.Errorf("jail.show_config needs a jail name")
				}
				params, err := jailShowConfig(c, states.Str(args, "config", ""), name)
				if err != nil {
					return nil, err
				}
				return params, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "jail", Function: "get_enabled",
				Doc: "Return the jails rc.conf starts at boot, and whether jails are enabled at all.",
				Params: []signature.Param{
					opt("file", signature.Path, "", "An rc.conf other than the default."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: jailOnly,
				Section:   "15.3",
			},
			// **`jail_enable` and `jail_list` are two different facts and
			// both are needed.** A jail defined in jail.conf does not
			// start at boot because it is defined; it starts because
			// `jail_list` names it and `jail_enable` is on. So a jail can
			// be configured, startable by hand, and still absent after a
			// reboot -- which is the question an operator is really
			// asking, and which `jail.configured` cannot answer.
			//
			// An empty `jail_list` with `jail_enable="YES"` is not a
			// misconfiguration: it is a host that runs jails on purpose
			// and starts none of them automatically.
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				file := states.Str(args, "file", "")
				enable, _, err := sysrcGet(c, "jail_enable", file)
				if err != nil {
					return nil, err
				}
				list, _, err := sysrcGet(c, "jail_list", file)
				if err != nil {
					return nil, err
				}
				names := strings.Fields(list)
				at := make([]any, len(names))
				for i, n := range names {
					at[i] = n
				}
				out := value.NewMap(3)
				out.Set("enabled", strings.EqualFold(strings.TrimSpace(enable), "YES"))
				out.Set("jails", at)
				out.Set("jail_enable", strings.TrimSpace(enable))
				return out, nil
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

	// **`config` is on every one of these, and on the state.** It was on
	// `jail.configured` alone, so a tree keeping its jails in a file of
	// its own could list them and could not start one: the listing read
	// the named file and the start read /etc/jail.conf. Two paths that
	// must agree and did not, which is the shape 5.70 was.
	//
	// `-rc` is restart, and it is jail(8)'s own spelling rather than a
	// stop followed by a start: the usage line reads `-[cmr]`, the verbs
	// combine, and a jail restarted this way is removed and recreated in
	// one call. Driven against a real jail, whose jid changed from 6 to
	// 7 across it, which is the observable difference between a restart
	// and a no-op.
	for _, m := range []struct{ fn, verb, doc string }{
		{"start", "-c", "Start a jail that `jail.conf` defines."},
		{"stop", "-r", "Stop a running jail."},
		{"restart", "-rc", "Stop a running jail and start it again."},
	} {
		fn, verb := m.fn, m.verb
		r.Exec.Add(exec.Module{
			Sig: signature.Signature{
				Module: "jail", Function: fn,
				Doc: m.doc,
				Params: []signature.Param{
					req("name", signature.String, "The jail's name."),
					opt("config", signature.Path, "", "The configuration to read; defaults to /etc/jail.conf."),
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
				return true, jailRun(c, states.Str(args, "config", ""), verb, name)
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
				opt("config", signature.Path, "", "The configuration to read; defaults to /etc/jail.conf."),
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
	// Dying is the kernel's own word for a jail that has been removed
	// and whose processes have not all exited yet. It is a real jail
	// parameter; `state` is not one, which is the whole of the defect
	// below.
	Dying bool
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
		"state", j.state(),
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
			Dying:    boolOf(e["dying"]),
		}
		j.OSRelease = stringOf(e["osrelease"])
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

// state reports a jail as an operator asks about it.
//
// **`state` is not a field `jls` prints, and this module read one for as
// long as it has existed.** The fixture it was tested against carried
// `"state": "ACTIVE"`, a value invented by whoever wrote the fixture, so
// the unit test agreed with the module and the field was empty on every
// real host. That is DIVERGENCE 5.31's lesson -- a fixture written in
// the module's own spelling asserts nothing -- arriving for the fourth
// time, and it was found by asking `jls` itself rather than by reading
// it: `jls -h` prints the complete list of parameters it knows, and
// `state` is not among them.
//
// What the kernel has instead is `dying`, one of the parameters in
// `security.jail.param`. A jail is dying when it has been removed and
// some process inside it has not exited yet, which is exactly the state
// an operator is looking for when a jail will not go away. So the
// reported word is derived from the parameter that exists rather than
// read from one that does not.
func (j jailInfo) state() string {
	if j.Dying {
		return "DYING"
	}
	return "ACTIVE"
}

// boolOf reads a flag libxo may have written as a boolean, as a number
// or as a string, the way intOf already allows for the same spread.
func boolOf(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "1", "yes":
			return true
		}
	}
	return false
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
	out, err := jailExhibit(c, conf)
	if err != nil {
		return nil, err
	}
	names, err := parseJailExhibit(out)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// jailExhibit runs `jail -e` and returns what it printed.
//
// Shared by the two readers that need it so there is one spelling of the
// command and one place the separator is chosen, rather than the pair
// that has already gone wrong twice in this module.
func jailExhibit(c *exec.Context, conf string) (string, error) {
	argv := []string{"jail"}
	if conf != "" {
		argv = append(argv, "-f", conf)
	}
	argv = append(argv, "-e", jailListSeparator)
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", fmt.Errorf("jail -e: %s", firstLine(res.Stderr+res.Stdout))
	}
	return res.Stdout, nil
}

// jailListSeparator is what `jail -e` is asked to put between
// parameters.
//
// **Not a comma, which is what this asked for until it was run against a
// jail.conf that defined something.** `-e` separates *parameters*, and a
// parameter's value may contain a comma without being quoted:
//
//	$ jail -f t.conf -e ,
//	name=tricky,path="/tmp/a b,c",host.hostname=x,y,persist
//
// `path` is quoted there because it has a space in it and
// `host.hostname` is not, so no rule recovers where one parameter ends
// and the next begins. ASCII unit separator cannot appear in a jail.conf
// value, so with it the record is unambiguous. This is the same refusal
// to guess that made `quota` read Linux through `-O csv`.
const jailListSeparator = "\x1f"

// parseJailExhibit reads the names out of `jail -e`.
//
// **`jail -e` does not print a list of names.** It prints one line per
// configured jail, and on each line that jail's parameters separated by
// the separator, with the name carried as the `name=` parameter:
//
//	name=web,path=/tmp/web,persist
//	name=db,path=/tmp/db,persist
//
// This module split the whole output on the separator and took every
// field as a jail name, so for the jail.conf above it answered
// `[name=web, path=/tmp/web, persist, name=db, ...]` -- not one of which
// is a name. `jail.configured` returned that, and `jail.running` looked
// for its jail in it and never found one, so **the state reported "is
// not defined in jail.conf, so there is nothing to start" for every jail
// that was in fact defined, and could not start any jail at all**.
//
// Nothing caught it because the fleet's own host has no jails in
// jail.conf: `jail -e` printed nothing, the function returned nothing,
// and an empty list is exactly what a host with no configured jails
// should produce. The defect needed a populated jail.conf to become
// visible, and the tests supplied their own spelling instead.
//
// A line with no `name=` is refused rather than skipped. `jail -e` puts
// the name first on every line it prints, so a line without one is a
// format this reader does not understand, and a list of configured jails
// that quietly omits one is worse than no list: a state would start a
// jail that is already running, or report a defined jail as undefined.
func parseJailExhibit(out string) ([]string, error) {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		name, ok := jailNameOf(line)
		if !ok {
			return nil, fmt.Errorf(
				"`jail -e` printed a line with no `name=` parameter on it: %q. "+
					"Every line it prints describes one configured jail and carries that "+
					"jail's name, so this build cannot tell which jails are defined", line)
		}
		names = append(names, name)
	}
	return names, nil
}

// jailShowConfig returns one configured jail's resolved parameters.
//
// A jail that jail.conf does not define is an error rather than an empty
// map, and the error lists what is defined. An empty map would read as
// "this jail has no parameters", which is a different and untrue thing:
// every jail has at least a path.
func jailShowConfig(c *exec.Context, conf, name string) (*value.Map, error) {
	out, err := jailExhibit(c, conf)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		got, ok := jailNameOf(line)
		if !ok {
			continue
		}
		if got == name {
			return jailParamsOf(line), nil
		}
		names = append(names, got)
	}
	sort.Strings(names)
	return nil, fmt.Errorf("jail.conf does not define a jail called %q. Defined: %s",
		name, states.SortedNames(names))
}

// jailParamsOf turns one `jail -e` line into its parameters.
//
// A parameter with no `=` is a flag rather than a setting -- `persist`
// and `allow.mount.devfs` are printed bare -- and those are reported as
// true rather than as an empty string, because that is what they mean
// and an empty string reads as "set to nothing".
func jailParamsOf(line string) *value.Map {
	params := strings.Split(line, jailListSeparator)
	out := value.NewMap(len(params))
	for _, param := range params {
		param = strings.TrimSpace(param)
		if param == "" {
			continue
		}
		key, val, ok := strings.Cut(param, "=")
		if !ok {
			out.Set(param, true)
			continue
		}
		if len(val) >= 2 && strings.HasPrefix(val, `"`) && strings.HasSuffix(val, `"`) {
			val = val[1 : len(val)-1]
		}
		out.Set(key, val)
	}
	return out
}

// jailNameOf pulls the name parameter out of one `jail -e` line.
func jailNameOf(line string) (string, bool) {
	for _, param := range strings.Split(line, jailListSeparator) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(param), "name=")
		if !ok {
			continue
		}
		// A name with a space or a separator in it comes back quoted,
		// the way `path="/tmp/a b,c"` does.
		if len(rest) >= 2 && strings.HasPrefix(rest, `"`) && strings.HasSuffix(rest, `"`) {
			rest = rest[1 : len(rest)-1]
		}
		if rest != "" {
			return rest, true
		}
	}
	return "", false
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
func jailRun(c *exec.Context, conf, verb, name string) error {
	if err := haveJail(c, "jail"); err != nil {
		return err
	}
	argv := []string{"jail"}
	if conf != "" {
		argv = append(argv, "-f", conf)
	}
	argv = append(argv, verb, name)
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
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
	conf := states.Str(args, "config", "")

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
		configured, err := jailConfigured(c, conf)
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
	if err := jailRun(c, conf, verb, name); err != nil {
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
