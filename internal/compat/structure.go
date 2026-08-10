package compat

import (
	"fmt"
	"strings"

	"github.com/edlitmus/halite/internal/modules"
	"github.com/edlitmus/halite/internal/sls"
	"github.com/edlitmus/halite/internal/yamlite"
)

// structure checks a parsed SLS tree: the shape of the file, the state
// declarations it holds, the requisites and arguments on them, and — for a
// top file — its targets.
func (s *Scanner) structure(fr *FileReport, tree any, kind Kind, root string, external map[string]bool) {
	m, ok := tree.(*yamlite.Map)
	if !ok {
		fr.Findings = append(fr.Findings, Finding{
			Severity: SevError, Code: "sls-shape",
			Message: "the top level of the file is not a mapping",
		})
		return
	}
	if strings.HasSuffix(fr.Kind, "top") {
		s.topStructure(fr, m, root, kind)
		return
	}
	s.includes(fr, m, root)
	if kind == KindPillar {
		return // pillar files are data: any mapping is valid
	}
	for _, id := range m.Keys {
		switch id {
		case "include":
			continue
		case "extend":
			fr.Findings = append(fr.Findings, Finding{
				Severity: SevError, Code: "extend",
				Message: "extend: is not supported",
				Hint:    "declare the override in the state itself, or split the SLS file",
			})
			continue
		case "exclude":
			fr.Findings = append(fr.Findings, Finding{
				Severity: SevError, Code: "exclude",
				Message: "exclude: is not supported",
				Hint:    "leave the SLS name out of the top file instead",
			})
			continue
		}
		body, ok := m.Vals[id].(*yamlite.Map)
		if !ok {
			fr.Findings = append(fr.Findings, Finding{
				Severity: SevError, Code: "sls-shape",
				Message: fmt.Sprintf("state %q: body is not a mapping of module functions", id),
			})
			continue
		}
		for _, fn := range body.Keys {
			s.declaration(fr, id, fn, body.Vals[fn], external)
		}
	}
}

// declaration checks one "module.function" block inside a state.
func (s *Scanner) declaration(fr *FileReport, id, fn string, body any, external map[string]bool) {
	if !strings.Contains(fn, ".") {
		if short, ok := shortForm(body); ok {
			fr.Findings = append(fr.Findings, Finding{
				Severity: SevError, Code: "short-declaration",
				Message: fmt.Sprintf("state %q declares %q with the function in the argument list", id, fn),
				Hint:    fmt.Sprintf("halite reads only the dotted form: write %s.%s", fn, short),
			})
			return
		}
		fr.Findings = append(fr.Findings, Finding{
			Severity: SevError, Code: "sls-shape",
			Message: fmt.Sprintf("state %q: %q is not of the form module.function", id, fn),
		})
		return
	}

	use := Use{Name: fn}
	module := fn[:strings.Index(fn, ".")]
	switch {
	case modules.Registry[fn] != nil:
		use.Supported = true
	case external[module]:
		use.Supported, use.External = true, true
	default:
		fr.Findings = append(fr.Findings, unsupportedModule(id, fn))
	}
	fr.Uses = append(fr.Uses, use)

	for _, arg := range stateArgs(body) {
		if f, ok := argFinding(id, fn, arg, use); ok {
			fr.Findings = append(fr.Findings, f)
		}
	}
}

// shortForm recognises Salt's undotted declaration, where the function is
// the first entry of the argument list:
//
//	nginx:
//	  pkg:
//	    - installed
func shortForm(body any) (string, bool) {
	list, ok := body.([]any)
	if !ok || len(list) == 0 {
		return "", false
	}
	name, ok := list[0].(string)
	return name, ok
}

// argEntry is one key/value pair from a state's argument block.
type argEntry struct {
	Key   string
	Value any
}

