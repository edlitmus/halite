package builtin

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/regexcompat"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerFileEdit installs the in-place editing half of the file module:
// replace, line, blockreplace, append, prepend, comment, uncomment,
// search, and contains.
//
// Every one of these is a pattern against a file that already exists, and
// every one of them writes through writeAtomic. The regular expressions go
// through regexcompat, so a pattern using a construct RE2 lacks is a hard
// error naming the construct rather than a silent non-match — which in
// file.replace would be a state that reports success and changes nothing.
// SPEC section 10.4.
func registerFileEdit(r *Registries) {
	registerFileEditExec(r)
	registerFileEditStates(r)
}

// editParams are shared by the exec and state forms of file.replace.
func replaceParams(nameDoc string) []signature.Param {
	return []signature.Param{
		{Name: "name", Type: signature.Path, Required: true, Doc: nameDoc},
		req("pattern", signature.String, "The RE2 pattern to match."),
		req("repl", signature.String, "The replacement, with \\1 or ${1} group references."),
		opt("count", signature.Int, int64(0), "Replace at most this many; 0 means all."),
		opt("flags", signature.List, nil, "Pattern flags: ignorecase, multiline, dotall."),
		opt("append_if_not_found", signature.Bool, false, "Append not_found_content when the pattern never matches."),
		opt("prepend_if_not_found", signature.Bool, false, "Prepend not_found_content when the pattern never matches."),
		opt("not_found_content", signature.String, "", "What to add when the pattern is not found; defaults to repl."),
		opt("backup", signature.String, "", "Keep a copy of the previous contents with this suffix."),
		opt("show_changes", signature.Bool, true, "Include a unified diff in the changes."),
	}
}

