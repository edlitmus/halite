package builtin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// DnfModulesDir is where dnf persists every choice an administrator has
// made about a module stream: one INI file per module, written by
// `dnf module enable/disable/reset/install/remove` and read back by dnf
// itself on every run. A variable so a test can redirect it.
var DnfModulesDir = "/etc/dnf/modules.d"

// registerDnfModule installs the `dnf_module` module of SPEC 15.3's RHEL
// row: dnf's modularity streams, the AppStream mechanism by which EL8
// and EL9 ship several major versions of the same software (nginx 1.14
// and 1.24, postgresql 10 and 16) side by side and let a host choose
// one.
//
// # Salt has no module of this name, so the surface is dnf's own
//
// Salt reaches modularity only obliquely, through `pkg.install`'s
// `name: '@nginx:1.24'` group syntax, and has no function that reports
// or resets a stream. The functions here are therefore dnf's own verbs
// under dnf's own names -- `enable`, `disable`, `reset`, `switch_to`,
// `install`, `remove` -- taking dnf's own `name:stream/profile` specs,
// so an estate converting from hand-run `dnf module` lines reads the
// same words. SPEC 15.5 lists no `dnf_module` state and none is added:
// a stream choice is reached from a state through `module.run`, and
// the day SPEC names a state is the day to write one.
//
// # Two readers, because there are two different questions
//
// "Which streams exist, and which is the default?" is only answerable
// by dnf, from repository metadata, and the only way it will say so is
// `dnf module list` -- a table printed for a person. `list` parses that
// table (see dnfModuleParseList for what the real one looks like on
// dnf 4.7 and 4.14, and why columns are read by offset). "What has this
// host chosen?" is a different question with a better source:
// /etc/dnf/modules.d/<name>.module is the persisted truth dnf itself
// reads back, a four-key INI file. `status` reads those files and does
// not run dnf at all, so it needs neither the network nor dnf's lock.
// The live test holds the two readers to each other on both hosts.
//
// # What is demonstrated
//
// Every function was run as root on Rocky Linux 9.8 (dnf 4.14.0) and
// AlmaLinux 8.10 (dnf 4.7.0); see internal/builtin/evidence.go for what
// that covered and what it did not.
func registerDnfModule(r *Registries) {
	modules := req("modules", signature.List,
		"Module specs in dnf's own syntax: `name`, `name:stream`, or `name:stream/profile`.")
	mutating := func(function, verb, doc string) exec.Module {
		return exec.Module{
			Sig: signature.Signature{
				Module: "dnf_module", Function: function,
				Doc:        doc,
				Params:     []signature.Param{modules},
				Mutates:    true,
				TestMode:   signature.TestUnreliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return dnfModuleMutate(c, verb, states.Strings(args, "modules"))
			},
		}
	}

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "dnf_module", Function: "list",
				Doc: "List module streams the configured repositories offer, as `dnf module list` " +
					"reports them: each stream's profiles and whether it is the default, enabled, " +
					"disabled, or has a profile installed.",
				Params: []signature.Param{
					opt("name", signature.String, "", "Only this module (dnf accepts a glob)."),
					choice("filter", "", "Only streams in this state, as dnf's own --enabled, "+
						"--disabled and --installed select them.", "", "enabled", "disabled", "installed"),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: dnfModuleListFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "dnf_module", Function: "status",
				Doc: "Return the stream choices this host has persisted -- enabled or disabled " +
					"modules, their stream and installed profiles -- read from " +
					"/etc/dnf/modules.d without running dnf. A module never touched, or reset, " +
					"is absent.",
				Params: []signature.Param{
					opt("name", signature.String, "", "Only this module."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: dnfModuleStatusFn,
		},
		mutating("enable", "enable",
			"Enable module streams (`dnf module enable`). Enabling a different stream of a module "+
				"that already has one enabled is refused by dnf; use `switch_to`."),
		mutating("disable", "disable",
			"Disable modules (`dnf module disable`): none of their streams is offered until reset."),
		mutating("reset", "reset",
			"Reset modules to their initial state (`dnf module reset`), forgetting any enabled or "+
				"disabled stream."),
		mutating("switch_to", "switch-to",
			"Switch modules to another stream, synchronising installed packages to it "+
				"(`dnf module switch-to`)."),
		mutating("install", "install",
			"Install module profiles (`dnf module install name:stream/profile`), enabling the "+
				"stream if needed."),
		mutating("remove", "remove",
			"Remove the packages of installed module profiles (`dnf module remove`). The stream "+
				"stays enabled; `reset` forgets it."),
	)
}

// dnfPresent refuses by the tool's name rather than the platform, the way
// `pro` and `dpkg` do: a Debian node has no dnf to be missing, and EL7's
// yum 3 has no modularity at all -- both are facts about the tool.
func dnfPresent(c *exec.Context) error {
	if c.Which("dnf") == "" {
		return errors.New("this node has no `dnf`; module streams exist only on EL8 and later")
	}
	return nil
}

// dnfModuleSpec is deliberately narrow: the characters module names,
// streams, versions, contexts, arches and profiles are built from, dnf's
// `:` and `/` separators, and `*` for dnf's own globbing. What it exists
// to stop is a leading `-`, which dnf would read as an option -- these
// specs arrive from pillar and templates, and `--setopt=...` smuggled in
// as a "module" would reconfigure the package manager.
var dnfModuleSpec = regexp.MustCompile(`^[A-Za-z0-9_.+*][A-Za-z0-9_.+*:/-]*$`)

func dnfModuleValidate(specs []string) error {
	if len(specs) == 0 {
		return errors.New("no modules given")
	}
	for _, spec := range specs {
		if !dnfModuleSpec.MatchString(spec) {
			return fmt.Errorf("%q is not a module spec; expected name, name:stream or name:stream/profile", spec)
		}
	}
	return nil
}

// dnfModuleName is the module a spec names, which is what its
// modules.d file is keyed by.
func dnfModuleName(spec string) string {
	if i := strings.IndexAny(spec, ":/"); i >= 0 {
		return spec[:i]
	}
	return spec
}

func dnfModuleArgv(verb string, specs []string) []string {
	return append([]string{"dnf", "-y", "-q", "module", verb}, specs...)
}

// dnfModuleMutate runs one `dnf module <verb>` and answers with what it
// changed, measured from modules.d before and after rather than read out
// of dnf's human-readable transaction summary.
//
// # Why the exit code is read here and not left to c.Run
//
// c.Run's own error keeps only the first line of stderr, and dnf's first
// line is the least useful one it prints: measured on both hosts,
// `dnf module enable nosuchmodule:1` says
//
//	Error: Problems in request:
//	missing groups or modules: nosuchmodule:1
//
// and a stream switch refused says why on its first line and what to do
// instead on its second. So the command asks for its exit code and the
// whole of stderr goes into the error.
//
// # Why success can carry stderr
//
// On AlmaLinux 8 `dnf module disable nginx` exits 0 and still prints
// "Problems in request: Modular dependency problems with Defaults" and
// a page of `php:7.2 requires module(nginx)` -- the disable took effect
// (modules.d says `state=disabled`), and dnf is warning that a default
// stream elsewhere now cannot resolve. Dropping that would hide exactly
// the consequence an operator most needs to see; failing on it would
// report as broken a change dnf made. It is returned as `stderr`.
func dnfModuleMutate(c *exec.Context, verb string, specs []string) (any, error) {
	if err := dnfModuleValidate(specs); err != nil {
		return nil, err
	}
	if err := dnfPresent(c); err != nil {
		return nil, err
	}
	before, err := dnfModuleReadStatus(DnfModulesDir)
	if err != nil {
		return nil, err
	}
	argv := dnfModuleArgv(verb, specs)
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("`%s` could not be run on this node: %w", exec.Command{Argv: argv}.String(), err)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("`%s` exited %d: %s", exec.Command{Argv: argv}.String(), res.Code,
			strings.TrimSpace(res.Stderr))
	}
	after, err := dnfModuleReadStatus(DnfModulesDir)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, spec := range specs {
		names[dnfModuleName(spec)] = true
	}
	out := value.MapOf("changes", dnfModuleDiff(before, after, names))
	if stderr := strings.TrimSpace(res.Stderr); stderr != "" {
		out.Set("stderr", stderr)
	}
	return out, nil
}

