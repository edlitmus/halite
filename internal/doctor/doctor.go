// Package doctor is SPEC 26.4's diagnostics: the checks `halite-node
// doctor` and `halite-hub doctor` run, each with a pass or fail and a
// remediation line.
//
// SPEC's argument for it is worth repeating because it is the design:
// "most operational tickets on a Salt estate are one of these checks,
// and making them a single command is worth more than it appears." The
// facts these checks report are almost all reported somewhere already —
// in a grain, in a log line, in a metric, in an error at the moment
// something failed. What did not exist is anything that puts the
// question to the node and answers it now, before something fails.
//
// # The remediation line is not decoration
//
// A check that says "certificate expires in 3 days" and stops has moved
// the problem rather than solved it: the operator still has to know
// which command renews it. Every check that can report anything other
// than a pass carries what to do about it, and `doctor_test.go` fails
// the build if one does not. That is the whole difference between this
// and reading the grains.
package doctor

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Status is what a check found.
type Status string

const (
	// Pass: the check looked and found nothing wrong.
	Pass Status = "pass"
	// Warn: something an operator should know and that is not stopping
	// anything today. A certificate a fortnight from expiry; a FIPS
	// build on a host whose kernel is not in FIPS mode.
	Warn Status = "warn"
	// Fail: something is broken now.
	Fail Status = "fail"
	// Skip: the check does not apply here, and why. Not a pass — a
	// check that cannot run and reports a pass is worse than one that
	// says nothing, because it answers a question it did not ask.
	Skip Status = "skip"
)

// Result is one check's answer.
type Result struct {
	// Name is the check's, matching SPEC 26.4's own wording.
	Name string
	// Status is what it found.
	Status Status
	// Detail is what it found, in one line. Present for every status,
	// including a pass: "expires in 71 days" is the answer, and "pass"
	// on its own is not.
	Detail string
	// Remedy is what to do about it. Required for Warn and Fail,
	// meaningless for Pass, and for Skip it says what would make the
	// check apply.
	Remedy string
}

// A Check is one diagnostic.
type Check struct {
	// Name matches SPEC 26.4's wording, and the guard in this package
	// holds the set of names to the specification's sentence.
	Name string
	// Roles is which commands run it: "node", "hub", or both. A check
	// that is not for this role is not run rather than skipped, because
	// "queue depths: skipped, this is a node" is noise on every run
	// rather than information on any.
	Roles []string
	// Run performs it. It must not change anything: an operator runs
	// this when something is already wrong, and a diagnostic with a
	// side effect is one nobody dares run twice.
	Run func(ctx context.Context) Result
}

// Applies reports whether this check belongs to a role.
func (c Check) Applies(role string) bool {
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Roles a check can carry.
const (
	RoleNode = "node"
	RoleHub  = "hub"
)

// Report is a run of the checks.
type Report struct {
	Role    string
	Results []Result
}

// Run performs every check that applies to the role, in order.
//
// One check's failure never stops another: an operator running this has
// something wrong already, and stopping at the first finding hides the
// second — which on a node that has been down for a week is usually the
// one that explains it.
func Run(ctx context.Context, role string, checks []Check) Report {
	report := Report{Role: role}
	for _, c := range checks {
		if !c.Applies(role) {
			continue
		}
		res := c.Run(ctx)
		if res.Name == "" {
			res.Name = c.Name
		}
		report.Results = append(report.Results, res)
	}
	return report
}

// Worst is the most serious status in the report, for an exit code.
func (r Report) Worst() Status {
	worst := Pass
	rank := map[Status]int{Skip: 0, Pass: 1, Warn: 2, Fail: 3}
	for _, res := range r.Results {
		if rank[res.Status] > rank[worst] {
			worst = res.Status
		}
	}
	return worst
}

// ExitCode is 1 when something is broken and 0 otherwise.
//
// A warning does not fail the command. `doctor` belongs in a cron job
// and in a state's `onlyif`, and a certificate three weeks from expiry
// must not fail either of those — it is a thing to do this month, not a
// reason to stop. A failure does, because a failure is a node that is
// not working now.
func (r Report) ExitCode() int {
	if r.Worst() == Fail {
		return 1
	}
	return 0
}

// Counts returns how many of each status, for a summary line.
func (r Report) Counts() map[Status]int {
	out := map[Status]int{}
	for _, res := range r.Results {
		out[res.Status]++
	}
	return out
}

// Text renders the report the way an operator reads it.
//
// One line per check, then the remediation lines indented under the
// checks that have something to say. Not a table: the detail lines are
// sentences of very different lengths, and a table of them is a table
// with one useful column.
func (r Report) Text() string {
	var b strings.Builder
	width := 0
	for _, res := range r.Results {
		if len(res.Name) > width {
			width = len(res.Name)
		}
	}
	for _, res := range r.Results {
		fmt.Fprintf(&b, "  %-4s  %-*s  %s\n", res.Status, width, res.Name, res.Detail)
		if res.Remedy != "" && res.Status != Pass {
			for _, line := range strings.Split(res.Remedy, "\n") {
				fmt.Fprintf(&b, "        %-*s  %s\n", width, "", line)
			}
		}
	}
	counts := r.Counts()
	var parts []string
	for _, s := range []Status{Pass, Warn, Fail, Skip} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	fmt.Fprintf(&b, "\n  %s\n", strings.Join(parts, ", "))
	return b.String()
}

// Names returns the check names in a set, for the guard.
func Names(checks []Check) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range checks {
		if !seen[c.Name] {
			seen[c.Name] = true
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out
}
