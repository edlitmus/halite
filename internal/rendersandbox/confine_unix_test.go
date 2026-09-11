//go:build unix

package rendersandbox

import (
	"os"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/render"
)

// A misspelled render account must fail rather than be ignored.
//
// This is a lab finding rather than a hypothetical. `render_sandbox_user:
// no-such-account` on a node that was not root rendered perfectly
// happily, because the lookup only ran on the branch that could act on
// it. A setting that reports itself as configured and does nothing is
// the worst of the three possible outcomes, and it is the one this
// arrangement produced.
func TestAMisspelledRenderAccountIsRefusedEvenWhereItCouldNotBeUsed(t *testing.T) {
	s := New(Config{Exe: os.Args[0], User: "no-such-account-halite-test"})
	t.Cleanup(func() { _ = s.Close() })

	_, err := s.Render([]byte("k: 1\n"), render.Options{File: "acct.sls"})
	if err == nil {
		t.Fatal("a render account that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "no-such-account-halite-test") {
		t.Errorf("the error does not name the account: %v", err)
	}
}

// What the sandbox cannot enforce, it says.
func TestAnAccountThatCannotBeDroppedToIsReportedAsUnenforced(t *testing.T) {
	s := New(Config{Exe: os.Args[0], User: "nobody"})
	t.Cleanup(func() { _ = s.Close() })

	lines := s.Unenforced()
	if canDropPrivilege() {
		// Running as root: the control is real, so there is nothing to
		// report and Describe says the child runs as the account.
		if len(lines) != 0 {
			t.Errorf("running as root, and the sandbox reports %v as unenforced", lines)
		}
		if !strings.Contains(strings.Join(s.Describe(), "\n"), "the child runs as nobody") {
			t.Errorf("running as root, and Describe does not say the child drops to the account:\n%s",
				strings.Join(s.Describe(), "\n"))
		}
		return
	}
	if len(lines) != 1 {
		t.Fatalf("not running as root, and the sandbox reports %d unenforced controls: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "nobody") || !strings.Contains(lines[0], "cannot be applied") {
		t.Errorf("the report reads %q", lines[0])
	}
}

func TestNoAccountMeansNothingIsUnenforced(t *testing.T) {
	if lines := New(Config{}).Unenforced(); len(lines) != 0 {
		t.Errorf("a sandbox that asked for no account reports %v", lines)
	}
}
