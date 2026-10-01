package builtin

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The fixtures under testdata/kernelpkg are transcripts, not files cut
// out of one: each records the argument vector run on a lab host, its
// exit status, and its standard output and error, byte for byte, as a
// tiny program that ran argv exactly printed them. A transcript keeps
// the exit status with the output, which is half of what the
// IgnoreExitCode trap (DIVERGENCE 5.113) is about.
//
// debian13/before.txt is the Debian 13.7 lab host on 2026-09-30, running
// 6.12.107+deb13-amd64 with 6.12.111+deb13-amd64 installed beside it by
// an unattended upgrade and not yet booted.

// loadKernelTranscript reads a transcript into scripted responses.
func loadKernelTranscript(t *testing.T, path string) map[string]exec.Result {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]exec.Result{}
	blocks := strings.Split(string(body), "@@@ end\n")
	for _, b := range blocks {
		if strings.TrimSpace(b) == "" {
			continue
		}
		header, rest, _ := strings.Cut(b, "\n")
		argv := strings.Split(strings.TrimPrefix(header, "@@@ "), "\t")
		codeLine, rest, _ := strings.Cut(rest, "\n")
		code, err := strconv.Atoi(strings.TrimPrefix(codeLine, "@@@ exit "))
		if err != nil {
			t.Fatalf("%s: %q is not an exit line", path, codeLine)
		}
		rest = strings.TrimPrefix(rest, "@@@ stdout\n")
		stdout, stderr, ok := strings.Cut(rest, "@@@ stderr\n")
		if !ok {
			t.Fatalf("%s: a block for %v has no stderr marker", path, argv)
		}
		out[exec.Command{Argv: argv}.String()] = exec.Result{Code: code, Stdout: stdout, Stderr: stderr}
	}
	return out
}

// kernelCtx is a Debian node answering from a transcript. Its release is
// the transcript's own `uname -r`.
func kernelCtx(t *testing.T, transcript string, test bool) (*exec.Context, *exec.RecordingRunner) {
	t.Helper()
	responses := loadKernelTranscript(t, filepath.Join("testdata", "kernelpkg", transcript))
	release := strings.TrimSpace(responses[exec.Command{Argv: []string{"uname", "-r"}}.String()].Stdout)
	if release == "" {
		t.Fatalf("%s has no uname -r", transcript)
	}
	rec := &exec.RecordingRunner{Responses: responses, Default: exec.Result{Code: 99, Stderr: "not in the transcript"}}
	c := &exec.Context{
		Ctx:    context.Background(),
		Grains: value.MapOf("os_family", "Debian", "kernelrelease", release),
		Test:   test,
		Runner: rec,
		Lookup: func(name string) string {
			switch name {
			case "dpkg-query", "apt-get", "apt-cache":
				return "/usr/bin/" + name
			}
			return ""
		},
	}
	return c, rec
}

func callKernel(t *testing.T, c *exec.Context, fn string, args *value.Map) (any, error) {
	t.Helper()
	if args == nil {
		args = value.NewMap(0)
	}
	// The functions directly rather than through the registry, which
	// refuses them off Linux; that refusal is TestLiveKernelpkg*'s.
	for _, m := range kernelpkgExecModules() {
		if m.Sig.Function == fn {
			return m.Fn(c, args)
		}
	}
	t.Fatalf("kernelpkg has no function %s", fn)
	return nil, nil
}

func TestKernelpkgReadsDebian13sKernels(t *testing.T) {
	c, _ := kernelCtx(t, "debian13/before.txt", false)
	want := map[string]any{
		"active":           "6.12.107+deb13-amd64",
		"list_installed":   []any{"6.12.107+deb13-amd64", "6.12.111+deb13-amd64"},
		"latest_installed": "6.12.111+deb13-amd64",
		// The image linux-image-amd64's candidate depends on, which no
		// pattern over the metapackage's own version 6.12.111-1 recovers.
		"latest_available":  "6.12.111+deb13-amd64",
		"needs_reboot":      true,
		"upgrade_available": false,
	}
	for fn, w := range want {
		got, err := callKernel(t, c, fn, nil)
		if err != nil {
			t.Errorf("%s: %v", fn, err)
			continue
		}
		if !reflect.DeepEqual(got, w) {
			t.Errorf("%s = %#v, want %#v", fn, got, w)
		}
	}
}

