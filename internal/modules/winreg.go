package modules

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
)

func init() {
	register("reg.present", regPresent)
	register("reg.absent", regAbsent)
}

// The registry states drive reg.exe. As with scheduled tasks, the fiddly
// part is not running the command but agreeing with it about what the
// current value *is*: reg query reports a DWORD as hex, so comparing it to
// the decimal an SLS file wrote needs care, and getting that wrong means a
// state that reports a change on every run.

// regHives maps the short hive names to their full form. reg.exe accepts
// both; normalising means the comparison and the command agree.
var regHives = map[string]string{
	"HKLM": "HKEY_LOCAL_MACHINE",
	"HKCU": "HKEY_CURRENT_USER",
	"HKCR": "HKEY_CLASSES_ROOT",
	"HKU":  "HKEY_USERS",
	"HKCC": "HKEY_CURRENT_CONFIG",
}

// regTypes are the value types halite will write.
var regTypes = map[string]bool{
	"REG_SZ":        true,
	"REG_EXPAND_SZ": true,
	"REG_MULTI_SZ":  true,
	"REG_DWORD":     true,
	"REG_QWORD":     true,
	"REG_BINARY":    true,
}

// normalizeKey validates a registry path and returns it with a full hive
// name. A path is `<hive>\<subkey>`; the hive must be one halite knows.
func normalizeKey(key string) (string, error) {
	trimmed := strings.TrimSpace(strings.ReplaceAll(key, "/", `\`))
	if trimmed == "" {
		return "", fmt.Errorf("a registry key is required")
	}
	hive, subkey, found := strings.Cut(trimmed, `\`)
	upper := strings.ToUpper(hive)
	if full, ok := regHives[upper]; ok {
		upper = full
	}
	valid := false
	for _, full := range regHives {
		if upper == full {
			valid = true
			break
		}
	}
	if !valid {
		return "", fmt.Errorf("%q is not a registry hive (HKLM, HKCU, HKCR, HKU, HKCC)", hive)
	}
	if !found || subkey == "" {
		return upper, nil
	}
	return upper + `\` + subkey, nil
}

// normalizeType checks a value type.
func normalizeType(vtype string) (string, error) {
	if vtype == "" {
		return "REG_SZ", nil
	}
	upper := strings.ToUpper(strings.TrimSpace(vtype))
	if !strings.HasPrefix(upper, "REG_") {
		upper = "REG_" + upper
	}
	if !regTypes[upper] {
		return "", fmt.Errorf("%q is not a registry value type halite writes", vtype)
	}
	return upper, nil
}

// parseRegQuery reads the type and data of one value out of `reg query`
// output. Value lines are indented and separated by runs of spaces:
//
//	HKEY_LOCAL_MACHINE\SOFTWARE\Acme
//	    Timeout    REG_DWORD    0x1e
func parseRegQuery(output, vname string) (vtype, data string, found bool) {
	want := vname
	if want == "" {
		want = "(Default)"
	}
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimRight(raw, "\r")
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			continue // a key heading, not a value
		}
		fields := splitRegLine(line)
		if len(fields) < 2 {
			continue
		}
		if !strings.EqualFold(fields[0], want) {
			continue
		}
		if len(fields) == 2 {
			return fields[1], "", true // a value with empty data
		}
		return fields[1], fields[2], true
	}
	return "", "", false
}

// splitRegLine splits a value line on runs of two or more spaces, which is
// what separates the name, type, and data. The data itself may contain
// single spaces, so splitting on whitespace would break it.
func splitRegLine(line string) []string {
	var fields []string
	var current strings.Builder
	spaces := 0
	for _, r := range strings.TrimLeft(line, " \t") {
		if r == ' ' || r == '\t' {
			spaces++
			continue
		}
		if spaces >= 2 && current.Len() > 0 {
			fields = append(fields, current.String())
			current.Reset()
		} else if spaces > 0 && current.Len() > 0 {
			current.WriteByte(' ')
		}
		spaces = 0
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		fields = append(fields, current.String())
	}
	// Cap at three: anything further belongs to the data.
	if len(fields) > 3 {
		fields = append(fields[:2], strings.Join(fields[2:], "  "))
	}
	return fields
}

// sameRegValue reports whether the registry already holds what was asked
// for. Numbers are compared numerically because reg query prints them in
// hex, and strings case-sensitively because their content matters.
func sameRegValue(vtype, want, got string) bool {
	switch vtype {
	case "REG_DWORD", "REG_QWORD":
		wantNum, wantErr := parseRegNumber(want)
		gotNum, gotErr := parseRegNumber(got)
		if wantErr != nil || gotErr != nil {
			return false
		}
		return wantNum == gotNum
	case "REG_BINARY":
		return strings.EqualFold(strings.TrimSpace(want), strings.TrimSpace(got))
	default:
		return want == got
	}
}

// parseRegNumber accepts decimal or 0x-prefixed hex.
func parseRegNumber(text string) (uint64, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, fmt.Errorf("empty")
	}
	if lower := strings.ToLower(trimmed); strings.HasPrefix(lower, "0x") {
		return strconv.ParseUint(lower[2:], 16, 64)
	}
	return strconv.ParseUint(trimmed, 10, 64)
}

// queryRegValue reads a value's current type and data.
func queryRegValue(key, vname string) (vtype, data string, found bool) {
	argv := []string{"query", key}
	if vname == "" {
		argv = append(argv, "/ve")
	} else {
		argv = append(argv, "/v", vname)
	}
	out, _, rc, err := run("reg", argv...)
	if err != nil || rc != 0 {
		return "", "", false
	}
	return parseRegQuery(out, vname)
}

// regPresent ensures a registry value exists with the given data.
//
//	HKLM\SOFTWARE\Acme:
//	  reg.present:
//	    - vname: Timeout
//	    - vdata: "30"
//	    - vtype: REG_DWORD
func regPresent(c *Ctx, id string, args map[string]any) Result {
	if runtime.GOOS != "windows" {
		return resFail("the registry is Windows-only")
	}
	key, err := normalizeKey(Str(args, "name", id))
	if err != nil {
		return resFail("%v", err)
	}
	vtype, err := normalizeType(Str(args, "vtype", ""))
	if err != nil {
		return resFail("%v", err)
	}
	vname := Str(args, "vname", "")
	vdata := Str(args, "vdata", "")
	if !has("reg") {
		return resFail("reg.exe not found")
	}

	currentType, currentData, exists := queryRegValue(key, vname)
	if exists && currentType == vtype && sameRegValue(vtype, vdata, currentData) {
		return resOK(fmt.Sprintf("%s\\%s is already set", key, valueLabel(vname)))
	}
	if c.Test {
		if exists {
			return resWould(fmt.Sprintf("%s\\%s would be updated", key, valueLabel(vname)))
		}
		return resWould(fmt.Sprintf("%s\\%s would be set", key, valueLabel(vname)))
	}

	argv := []string{"add", key, "/t", vtype, "/d", vdata, "/f"}
	if vname == "" {
		argv = append(argv, "/ve")
	} else {
		argv = append(argv, "/v", vname)
	}
	if _, errOut, rc, err := run("reg", argv...); err != nil || rc != 0 {
		return resFail("reg add %s: %s", key, cmdError(errOut, err))
	}
	changes := map[string]string{"new": vdata}
	if exists {
		changes["old"] = currentData
	}
	return resChanged(fmt.Sprintf("%s\\%s set", key, valueLabel(vname)), changes)
}

// regAbsent removes a registry value, or a whole key when no value is
// named. Removing a key removes everything under it, so it has to be
// asked for explicitly with delete_key.
func regAbsent(c *Ctx, id string, args map[string]any) Result {
	if runtime.GOOS != "windows" {
		return resFail("the registry is Windows-only")
	}
	key, err := normalizeKey(Str(args, "name", id))
	if err != nil {
		return resFail("%v", err)
	}
	vname := Str(args, "vname", "")
	deleteKey := Bool(args, "delete_key", false)
	if vname == "" && !deleteKey {
		return resFail("reg.absent needs a vname, or delete_key: true to remove the whole key")
	}
	if !has("reg") {
		return resFail("reg.exe not found")
	}

	if deleteKey {
		if _, _, rc, _ := run("reg", "query", key); rc != 0 {
			return resOK(fmt.Sprintf("%s is already absent", key))
		}
		if c.Test {
			return resWould(fmt.Sprintf("%s and everything under it would be removed", key))
		}
		if _, errOut, rc, err := run("reg", "delete", key, "/f"); err != nil || rc != 0 {
			return resFail("reg delete %s: %s", key, cmdError(errOut, err))
		}
		return resChanged(fmt.Sprintf("%s removed", key), map[string]string{"removed": key})
	}

	if _, _, exists := queryRegValue(key, vname); !exists {
		return resOK(fmt.Sprintf("%s\\%s is already absent", key, vname))
	}
	if c.Test {
		return resWould(fmt.Sprintf("%s\\%s would be removed", key, vname))
	}
	if _, errOut, rc, err := run("reg", "delete", key, "/v", vname, "/f"); err != nil || rc != 0 {
		return resFail("reg delete %s: %s", key, cmdError(errOut, err))
	}
	return resChanged(fmt.Sprintf("%s\\%s removed", key, vname),
		map[string]string{"removed": vname})
}

func valueLabel(vname string) string {
	if vname == "" {
		return "(Default)"
	}
	return vname
}
