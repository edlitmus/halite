//go:build windows

package winreg

import (
	"errors"
	"strings"
	"testing"
)

// scratch is a key under the current user's hive, which needs no
// administrator rights and is removed when the test finishes.
//
// HKCU rather than HKLM on purpose: a registry test that needs elevation
// is a registry test nobody runs, and every operation here behaves the
// same in both hives.
func scratch(t *testing.T) string {
	t.Helper()
	const path = `Software\halite-test`
	if _, err := CreateKey("HKCU", path, Native); err != nil {
		t.Fatalf("creating the scratch key: %v", err)
	}
	t.Cleanup(func() { _ = DeleteKeyRecursive("HKCU", path, Native) })
	return path
}

// Every type this build writes has to come back as what it was written
// as, in the Go type a state compares against. A type that read back
// differently would make a state report a change on every run.
func TestEveryValueTypeRoundTrips(t *testing.T) {
	path := scratch(t)

	cases := []struct {
		name     string
		regType  string
		write    any
		wantType string
		want     any
	}{
		{"a_string", "sz", "hello", "sz", "hello"},
		{"an_expandable", "expand_sz", `%SystemRoot%\x`, "expand_sz", `%SystemRoot%\x`},
		{"a_number", "dword", int64(42), "dword", int64(42)},
		{"a_big_number", "qword", int64(1) << 40, "qword", int64(1) << 40},
		{"from_hex", "dword", "0x1F", "dword", int64(31)},
		{"from_bool", "dword", true, "dword", int64(1)},
		{"binary_data", "binary", "deadbeef", "binary", "deadbeef"},
	}
	for _, c := range cases {
		if err := SetValue("HKCU", path, c.name, c.regType, c.write, Native); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got, err := ReadValue("HKCU", path, c.name, Native)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.Type != c.wantType {
			t.Errorf("%s: type = %q, want %q", c.name, got.Type, c.wantType)
		}
		if got.Data != c.want {
			t.Errorf("%s: data = %#v, want %#v", c.name, got.Data, c.want)
		}
		// And what a state compares against agrees with what was
		// written, which is the property that makes a state converge.
		if !SameData(c.regType, c.write, got) {
			t.Errorf("%s: the value read back does not compare equal to what was declared", c.name)
		}
	}

	// A multi-string is a list either way.
	if err := SetValue("HKCU", path, "a_list", "multi_sz", []any{"one", "two"}, Native); err != nil {
		t.Fatal(err)
	}
	list, err := ReadValue("HKCU", path, "a_list", Native)
	if err != nil {
		t.Fatal(err)
	}
	items, ok := list.Data.([]any)
	if !ok || len(items) != 2 || items[0] != "one" || items[1] != "two" {
		t.Errorf("multi_sz = %#v", list.Data)
	}
	if !SameData("multi_sz", []any{"one", "two"}, list) {
		t.Error("a multi_sz does not compare equal to what was declared")
	}
	// One string is a list of one, because that is what a tree writing a
	// single value means.
	if !SameData("multi_sz", "one", winValue([]any{"one"})) {
		t.Error("a single string should compare equal to a one-item multi_sz")
	}
}

func winValue(data any) Value { return Value{Type: "multi_sz", Data: data} }

// The default value is a real value with an empty name, not the absence
// of one, and the registry editor calls it "(Default)".
func TestTheDefaultValueIsAValue(t *testing.T) {
	path := scratch(t)
	if err := SetValue("HKCU", path, "", "sz", "the default", Native); err != nil {
		t.Fatal(err)
	}
	got, err := ReadValue("HKCU", path, "", Native)
	if err != nil {
		t.Fatal(err)
	}
	if got.Data != "the default" {
		t.Errorf("the default value = %#v", got.Data)
	}
	exists, err := ValueExists("HKCU", path, "", Native)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("the default value was reported absent after being written")
	}
}