// The metapackages, the debug symbols and the unsigned twins are all
// linux-image-*, and only kernels of the running flavour count.
func TestKernelpkgReadsReleasesOutOfPackageNames(t *testing.T) {
	cases := []struct {
		pkg, flavour, release string
		ok                    bool
	}{
		{"linux-image-6.12.107+deb13-amd64", "amd64", "6.12.107+deb13-amd64", true},
		{"linux-image-6.12.107+deb13-amd64-unsigned", "amd64", "6.12.107+deb13-amd64", true},
		{"linux-image-6.12.90+deb13.1-amd64", "amd64", "6.12.90+deb13.1-amd64", true},
		{"linux-image-6.12.107+deb13-cloud-amd64", "amd64", "", false},
		{"linux-image-6.12.107+deb13-cloud-amd64", "cloud-amd64", "6.12.107+deb13-cloud-amd64", true},
		{"linux-image-6.12.107+deb13-amd64-dbg", "amd64", "", false},
		{"linux-image-amd64", "amd64", "", false},
		{"linux-image-generic", "generic", "", false},
		// Debian 12's and Ubuntu's shapes, from their package names: not
		// measured on a host here.
		{"linux-image-6.1.0-28-cloud-amd64", "cloud-amd64", "6.1.0-28-cloud-amd64", true},
		{"linux-image-5.15.0-91-generic", "generic", "5.15.0-91-generic", true},
	}
	for _, tc := range cases {
		got, ok := debianImageRelease(tc.pkg, tc.flavour)
		if ok != tc.ok || got != tc.release {
			t.Errorf("debianImageRelease(%q, %q) = %q, %v; want %q, %v", tc.pkg, tc.flavour, got, ok, tc.release, tc.ok)
		}
	}
}

// On Debian 13 purging the signed image alone installs its unsigned twin:
// the kernel stays, under another name. Measured with apt-get -s.
func TestKernelpkgRefusesARemovalThatInstallsTheTwin(t *testing.T) {
	responses := loadKernelTranscript(t, filepath.Join("testdata", "kernelpkg", "debian13", "before.txt"))
	sim := responses[exec.Command{Argv: []string{"apt-get", "-s", "purge", "linux-image-6.12.111+deb13-amd64"}}.String()]
	if sim.Stdout == "" {
		t.Fatal("the transcript lost the signed-only simulation")
	}
	_, err := checkDebianRemoval(sim.Stdout, "6.12.111+deb13-amd64")
	if err == nil || !strings.Contains(err.Error(), "linux-image-6.12.111+deb13-amd64-unsigned") {
		t.Fatalf("a removal apt would answer by installing the twin was not refused: %v", err)
	}
}

// Purging the image the metapackage depends on removes the metapackage,
// and with it every later kernel.
func TestKernelpkgRefusesToRemoveTheMetapackagesKernel(t *testing.T) {
	c, rec := kernelCtx(t, "debian13/before.txt", false)
	_, err := callKernel(t, c, "remove", value.MapOf("release", "6.12.111+deb13-amd64"))
	if err == nil || !strings.Contains(err.Error(), "linux-image-amd64") || !strings.Contains(err.Error(), "linux-headers-amd64") {
		t.Fatalf("removing the metapackage's kernel: %v", err)
	}
	for _, ran := range rec.RanCommands() {
		if strings.HasPrefix(ran, "apt-get purge") {
			t.Errorf("a refused removal still ran %q", ran)
		}
	}
}

func TestKernelpkgRefusesToRemoveTheRunningKernel(t *testing.T) {
	c, rec := kernelCtx(t, "debian13/before.txt", false)
	_, err := callKernel(t, c, "remove", value.MapOf("release", "6.12.107+deb13-amd64"))
	if err == nil || !strings.Contains(err.Error(), "running kernel") {
		t.Fatalf("removing the running kernel: %v", err)
	}
	for _, ran := range rec.RanCommands() {
		if strings.HasPrefix(ran, "apt-get") {
			t.Errorf("the running kernel's removal reached apt: %q", ran)
		}
	}
}

func TestKernelpkgRefusesAKernelThatIsNotInstalled(t *testing.T) {
	c, _ := kernelCtx(t, "debian13/before.txt", false)
	_, err := callKernel(t, c, "remove", value.MapOf("release", "6.12.105+deb13-amd64"))
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("removing a kernel that is not installed: %v", err)
	}
}

