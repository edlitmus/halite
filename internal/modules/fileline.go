package modules

import (
	"fmt"
	"strings"
)

func init() {
	register("file.line", fileLine)
}

// fileLine manages a single line in a file.
//
//	/etc/ssh/sshd_config:
//	  file.line:
//	    - content: "PermitRootLogin no"
//	    - match: "PermitRootLogin"
//	    - mode: ensure
//
// `match` is a substring of the line to act on, defaulting to the content
// itself. It is a substring rather than a regular expression because that
// is what Salt's file.line matches on — file.replace is the regular
// expression state, and keeping the two distinct means a pattern written
// for one does not quietly half-work in the other.
//
// Modes:
//
//	ensure  (default) the line is present exactly once: matched lines are
//	        replaced, and if nothing matches the line is inserted
//	replace  matched lines become the content; nothing is inserted
//	delete   matched lines are removed
//	insert   the line is added if no line matches
//
// Where an insert lands: after `after`, before `before`, or at `location`
// (start or end, the default).
func fileLine(c *Ctx, id string, args map[string]any) Result {
	content := Str(args, "content", "")
	match := Str(args, "match", content)
	mode := strings.ToLower(Str(args, "mode", "ensure"))

	switch mode {
	case "ensure", "replace", "insert":
		if content == "" {
			return resFail("file.line %s needs content", mode)
		}
	case "delete":
		if match == "" {
			return resFail("file.line delete needs match")
		}
	default:
		return resFail("mode %q is not one of ensure, replace, delete, insert", mode)
	}
	if match == "" {
		return resFail("file.line needs match when content is empty")
	}

	// Salt spells file.line's action `mode`, which is the permission bits
	// everywhere else in halite. It is read here and kept from the shared
	// edit helper, which would otherwise parse it as an octal mode — an
	// edit keeps the file's existing permissions in any case.
	args = withoutArg(args, "mode")

	before, after := Str(args, "before", ""), Str(args, "after", "")
	location := strings.ToLower(Str(args, "location", "end"))
	if location != "start" && location != "end" {
		return resFail("location %q is not start or end", location)
	}

	return editFile(c, id, args, func(current []byte, exists bool) ([]byte, string, error) {
		if !exists && !Bool(args, "create", true) {
			return nil, "", fmt.Errorf("%s does not exist", Str(args, "name", id))
		}
		lines := splitLines(current)
		matched := matchingLines(lines, match)

		switch mode {
		case "delete":
			if len(matched) == 0 {
				return current, "", nil
			}
			return joinLines(without(lines, matched)), fmt.Sprintf("%d line(s) removed", len(matched)), nil

		case "replace", "ensure":
			if len(matched) > 0 {
				updated, changed := replaceAt(lines, matched, content)
				if !changed {
					return current, "", nil
				}
				return joinLines(updated), fmt.Sprintf("%d line(s) replaced", len(matched)), nil
			}
			if mode == "replace" {
				return current, "", nil // replace does not create what is not there
			}
		}

		// insert, or ensure with nothing to replace
		if containsLine(lines, content) {
			return current, "", nil
		}
		updated, where, err := insertLine(lines, content, before, after, location)
		if err != nil {
			return nil, "", err
		}
		return joinLines(updated), "line inserted " + where, nil
	})
}

// matchingLines returns the indexes of the lines containing match.
func matchingLines(lines []string, match string) []int {
	var out []int
	for i, line := range lines {
		if strings.Contains(line, match) {
			out = append(out, i)
		}
	}
	return out
}

// replaceAt sets the given lines to content, keeping the first and
// dropping any others: "present exactly once" is the point of the state.
func replaceAt(lines []string, indexes []int, content string) ([]string, bool) {
	out := make([]string, 0, len(lines))
	changed := false
	kept := false
	drop := map[int]bool{}
	for _, i := range indexes[1:] {
		drop[i] = true
	}
	for i, line := range lines {
		switch {
		case i == indexes[0]:
			kept = true
			if line != content {
				changed = true
			}
			out = append(out, content)
		case drop[i]:
			changed = true
		default:
			out = append(out, line)
		}
	}
	return out, changed && kept
}

// insertLine places content relative to an anchor, or at one end.
func insertLine(lines []string, content, before, after, location string) ([]string, string, error) {
	insertAt := func(i int) []string {
		out := make([]string, 0, len(lines)+1)
		out = append(out, lines[:i]...)
		out = append(out, content)
		return append(out, lines[i:]...)
	}
	switch {
	case after != "":
		if i := lastMatch(lines, after); i >= 0 {
			return insertAt(i + 1), fmt.Sprintf("after %q", after), nil
		}
		return nil, "", fmt.Errorf("no line matches after: %q", after)
	case before != "":
		if i := firstMatch(lines, before); i >= 0 {
			return insertAt(i), fmt.Sprintf("before %q", before), nil
		}
		return nil, "", fmt.Errorf("no line matches before: %q", before)
	case location == "start":
		return insertAt(0), "at the start", nil
	}
	return append(append([]string{}, lines...), content), "at the end", nil
}

func firstMatch(lines []string, match string) int {
	for i, line := range lines {
		if strings.Contains(line, match) {
			return i
		}
	}
	return -1
}

func lastMatch(lines []string, match string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], match) {
			return i
		}
	}
	return -1
}

// without returns the lines with the given indexes removed.
func without(lines []string, indexes []int) []string {
	drop := map[int]bool{}
	for _, i := range indexes {
		drop[i] = true
	}
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if !drop[i] {
			out = append(out, line)
		}
	}
	return out
}
