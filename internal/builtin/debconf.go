package builtin

import (
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerDebconf installs the debconf module of SPEC 15.3's Debian row,
// and the `debconf.set` state of 15.5.
//
// debconf is how a Debian package asks its questions, and pre-seeding it
// is how an estate installs one without a prompt. That is the whole
// reason to manage it: `pkg.installed` on a package with an unanswered
// question either hangs waiting for an answer nobody is there to give,
// or takes a default the estate did not choose.
//
// **Setting an answer does not reconfigure a package that is already
// installed.** This is the thing most often got wrong about debconf, and
// it is a property of debconf rather than of this module: the database
// holds the answers, and a package reads them when it is configured. An
// answer set afterwards applies at the next install or the next
// `dpkg-reconfigure`, and this module says so rather than reporting a
// change the machine did not make. Nothing here runs dpkg-reconfigure:
// it restarts services and can prompt, so it is an operator's decision
// and not a side effect of a state converging.
func registerDebconf(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "debconf", Function: "show",
				Doc: "Return the answers debconf holds for a package, and whether each has been seen.",
				Params: []signature.Param{
					req("package", signature.String, "The package."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: debianOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return debconfShow(c, states.Str(args, "package", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "debconf", Function: "get_selections",
				Doc: "Return every answer debconf holds, in the form debconf-set-selections reads.",
				Params: []signature.Param{
					opt("package", signature.String, "", "Limit to one package."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: debianOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return debconfGetSelections(c, states.Str(args, "package", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "debconf", Function: "set",
				Doc: "Answer one of a package's questions.",
				Params: []signature.Param{
					req("package", signature.String, "The package."),
					req("question", signature.String, "The question, such as postfix/main_mailer_type."),
					req("type", signature.String, "The answer's type: select, boolean, string, password, note, text, multiselect."),
					req("value", signature.String, "The answer."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  debianOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				sel := debconfSelection{
					Package:  states.Str(args, "package", ""),
					Question: states.Str(args, "question", ""),
					Type:     states.Str(args, "type", ""),
					Value:    states.Str(args, "value", ""),
				}
				if err := sel.validate(); err != nil {
					return nil, err
				}
				if c.Test {
					return true, nil
				}
				return true, debconfSet(c, []debconfSelection{sel})
			},
		},
	)

	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "debconf", Function: "set",
			Doc: "Ensure a package's debconf answers are the ones given.",
			Params: []signature.Param{
				nameParam("The package. Defaults to the state ID."),
				req("data", signature.Map, "Question to `{type, value}`, or question to the answer where the type is given by `type`."),
				opt("type", signature.String, "", "The type for every answer that does not name its own."),
			},
			Mutates:    true,
			TestMode:   signature.TestReliable,
			Privileges: []string{"root"},
			Platforms:  debianOnly,
			Section:    "15.5",
		},
		Fn: debconfSetState,
	})
}

// debconfSelection is one answer.
type debconfSelection struct {
	Package  string
	Question string
	Type     string
	Value    string
}

// debconfTypes are the types debconf has. A type it does not know is
// rejected by debconf-set-selections with a warning on stderr and a zero
// exit, which is the worst combination: the answer is not stored and
// nothing failed.
var debconfTypes = map[string]bool{
	"select": true, "boolean": true, "string": true, "password": true,
	"note": true, "text": true, "multiselect": true, "title": true,
	"error": true,
}

func (s debconfSelection) validate() error {
	if s.Package == "" {
		return fmt.Errorf("a debconf answer needs a package")
	}
	if s.Question == "" {
		return fmt.Errorf("%s: a debconf answer needs a question", s.Package)
	}
	if !debconfTypes[s.Type] {
		return fmt.Errorf("%s %s: %q is not a debconf type; it takes %s",
			s.Package, s.Question, s.Type, states.SortedNames(debconfTypeNames()))
	}
	// The format is tab separated and debconf-set-selections splits on
	// whitespace, so a tab inside a value truncates the answer and
	// stores something else. A newline ends the record outright.
	if strings.ContainsAny(s.Question, "\t\n") || strings.ContainsAny(s.Value, "\t\n") {
		return fmt.Errorf("%s %s: a debconf answer cannot contain a tab or a newline; the format is one record a line, tab separated",
			s.Package, s.Question)
	}
	return nil
}

func debconfTypeNames() []string {
	out := make([]string, 0, len(debconfTypes))
	for t := range debconfTypes {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func (s debconfSelection) line() string {
	return strings.Join([]string{s.Package, s.Question, s.Type, s.Value}, "\t")
}

// debconfShow reads one package's answers.
//
// `debconf-show` ships with debconf itself, so this works on any Debian
// system. `debconf-get-selections` does not — it is in debconf-utils,
// which is not installed by default — which is why the two are separate
// functions rather than one that sometimes cannot answer.
func debconfShow(c *exec.Context, pkg string) (*value.Map, error) {
	if pkg == "" {
		return nil, fmt.Errorf("debconf.show needs a package")
	}
	if err := haveDpkg(c, "debconf-show"); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"debconf-show", pkg},
		Env:            aptEnv(),
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("%s: %s", pkg, firstLine(res.Stderr+res.Stdout))
	}
	return parseDebconfShow(res.Stdout), nil
}

// parseDebconfShow reads `debconf-show` output, which looks like this,
// with the marker column kept verbatim:
//
//	*⎵postfix/main_mailer_type: Internet Site
//	⎵⎵postfix/mynetworks: 127.0.0.0/8
//
// The leading asterisk means the question has been asked and answered —
// debconf calls it "seen" — and a blank marker means the value is a
// default nobody has confirmed. That distinction is why this reports a
// mapping rather than a plain value: a state that treated an unseen
// default as an answer would report convergence on a package that will
// still ask.
func parseDebconfShow(stdout string) *value.Map {
	out := value.NewMap(16)
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		seen := strings.HasPrefix(line, "*")
		body := strings.TrimSpace(strings.TrimPrefix(line, "*"))
		question, answer, ok := strings.Cut(body, ":")
		if !ok {
			continue
		}
		out.Set(strings.TrimSpace(question), value.MapOf(
			"value", strings.TrimSpace(answer),
			"seen", seen,
		))
	}
	return out
}

// debconfGetSelections reads the whole database, in the format
// debconf-set-selections takes back.
func debconfGetSelections(c *exec.Context, pkg string) (*value.Map, error) {
	if c.Which("debconf-get-selections") == "" {
		// Named rather than generic, because the fix is one package and
		// an operator who is told the name will install it. `show`
		// answers for a single package without it.
		return nil, fmt.Errorf("debconf-get-selections is not installed; it is in the debconf-utils package, " +
			"and `debconf.show` reads one package's answers without it")
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"debconf-get-selections"},
		Env:            aptEnv(),
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("%s", firstLine(res.Stderr+res.Stdout))
	}
	return parseDebconfSelections(res.Stdout, pkg), nil
}

// parseDebconfSelections reads the tab-separated selections format,
// keyed by "package question" so that two packages asking the same
// question do not collide.
func parseDebconfSelections(stdout, only string) *value.Map {
	out := value.NewMap(64)
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		fields := strings.SplitN(line, "\t", 4)
		if len(fields) < 3 {
			continue
		}
		pkg, question, kind := fields[0], fields[1], fields[2]
		answer := ""
		if len(fields) > 3 {
			answer = fields[3]
		}
		if only != "" && pkg != only {
			continue
		}
		out.Set(pkg+" "+question, value.MapOf(
			"package", pkg, "question", question,
			"type", kind, "value", answer,
		))
	}
	return out
}

// debconfSet writes answers through debconf-set-selections.
func debconfSet(c *exec.Context, sels []debconfSelection) error {
	if err := haveDpkg(c, "debconf-set-selections"); err != nil {
		return err
	}
	var lines []string
	for _, s := range sels {
		lines = append(lines, s.line())
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"debconf-set-selections"},
		Env:            aptEnv(),
		Stdin:          strings.Join(lines, "\n") + "\n",
		IgnoreExitCode: true,
	})
	if err != nil {
		return err
	}
	// debconf-set-selections warns on stderr and still exits zero for a
	// record it did not like, so the exit code alone would report a
	// success that stored nothing.
	if res.Code != 0 || strings.Contains(res.Stderr, "warning") {
		return fmt.Errorf("%s", firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

// debconfSetState converges a package's answers.
func debconfSetState(c *exec.Context, args *value.Map) (states.Result, error) {
	pkg := strings.TrimSpace(states.Str(args, "name", ""))
	if pkg == "" {
		return states.False("This state needs a package."), nil
	}
	data := states.Mapping(args, "data")
	if data == nil || data.Len() == 0 {
		return states.False(fmt.Sprintf("%s: this state needs `data` naming at least one question.", pkg)), nil
	}
	defaultType := states.Str(args, "type", "")

	want, err := debconfWanted(pkg, data, defaultType)
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}

	have, err := debconfShow(c, pkg)
	if err != nil {
		return states.False(fmt.Sprintf("%s's answers could not be read: %v", pkg, err)), nil
	}

	changes := value.NewMap(len(want))
	var pending []debconfSelection
	for _, s := range want {
		current, seen := debconfCurrent(have, s.Question)
		// An answer that matches but has never been seen is not
		// converged: debconf will still ask, because the value is a
		// default rather than an answer. Setting it marks it seen,
		// which is the point of pre-seeding.
		if current == s.Value && seen {
			continue
		}
		pending = append(pending, s)
		changes.Set(s.Question, states.Change(current, s.Value))
	}

	if len(pending) == 0 {
		return states.True(fmt.Sprintf("%s's answers are already set.", pkg)), nil
	}
	// The caveat travels with the result rather than living only in the
	// documentation, because it is the difference between what this
	// state did and what an operator may think it did.
	note := " Answers apply at the next install or dpkg-reconfigure; a package already configured is not reconfigured by this."
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%d of %s's answers would be set.%s",
			len(pending), pkg, note), changes), nil
	}
	if err := debconfSet(c, pending); err != nil {
		return states.False(fmt.Sprintf("%s's answers could not be set: %v", pkg, err)), nil
	}
	return states.Changed(fmt.Sprintf("%d of %s's answers were set.%s",
		len(pending), pkg, note), changes), nil
}