// keep_latest=false would remove the metapackage's kernel, which is
// refused, and the refusal comes before anything is removed.
func TestKernelpkgCleanupPlansEveryRemovalFirst(t *testing.T) {
	c, rec := kernelCtx(t, "debian13/before.txt", false)
	_, err := callKernel(t, c, "cleanup", value.MapOf("keep_latest", false))
	if err == nil || !strings.Contains(err.Error(), "nothing was removed") {
		t.Fatalf("cleanup keep_latest=false: %v", err)
	}
	for _, ran := range rec.RanCommands() {
		if strings.HasPrefix(ran, "apt-get purge") {
			t.Errorf("cleanup removed something before its plan was refused: %q", ran)
		}
	}
	// With the newest kept there is nothing to remove: the other kernel is
	// the running one.
	got, err := callKernel(t, c, "cleanup", nil)
	if err != nil {
		t.Fatal(err)
	}
	if removed, _ := got.(*value.Map).Get("removed"); len(removed.([]any)) != 0 {
		t.Errorf("cleanup with the newest kept removed %v", removed)
	}
}

// A metapackage no repository knows leaves latest_available at the newest
// installed, as Salt's does, rather than failing.
func TestKernelpkgFallsBackWhenNoMetapackageIsKnown(t *testing.T) {
	c, rec := kernelCtx(t, "debian13/before.txt", false)
	unknown := rec.Responses[exec.Command{Argv: []string{"apt-cache", "show", "--no-all-versions", "linux-image-nosuch-flavour"}}.String()]
	rec.Responses[exec.Command{Argv: []string{"apt-cache", "show", "--no-all-versions", "linux-image-amd64"}}.String()] = unknown
	got, err := callKernel(t, c, "latest_available", nil)
	if err != nil || got != "6.12.111+deb13-amd64" {
		t.Fatalf("latest_available with no metapackage = %v, %v", got, err)
	}
}

func TestKernelpkgListsNothingWhenNoImageIsInstalled(t *testing.T) {
	c, rec := kernelCtx(t, "debian13/before.txt", false)
	none := rec.Responses[exec.Command{Argv: []string{"dpkg-query", "-W", "-f=${Package}\\n", "linux-image-nosuch*"}}.String()]
	rec.Responses[exec.Command{Argv: []string{"dpkg-query", "-W", "-f=${Package}\\t${Version}\\t${Status}\\n", "linux-image-*"}}.String()] = none
	got, err := callKernel(t, c, "list_installed", nil)
	if err != nil || len(got.([]any)) != 0 {
		t.Fatalf("list_installed with no image = %v, %v", got, err)
	}
	// And needs_reboot says it cannot tell rather than false: this is what
	// a container looks like.
	if _, err := callKernel(t, c, "needs_reboot", nil); err == nil || !strings.Contains(err.Error(), "container") {
		t.Fatalf("needs_reboot with the running kernel not a package: %v", err)
	}
}