// dnfModuleDiff reports, for each module the call named, its persisted
// state before and after, and only where the two differ -- the Salt
// shape of `{name: {old, new}}`. A glob names no one module, so a glob
// spec widens the diff to every module whose state moved.
func dnfModuleDiff(before, after map[string]dnfModuleState, named map[string]bool) *value.Map {
	glob := false
	for name := range named {
		if strings.Contains(name, "*") {
			glob = true
		}
	}
	all := map[string]bool{}
	for name := range before {
		all[name] = true
	}
	for name := range after {
		all[name] = true
	}
	keys := make([]string, 0, len(all))
	for name := range all {
		if glob || named[name] {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	changes := value.NewMap(len(keys))
	for _, name := range keys {
		old, hadOld := before[name]
		neu, hasNew := after[name]
		if hadOld == hasNew && old.equal(neu) {
			continue
		}
		var o, n any
		if hadOld {
			o = old.toValue()
		}
		if hasNew {
			n = neu.toValue()
		}
		changes.Set(name, value.MapOf("old", o, "new", n))
	}
	return changes
}

// dnfModuleState is one modules.d section.
type dnfModuleState struct {
	Stream   string
	Profiles []string
	State    string
}

func (s dnfModuleState) equal(o dnfModuleState) bool {
	return s.Stream == o.Stream && s.State == o.State &&
		strings.Join(s.Profiles, ",") == strings.Join(o.Profiles, ",")
}

func (s dnfModuleState) toValue() *value.Map {
	profiles := make([]any, 0, len(s.Profiles))
	for _, p := range s.Profiles {
		profiles = append(profiles, p)
	}
	return value.MapOf("stream", s.Stream, "profiles", profiles, "state", s.State)
}

func dnfModuleStatusFn(c *exec.Context, args *value.Map) (any, error) {
	status, err := dnfModuleReadStatus(DnfModulesDir)
	if err != nil {
		return nil, err
	}
	only := states.Str(args, "name", "")
	names := make([]string, 0, len(status))
	for name := range status {
		if only == "" || name == only {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := value.NewMap(len(names))
	for _, name := range names {
		out.Set(name, status[name].toValue())
	}
	return out, nil
}

// dnfModuleReadStatus reads every *.module file under dir.
//
// # A file is not a choice
//
// `dnf module reset` does not delete the file. Measured on both hosts,
// it rewrites it to
//
//	[nginx]
//	name=nginx
//	stream=
//	profiles=
//	state=
//
// and `dnf module list` then shows no marker for the module, exactly as
// for one never touched. So an empty `state` is dropped here, and "reset"
// and "never touched" read the same -- as dnf itself reads them -- rather
// than "is there a file" becoming a second, wrong answer to "has this
// host chosen".
//
// A missing directory is an empty answer, not an error: rocky9 shipped
// with the directory present and empty, but a host whose dnf has never
// written one has made no choices either.
func dnfModuleReadStatus(dir string) (map[string]dnfModuleState, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.module"))
	if err != nil {
		return nil, err
	}
	out := map[string]dnfModuleState{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		for name, st := range dnfModuleParseStatus(string(data)) {
			if st.State != "" {
				out[name] = st
			}
		}
	}
	return out, nil
}

// dnfModuleParseStatus reads one modules.d file. The section header is
// the module's name; `name=` repeats it and is not trusted over the
// header. `profiles=` is split on commas: one installed profile is what
// was captured (`profiles=common` on both hosts), and the comma is
// libdnf's list separator for this key -- a file with two installed
// profiles was not produced on either host, which evidence.go says.
func dnfModuleParseStatus(text string) map[string]dnfModuleState {
	out := map[string]dnfModuleState{}
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			out[section] = dnfModuleState{}
			continue
		}
		if section == "" {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		st := out[section]
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "stream":
			st.Stream = val
		case "state":
			st.State = val
		case "profiles":
			st.Profiles = nil
			for _, p := range strings.Split(val, ",") {
				if p = strings.TrimSpace(p); p != "" {
					st.Profiles = append(st.Profiles, p)
				}
			}
		}
		out[section] = st
	}
	return out
}

func dnfModuleListFn(c *exec.Context, args *value.Map) (any, error) {
	if err := dnfPresent(c); err != nil {
		return nil, err
	}
	argv, err := dnfModuleListArgv(states.Str(args, "name", ""), states.Str(args, "filter", ""))
	if err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("`%s` could not be run on this node: %w", exec.Command{Argv: argv}.String(), err)
	}
	// A name that matches nothing is exit 1 with this exact message on
	// both dnf 4.7 and 4.14, where a filter that matches nothing is exit
	// 0 and no output at all. Both mean "no such streams", which for a
	// listing is an empty list rather than a failure.
	if res.Code != 0 {
		if strings.Contains(res.Stderr, "No matching Modules to list") {
			return []any{}, nil
		}
		return nil, fmt.Errorf("`%s` exited %d: %s", exec.Command{Argv: argv}.String(), res.Code,
			strings.TrimSpace(res.Stderr))
	}
	streams, err := dnfModuleParseList(res.Stdout)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(streams))
	for _, s := range streams {
		out = append(out, s.toValue())
	}
	return out, nil
}