// debconfCurrent reads one question out of a `debconf.show` result.
func debconfCurrent(have *value.Map, question string) (val string, seen bool) {
	raw, ok := have.Get(question)
	if !ok {
		return "", false
	}
	m, ok := raw.(*value.Map)
	if !ok {
		return "", false
	}
	v, _ := m.Get("value")
	s, _ := m.Get("seen")
	val, _ = v.(string)
	seen, _ = s.(bool)
	return val, seen
}

// debconfWanted reads the state's `data` into selections.
//
// Two shapes are accepted because trees are written both ways: a
// question mapped to `{type, value}`, and a question mapped straight to
// its answer with one `type` for the state. The second is what a tree
// setting five booleans looks like, and making it write the type five
// times would be noise.
func debconfWanted(pkg string, data *value.Map, defaultType string) ([]debconfSelection, error) {
	var out []debconfSelection
	for _, question := range data.StringKeys() {
		raw, _ := data.Get(question)
		sel := debconfSelection{Package: pkg, Question: question, Type: defaultType}
		switch v := raw.(type) {
		case *value.Map:
			if t, ok := v.Get("type"); ok {
				sel.Type, _ = t.(string)
			}
			answer, ok := v.Get("value")
			if !ok {
				return nil, fmt.Errorf("%s %s: this answer names no `value`", pkg, question)
			}
			sel.Value = debconfScalar(answer)
		default:
			sel.Value = debconfScalar(raw)
		}
		if err := sel.validate(); err != nil {
			return nil, err
		}
		out = append(out, sel)
	}
	return out, nil
}

// debconfScalar renders an answer as debconf stores it, which is text.
//
// A boolean written in YAML as `true` is `true` to debconf, not `True`
// or `1`: a boolean question with anything else in it is treated as
// false, silently, which is the shape of bug this exists to prevent.
func debconfScalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case bool:
		if t {
			return "true"
		}
		return "false"
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}
