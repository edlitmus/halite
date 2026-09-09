package builtin

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// mac_defaults, SPEC section 15.3's macOS row and the one macOS module
// SPEC 15.5 also names as a core state.
//
// It drives `defaults(1)`, which is the only supported way to read and
// write a macOS preference domain: the files under `Library/Preferences`
// are a binary plist cache that `cfprefsd` owns, and a process that
// writes one behind its back has its change dropped the next time the
// daemon flushes. So this shells out, as Salt's `macdefaults` does, and
// does not touch the plist on disk.
//
// **Reading goes through `defaults export`, not `defaults read`.**
// `defaults read <domain> <key>` prints a value in a display format that
// is lossy — a boolean reads as `1`, a float's precision is trimmed, and
// an array is a parenthesised list a parser has to guess the element
// types of. `defaults export <domain> -` writes a real XML property
// list, which is unambiguous, and it exits 0 with an empty dict for a
// domain that does not exist rather than failing. The state's whole job
// is deciding whether the current value already matches, so it needs the
// value and its type, not a rendering of it.
//
// **The comparison is typed.** `defaults` keeps `-int 1` and `-bool 1`
// and `-string 1` as three different things, and a state that wrote a
// string where the tree asked for an integer would report the key
// converged while a program reading it as a number found nothing. The
// plist parser returns `int64` for `<integer>` and `float64` for
// `<real>`, `wantValue` produces the same Go type the declared `vtype`
// implies, and a mismatch of kind is a difference that rewrites — so the
// second run after a type change still converges.
//
// **`user` runs `defaults` as that account.** A preference domain is
// per-user, resolved from `$HOME`, so managing a login item or a Finder
// setting for a real user means becoming them. The `user` parameter maps
// to the command's `RunAs`, which is setuid/setgid with the account's
// full group set and its `HOME` — not `su -c` and not `sudo`.
// macOnly is the platform restriction every function in the macOS row
// carries: the signature refuses the call before it reaches `defaults`,
// so a node that is not a Mac gets "runs on darwin, and this node is X"
// rather than "defaults: command not found".
var macOnly = []string{"darwin"}

func registerMacDefaults(r *Registries) {
	domain := req("domain", signature.String,
		"The preference domain, such as `com.apple.dock` or `NSGlobalDomain`. "+
			"An absolute path to a plist file is also accepted, as `defaults` accepts one.")
	keyOpt := opt("key", signature.String, "",
		"The key within the domain. Omitted, the whole domain is read or removed.")
	keyReq := req("key", signature.String, "The key within the domain.")
	user := opt("user", signature.String, "",
		"Run `defaults` as this account, so its own preference domain is the one "+
			"read or written. The invoking user by default.")

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_defaults", Function: "read",
				Doc: "Read a preference value, or the whole domain as a mapping when no key is given. " +
					"The value keeps its plist type: a string, an integer, a float, a boolean, " +
					"a list, or a mapping.",
				Params:    []signature.Param{domain, keyOpt, user},
				Returns:   "the typed value, or the domain as a mapping; an error if the key is not set",
				TestMode:  signature.TestNotApplicable,
				Platforms: macOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				dom := states.Str(args, "domain", "")
				key := states.Str(args, "key", "")
				dict, err := macDefaultsExport(c, dom, states.Str(args, "user", ""))
				if err != nil {
					return nil, err
				}
				if key == "" {
					return dict, nil
				}
				v, ok := dict.Get(key)
				if !ok {
					return nil, fmt.Errorf("mac_defaults.read: %q is not set in domain %q", key, dom)
				}
				return v, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_defaults", Function: "read_type",
				Doc: "Name the plist type `defaults` records for a key: string, integer, " +
					"float, boolean, array, dictionary, data or date.",
				Params:    []signature.Param{domain, keyReq, user},
				Returns:   "the type name; an error if the key is not set",
				TestMode:  signature.TestNotApplicable,
				Platforms: macOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return macDefaultsReadType(c,
					states.Str(args, "domain", ""), states.Str(args, "key", ""),
					states.Str(args, "user", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_defaults", Function: "write",
				Doc: "Write a preference value with a declared type.",
				Params: []signature.Param{
					domain, keyReq,
					req("value", signature.Any, "The value to write. For `array` and `dict` it is "+
						"a list or a mapping; for the scalar types a string, number or boolean."),
					macDefaultsTypeParam,
					user,
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  macOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				v, _ := args.Get("value")
				err := macDefaultsWrite(c,
					states.Str(args, "domain", ""), states.Str(args, "key", ""),
					states.Str(args, "vtype", "string"), v, states.Str(args, "user", ""))
				if err != nil {
					return nil, err
				}
				return true, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_defaults", Function: "delete",
				Doc: "Remove a key, or the whole domain when no key is given. " +
					"Removing a key that is not there is not an error.",
				Params:     []signature.Param{domain, keyOpt, user},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  macOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := macDefaultsDelete(c,
					states.Str(args, "domain", ""), states.Str(args, "key", ""),
					states.Str(args, "user", ""))
				if err != nil {
					return nil, err
				}
				return true, nil
			},
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "mac_defaults", Function: "write",
				Doc: "Ensure a preference key holds a value of the declared type.",
				Params: []signature.Param{
					nameParam("A description. The domain and key are their own parameters."),
					domain, keyReq,
					req("value", signature.Any, "The value the key should hold."),
					macDefaultsTypeParam,
					user,
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  macOnly,
				Section:    "15.5",
			},
			Fn: macDefaultsWriteState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "mac_defaults", Function: "absent",
				Doc: "Ensure a preference key is not set, or a whole domain is gone.",
				Params: []signature.Param{
					nameParam("A description. The domain and key are their own parameters."),
					domain, keyOpt, user,
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  macOnly,
				Section:    "15.5",
			},
			Fn: macDefaultsAbsentState,
		},
	)
}

