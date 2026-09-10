package builtin

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// PamDir is the directory holding one file per PAM service. A variable
// so a test can point it at a tree it owns.
var PamDir = "/etc/pam.d"

// pamPlatforms is every platform with a /etc/pam.d.
//
// Windows is the only one this build targets that has no PAM at all;
// everything else in the list has either Linux-PAM or OpenPAM, and the
// two agree on the file format even where they disagree on which
// modules exist. The BSDs and macOS are in the list deliberately rather
// than by accident of `unixOnly`: this module is filed under SPEC 15.3's
// Common Linux row, and it would be wrong to let that filing decide
// that a FreeBSD node cannot be asked what authenticates a login on it.
var pamPlatforms = []string{"linux", "freebsd", "openbsd", "netbsd", "dragonfly", "darwin"}

// pamTypes are the four chains a service has, in the order PAM
// evaluates them within a request.
//
// `-auth` and the other leading-dash spellings are Linux-PAM's "do not
// complain if this module is missing" and are the same type; the dash is
// stripped into Optional below rather than becoming a fifth type.
var pamTypes = map[string]bool{
	"auth": true, "account": true, "session": true, "password": true,
}

// pamRule is one line of a service's configuration.
type pamRule struct {
	// Type is auth, account, session or password.
	Type string
	// Control is the flag, which is either one word — required,
	// requisite, sufficient, optional, binding, include, substack — or
	// Linux-PAM's bracketed form, kept whole with its brackets.
	Control string
	// Module is the module path, such as pam_unix.so. For an `include`
	// or `substack` control it is the *service* being pulled in, not a
	// shared object, which is the distinction pamResolve turns on.
	Module string
	// Args are the module's own options.
	Args []string
	// Optional records Linux-PAM's leading dash on the type: the module
	// is loaded if it exists and its absence is not an error.
	Optional bool
	// File is where the rule was read from, and Line is its 1-based
	// position in that file. Both survive include resolution, which is
	// the whole point of carrying them: a rule that arrived through two
	// levels of `@include` still says which file an operator must edit.
	File string
	Line int
}

// pamAtInclude is Debian's whole-file include. It is a different
// mechanism from the `include` control flag, not a spelling of it:
// `@include common-auth` pulls in every type at once and takes no type
// of its own, while `auth include system` pulls in one chain. Debian
// uses the first and FreeBSD and the RHEL family use the second, so a
// reader that knows only one of them silently reports a truncated chain
// on the other half of any real estate.
const pamAtInclude = "@include"

