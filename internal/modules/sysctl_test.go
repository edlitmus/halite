package modules

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSysctlValuesEqualNormalizesWhitespace(t *testing.T) {
	cases := []struct {
		current, want string
		equal         bool
	}{
		{"1024", "1024", true},
		{"1024", "2048", false},
		// `sysctl -n` prints multi-value keys tab-separated; SLS files use
		// spaces. The two spellings are the same setting.
		{"4096\t87380\t6291456", "4096 87380 6291456", true},
		{"4096  87380   6291456", "4096 87380 6291456", true},
		{" 1 ", "1", true},
		{"4096\t87380\t6291456", "4096 87380 999", false},
	}
	for _, c := range cases {
		if got := sysctlValuesEqual(c.current, c.want); got != c.equal {
			t.Errorf("sysctlValuesEqual(%q, %q) = %v, want %v", c.current, c.want, got, c.equal)
		}
	}
}

func TestConfSetSettingReplacesAndAppends(t *testing.T) {
	config := filepath.Join(t.TempDir(), "sysctl.conf")
	if err := os.WriteFile(config, []byte("# tuned\nkern.maxfiles=100\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := confSetSetting(config, "kern.maxfiles", "200"); err != nil {
		t.Fatal(err)
	}
	if err := confSetSetting(config, "kern.ipc.somaxconn", "1024"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	want := "# tuned\nkern.maxfiles=200\nkern.ipc.somaxconn=1024\n"
	if string(b) != want {
		t.Errorf("config = %q, want %q", b, want)
	}
	// The atomic write must not strand its temp file next to the config.
	entries, err := os.ReadDir(filepath.Dir(config))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want just the config: %v", len(entries), entries)
	}
}

func TestConfHasSettingIgnoresWhitespaceLayout(t *testing.T) {
	config := filepath.Join(t.TempDir(), "sysctl.conf")
	if err := os.WriteFile(config, []byte("net.ipv4.tcp_rmem=4096\t87380\t6291456\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !confHasSetting(config, "net.ipv4.tcp_rmem", "4096 87380 6291456") {
		t.Error("tab-separated persisted value not recognized as converged")
	}
}
