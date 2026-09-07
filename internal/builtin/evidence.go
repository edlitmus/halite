package builtin

import (
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/doctor"
	"github.com/edlitmus/halite/internal/exec"
)

// registerEvidence declares what has actually been demonstrated about
// each module's dealings with the tool it drives.
//
// # The default is "assumed", and most of this table says so
//
// A module that mutates a machine mostly works by running another
// program and reading what it says back. A unit test supplies that
// output, so it establishes that the parser reads what the test author
// believed the tool prints — and that belief has been wrong three times
// in two weeks. `pf` matched no rule at all on a real host while its
// idempotence test passed, because the fixture was written in the
// module's own spelling (DIVERGENCE 5.31). Before that, `timezone`'s
// fixture forced the file branch on a platform that drives
// `systemsetup`, and `hostname`'s did the same to FreeBSD's `sysrc`
// branch (plan.md §1.3, §1.4). Each looked like coverage.
//
// So this table is deliberately unflattering. Most of it is `Assumed`,
// because most of these modules have never been pointed at the program
// they drive, and writing that down is the only way an operator learns
// it before they need to know. `exec.Registry` appends the note to a
// *failing* mutation, `sys.evidence` and `doctor` answer it on request,
// and the release gate refuses to ship a build whose root-privileged
// mutating modules are still assumptions.
//
// # What each level means here
//
//   - Hardware: the module's *mutating* path has been run against the
//     real tool on a real machine, and the note says which.
//   - Captured: the module has been run against the real tool, but only
//     reading it — nothing has watched this module change anything.
//   - Assumed: the fixtures were written from documentation or from what
//     the author expected the tool to print. The note says what is
//     assumed.
//
// A claim here is a claim about *this project's own history*, not about
// what looks plausible. Nothing is promoted because the code reads well.
func registerEvidence(r *Registries) {
	for module, e := range moduleEvidence {
		r.Exec.SetEvidence(module, e)
	}
}

