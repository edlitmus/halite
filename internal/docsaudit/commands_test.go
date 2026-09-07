package docsaudit

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every subcommand of every binary is in the command reference, or is
// declared here as deliberately absent with a reason.
//
// Nothing held that document to the code, and it showed: `doctor`
// shipped on both `halite-node` and `halite-hub` — SPEC 26.4's whole
// diagnostics command, the one an operator reaches for when something is
// wrong — and neither docs/command-reference.md nor docs/operations.md
// mentioned it. The generated pages are audited, the module tables are
// audited, the metric families are audited; the page a person actually
// reads to find out what they can type was audited by nobody.
//
// That is this project's recurring shape and the reason the guard is
// here rather than the fix alone: a document nothing checks is a
// document that drifts, and the fix without the guard just resets the
// clock.
//
// The reference is prose organised by task rather than a list of
// commands, which is the right shape for it — so this checks that the
// command appears, not where or how. A reader searching for `doctor`
// finds something; whether what they find is any good is a judgement no
// test makes.
func TestEverySubcommandIsInTheCommandReference(t *testing.T) {
	root := repoRoot(t)
	ref := readDoc(t, filepath.Join(root, "docs", "command-reference.md"))

	found := 0
	for _, binary := range []string{"halite-node", "halite-hub", "halite-api"} {
		for _, sub := range subcommandsOf(t, root, binary) {
			if reason, deliberate := undocumented[binary+" "+sub]; deliberate {
				if reason == "" {
					t.Errorf("%s %s is declared undocumented with no reason", binary, sub)
				}
				continue
			}
			found++
			// `halite-node doctor` or, in a heading or sentence that has
			// already named the binary, just the subcommand in code
			// spans. Both spellings appear in the document today.
			if strings.Contains(ref, binary+" "+sub) {
				continue
			}
			t.Errorf("`%s %s` is a subcommand and docs/command-reference.md never names it. "+
				"A command a reader cannot find is one they do not know they have; if it "+
				"is deliberately undocumented, say so in this file's `undocumented` table "+
				"with the reason.", binary, sub)
		}
	}
	if found == 0 {
		t.Fatal("no subcommands were read out of the binaries; this audit has stopped checking anything")
	}
	t.Logf("checked %d subcommands against the command reference", found)
}

// undocumented is the set that is deliberately not in the reference, and
// why. A subcommand may only be missing from the document if it is
// named here.
var undocumented = map[string]string{
	// Not a command a person runs: it reads a job on stdin and is what
	// `halite-hub ssh` invokes on a target after pushing the binary.
	// The node's own usage text leaves it out for the same reason.
	"halite-node oneshot": "the agentless mode's callee; it reads a job on stdin and no person invokes it",

	// `version` and `help` are on every binary and are what `--help`
	// tells you about. A reference that spent three sections saying so
	// would bury the commands that need explaining.
	"halite-node version": "universal, and self-describing",
	"halite-node help":    "universal, and self-describing",
	"halite-hub version":  "universal, and self-describing",
	"halite-hub help":     "universal, and self-describing",
	"halite-api version":  "universal, and self-describing",
	"halite-api help":     "universal, and self-describing",

	// Refused with a message naming SPEC 32, rather than implemented.
	// Documenting it would describe something that does not run.
	"halite-hub files": "not built; the command refuses by name and says so",

	// An alias of `connect`, which the reference documents. Both words
	// reach the same function; `serve` is there because it is what
	// somebody used to a service manager types.
	"halite-node serve": "an alias of `connect`, which is documented",

	// Not a command either: it refuses and points at `halite-hub policy
	// show`, because the policy is the hub's and the API reads the same
	// file. It exists to answer somebody who guessed, and documenting
	// the guess would suggest it was the way in.
	"halite-api policy": "a redirect to `halite-hub policy`; it refuses and says where to go",
}

// subcommandsOf reads a binary's dispatch switch.
//
// The switch rather than the usage text: usage is prose that can leave a
// command out on purpose — `oneshot` is left out of the node's — and
// reading it would make this audit agree with whatever the usage text
// happened to say. The switch is what actually answers when somebody
// types the word.
func subcommandsOf(t *testing.T, root, binary string) []string {
	t.Helper()
	path := filepath.Join(root, "cmd", binary, "main.go")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	body := string(b)

	// The dispatch switch, not every switch in the file: the hub has
	// others, and one of them switches on note/review/blocking.
	start := strings.Index(body, "switch ")
	if start < 0 {
		t.Fatalf("%s has no switch to read", path)
	}
	end := strings.Index(body[start:], "\n\tdefault:")
	if end < 0 {
		t.Fatalf("%s's dispatch switch has no default branch, so this audit cannot "+
			"tell where it ends", path)
	}
	block := body[start : start+end]

	caseLine := regexp.MustCompile(`(?m)^\tcase (.+):$`)
	name := regexp.MustCompile(`"([a-z][a-z-]*)"`)
	seen := map[string]bool{}
	var out []string
	for _, m := range caseLine.FindAllStringSubmatch(block, -1) {
		for _, n := range name.FindAllStringSubmatch(m[1], -1) {
			// `--version` and `-h` are aliases of the word beside them.
			if seen[n[1]] {
				continue
			}
			seen[n[1]] = true
			out = append(out, n[1])
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatalf("no cases were read from %s's dispatch switch", path)
	}
	return out
}

// Nothing is declared undocumented that is not a subcommand.
//
// A name left in the table after the command it named was removed or
// renamed is an exemption nobody granted, quietly covering whatever
// takes that name next.
func TestNothingIsExemptedThatIsNotASubcommand(t *testing.T) {
	root := repoRoot(t)
	real := map[string]bool{}
	for _, binary := range []string{"halite-node", "halite-hub", "halite-api"} {
		for _, sub := range subcommandsOf(t, root, binary) {
			real[binary+" "+sub] = true
		}
	}
	for name := range undocumented {
		if !real[name] {
			t.Errorf("`%s` is declared undocumented and is not a subcommand; the exemption "+
				"now covers nothing, or covers something that took the name", name)
		}
	}
}

// readDoc reads a documentation file, failing loudly if it has moved.
func readDoc(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return string(b)
}
