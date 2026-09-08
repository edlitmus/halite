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
// # The default is "assumed", and earning better has been the work
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
// `Assumed` is the zero value on purpose: a module nobody has
// classified must read as "nobody looked", not as "fine". DIVERGENCE
// 5.33 wrote the table down per module and it was mostly `Assumed`
// then; 5.35 through 5.38 drove all but two of the root-mutating
// modules against their real tools on real machines. What is still
// `Assumed` — `apparmor` and `snap` — says why in its own note, and
// the release gate refuses to ship while either is. `exec.Registry`
// appends the note to a *failing* mutation, and `sys.evidence` and
// `doctor` answer it on request.
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
		"were run on Ubuntu 24.04 (DIVERGENCE 4.5); `info_installed`, `file_dict`, " +
		"`download` and `list_downloaded` were checked field by field against real " +
		"dpkg-query and dpkg-deb, and `autoremove` really reclaimed a package in the " +
		"fleet container (5.40). The Chocolatey provider has only been read from, and " +
		"dnf, yum, zypper, apk, pacman and pkgng have not been driven at all"},
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

	// ---- Mutated a real Debian, in `make fleetcheck`'s container ----
	//
	// The container is thrown away and is not a machine anybody depends
	// on, and the note says so. What it is *not* is a stand-in: the
	// binary is Debian's own `dpkg` at Debian's own version, reading
	// Debian's own package database, and the writes below really happen.
	// The scope it does not cover is the other distributions and the
	// other init systems, which is the same caveat 4.5 makes about the
	// one real Ubuntu node.

	"dpkg": {Level: exec.Hardware, Note: "driven against the real dpkg 1.21.23 on Debian 12 " +
		"in `make fleetcheck`: the listing, control fields, file list and ownership search " +
		"read from the real package database, a real .deb read from disk, and a hold placed " +
		"through `dpkg --set-selections` and confirmed by dpkg itself (DIVERGENCE 5.35). " +
		"Not covered: `dpkg.verify`, and any distribution other than Debian 12"},
	"debconf": {Level: exec.Hardware, Note: "driven against the real debconf 1.5.82 on " +
		"Debian 12 in `make fleetcheck`: an answer written through " +
		"`debconf-set-selections` and read back by both `debconf-get-selections` and " +
		"`debconf-show` (DIVERGENCE 5.35). A malformed line makes debconf warn and " +
		"continue rather than fail, which this module turns into an error -- demonstrated " +
		"by breaking the field order on purpose"},
	"pkgrepo": {Level: exec.Hardware, Note: "the apt provider was driven on Debian 12 in " +
		"`make fleetcheck`: a signed repository written, read by a real `apt-get update`, " +
		"listed, and removed (DIVERGENCE 5.35). Not covered: the yum, zypper and " +
		"Chocolatey providers, and a repository reached over the network rather than from " +
		"the filesystem"},
	"timezone": {Level: exec.Hardware, Note: "driven on Debian 12 in `make fleetcheck`: " +
		"`/etc/localtime` really relinked against a real tzdata 2026b and read back, and a " +
		"zone the machine does not have refused (DIVERGENCE 5.35). Not covered: the " +
		"`timedatectl` branch, which needs systemd running, and the macOS `systemsetup` " +
		"branch, which had no test at all behind a fixture that looked like one " +
		"(plan.md §1.3)"},

	// ---- Mutated a real machine, in the live CI legs ----
	//
	// Not the fleetcheck container: `sysctl` is the kernel and a
	// container shares the host's, Docker bind-mounts `/etc/hostname` so
	// the atomic replace cannot work there, the container has no netplan
	// at all, and a container has no running systemd for `service` to
	// speak to. These run on machines rather than images -- a GitHub
	// runner and the FreeBSD virtual machine, and this project's own
	// Ubuntu host -- and really rename them, really move a kernel
	// parameter, really have a running netplan validate a document, and
	// really start and stop a real unit over systemd's own D-Bus API.
	//
	// `netplan` never gets as far as `netplan apply`, and that is
	// deliberate rather than a gap in coverage: it is the one path the
	// module keeps behind an explicit `apply: true`, because it
	// reconfigures the interface the run arrives over.

	"hostname": {Level: exec.Hardware, Note: "renamed a real machine on both branches: " +
		"`hostnamectl` on an Ubuntu 24.04 runner under systemd, and `sysrc` on FreeBSD " +
		"15.1 -- the branch that had no test at all behind a fixture that looked like one " +
		"(plan.md §1.4). The running name and the boot-time name are checked separately, " +
		"and checked while they *differ*, which is the node somebody renamed by hand " +
		"(DIVERGENCE 5.36). Not covered: macOS and the other BSDs"},
	"sysctl": {Level: exec.Hardware, Note: "set a parameter on two real kernels: Linux " +
		"6.x on an Ubuntu 24.04 runner, persisted to a drop-in, and FreeBSD 15.1, " +
		"persisted to sysctl.conf -- so both spellings `sysctlAssign` tries are now known " +
		"to work on the platform that takes them (DIVERGENCE 5.36). Not covered: the real " +
		"`/etc/sysctl.conf` path, because a test that edits a hand-maintained file and " +
		"puts it back can cost an operator more than the coverage is worth; the persist " +
		"target is redirected and nothing else is"},
	"netplan": {Level: exec.Hardware, Note: "`netplan.managed` wrote a document into " +
		"/etc/netplan on a real netplan 1.1.2 (Ubuntu 24.04), real `netplan generate` " +
		"validated it, and `netplan get` read the values back -- so the YAML this " +
		"module's encoder produces is now known to be the YAML netplan reads, not merely " +
		"valid YAML (DIVERGENCE 5.38). A document with an unknown key was refused with " +
		"netplan's own message. Not covered, and deliberately: `netplan apply`, which " +
		"reconfigures the interface the run arrives over -- the module never calls it " +
		"unless a declaration names `apply: true`, and no live test applies"},
	"service": {Level: exec.Hardware, Note: "the systemd provider was driven against a real " +
		"systemd 255 on an Ubuntu 24.04 host over its own D-Bus API: a unit started, " +
		"stopped, restarted, enabled, disabled, masked and unmasked, each checked against " +
		"`systemctl` directly rather than the module's own read-back, and the `JobRemoved` " +
		"wait shown to be awaited (DIVERGENCE 5.39). The `systemctl` fallback was driven " +
		"against the same unit. Not covered: the launchd, sysvinit and openrc providers, " +
		"which have not been run at all, and the FreeBSD rc branch, which still only reads"},

	// ---- Read from a real system, mutation never watched ----

	"win_service": {Level: exec.Captured, Note: "reads the real service control manager " +
		"through its API on every Windows run and converges against what it finds, but " +
		"nothing has watched this module start, stop or re-type a service"},
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

	"apparmor": {Level: exec.Assumed, Note: "the *reading* half is demonstrated: " +
		"securityfs parses correctly against 123 real profiles on Ubuntu 24.04, in all " +
		"four modes. The *mutating* half is not, and now for a known reason rather than " +
		"an unexamined one -- apparmor-utils 4.0.1 cannot parse the profile set Ubuntu " +
		"itself ships, so `aa-enforce`, `aa-complain` and `aa-disable` fail on every " +
		"profile on that platform and this module has no other way to change a mode " +
		"(DIVERGENCE 5.37). `apparmor.status` reports that as `tools: false` with the " +
		"reason, rather than `true` because the binary is on PATH"},
	"snap": {Level: exec.Assumed, Note: "nothing here has run against a real snapd, and " +
		"the `snap list` fixtures were written from its documented columns rather than " +
		"captured (DIVERGENCE 5.28)"},
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