// macDefaultsTypeParam is shared by the exec and state `write`, so the
// accepted spellings are declared in exactly one place.
var macDefaultsTypeParam = choice("vtype", "string",
	"The plist type to write. `int` and `integer` are the same, as are `bool` and "+
		"`boolean`; `array` and `dict` take a list or a mapping and are written as a plist.",
	"string", "int", "integer", "float", "bool", "boolean", "array", "dict", "plist")

// macDefaultsBin is the path to `defaults`, or "" when it is not here.
func macDefaultsBin(c *exec.Context) string { return c.Which("defaults") }

func macDefaultsRequire(c *exec.Context) error {
	if macDefaultsBin(c) == "" {
		return fmt.Errorf("mac_defaults: `defaults` was not found; this build's macOS module needs it")
	}
	return nil
}

// macDefaultsExport reads a whole domain as an XML property list and
// parses it. A missing domain is an empty mapping, not an error, because
// every caller is asking "what is set here" and "nothing" is an answer.
func macDefaultsExport(c *exec.Context, domain, user string) (*value.Map, error) {
	if err := macDefaultsRequire(c); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{
		Argv:  []string{"defaults", "export", domain, "-"},
		RunAs: user,
	})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("defaults export %s: %s", domain, firstLine(res.Stderr+res.Stdout))
	}
	v, err := parsePlist([]byte(res.Stdout))
	if err != nil {
		return nil, fmt.Errorf("defaults export %s: %w", domain, err)
	}
	m, ok := v.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("defaults export %s: top level is %s, not a dict", domain, value.TypeName(v))
	}
	return m, nil
}

func macDefaultsReadType(c *exec.Context, domain, key, user string) (string, error) {
	if err := macDefaultsRequire(c); err != nil {
		return "", err
	}
	res, err := c.Run(exec.Command{
		Argv:  []string{"defaults", "read-type", domain, key},
		RunAs: user,
	})
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", fmt.Errorf("mac_defaults.read_type: %q is not set in domain %q", key, domain)
	}
	// `defaults read-type` prints "Type is string".
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(res.Stdout), "Type is")), nil
}

