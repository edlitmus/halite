package builtin

import (
	"fmt"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
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

	lines := splitKeepEmpty(string(before))
	trailingNewline := strings.HasSuffix(string(before), "\n") || len(before) == 0

	var after []string
	var err error
	switch mode {
	case "absent", "delete":
		after = lineDelete(lines, match)
	case "replace":
		after = lineReplace(lines, match, content, states.Bool(args, "indent", true))
	case "insert":
		after, err = lineInsert(lines, content, args)
	case "ensure":
		after, err = lineEnsure(lines, match, content, args)
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

func lineDelete(lines []string, match string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) == strings.TrimSpace(match) || strings.Contains(l, match) {
			continue
		}
		out = append(out, l)
	}
	return out
}

func lineReplace(lines []string, match, content string, keepIndent bool) []string {
	out := make([]string, len(lines))
	copy(out, lines)
	for i, l := range out {
		if !lineMatches(l, match) {
			continue
		}
		out[i] = content
		if keepIndent {
			out[i] = indentOf(l) + strings.TrimLeft(content, " \t")
		}
	}
	return out
}

// lineMatches is the comparison Salt uses: a line matches when it is the
// same once trimmed, or when it contains the match text. The second form
// is what lets `match: '^PermitRootLogin'` behave the way an operator
// expects even though it is not a pattern here.
func lineMatches(line, match string) bool {
	if strings.TrimSpace(line) == strings.TrimSpace(match) {
		return true
	}
	return match != "" && strings.Contains(line, match)
}

func indentOf(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

func lineInsert(lines []string, content string, args *value.Map) ([]string, error) {
	location := states.Str(args, "location", "")
	before := states.Str(args, "before", "")
	after := states.Str(args, "after", "")

	switch {
	case location == "start":
		return append([]string{content}, lines...), nil
	case location == "end":
		return append(append([]string{}, lines...), content), nil
	case before != "":
		for i, l := range lines {
			if lineMatches(l, before) {
				out := append([]string{}, lines[:i]...)
				out = append(out, content)
				return append(out, lines[i:]...), nil
			}
		}
		return nil, fmt.Errorf("no line matches before: %q", before)
	case after != "":
		for i, l := range lines {
			if lineMatches(l, after) {
				out := append([]string{}, lines[:i+1]...)
				out = append(out, content)
				return append(out, lines[i+1:]...), nil
			}
		}
		return nil, fmt.Errorf("no line matches after: %q", after)
	}
	return nil, fmt.Errorf("insert needs one of location, before, or after")
}

func lineEnsure(lines []string, match, content string, args *value.Map) ([]string, error) {
	found := false
	for _, l := range lines {
		if lineMatches(l, match) {
			found = true
			break
		}
	}
	if found {
		return lineReplace(lines, match, content, states.Bool(args, "indent", true)), nil
	}

	// Not there yet: place it where the state said, or at the end.
	if states.Str(args, "location", "") == "" &&
		states.Str(args, "before", "") == "" &&
		states.Str(args, "after", "") == "" {
		return append(append([]string{}, lines...), content), nil
	}
	return lineInsert(lines, content, args)
}