func dnfModuleListArgv(name, filter string) ([]string, error) {
	argv := []string{"dnf", "-q", "module", "list"}
	switch filter {
	case "":
	case "enabled", "disabled", "installed":
		argv = append(argv, "--"+filter)
	default:
		return nil, fmt.Errorf("filter %q is not one of enabled, disabled, installed", filter)
	}
	if name != "" {
		if err := dnfModuleValidate([]string{name}); err != nil {
			return nil, err
		}
		argv = append(argv, name)
	}
	return argv, nil
}

// dnfModuleStream is one row of `dnf module list`.
type dnfModuleStream struct {
	Repo, Name, Stream, Summary string
	Default, Enabled, Disabled  bool
	Profiles                    []string
	DefaultProfiles             []string
	InstalledProfiles           []string
}

func (s dnfModuleStream) toValue() *value.Map {
	list := func(items []string) []any {
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, item)
		}
		return out
	}
	return value.MapOf(
		"repo", s.Repo,
		"name", s.Name,
		"stream", s.Stream,
		"summary", s.Summary,
		"default", s.Default,
		"enabled", s.Enabled,
		"disabled", s.Disabled,
		"installed", len(s.InstalledProfiles) > 0,
		"profiles", list(s.Profiles),
		"default_profiles", list(s.DefaultProfiles),
		"installed_profiles", list(s.InstalledProfiles),
	)
}