func macDefaultsWrite(c *exec.Context, domain, key, vtype string, v any, user string) error {
	if err := macDefaultsRequire(c); err != nil {
		return err
	}
	argv, err := macDefaultsWriteArgv(domain, key, vtype, v)
	if err != nil {
		return err
	}
	res, err := c.Run(exec.Command{Argv: argv, RunAs: user})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("defaults write %s %s: %s", domain, key, firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

func macDefaultsDelete(c *exec.Context, domain, key, user string) error {
	if err := macDefaultsRequire(c); err != nil {
		return err
	}
	argv := []string{"defaults", "delete", domain}
	if key != "" {
		argv = append(argv, key)
	}
	res, err := c.Run(exec.Command{Argv: argv, RunAs: user})
	if err != nil {
		return err
	}
	// `defaults delete` on a key or domain that is not there exits
	// non-zero with "does not exist". That is the state we wanted, so it
	// is not an error here; anything else is.
	if res.Code != 0 && !strings.Contains(res.Stderr+res.Stdout, "does not exist") {
		return fmt.Errorf("defaults delete %s %s: %s", domain, key, firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

// macDefaultsWriteArgv builds the `defaults write` invocation for a
// value and a declared type. The scalar types go through `defaults`'
// own `-string`/`-int`/`-float`/`-bool` flags; `array`, `dict` and
// `plist` are serialised to an XML property list and passed as the
// single value argument, which `defaults write` accepts.
func macDefaultsWriteArgv(domain, key, vtype string, v any) ([]string, error) {
	base := []string{"defaults", "write", domain, key}
	switch vtype {
	case "string":
		return append(base, "-string", scalarString(v)), nil
	case "int", "integer":
		n, err := scalarInt(v)
		if err != nil {
			return nil, fmt.Errorf("mac_defaults: value %v is not an integer", v)
		}
		return append(base, "-int", strconv.FormatInt(n, 10)), nil
	case "float":
		f, err := scalarFloat(v)
		if err != nil {
			return nil, fmt.Errorf("mac_defaults: value %v is not a number", v)
		}
		return append(base, "-float", strconv.FormatFloat(f, 'g', -1, 64)), nil
	case "bool", "boolean":
		if value.Truthy(v) {
			return append(base, "-bool", "true"), nil
		}
		return append(base, "-bool", "false"), nil
	case "array", "dict", "plist":
		return append(base, plistXML(v)), nil
	default:
		return nil, fmt.Errorf("mac_defaults: unknown type %q", vtype)
	}
}

// wantValue is the value a converged key would hold, in the same Go
// types parsePlist produces, so the two can be compared directly.
func wantValue(vtype string, v any) (any, error) {
	switch vtype {
	case "string":
		return scalarString(v), nil
	case "int", "integer":
		n, err := scalarInt(v)
		return n, err
	case "float":
		f, err := scalarFloat(v)
		return f, err
	case "bool", "boolean":
		return value.Truthy(v), nil
	case "array", "dict", "plist":
		// Round-trip through the plist so the wanted value is in the
		// exact shape a read-back would be: keys ordered, numbers typed.
		x, err := parsePlist([]byte(plistXML(v)))
		if err != nil {
			return nil, err
		}
		return x, nil
	default:
		return nil, fmt.Errorf("mac_defaults: unknown type %q", vtype)
	}
}

func scalarString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return value.KeyString(v)
	}
}

func scalarInt(v any) (int64, error) {
	switch t := v.(type) {
	case int64:
		return t, nil
	case int:
		return int64(t), nil
	case float64:
		return int64(t), nil
	case string:
		return strconv.ParseInt(strings.TrimSpace(t), 10, 64)
	default:
		return 0, fmt.Errorf("not an integer")
	}
}

func scalarFloat(v any) (float64, error) {
	switch t := v.(type) {
	case float64:
		return t, nil
	case int64:
		return float64(t), nil
	case int:
		return float64(t), nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(t), 64)
	default:
		return 0, fmt.Errorf("not a number")
	}
}

// ---- the states ----

func macDefaultsWriteState(c *exec.Context, args *value.Map) (states.Result, error) {
	domain := strings.TrimSpace(states.Str(args, "domain", ""))
	key := strings.TrimSpace(states.Str(args, "key", ""))
	if domain == "" || key == "" {
		return states.False("mac_defaults.write needs a domain and a key."), nil
	}
	vtype := states.Str(args, "vtype", "string")
	user := states.Str(args, "user", "")
	raw, _ := args.Get("value")

	want, err := wantValue(vtype, raw)
	if err != nil {
		return states.False(fmt.Sprintf("%s %s: %v", domain, key, err)), nil
	}

	dict, err := macDefaultsExport(c, domain, user)
	if err != nil {
		return states.False(fmt.Sprintf("%s: the domain could not be read: %v", domain, err)), nil
	}
	current, set := dict.Get(key)

	if set && macDefaultsEqual(current, want) {
		return states.True(fmt.Sprintf("%s %s is already %s.", domain, key, macDefaultsShow(want))), nil
	}

	changes := value.MapOf(domain+" "+key, states.Change(macDefaultsChangeSide(current, set), macDefaultsChangeSide(want, true)))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s %s would be set to %s.", domain, key, macDefaultsShow(want)), changes), nil
	}
	if err := macDefaultsWrite(c, domain, key, vtype, raw, user); err != nil {
		return states.False(fmt.Sprintf("%s %s could not be written: %v", domain, key, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s %s was set to %s.", domain, key, macDefaultsShow(want)), changes), nil
}

func macDefaultsAbsentState(c *exec.Context, args *value.Map) (states.Result, error) {
	domain := strings.TrimSpace(states.Str(args, "domain", ""))
	key := strings.TrimSpace(states.Str(args, "key", ""))
	if domain == "" {
		return states.False("mac_defaults.absent needs a domain."), nil
	}
	user := states.Str(args, "user", "")

	dict, err := macDefaultsExport(c, domain, user)
	if err != nil {
		return states.False(fmt.Sprintf("%s: the domain could not be read: %v", domain, err)), nil
	}

	target := domain
	if key != "" {
		target = domain + " " + key
	}

	if key == "" {
		if dict.Len() == 0 {
			return states.True(fmt.Sprintf("%s holds no preferences.", domain)), nil
		}
	} else if _, set := dict.Get(key); !set {
		return states.True(fmt.Sprintf("%s is not set.", target)), nil
	}

	changes := value.MapOf(target, states.Change("present", nil))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be removed.", target), changes), nil
	}
	if err := macDefaultsDelete(c, domain, key, user); err != nil {
		return states.False(fmt.Sprintf("%s could not be removed: %v", target, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s was removed.", target), changes), nil
}

// macDefaultsChangeSide renders one half of a changes pair. A value that
// is a mapping or a list is kept as-is so the diff shows structure; a
// key that is not set is nil.
func macDefaultsChangeSide(v any, set bool) any {
	if !set {
		return nil
	}
	return v
}

// macDefaultsShow renders a value for a comment sentence.
func macDefaultsShow(v any) string {
	switch t := v.(type) {
	case string:
		return strconv.Quote(t)
	case *value.Map, []any:
		b, err := value.EncodeJSON(t, 0)
		if err == nil {
			return string(b)
		}
	}
	return fmt.Sprintf("%v", v)
}

// macDefaultsEqual compares a read-back value against a wanted one with
// the plist type kinds kept distinct: `defaults` stores an integer, a
// float, a string and a boolean as four different things, and a state
// that treated `1` and `true` and `"1"` as the same would report a key
// converged that a program reading it would not accept.
func macDefaultsEqual(a, b any) bool {
	switch av := a.(type) {
	case nil:
		return b == nil
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case int64:
		bv, ok := b.(int64)
		return ok && av == bv
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !macDefaultsEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	case *value.Map:
		bv, ok := b.(*value.Map)
		if !ok || av.Len() != bv.Len() {
			return false
		}
		for _, k := range av.StringKeys() {
			x, _ := av.Get(k)
			y, has := bv.Get(k)
			if !has || !macDefaultsEqual(x, y) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// ---- a minimal XML property-list reader ----
//
// `defaults export` writes a plain Apple XML plist. Only the element set
// that produces covers this: dict, array, string, integer, real, true,
// false, data and date. `data` and `date` are kept as their text — this
// module reads and writes scalars, lists and mappings, and a caller that
// needs a raw blob or a timestamp reaches `defaults` through `cmd`.

func parsePlist(b []byte) (any, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil, fmt.Errorf("plist has no value")
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local == "plist" {
			continue
		}
		return parsePlistValue(dec, se)
	}
}

func parsePlistValue(dec *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "true":
		return true, skipToEnd(dec, start.Name.Local)
	case "false":
		return false, skipToEnd(dec, start.Name.Local)
	case "string", "data", "date":
		return plistText(dec, start.Name.Local)
	case "integer":
		s, err := plistText(dec, "integer")
		if err != nil {
			return nil, err
		}
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("plist integer %q: %w", s, err)
		}
		return n, nil
	case "real":
		s, err := plistText(dec, "real")
		if err != nil {
			return nil, err
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, fmt.Errorf("plist real %q: %w", s, err)
		}
		return f, nil
	case "array":
		var out []any
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				v, err := parsePlistValue(dec, t)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			case xml.EndElement:
				if t.Name.Local == "array" {
					if out == nil {
						out = []any{}
					}
					return out, nil
				}
			}
		}
	case "dict":
		m := value.NewMap(0)
		var pendingKey string
		haveKey := false
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "key" {
					k, err := plistText(dec, "key")
					if err != nil {
						return nil, err
					}
					pendingKey = k
					haveKey = true
					continue
				}
				if !haveKey {
					return nil, fmt.Errorf("plist dict: value <%s> with no key", t.Name.Local)
				}
				v, err := parsePlistValue(dec, t)
				if err != nil {
					return nil, err
				}
				m.Set(pendingKey, v)
				haveKey = false
			case xml.EndElement:
				if t.Name.Local == "dict" {
					return m, nil
				}
			}
		}
	default:
		return nil, fmt.Errorf("plist: unexpected element <%s>", start.Name.Local)
	}
}

// plistText reads character data up to the matching end element.
func plistText(dec *xml.Decoder, name string) (string, error) {
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			if t.Name.Local == name {
				return b.String(), nil
			}
		case xml.StartElement:
			return "", fmt.Errorf("plist: <%s> inside <%s>", t.Name.Local, name)
		}
	}
}

