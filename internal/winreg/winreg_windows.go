//go:build windows

package winreg

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ErrNotExist is returned for a key or a value that is not there.
//
// Its own error rather than the platform's, because "not there" is the
// answer to `value_exists` and a failure to `read_value`, and a caller
// has to be able to tell it from a permission problem without reading
// the message.
var ErrNotExist = errors.New("no such registry key or value")

// hives maps the names a tree writes onto the roots.
//
// Both the long and the short spelling, because Salt's own module
// accepts both and an estate's states are written in whichever the
// author had to hand.
var hives = map[string]registry.Key{
	"HKEY_LOCAL_MACHINE":  registry.LOCAL_MACHINE,
	"HKLM":                registry.LOCAL_MACHINE,
	"HKEY_CURRENT_USER":   registry.CURRENT_USER,
	"HKCU":                registry.CURRENT_USER,
	"HKEY_CLASSES_ROOT":   registry.CLASSES_ROOT,
	"HKCR":                registry.CLASSES_ROOT,
	"HKEY_USERS":          registry.USERS,
	"HKU":                 registry.USERS,
	"HKEY_CURRENT_CONFIG": registry.CURRENT_CONFIG,
	"HKCC":                registry.CURRENT_CONFIG,
}

// Hives are the names this build understands, for a message that has to
// list them.
func Hives() []string {
	out := make([]string, 0, len(hives))
	for name := range hives {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// hiveKey resolves a hive name.
func hiveKey(name string) (registry.Key, error) {
	if k, ok := hives[strings.ToUpper(strings.TrimSpace(name))]; ok {
		return k, nil
	}
	return 0, fmt.Errorf("%q is not a registry hive; this build understands %s",
		name, strings.Join(Hives(), ", "))
}

// View is which of the two registries a 64-bit Windows keeps.
//
// A 32-bit program on a 64-bit Windows that writes to
// HKLM\SOFTWARE\Vendor is redirected to HKLM\SOFTWARE\WOW6432Node\Vendor
// without being told. halite is a 64-bit program, so by default it
// writes where a 64-bit program writes — which is not where a 32-bit
// application will look for its settings. A tree managing one has to be
// able to say so, and this is how.
type View int

const (
	// Native is the view this process would get on its own.
	Native View = iota
	// Bits32 is the WOW6432Node view a 32-bit program sees.
	Bits32
	// Bits64 is the 64-bit view, stated explicitly.
	Bits64
)

// ParseView reads a view from what a state writes.
func ParseView(s string) (View, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "native", "default":
		return Native, nil
	case "32", "32bit", "wow64", "wow6432node":
		return Bits32, nil
	case "64", "64bit":
		return Bits64, nil
	}
	return Native, fmt.Errorf("%q is not a registry view; this build understands "+
		"native, 32 and 64", s)
}

// access adds the view's flag to a set of rights.
func (v View) access(rights uint32) uint32 {
	switch v {
	case Bits32:
		return rights | registry.WOW64_32KEY
	case Bits64:
		return rights | registry.WOW64_64KEY
	}
	return rights
}

func (v View) String() string {
	switch v {
	case Bits32:
		return "32"
	case Bits64:
		return "64"
	}
	return "native"
}

// Value is one registry value, read back.
type Value struct {
	// Name is the value's name. Empty is the key's default value, which
	// is a real value with an empty name and not the absence of one.
	Name string
	// Type is the registry type: sz, expand_sz, multi_sz, dword, qword,
	// or binary.
	Type string
	// Data is the value, in the Go type that matches: a string for sz
	// and expand_sz, a list of strings for multi_sz, an int64 for dword
	// and qword, and a lowercase hex string for binary.
	Data any
}

// typeNames maps the registry's own codes onto the names a state writes.
var typeNames = map[uint32]string{
	registry.SZ:        "sz",
	registry.EXPAND_SZ: "expand_sz",
	registry.MULTI_SZ:  "multi_sz",
	registry.DWORD:     "dword",
	registry.QWORD:     "qword",
	registry.BINARY:    "binary",
	registry.NONE:      "none",
}

// TypeNames are the value types this build can write.
func TypeNames() []string {
	return []string{"sz", "expand_sz", "multi_sz", "dword", "qword", "binary"}
}

func typeName(code uint32) string {
	if name, ok := typeNames[code]; ok {
		return name
	}
	return fmt.Sprintf("type(%d)", code)
}

// openKey opens a key for the rights and view asked for.
func openKey(hive, path string, view View, rights uint32) (registry.Key, error) {
	root, err := hiveKey(hive)
	if err != nil {
		return 0, err
	}
	k, err := registry.OpenKey(root, path, view.access(rights))
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return 0, fmt.Errorf("%s\\%s: %w", strings.ToUpper(hive), path, ErrNotExist)
		}
		return 0, fmt.Errorf("opening %s\\%s: %w%s", strings.ToUpper(hive), path, err, adminHint(err))
	}
	return k, nil
}

