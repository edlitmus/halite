package buildpolicy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The lab refuses an SSH allow-list that is the whole internet, however it
// is spelled.
//
// variables.tf refused the single string "0.0.0.0/0" and nothing else, so
// "0.0.0.0/1" with "128.0.0.0/1", "::/0" and "1.2.3.4/0" each opened key-only
// root SSH on a machine that runs this tree as root, to everybody (issue
// 321, section 5; DIVERGENCE 5.485). The rule is now the prefix length.
//
// This runs `tofu plan` against the real `variable "allowed_ssh_cidrs"`
// block, cut out of variables.tf, in an empty directory: validation is
// evaluated before any provider is asked anything, so it needs no
// credentials and creates nothing. It skips, with the reason, where there is
// no `tofu` on the PATH; no workflow here installs it, so the run recorded in
// 5.485 is the evidence, and CI may well skip it.
func TestLabSSHCIDRValidationRefusesWideAndMalformedEntries(t *testing.T) {
	tofu, err := exec.LookPath("tofu")
	if err != nil {
		t.Skip("no tofu on the PATH: this runs `tofu plan` against the lab's variable block")
	}
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "contrib", "tofu", "variables.tf"))
	if err != nil {
		t.Fatal(err)
	}
	block := variableBlock(t, string(src), "allowed_ssh_cidrs")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(block+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		cidrs  string
		refuse bool
	}{
		// What `make lab-up` and `make lab-down` pass, and ordinary networks.
		{"one host", `["203.0.113.1/32"]`, false},
		{"an office /24 and a /8", `["198.51.100.0/24","10.0.0.0/8"]`, false},
		{"an IPv6 /32", `["2001:db8::/32"]`, false},
		// The spelling the old check caught, and the ones it did not.
		{"0.0.0.0/0", `["0.0.0.0/0"]`, true},
		{"two halves", `["0.0.0.0/1","128.0.0.0/1"]`, true},
		{"IPv6 everything", `["::/0"]`, true},
		{"a /0 with a base address", `["1.2.3.4/0"]`, true},
		{"wider than /8", `["10.0.0.0/7"]`, true},
		{"IPv6 wider than /32", `["2001:db8::/16"]`, true},
		{"not a CIDR", `["nonsense"]`, true},
		{"a host with no prefix", `["203.0.113.1"]`, true},
		{"empty", `[]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(tofu, "plan", "-input=false", "-no-color", "-var", "allowed_ssh_cidrs="+tc.cidrs)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			refused := err != nil && strings.Contains(string(out), "Invalid value for variable")
			if err != nil && !refused {
				t.Fatalf("tofu plan failed for another reason: %v\n%s", err, out)
			}
			if refused != tc.refuse {
				t.Errorf("allowed_ssh_cidrs=%s: refused = %v, want %v\n%s", tc.cidrs, refused, tc.refuse, out)
			}
		})
	}
}

// variableBlock returns the text of `variable "name" { ... }`, braces
// balanced, with the leading comments left out.
func variableBlock(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, `variable "`+name+`"`)
	if start < 0 {
		t.Fatalf("variables.tf has no variable %q", name)
	}
	depth := 0
	for i := strings.Index(src[start:], "{") + start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	t.Fatalf("variable %q has unbalanced braces", name)
	return ""
}