// dnfModuleParseList reads `dnf -q module list`.
//
// # What the real table looks like
//
// Captured on both hosts (testdata/dnf_module), one table per repository:
//
//	AlmaLinux 8 - AppStream
//	Name                 Stream          Profiles                                 Summary
//	389-ds               1.4                                                      389 Directory Server (base)
//	container-tools      rhel8 [d][e]    common [d]                               Most recent (rolling) ...
//	idm                  DL1             adtrust, client, common [d], dns, server The Red Hat ...
//	redis                6 [e]           common [d] [i]                           Redis persistent ...
//
//	Hint: [d]efault, [e]nabled, [x]disabled, [i]nstalled
//
// # Why the columns are read by offset
//
// Splitting on whitespace cannot work: the Profiles cell is a comma
// list with spaces in it, the Summary is prose, and `389-ds` above has
// an empty Profiles cell, so a row's third word is sometimes a profile
// and sometimes the summary. The header is padded to exactly the width
// of each column -- on both dnf 4.7 and 4.14, and the widths differ
// between every invocation, since they fit that table's content -- so
// where "Stream", "Profiles" and "Summary" begin in the header is where
// those cells begin in every row beneath it. A name cell holding a space
// means that assumption broke, and the parser refuses rather than
// return a table silently shifted by a column.
//
// Two marker spellings, both real: stream markers are run together
// (`rhel8 [d][e]`, `1.14 [d][x]`), profile markers are space-separated
// (`common [d] [i]`). `-q` matters: without it dnf 4.7 printed a
// "Modular dependency problem" block and a metadata-age line above the
// first table when a module was disabled that a default stream needed;
// with it stdout carried the tables alone on both versions. The parser
// still skips anything that is not inside a table.
func dnfModuleParseList(out string) ([]dnfModuleStream, error) {
	lines := strings.Split(out, "\n")
	var streams []dnfModuleStream
	for i := 0; i < len(lines); i++ {
		cols, ok := dnfModuleHeader(lines[i])
		if !ok {
			continue
		}
		repo := ""
		if i > 0 {
			repo = strings.TrimSpace(lines[i-1])
		}
		for i++; i < len(lines) && strings.TrimSpace(lines[i]) != ""; i++ {
			row, err := dnfModuleParseRow(lines[i], cols)
			if err != nil {
				return nil, err
			}
			row.Repo = repo
			streams = append(streams, row)
		}
	}
	return streams, nil
}

