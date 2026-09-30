package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// Every fixture in this file is the stdout of the exact argv the module
// runs, captured on 2026-09-29/30 from two lab instances: Rocky Linux 9.8
// with rpm 4.16.1.3, and AlmaLinux 8.10 with rpm 4.14.3. They were taken
// by a small program that ran the argv without a shell and printed the
// bytes as Go literals, so nothing here was retyped. Where the two
// versions agree, one host's output stands for both and the comment says
// so; where they differ, both are here.

// rpmListAlma8 is `rpm -q --queryformat <rpmListFormat> bash gpg-pubkey
// kernel-core nosuchpkg` on AlmaLinux 8.10. Two kernel-core packages and
// three gpg-pubkeys: the reason this module reports a list per name. The
// "not installed" line is on stdout, among the records.
const rpmListAlma8 = "bash\t(none)\t4.4.20\t6.el8_10\tx86_64\t1789549949\ngpg-pubkey\t(none)\t3abb34f8\t5ffd890e\t(none)\t1789549932\ngpg-pubkey\t(none)\tced7258b\t6525146f\t(none)\t1789549932\ngpg-pubkey\t(none)\t2f86d6a1\t5cf7cefb\t(none)\t1789550312\nkernel-core\t(none)\t4.18.0\t553.el8_10\tx86_64\t1789549720\nkernel-core\t(none)\t4.18.0\t553.163.1.el8_10\tx86_64\t1789550057\npackage nosuchpkg is not installed\n"

// rpmListRocky9 is the same format on Rocky 9.8: the first lines of `-a`
// (sorted), which carry a real epoch, and the two gpg-pubkeys.
const rpmListRocky9 = "aardvark-dns\t2\t1.17.1\t1.el9_8\tx86_64\t1789628168\nabattis-cantarell-fonts\t(none)\t0.301\t4.el9\tnoarch\t1789628163\ngpg-pubkey\t(none)\t350d275d\t6279464b\t(none)\t1789628476\ngpg-pubkey\t(none)\t3228467c\t613798eb\t(none)\t1789628589\n"

