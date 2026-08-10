package modules

import "testing"

func TestNormalizeKeyExpandsHives(t *testing.T) {
	cases := map[string]string{
		`HKLM\SOFTWARE\Acme`:               `HKEY_LOCAL_MACHINE\SOFTWARE\Acme`,
		`hklm\SOFTWARE\Acme`:               `HKEY_LOCAL_MACHINE\SOFTWARE\Acme`,
		`HKEY_LOCAL_MACHINE\SOFTWARE\Acme`: `HKEY_LOCAL_MACHINE\SOFTWARE\Acme`,
		`HKCU\Console`:                     `HKEY_CURRENT_USER\Console`,
		`HKLM`:                             `HKEY_LOCAL_MACHINE`,
		// Forward slashes are what a unix-shaped brain types.
		`HKLM/SOFTWARE/Acme`: `HKEY_LOCAL_MACHINE\SOFTWARE\Acme`,
	}
	for input, want := range cases {
		got, err := normalizeKey(input)
		if err != nil {
			t.Errorf("%s: %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("normalizeKey(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeKeyRejectsNonHives(t *testing.T) {
	for _, key := range []string{"", "   ", `SOFTWARE\Acme`, `HKXX\Thing`, `C:\not\a\key`} {
		if got, err := normalizeKey(key); err == nil {
			t.Errorf("normalizeKey(%q) = %q, want an error", key, got)
		}
	}
}

func TestNormalizeType(t *testing.T) {
	cases := map[string]string{
		"":              "REG_SZ",
		"REG_DWORD":     "REG_DWORD",
		"reg_dword":     "REG_DWORD",
		"DWORD":         "REG_DWORD",
		"sz":            "REG_SZ",
		"REG_MULTI_SZ":  "REG_MULTI_SZ",
		"REG_EXPAND_SZ": "REG_EXPAND_SZ",
		"REG_QWORD":     "REG_QWORD",
		"REG_BINARY":    "REG_BINARY",
	}
	for input, want := range cases {
		got, err := normalizeType(input)
		if err != nil {
			t.Errorf("%q: %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("normalizeType(%q) = %q, want %q", input, got, want)
		}
	}
	for _, bad := range []string{"REG_LINK", "text", "REG_NONE"} {
		if _, err := normalizeType(bad); err == nil {
			t.Errorf("normalizeType(%q) succeeded", bad)
		}
	}
}

func TestParseRegQuery(t *testing.T) {
	output := "\r\n" +
		"HKEY_LOCAL_MACHINE\\SOFTWARE\\Acme\r\n" +
		"    Timeout    REG_DWORD    0x1e\r\n" +
		"    Banner    REG_SZ    hello there\r\n" +
		"    (Default)    REG_SZ    the default\r\n" +
		"    Empty    REG_SZ\r\n" +
		"\r\n"

	cases := []struct {
		vname, wantType, wantData string
	}{
		{"Timeout", "REG_DWORD", "0x1e"},
		// Data containing single spaces must survive the split.
		{"Banner", "REG_SZ", "hello there"},
		{"", "REG_SZ", "the default"},
		{"Empty", "REG_SZ", ""},
	}
	for _, c := range cases {
		gotType, gotData, found := parseRegQuery(output, c.vname)
		if !found {
			t.Errorf("%q not found", c.vname)
			continue
		}
		if gotType != c.wantType || gotData != c.wantData {
			t.Errorf("%q = (%q, %q), want (%q, %q)", c.vname, gotType, gotData, c.wantType, c.wantData)
		}
	}

	if _, _, found := parseRegQuery(output, "Missing"); found {
		t.Error("a value that is not there was found")
	}
	// The key heading is not a value, however much it looks like one.
	if _, _, found := parseRegQuery(output, "HKEY_LOCAL_MACHINE\\SOFTWARE\\Acme"); found {
		t.Error("the key heading was parsed as a value")
	}
}

func TestParseRegQueryOnAnError(t *testing.T) {
	output := "ERROR: The system was unable to find the specified registry key or value.\r\n"
	if _, _, found := parseRegQuery(output, "Timeout"); found {
		t.Error("found a value in an error message")
	}
}

func TestSameRegValueComparesNumbersNumerically(t *testing.T) {
	// This is the case that matters: reg query prints a DWORD in hex, and
	// an SLS file writes decimal. Comparing them as text would report a
	// change on every single run.
	if !sameRegValue("REG_DWORD", "30", "0x1e") {
		t.Error("30 and 0x1e are the same DWORD")
	}
	if !sameRegValue("REG_QWORD", "0x10", "16") {
		t.Error("0x10 and 16 are the same QWORD")
	}
	if sameRegValue("REG_DWORD", "30", "0x1f") {
		t.Error("30 and 0x1f are different")
	}
	if sameRegValue("REG_DWORD", "notanumber", "0x1e") {
		t.Error("unparseable data must not compare equal")
	}
}

func TestSameRegValueForStringsAndBinary(t *testing.T) {
	if !sameRegValue("REG_SZ", "hello", "hello") {
		t.Error("identical strings differ")
	}
	// String content is case-sensitive: a path is not the same in two cases.
	if sameRegValue("REG_SZ", "Hello", "hello") {
		t.Error("strings compared case-insensitively")
	}
	// Binary is hex, where case carries no meaning.
	if !sameRegValue("REG_BINARY", "deadBEEF", "DEADbeef") {
		t.Error("binary compared case-sensitively")
	}
}

func TestSplitRegLineKeepsDataIntact(t *testing.T) {
	fields := splitRegLine("    Path    REG_EXPAND_SZ    %SystemRoot%\\system32;C:\\Program Files\\app")
	if len(fields) != 3 {
		t.Fatalf("got %d fields: %q", len(fields), fields)
	}
	if fields[0] != "Path" || fields[1] != "REG_EXPAND_SZ" {
		t.Errorf("fields = %q", fields)
	}
	if fields[2] != `%SystemRoot%\system32;C:\Program Files\app` {
		t.Errorf("data = %q", fields[2])
	}
}

func TestRegistryStatesRefuseToRunOffWindows(t *testing.T) {
	// The whole module is compiled everywhere so its logic can be tested;
	// the states themselves must not pretend to work.
	res := regPresent(&Ctx{}, `HKLM\SOFTWARE\Acme`, map[string]any{"vname": "x", "vdata": "1"})
	if res.Ok {
		t.Error("reg.present reported success off Windows")
	}
	if res := regAbsent(&Ctx{}, `HKLM\SOFTWARE\Acme`, map[string]any{"vname": "x"}); res.Ok {
		t.Error("reg.absent reported success off Windows")
	}
}
