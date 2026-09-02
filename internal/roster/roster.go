// Package roster reads the target list for agentless mode.
//
// SPEC 21.2 names eight backends. This builds the ones an estate can
// use without another system in the loop — `flat`, `sshconfig`,
// `cache`, and `ansible` — and names the rest rather than pretending
// they are absent.
package roster

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// Target is one machine reachable over ssh.
//
// The field names are Salt's, because an estate has a roster file
// already and rewriting it to move is work with no benefit. `node_opts`
// is the one rename SPEC 21.2 makes, and the old spelling is read.
type Target struct {
	// ID is how the target is named in a job and a return.
	ID string
	// Host is what ssh connects to. Empty takes the ID, which is what
	// a roster keyed by hostname means.
	Host string
	Port int
	User string
	// Password is discouraged and warned about: it puts a credential
	// in a file that is usually in the state tree.
	Password string
	// Priv is a private key path; PrivPassword its passphrase.
	Priv       string
	PrivPasswd string
	// Sudo runs the pushed binary through sudo, as SudoUser.
	Sudo     bool
	SudoUser string
	// TTY forces a pseudo-terminal, which some sudo configurations
	// require.
	TTY bool
	// Timeout bounds one connection.
	Timeout time.Duration
	// ThinDir is where the pushed binary is cached on the target.
	ThinDir string
	// NodeOpts are configuration settings the pushed binary runs with.
	// Salt spells this `minion_opts`, and that spelling is read. lexicon:allow
	NodeOpts *value.Map
	// SetPath is prepended to PATH on the target.
	SetPath string
	// Tunnel asks for the reverse tunnel rather than inline content.
	Tunnel bool
	// IdentitiesOnly passes `-o IdentitiesOnly=yes`, which stops ssh
	// offering every key in the agent to a host that will refuse most
	// of them.
	IdentitiesOnly bool
	// ProxyJump is `-J`, which is why this uses the system ssh: an
	// estate's jump hosts are already in its ssh config.
	ProxyJump string
	// Grains are attached to the target, so a roster can carry what a
	// node would otherwise report.
	Grains *value.Map
}

// Roster is a set of targets.
type Roster struct {
	Targets []Target
	// Warnings are things an operator should know that are not
	// failures — a password in the file, most often.
	Warnings []string
}

// IDs is every target's name, in order.
func (r *Roster) IDs() []string {
	out := make([]string, 0, len(r.Targets))
	for _, t := range r.Targets {
		out = append(out, t.ID)
	}
	sort.Strings(out)
	return out
}

// Get finds a target by name.
func (r *Roster) Get(id string) (Target, bool) {
	for _, t := range r.Targets {
		if t.ID == id {
			return t, true
		}
	}
	return Target{}, false
}

// Backends are the roster kinds SPEC 21.2 names, with whether this
// build reads them.
//
// Named rather than omitted: an estate with `roster: ansible` has made
// a reasonable request, and "ansible is not a roster" would be a lie.
var Backends = map[string]string{
	"flat":      "",
	"sshconfig": "",
	"cache":     "",
	"ansible":   "",
	"scan":      "a CIDR sweep, which is not built (SPEC section 21.2)",
	"cloud":     "reads cloud APIs through a bridge, which is not built (SPEC section 21.2)",
	"terraform": "reads a Terraform state file, which is not built (SPEC section 21.2)",
}

// Available is the backends this build reads, in order.
func Available() []string {
	var out []string
	for name, unbuilt := range Backends {
		if unbuilt == "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// CheckBackend reports why a backend cannot be used.
func CheckBackend(name string) error {
	unbuilt, known := Backends[name]
	if !known {
		return fmt.Errorf("%q is not a roster backend; this build reads %s",
			name, strings.Join(Available(), ", "))
	}
	if unbuilt != "" {
		return fmt.Errorf("the %s roster %s", name, unbuilt)
	}
	return nil
}

// applyDefaults fills in what a target did not say.
func (t *Target) applyDefaults() {
	if t.Host == "" {
		t.Host = t.ID
	}
	if t.Port == 0 {
		t.Port = 22
	}
	if t.Timeout <= 0 {
		t.Timeout = 30 * time.Second
	}
	if t.ThinDir == "" {
		// SPEC 21.1's location. Under /var/tmp rather than /tmp
		// because /tmp is cleared on boot on many systems, and the
		// whole point of the cache is that the second run skips the
		// transfer.
		t.ThinDir = "/var/tmp/halite-thin"
	}
}