// adminHint explains the failure an operator will meet most.
func adminHint(err error) string {
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return " (this needs administrator rights, and this process does not have them)"
	}
	return ""
}

// KeyExists reports whether a key is there.
func KeyExists(hive, path string, view View) (bool, error) {
	k, err := openKey(hive, path, view, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	k.Close()
	return true, nil
}

// ReadValue reads one value.
//
// An empty name reads the key's default value, which is what a tree
// means by `vname: ""` and what the registry editor shows as
// "(Default)".
func ReadValue(hive, path, name string, view View) (Value, error) {
	k, err := openKey(hive, path, view, registry.QUERY_VALUE)
	if err != nil {
		return Value{}, err
	}
	defer k.Close()
	return readFrom(k, name, hive, path)
}

func readFrom(k registry.Key, name, hive, path string) (Value, error) {
	_, valtype, err := k.GetValue(name, nil)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return Value{}, fmt.Errorf("%s\\%s has no value named %q: %w",
				strings.ToUpper(hive), path, name, ErrNotExist)
		}
		return Value{}, fmt.Errorf("reading %s\\%s\\%s: %w", strings.ToUpper(hive), path, name, err)
	}

	out := Value{Name: name, Type: typeName(valtype)}
	switch valtype {
	case registry.SZ, registry.EXPAND_SZ:
		// The unexpanded form. A state comparing against `%SystemRoot%`
		// has to see `%SystemRoot%`, not what it expands to on the
		// machine that happened to read it — otherwise the comparison
		// never matches and the state never converges.
		s, _, err := k.GetStringValue(name)
		if err != nil {
			return Value{}, err
		}
		out.Data = s
	case registry.MULTI_SZ:
		list, _, err := k.GetStringsValue(name)
		if err != nil {
			return Value{}, err
		}
		items := make([]any, 0, len(list))
		for _, s := range list {
			items = append(items, s)
		}
		out.Data = items
	case registry.DWORD, registry.QWORD:
		n, _, err := k.GetIntegerValue(name)
		if err != nil {
			return Value{}, err
		}
		out.Data = int64(n)
	default:
		// Binary, and anything this build has no name for. Hex rather
		// than a byte list, because it is what regedit shows and what a
		// state file can hold without becoming unreadable.
		raw, _, err := k.GetBinaryValue(name)
		if err != nil {
			// A type with no binary reading — REG_NONE with no data —
			// is reported as present and empty rather than as an error.
			out.Data = ""
			return out, nil
		}
		out.Data = hex.EncodeToString(raw)
	}
	return out, nil
}

