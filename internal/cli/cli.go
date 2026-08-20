// Package cli holds what the three binaries share: argument parsing in
// Salt's conventions, output formatting, and the local execution context.
package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// Args is a parsed command line: the flags, the positional arguments, and
// the key=value pairs.
//
// Salt's habit of YAML-parsing every command line argument is not
// reproduced. It is the cause of a long tail of surprises, such as a
// package version 1.0 becoming a float and NO becoming a boolean:
// arguments are strings unless a type is declared, and each module
// function declares its parameter types. SPEC section 9.2.
type Args struct {
	// Flags are the --name and --name=value options.
	Flags map[string]string
	// Positional are the bare arguments, in order.
	Positional []string
	// Kwargs are the key=value arguments.
	Kwargs *value.Map
}

// Flag reads a flag with a default.
func (a *Args) Flag(name, def string) string {
	if v, ok := a.Flags[name]; ok {
		if v == "" {
			return "true"
		}
		return v
	}
	return def
}

// Has reports whether a flag was given.
func (a *Args) Has(name string) bool { _, ok := a.Flags[name]; return ok }

// Bool reads a boolean flag. A bare `--name` is true.
func (a *Args) Bool(name string, def bool) bool {
	v, ok := a.Flags[name]
	if !ok {
		return def
	}
	switch strings.ToLower(v) {
	case "", "true", "yes", "1", "on":
		return true
	case "false", "no", "0", "off":
		return false
	}
	return def
}

// BoolFlags are the long flags that never take a value, so that
// `--test apply` reads as the flag plus a positional rather than
// swallowing the subcommand.
//
// Everything else is assumed to take a value when the next token is not
// itself a flag, which is what makes both `--out json` and `--out=json`
// work the way an operator expects.
var BoolFlags = map[string]bool{
	"local": true, "test": true, "permissive": true, "help": true,
	"version": true, "diff": true, "progress": true, "force": true,
	"async": true, "verbose": true, "require-match": true, "fresh": true,
	"clean": true, "no-color": true, "failhard": true,
}

// Parse splits an argument vector.
func Parse(argv []string) (*Args, error) {
	a := &Args{Flags: map[string]string{}, Kwargs: value.NewMap(4)}
	positionalOnly := false

	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--":
			// -- ends option parsing, not argument parsing: a key=value
			// pair after it is still a kwarg, because it is an argument
			// rather than an option. A literal that must not be read as a
			// kwarg is one whose key is not an identifier, which is
			// already the rule looksLikeKwarg applies.
			positionalOnly = true

		case !positionalOnly && strings.HasPrefix(arg, "--"):
			name, val, hasVal := strings.Cut(arg[2:], "=")
			if hasVal {
				a.Flags[name] = val
				continue
			}
			if !BoolFlags[name] && i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
				a.Flags[name] = argv[i+1]
				i++
				continue
			}
			a.Flags[name] = ""

		case !positionalOnly && strings.HasPrefix(arg, "-") && len(arg) > 1:
			// A single-letter target flag takes the next argument, which
			// is how `-G os_family:Debian` is written.
			name := arg[1:]
			if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
				a.Flags[name] = argv[i+1]
				i++
				continue
			}
			a.Flags[name] = ""

		case looksLikeKwarg(arg):
			key, val, _ := strings.Cut(arg, "=")
			parsed, err := parseKwargValue(val)
			if err != nil {
				return nil, fmt.Errorf("argument %s: %w", key, err)
			}
			a.Kwargs.Set(key, parsed)

		default:
			a.Positional = append(a.Positional, arg)
		}
	}
	return a, nil
}

// looksLikeKwarg reports whether an argument is a key=value pair rather
// than a positional argument that happens to contain an equals sign.
func looksLikeKwarg(arg string) bool {
	key, _, ok := strings.Cut(arg, "=")
	if !ok || key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// parseKwargValue reads a command line value.
//
// A bare value is a string. JSON is recognised only when it is written as
// JSON, which is how a structured argument is passed unambiguously:
// `pkgs='["a","b"]'`. That is the whole departure from Salt here, and it
// is what stops a version 1.0 from becoming a float.
func parseKwargValue(s string) (any, error) {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) >= 2 {
		switch trimmed[0] {
		case '[', '{':
			v, err := value.DecodeJSON([]byte(trimmed))
			if err != nil {
				return nil, fmt.Errorf("looks like JSON but did not parse: %w", err)
			}
			return v, nil
		}
	}
	// The explicit booleans and null are recognised, because a flag
	// argument such as `test=True` has no other sensible reading.
	switch trimmed {
	case "True", "true":
		return true, nil
	case "False", "false":
		return false, nil
	case "None", "null":
		return nil, nil
	}
	return s, nil
}