func registerFileEditExec(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "replace",
				Doc:      "Replace every match of a pattern in a file.",
				Params:   replaceParams("The file to edit."),
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				res, err := fileReplace(c, args)
				if err != nil {
					return nil, err
				}
				return res.Comment, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "search",
				Doc: "Report whether a pattern matches anywhere in a file.",
				Params: []signature.Param{
					req("path", signature.Path, "The file to search."),
					req("pattern", signature.String, "The RE2 pattern."),
					opt("flags", signature.List, nil, "Pattern flags: ignorecase, multiline, dotall."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				re, err := compilePattern(states.Str(args, "pattern", ""), states.Strings(args, "flags"))
				if err != nil {
					return nil, err
				}
				b, err := os.ReadFile(states.Str(args, "path", ""))
				if err != nil {
					return nil, err
				}
				return re.Match(b), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "contains",
				Doc: "Report whether a file contains a literal string.",
				Params: []signature.Param{
					req("path", signature.Path, "The file to search."),
					req("text", signature.String, "The literal text."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				b, err := os.ReadFile(states.Str(args, "path", ""))
				if err != nil {
					return nil, err
				}
				return strings.Contains(string(b), states.Str(args, "text", "")), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "get_diff",
				Doc: "Return a unified diff between two files.",
				Params: []signature.Param{
					req("a", signature.Path, "The first file."),
					req("b", signature.Path, "The second file."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				aPath, bPath := states.Str(args, "a", ""), states.Str(args, "b", "")
				aBytes, err := os.ReadFile(aPath)
				if err != nil {
					return nil, err
				}
				bBytes, err := os.ReadFile(bPath)
				if err != nil {
					return nil, err
				}
				return unifiedDiff(string(aBytes), string(bBytes), aPath), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "copy",
				Doc: "Copy a file, atomically.",
				Params: []signature.Param{
					req("src", signature.Path, "The file to copy."),
					req("dst", signature.Path, "Where to copy it."),
					opt("mode", signature.Mode, "", "The mode of the copy; defaults to the source's."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				src, dst := states.Str(args, "src", ""), states.Str(args, "dst", "")
				b, err := os.ReadFile(src)
				if err != nil {
					return nil, err
				}
				mode := os.FileMode(0o644)
				if m := states.Str(args, "mode", ""); m != "" {
					mode, err = parseMode(m)
					if err != nil {
						return nil, err
					}
				} else if info, err := os.Stat(src); err == nil {
					mode = info.Mode().Perm()
				}
				if c.Test {
					return true, nil
				}
				return true, writeAtomic(dst, b, mode)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "append",
				Doc: "Append lines to a file if they are not already present.",
				Params: []signature.Param{
					req("path", signature.Path, "The file to append to."),
					req("text", signature.List, "The line or lines to append."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				res, err := fileAppendPrepend(c, args, false)
				if err != nil {
					return nil, err
				}
				return res.Comment, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "prepend",
				Doc: "Prepend lines to a file if they are not already present.",
				Params: []signature.Param{
					req("path", signature.Path, "The file to prepend to."),
					req("text", signature.List, "The line or lines to prepend."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				res, err := fileAppendPrepend(c, args, true)
				if err != nil {
					return nil, err
				}
				return res.Comment, nil
			},
		},
	)
}

func registerFileEditStates(r *Registries) {
	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "file", Function: "replace",
				Doc:      "Ensure every match of a pattern in a file has been replaced.",
				Params:   replaceParams("The file to edit. Defaults to the state ID."),
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: fileReplace,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "file", Function: "line",
				Doc: "Ensure a single line is present, absent, or replaced in a file.",
				Params: []signature.Param{
					pathParam("The file to edit. Defaults to the state ID."),
					opt("content", signature.String, "", "The line."),
					choice("mode", "ensure", "What to do: ensure, absent, replace, insert, or delete.",
						"ensure", "absent", "replace", "insert", "delete"),
					opt("match", signature.String, "", "The existing line to act on; defaults to content."),
					choice("location", "", "Where to insert: start or end.", "", "start", "end"),
					opt("before", signature.String, "", "Insert before the first line matching this."),
					opt("after", signature.String, "", "Insert after the first line matching this."),
					opt("indent", signature.Bool, true, "Match the indentation of the surrounding line."),
					opt("backup", signature.String, "", "Keep a copy with this suffix."),
					opt("show_changes", signature.Bool, true, "Include a unified diff."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: fileLine,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "file", Function: "blockreplace",
				Doc: "Ensure a marked block of a file holds the given content, adding the markers if they are missing.",
				Params: []signature.Param{
					pathParam("The file to edit. Defaults to the state ID."),
					req("marker_start", signature.String, "The line that opens the managed block."),
					req("marker_end", signature.String, "The line that closes the managed block."),
					opt("content", signature.String, "", "What goes between the markers."),
					opt("append_if_not_found", signature.Bool, true, "Append the block when the markers are missing."),
					opt("prepend_if_not_found", signature.Bool, false, "Prepend the block when the markers are missing."),
					opt("backup", signature.String, "", "Keep a copy with this suffix."),
					opt("show_changes", signature.Bool, true, "Include a unified diff."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: fileBlockReplace,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "file", Function: "append",
				Doc: "Ensure lines are present at the end of a file.",
				Params: []signature.Param{
					pathParam("The file to append to. Defaults to the state ID."),
					opt("text", signature.List, nil, "The line or lines."),
					opt("backup", signature.String, "", "Keep a copy with this suffix."),
					opt("show_changes", signature.Bool, true, "Include a unified diff."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return fileAppendPrepend(c, withPathFromName(args), false)
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "file", Function: "prepend",
				Doc: "Ensure lines are present at the start of a file.",
				Params: []signature.Param{
					pathParam("The file to prepend to. Defaults to the state ID."),
					opt("text", signature.List, nil, "The line or lines."),
					opt("backup", signature.String, "", "Keep a copy with this suffix."),
					opt("show_changes", signature.Bool, true, "Include a unified diff."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return fileAppendPrepend(c, withPathFromName(args), true)
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "file", Function: "comment",
				Doc: "Ensure the lines matching a pattern are commented out.",
				Params: []signature.Param{
					pathParam("The file to edit. Defaults to the state ID."),
					req("regex", signature.String, "The RE2 pattern selecting the lines."),
					opt("char", signature.String, "#", "The comment character."),
					opt("backup", signature.String, "", "Keep a copy with this suffix."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return fileComment(c, args, true)
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "file", Function: "uncomment",
				Doc: "Ensure the commented lines matching a pattern are uncommented.",
				Params: []signature.Param{
					pathParam("The file to edit. Defaults to the state ID."),
					req("regex", signature.String, "The RE2 pattern selecting the lines, without the comment character."),
					opt("char", signature.String, "#", "The comment character."),
					opt("backup", signature.String, "", "Keep a copy with this suffix."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return fileComment(c, args, false)
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "file", Function: "copy",
				Doc: "Ensure a file is a copy of another.",
				Params: []signature.Param{
					pathParam("The destination. Defaults to the state ID."),
					req("source", signature.Path, "The file to copy from."),
					opt("mode", signature.Mode, "", "The mode of the copy."),
					opt("makedirs", signature.Bool, false, "Create the parent directories."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.5",
			},
			Fn: fileCopyState,
		},
	)
}

// withPathFromName lets one implementation back both the exec form, which
// takes `path`, and the state form, which takes `name`.
func withPathFromName(args *value.Map) *value.Map {
	if args.Has("path") {
		return args
	}
	out := args.Clone()
	if v, ok := args.Get("name"); ok {
		out.Set("path", v)
	}
	return out
}

// compilePattern builds a pattern with Salt's flag names.
func compilePattern(pattern string, flags []string) (*regexp.Regexp, error) {
	var ignoreCase, multiline, dotAll bool
	for _, f := range flags {
		switch strings.ToUpper(f) {
		case "IGNORECASE", "I":
			ignoreCase = true
		case "MULTILINE", "M":
			multiline = true
		case "DOTALL", "S":
			dotAll = true
		default:
			return nil, fmt.Errorf("unknown pattern flag %q; halite accepts ignorecase, multiline, and dotall", f)
		}
	}
	// Multiline is the default for a file edit, because a pattern written
	// against a configuration file almost always means "this line".
	if !dotAll {
		multiline = true
	}
	return regexcompat.CompileWithFlags(pattern, ignoreCase, multiline, dotAll)
}

// editResult is the shared tail of every editing function: write the new
// contents if they differ, and report what changed.
func editResult(c *exec.Context, path string, before, after []byte, args *value.Map, describe func(changed bool) string) (states.Result, error) {
	if string(before) == string(after) {
		return states.True(describe(false)), nil
	}

	changes := value.NewMap(2)
	if states.Bool(args, "show_changes", true) {
		changes.Set("diff", unifiedDiff(string(before), string(after), path))
	} else {
		changes.Set("diff", "<changed>")
	}

	if c.Test {
		return states.WouldChange(describe(true), changes), nil
	}

	info, err := os.Stat(path)
	mode := os.FileMode(0o644)
	if err == nil {
		mode = info.Mode().Perm()
	}
	if suffix := states.Str(args, "backup", ""); suffix != "" {
		if err := writeAtomic(path+suffix, before, mode); err != nil {
			return states.False(fmt.Sprintf("The backup of %s could not be written: %v", path, err)), nil
		}
		changes.Set("backup", path+suffix)
	}
	if err := writeAtomic(path, after, mode); err != nil {
		return states.False(fmt.Sprintf("%s could not be written: %v", path, err)), nil
	}
	return states.Changed(describe(true), changes), nil
}

// readEditTarget reads the file an edit acts on, reporting a missing file
// as a failure rather than creating one: an edit is a change to something
// that exists, and file.managed is the state that creates.
func readEditTarget(args *value.Map) (path string, data []byte, res states.Result, ok bool) {
	path = states.Str(args, "path", "")
	if path == "" {
		path = states.Str(args, "name", "")
	}
	if path == "" {
		return "", nil, states.False("This state needs a file path."), false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, nil, states.False(fmt.Sprintf(
				"%s does not exist; use file.managed to create it before editing it.", path)), false
		}
		return path, nil, states.False(fmt.Sprintf("%s could not be read: %v", path, err)), false
	}
	return path, data, states.Result{}, true
}

func fileReplace(c *exec.Context, args *value.Map) (states.Result, error) {
	path, before, res, ok := readEditTarget(args)
	if !ok {
		return res, nil
	}

	re, err := compilePattern(states.Str(args, "pattern", ""), states.Strings(args, "flags"))
	if err != nil {
		return states.False(fmt.Sprintf("The pattern for %s is not usable: %v", path, err)), nil
	}
	repl := pythonGroupRefs(states.Str(args, "repl", ""))

	var after []byte
	count := states.Int(args, "count", 0)
	if count > 0 {
		after = replaceN(re, before, repl, int(count))
	} else {
		after = re.ReplaceAll(before, []byte(repl))
	}

	if !re.Match(before) {
		// The pattern never matched. Salt trees use this to mean "add the
		// setting if the file does not have it yet".
		content := states.Str(args, "not_found_content", "")
		if content == "" {
			content = states.Str(args, "repl", "")
		}
		switch {
		case states.Bool(args, "append_if_not_found", false):
			after = []byte(ensureTrailingNewline(string(before)) + ensureTrailingNewline(content))
		case states.Bool(args, "prepend_if_not_found", false):
			after = []byte(ensureTrailingNewline(content) + string(before))
		default:
			return states.True(fmt.Sprintf(
				"The pattern does not match anything in %s, and neither append_if_not_found nor prepend_if_not_found is set.", path)), nil
		}
	}

	return editResult(c, path, before, after, args, func(changed bool) string {
		if !changed {
			return fmt.Sprintf("%s already has every match replaced.", path)
		}
		return fmt.Sprintf("The pattern was replaced in %s.", path)
	})
}

// replaceN replaces at most n matches, which Go's regexp has no direct
// form for.
func replaceN(re *regexp.Regexp, src []byte, repl string, n int) []byte {
	var out []byte
	last := 0
	done := 0
	for _, m := range re.FindAllSubmatchIndex(src, -1) {
		if done >= n {
			break
		}
		out = append(out, src[last:m[0]]...)
		out = re.Expand(out, []byte(repl), src, m)
		last = m[1]
		done++
	}
	return append(out, src[last:]...)
}

// pythonGroupRefs rewrites Python's \1 group references into Go's ${1}, so
// a pattern carried over from a Salt tree substitutes the same way.
func pythonGroupRefs(repl string) string {
	var b strings.Builder
	for i := 0; i < len(repl); i++ {
		if repl[i] == '\\' && i+1 < len(repl) && repl[i+1] >= '0' && repl[i+1] <= '9' {
			j := i + 1
			for j < len(repl) && repl[j] >= '0' && repl[j] <= '9' {
				j++
			}
			b.WriteString("${" + repl[i+1:j] + "}")
			i = j - 1
			continue
		}
		if repl[i] == '$' {
			b.WriteString("$$")
			continue
		}
		b.WriteByte(repl[i])
	}
	return b.String()
}

func fileAppendPrepend(c *exec.Context, args *value.Map, prepend bool) (states.Result, error) {
	path, before, res, ok := readEditTarget(args)
	if !ok {
		return res, nil
	}
	lines := states.Strings(args, "text")
	if len(lines) == 0 {
		lines = states.Strings(args, "content")
	}
	if len(lines) == 0 {
		return states.False("This state names no lines to add."), nil
	}

	existing := splitKeepEmpty(string(before))
	present := map[string]bool{}
	for _, l := range existing {
		present[l] = true
	}

	var missing []string
	for _, l := range lines {
		if !present[l] {
			missing = append(missing, l)
			present[l] = true
		}
	}
	if len(missing) == 0 {
		verb := "end"
		if prepend {
			verb = "start"
		}
		return states.True(fmt.Sprintf("Every line is already present at the %s of %s.", verb, path)), nil
	}

	var after string
	if prepend {
		after = strings.Join(missing, "\n") + "\n" + string(before)
	} else {
		after = ensureTrailingNewline(string(before)) + strings.Join(missing, "\n") + "\n"
	}

	return editResult(c, path, before, []byte(after), args, func(changed bool) string {
		verb := "appended to"
		if prepend {
			verb = "prepended to"
		}
		return fmt.Sprintf("%d line(s) were %s %s.", len(missing), verb, path)
	})
}

func fileComment(c *exec.Context, args *value.Map, commenting bool) (states.Result, error) {
	path, before, res, ok := readEditTarget(args)
	if !ok {
		return res, nil
	}
	char := states.Str(args, "char", "#")
	pattern := states.Str(args, "regex", "")

	re, err := compilePattern(pattern, nil)
	if err != nil {
		return states.False(fmt.Sprintf("The pattern for %s is not usable: %v", path, err)), nil
	}

	lines := strings.Split(string(before), "\n")
	touched := 0
	for i, line := range lines {
		isCommented := strings.HasPrefix(strings.TrimSpace(line), char)
		switch {
		case commenting && !isCommented && re.MatchString(line):
			lines[i] = char + line
			touched++
		case !commenting && isCommented:
			// The pattern is matched against the line with its comment
			// character removed, which is how an operator writes it.
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			body := strings.TrimPrefix(strings.TrimLeft(line, " \t"), char)
			if re.MatchString(body) {
				lines[i] = indent + body
				touched++
			}
		}
	}

	after := strings.Join(lines, "\n")
	return editResult(c, path, before, []byte(after), args, func(changed bool) string {
		verb := "commented"
		if !commenting {
			verb = "uncommented"
		}
		if !changed {
			return fmt.Sprintf("The matching lines in %s are already %s.", path, verb)
		}
		return fmt.Sprintf("%d line(s) in %s were %s.", touched, path, verb)
	})
}

func fileBlockReplace(c *exec.Context, args *value.Map) (states.Result, error) {
	path, before, res, ok := readEditTarget(args)
	if !ok {
		return res, nil
	}
	start := states.Str(args, "marker_start", "")
	end := states.Str(args, "marker_end", "")
	content := states.Str(args, "content", "")

	block := start + "\n" + ensureTrailingNewline(content) + end + "\n"

	text := string(before)
	startIdx := strings.Index(text, start)
	endIdx := strings.Index(text, end)

	var after string
	switch {
	case startIdx >= 0 && endIdx > startIdx:
		after = text[:startIdx] + block + text[endIdx+len(end):]
		// A newline immediately after the end marker belongs to the
		// block, not to what follows it.
		after = strings.Replace(after, block+"\n", block, 1)
	case states.Bool(args, "prepend_if_not_found", false):
		after = block + text
	case states.Bool(args, "append_if_not_found", true):
		after = ensureTrailingNewline(text) + block
	default:
		return states.False(fmt.Sprintf(
			"The markers were not found in %s, and neither append_if_not_found nor prepend_if_not_found is set.", path)), nil
	}

	return editResult(c, path, before, []byte(after), args, func(changed bool) string {
		if !changed {
			return fmt.Sprintf("The managed block in %s already holds the requested content.", path)
		}
		return fmt.Sprintf("The managed block in %s was updated.", path)
	})
}

func fileCopyState(c *exec.Context, args *value.Map) (states.Result, error) {
	dst := states.Str(args, "name", "")
	src := states.Str(args, "source", "")
	if dst == "" || src == "" {
		return states.False("This state needs both a destination and a source."), nil
	}

	want, err := os.ReadFile(src)
	if err != nil {
		return states.False(fmt.Sprintf("The source %s could not be read: %v", src, err)), nil
	}
	current, _ := os.ReadFile(dst)

	if string(current) == string(want) {
		return states.True(fmt.Sprintf("%s is already a copy of %s.", dst, src)), nil
	}
	changes := value.MapOf("diff", unifiedDiff(string(current), string(want), dst))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be copied from %s.", dst, src), changes), nil
	}
	if states.Bool(args, "makedirs", false) {
		if err := os.MkdirAll(dirOf(dst), 0o755); err != nil {
			return states.False(fmt.Sprintf("The parent directories of %s could not be created: %v", dst, err)), nil
		}
	}
	mode := os.FileMode(0o644)
	if m := states.Str(args, "mode", ""); m != "" {
		mode, err = parseMode(m)
		if err != nil {
			return states.False(fmt.Sprintf("The mode for %s is invalid: %v", dst, err)), nil
		}
	} else if info, err := os.Stat(src); err == nil {
		mode = info.Mode().Perm()
	}
	if err := writeAtomic(dst, want, mode); err != nil {
		return states.False(fmt.Sprintf("%s could not be written: %v", dst, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s was copied from %s.", dst, src), changes), nil
}
