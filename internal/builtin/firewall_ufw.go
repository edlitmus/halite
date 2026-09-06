package builtin

import (
	"fmt"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
)

// ufwProvider manages Ubuntu's ufw, and is SPEC 15.3's `ufw`.
//
// Two decisions worth stating, because both are about ufw rather than
// about this build.
//
// **Convergence is asked of ufw, not computed from its listing.** `ufw
// status` is written for a person: it aligns columns, prints `Anywhere`
// for an unset address, duplicates every rule for v6, and renders an
// application profile by name rather than by the ports it stands for.
// Reimplementing ufw's own matching against that text would be a second
// implementation of the part that decides whether a rule already exists,
// and the two would disagree eventually. ufw is already idempotent and
// says which it did — "Skipping adding existing rule" — so the question
// is put to it.
//
// **`--dry-run` is what makes test mode real.** ufw takes it and reports
// what it would do without doing it, which is exactly what a state in
// test mode needs and is rarer than it should be among the tools this
// project drives. It means `test=True` here is ufw's own answer rather
// than a prediction this build made on its behalf.
type ufwProvider struct{}

func (ufwProvider) Name() string { return "ufw" }

func (ufwProvider) Available(c *exec.Context) bool { return c.Which("ufw") != "" }

// Status parses `ufw status verbose`.
//
//	Status: active
//	Logging: on (low)
//	Default: deny (incoming), allow (outgoing), disabled (routed)
func (ufwProvider) Status(c *exec.Context) (firewallStatus, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"ufw", "status", "verbose"},
		IgnoreExitCode: true,
	})
	if err != nil {
		return firewallStatus{}, err
	}
	if res.Code != 0 {
		return firewallStatus{}, fmt.Errorf("%s", firstLine(res.Stderr+res.Stdout))
	}
	return parseUFWStatus(res.Stdout), nil
}

func parseUFWStatus(stdout string) firewallStatus {
	st := firewallStatus{Defaults: map[string]string{}}
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		switch {
		case strings.HasPrefix(line, "Status:"):
			st.Enabled = strings.TrimSpace(strings.TrimPrefix(line, "Status:")) == "active"
		case strings.HasPrefix(line, "Default:"):
			// `deny (incoming), allow (outgoing), disabled (routed)`
			for _, part := range strings.Split(strings.TrimPrefix(line, "Default:"), ",") {
				part = strings.TrimSpace(part)
				policy, rest, ok := strings.Cut(part, " ")
				if !ok {
					continue
				}
				direction := strings.Trim(strings.TrimSpace(rest), "()")
				if direction != "" {
					st.Defaults[direction] = policy
				}
			}
		}
	}
	return st
}

func (ufwProvider) SetEnabled(c *exec.Context, on bool) error {
	verb := "disable"
	if on {
		verb = "enable"
	}
	// --force, because `ufw enable` prompts about disrupting existing
	// ssh connections and there is nobody at the terminal to answer. The
	// prompt is a real warning and it is worth repeating where an
	// operator will see it: enabling a firewall whose rules do not admit
	// ssh will lock this node out, which is why `firewall.enabled` sets
	// the defaults before it turns anything on.
	res, err := c.Run(exec.Command{
		Argv:           []string{"ufw", "--force", verb},
		IgnoreExitCode: true,
	})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s", firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

func (ufwProvider) SetDefault(c *exec.Context, direction, policy string) error {
	res, err := c.Run(exec.Command{
		Argv:           []string{"ufw", "default", policy, direction},
		IgnoreExitCode: true,
	})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s", firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

func (ufwProvider) Reload(c *exec.Context) error {
	res, err := c.Run(exec.Command{Argv: []string{"ufw", "reload"}, IgnoreExitCode: true})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s", firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

func (p ufwProvider) Apply(c *exec.Context, r firewallRule, remove, dryRun bool) (bool, error) {
	argv := []string{"ufw"}
	if dryRun {
		argv = append(argv, "--dry-run")
	}
	if remove {
		argv = append(argv, "delete")
	}
	argv = append(argv, ufwRuleArgs(r)...)

	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return false, err
	}
	if res.Code != 0 {
		return false, fmt.Errorf("%s: %s", strings.Join(argv, " "), firstLine(res.Stderr+res.Stdout))
	}
	return ufwChanged(res.Stdout + res.Stderr), nil
}

// ufwChanged reads whether ufw did anything.
//
// ufw prints "Skipping adding existing rule" when the rule is already
// there and "Could not delete non-existent rule" when it is already
// gone. Both are successes with a zero exit — the state asked for is the
// state that holds — and both mean nothing changed.
func ufwChanged(out string) bool {
	for _, unchanged := range []string{
		"Skipping adding existing rule",
		"Skipping inserting existing rule",
		"Could not delete non-existent rule",
	} {
		if strings.Contains(out, unchanged) {
			return false
		}
	}
	return true
}

// ufwRuleArgs renders a rule as ufw's own grammar.
//
// ufw takes two forms and the simple one is not a prefix of the full
// one: `ufw allow 22/tcp`, and `ufw allow from 10.0.0.0/8 to any port 22
// proto tcp`. Mixing them produces a rule ufw parses as something else,
// so this picks one on whether an address is involved.
func ufwRuleArgs(r firewallRule) []string {
	argv := []string{r.Action}
	if r.Direction == "out" {
		argv = append(argv, "out")
	}

	if r.From == "" && r.To == "" {
		// The short form. A protocol without a port is not expressible
		// here — `ufw allow tcp` is not a rule — so it goes the long
		// way round, which is what the caller gets for asking.
		if r.Protocol != "" && r.Port != "" {
			argv = append(argv, r.Port+"/"+r.Protocol)
		} else if r.Port != "" {
			argv = append(argv, r.Port)
		}
		return appendUFWComment(argv, r)
	}

	// The full form. `any` is ufw's word for "unspecified", and it has
	// to be written: omitting the clause changes what the rule means.
	from := r.From
	if from == "" {
		from = "any"
	}
	to := r.To
	if to == "" {
		to = "any"
	}
	argv = append(argv, "from", from, "to", to)
	if r.Port != "" {
		argv = append(argv, "port", r.Port)
	}
	if r.Protocol != "" {
		argv = append(argv, "proto", r.Protocol)
	}
	return appendUFWComment(argv, r)
}

func appendUFWComment(argv []string, r firewallRule) []string {
	if r.Comment == "" {
		return argv
	}
	return append(argv, "comment", r.Comment)
}
