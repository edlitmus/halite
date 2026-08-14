package modules

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/sls"
)

func init() {
	register("file.replace", fileReplace)
	register("file.blockreplace", fileBlockReplace)
}

// fileReplace substitutes a regular expression throughout a file.
//
//	/etc/ssh/sshd_config:
//	  file.replace:
//	    - pattern: '^#?PermitRootLogin .*'
//	    - repl: 'PermitRootLogin no'
//	    - append_if_not_found: true
//
// The pattern is Go's regexp syntax and the replacement uses `$1` for a
// capture group, not Python's `\1`. ^ and $ match at line boundaries, as
// they do in Salt.
func fileReplace(c *Ctx, id string, args map[string]any) Result {
	pattern := Str(args, "pattern", "")
	if pattern == "" {
		return resFail("file.replace needs a pattern")
	}
	// Salt's file.replace defaults to MULTILINE, and nearly every pattern
	// written for it anchors a line with ^ or $. Go anchors to the whole
	// text unless told otherwise, so the flag is set here rather than
	// leaving every ported pattern to silently match nothing.
	re, err := regexp.Compile("(?m)" + pattern)
	if err != nil {
		return resFail("invalid pattern %q: %v", pattern, err)
	}
	repl := Str(args, "repl", "")
	count, err := replaceCount(args)
	if err != nil {
		return resFail("%v", err)
	}
	appendMissing := Bool(args, "append_if_not_found", false)
	prependMissing := Bool(args, "prepend_if_not_found", false)
	notFound := Str(args, "not_found_content", repl)

	return editFile(c, id, args, func(current []byte, exists bool) ([]byte, string, error) {
		if !exists {
			if Bool(args, "ignore_if_missing", false) {
				return nil, "", nil
			}
			return nil, "", fmt.Errorf("%s does not exist", Str(args, "name", id))
		}
		if hits := len(re.FindAll(current, -1)); hits > 0 {
			updated := replaceN(re, current, repl, count)
			if string(updated) == string(current) {
				return current, "", nil
			}
			replaced := hits
			if count > 0 && count < hits {
				replaced = count
			}
			return updated, fmt.Sprintf("%d occurrence(s) replaced", replaced), nil
		}
		if !appendMissing && !prependMissing {
			return current, "", nil
		}
		lines := splitLines(current)
		if containsLine(lines, notFound) {
			return current, "", nil
		}
		if prependMissing {
			return joinLines(append([]string{notFound}, lines...)), "not found, prepended", nil
		}
		return joinLines(append(lines, notFound)), "not found, appended", nil
	})
}

// replaceCount reads the `count` argument: 0 (the default) means every
// occurrence, as it does in Salt.
func replaceCount(args map[string]any) (int, error) {
	raw := Str(args, "count", "")
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("count %q is not a positive number", raw)
	}
	return n, nil
}

// replaceN replaces the first n matches, or all of them when n is 0.
func replaceN(re *regexp.Regexp, src []byte, repl string, n int) []byte {
	if n <= 0 {
		return re.ReplaceAll(src, []byte(repl))
	}
	done := 0
	return re.ReplaceAllFunc(src, func(match []byte) []byte {
		if done >= n {
			return match
		}
		done++
		return re.Expand(nil, []byte(repl), match, re.FindSubmatchIndex(match))
	})
}

// fileBlockReplace manages the text between two marker lines, leaving the
// rest of the file alone.
//
//	/etc/hosts:
//	  file.blockreplace:
//	    - marker_start: '# BEGIN halite'
//	    - marker_end: '# END halite'
//	    - source: files/hosts-block
//	    - append_if_not_found: true
//
// It is how one state owns its share of a file that other things also
// write to — the case file.managed cannot serve, because it owns all of it.
func fileBlockReplace(c *Ctx, id string, args map[string]any) Result {
	start := Str(args, "marker_start", "")
	end := Str(args, "marker_end", "")
	if start == "" || end == "" {
		return resFail("file.blockreplace needs marker_start and marker_end")
	}
	body, err := blockContent(c, args)
	if err != nil {
		return resFail("%v", err)
	}
	appendMissing := Bool(args, "append_if_not_found", false)
	prependMissing := Bool(args, "prepend_if_not_found", false)

	return editFile(c, id, args, func(current []byte, exists bool) ([]byte, string, error) {
		if !exists && !appendMissing && !prependMissing {
			return nil, "", fmt.Errorf("%s does not exist", Str(args, "name", id))
		}
		lines := splitLines(current)
		block := append(append([]string{start}, body...), end)

		first := firstMatch(lines, start)
		last := -1
		if first >= 0 {
			last = firstMatchFrom(lines, end, first+1)
		}
		switch {
		case first >= 0 && last >= 0:
			updated := append(append(append([]string{}, lines[:first]...), block...), lines[last+1:]...)
			if strings.Join(updated, "\n") == strings.Join(lines, "\n") {
				return current, "", nil
			}
			return joinLines(updated), "block updated", nil
		case first >= 0:
			return nil, "", fmt.Errorf("marker_start %q has no matching marker_end %q", start, end)
		case prependMissing:
			return joinLines(append(block, lines...)), "block prepended", nil
		case appendMissing:
			return joinLines(append(lines, block...)), "block appended", nil
		}
		return current, "", nil
	})
}

// blockContent is the block's body, from `content` or from a `source` file
// beside the SLS, optionally rendered like file.managed's.
func blockContent(c *Ctx, args map[string]any) ([]string, error) {
	if source := Str(args, "source", ""); source != "" {
		path := source
		if !filepath.IsAbs(path) && c.BaseDir != "" {
			path = filepath.Join(c.BaseDir, path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("cannot read source %s: %w", path, err)
		}
		if tpl := Str(args, "template", ""); tpl == "true" || tpl == "go" {
			rendered, err := sls.Render(filepath.Base(path), string(b),
				sls.TemplateData{Grains: c.Grains, Pillar: c.Pillar, Mine: c.Mine})
			if err != nil {
				return nil, fmt.Errorf("render source %s: %w", path, err)
			}
			b = []byte(rendered)
		}
		return splitLines(b), nil
	}
	return splitLines([]byte(Str(args, "content", ""))), nil
}

func firstMatchFrom(lines []string, match string, from int) int {
	for i := from; i < len(lines); i++ {
		if strings.Contains(lines[i], match) {
			return i
		}
	}
	return -1
}
