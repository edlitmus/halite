package specaudit

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/config"
)

// proseFiles are the documents a reader plans from. Their tables are
// machine-checked already; their prose was not.
var proseFiles = []string{"docs/DIVERGENCE.md", "README.md"}

// denials are the ways a document says a thing does not exist. A
// delivered phase in the same sentence as one of these is a claim that
// has expired.
var denials = regexp.MustCompile(`(?i)\b(` +
	`ha(?:s|ve) not been started|ha(?:s|ve) not started|not been started|` +
	`does not exist|do not exist|is not built|are not built|not yet built|` +
	`is not started|are not started|has not begun` +
	`)\b`)

// TestNoProseDeniesADeliveredPhase holds the documents to the same rule
// the source is held to.
//
// `TestNothingClaimsADeliveredPhase` reads Go string literals and skips
// Markdown, which is why the ledger's header still said "phases 2
// through 6 have not been started" long after phase 4 shipped, and why
// one section said the FIPS artifact set did not exist eleven paragraphs
// after another said it was built. The tables were right the whole time.
// Prose is what a reader plans from, so it is worth the same guard.
//
// Only denial is checked. A document must be free to say a phase is
// delivered, to describe what it delivered, and to say a phase that has
// not shipped has not shipped.
func TestNoProseDeniesADeliveredPhase(t *testing.T) {
	root := filepath.Join("..", "..")
	phase := regexp.MustCompile(`(?i)\bphase(?:s)? ([0-9])\b`)

	checked, problems := 0, 0
	for _, name := range proseFiles {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// A sentence, not a line: these files are hard-wrapped, so a
		// claim and its denial routinely sit on different lines.
		for _, sentence := range splitSentences(string(body)) {
			if !denials.MatchString(sentence) {
				continue
			}
			checked++
			for _, m := range phase.FindAllStringSubmatch(sentence, -1) {
				named := "phase " + m[1]
				for _, delivered := range DeliveredPhases {
					if named != delivered {
						continue
					}
					t.Errorf("%s says %s does not exist, and it is delivered:\n  %s",
						name, named, collapse(sentence))
					problems++
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no denials were found in any document; this audit has stopped checking")
	}
	t.Logf("read %d denying sentences, %d problems", checked, problems)
}

// splitSentences breaks text at sentence ends and at blank lines, so a
// denial in one paragraph cannot reach a phase named in the next.
func splitSentences(text string) []string {
	var out []string
	for _, para := range strings.Split(text, "\n\n") {
		start := 0
		for i := 0; i < len(para); i++ {
			if para[i] != '.' && para[i] != ':' {
				continue
			}
			// Not a decimal point or a section number: SPEC 32 and 26.2
			// appear constantly in this prose.
			if i+1 < len(para) && para[i+1] != ' ' && para[i+1] != '\n' {
				continue
			}
			out = append(out, para[start:i+1])
			start = i + 1
		}
		if start < len(para) {
			out = append(out, para[start:])
		}
	}
	return out
}

func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// TestNoWaiverCitesADeliveredPhase holds the unread-key waivers to the
// phase list.
//
// Twelve settings were waived as "phase 2: there is no job cache" and
// the like, on a build where phase 2 had shipped. Nothing noticed,
// because the reasons lived in a test file that only asserted a key was
// accounted for — not that the account was still true. An excuse keyed
// to a phase expires when the phase lands, and this is what says so.
//
// The waivers are read from the package rather than from a test file so
// that one place says why a key is unread.
func TestNoWaiverCitesADeliveredPhase(t *testing.T) {
	phase := regexp.MustCompile(`(?i)\bphase ([0-9])\b`)

	waivers := map[string]string{}
	for name, effect := range config.InertKeys {
		waivers[name] = effect
	}
	for name, reason := range config.UnreadKeys {
		waivers[name] = reason
	}
	if len(waivers) == 0 {
		t.Fatal("no waivers were found; this audit has stopped checking")
	}

	for name, reason := range waivers {
		for _, m := range phase.FindAllStringSubmatch(reason, -1) {
			named := "phase " + m[1]
			for _, delivered := range DeliveredPhases {
				if named != delivered {
					continue
				}
				t.Errorf("the waiver for %q blames %s, which is delivered: %q",
					name, delivered, reason)
			}
		}
	}
	t.Logf("checked %d waivers", len(waivers))
}