var moduleEvidence = map[string]exec.Evidence{
	// ---- Mutated a real system ----

	"pkg": {Level: exec.Hardware, Note: "the apt provider installed packages in a " +
		"highstate on a real Ubuntu node and converged, and its optional capabilities " +
		"were run on Ubuntu 24.04 (DIVERGENCE 4.5). The Chocolatey provider has only " +
		"been read from, and dnf, yum, zypper, apk, pacman and pkgng have not been " +
		"driven at all"},
	"zpool": {Level: exec.Hardware, Note: "driven against real pools on Linux with " +
		"OpenZFS 2.2.2, which found two defects in reading `zpool list` that the " +
		"fixtures had agreed with (DIVERGENCE 4.7). FreeBSD, where this project's ZFS " +
		"reading was first checked, is not covered, and `zpool.healthy` has only been " +
		"run against pools that are healthy"},
	"win_registry": {Level: exec.Hardware, Note: "writes and reads real values in a real " +
		"registry hive on every Windows run (DIVERGENCE 4.6)"},
	"win_task": {Level: exec.Hardware, Note: "registers a real scheduled task through " +
		"`schtasks` and converges against it on every Windows run (DIVERGENCE 4.6)"},
	"win_dacl": {Level: exec.Hardware, Note: "grants, tightens and removes real access " +
		"control entries on every Windows run (DIVERGENCE 4.6)"},
	"firewall": {Level: exec.Hardware, Note: "the pf provider was driven on a real FreeBSD " +
		"host — status, enable, allow and absent, idempotent across runs — and that run " +
		"found a defect no test had, so no rule matched itself and `firewall.absent` " +
		"could remove nothing (DIVERGENCE 5.31). Still not seen on hardware: the anchor " +
		"refusal refusing. A port list still cannot match, because pf expands one. The " +
		"ufw provider has not been driven at all"},

	// ---- Read from a real system, mutation never watched ----

	"win_service": {Level: exec.Captured, Note: "reads the real service control manager " +
		"through its API on every Windows run and converges against what it finds, but " +
		"nothing has watched this module start, stop or re-type a service"},
	"service": {Level: exec.Captured, Note: "halite's own systemd units run under a real " +
		"systemd (DIVERGENCE 4.5) and the Windows provider reads the real service control " +
		"manager, but nothing has watched this module start or stop a service on any " +
		"platform, and the launchd and rc.d providers have not been run at all"},
	"jail": {Level: exec.Captured, Note: "the shape of `jls --libxo=json` is checked " +
		"against the real jls on CI's FreeBSD runner, but no jail has been started or " +
		"stopped by this module and the field names inside a jail entry are still " +
		"assumed (DIVERGENCE 5.32)"},
	"mount": {Level: exec.Captured, Note: "reads the real /proc/self/mounts and the real " +
		"`mount` output on the platforms CI runs, and nothing has been mounted or " +
		"unmounted by this module"},
	"sysrc": {Level: exec.Captured, Note: "reads real rc.conf through the real `sysrc` on " +
		"CI's FreeBSD runner; nothing has watched this module write one"},
	"user": {Level: exec.Captured, Note: "reads go through os/user against the real " +
		"account database, and no account has been created, changed or removed on a real " +
		"machine by this module"},

	// ---- Never pointed at the tool it drives ----

	"apparmor": {Level: exec.Assumed, Note: "no node with AppArmor running has been asked " +
		"to enforce or disable a profile by this module; both the `aa-enforce` behaviour " +
		"and the securityfs format are taken from documentation (DIVERGENCE 5.27)"},
	"snap": {Level: exec.Assumed, Note: "nothing here has run against a real snapd, and " +
		"the `snap list` fixtures were written from its documented columns rather than " +
		"captured (DIVERGENCE 5.28)"},
	"dpkg": {Level: exec.Assumed, Note: "the `dpkg-query` and `dpkg` output this parses " +
		"was written from documentation, not captured from a Debian host"},
	"debconf": {Level: exec.Assumed, Note: "`debconf-show` and `debconf-set-selections` " +
		"have not been run; both formats are taken from their manual pages"},
	"netplan": {Level: exec.Assumed, Note: "`netplan generate` and `netplan apply` have " +
		"not been run, and the YAML this writes has never been round-tripped through a " +
		"real netplan — which is the module that reconfigures the interface an operator " +
		"is connected over"},
	"pkgrepo": {Level: exec.Assumed, Note: "no repository has been added, changed or " +
		"removed on a real machine by this module, on any platform"},
	"sysctl": {Level: exec.Assumed, Note: "no kernel parameter has been set on a real " +
		"machine by this module, on any platform"},
	"hostname": {Level: exec.Assumed, Note: "the FreeBSD `sysrc` branch had no test at " +
		"all behind a fixture that looked like one (plan.md §1.4) and is covered now, but " +
		"no machine has been renamed by this module"},
	"timezone": {Level: exec.Assumed, Note: "the macOS `systemsetup` branch could not " +
		"converge behind its fixture (plan.md §1.3) and is covered now, but no machine's " +
		"clock has been re-zoned by this module"},
}

// Trust renders this registry's evidence for `doctor`.
//
// The registry is the only thing that knows both which functions change
// a machine and which of them need root, so the mapping lives here
// rather than being restated in each command — a second copy of "what
// counts as root-mutating" is a second copy that can drift, and this
// one decides what an operator is warned about.
func (r *Registries) Trust() []doctor.ModuleTrust {
	sigs := r.Exec.Signatures()
	mutating := map[string]bool{}
	needsRoot := map[string]bool{}
	for _, fn := range sigs.Names() {
		sig, ok := sigs.Lookup(fn)
		if !ok || !sig.Mutates {
			continue
		}
		module, _, _ := strings.Cut(fn, ".")
		mutating[module] = true
		for _, p := range sig.Privileges {
			if strings.Contains(p, "root") {
				needsRoot[module] = true
			}
		}
	}
	names := make([]string, 0, len(mutating))
	for m := range mutating {
		names = append(names, m)
	}
	sort.Strings(names)

	out := make([]doctor.ModuleTrust, 0, len(names))
	for _, m := range names {
		out = append(out, doctor.ModuleTrust{
			Module:       m,
			Demonstrated: r.Exec.Evidence(m).Demonstrated(),
			Root:         needsRoot[m],
		})
	}
	return out
}
