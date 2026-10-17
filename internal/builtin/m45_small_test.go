package builtin

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// **A sysctl value cannot add a line to the configuration** (issue 321,
// M-45). The name and value become `name=value`, and a newline in either
// set a second key at every boot. The state refuses before it asks the
// kernel anything, so a refusal changes nothing, now or later.
func TestSysctlRefusesANewlineBeforeItChangesAnything(t *testing.T) {
	for _, kv := range [][2]string{
		{"vm.swappiness", "10\nkernel.modules_disabled=1"},
		{"vm.swappiness\nkernel.x", "10"},
		{"vm swappiness", "10"},
	} {
		rec := &exec.RecordingRunner{}
		c := newCtx(false)
		c.Runner = rec
		res, err := sysctlPresent(c, value.MapOf("name", kv[0], "value", kv[1],
			"config", filepath.Join(t.TempDir(), "sysctl.conf")))
		if err != nil {
			t.Fatal(err)
		}
		if res.Result == nil || *res.Result {
			t.Errorf("%q=%q was accepted: %+v", kv[0], kv[1], res)
		}
		if len(rec.Ran) != 0 {
			t.Errorf("%q=%q ran %q", kv[0], kv[1], rec.RanCommands())
		}
	}
	if err := sysctlValid("net.ipv4.ip_local_port_range", "32768 60999"); err != nil {
		t.Errorf("a value with spaces was refused: %v", err)
	}
}

// **A debconf package name cannot start a record** (issue 321, M-45; the
// triage found the question and value already checked, the package not).
func TestDebconfRefusesAPackageWithWhitespace(t *testing.T) {
	for _, pkg := range []string{"tzdata\ntzdata tzdata/Areas select Etc", "two words"} {
		s := debconfSelection{Package: pkg, Question: "tzdata/Areas", Type: "select", Value: "Etc"}
		if err := s.validate(); err == nil || !strings.Contains(err.Error(), "package name") {
			t.Errorf("package %q: %v", pkg, err)
		}
	}
	if err := (debconfSelection{Package: "tzdata", Question: "tzdata/Areas", Type: "select", Value: "Etc"}).validate(); err != nil {
		t.Errorf("an ordinary selection was refused: %v", err)
	}
}