// ValueExists reports whether a value is there.
func ValueExists(hive, path, name string, view View) (bool, error) {
	_, err := ReadValue(hive, path, name, view)
	if err != nil {
		if errors.Is(err, ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ListKeys names the subkeys of a key, sorted.
func ListKeys(hive, path string, view View) ([]string, error) {
	k, err := openKey(hive, path, view, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil, fmt.Errorf("listing the keys under %s\\%s: %w", strings.ToUpper(hive), path, err)
	}
	sort.Strings(names)
	return names, nil
}

// ListValues reads every value in a key, sorted by name.
func ListValues(hive, path string, view View) ([]Value, error) {
	k, err := openKey(hive, path, view, registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	names, err := k.ReadValueNames(-1)
	if err != nil {
		return nil, fmt.Errorf("listing the values in %s\\%s: %w", strings.ToUpper(hive), path, err)
	}
	sort.Strings(names)
	out := make([]Value, 0, len(names))
	for _, name := range names {
		v, err := readFrom(k, name, hive, path)
		if err != nil {
			// A value that cannot be read is reported as itself rather
			// than failing the listing: one unreadable value in a key of
			// forty should not hide the other thirty-nine.
			out = append(out, Value{Name: name, Type: "unreadable", Data: err.Error()})
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

// CreateKey creates a key and every key above it, and reports whether it
// had to.
func CreateKey(hive, path string, view View) (created bool, err error) {
	root, err := hiveKey(hive)
	if err != nil {
		return false, err
	}
	k, existed, err := registry.CreateKey(root, path, view.access(registry.ALL_ACCESS))
	if err != nil {
		return false, fmt.Errorf("creating %s\\%s: %w%s",
			strings.ToUpper(hive), path, err, adminHint(err))
	}
	k.Close()
	return !existed, nil
}

// SetValue writes a value, creating the key if it is not there.
//
// The type is stated rather than guessed. A registry value's type is
// part of what it is: a program reading a setting as a DWORD does not
// find one written as a string, and a state that guessed from the YAML
// would write `1` as a string to a key that wants a number and leave the
// operator wondering why nothing changed.
func SetValue(hive, path, name, valueType string, data any, view View) error {
	root, err := hiveKey(hive)
	if err != nil {
		return err
	}
	k, _, err := registry.CreateKey(root, path, view.access(registry.SET_VALUE|registry.QUERY_VALUE))
	if err != nil {
		return fmt.Errorf("opening %s\\%s to write: %w%s",
			strings.ToUpper(hive), path, err, adminHint(err))
	}
	defer k.Close()

	switch strings.ToLower(strings.TrimSpace(valueType)) {
	case "sz", "":
		s, err := asString(data)
		if err != nil {
			return err
		}
		err = k.SetStringValue(name, s)
		return wrapWrite(err, hive, path, name)
	case "expand_sz":
		s, err := asString(data)
		if err != nil {
			return err
		}
		err = k.SetExpandStringValue(name, s)
		return wrapWrite(err, hive, path, name)
	case "multi_sz":
		list, err := asStrings(data)
		if err != nil {
			return err
		}
		err = k.SetStringsValue(name, list)
		return wrapWrite(err, hive, path, name)
	case "dword":
		n, err := asUint(data, 32)
		if err != nil {
			return err
		}
		err = k.SetDWordValue(name, uint32(n))
		return wrapWrite(err, hive, path, name)
	case "qword":
		n, err := asUint(data, 64)
		if err != nil {
			return err
		}
		err = k.SetQWordValue(name, n)
		return wrapWrite(err, hive, path, name)
	case "binary":
		raw, err := asBinary(data)
		if err != nil {
			return err
		}
		err = k.SetBinaryValue(name, raw)
		return wrapWrite(err, hive, path, name)
	}
	return fmt.Errorf("%q is not a registry value type; this build writes %s",
		valueType, strings.Join(TypeNames(), ", "))
}

func wrapWrite(err error, hive, path, name string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("writing %s\\%s\\%s: %w%s",
		strings.ToUpper(hive), path, name, err, adminHint(err))
}

// DeleteValue removes a value.
func DeleteValue(hive, path, name string, view View) error {
	k, err := openKey(hive, path, view, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(name); err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("%s\\%s has no value named %q: %w",
				strings.ToUpper(hive), path, name, ErrNotExist)
		}
		return fmt.Errorf("deleting %s\\%s\\%s: %w%s",
			strings.ToUpper(hive), path, name, err, adminHint(err))
	}
	return nil
}

// DeleteKey removes a key that has no subkeys.
//
// Not recursive. RegDeleteKey refuses a key with children, and that
// refusal is a guard worth keeping: a state that names the wrong key and
// takes a subtree with it is not recoverable from the state file.
// DeleteKeyRecursive is the explicit form.
func DeleteKey(hive, path string, view View) error {
	root, err := hiveKey(hive)
	if err != nil {
		return err
	}
	// The view flag has to be on the *open*, and DeleteKey takes a path
	// under an already-open key, so the parent is opened in the right
	// view and the leaf deleted from it.
	parent, leaf := splitKey(path)
	k, err := registry.OpenKey(root, parent, view.access(registry.ALL_ACCESS))
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("%s\\%s: %w", strings.ToUpper(hive), path, ErrNotExist)
		}
		return fmt.Errorf("opening %s\\%s: %w%s", strings.ToUpper(hive), parent, err, adminHint(err))
	}
	defer k.Close()
	if err := registry.DeleteKey(k, leaf); err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("%s\\%s: %w", strings.ToUpper(hive), path, ErrNotExist)
		}
		return fmt.Errorf("deleting %s\\%s: %w%s",
			strings.ToUpper(hive), path, err, adminHint(err))
	}
	return nil
}

// DeleteKeyRecursive removes a key and everything under it.
func DeleteKeyRecursive(hive, path string, view View) error {
	children, err := ListKeys(hive, path, view)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := DeleteKeyRecursive(hive, path+`\`+child, view); err != nil {
			return err
		}
	}
	return DeleteKey(hive, path, view)
}

// splitKey separates a key path into its parent and its last segment.
func splitKey(path string) (parent, leaf string) {
	trimmed := strings.Trim(path, `\`)
	if i := strings.LastIndex(trimmed, `\`); i >= 0 {
		return trimmed[:i], trimmed[i+1:]
	}
	return "", trimmed
}

// ---- argument coercion ----
//
// A value arrives from YAML, where `1` is an integer, `"1"` is a string
// and `yes` is a boolean, and the registry type says which of those the
// caller meant. These convert where the meaning is unambiguous and
// refuse where it is not, rather than writing something plausible.

func asString(data any) (string, error) {
	switch v := data.(type) {
	case string:
		return v, nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(v), nil
	case nil:
		return "", nil
	}
	return "", fmt.Errorf("a string value cannot be written from %T", data)
}

func asStrings(data any) ([]string, error) {
	switch v := data.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, err := asString(item)
			if err != nil {
				return nil, fmt.Errorf("a multi_sz item: %w", err)
			}
			out = append(out, s)
		}
		return out, nil
	case []string:
		return v, nil
	case string:
		// One string is a list of one. A tree that writes a single
		// value for a multi_sz means that, and refusing it would be
		// pedantry.
		return []string{v}, nil
	case nil:
		return nil, nil
	}
	return nil, fmt.Errorf("a multi_sz value needs a list of strings, not %T", data)
}

func asUint(data any, bits int) (uint64, error) {
	var n uint64
	switch v := data.(type) {
	case int64:
		if v < 0 {
			// Two's complement, because that is what the registry
			// stores and what a state writing -1 for 0xFFFFFFFF means.
			n = uint64(v) & mask(bits)
		} else {
			n = uint64(v)
		}
	case float64:
		if v != float64(int64(v)) {
			return 0, fmt.Errorf("a %d-bit registry number cannot hold %v", bits, v)
		}
		n = uint64(int64(v)) & mask(bits)
	case bool:
		if v {
			n = 1
		}
	case string:
		parsed, err := parseNumber(v)
		if err != nil {
			return 0, err
		}
		n = parsed & mask(bits)
	case nil:
		n = 0
	default:
		return 0, fmt.Errorf("a numeric registry value cannot be written from %T", data)
	}
	if bits == 32 && n > 0xFFFFFFFF {
		return 0, fmt.Errorf("%d does not fit in a dword; use qword", n)
	}
	return n, nil
}

func mask(bits int) uint64 {
	if bits >= 64 {
		return ^uint64(0)
	}
	return (uint64(1) << uint(bits)) - 1
}

// parseNumber reads a number a state wrote as text, in decimal or hex.
func parseNumber(s string) (uint64, error) {
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(strings.ToLower(trimmed), "0x") {
		n, err := strconv.ParseUint(trimmed[2:], 16, 64)
		if err != nil {
			return 0, fmt.Errorf("%q is not a hexadecimal number", s)
		}
		return n, nil
	}
	if n, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return uint64(n), nil
	}
	return 0, fmt.Errorf("%q is not a number", s)
}

// asBinary reads binary data written as hex.
//
// Hex rather than base64, because it is what regedit shows and what
// every other tool on the platform prints, so a value copied out of one
// can be pasted into a state.
func asBinary(data any) ([]byte, error) {
	switch v := data.(type) {
	case string:
		cleaned := strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "", ",", "").Replace(v)
		raw, err := hex.DecodeString(cleaned)
		if err != nil {
			return nil, fmt.Errorf("a binary value is written as hex, and %q is not: %w", v, err)
		}
		return raw, nil
	case []byte:
		return v, nil
	case nil:
		return nil, nil
	}
	return nil, fmt.Errorf("a binary value needs hex, not %T", data)
}

// SameData reports whether a value read back matches what a state
// declared, with the declaration coerced the way a write would coerce
// it.
//
// Comparing the coerced forms rather than the raw ones is what makes a
// state converge: `1` from YAML is an int64 and a dword read back is an
// int64, but `"0x1"` is a string that must compare equal to the same
// dword, and a multi_sz declared as one string must equal a list of one.
func SameData(valueType string, declared any, current Value) bool {
	switch strings.ToLower(strings.TrimSpace(valueType)) {
	case "sz", "expand_sz", "":
		want, err := asString(declared)
		if err != nil {
			return false
		}
		got, _ := current.Data.(string)
		return want == got
	case "multi_sz":
		want, err := asStrings(declared)
		if err != nil {
			return false
		}
		got, ok := current.Data.([]any)
		if !ok || len(got) != len(want) {
			return false
		}
		for i := range want {
			s, _ := got[i].(string)
			if s != want[i] {
				return false
			}
		}
		return true
	case "dword", "qword":
		bits := 32
		if strings.EqualFold(valueType, "qword") {
			bits = 64
		}
		want, err := asUint(declared, bits)
		if err != nil {
			return false
		}
		got, ok := current.Data.(int64)
		return ok && uint64(got) == want
	case "binary":
		want, err := asBinary(declared)
		if err != nil {
			return false
		}
		got, _ := current.Data.(string)
		return strings.EqualFold(hex.EncodeToString(want), got)
	}
	return false
}