func TestParseRpmListKeepsEveryInstalledInstance(t *testing.T) {
	got := parseRpmList(rpmListAlma8)
	kernels, _ := got.Get("kernel-core")
	list, ok := kernels.([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("kernel-core = %#v, want both installed kernels", kernels)
	}
	second := list[1].(*value.Map)
	if v, _ := second.Get("version"); v != "4.18.0-553.163.1.el8_10" {
		t.Errorf("second kernel version = %#v", v)
	}
	keys, _ := got.Get("gpg-pubkey")
	if l, _ := keys.([]any); len(l) != 3 {
		t.Errorf("gpg-pubkey = %#v, want three keys", keys)
	}
	key := keys.([]any)[0].(*value.Map)
	if arch, _ := key.Get("arch"); arch != "" {
		t.Errorf("gpg-pubkey arch = %#v, want rpm's (none) read as empty", arch)
	}
	if got.Has("package nosuchpkg is not installed") || got.Has("nosuchpkg") {
		t.Error("the not-installed line was read as a package")
	}
	if got.Len() != 3 {
		t.Errorf("got %d names, want bash, gpg-pubkey and kernel-core", got.Len())
	}
}

func TestParseRpmListSpellsTheEpochOnlyWhenThereIsOne(t *testing.T) {
	got := parseRpmList(rpmListRocky9)
	aardvark, _ := got.Get("aardvark-dns")
	a := aardvark.([]any)[0].(*value.Map)
	if v, _ := a.Get("version"); v != "2:1.17.1-1.el9_8" {
		t.Errorf("aardvark-dns version = %#v", v)
	}
	if e, _ := a.Get("epoch"); e != "2" {
		t.Errorf("aardvark-dns epoch = %#v", e)
	}
	fonts, _ := got.Get("abattis-cantarell-fonts")
	f := fonts.([]any)[0].(*value.Map)
	if v, _ := f.Get("version"); v != "0.301-4.el9" {
		t.Errorf("abattis-cantarell-fonts version = %#v, want no (none): prefix", v)
	}
}

// rpmInfoAlma8 is `rpm -q --queryformat <rpmInfoFormat> -- bash nosuchpkg`
// on AlmaLinux 8.10, verbatim.
const rpmInfoAlma8 = "name: bash\nepoch: (none)\nversion: 4.4.20\nrelease: 6.el8_10\narch: x86_64\ninstall_date_time_t: 1789549949\nbuild_date_time_t: 1756198277\nbuild_host: x64-builder01.almalinux.org\ngroup: Unspecified\nsource_rpm: bash-4.4.20-6.el8_10.src.rpm\nsize: 6865564\nlicense: GPLv3+\nsignature: RSA/SHA256, Tue Aug 26 09:54:46 2025, Key ID 2ae81e8aced7258b\npackager: AlmaLinux Packaging Team <packager@almalinux.org>\nvendor: AlmaLinux\nurl: https://www.gnu.org/software/bash\nsummary: The GNU Bourne Again shell\ndescription:\nThe GNU Bourne Again shell (Bash) is a shell or command language\ninterpreter that is compatible with the Bourne shell (sh). Bash\nincorporates useful features from the Korn shell (ksh) and the C shell\n(csh). Most sh scripts can be run by bash without modification.\n@@halite-rpm-record-end@@\npackage nosuchpkg is not installed\n"

// rpmInfoGpgPubkeyRocky9 is the first gpg-pubkey record of the same query
// on Rocky 9.8. Its description is an armored key, which carries a blank
// line and ends with one -- the shape a description parser most easily
// gets wrong. The key body is cut to its first and last lines; every
// line kept is verbatim, and no line was added.
const rpmInfoGpgPubkeyRocky9 = "name: gpg-pubkey\nepoch: (none)\nversion: 350d275d\nrelease: 6279464b\narch: (none)\ninstall_date_time_t: 1789628476\nbuild_date_time_t: 1652115019\nbuild_host: localhost\ngroup: Public Keys\nsource_rpm: (none)\nsize: 0\nlicense: pubkey\nsignature: (none)\npackager: Rocky Enterprise Software Foundation - Release key 2022 <releng@rockylinux.org>\nvendor: (none)\nurl: (none)\nsummary: Rocky Enterprise Software Foundation - Release key 2022 <releng@rockylinux.org> public key\ndescription:\n-----BEGIN PGP PUBLIC KEY BLOCK-----\nVersion: rpm-4.16.1.3 (NSS-3)\n\nmQINBGJ5RksBEADF/Lzssm7uryV6+VHAgL36klyCVcHwvx9Bk853LBOuHVEZWsme\n=LZX/\n-----END PGP PUBLIC KEY BLOCK-----\n\n@@halite-rpm-record-end@@\n"

func TestParseRpmInfoReadsARealRecord(t *testing.T) {
	got, missing := parseRpmInfo(rpmInfoAlma8)
	if len(missing) != 1 || missing[0] != "nosuchpkg" {
		t.Errorf("missing = %v, want [nosuchpkg]", missing)
	}
	raw, _ := got.Get("bash")
	list, ok := raw.([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("bash = %#v", raw)
	}
	bash := list[0].(*value.Map)
	want := map[string]string{
		"version":    "4.4.20",
		"release":    "6.el8_10",
		"epoch":      "",
		"evr":        "4.4.20-6.el8_10",
		"arch":       "x86_64",
		"source_rpm": "bash-4.4.20-6.el8_10.src.rpm",
		"signature":  "RSA/SHA256, Tue Aug 26 09:54:46 2025, Key ID 2ae81e8aced7258b",
		"summary":    "The GNU Bourne Again shell",
		"vendor":     "AlmaLinux",
	}
	for key, w := range want {
		if v, _ := bash.Get(key); v != w {
			t.Errorf("%s = %#v, want %q", key, v, w)
		}
	}
	desc, _ := bash.Get("description")
	if d, _ := desc.(string); !strings.HasPrefix(d, "The GNU Bourne Again shell (Bash)") ||
		!strings.HasSuffix(d, "without modification.") || strings.Count(d, "\n") != 3 {
		t.Errorf("description = %q", desc)
	}
}

func TestParseRpmInfoKeepsABlankLineInsideADescription(t *testing.T) {
	got, missing := parseRpmInfo(rpmInfoGpgPubkeyRocky9)
	if len(missing) != 0 {
		t.Errorf("missing = %v", missing)
	}
	raw, _ := got.Get("gpg-pubkey")
	key := raw.([]any)[0].(*value.Map)
	desc, _ := key.Get("description")
	d, _ := desc.(string)
	if !strings.Contains(d, "(NSS-3)\n\nmQINB") || !strings.HasSuffix(d, "-----END PGP PUBLIC KEY BLOCK-----") {
		t.Errorf("description = %q", d)
	}
	for _, field := range []string{"vendor", "url", "source_rpm", "signature", "arch"} {
		if v, _ := key.Get(field); v != "" {
			t.Errorf("%s = %#v, want rpm's (none) read as empty", field, v)
		}
	}
}

// rpmFilesRocky9 is `rpm -q --queryformat [%{=NAME}\t%{FILENAMES}\n] --
// which gpg-pubkey nosuchpkg` on Rocky 9.8. gpg-pubkey contributes
// nothing -- `rpm -ql` would have printed "(contains no files)" twice.
const rpmFilesRocky9 = "which\t/etc/profile.d/which2.csh\nwhich\t/etc/profile.d/which2.sh\nwhich\t/usr/bin/which\nwhich\t/usr/lib/.build-id\nwhich\t/usr/lib/.build-id/e1\nwhich\t/usr/lib/.build-id/e1/57f4fc42e42d9e479d409d081262042c8e200c\nwhich\t/usr/share/doc/which\nwhich\t/usr/share/doc/which/AUTHORS\nwhich\t/usr/share/doc/which/EXAMPLES\nwhich\t/usr/share/doc/which/NEWS\nwhich\t/usr/share/doc/which/README\nwhich\t/usr/share/info/which.info.gz\nwhich\t/usr/share/licenses/which\nwhich\t/usr/share/licenses/which/COPYING\nwhich\t/usr/share/man/man1/which.1.gz\npackage nosuchpkg is not installed\n"

func TestParseRpmFileDict(t *testing.T) {
	got, missing := parseRpmFileDict(rpmFilesRocky9)
	if len(missing) != 1 || missing[0] != "nosuchpkg" {
		t.Errorf("missing = %v", missing)
	}
	if got.Has("gpg-pubkey") {
		t.Error("a package with no files was given an entry")
	}
	raw, _ := got.Get("which")
	files, _ := raw.([]any)
	if len(files) != 15 || files[2] != "/usr/bin/which" {
		t.Fatalf("which = %#v", raw)
	}
	flat := rpmFlattenFiles(got)
	if len(flat) != 15 || flat[0] != "/etc/profile.d/which2.csh" {
		t.Errorf("flattened = %#v", flat)
	}
}

func TestRpmFileListRefusesAMissingPackage(t *testing.T) {
	argv := []string{"rpm", "-q", "--queryformat", "[%{=NAME}\\t%{FILENAMES}\\n]", "--", "which", "gpg-pubkey", "nosuchpkg"}
	c := proTestContext(map[string]exec.Result{
		exec.Command{Argv: argv}.String(): {Stdout: rpmFilesRocky9, Code: 1},
	})
	_, err := rpmFileDict(c, []string{"which", "gpg-pubkey", "nosuchpkg"})
	if err == nil || !strings.Contains(err.Error(), "nosuchpkg") {
		t.Errorf("err = %v, want it to name nosuchpkg", err)
	}
}

// The four `rpm -qf --queryformat %{NAME}\n -- <path>` answers captured on
// Rocky 9.8: owned by one, a path that does not exist (stderr, exit 1),
// owned by two (Rocky's binutils owns man1 as well as filesystem;
// AlmaLinux 8's does not), and not owned by any package (stdout, exit 1).
func TestParseRpmOwner(t *testing.T) {
	cases := []struct {
		path    string
		res     exec.Result
		want    []any
		wantErr string
	}{
		{"/etc/hosts", exec.Result{Stdout: "setup\n"}, []any{"setup"}, ""},
		{"/nonexist", exec.Result{Stderr: "error: file /nonexist: No such file or directory\n", Code: 1}, nil,
			"No such file or directory"},
		{"/usr/share/man/man1", exec.Result{Stdout: "filesystem\nbinutils\n"}, []any{"filesystem", "binutils"}, ""},
		{"/root/rpmchattr-capture", exec.Result{Stdout: "file /root/rpmchattr-capture is not owned by any package\n", Code: 1},
			[]any{}, ""},
	}
	for _, tc := range cases {
		got, err := parseRpmOwner(tc.path, tc.res)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err = %v, want %q", tc.path, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.path, err)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("%s: owners = %#v, want %#v", tc.path, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: owners = %#v, want %#v", tc.path, got, tc.want)
			}
		}
	}
}

