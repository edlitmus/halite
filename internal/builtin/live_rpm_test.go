package builtin

import (
	"bytes"
	"os"
	osexec "os/exec"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// requireRpm skips, with the reason, anywhere the rpm module cannot run.
// The reads need neither root nor HALITE_SYSTEM_LIVE, the way
// `modprobe.list` and `pro.status` do not: they change nothing.
func requireRpm(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("rpm is a RedHat-family tool; this is %s", runtime.GOOS)
	}
	if _, err := osexec.LookPath("rpm"); err != nil {
		t.Skip("this host has no rpm; the rpm module needs a RedHat-family system")
	}
	if out, err := osexec.Command("rpm", "-q", "bash").CombinedOutput(); err != nil {
		t.Skipf("rpm is here but its database has no bash (%s); not a RedHat-family host",
			strings.TrimSpace(string(out)))
	}
}

// rpmTool runs rpm directly, bypassing the module, so the module's answer
// is checked against a second reading of the same database rather than
// against itself. The forms used here -- plain `rpm -q`, `rpm -ql` -- are
// deliberately not the queryformats the module sends.
func rpmTool(t *testing.T, args ...string) string {
	t.Helper()
	cmd := osexec.Command("rpm", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, _ := cmd.Output()
	return string(out)
}

func TestLiveRpmReadsTheRealDatabase(t *testing.T) {
	requireRpm(t)
	r := New()
	c := &exec.Context{}

	// list_pkgs, all of it, counted against `rpm -qa` one line per
	// installed instance -- so several kernels or several gpg-pubkeys
	// have to all be there, not just the last one read.
	raw, err := r.Exec.Call(c, "rpm.list_pkgs", value.NewMap(0))
	if err != nil {
		t.Fatalf("rpm.list_pkgs: %v", err)
	}
	all := raw.(*value.Map)
	instances := 0
	for _, name := range all.StringKeys() {
		v, _ := all.Get(name)
		instances += len(v.([]any))
	}
	want := len(strings.Fields(rpmTool(t, "-qa")))
	if instances != want {
		t.Errorf("rpm.list_pkgs reports %d installed instances, `rpm -qa` lists %d", instances, want)
	}

	// info, assembled back into the NEVRA `rpm -q bash` prints on its own.
	raw, err = r.Exec.Call(c, "rpm.info", value.MapOf("names", []any{"bash"}))
	if err != nil {
		t.Fatalf("rpm.info: %v", err)
	}
	bashList, _ := raw.(*value.Map).Get("bash")
	bash := bashList.([]any)[0].(*value.Map)
	name, _ := bash.Get("name")
	version, _ := bash.Get("version")
	release, _ := bash.Get("release")
	arch, _ := bash.Get("arch")
	nevra := value.KeyString(name) + "-" + value.KeyString(version) + "-" + value.KeyString(release) + "." + value.KeyString(arch)
	if tool := strings.TrimSpace(rpmTool(t, "-q", "bash")); nevra != tool {
		t.Errorf("rpm.info assembles %q, `rpm -q bash` says %q", nevra, tool)
	}
	if desc, _ := bash.Get("description"); !strings.Contains(value.KeyString(desc), "Bourne") {
		t.Errorf("bash's description = %q", desc)
	}
	if _, err := r.Exec.Call(c, "rpm.info", value.MapOf("names", []any{"bash", "halite-no-such-package"})); err == nil ||
		!strings.Contains(err.Error(), "halite-no-such-package") {
		t.Errorf("info on a package that is not installed: err = %v", err)
	}

	// file_list against `rpm -ql`, as sets.
	raw, err = r.Exec.Call(c, "rpm.file_list", value.MapOf("names", []any{"bash"}))
	if err != nil {
		t.Fatalf("rpm.file_list: %v", err)
	}
	var mine []string
	for _, p := range raw.([]any) {
		mine = append(mine, value.KeyString(p))
	}
	theirs := strings.Fields(rpmTool(t, "-ql", "bash"))
	sort.Strings(theirs)
	if strings.Join(mine, "\n") != strings.Join(theirs, "\n") {
		t.Errorf("rpm.file_list bash has %d paths, `rpm -ql bash` %d", len(mine), len(theirs))
	}

	// A package with no files: `rpm -ql` prints "(contains no files)",
	// which must not come back as a path.
	raw, err = r.Exec.Call(c, "rpm.file_dict", value.MapOf("names", []any{"gpg-pubkey"}))
	if err != nil {
		t.Logf("rpm.file_dict gpg-pubkey: %v (no key imported on this host)", err)
	} else if raw.(*value.Map).Len() != 0 {
		t.Errorf("gpg-pubkey's files = %v, want none", raw)
	}

	// owner: owned, unowned (this test binary), absent.
	self, _ := os.Executable()
	raw, err = r.Exec.Call(c, "rpm.owner", value.MapOf("paths", []any{"/usr/bin/bash", self}))
	if err != nil {
		t.Fatalf("rpm.owner: %v", err)
	}
	owners := raw.(*value.Map)
	if got, _ := owners.Get("/usr/bin/bash"); len(got.([]any)) != 1 || got.([]any)[0] != "bash" {
		t.Errorf("owner of /usr/bin/bash = %#v", got)
	}
	if got, _ := owners.Get(self); len(got.([]any)) != 0 {
		t.Errorf("owner of this test binary = %#v, want nobody", got)
	}
	if _, err := r.Exec.Call(c, "rpm.owner", value.MapOf("paths", []any{"/halite/no/such/path"})); err == nil ||
		!strings.Contains(err.Error(), "No such file") {
		t.Errorf("owner of an absent path: err = %v", err)
	}
}

// TestLiveRpmVersionCmpAgreesWithRpm is a differential: CompareRPM
// against rpm's own comparison, reached through its embedded Lua.
//
// Only bare versions -- no epoch, no release. `rpm.vercmp` is raw
// rpmvercmp on rpm 4.14.3, where "0:1.0" against "1.0" is -1 because the
// colon is just a separator, and an EVR comparison on rpm 4.16.1.3,
// where the same pair is 0. That was measured on the two lab hosts, and
// it means only the version-segment comparison has an oracle that means
// the same thing on both. Caret is included: 4.14.3 on AlmaLinux 8.10
// sorts it as rpm 4.15 introduced, not as a separator.
func TestLiveRpmVersionCmpAgreesWithRpm(t *testing.T) {
	requireRpm(t)
	pairs := [][2]string{
		{"1.0", "1.0"}, {"1.0", "1.0~rc1"}, {"1.0~rc1", "1.0~rc2"}, {"1.0^git1", "1.0"},
		{"1.0^1", "1.0.1"}, {"1.0~rc1^git1", "1.0~rc1"}, {"1.0a", "1.0.1"}, {"1.0.0", "1.0"},
		{"1_0", "1.0"}, {"a", "b"}, {"2.10", "2.9"}, {"1.01", "1.1"}, {"5.1.8", "5.1.10"},
		{"4.18.0", "5.14.0"}, {"1.0rc1", "1.0"}, {"fc4", "fc.4"},
	}
	r := New()
	for _, p := range pairs {
		out := rpmTool(t, "--eval", "%{lua: print(rpm.vercmp(\""+p[0]+"\", \""+p[1]+"\"))}")
		want := strings.TrimSpace(out)
		got, err := r.Exec.Call(&exec.Context{}, "rpm.version_cmp", value.MapOf("ver1", p[0], "ver2", p[1]))
		if err != nil {
			t.Fatal(err)
		}
		if value.KeyString(got) != want {
			t.Errorf("version_cmp(%q, %q) = %v, rpm says %q", p[0], p[1], got, want)
		}
	}
}

// TestLiveRpmVerifySeesARealModification changes files a package owns
// and asks rpm.verify to find them: a config file with a line appended,
// and a doc file moved away. Gated on root and HALITE_SYSTEM_LIVE because
// it edits /etc; each file is put back byte for byte, mode and mtime
// included, and the test then asks rpm.verify again to prove it.
func TestLiveRpmVerifySeesARealModification(t *testing.T) {
	requireRpm(t)
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this modify and restore a packaged config file")
	}
	if os.Geteuid() != 0 {
		t.Skip("modifying a packaged config file needs root")
	}
	const config = "/etc/DIR_COLORS.lightbgcolor"
	const doc = "/usr/share/doc/which/NEWS"
	for _, p := range []string{config, doc} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("%s is not on this host (%v); the test needs coreutils-common and which", p, err)
		}
	}
	r := New()
	c := &exec.Context{}
	raw, err := r.Exec.Call(c, "rpm.owner", value.MapOf("paths", []any{config, doc}))
	if err != nil {
		t.Fatalf("rpm.owner: %v", err)
	}
	var pkgs []any
	for _, p := range []string{config, doc} {
		o, _ := raw.(*value.Map).Get(p)
		pkgs = append(pkgs, o.([]any)...)
	}

	original, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(config)
	restore := func() {
		if err := os.WriteFile(config, original, info.Mode().Perm()); err != nil {
			t.Errorf("restoring %s: %v", config, err)
		}
		if err := os.Chtimes(config, time.Now(), info.ModTime()); err != nil {
			t.Errorf("restoring %s's mtime: %v", config, err)
		}
		if _, err := os.Stat(doc); os.IsNotExist(err) {
			if err := os.Rename(doc+".halite-moved", doc); err != nil {
				t.Errorf("restoring %s: %v", doc, err)
			}
		}
	}
	t.Cleanup(restore)

	if err := os.WriteFile(config, append(bytes.Clone(original), []byte("# halite rpm.verify live test\n")...),
		info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(doc, doc+".halite-moved"); err != nil {
		t.Fatal(err)
	}

	raw, err = r.Exec.Call(c, "rpm.verify", value.MapOf("names", pkgs))
	if err != nil {
		t.Fatalf("rpm.verify: %v", err)
	}
	found := raw.(*value.Map)
	entry, ok := found.Get(config)
	if !ok {
		t.Fatalf("rpm.verify did not report %s; it reported %v", config, found.StringKeys())
	}
	m := entry.(*value.Map)
	if typ, _ := m.Get("type"); typ != "config" {
		t.Errorf("%s type = %#v, want config", config, typ)
	}
	failed, _ := m.Get("failed")
	joined := strings.Join(func() []string {
		var s []string
		for _, f := range failed.([]any) {
			s = append(s, value.KeyString(f))
		}
		return s
	}(), ",")
	if !strings.Contains(joined, "size") || !strings.Contains(joined, "digest") {
		t.Errorf("%s failed = %s, want size and digest among them", config, joined)
	}
	entry, ok = found.Get(doc)
	if !ok {
		t.Fatalf("rpm.verify did not report the missing %s", doc)
	}
	if missing, _ := entry.(*value.Map).Get("missing"); missing != true {
		t.Errorf("%s missing = %#v", doc, missing)
	}

	restore()
	raw, err = r.Exec.Call(c, "rpm.verify", value.MapOf("names", pkgs))
	if err != nil {
		t.Fatalf("rpm.verify after restoring: %v", err)
	}
	for _, p := range []string{config, doc} {
		if raw.(*value.Map).Has(p) {
			t.Errorf("after restoring, rpm.verify still reports %s", p)
		}
	}
}