// ---- output ----

// Format names an output rendering.
type Format string

const (
	// Nested is Salt's default rendering.
	Nested Format = "nested"
	// JSON is the frozen, versioned schema downstream tooling depends on.
	JSON Format = "json"
	// YAML renders through halite's own encoder.
	YAML Format = "yaml"
	// Quiet prints nothing.
	Quiet Format = "quiet"
	// Txt prints one line per top-level key.
	Txt Format = "txt"
)

// ParseFormat reads an --out value.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case Nested, JSON, YAML, Quiet, Txt:
		return Format(s), nil
	case "":
		return Nested, nil
	}
	return "", fmt.Errorf("unknown output format %q; halite offers nested, json, yaml, txt, and quiet", s)
}

// Write renders a value in the chosen format.
func Write(w io.Writer, v any, f Format, indent int) error {
	switch f {
	case Quiet:
		return nil
	case JSON:
		b, err := value.EncodeJSON(v, indent)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	case YAML:
		_, err := fmt.Fprint(w, yaml.Encode(v, yaml.EncodeOptions{}))
		return err
	case Txt:
		return writeTxt(w, v)
	default:
		return writeNested(w, v, 0)
	}
}

func writeTxt(w io.Writer, v any) error {
	m, ok := v.(*value.Map)
	if !ok {
		_, err := fmt.Fprintln(w, renderScalar(v))
		return err
	}
	for _, e := range m.Entries() {
		if _, err := fmt.Fprintf(w, "%s: %s\n", value.KeyString(e.Key), renderScalar(e.Val)); err != nil {
			return err
		}
	}
	return nil
}

// writeNested renders the tree the way `--out=nested` does.
func writeNested(w io.Writer, v any, depth int) error {
	pad := strings.Repeat("    ", depth)
	switch t := v.(type) {
	case *value.Map:
		for _, e := range t.Entries() {
			key := value.KeyString(e.Key)
			switch sub := e.Val.(type) {
			case *value.Map:
				if sub.Len() == 0 {
					fmt.Fprintf(w, "%s%s: {}\n", pad, key)
					continue
				}
				fmt.Fprintf(w, "%s%s:\n", pad, key)
				if err := writeNested(w, sub, depth+1); err != nil {
					return err
				}
			case []any:
				if len(sub) == 0 {
					fmt.Fprintf(w, "%s%s: []\n", pad, key)
					continue
				}
				fmt.Fprintf(w, "%s%s:\n", pad, key)
				if err := writeNested(w, sub, depth+1); err != nil {
					return err
				}
			default:
				fmt.Fprintf(w, "%s%s: %s\n", pad, key, renderScalar(e.Val))
			}
		}
	case []any:
		for _, item := range t {
			switch sub := item.(type) {
			case *value.Map, []any:
				fmt.Fprintf(w, "%s-\n", pad)
				if err := writeNested(w, sub, depth+1); err != nil {
					return err
				}
			default:
				fmt.Fprintf(w, "%s- %s\n", pad, renderScalar(item))
			}
		}
	default:
		fmt.Fprintf(w, "%s%s\n", pad, renderScalar(v))
	}
	return nil
}

func renderScalar(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case bool:
		if t {
			return "True"
		}
		return "False"
	case string:
		if strings.Contains(t, "\n") {
			return "|\n        " + strings.ReplaceAll(strings.TrimRight(t, "\n"), "\n", "\n        ")
		}
		return t
	}
	return value.KeyString(v)
}

// Fatalf prints an error and exits non-zero. The message goes to stderr so
// that a caller piping the output gets only the data.
func Fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "halite: "+format+"\n", args...)
	os.Exit(1)
}
