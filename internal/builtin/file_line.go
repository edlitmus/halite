package builtin

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/regexcompat"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// fileLine manages a single line in a file.
//
// It is the state Salt trees reach for most often after file.managed,
// because it is what you use when a file is owned by a package and you
// need exactly one setting in it changed. The modes are Salt's, and the
// awkward one is `ensure`: it means "this line should be present, and if a
// line matching `match` is already there, replace it rather than adding a
// second one".
func fileLine(c *exec.Context, args *value.Map) (states.Result, error) {
	path, before, res, ok := readEditTarget(args)
	if !ok {
		return res, nil
	}

	content := states.Str(args, "content", "")
	mode := states.Str(args, "mode", "ensure")
	match := states.Str(args, "match", "")
	if match == "" {
		match = content
	}
	if content == "" && mode != "absent" && mode != "delete" {
		return states.False("file.line needs content unless its mode is absent or delete."), nil
	}
	// Every pattern is compiled before anything is read for a decision,
	// as Salt does, so a bad `before` fails the state even on a run where
	// `match` would have been enough. A pattern that does not compile is
	// a failure, never a silent non-match: a silent non-match in ensure
	// is an appended duplicate line, and in delete it is a delete that
	// reports success and removed nothing.
	matchP, err := compileLinePattern("match", match)
	if err != nil {
		return states.False(fmt.Sprintf("%s: %v", path, err)), nil
	}
	beforeP, err := compileLinePattern("before", states.Str(args, "before", ""))
	if err != nil {
		return states.False(fmt.Sprintf("%s: %v", path, err)), nil
	}
	afterP, err := compileLinePattern("after", states.Str(args, "after", ""))
	if err != nil {
		return states.False(fmt.Sprintf("%s: %v", path, err)), nil
	}
	anchors := lineAnchors{before: beforeP, after: afterP, location: states.Str(args, "location", "")}

	lines := splitKeepEmpty(string(before))
	trailingNewline := strings.HasSuffix(string(before), "\n") || len(before) == 0

	var after []string
	switch mode {
	case "absent", "delete":
		after = lineDelete(lines, matchP)
	case "replace":
		after = lineReplace(lines, matchP, content, states.Bool(args, "indent", true))
	case "insert":
		after, err = lineInsert(lines, content, anchors)
	case "ensure":
		after, err = lineEnsure(lines, matchP, content, anchors, states.Bool(args, "indent", true))
	default:
		return states.False(fmt.Sprintf(
			"file.line has no mode %q; it takes ensure, absent, replace, insert, or delete.", mode)), nil
	}
	if err != nil {
		return states.False(fmt.Sprintf("%s: %v", path, err)), nil
	}

	text := strings.Join(after, "\n")
	if trailingNewline && text != "" {
		text += "\n"
	}

	return editResult(c, path, before, []byte(text), args, func(changed bool) string {
		if !changed {
			return fmt.Sprintf("The line is already in the requested state in %s.", path)
		}
		switch mode {
		case "absent", "delete":
			return fmt.Sprintf("The matching line was removed from %s.", path)
		case "replace":
			return fmt.Sprintf("The matching line in %s was replaced.", path)
		case "insert":
			return fmt.Sprintf("The line was inserted into %s.", path)
		default:
			return fmt.Sprintf("The line was set in %s.", path)
		}
	})
}

func lineDelete(lines []string, match *linePattern) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if match.matches(l) {
			continue
		}
		out = append(out, l)
	}
	return out
}

func lineReplace(lines []string, match *linePattern, content string, keepIndent bool) []string {
	out := make([]string, len(lines))
	copy(out, lines)
	for i, l := range out {
		if !match.matches(l) {
			continue
		}
		out[i] = content
		if keepIndent {
			out[i] = indentOf(l) + strings.TrimLeft(content, " \t")
		}
	}
	return out
}

// linePattern is how Salt's file.line finds a line, for `match`,
// `before` and `after` alike: `_regex_to_static` in
// salt/modules/file.py keeps a line when `re.compile(p, re.DOTALL)`
// *searches* it successfully, or when the pattern occurs in it as plain
// text (`line.count(p)`). Salt's own documentation says the value is
// "tried as both a regex and a part of the line".
//
// Both halves matter. The regex half is what makes the commonest
// sshd_config idiom work: `match: '^PermitRootLogin'` is an anchored
// search, and a plain substring test never finds a line beginning with a
// caret, so ensure appended a second PermitRootLogin on every run while
// the original `yes` line, the one sshd honours, stayed first. The
// substring half is what keeps a literal containing regex metacharacters
// working: as a regex `match: 'allow(all)'` finds `allowall` and not
// the line `allow(all)`, and only the literal half finds it as written.
//
// The search is unanchored, as Python's re.search is: `match: Port`
// matches `GatewayPorts no` too. That is Salt's behaviour and it is why
// trees anchor; the cure is `^Port\b`, not a different default.
//
// The pattern is compiled through regexcompat, so a construct RE2 lacks
// (SPEC section 10.4) is an error naming it rather than a pattern that
// quietly finds nothing.
type linePattern struct {
	re   *regexp.Regexp
	text string
}

// compileLinePattern returns nil for an empty pattern, which Salt treats
// as "not given" rather than as the pattern every line matches.
func compileLinePattern(arg, pattern string) (*linePattern, error) {
	if pattern == "" {
		return nil, nil
	}
	re, err := regexcompat.CompileWithFlags(pattern, false, false, true)
	if err != nil {
		return nil, fmt.Errorf("file.line %s is not a usable regular expression: %w", arg, err)
	}
	return &linePattern{re: re, text: pattern}, nil
}

func (p *linePattern) matches(line string) bool {
	if p == nil {
		return false
	}
	return p.re.MatchString(line) || strings.Contains(line, p.text)
}

func (p *linePattern) String() string {
	if p == nil {
		return ""
	}
	return p.text
}

// lineAnchors is where insert and ensure place a new line.
type lineAnchors struct {
	before, after *linePattern
	location      string
}

func (a lineAnchors) none() bool {
	return a.location == "" && a.before == nil && a.after == nil
}

func indentOf(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

func lineInsert(lines []string, content string, a lineAnchors) ([]string, error) {
	switch {
	case a.location == "start":
		return append([]string{content}, lines...), nil
	case a.location == "end":
		return append(append([]string{}, lines...), content), nil
	case a.before != nil:
		for i, l := range lines {
			if a.before.matches(l) {
				out := append([]string{}, lines[:i]...)
				out = append(out, content)
				return append(out, lines[i:]...), nil
			}
		}
		return nil, fmt.Errorf("no line matches before: %q", a.before)
	case a.after != nil:
		for i, l := range lines {
			if a.after.matches(l) {
				out := append([]string{}, lines[:i+1]...)
				out = append(out, content)
				return append(out, lines[i+1:]...), nil
			}
		}
		return nil, fmt.Errorf("no line matches after: %q", a.after)
	}
	return nil, fmt.Errorf("insert needs one of location, before, or after")
}

func lineEnsure(lines []string, match *linePattern, content string, a lineAnchors, keepIndent bool) ([]string, error) {
	found := false
	for _, l := range lines {
		if match.matches(l) {
			found = true
			break
		}
	}
	if found {
		return lineReplace(lines, match, content, keepIndent), nil
	}

	// Not there yet: place it where the state said, or at the end.
	if a.none() {
		return append(append([]string{}, lines...), content), nil
	}
	return lineInsert(lines, content, a)
}