// stateArgs flattens an argument block the way the state compiler does, but
// keeps requisites in place and tolerates shapes the compiler rejects: the
// checker has to keep going where the compiler stops.
func stateArgs(v any) []argEntry {
	var out []argEntry
	addMap := func(m *yamlite.Map) {
		for _, k := range m.Keys {
			out = append(out, argEntry{Key: k, Value: m.Vals[k]})
		}
	}
	switch t := v.(type) {
	case *yamlite.Map:
		addMap(t)
	case []any:
		for _, item := range t {
			switch it := item.(type) {
			case *yamlite.Map:
				addMap(it)
			case string:
				out = append(out, argEntry{Key: it, Value: "true"})
			}
		}
	}
	return out
}

// argFinding checks one argument of a state declaration.
func argFinding(id, fn string, arg argEntry, use Use) (Finding, bool) {
	at := func(sev Severity, code, msg, hint string) (Finding, bool) {
		return Finding{Severity: sev, Code: code,
			Message: fmt.Sprintf("state %q (%s): %s", id, fn, msg), Hint: hint}, true
	}
	switch arg.Key {
	case "require", "watch", "onchanges", "prereq", "name", "unless", "onlyif", "creates":
		return Finding{}, false
	}
	if note, ok := unsupportedRequisites[arg.Key]; ok {
		return at(note.severity, "unsupported-requisite", arg.Key+" is not implemented", note.hint)
	}
	if hint, ok := behaviourArgs[fn+":"+arg.Key]; ok {
		return at(SevError, "ignored-argument", arg.Key+" is not implemented", hint)
	}
	if f, ok := valueFinding(id, fn, arg); ok {
		return f, true
	}
	if !use.Supported || use.External {
		return Finding{}, false // nothing to check the argument against
	}
	if known, ok := knownArgs[fn]; ok && !contains(known, arg.Key) {
		return at(SevWarn, "ignored-argument",
			fmt.Sprintf("%s is not an argument of %s", arg.Key, fn),
			fmt.Sprintf("halite ignores it; %s takes: %s", fn, strings.Join(known, ", ")))
	}
	return Finding{}, false
}

// valueFinding checks argument values whose form matters: source URIs and
// the template selector.
func valueFinding(id, fn string, arg argEntry) (Finding, bool) {
	val, _ := arg.Value.(string)
	at := func(code, msg, hint string) (Finding, bool) {
		return Finding{Severity: SevError, Code: code,
			Message: fmt.Sprintf("state %q (%s): %s", id, fn, msg), Hint: hint}, true
	}
	switch {
	case arg.Key == "source" && strings.HasPrefix(val, "salt://"):
		return at("salt-uri", "source "+val+" uses a salt:// URI",
			"halite ships the whole tree and resolves sources relative to the SLS file: drop the salt:// prefix")
	case arg.Key == "source" && fn == "file.managed" && isRemoteURI(val):
		return at("remote-source", "source "+val+" is remote",
			"file.managed reads local paths only; fetch it with archive.extracted or cmd.run")
	case arg.Key == "template" && val != "" && val != "true" && val != "go":
		return at("template-renderer", fmt.Sprintf("template: %s", val),
			"halite renders sources with Go text/template under 'template: true'; any other value renders nothing at all")
	}
	return Finding{}, false
}

func isRemoteURI(val string) bool {
	for _, scheme := range []string{"http://", "https://", "ftp://", "s3://", "swift://"} {
		if strings.HasPrefix(val, scheme) {
			return true
		}
	}
	return false
}

// unsupportedModule reports a state function halite does not implement,
// with a specific note for the ones a Salt tree is most likely to hold.
func unsupportedModule(id, fn string) Finding {
	hint, ok := moduleHints[fn]
	if !ok {
		hint = moduleHints[fn[:strings.Index(fn, ".")]+".*"]
	}
	if hint == "" {
		hint = "not implemented; run it with cmd.run, or ship an executable in _modules/ (docs/external-modules.md)"
	}
	return Finding{
		Severity: SevError, Code: "unsupported-module",
		Message: fmt.Sprintf("state %q: %s is not a halite state module", id, fn),
		Hint:    hint,
	}
}

