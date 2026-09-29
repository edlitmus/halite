package fileperm

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// `Advice` told an operator to run a command that would have locked them out.
//
// # What was wrong, and why nothing caught it
//
// It said `chmod 600` for every path. On a **directory** the execute bit is what
// permits traversal, so 600 makes it unreachable by its own owner rather than
// private: an operator told that, about a key directory, would follow it and end
// up with a directory the hub itself cannot read. On Windows the same advice
// omitted the inheritance flags that `RestrictDir` sets, so it would have
// produced a private directory holding keys that are not.
//
// Nothing caught it because **advice is a string nothing compares against
// anything**. Every other claim in this package is checked — `Apply` against
// `Others`, `Others` against a real ACL — and this one is printed. It was seen
// only because `internal/pki`'s new permission test printed it beside a
// directory and the word `600` was visible next to a path ending in `/pki`.
//
// # What these tests hold
//
// Three kinds of assertion, because the text, the selection and the effect are
// different claims:
//
//   - **both platforms' wording, on either platform.** `unixAdvice` and
//     `windowsAdvice` take "is this a directory?" as a parameter and live in a
//     file that compiles everywhere, so the icacls flags are checked on this
//     machine rather than only on a Windows runner. That restructuring is the
//     point of it: a branch no test on the running machine can reach is a
//     branch nobody has read since it was written.
//   - that `Advice` **picks** the form matching what is on disk, which is the
//     one thing that does need a real file and a real directory.
//   - and on unix, that **following the advice leaves the directory usable by
//     its owner**, which is the property that was broken. That one applies the
//     mode the advice names rather than trusting its text, and it is skipped as
//     root, which ignores the mode it just set.
//
// DIVERGENCE 5.163.

// The wording, both platforms, on whichever this is.
func TestAdviceWordingForBothPlatforms(t *testing.T) {
	const path = "/var/db/halite/pki"

	// unix. 600 on a directory removes the execute bit, which is what permits
	// traversal, so the two must differ and the directory must be 700.
	if got := unixAdvice(path, true); !strings.Contains(got, "chmod 700 ") {
		t.Errorf("unixAdvice for a directory = %q; 600 on a directory removes the "+
			"execute bit, which is what permits traversal", got)
	}
	if got := unixAdvice(path, false); !strings.Contains(got, "chmod 600 ") {
		t.Errorf("unixAdvice for a file = %q, want chmod 600", got)
	}

	// Windows. (OI)(CI) is icacls's spelling of
	// SUB_CONTAINERS_AND_OBJECTS_INHERIT, which is exactly the flag
	// winsec.RestrictDir sets and winsec.Restrict does not -- so the advice
	// and Apply say the same thing, which is the whole reason this is
	// checked rather than read.
	forDir := windowsAdvice(path, true)
	if !strings.Contains(forDir, "(OI)(CI)") {
		t.Errorf("windowsAdvice for a directory = %q; without the inheritance flags "+
			"the restriction stops at the directory and a key written into it "+
			"afterwards is not covered", forDir)
	}
	forFile := windowsAdvice(path, false)
	if strings.Contains(forFile, "(OI)(CI)") {
		t.Errorf("windowsAdvice for a file = %q; a file has nothing to inherit to", forFile)
	}
	// Both are one command naming the path, and `%USERNAME%` survives being
	// formatted -- it is a literal the operator's shell expands, and a stray
	// Sprintf verb would have eaten it.
	for _, got := range []string{forDir, forFile} {
		if !strings.HasPrefix(got, `icacls "`+path+`"`) {
			t.Errorf("the advice does not start by naming the path: %q", got)
		}
		if !strings.Contains(got, "%USERNAME%") {
			t.Errorf("the advice lost %%USERNAME%%, which the operator's shell expands: %q", got)
		}
		if strings.Contains(got, "%!") {
			t.Errorf("the advice carries a formatting error: %q", got)
		}
	}
}

