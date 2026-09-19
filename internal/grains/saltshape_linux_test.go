//go:build linux

package grains

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// The Linux half of the comparison described in saltshape_test.go.

func TestKernelParamsKeepsRepeatedAndEmbeddedValues(t *testing.T) {
	// Both halves of this were losing data. A mapping cannot hold
	// `console=tty1 console=ttyS0`, which is how every cloud image asks
	// for both consoles, and Salt's own parser cannot hold
	// `root=UUID=...`, because it splits on every `=` and keeps the
	// value only when there are exactly two fields.
	got := kernelParams(`BOOT_IMAGE=/vmlinuz root=UUID=2f3f-ae0e ro console=tty1 console=ttyS0 quiet="a b"`)
	want := []any{
		[]any{"BOOT_IMAGE", "/vmlinuz"},
		[]any{"root", "UUID=2f3f-ae0e"},
		[]any{"ro", ""},
		[]any{"console", "tty1"},
		[]any{"console", "ttyS0"},
		[]any{"quiet", `a b`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("kernelParams =\n  %v\nwant\n  %v", got, want)
	}
}

func TestKernelParamsIsEmptyRatherThanNil(t *testing.T) {
	// A template iterating the grain on a host with no command line
	// should get an empty sequence, not a null it has to guard.
	if got := kernelParams(""); !reflect.DeepEqual(got, []any{}) {
		t.Errorf("kernelParams(\"\") = %#v, want []any{}", got)
	}
}

// The systemd grain, read back from the tool it claims to be reporting.
//
// This is the assertion that was missing. `detectSystemd` returned
// /proc/1/comm -- the process name -- so the grain said its version was
// "systemd" on every Linux host, and the only test that touched it
// checked that it was a mapping.
func TestSystemdVersionIsWhatSystemctlReports(t *testing.T) {
	out, err := exec.Command("systemctl", "--version").Output()
	if err != nil {
		t.Skipf("no usable systemctl here: %v", err)
	}
	lines := strings.Split(string(out), "\n")
	fields := strings.Fields(lines[0])
	if len(fields) < 2 {
		t.Skipf("systemctl --version printed %q, which has no version field", lines[0])
	}

	// Through detectSystemd, which is what sets the grain. Asking
	// systemctlVersion directly would leave the defect this test exists
	// for entirely uncovered: the parser was always right, and what was
	// wrong was that the grain never called it.
	sd := detectSystemd()
	version, _ := sd.Get("version")
	features, _ := sd.Get("features")

	// **systemctl installed and systemd not running is a real machine**,
	// not a broken one: the lab's `debian13sysv` row converts a Debian
	// to sysvinit and leaves every systemd binary in place. The grain is
	// empty there on purpose -- Salt reports it only where systemd is
	// the init -- so that is what gets asserted, and this test used to
	// fail on such a host because it took `systemctl --version` working
	// as proof that systemd was running. DIVERGENCE 5.126.
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		if version != "" || features != "" {
			t.Errorf("systemd is not the init here and the grain still reports version %v, features %v",
				version, features)
		}
		return
	}
	if version != fields[1] {
		t.Errorf("the systemd version grain is %v, want %q", version, fields[1])
	}
	// The series, not the distribution's package string: `249`, not
	// `249.11-0ubuntu3.22`.
	if v, ok := version.(string); !ok || strings.ContainsAny(v, ".-") {
		t.Errorf("the systemd version grain is %v, which is not a bare series", version)
	}
	if len(lines) >= 2 {
		if want := strings.TrimSpace(lines[1]); features != want {
			t.Errorf("the systemd features grain is %v, want %q", features, want)
		}
	}
	if features == "" {
		t.Error("the systemd features grain is empty; systemctl prints a feature list")
	}
}

// osarch is the package architecture, which on Debian is not the CPU's
// name: dpkg says `arm64` where uname says `aarch64`.
func TestPackageArchIsWhatTheDebianToolPrints(t *testing.T) {
	out, err := exec.Command("dpkg", "--print-architecture").Output()
	if err != nil {
		t.Skipf("no dpkg here: %v", err)
	}
	want := strings.TrimSpace(string(out))
	if got := packageArch("Debian"); got != want {
		t.Errorf("packageArch(\"Debian\") = %q, want %q", got, want)
	}
	// A family with no package architecture of its own falls back to
	// the CPU name, which is what Salt does.
	if got := packageArch("Gentoo"); got != goarchToCPU() {
		t.Errorf("packageArch(\"Gentoo\") = %q, want %q", got, goarchToCPU())
	}
}

// disks and ssds partition the block devices; they do not nest.
func TestDisksAndSsdsDoNotOverlap(t *testing.T) {
	disks, ssds := blockDevices()
	inDisks := map[any]bool{}
	for _, d := range disks {
		inDisks[d] = true
	}
	for _, s := range ssds {
		if inDisks[s] {
			t.Errorf("%v is reported as both a disk and an ssd", s)
		}
	}
}