func skipToEnd(dec *xml.Decoder, name string) error {
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if e, ok := tok.(xml.EndElement); ok && e.Name.Local == name {
			return nil
		}
	}
}

// ---- writing an XML property list ----

func plistXML(v any) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	writePlistValue(&b, v, 0)
	b.WriteString("\n</plist>\n")
	return b.String()
}

func writePlistValue(b *strings.Builder, v any, depth int) {
	ind := strings.Repeat("\t", depth)
	switch t := v.(type) {
	case nil:
		b.WriteString(ind + "<string></string>")
	case bool:
		if t {
			b.WriteString(ind + "<true/>")
		} else {
			b.WriteString(ind + "<false/>")
		}
	case int:
		b.WriteString(ind + "<integer>" + strconv.Itoa(t) + "</integer>")
	case int64:
		b.WriteString(ind + "<integer>" + strconv.FormatInt(t, 10) + "</integer>")
	case float64:
		b.WriteString(ind + "<real>" + strconv.FormatFloat(t, 'g', -1, 64) + "</real>")
	case string:
		b.WriteString(ind + "<string>" + plistEscape(t) + "</string>")
	case []any:
		if len(t) == 0 {
			b.WriteString(ind + "<array/>")
			return
		}
		b.WriteString(ind + "<array>\n")
		for i, e := range t {
			writePlistValue(b, e, depth+1)
			if i < len(t)-1 {
				b.WriteString("\n")
			}
		}
		b.WriteString("\n" + ind + "</array>")
	case []string:
		as := make([]any, len(t))
		for i, s := range t {
			as[i] = s
		}
		writePlistValue(b, as, depth)
	case *value.Map:
		if t.Len() == 0 {
			b.WriteString(ind + "<dict/>")
			return
		}
		b.WriteString(ind + "<dict>\n")
		keys := t.StringKeys()
		sort.Strings(keys)
		for i, k := range keys {
			ev, _ := t.Get(k)
			b.WriteString(ind + "\t<key>" + plistEscape(k) + "</key>\n")
			writePlistValue(b, ev, depth+1)
			if i < len(keys)-1 {
				b.WriteString("\n")
			}
		}
		b.WriteString("\n" + ind + "</dict>")
	default:
		b.WriteString(ind + "<string>" + plistEscape(fmt.Sprintf("%v", t)) + "</string>")
	}
}

func plistEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
