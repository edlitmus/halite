package buildpolicy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sixtyFourBit is every GOARCH whose int is 64 bits. An architecture not
// named here fails the test below until somebody decides which it is.
var sixtyFourBit = map[string]bool{
	"amd64": true, "arm64": true, "riscv64": true, "ppc64": true, "ppc64le": true,
	"s390x": true, "loong64": true, "mips64": true, "mips64le": true,
}

// Every target the release builds has a 64-bit int.
//
// The tree converts int64 to int without a bound in many places, each
// harmless where int is 64 bits: every `int(cfg.Int(...))` in the three
// commands, and the template evaluator's integers. Code scanning flagged
// 23 of them (go/incorrect-integer-conversion, 2026-11-20). On a 32-bit
// target each would truncate silently -- a template index of 2^32+1 would
// read element 1, measured on GOARCH=386 -- so rather than bound every
// site, a 32-bit target is refused here and the two packages the
// conversions run through refuse to compile on one (internal/config and
// internal/template, int64.go). Adding a 32-bit platform means removing
// both guards and bounding the conversions first. DIVERGENCE 5.690.
func TestEveryReleaseTargetHasA64BitInt(t *testing.T) {
	root := repoRoot(t)
	targets := makefileTargets(t, root)
	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`(?m)^FIPS_TARGETS\s*=\s*(.+)$`).FindSubmatch(mk); m != nil {
		for _, target := range strings.Fields(string(m[1])) {
			targets[target] = true
		}
	}
	if len(targets) == 0 {
		t.Fatal("no targets read from the Makefile; this check has stopped checking anything")
	}
	for target := range targets {
		_, arch, ok := strings.Cut(target, "/")
		if !ok {
			t.Errorf("target %q is not os/arch", target)
			continue
		}
		if !sixtyFourBit[arch] {
			t.Errorf("the release builds %s, and %s is not a 64-bit architecture this test knows: "+
				"int64-to-int conversions across the tree assume a 64-bit int (see this test's comment)",
				target, arch)
		}
	}
}
