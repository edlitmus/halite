package buildpolicy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every download CI unpacks and runs is checked against a pinned digest,
// and the pins agree with each other.
//
// The FreeBSD legs of ci.yml and fleet.yml fetched go.dev's tarball and
// untarred it with no checksum, while contrib/tofu's bootstrap checked
// the same file against a pinned sha256. The saltdiff image curled
// Salt's onedir bundle and executed it unchecked. DIVERGENCE 5.403.
//
// These are text checks, like the rest of this package, so what they
// hold is the shape: each fetch of the FreeBSD tarball sits in a step
// that names a digest and compares `sha256 -q` against it, and that
// digest is the one contrib/tofu pins for the version go.mod pins. Two
// copies of one digest are a coupling, and this is the guard for it --
// a toolchain bump that moves one and not the other fails here, saying
// which, instead of on an emulated VM as "checksum mismatch".
func TestTheFreeBSDToolchainDownloadIsVerified(t *testing.T) {
	root := repoRoot(t)
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	pin := regexp.MustCompile(`(?m)^toolchain (go1\.\d+(?:\.\d+)?)$`).FindStringSubmatch(string(mod))
	if pin == nil {
		t.Fatal("go.mod has no `toolchain` directive")
	}
	wantVersion := pin[1]

	vars, err := os.ReadFile(filepath.Join(root, "contrib", "tofu", "variables.tf"))
	if err != nil {
		t.Fatal(err)
	}
	tofuVersion := tfDefault(t, string(vars), "go_version")
	tofuSHA := tfDefault(t, string(vars), "go_sha256_freebsd")
	if "go"+tofuVersion != wantVersion {
		t.Errorf("contrib/tofu/variables.tf installs Go %s and go.mod pins %s; "+
			"its go_sha256 and go_sha256_freebsd are digests of the version it names",
			tofuVersion, wantVersion)
	}

	fetch := regexp.MustCompile(`(?m)^\s*fetch .*\.freebsd-amd64\.tar\.gz`)
	versionLine := regexp.MustCompile(`(?m)^\s*want_version=(\S+)\s*$`)
	shaLine := regexp.MustCompile(`(?m)^\s*want_sha256=([0-9a-f]{64})\s*$`)
	check := regexp.MustCompile(`(?m)^\s*got_sha256=\$\(sha256 -q /tmp/go\.tar\.gz\)\s*$` +
		`(?:\n.*){0,1}\n\s*if \[ "\$got_sha256" != "\$want_sha256" \]; then\s*$` +
		`(?:\n.*){0,1}\n\s*exit 1\s*$`)

	fetches := 0
	for _, name := range []string{"ci.yml", "fleet.yml"} {
		rel := filepath.Join(".github", "workflows", name)
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		// One step at a time, so a check in one job cannot vouch for a
		// fetch in another.
		for _, step := range splitSteps(string(b)) {
			if !fetch.MatchString(step) {
				continue
			}
			fetches++
			v := versionLine.FindStringSubmatch(step)
			s := shaLine.FindStringSubmatch(step)
			switch {
			case v == nil || s == nil:
				t.Errorf("%s fetches the FreeBSD Go tarball in a step that pins no "+
					"want_version and want_sha256; it untars whatever go.dev answers", rel)
				continue
			case !check.MatchString(step):
				t.Errorf("%s pins a digest for the FreeBSD Go tarball and never "+
					"compares `sha256 -q` against it before unpacking", rel)
			}
			if v[1] != wantVersion {
				t.Errorf("%s has the digest of %s and go.mod pins %s", rel, v[1], wantVersion)
			}
			if s[1] != tofuSHA {
				t.Errorf("%s pins %s for the FreeBSD tarball and contrib/tofu pins %s "+
					"for the same file; one of them is wrong", rel, s[1], tofuSHA)
			}
			if strings.Index(step, "got_sha256=") > strings.Index(step, "tar -C") {
				t.Errorf("%s unpacks the FreeBSD Go tarball before checking it", rel)
			}
		}
	}
	if fetches < 2 {
		t.Fatalf("found %d steps fetching the FreeBSD Go tarball, want ci.yml's and "+
			"fleet.yml's; this guard has stopped checking anything", fetches)
	}
	t.Logf("%d steps fetch %s.freebsd-amd64.tar.gz and check it against %s", fetches, wantVersion, tofuSHA)
}

