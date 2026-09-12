package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `sudo`, driven against the real sudo and visudo.
//
// # Most of this needs no privilege, which is the point
//
// `visudo -c -f <file>` checks a file that is not installed, so it needs
// neither root nor a sudoers rule. That is what makes `sudo.validate`
// worth having: a tree can check the sudoers it is about to write,
// before writing it, on any node, as any account.
//
// So this leg is ungated except for needing the tools. Only the two
// functions that read the *node's own* configuration are gated, because
// sudo shows its plugin settings to root alone.
func TestLiveSudoValidatesWithTheRealVisudo(t *testing.T) {
	c := liveSudoSetup(t)
	r := New()
	dir := t.TempDir()

	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte("ed ALL=(ALL) NOPASSWD: /bin/ls\n"), 0o440); err != nil {
		t.Fatal(err)
	}
	// A sudoers that is wrong in the way a template gets wrong: a line
	// that is not a rule at all.
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("this is not sudoers\n"), 0o440); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		{"a rule the real grammar accepts", good, true},
		{"a line that is not a rule", bad, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Exec.Call(c, "sudo.validate", value.MapOf("path", tc.path))
			if err != nil {
				t.Fatalf("sudo.validate: %v", err)
			}
			m := got.(*value.Map)
			valid, _ := m.GetString("valid")
			if valid != tc.want {
				comment, _ := m.GetString("comment")
				t.Errorf("valid=%v, want %v -- visudo said %q", valid, tc.want, comment)
			}
			// The reply must carry visudo's own words either way. A bare
			// boolean tells an operator a template is broken and not
			// where, which is the half that costs the time.
			if comment, _ := m.GetString("comment"); strings.TrimSpace(comment.(string)) == "" {
				t.Error("the reply carries none of visudo's output")
			}
		})
	}
}

// A file visudo cannot read is not a valid file.
//
// Worth asserting on its own: a check that returned true for a missing
// path would green-light writing a sudoers from a source that is not
// there.
func TestLiveSudoDoesNotCallAMissingFileValid(t *testing.T) {
	c := liveSudoSetup(t)
	r := New()
	got, err := r.Exec.Call(c, "sudo.validate",
		value.MapOf("path", filepath.Join(t.TempDir(), "absent")))
	if err != nil {
		t.Fatalf("sudo.validate: %v", err)
	}
	if valid, _ := got.(*value.Map).GetString("valid"); valid != false {
		t.Errorf("a file that does not exist was reported valid=%v", valid)
	}
}

// The version comes back from the real sudo.
func TestLiveSudoReadsItsOwnVersion(t *testing.T) {
	c := liveSudoSetup(t)
	r := New()
	got, err := r.Exec.Call(c, "sudo.version", value.NewMap(0))
	if err != nil {
		t.Fatalf("sudo.version: %v", err)
	}
	v, _ := got.(string)
	if v == "" || !strings.ContainsAny(v, "0123456789") {
		t.Errorf("version reads %q, which is not a version", v)
	}
	t.Logf("this host runs sudo %s", v)
}

// Unprivileged, the path falls back and says that it fell back.
//
// This is the branch a silent fallback would hide. The answer must be
// usable *and* must not claim to be sudo's own word when it is not.
func TestLiveSudoPathSaysWhichRouteItTook(t *testing.T) {
	c := liveSudoSetup(t)
	r := New()
	got, err := r.Exec.Call(c, "sudo.path", value.NewMap(0))
	if err != nil {
		t.Fatalf("sudo.path: %v", err)
	}
	m := got.(*value.Map)
	path, _ := m.GetString("path")
	source, _ := m.GetString("source")
	comment, _ := m.GetString("comment")
	if path == "" {
		t.Fatal("no path came back")
	}
	switch source {
	case "sudo":
		if os.Geteuid() != 0 {
			t.Errorf("sudo reported its path to a caller that is not root, which contradicts "+
				"the reason the fallback exists: %v", comment)
		}
	case "convention":
		if os.Geteuid() == 0 {
			t.Errorf("running as root the path should come from sudo itself, not from "+
				"convention: %v", comment)
		}
		if !strings.Contains(comment.(string), "not a statement about this node") {
			t.Errorf("the fallback does not say it is one: %v", comment)
		}
	default:
		t.Errorf("source=%v, which is neither route", source)
	}
	t.Logf("sudoers is %v, per %v", path, source)
}

// As root, `sudo -l -U` is read for a real account.
func TestLiveSudoListsARealAccount(t *testing.T) {
	c := liveSudoSetup(t)
	if os.Geteuid() != 0 {
		t.Skip("sudo shows another account's rules to root only")
	}
	r := New()
	got, err := r.Exec.Call(c, "sudo.list", value.MapOf("name", "root"))
	if err != nil {
		t.Fatalf("sudo.list: %v", err)
	}
	m := got.(*value.Map)
	rules, _ := m.GetString("rules")
	t.Logf("root may run: %v", rules)

	// And an account that does not exist is refused rather than reported
	// as one with no rules.
	if _, err := r.Exec.Call(c, "sudo.list",
		value.MapOf("name", "halite_no_such_account")); err == nil {
		t.Error("an account that does not exist was reported as having no rules")
	}
}

// liveSudoSetup skips unless the real tools are here.
func liveSudoSetup(t *testing.T) *exec.Context {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("sudo is a unix program")
	}
	c := &exec.Context{}
	for _, tool := range []string{"sudo", "visudo"} {
		if c.Which(tool) == "" {
			t.Skipf("this host has no `%s`; sudo is not part of a base install", tool)
		}
	}
	return c
}