type dnfModuleColumns struct{ stream, profiles, summary int }

func dnfModuleHeader(line string) (dnfModuleColumns, bool) {
	fields := strings.Fields(line)
	if len(fields) != 4 || fields[0] != "Name" || fields[1] != "Stream" ||
		fields[2] != "Profiles" || fields[3] != "Summary" || !strings.HasPrefix(line, "Name") {
		return dnfModuleColumns{}, false
	}
	return dnfModuleColumns{
		stream:   strings.Index(line, "Stream"),
		profiles: strings.Index(line, "Profiles"),
		summary:  strings.Index(line, "Summary"),
	}, true
}

// dnfModuleCell returns line[from:to] trimmed, tolerating a row shorter than the
// header -- a trailing empty cell is not always padded.
func dnfModuleCell(line string, from, to int) string {
	if from >= len(line) {
		return ""
	}
	if to < 0 || to > len(line) {
		to = len(line)
	}
	return strings.TrimSpace(line[from:to])
}

func dnfModuleParseRow(line string, cols dnfModuleColumns) (dnfModuleStream, error) {
	name := dnfModuleCell(line, 0, cols.stream)
	streamCell := dnfModuleCell(line, cols.stream, cols.profiles)
	if name == "" || strings.ContainsAny(name, " \t") || streamCell == "" {
		return dnfModuleStream{}, fmt.Errorf("`dnf module list` row %q does not fit its header's columns", line)
	}
	row := dnfModuleStream{Name: name, Summary: dnfModuleCell(line, cols.summary, -1)}

	stream, markers, _ := strings.Cut(streamCell, " ")
	row.Stream = stream
	markers = strings.ReplaceAll(markers, " ", "")
	for markers != "" {
		if len(markers) < 3 || markers[0] != '[' || markers[2] != ']' {
			return dnfModuleStream{}, fmt.Errorf("`dnf module list` row %q has a stream marker this parser does not know", line)
		}
		switch markers[1] {
		case 'd':
			row.Default = true
		case 'e':
			row.Enabled = true
		case 'x':
			row.Disabled = true
		default:
			return dnfModuleStream{}, fmt.Errorf("`dnf module list` row %q has a stream marker this parser does not know", line)
		}
		markers = markers[3:]
	}

	profilesCell := dnfModuleCell(line, cols.profiles, cols.summary)
	if profilesCell == "" {
		return row, nil
	}
	for _, entry := range strings.Split(profilesCell, ",") {
		words := strings.Fields(entry)
		if len(words) == 0 {
			continue
		}
		profile := words[0]
		row.Profiles = append(row.Profiles, profile)
		for _, marker := range words[1:] {
			switch marker {
			case "[d]":
				row.DefaultProfiles = append(row.DefaultProfiles, profile)
			case "[i]":
				row.InstalledProfiles = append(row.InstalledProfiles, profile)
			default:
				return dnfModuleStream{}, fmt.Errorf("`dnf module list` row %q has a profile marker this parser does not know", line)
			}
		}
	}
	return row, nil
}