// Every command whose exit status this module reads must ask for it, or a
// real runner turns the status into an error first (DIVERGENCE 5.113).
// The fake cannot show that, so the property is asserted instead.
func TestKernelpkgAsksForTheExitCodesItReads(t *testing.T) {
	c, rec := kernelCtx(t, "debian13/before.txt", false)
	for _, fn := range []string{"list_installed", "latest_available", "upgrade_available"} {
		if _, err := callKernel(t, c, fn, nil); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = callKernel(t, c, "remove", value.MapOf("release", "6.12.111+deb13-amd64"))
	reads := map[string]bool{"dpkg-query": false, "apt-cache": false, "apt-get -s": false}
	for _, cmd := range rec.Ran {
		s := cmd.String()
		for prefix := range reads {
			if strings.HasPrefix(s, prefix) {
				reads[prefix] = true
				if !cmd.IgnoreExitCode {
					t.Errorf("%q reads its exit status and does not set IgnoreExitCode", s)
				}
			}
		}
	}
	for prefix, seen := range reads {
		if !seen {
			t.Errorf("no %s command ran, so this test checked nothing about it", prefix)
		}
	}
}

func TestKernelpkgLatestInstalledIsConvergedWhenTheNewestIsInstalled(t *testing.T) {
	c, rec := kernelCtx(t, "debian13/before.txt", false)
	res, err := kernelpkgLatestInstalledState(c, value.MapOf("name", "k"))
	if err != nil || res.Result == nil || !*res.Result || res.HasChanges() {
		t.Fatalf("latest_installed with 6.12.111 installed: %+v, %v", res, err)
	}
	for _, ran := range rec.RanCommands() {
		if strings.HasPrefix(ran, "apt-get install") {
			t.Errorf("a converged state installed: %q", ran)
		}
	}
}

// With the newest image gone from the listing, test mode predicts the
// install and runs nothing that changes the node.
func TestKernelpkgLatestInstalledPredictsInTestMode(t *testing.T) {
	c, rec := kernelCtx(t, "debian13/before.txt", true)
	key := exec.Command{Argv: []string{"dpkg-query", "-W", "-f=${Package}\\t${Version}\\t${Status}\\n", "linux-image-*"}}.String()
	r := rec.Responses[key]
	r.Stdout = strings.Replace(r.Stdout, "linux-image-6.12.111+deb13-amd64\t6.12.111-1\tinstall ok installed",
		"linux-image-6.12.111+deb13-amd64\t\tunknown ok not-installed", 1)
	rec.Responses[key] = r
	res, err := kernelpkgLatestInstalledState(c, value.MapOf("name", "k"))
	if err != nil || res.Result != nil || !res.HasChanges() {
		t.Fatalf("test mode: %+v, %v", res, err)
	}
	if !strings.Contains(res.Comment, "6.12.111+deb13-amd64") {
		t.Errorf("the prediction does not name the kernel: %s", res.Comment)
	}
	for _, ran := range rec.RanCommands() {
		if strings.HasPrefix(ran, "apt-get") && !strings.HasPrefix(ran, "apt-get -s") {
			t.Errorf("test mode ran %q", ran)
		}
	}
}

func TestKernelpkgStripsTheArchitectureFromAnELRelease(t *testing.T) {
	for in, want := range map[string]string{
		"5.14.0-570.12.1.el9_6.x86_64": "5.14.0-570.12.1.el9_6",
		"4.18.0-553.el8_10.aarch64":    "4.18.0-553.el8_10",
		"6.12.107+deb13-amd64":         "6.12.107+deb13-amd64",
	} {
		if got := rpmStripArch(in); got != want {
			t.Errorf("rpmStripArch(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKernelpkgRefusesAPackageManagerSaltsDoesNotLoadOn(t *testing.T) {
	c := &exec.Context{
		Ctx:    context.Background(),
		Runner: &exec.RecordingRunner{},
		Lookup: func(name string) string {
			if name == "apk" {
				return "/sbin/apk"
			}
			return ""
		},
	}
	_, err := pickKernelFamily(c)
	if err == nil || !strings.Contains(err.Error(), "apkpkg") {
		t.Fatalf("an apk node: %v", err)
	}
}

// A node whose running kernel is no package it holds -- an EL container
// on the fleet's rpm legs, where this was an error after a successful
// install -- reports reboot_required as unknown, and refuses only a reboot
// it was asked for.
func TestKernelpkgUpgradeReportsAnUnknownRebootAsUnknown(t *testing.T) {
	c, rec := kernelCtx(t, "debian13/before.txt", false)
	key := exec.Command{Argv: []string{"dpkg-query", "-W", "-f=${Package}\\t${Version}\\t${Status}\\n", "linux-image-*"}}.String()
	r := rec.Responses[key]
	r.Stdout = strings.Replace(r.Stdout, "linux-image-6.12.107+deb13-amd64\t6.12.107-1\tinstall ok installed",
		"linux-image-6.12.107+deb13-amd64\t\tunknown ok not-installed", 1)
	rec.Responses[key] = r

	got, err := callKernel(t, c, "upgrade", nil)
	if err != nil {
		t.Fatalf("upgrade with the running kernel not a package: %v", err)
	}
	if v, ok := got.(*value.Map).Get("reboot_required"); !ok || v != nil {
		t.Errorf("reboot_required = %v, %v; want present and null", v, ok)
	}
	if _, err := callKernel(t, c, "upgrade", value.MapOf("reboot", true)); err == nil ||
		!strings.Contains(err.Error(), "no reboot was scheduled") {
		t.Errorf("upgrade reboot=true with the reboot unknowable: %v", err)
	}
}

// rpmKernelCtx is an EL container on the fleet's rpm legs, answering from
// what the leg logged: rpm and dnf are the distribution's, the kernel is
// the Ubuntu runner's, and no kernel package is installed.
func rpmKernelCtx(t *testing.T, transcript string) *exec.Context {
	t.Helper()
	responses := loadKernelTranscript(t, filepath.Join("testdata", "kernelpkg", transcript))
	return &exec.Context{
		Ctx:    context.Background(),
		Grains: value.MapOf("os_family", "RedHat", "kernelrelease", "6.17.0-1022-azure"),
		Runner: &exec.RecordingRunner{Responses: responses, Default: exec.Result{Code: 99, Stderr: "not in the transcript"}},
		Lookup: func(name string) string {
			if name == "rpm" || name == "dnf" {
				return "/usr/bin/" + name
			}
			return ""
		},
	}
}

func TestKernelpkgReadsAnELContainer(t *testing.T) {
	for transcript, newest := range map[string]string{
		"rocky9-container/fleet.txt": "5.14.0-687.53.1.el9_8",
		"alma8-container/fleet.txt":  "4.18.0-553.169.1.el8_10",
	} {
		c := rpmKernelCtx(t, transcript)
		if got, err := callKernel(t, c, "list_installed", nil); err != nil || len(got.([]any)) != 0 {
			t.Errorf("%s: list_installed = %v, %v; rpm said kernel-core is not installed", transcript, got, err)
		}
		// dnf prints the epoch as 0:, which no `uname -r` carries.
		if got, err := callKernel(t, c, "latest_available", nil); err != nil || got != newest {
			t.Errorf("%s: latest_available = %v, %v; want %s", transcript, got, err, newest)
		}
		if _, err := callKernel(t, c, "needs_reboot", nil); err == nil || !strings.Contains(err.Error(), "container") {
			t.Errorf("%s: needs_reboot in a container: %v", transcript, err)
		}
	}
}

// dnf's own transaction table, with --assumeno, names what removing
// kernel-core takes with it: `kernel`, `kernel-modules`, and on EL9
// `kernel-modules-core`, all of that version.
func TestKernelpkgReadsDnfsRemovalTable(t *testing.T) {
	cases := []struct {
		transcript, target, release string
		want                        []string
	}{
		{"rocky9-container/fleet.txt", "kernel-core-5.14.0-687.52.1.el9_8.x86_64", "5.14.0-687.52.1.el9_8", []string{
			"kernel-5.14.0-687.52.1.el9_8.x86_64", "kernel-core-5.14.0-687.52.1.el9_8.x86_64",
			"kernel-modules-5.14.0-687.52.1.el9_8.x86_64", "kernel-modules-core-5.14.0-687.52.1.el9_8.x86_64",
		}},
		{"alma8-container/fleet.txt", "kernel-core-4.18.0-553.168.1.el8_10.x86_64", "4.18.0-553.168.1.el8_10", []string{
			"kernel-4.18.0-553.168.1.el8_10.x86_64", "kernel-core-4.18.0-553.168.1.el8_10.x86_64",
			"kernel-modules-4.18.0-553.168.1.el8_10.x86_64",
		}},
	}
	for _, tc := range cases {
		responses := loadKernelTranscript(t, filepath.Join("testdata", "kernelpkg", tc.transcript))
		table := responses[exec.Command{Argv: []string{"dnf", "remove", "--assumeno", tc.target}}.String()]
		if table.Code != 1 || !strings.Contains(table.Stderr, "Operation aborted") {
			t.Fatalf("%s: the transcript's --assumeno is not dnf declining: %+v", tc.transcript, table)
		}
		got, err := checkRPMRemoval(table.Stdout, tc.release)
		if err != nil || strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("%s: checkRPMRemoval = %v, %v; want %v", tc.transcript, got, err, tc.want)
		}
		// The same table read for another kernel is every row foreign,
		// which is the refusal a row of another version gets.
		if _, err := checkRPMRemoval(table.Stdout, "5.14.0-1.el9"); err == nil ||
			!strings.Contains(err.Error(), "do not belong") {
			t.Errorf("%s: rows of another version were not refused: %v", tc.transcript, err)
		}
	}
}