// registerPam installs the `pam` module of SPEC 15.3's Common Linux row.
//
// # What this does that reading the file does not
//
// A PAM service is almost never one file. `su` on FreeBSD is four lines
// and an `auth include system`; `sshd` on Debian is mostly `@include`
// of the four `common-*` files. So the question an operator actually
// has — "what authenticates an ssh login on this node" — is not
// answered by any single file, and the two include mechanisms are
// spelled differently on the two halves of a mixed estate.
// `pam.rules` resolves both and keeps the origin of every rule, so the
// answer names the file to edit rather than the file that was asked
// about.
//
// The other thing it does is ask the question in reverse.
// `pam.services_using` sweeps every service for a module, which is how
// "is pam_faillock actually enforced here" gets answered; grepping the
// directory misses a rule that arrives through an include and counts
// commented-out lines, and this misses neither.
//
// # Why the mutating half is as narrow as it is
//
// A wrong edit to /etc/pam.d locks every account out of the node,
// including the one that would repair it, and PAM has no dry run. So
// `set_module` refuses to guess: the caller names the type, the module
// and where in the chain it goes, the file is rewritten atomically, and
// the result is re-read and re-parsed before the old bytes are let go.
// `remove_module` will not empty a chain, because a service whose auth
// chain has no rules denies everyone.
//
// There is deliberately no `pam` state. SPEC 15.5 names none, and the
// convergence a state implies is the wrong shape here: the thing a tree
// wants to assert about PAM is usually the whole file, which
// `file.managed` already does with a template and a backup.
func registerPam(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "pam", Function: "list_services",
				Doc:       "Return the name of every PAM service configured on this node.",
				TestMode:  signature.TestNotApplicable,
				Platforms: pamPlatforms,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				names, err := pamServices()
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
		exec.Module{
			Sig: signature.Signature{
				Module: "pam", Function: "read_file",
				Doc: "Parse one PAM configuration file and return its rules in order.",
				Params: []signature.Param{
					req("file_name", signature.Path, "The file to read. A bare name is taken as a service in "+PamDir+"."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: pamPlatforms,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				path := pamPath(states.Str(args, "file_name", ""))
				rules, err := pamReadFile(path)
				if err != nil {
					return nil, err
				}
				return pamRulesValue(rules), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pam", Function: "rules",
				Doc: "Return one service's rules, following its includes so the answer is the chain PAM will actually evaluate.",
				Params: []signature.Param{
					req("service", signature.String, "The service, such as sshd."),
					opt("resolve_includes", signature.Bool, true,
						"Follow `include`, `substack` and `@include` into the services they name. With this false the include rules are returned as they are written."),
					opt("type", signature.String, "",
						"Return only this chain: auth, account, session or password. Empty means all four."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: pamPlatforms,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				service := states.Str(args, "service", "")
				chain := strings.TrimSpace(states.Str(args, "type", ""))
				if chain != "" && !pamTypes[chain] {
					return nil, fmt.Errorf("%q is not a PAM chain; it is one of auth, account, session or password", chain)
				}
				rules, err := pamServiceRules(service, states.Bool(args, "resolve_includes", true))
				if err != nil {
					return nil, err
				}
				if chain != "" {
					rules = pamOfType(rules, chain)
				}
				return pamRulesValue(rules), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pam", Function: "has_module",
				Doc: "Report whether a service's chain reaches a module, following includes.",
				Params: []signature.Param{
					req("service", signature.String, "The service, such as sshd."),
					req("module", signature.String, "The module, such as pam_unix.so. A bare name gains the .so."),
					opt("type", signature.String, "", "Limit the question to one chain. Empty means any of the four."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: pamPlatforms,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				rules, err := pamServiceRules(states.Str(args, "service", ""), true)
				if err != nil {
					return nil, err
				}
				chain := strings.TrimSpace(states.Str(args, "type", ""))
				if chain != "" && !pamTypes[chain] {
					return nil, fmt.Errorf("%q is not a PAM chain; it is one of auth, account, session or password", chain)
				}
				want := pamModuleName(states.Str(args, "module", ""))
				for _, rule := range rules {
					if chain != "" && rule.Type != chain {
						continue
					}
					if pamSameModule(rule.Module, want) {
						return true, nil
					}
				}
				return false, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pam", Function: "services_using",
				Doc: "Return every service whose chain reaches a module, and where each reference comes from.",
				Params: []signature.Param{
					req("module", signature.String, "The module, such as pam_faillock.so."),
					opt("type", signature.String, "", "Limit the sweep to one chain. Empty means any of the four."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: pamPlatforms,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				chain := strings.TrimSpace(states.Str(args, "type", ""))
				if chain != "" && !pamTypes[chain] {
					return nil, fmt.Errorf("%q is not a PAM chain; it is one of auth, account, session or password", chain)
				}
				return pamServicesUsing(pamModuleName(states.Str(args, "module", "")), chain)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pam", Function: "set_module",
				Doc: "Add a rule to a service's chain, or change the control flag and arguments of the one that is there.",
				Params: []signature.Param{
					req("service", signature.String, "The service, such as sshd."),
					req("type", signature.String, "The chain: auth, account, session or password."),
					req("module", signature.String, "The module, such as pam_faillock.so."),
					req("control", signature.String, "The control flag: required, requisite, sufficient, optional, binding, or Linux-PAM's bracketed form."),
					opt("args", signature.List, nil, "The module's own options, in order."),
					choice("position", "end",
						"Where a rule that is not there yet is placed within its chain.",
						"start", "end"),
					opt("optional", signature.Bool, false,
						"Write Linux-PAM's leading dash, so a node without the module logs and carries on rather than failing."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  pamPlatforms,
				Section:    "15.3",
			},
			Fn: pamSetModule,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pam", Function: "remove_module",
				Doc: "Remove a module from a service's chain. Refuses to leave the chain empty.",
				Params: []signature.Param{
					req("service", signature.String, "The service, such as sshd."),
					req("type", signature.String, "The chain: auth, account, session or password."),
					req("module", signature.String, "The module to remove."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  pamPlatforms,
				Section:    "15.3",
			},
			Fn: pamRemoveModule,
		},
	)
}

// ---- reading ----

// pamPath resolves what a caller named to a file.
//
// A bare word is a service, because that is what an operator says, and
// anything with a separator in it is a path, because that is what Salt's
// `pam.read_file` takes. Both spellings reach the same parser.
func pamPath(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return name
	}
	if strings.ContainsRune(name, os.PathSeparator) || strings.HasPrefix(name, "/") {
		return name
	}
	return filepath.Join(PamDir, name)
}

// pamServices lists the configured services.
//
// Subdirectories and PamDir's own README are skipped. The README is not
// a special case for FreeBSD's benefit: a file in this directory is a
// service definition to PAM whatever it is called, so the only honest
// filter is "is it a regular file", and README is excluded because
// FreeBSD ships one and no service is named after it.
func pamServices() ([]string, error) {
	entries, err := os.ReadDir(PamDir)
	if err != nil {
		return nil, fmt.Errorf("this node's PAM directory %s could not be read: %w", PamDir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == "README" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// pamReadFile parses one file.
func pamReadFile(path string) ([]pamRule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("this node has no PAM configuration at %s", path)
		}
		return nil, fmt.Errorf("%s could not be read: %w", path, err)
	}
	return pamParse(string(data), path)
}

// pamParse turns a file's text into rules.
//
// Three parts of the format are easy to get wrong and are the reason
// this is not a `strings.Fields` on each line.
//
//   - A backslash at the end of a line continues it. The rule is one
//     rule and the reported line number is where it started, because
//     that is the line an operator's editor has to reach.
//   - Linux-PAM's control flag can be a bracketed list of return-value
//     actions — `[success=1 default=ignore]` — which contains spaces.
//     Splitting on whitespace tears it into three fields and files
//     `default=ignore]` as the module path.
//   - Debian's `@include` is a whole line with no type, so it is
//     recognised before the field split rather than inside it.
//
// A line this parser cannot make sense of is skipped rather than
// refused. That is deliberate and is the one place this reader is
// lenient: PAM itself skips a malformed line with a syslog warning and
// carries on authenticating, so a reader that refused the file would
// report "there is no configuration" about a node that is happily
// logging people in.
func pamParse(text, path string) ([]pamRule, error) {
	var rules []pamRule
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		start := i + 1
		line := strings.TrimRight(lines[i], "\r")
		for strings.HasSuffix(strings.TrimRight(line, " \t"), `\`) && i+1 < len(lines) {
			trimmed := strings.TrimRight(line, " \t")
			line = trimmed[:len(trimmed)-1] + " " + strings.TrimRight(lines[i+1], "\r")
			i++
		}
		rule, ok := pamParseLine(line)
		if !ok {
			continue
		}
		rule.File = path
		rule.Line = start
		rules = append(rules, rule)
	}
	return rules, nil
}

// pamParseLine parses one logical line, which is a continuation-joined
// line by the time it arrives here.
func pamParseLine(line string) (pamRule, bool) {
	if cut := strings.IndexByte(line, '#'); cut >= 0 {
		line = line[:cut]
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return pamRule{}, false
	}

	if rest, ok := strings.CutPrefix(line, pamAtInclude); ok {
		service := strings.TrimSpace(rest)
		if service == "" {
			return pamRule{}, false
		}
		// Debian's whole-file include carries no type. The empty Type is
		// what pamResolve keys on: it pulls in every chain of the named
		// service rather than one.
		return pamRule{Control: pamAtInclude, Module: service}, true
	}

	fields, ok := pamFields(line)
	if !ok || len(fields) < 3 {
		return pamRule{}, false
	}
	typ, optional := strings.CutPrefix(fields[0], "-")
	typ = strings.ToLower(typ)
	if !pamTypes[typ] {
		return pamRule{}, false
	}
	return pamRule{
		Type:     typ,
		Control:  fields[1],
		Module:   fields[2],
		Args:     fields[3:],
		Optional: optional,
	}, true
}

// pamFields splits a rule into fields, keeping a bracketed control flag
// whole.
//
// The brackets do not nest and PAM does not escape them, so the state
// this needs is one bool. It reports failure for an unclosed bracket
// rather than returning the rest of the line as one field, because that
// is a line PAM itself will not act on and reporting it as a rule would
// describe a node that does not exist.
func pamFields(line string) ([]string, bool) {
	var (
		fields  []string
		current strings.Builder
		bracket bool
	)
	flush := func() {
		if current.Len() > 0 {
			fields = append(fields, current.String())
			current.Reset()
		}
	}
	for _, r := range line {
		switch {
		case r == '[' && !bracket:
			bracket = true
			current.WriteRune(r)
		case r == ']' && bracket:
			bracket = false
			current.WriteRune(r)
			flush()
		case (r == ' ' || r == '\t') && !bracket:
			flush()
		default:
			current.WriteRune(r)
		}
	}
	if bracket {
		return nil, false
	}
	flush()
	return fields, true
}

// pamServiceRules reads a service and, unless told not to, follows its
// includes.
func pamServiceRules(service string, resolve bool) ([]pamRule, error) {
	service = strings.TrimSpace(service)
	if service == "" {
		return nil, errors.New("a PAM service must be named")
	}
	rules, err := pamReadFile(pamPath(service))
	if err != nil {
		return nil, err
	}
	if !resolve {
		return rules, nil
	}
	return pamResolve(rules, map[string]bool{filepath.Base(pamPath(service)): true})
}

// pamResolve expands the include rules in place.
//
// `seen` breaks a cycle. PAM itself has a recursion limit and a service
// that includes itself is a configuration error rather than a hang, but
// this runs on an operator's node against files nobody has validated,
// and a reader that hung on a bad file would be worse than the file.
// A cycle keeps the include rule as written rather than dropping it, so
// the answer still shows where the loop is.
func pamResolve(rules []pamRule, seen map[string]bool) ([]pamRule, error) {
	out := make([]pamRule, 0, len(rules))
	for _, rule := range rules {
		target, chain, ok := pamIncludeTarget(rule)
		if !ok {
			out = append(out, rule)
			continue
		}
		if seen[target] {
			out = append(out, rule)
			continue
		}
		included, err := pamReadFile(filepath.Join(PamDir, target))
		if err != nil {
			// An include naming a service that is not there is how a
			// half-removed package leaves a node, and PAM treats it as
			// a failing module rather than as a parse error. Keeping
			// the rule as written says the same thing to a reader.
			out = append(out, rule)
			continue
		}
		seen[target] = true
		expanded, err := pamResolve(included, seen)
		delete(seen, target)
		if err != nil {
			return nil, err
		}
		for _, inc := range expanded {
			// A typed include pulls in only its own chain. An `@include`
			// has no type and pulls in all four, which is why the type
			// is copied from the includer only when there is one.
			if chain != "" && inc.Type != chain {
				continue
			}
			out = append(out, inc)
		}
	}
	return out, nil
}

// pamIncludeTarget reports whether a rule is an include, and if so which
// service it names and which chain it contributes to.
//
// The empty chain means every chain, which is Debian's `@include`.
func pamIncludeTarget(rule pamRule) (target, chain string, ok bool) {
	if rule.Control == pamAtInclude {
		return rule.Module, "", true
	}
	switch strings.ToLower(rule.Control) {
	case "include", "substack":
		return rule.Module, rule.Type, true
	}
	return "", "", false
}

// pamOfType keeps one chain.
func pamOfType(rules []pamRule, chain string) []pamRule {
	out := make([]pamRule, 0, len(rules))
	for _, r := range rules {
		if r.Type == chain {
			out = append(out, r)
		}
	}
	return out
}

// pamServicesUsing sweeps every service for a module.
func pamServicesUsing(module, chain string) (any, error) {
	services, err := pamServices()
	if err != nil {
		return nil, err
	}
	out := value.NewMap(0)
	for _, service := range services {
		rules, err := pamServiceRules(service, true)
		if err != nil {
			// A service whose file cannot be read is reported rather
			// than skipped: "pam_faillock is in none of them" would be
			// the wrong answer to give about a directory half of which
			// was unreadable.
			entry := value.NewMap(1)
			entry.Set("error", err.Error())
			out.Set(service, entry)
			continue
		}
		var hits []any
		for _, rule := range rules {
			if chain != "" && rule.Type != chain {
				continue
			}
			if !pamSameModule(rule.Module, module) {
				continue
			}
			hits = append(hits, pamRuleValue(rule))
		}
		if len(hits) > 0 {
			out.Set(service, hits)
		}
	}
	return out, nil
}

// pamModuleName is what a caller meant by a module.
//
// A bare `pam_unix` gains the `.so`, because that is how an operator
// says it and how every piece of documentation writes it in prose. A
// name that already has a suffix is left alone, and so is a path: Linux
// -PAM accepts an absolute module path and a node that uses one should
// still match.
func pamModuleName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsRune(name, '/') || strings.Contains(name, ".so") {
		return name
	}
	return name + ".so"
}

// pamSameModule compares two module references.
//
// A rule may name `pam_unix.so` or `/usr/lib/security/pam_unix.so` and
// they are the same module, so the comparison is on the base name. It is
// case sensitive because the filesystems that hold these are.
func pamSameModule(a, b string) bool {
	return filepath.Base(a) == filepath.Base(b)
}

// ---- rendering ----

func pamRulesValue(rules []pamRule) []any {
	out := make([]any, len(rules))
	for i, r := range rules {
		out[i] = pamRuleValue(r)
	}
	return out
}

func pamRuleValue(r pamRule) *value.Map {
	m := value.NewMap(7)
	m.Set("type", r.Type)
	m.Set("control", r.Control)
	m.Set("module", r.Module)
	args := make([]any, len(r.Args))
	for i, a := range r.Args {
		args[i] = a
	}
	m.Set("args", args)
	m.Set("optional", r.Optional)
	m.Set("file", r.File)
	m.Set("line", int64(r.Line))
	return m
}

// pamRender writes a rule back in the format the file uses.
//
// Tabs rather than spaces, because every stock pam.d file on every
// platform this runs on is tab-aligned and a rewritten file that is not
// reads as vandalism in a diff an operator has to review.
func pamRender(r pamRule) string {
	typ := r.Type
	if r.Optional {
		typ = "-" + typ
	}
	if r.Control == pamAtInclude {
		return pamAtInclude + " " + r.Module
	}
	parts := []string{typ, r.Control, r.Module}
	parts = append(parts, r.Args...)
	return strings.Join(parts, "\t")
}

// ---- writing ----

// pamSetModule adds or corrects one rule.
//
// It rewrites only the file the service's own name points at. Following
// an include and editing `system` or `common-auth` would change every
// service on the node from a call that named one, which is the way to
// take a fleet down from a state that looked narrow.
func pamSetModule(c *exec.Context, args *value.Map) (any, error) {
	service := strings.TrimSpace(states.Str(args, "service", ""))
	chain := strings.ToLower(strings.TrimSpace(states.Str(args, "type", "")))
	module := pamModuleName(states.Str(args, "module", ""))
	control := strings.TrimSpace(states.Str(args, "control", ""))
	position := strings.TrimSpace(states.Str(args, "position", ""))
	if position == "" {
		position = "end"
	}
	if err := pamCheckEdit(service, chain, module); err != nil {
		return nil, err
	}
	if control == "" {
		return nil, errors.New("a control flag is required; PAM has no default for one")
	}
	if position != "start" && position != "end" {
		return nil, fmt.Errorf("position is start or end, not %q", position)
	}

	path := pamPath(service)
	text, rules, err := pamLoadForEdit(path)
	if err != nil {
		return nil, err
	}

	want := pamRule{
		Type:     chain,
		Control:  control,
		Module:   module,
		Args:     states.Strings(args, "args"),
		Optional: states.Bool(args, "optional", false),
	}

	for _, rule := range rules {
		if rule.Type != chain || !pamSameModule(rule.Module, module) {
			continue
		}
		// The module is there. Replacing the line in place keeps its
		// position in the chain, which is the part of a PAM rule that
		// decides what it does: moving pam_deny.so from last to first
		// denies every login while changing no flag.
		if pamRender(rule) == pamRender(withOrigin(want, rule)) {
			return pamEditResult(c, false, path, fmt.Sprintf(
				"%s already has %s in its %s chain, as written.", service, module, chain), nil), nil
		}
		if c.Test {
			return pamEditResult(c, true, path, fmt.Sprintf(
				"%s's %s rule for %s would be changed.", service, chain, module),
				map[string]string{"old": pamRender(rule), "new": pamRender(withOrigin(want, rule))}), nil
		}
		updated := pamReplaceLine(text, rule.Line, pamRender(withOrigin(want, rule)))
		if err := pamCommit(path, updated); err != nil {
			return nil, err
		}
		return pamEditResult(c, true, path, fmt.Sprintf(
			"%s's %s rule for %s was changed.", service, chain, module),
			map[string]string{"old": pamRender(rule), "new": pamRender(withOrigin(want, rule))}), nil
	}

	line := pamRender(want)
	if c.Test {
		return pamEditResult(c, true, path, fmt.Sprintf(
			"%s would be added to %s's %s chain, at the %s.", module, service, chain, position),
			map[string]string{"old": "", "new": line}), nil
	}
	updated, err := pamInsert(text, rules, chain, line, position)
	if err != nil {
		return nil, err
	}
	if err := pamCommit(path, updated); err != nil {
		return nil, err
	}
	return pamEditResult(c, true, path, fmt.Sprintf(
		"%s was added to %s's %s chain, at the %s.", module, service, chain, position),
		map[string]string{"old": "", "new": line}), nil
}

// pamRemoveModule takes a rule out, unless it is the last one holding
// the chain up.
func pamRemoveModule(c *exec.Context, args *value.Map) (any, error) {
	service := strings.TrimSpace(states.Str(args, "service", ""))
	chain := strings.ToLower(strings.TrimSpace(states.Str(args, "type", "")))
	module := pamModuleName(states.Str(args, "module", ""))
	if err := pamCheckEdit(service, chain, module); err != nil {
		return nil, err
	}

	path := pamPath(service)
	text, rules, err := pamLoadForEdit(path)
	if err != nil {
		return nil, err
	}

	var doomed []pamRule
	remaining := 0
	for _, rule := range rules {
		if rule.Type != chain {
			continue
		}
		if pamSameModule(rule.Module, module) {
			doomed = append(doomed, rule)
			continue
		}
		remaining++
	}
	if len(doomed) == 0 {
		return pamEditResult(c, false, path, fmt.Sprintf(
			"%s's %s chain does not name %s.", service, chain, module), nil), nil
	}
	// An empty chain is not "no policy", it is a denial: PAM returns
	// PAM_MUST_FAIL_ERROR for a service whose chain has no modules, so
	// removing the last auth rule from sshd locks the node. This is the
	// one refusal in the module that is not about a malformed argument.
	if remaining == 0 {
		return nil, fmt.Errorf(
			"removing %s would leave %s's %s chain empty, and PAM denies a request whose chain has no rules; "+
				"add the rule that replaces it first", module, service, chain)
	}

	lines := make([]int, len(doomed))
	for i, rule := range doomed {
		lines[i] = rule.Line
	}
	if c.Test {
		return pamEditResult(c, true, path, fmt.Sprintf(
			"%s would be removed from %s's %s chain.", module, service, chain),
			map[string]string{"old": pamRender(doomed[0]), "new": ""}), nil
	}
	if err := pamCommit(path, pamDeleteLines(text, lines)); err != nil {
		return nil, err
	}
	return pamEditResult(c, true, path, fmt.Sprintf(
		"%s was removed from %s's %s chain.", module, service, chain),
		map[string]string{"old": pamRender(doomed[0]), "new": ""}), nil
}

// pamCheckEdit validates the three arguments every edit shares.
func pamCheckEdit(service, chain, module string) error {
	if service == "" {
		return errors.New("a PAM service must be named")
	}
	if strings.ContainsAny(service, `/\`) {
		// An edit takes a service, not a path. Accepting a path here
		// would let a caller write outside PamDir through a name that
		// reads like a service.
		return fmt.Errorf("%q is a path; an edit names a service, such as sshd", service)
	}
	if !pamTypes[chain] {
		return fmt.Errorf("%q is not a PAM chain; it is one of auth, account, session or password", chain)
	}
	if module == "" {
		return errors.New("a module must be named")
	}
	return nil
}

// pamLoadForEdit reads the file and parses it, returning both, because
// an edit rewrites the text and reasons about the rules.
func pamLoadForEdit(path string) (string, []pamRule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil, fmt.Errorf("this node has no PAM configuration at %s; "+
				"an edit changes an existing service rather than creating one", path)
		}
		return "", nil, fmt.Errorf("%s could not be read: %w", path, err)
	}
	rules, err := pamParse(string(data), path)
	if err != nil {
		return "", nil, err
	}
	return string(data), rules, nil
}

// withOrigin carries a parsed rule's position onto a replacement, so the
// rendering compared against the file is the one that would replace it.
func withOrigin(want, from pamRule) pamRule {
	want.File, want.Line = from.File, from.Line
	return want
}

// pamReplaceLine swaps one 1-based line for another.
func pamReplaceLine(text string, line int, with string) string {
	lines := strings.Split(text, "\n")
	if line < 1 || line > len(lines) {
		return text
	}
	lines[line-1] = with
	return strings.Join(lines, "\n")
}

// pamDeleteLines removes 1-based lines.
func pamDeleteLines(text string, remove []int) string {
	drop := make(map[int]bool, len(remove))
	for _, n := range remove {
		drop[n] = true
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		if drop[i+1] {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// pamInsert places a new rule within its chain.
//
// Position within a chain is the whole meaning of a PAM rule, so the
// insertion point is computed from the chain's own existing rules rather
// than from the end of the file: a service that lists auth, account,
// session and password in that order — which every stock file does —
// would otherwise get its new `auth` rule after the password chain,
// where PAM would still evaluate it last in auth but where no operator
// reading the file would expect to find it.
//
// A chain that has no rules yet has no position to be relative to, so
// the rule goes at the end of the file and the comment says where.
func pamInsert(text string, rules []pamRule, chain, line, position string) (string, error) {
	first, last := 0, 0
	for _, r := range rules {
		if r.Type != chain || r.Line == 0 {
			continue
		}
		if first == 0 || r.Line < first {
			first = r.Line
		}
		if r.Line > last {
			last = r.Line
		}
	}
	lines := strings.Split(text, "\n")
	at := len(lines)
	switch {
	case first == 0:
		// No rules of this type; append.
	case position == "start":
		at = first - 1
	default:
		at = last
	}
	if at > len(lines) {
		at = len(lines)
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:at]...)
	out = append(out, line)
	out = append(out, lines[at:]...)
	return strings.Join(out, "\n"), nil
}

// pamCommit writes the file and proves the result is readable before the
// call returns.
//
// The re-read is not belt and braces. This module's whole risk is that a
// node stops authenticating, and the failure that produces it is a file
// that no longer parses. Reading the bytes back through the same parser
// that will be asked about them tomorrow is the cheapest check that the
// node is still configurable, and it runs while the caller is still on
// the line to be told.
func pamCommit(path, text string) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := atomicfile.Write(path, []byte(text), mode); err != nil {
		return fmt.Errorf("%s could not be written: %w", path, err)
	}
	back, err := pamReadFile(path)
	if err != nil {
		return fmt.Errorf("%s was written and cannot be read back: %w", path, err)
	}
	if len(back) == 0 {
		return fmt.Errorf("%s was written and now has no rules at all; PAM denies a service with an empty chain", path)
	}
	return nil
}

// pamEditResult is the shape both mutators return.
//
// An execution function is not a state and has no states.Result, so the
// change set is spelled out here rather than borrowed: `changed`, a
// comment, the file that was touched, and the old and new line when
// there is one.
func pamEditResult(c *exec.Context, changed bool, path, comment string, change map[string]string) *value.Map {
	out := value.NewMap(4)
	out.Set("changed", changed)
	out.Set("file", path)
	if c.Test && changed {
		out.Set("comment", comment+" Nothing was written: this was a test run.")
	} else {
		out.Set("comment", comment)
	}
	if change != nil {
		m := value.NewMap(2)
		m.Set("old", change["old"])
		m.Set("new", change["new"])
		out.Set("rule", m)
	}
	return out
}
