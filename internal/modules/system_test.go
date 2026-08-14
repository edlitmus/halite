package modules

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocaleValueIsReadFromTheConfFile(t *testing.T) {
	conf := `# set by the installer
LANG="en_GB.UTF-8"
LC_TIME=en_DK.UTF-8
`
	cases := []struct {
		key  string
		want string
	}{
		{"LANG", "en_GB.UTF-8"}, // quotes are Debian's habit, not part of the value
		{"LC_TIME", "en_DK.UTF-8"},
		{"LC_ALL", ""},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			if got := localeValue(conf, tc.key); got != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestWritingALocaleKeepsTheOthers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locale.conf")
	if err := os.WriteFile(path, []byte("LANG=en_GB.UTF-8\nLC_TIME=en_DK.UTF-8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeLocaleConf(path, "LANG", "en_US.UTF-8"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "LANG=en_US.UTF-8\nLC_TIME=en_DK.UTF-8\n" {
		t.Fatalf("unexpected contents %q", got)
	}

	if err := writeLocaleConf(path, "LC_ALL", "C"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "LANG=en_US.UTF-8\nLC_TIME=en_DK.UTF-8\nLC_ALL=C\n" {
		t.Fatalf("a new key should be appended, got %q", got)
	}
}

func TestUnknownTimezoneIsRefused(t *testing.T) {
	r := timezoneSystem(&Ctx{}, "Mars/Olympus_Mons", nil)
	if r.Ok {
		t.Fatal("a zone with no zoneinfo file should fail rather than silently leaving UTC")
	}
}

func TestKmodListedReadsProcModules(t *testing.T) {
	procModules := "nfsd followed by fields\nip_tables 32768 2 iptable_filter,iptable_nat, Live 0x0\n"
	cases := []struct {
		module string
		want   bool
	}{
		{"nfsd", true},
		{"ip_tables", true},
		{"ip-tables", true}, // modprobe takes dashes where /proc prints underscores
		{"ip_tables.ko", true},
		{"iptable_nat", false}, // a dependency of another module is not loaded itself
		{"absent", false},
	}
	for _, tc := range cases {
		t.Run(tc.module, func(t *testing.T) {
			if got := kmodListed(procModules, tc.module, 0); got != tc.want {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

func TestKmodPersistFileAddsAndRemovesOneLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loader.conf")
	if err := os.WriteFile(path, []byte("# the host's own settings\nzfs_load=\"YES\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	line := `nfs_load="YES"`

	if got, err := kmodPersistFile(path, line, true); err != nil || got != "added" {
		t.Fatalf("want it added, got %q (%v)", got, err)
	}
	if got := read(t, path); got != "# the host's own settings\nzfs_load=\"YES\"\nnfs_load=\"YES\"\n" {
		t.Fatalf("the rest of the file belongs to the host, got %q", got)
	}
	if got, _ := kmodPersistFile(path, line, true); got != "unchanged" {
		t.Fatalf("a second add should be a no-op, got %q", got)
	}
	if got, _ := kmodPersistFile(path, line, false); got != "removed" {
		t.Fatalf("want it removed, got %q", got)
	}
	if got := read(t, path); got != "# the host's own settings\nzfs_load=\"YES\"\n" {
		t.Fatalf("unexpected contents %q", got)
	}
	if got, _ := kmodPersistFile(path, line, false); got != "unchanged" {
		t.Fatalf("removing what is not there is a no-op, got %q", got)
	}
}

func TestAlternativesQueryIsRead(t *testing.T) {
	query := `Name: editor
Link: /usr/bin/editor
Status: manual
Best: /usr/bin/vim.basic
Value: /usr/bin/vim.basic

Alternative: /usr/bin/vim.basic
Priority: 30

Alternative: /bin/ed
Priority: -100
`
	if got := alternativeCurrent(query); got != "/usr/bin/vim.basic" {
		t.Fatalf("want the selected path, got %q", got)
	}
	for _, path := range []string{"/usr/bin/vim.basic", "/bin/ed"} {
		if !alternativeHasPath(query, path) {
			t.Fatalf("%s is registered", path)
		}
	}
	if alternativeHasPath(query, "/usr/bin/nano") {
		t.Fatal("an unregistered candidate should not be found")
	}
	if alternativeCurrent("") != "" {
		t.Fatal("an empty query has no value")
	}
}
