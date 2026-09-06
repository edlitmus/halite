package buildpolicy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// SPEC 27.1's support tiers are a promise about compilation, and
// `build-all` is the only thing that can keep it.
//
// Tier 3 says "compiles and is published" of OpenBSD, NetBSD, Solaris
// and illumos, and of Linux on riscv64, ppc64le and s390x. Nothing
// compiled for any of them. When somebody finally did, four of the nine
// failed — OpenBSD has no RLIMIT_AS, and Solaris and illumos have no
// RLIMIT_NPROC, so internal/bridge did not build there and had not for
// as long as the sandbox has existed. The specification had been making
// a claim that no part of the build tested, which is the exact shape
// this package exists to catch.
//
// So the tier table and the Makefile's target list are checked against
// each other. The mapping from a tier's prose to GOOS/GOARCH lives
// here, in the test, because it is a judgement — "Ubuntu 24.04 on
// arm64" is linux/arm64 and a reader knows it, but no parser does.
//
// The platform cell of each row is matched in full rather than
// searched. Any edit to that table — a platform added, dropped, or
// renamed — fails this test and puts the change in front of somebody
// who has to decide what it means for the build, which is the point.
// A row that quietly grew a platform nothing compiles for is how the
// tier 3 claim came to be false in the first place.
type tierRow struct {
	tier      string
	platforms string
	targets   []string
}

var supportTiers = []tierRow{
	{
		tier: "1",
		platforms: "Ubuntu 22.04, 24.04, 26.04; Debian 12, 13; RHEL, Rocky, " +
			"Alma 8 and 9; Amazon Linux 2023; Windows Server 2019, 2022, 2025; " +
			"all on amd64 and arm64",
		targets: []string{"linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64"},
	},
	{
		tier:      "2",
		platforms: "SUSE 15, Alpine 3.19 and later, macOS 14 and later, FreeBSD 14",
		// SUSE and Alpine are linux, already covered by tier 1's row.
		targets: []string{
			"linux/amd64", "linux/arm64",
			"darwin/amd64", "darwin/arm64",
			"freebsd/amd64", "freebsd/arm64",
		},
	},
	{
		tier:      "3",
		platforms: "OpenBSD, NetBSD, Solaris and illumos, Linux on riscv64, ppc64le, s390x",
		// The BSDs get the two architectures the other tiers get.
		// Solaris and illumos have one port each and Go offers no
		// other, so naming more would be naming something that does
		// not exist.
		targets: []string{
			"openbsd/amd64", "openbsd/arm64",
			"netbsd/amd64", "netbsd/arm64",
			"solaris/amd64", "illumos/amd64",
			"linux/riscv64", "linux/ppc64le", "linux/s390x",
		},
	},
}

func TestEveryPlatformTheSpecTiersIsBuilt(t *testing.T) {
	root := repoRoot(t)
	spec, err := os.ReadFile(filepath.Join(root, "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}
	rows := tierTable(t, string(spec))
	if len(rows) != len(supportTiers) {
		t.Fatalf("SPEC 27.1 has %d tiers and this test knows %d. A tier was "+
			"added or removed and nothing decided what it means for the build.",
			len(rows), len(supportTiers))
	}

	built := makefileTargets(t, root)
	for _, want := range supportTiers {
		got, ok := rows[want.tier]
		if !ok {
			t.Errorf("SPEC 27.1 no longer has a tier %s row", want.tier)
			continue
		}
		if got != want.platforms {
			t.Errorf("SPEC 27.1's tier %s platforms have changed and the build "+
				"targets have not been reconsidered.\n  spec: %s\n  this test: %s",
				want.tier, got, want.platforms)
		}
		for _, target := range want.targets {
			if !built[target] {
				t.Errorf("SPEC 27.1 puts %s in tier %s, and the Makefile's TARGETS "+
					"does not build it. A tier is a promise that the target "+
					"compiles, and nothing here checks a target that is not built.",
					target, want.tier)
			}
		}
	}

	// And the reverse: a target in the Makefile that no tier covers is
	// either a platform the specification forgot or one the build should
	// stop paying for. Either way somebody decides rather than nobody.
	promised := map[string]bool{}
	for _, row := range supportTiers {
		for _, target := range row.targets {
			promised[target] = true
		}
	}
	for target := range built {
		if !promised[target] {
			t.Errorf("the Makefile builds %s and SPEC 27.1 puts it in no tier: "+
				"either the specification is missing a platform or the build is "+
				"carrying one nobody supports", target)
		}
	}
}

// tierTable reads section 27.1's table into tier number and platform cell.
func tierTable(t *testing.T, spec string) map[string]string {
	t.Helper()
	start := strings.Index(spec, "### 27.1 Support tiers")
	if start < 0 {
		t.Fatal("SPEC has no section 27.1; this check is reading the wrong file")
	}
	section := spec[start:]
	if end := strings.Index(section, "\n### "); end > 0 {
		section = section[:end]
	}
	rows := map[string]string{}
	for _, line := range strings.Split(section, "\n") {
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) != 3 {
			continue
		}
		tier := strings.TrimSpace(cells[0])
		if !regexp.MustCompile(`^\d+$`).MatchString(tier) {
			continue
		}
		rows[tier] = strings.TrimSpace(cells[1])
	}
	if len(rows) == 0 {
		t.Fatal("no tier rows were read from SPEC 27.1; the table's shape has changed")
	}
	return rows
}

// makefileTargets reads TARGETS, following the variables it is built
// from, so that grouping the list by tier does not blind this check.
func makefileTargets(t *testing.T, root string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	// Continuations are folded first, so a list split across several
	// lines reads as the one assignment it is.
	var lines []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		if n := len(lines); n > 0 && strings.HasSuffix(lines[n-1], `\`) {
			lines[n-1] = strings.TrimSuffix(lines[n-1], `\`) + " " + strings.TrimSpace(line)
			continue
		}
		lines = append(lines, line)
	}
	text := strings.Join(lines, "\n")
	assign := regexp.MustCompile(`(?m)^([A-Z0-9_]+)\s*=\s*(.*)$`)
	values := map[string]string{}
	for _, m := range assign.FindAllStringSubmatch(text, -1) {
		values[m[1]] = m[2]
	}
	raw, ok := values["TARGETS"]
	if !ok {
		t.Fatal("the Makefile has no TARGETS; this check is reading the wrong file")
	}
	// One round of expansion is all this list uses.
	raw = regexp.MustCompile(`\$\(([A-Z0-9_]+)\)`).ReplaceAllStringFunc(raw, func(ref string) string {
		return values[strings.Trim(ref, "$()")]
	})
	out := map[string]bool{}
	for _, field := range strings.Fields(raw) {
		if strings.Contains(field, "/") {
			out[field] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("TARGETS expanded to nothing; this check has stopped checking anything")
	}
	return out
}