// Advice picks the form matching what is on disk, which is the half that needs
// a real file and a real directory rather than a parameter.
func TestAdviceForADirectoryIsNotTheAdviceForAFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secret")
	if err := os.WriteFile(file, []byte("s\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	forDir, forFile := Advice(dir), Advice(file)
	if forDir == forFile {
		t.Fatalf("a directory and a file are advised identically: %q", forDir)
	}
	// Both name the path, since advice that does not is advice about nothing.
	if !strings.Contains(forDir, dir) {
		t.Errorf("the directory advice does not name it: %q", forDir)
	}
	if !strings.Contains(forFile, file) {
		t.Errorf("the file advice does not name it: %q", forFile)
	}

	switch runtime.GOOS {
	case "windows":
		// (OI)(CI) is icacls's spelling of
		// SUB_CONTAINERS_AND_OBJECTS_INHERIT, which is exactly the flag
		// RestrictDir sets and Restrict does not.
		if !strings.Contains(forDir, "(OI)(CI)") {
			t.Errorf("the directory advice has no inheritance flags, so a file written "+
				"into it afterwards would not be covered: %q", forDir)
		}
		if strings.Contains(forFile, "(OI)(CI)") {
			t.Errorf("the file advice carries inheritance flags, which a file has "+
				"nothing to inherit to: %q", forFile)
		}
	default:
		if !strings.Contains(forDir, "chmod 700") {
			t.Errorf("the directory advice is %q; 600 on a directory removes the execute "+
				"bit, which is what permits traversal", forDir)
		}
		if !strings.Contains(forFile, "chmod 600") {
			t.Errorf("the file advice is %q, want chmod 600", forFile)
		}
	}
}

// **Following the advice leaves the directory usable by its owner.**
//
// The property that was broken, checked rather than read. The mode named in the
// advice is applied and then the directory is traversed: under `chmod 600` the
// open fails with a permission error, which is the failure an operator would
// have met after doing what they were told.
//
// Skipped as root, which holds CAP_DAC_OVERRIDE and traverses a directory
// whatever its mode — so the assertion cannot express itself there, and
// `permtest`'s own comment records what pretending otherwise cost. Windows is
// skipped because following the advice there means running `icacls`, and a test
// that shells out to it would be a test of icacls's argument parsing.
func TestFollowingTheDirectoryAdviceLeavesItTraversable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("following this advice means running icacls; the wording is checked above")
	}
	if os.Geteuid() == 0 {
		t.Skip("this test needs an unprivileged account: root traverses a directory " +
			"whatever its mode, so the advice cannot be seen to be wrong here")
	}
	dir := filepath.Join(t.TempDir(), "pki")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(dir, "ca.key")
	if err := os.WriteFile(inside, []byte("key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The mode the advice names, taken out of the advice itself, so that a
	// change to the wording is a change to what this applies.
	mode := advisedMode(t, Advice(dir))
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(inside)
	if err != nil {
		t.Fatalf("after following `%s` the owner cannot read a file in its own key "+
			"directory: %v", Advice(dir), err)
	}
	_ = f.Close()

	// And it is still private, so the advice did not trade one fault for
	// another.
	others, err := Others(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(others) > 0 {
		t.Errorf("after following the advice the directory is still reachable by %v", others)
	}
}

// advisedMode reads the octal out of `chmod NNN path`.
//
// Parsed from the advice rather than written down here, so that the test
// applies whatever the advice currently says: a wording change that went back
// to 600 would be applied by this and caught by the assertion, where a
// hardcoded 0o700 would quietly keep passing.
func advisedMode(t *testing.T, advice string) os.FileMode {
	t.Helper()
	fields := strings.Fields(advice)
	if len(fields) < 2 || fields[0] != "chmod" {
		t.Fatalf("the advice is not a chmod this test can follow: %q", advice)
	}
	var mode os.FileMode
	for _, r := range fields[1] {
		if r < '0' || r > '7' {
			t.Fatalf("%q is not an octal mode: %q", fields[1], advice)
		}
		mode = mode*8 + os.FileMode(r-'0')
	}
	return mode
}