// rpmVerifyCaptured is `rpm -V which coreutils-common bash nosuchpkg`
// with /usr/share/doc/which/AUTHORS chmod'ed 600, /usr/share/doc/which/NEWS
// moved away and a comment appended to /etc/DIR_COLORS.lightbgcolor, all
// put back afterwards. Byte-identical on rpm 4.14.3 (AlmaLinux 8.10) and
// 4.16.1.3 (Rocky 9.8), exit status 4 on both. /etc/skel/.bashrc was
// already modified on both images by whoever built them.
const rpmVerifyCaptured = ".M.......  d /usr/share/doc/which/AUTHORS\nmissing   d /usr/share/doc/which/NEWS\nS.5....T.  c /etc/DIR_COLORS.lightbgcolor\nS.5....T.  c /etc/skel/.bashrc\npackage nosuchpkg is not installed\n"

func TestParseRpmVerify(t *testing.T) {
	got, missing := parseRpmVerify(rpmVerifyCaptured)
	if len(missing) != 1 || missing[0] != "nosuchpkg" {
		t.Errorf("missing = %v", missing)
	}
	if got.Len() != 4 {
		t.Fatalf("got %d paths, want 4: %v", got.Len(), got.StringKeys())
	}
	check := func(path, typ string, isMissing bool, failed ...string) {
		t.Helper()
		raw, ok := got.Get(path)
		if !ok {
			t.Fatalf("%s absent", path)
		}
		m := raw.(*value.Map)
		if v, _ := m.Get("type"); v != typ {
			t.Errorf("%s type = %#v, want %q", path, v, typ)
		}
		if v, _ := m.Get("missing"); v != isMissing {
			t.Errorf("%s missing = %#v", path, v)
		}
		f, _ := m.Get("failed")
		list, _ := f.([]any)
		if len(list) != len(failed) {
			t.Fatalf("%s failed = %#v, want %v", path, f, failed)
		}
		for i := range failed {
			if list[i] != failed[i] {
				t.Errorf("%s failed = %#v, want %v", path, f, failed)
			}
		}
	}
	check("/usr/share/doc/which/AUTHORS", "doc", false, "mode")
	check("/usr/share/doc/which/NEWS", "doc", true)
	check("/etc/DIR_COLORS.lightbgcolor", "config", false, "size", "digest", "mtime")
}