// The saltdiff image refuses a Salt release it has no digest for, so
// every release CI runs, and the Makefile's default, must have one --
// otherwise the gate fails on a refusal rather than on a difference.
func TestTheSaltdiffImagePinsEveryReleaseItRuns(t *testing.T) {
	root := repoRoot(t)
	df, err := os.ReadFile(filepath.Join(root, "contrib", "docker", "saltdiff", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(df)
	pinned := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s*([0-9.]+)-x86_64\)\s+sha=[0-9a-f]{64} ;;`).FindAllStringSubmatch(body, -1) {
		pinned[m[1]] = true
	}
	if len(pinned) == 0 {
		t.Fatal("contrib/docker/saltdiff/Dockerfile pins no onedir digest; this guard has stopped checking anything")
	}
	if !regexp.MustCompile(`(?m)echo "\$\{sha\}  /tmp/salt\.tar\.xz" \| sha256sum -c -`).MatchString(body) {
		t.Error("contrib/docker/saltdiff/Dockerfile pins digests and never checks the bundle against one")
	}
	if !regexp.MustCompile(`(?m)^\s*\*\) echo .*exit 1 ;;`).MatchString(body) {
		t.Error("contrib/docker/saltdiff/Dockerfile has no refusal for a release it has no digest " +
			"for, so one would be fetched and run unchecked")
	}
	if strings.Index(body, "sha256sum -c") > strings.Index(body, "tar -C /opt") {
		t.Error("contrib/docker/saltdiff/Dockerfile unpacks the bundle before checking it")
	}

	var want []string
	ci, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	matrix := regexp.MustCompile(`(?m)^\s*salt: \[([^\]]*)\]`).FindStringSubmatch(string(ci))
	if matrix == nil {
		t.Fatal("ci.yml has no saltdiff `salt:` matrix; this guard is reading the wrong file")
	}
	for _, v := range strings.Split(matrix[1], ",") {
		want = append(want, strings.Trim(strings.TrimSpace(v), `'"`))
	}
	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	def := regexp.MustCompile(`(?m)^SALT_VERSION \?= (\S+)$`).FindStringSubmatch(string(mk))
	if def == nil {
		t.Fatal("the Makefile sets no default SALT_VERSION; this guard is reading the wrong file")
	}
	want = append(want, def[1])
	for _, v := range want {
		if !pinned[v] {
			t.Errorf("Salt %s is run by CI or the Makefile and contrib/docker/saltdiff/Dockerfile "+
				"has no digest for it, so the image build refuses it", v)
		}
	}
	t.Logf("saltdiff pins %d releases; CI and the Makefile run %v", len(pinned), want)
}

// tfDefault reads a variable's `default` out of a variables.tf, which is
// all the HCL this needs to understand.
func tfDefault(t *testing.T, body, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)variable "` + regexp.QuoteMeta(name) + `" \{.*?\n\s*default\s*=\s*"([^"]*)"`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("contrib/tofu/variables.tf has no default for %s", name)
	}
	return m[1]
}

// splitSteps cuts a workflow into its steps at each `- name:` or
// `- uses:` line. It is not YAML, and does not need to be: the steps in
// question are flat lists under `steps:`.
func splitSteps(body string) []string {
	start := regexp.MustCompile(`(?m)^\s*- (?:name|uses|id|run):`)
	idx := start.FindAllStringIndex(body, -1)
	var out []string
	for i, at := range idx {
		end := len(body)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		out = append(out, body[at[0]:end])
	}
	return out
}