// moduleHints answers "then what do I do?" for the Salt state functions a
// real tree most often uses. A "<module>.*" key covers a whole module.
var moduleHints = map[string]string{
	"file.symlink":       "not implemented; cmd.run with 'creates:' is the usual stand-in",
	"file.line":          "halite manages whole files: use file.managed with contents or a source",
	"file.replace":       "halite manages whole files: use file.managed with contents or a source",
	"file.append":        "halite manages whole files: use file.managed with contents or a source",
	"file.blockreplace":  "halite manages whole files: use file.managed with contents or a source",
	"file.recurse":       "not implemented; declare the files, or unpack them with archive.extracted",
	"file.touch":         "file.managed creates an empty file when given neither contents nor source",
	"pkg.latest":         "halite installs what the backend considers current; pkg.installed is the only form",
	"pkg.purged":         "pkg.removed is the only removal form",
	"pkgrepo.*":          "repository management is not implemented; manage the repo file with file.managed",
	"service.enabled":    "service.running takes 'enable: true'",
	"service.disabled":   "not implemented; service.dead stops the service",
	"host.present":       "manage /etc/hosts with file.managed",
	"network.managed":    "a deliberate non-goal: it is too OS-entangled (docs/salt-parity.md)",
	"selinux.*":          "not implemented; drive it from cmd.run",
	"firewalld.*":        "not implemented; manage the rules as files and reload the service",
	"pip.*":              "not implemented; run pip from cmd.run with 'creates:' or 'unless:'",
	"virtualenv.*":       "not implemented; run it from cmd.run with 'creates:'",
	"npm.*":              "not implemented; run npm from cmd.run with 'creates:' or 'unless:'",
	"docker_container.*": "not implemented; drive docker from cmd.run, or write an external module",
	"ssh_auth.*":         "manage authorized_keys with file.managed",
	"timezone.*":         "not implemented; set it from cmd.run",
	"module.run":         "there are no Python execution modules to call; use cmd.run or an external module",
	"test.*":             "no test module; the closest equivalent is cmd.run with 'unless:'",
}

// requisiteNote is what halite does with a requisite it does not implement.
type requisiteNote struct {
	severity Severity
	hint     string
}

// unsupportedRequisites are the state-level keys Salt gives meaning to and
// halite does not. They reach the module as ordinary arguments, where they
// are ignored — so each one is a silent difference until it is reported.
var unsupportedRequisites = map[string]requisiteNote{
	"require_in":     {SevError, "invert it: declare 'require' on the state that must run later"},
	"watch_in":       {SevError, "invert it: declare 'watch' on the state that reacts"},
	"onchanges_in":   {SevError, "invert it: declare 'onchanges' on the state that reacts"},
	"prereq_in":      {SevError, "invert it: declare 'prereq' on the state that runs first"},
	"onfail":         {SevError, "not implemented; a failed state already blocks the states that require it"},
	"onfail_in":      {SevError, "not implemented; a failed state already blocks the states that require it"},
	"onfail_any":     {SevError, "not implemented"},
	"onfail_all":     {SevError, "not implemented"},
	"require_any":    {SevError, "halite requisites are all-of; split the state or drop the alternatives"},
	"watch_any":      {SevError, "halite requisites are all-of; split the state or drop the alternatives"},
	"onchanges_any":  {SevError, "halite requisites are all-of; split the state or drop the alternatives"},
	"use":            {SevError, "arguments are not copied between states; write them out"},
	"use_in":         {SevError, "arguments are not copied between states; write them out"},
	"listen":         {SevError, "not implemented; 'watch' runs the reaction in requisite order"},
	"listen_in":      {SevError, "not implemented; 'watch' runs the reaction in requisite order"},
	"names":          {SevError, "multi-name expansion is not implemented; declare one state per name"},
	"check_cmd":      {SevError, "not implemented; follow the state with a cmd.run"},
	"retry":          {SevError, "retries are not implemented"},
	"failhard":       {SevWarn, "halite blocks the dependents of a failed state and runs the rest"},
	"order":          {SevWarn, "halite orders states by requisites, then by declaration order"},
	"parallel":       {SevWarn, "states run one at a time, in order"},
	"reload_modules": {SevWarn, "there are no Python modules to reload"},
	"aggregate":      {SevWarn, "package installs are not aggregated"},
	"runas":          {SevWarn, "halite runs as the invoking user; wrap the command in su(1)"},
}