func TestParseRpmVerifySkipsLinesThatAreNotResults(t *testing.T) {
	got, _ := parseRpmVerify("Unsatisfied dependencies for x-1-1.noarch:\n\tfoo is needed by x-1-1.noarch\nS.5....T.    /opt/has space/f\n")
	if got.Len() != 1 || !got.Has("/opt/has space/f") {
		t.Errorf("got %v", got.StringKeys())
	}
}

// Every rpm call must ask for its exit code: rpm -q exits 1 for a missing
// name and rpm -V exits non-zero because it found something, and the
// fake runner cannot show what exec.OSRunner does when the flag is absent
// (DIVERGENCE 5.113). So the property is asserted directly.
func TestRpmCallsAskForTheirExitCode(t *testing.T) {
	runner := &exec.RecordingRunner{}
	c := &exec.Context{Runner: runner, Lookup: func(name string) string { return "/usr/bin/" + name }}
	_, _ = rpmListPkgs(c, nil)
	_, _ = rpmInfo(c, []string{"bash"})
	_, _ = rpmFileDict(c, []string{"bash"})
	_, _ = rpmOwner(c, []string{"/etc/hosts"})
	_, _ = rpmVerify(c, []string{"bash"})
	if len(runner.Ran) != 5 {
		t.Fatalf("ran %d commands, want 5", len(runner.Ran))
	}
	for _, cmd := range runner.Ran {
		if !cmd.IgnoreExitCode {
			t.Errorf("%s does not set IgnoreExitCode", cmd.String())
		}
	}
}