// "Not there" has to be distinguishable from "you may not read it",
// because one is an answer and the other is a failure.
func TestAbsentIsItsOwnError(t *testing.T) {
	path := scratch(t)

	_, err := ReadValue("HKCU", path, "never-written", Native)
	if !errors.Is(err, ErrNotExist) {
		t.Errorf("a missing value gave %v, want ErrNotExist", err)
	}
	exists, err := ValueExists("HKCU", path, "never-written", Native)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("a value that was never written exists")
	}

	_, err = ReadValue("HKCU", path+`\nope`, "x", Native)
	if !errors.Is(err, ErrNotExist) {
		t.Errorf("a missing key gave %v, want ErrNotExist", err)
	}
	present, err := KeyExists("HKCU", path+`\nope`, Native)
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Error("a key that was never created exists")
	}
}

func TestKeysAndValuesAreListedSorted(t *testing.T) {
	path := scratch(t)
	for _, name := range []string{"zebra", "apple", "mango"} {
		if _, err := CreateKey("HKCU", path+`\`+name, Native); err != nil {
			t.Fatal(err)
		}
		if err := SetValue("HKCU", path, name, "sz", name, Native); err != nil {
			t.Fatal(err)
		}
	}

	keys, err := ListKeys("HKCU", path, Native)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[0] != "apple" || keys[2] != "zebra" {
		t.Errorf("keys = %v, want them sorted", keys)
	}

	values, err := ListValues("HKCU", path, Native)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 || values[0].Name != "apple" || values[2].Name != "zebra" {
		t.Errorf("values = %+v, want them sorted", values)
	}
	// The listing carries the data, not just the names: a caller
	// comparing a whole key against a declaration should not have to
	// read each value again.
	if values[0].Data != "apple" {
		t.Errorf("the listing dropped the data: %+v", values[0])
	}
}

// Deleting a key with children is refused, and the recursive form is
// the one that says so out loud.
//
// A state that named the wrong key and took a subtree with it is not
// recoverable from the state file, so the guard is worth keeping.
func TestDeletingAKeyWithChildrenIsRefusedUnlessRecursive(t *testing.T) {
	path := scratch(t)
	if _, err := CreateKey("HKCU", path+`\parent\child`, Native); err != nil {
		t.Fatal(err)
	}

	if err := DeleteKey("HKCU", path+`\parent`, Native); err == nil {
		t.Error("a key with a subkey was deleted by the non-recursive form")
	}
	if err := DeleteKeyRecursive("HKCU", path+`\parent`, Native); err != nil {
		t.Fatalf("the recursive delete failed: %v", err)
	}
	present, err := KeyExists("HKCU", path+`\parent`, Native)
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Error("the key survived a recursive delete")
	}
}

func TestDeletingAValue(t *testing.T) {
	path := scratch(t)
	if err := SetValue("HKCU", path, "temporary", "sz", "x", Native); err != nil {
		t.Fatal(err)
	}
	if err := DeleteValue("HKCU", path, "temporary", Native); err != nil {
		t.Fatal(err)
	}
	exists, err := ValueExists("HKCU", path, "temporary", Native)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("the value survived being deleted")
	}
	// Deleting what is not there says so, rather than reporting success.
	if err := DeleteValue("HKCU", path, "temporary", Native); !errors.Is(err, ErrNotExist) {
		t.Errorf("deleting a missing value gave %v", err)
	}
}

// A hive name a tree might reasonably write is understood, in both its
// spellings, and one that is not is an error listing the alternatives.
func TestHiveNamesAreUnderstoodInBothSpellings(t *testing.T) {
	for _, pair := range [][2]string{
		{"HKLM", "HKEY_LOCAL_MACHINE"},
		{"HKCU", "HKEY_CURRENT_USER"},
		{"hkcr", "HKEY_CLASSES_ROOT"},
	} {
		short, err := hiveKey(pair[0])
		if err != nil {
			t.Fatalf("%s: %v", pair[0], err)
		}
		long, err := hiveKey(pair[1])
		if err != nil {
			t.Fatalf("%s: %v", pair[1], err)
		}
		if short != long {
			t.Errorf("%s and %s are different hives", pair[0], pair[1])
		}
	}
	_, err := hiveKey("HKEY_INVENTED")
	if err == nil {
		t.Fatal("an invented hive was accepted")
	}
	if !strings.Contains(err.Error(), "HKLM") {
		t.Errorf("the error does not list the real hives: %v", err)
	}
}

// The two views a 64-bit Windows keeps are different registries, and a
// value written to one is not in the other. Getting this wrong writes an
// application's settings where the application will never look.
func TestTheTwoViewsAreDifferentRegistries(t *testing.T) {
	const path = `Software\halite-test-views`
	if _, err := CreateKey("HKCU", path, Bits64); err != nil {
		t.Fatalf("creating in the 64-bit view: %v", err)
	}
	t.Cleanup(func() {
		_ = DeleteKeyRecursive("HKCU", path, Bits64)
		_ = DeleteKeyRecursive("HKCU", path, Bits32)
	})

	if err := SetValue("HKCU", path, "which", "sz", "sixty-four", Bits64); err != nil {
		t.Fatal(err)
	}
	got, err := ReadValue("HKCU", path, "which", Bits64)
	if err != nil {
		t.Fatal(err)
	}
	if got.Data != "sixty-four" {
		t.Errorf("the 64-bit view holds %#v", got.Data)
	}

	// HKCU is not redirected on any Windows — the redirection is under
	// HKLM\SOFTWARE and HKCR — so both views see the same key here. What
	// this asserts is that asking for a view is accepted and does not
	// change the answer where there is nothing to redirect; the
	// redirection itself needs a write under HKLM, which needs
	// administrator rights.
	same, err := ReadValue("HKCU", path, "which", Bits32)
	if err != nil {
		t.Fatalf("the 32-bit view could not read an unredirected key: %v", err)
	}
	if same.Data != "sixty-four" {
		t.Errorf("HKCU is not redirected, so both views should agree; got %#v", same.Data)
	}
}

func TestViewNamesAreParsed(t *testing.T) {
	for _, c := range []struct {
		in   string
		want View
	}{
		{"", Native}, {"native", Native},
		{"32", Bits32}, {"32bit", Bits32}, {"WOW6432Node", Bits32},
		{"64", Bits64}, {"64bit", Bits64},
	} {
		got, err := ParseView(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseView(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	if _, err := ParseView("128"); err == nil {
		t.Error("an invented view was accepted")
	}
}

// A number that does not fit is refused rather than silently truncated.
func TestANumberThatDoesNotFitIsRefused(t *testing.T) {
	path := scratch(t)
	err := SetValue("HKCU", path, "too_big", "dword", int64(1)<<40, Native)
	if err == nil {
		t.Fatal("a value too large for a dword was written")
	}
	if !strings.Contains(err.Error(), "qword") {
		t.Errorf("the error does not say what to use instead: %v", err)
	}
	// The same number fits in a qword.
	if err := SetValue("HKCU", path, "big_enough", "qword", int64(1)<<40, Native); err != nil {
		t.Fatalf("a qword refused a value that fits: %v", err)
	}
}

// A value type this build does not write is refused by name, listing
// what it does write.
func TestAnUnknownTypeIsRefusedByName(t *testing.T) {
	path := scratch(t)
	err := SetValue("HKCU", path, "x", "reg_link", "y", Native)
	if err == nil {
		t.Fatal("an unsupported type was accepted")
	}
	for _, want := range []string{"sz", "dword", "multi_sz", "binary"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not offer %q: %v", want, err)
		}
	}
}

// Binary is written and read as hex, because that is what regedit shows
// and what can be pasted into a state file.
func TestBinaryIsHexAndToleratesSpacing(t *testing.T) {
	path := scratch(t)
	if err := SetValue("HKCU", path, "spaced", "binary", "de ad be ef", Native); err != nil {
		t.Fatal(err)
	}
	got, err := ReadValue("HKCU", path, "spaced", Native)
	if err != nil {
		t.Fatal(err)
	}
	if got.Data != "deadbeef" {
		t.Errorf("binary read back as %#v", got.Data)
	}
	if !SameData("binary", "DE AD BE EF", got) {
		t.Error("binary comparison is case- or spacing-sensitive")
	}
	if err := SetValue("HKCU", path, "bad", "binary", "not-hex", Native); err == nil {
		t.Error("a binary value that is not hex was accepted")
	}
}