// behaviourArgs are arguments halite has no equivalent for and would ignore
// silently, changing what the state does rather than doing less of it.
var behaviourArgs = map[string]string{
	"pkg.installed:version":         "version pinning is not implemented: halite installs what the backend considers current",
	"pkg.installed:sources":         "installing from explicit package files is not implemented",
	"pkg.installed:fromrepo":        "alternate repositories are not implemented",
	"pkg.installed:hold":            "package holds are not implemented",
	"file.managed:replace":          "halite always writes the managed contents to an existing file",
	"file.managed:source_hash":      "halite does not verify a source hash for file.managed (archive.extracted requires one for remote sources)",
	"file.managed:backup":           "halite keeps no backup copies",
	"file.managed:context":          "template variables are the grains, pillar and mine trees",
	"file.managed:defaults":         "template variables are the grains, pillar and mine trees",
	"file.directory:recurse":        "halite does not walk a directory to fix ownership or modes",
	"file.directory:clean":          "halite does not remove unmanaged files",
	"file.directory:file_mode":      "halite applies 'mode' to the directory only",
	"cmd.run:runas":                 "halite runs as the invoking user; wrap the command in su(1)",
	"cmd.run:shell":                 "commands run through the platform shell: sh -c, or cmd /C on Windows",
	"cmd.run:timeout":               "commands are not bounded by a timeout",
	"cmd.run:stateful":              "stateful command output is not parsed",
	"user.present:password":         "halite does not set passwords",
	"user.present:enforce_password": "halite does not manage passwords",
	"service.running:reload":        "halite restarts a watched service; reload is not implemented",
	"service.running:sig":           "signals are not implemented",
	"service.running:init_delay":    "not implemented; follow the service with a cmd.run",
	"git.latest:submodules":         "submodules are not updated",
	"git.latest:user":               "halite runs git as the invoking user",
}

// knownArgs is the argument each state function reads. Anything else on a
// supported module is accepted by the parser and then ignored.
var knownArgs = map[string][]string{
	"file.managed":      {"name", "contents", "source", "mode", "makedirs", "show_diff", "user", "group", "template"},
	"file.directory":    {"name", "mode", "user", "group"},
	"file.absent":       {"name"},
	"pkg.installed":     {"name", "pkgs"},
	"pkg.removed":       {"name", "pkgs"},
	"service.running":   {"name", "enable"},
	"service.dead":      {"name"},
	"cmd.run":           {"name", "cwd", "env"},
	"cmd.wait":          {"name", "cwd", "env"},
	"user.present":      {"name", "uid", "shell", "home", "gecos", "groups", "createhome", "system"},
	"user.absent":       {"name", "purge"},
	"group.present":     {"name", "gid"},
	"group.absent":      {"name"},
	"cron.present":      {"name", "minute", "hour", "daymonth", "month", "dayweek", "identifier", "user"},
	"cron.absent":       {"name", "identifier", "user"},
	"sysctl.present":    {"name", "value", "persist", "config"},
	"archive.extracted": {"name", "source", "source_hash", "if_missing", "archive_format"},
	"git.latest":        {"name", "target", "rev", "depth", "force"},
	"mount.mounted":     {"name", "device", "fstype", "opts", "dump", "pass", "mkmnt", "persist"},
	"mount.unmounted":   {"name", "persist"},
	"reg.present":       {"name", "vname", "vdata", "vtype"},
	"reg.absent":        {"name", "vname", "delete_key"},
}