func TestRpmRefusesByTheToolsName(t *testing.T) {
	c := &exec.Context{Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "" }}
	_, err := rpmListPkgs(c, nil)
	if err == nil || !strings.Contains(err.Error(), "rpm is not installed") {
		t.Errorf("err = %v", err)
	}
}

func TestRpmVersionCmpIsCompareRPM(t *testing.T) {
	r := New()
	got, err := r.Exec.Call(&exec.Context{}, "rpm.version_cmp", value.MapOf("ver1", "1.0~rc1", "ver2", "1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if got != int64(-1) {
		t.Errorf("1.0~rc1 vs 1.0 = %#v, want -1", got)
	}
}

// pkg.owner on the RedHat and SUSE families is `rpm -qf`, and it used to
// ask with `--queryformat %{NAME}` -- no newline in the format. rpm prints
// the format once per owner, so for a path two packages own, Rocky 9.8's
// rpm 4.16.1.3 answered `filesystembinutils` for /usr/share/man/man1:
// two names run together into one that does not exist (DIVERGENCE 5.172).
// Both answers below are what Rocky 9.8 printed, each under the argv that
// produced it, so the test fails the way the provider did if it goes back
// to the old argv, and fails with no answer at all if it drifts to a third
// argv nobody captured.
func TestRpmPkgOwnerOfASharedPathIsOneRealPackage(t *testing.T) {
	oldArgv := exec.Command{Argv: []string{"rpm", "-qf", "--queryformat", "%{NAME}", "/usr/share/man/man1"}}
	newArgv := exec.Command{Argv: []string{"rpm", "-qf", "--queryformat", "%{NAME}\\n", "--", "/usr/share/man/man1"}}
	for _, p := range []pkgOwner{dnfProvider{binary: "dnf"}, zypperProvider{}} {
		runner := &exec.RecordingRunner{
			Responses: map[string]exec.Result{
				oldArgv.String(): {Stdout: "filesystembinutils"},
				newArgv.String(): {Stdout: "filesystem\nbinutils\n"},
			},
			Default: exec.Result{Code: 99, Stderr: "no captured answer for this argv"},
		}
		c := &exec.Context{Runner: runner, Lookup: func(name string) string { return "/usr/bin/" + name }}
		got, err := p.OwnerOf(c, "/usr/share/man/man1")
		if err != nil {
			t.Fatalf("%T: %v", p, err)
		}
		// The first owner rpm names, as the apt provider takes the first
		// package dpkg names; rpm.owner is the call that lists them all.
		if got != "filesystem" {
			t.Errorf("%T: owner = %q, want filesystem (ran %v)", p, got, runner.RanCommands())
		}
	}
}

// A path nobody owns, and a path that does not exist, are both the empty
// string from pkg.owner, as they are from the apt and pkgng providers:
// that is pkg.owner's documented contract. rpm.owner, which speaks rpm's
// own vocabulary, keeps the difference. Both answers are Rocky 9.8's.
func TestRpmPkgOwnerOfAnUnownedOrAbsentPathIsEmpty(t *testing.T) {
	for path, res := range map[string]exec.Result{
		"/root/rpmchattr-capture": {Stdout: "file /root/rpmchattr-capture is not owned by any package\n", Code: 1},
		"/nonexist":               {Stderr: "error: file /nonexist: No such file or directory\n", Code: 1},
	} {
		argv := exec.Command{Argv: []string{"rpm", "-qf", "--queryformat", "%{NAME}\\n", "--", path}}
		c := proTestContext(map[string]exec.Result{argv.String(): res})
		got, err := dnfProvider{binary: "dnf"}.OwnerOf(c, path)
		if err != nil || got != "" {
			t.Errorf("%s: owner = %q, %v; want the empty string", path, got, err)
		}
	}
}