// includes checks that every included SLS name resolves under the root.
func (s *Scanner) includes(fr *FileReport, m *yamlite.Map, root string) {
	list, ok := m.Vals["include"].([]any)
	if !ok {
		return
	}
	for _, item := range list {
		name, ok := item.(string)
		if !ok {
			continue
		}
		if strings.HasPrefix(name, ".") {
			fr.Findings = append(fr.Findings, Finding{
				Severity: SevError, Code: "relative-include",
				Message: fmt.Sprintf("include %q is relative to the current SLS file", name),
				Hint:    "halite resolves include names from the tree root; write the full dotted name",
			})
			continue
		}
		if _, err := sls.ResolveName(root, name); err != nil {
			fr.Findings = append(fr.Findings, Finding{
				Severity: SevError, Code: "missing-include",
				Message: fmt.Sprintf("include %q does not resolve under %s", name, root),
			})
		}
	}
}

// topStructure checks a top file: its environments, its target patterns,
// and whether the SLS names it selects exist.
func (s *Scanner) topStructure(fr *FileReport, m *yamlite.Map, root string, kind Kind) {
	for _, env := range m.Keys {
		if env != "base" {
			fr.Findings = append(fr.Findings, Finding{
				Severity: SevInfo, Code: "top-environment",
				Message: fmt.Sprintf("environment %q", env),
				Hint:    "halite has no environment selection: every environment in a top file is applied",
			})
		}
		body, ok := m.Vals[env].(*yamlite.Map)
		if !ok {
			fr.Findings = append(fr.Findings, Finding{
				Severity: SevError, Code: "top-shape",
				Message: fmt.Sprintf("environment %q is not a mapping of targets", env),
			})
			continue
		}
		for _, pattern := range body.Keys {
			if f, ok := targetFinding(pattern); ok {
				fr.Findings = append(fr.Findings, f)
			}
			names, ok := body.Vals[pattern].([]any)
			if !ok {
				fr.Findings = append(fr.Findings, Finding{
					Severity: SevError, Code: "top-shape",
					Message: fmt.Sprintf("target %q does not map to a list of sls names", pattern),
				})
				continue
			}
			s.topNames(fr, pattern, names, root)
		}
	}
	if matched, err := sls.MatchTop(m, s.Grains); err == nil {
		fr.Matched = matched
	}
	if kind == KindState && len(fr.Matched) == 0 {
		fr.Findings = append(fr.Findings, Finding{
			Severity: SevInfo, Code: "top-no-match",
			Message: "no target in this top file matches the scanning host",
			Hint:    "a highstate here would report that nothing matched; check it again on a host the tree targets",
		})
	}
}

// topNames checks the SLS names under one target.
func (s *Scanner) topNames(fr *FileReport, pattern string, names []any, root string) {
	for _, item := range names {
		switch name := item.(type) {
		case string:
			if _, err := sls.ResolveName(root, name); err != nil {
				fr.Findings = append(fr.Findings, Finding{
					Severity: SevError, Code: "missing-sls",
					Message: fmt.Sprintf("target %q names %q, which does not resolve under %s", pattern, name, root),
				})
			}
		case *yamlite.Map:
			fr.Findings = append(fr.Findings, Finding{
				Severity: SevError, Code: "top-match-directive",
				Message: fmt.Sprintf("target %q uses a match directive", pattern),
				Hint:    "halite has one target language: '*', 'grain:glob', or a glob on the host id",
			})
		}
	}
}

// targetFinding checks a top-file target against halite's target language:
// '*', 'grain:valueglob', or a glob on the host id.
func targetFinding(pattern string) (Finding, bool) {
	at := func(code, msg, hint string) (Finding, bool) {
		return Finding{Severity: SevError, Code: code,
			Message: fmt.Sprintf("target %q: %s", pattern, msg), Hint: hint}, true
	}
	hint := "halite targets are '*', 'grain:valueglob' (e.g. 'os_family:FreeBSD'), or a glob on the host id"
	switch {
	case strings.Contains(pattern, "@"):
		return at("compound-target", "compound matchers (G@, I@, P@, E@, L@, N@, S@) are not implemented", hint)
	case strings.Contains(pattern, " and "), strings.Contains(pattern, " or "),
		strings.HasPrefix(pattern, "not "), strings.Contains(pattern, " not "):
		return at("compound-target", "boolean target expressions are not implemented", hint)
	}
	return Finding{}, false
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
