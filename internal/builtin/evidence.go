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
// modules against their real tools on real machines, and `apparmor`
// closed since (DIVERGENCE 5.37's route 1 — a machine whose `aa-*`
// tools actually parse its own profile tree, unlike the one 5.37
// found), and `snap`'s reading closed with it (DIVERGENCE 5.28). What
// is still `Assumed` is nothing: **no module that changes a machine as
// root is unverified**, and the release gate passes for the first time.
// `mac_softwareupdate` was the last, and it closed the way its note
// says -- `--download` against Apple's real service on the `macos` leg,
// with the machine's version unchanged afterwards, which is the claim
// that module is built around.
//
// `mac_assistive` was the other, deferred deliberately because
// the only way to close it is a standing manual grant. It is no longer
// here: a module nobody can demonstrate and nobody intends to is not
// something to ship red forever, so it was taken out of the build
// (5.119). That is the second of the two ways past this gate, and the
// one the gate's own text offers alongside doing the work.
//
// The six that closed did so the only way this row can: by hand on a
// real Mac under `sudo`. `mac_defaults` went first and found two
// defects no unit test could reach (5.114); `mac_user`, `mac_group`
// and `mac_shadow` followed as one account arc and found none, which
// is its own kind of result (5.115); `mac_power` found a setting the
// tool reports and cannot write (5.116); `mac_keychain` found a
// removal that could not converge and a read that failed for root and
// nobody else (5.118).
// `exec.Registry` appends the note to a *failing* mutation, and
// `sys.evidence` and `doctor` answer it on request.
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
		"dnf, yum, zypper, pacman and pkgng have not been driven at all. **apk has**: " +
		"on Alpine 3.24 in the lab a package was installed through `pkg.install`, read " +
		"back through `list_pkgs`, `version` and `file_list` -- the owner capability's " +
		"first run anywhere -- and removed, each answer checked against apk itself rather " +
		"than against another reader here, and the install broken on purpose to watch the " +
		"test fail. Not covered there: `list_upgrades` parsed an empty answer because the " +
		"instance had nothing to upgrade, and `pkg.upgrade` was not run at all " +
		"(DIVERGENCE 5.124)"},
	// `cmd` had no row at all until the gate could see it. It was outside
	// the selection because that read `strings.Contains(p, "root")` over
	// the free-text Privileges field, and `cmd` says "whatever the command
	// needs" -- so the module that runs arbitrary code was outside the one
	// check written to catch a module nobody had considered. DIVERGENCE
	// 5.135.
	"cmd": {Level: exec.Hardware, Note: "runs real binaries and real shells through " +
		"`exec.OSRunner` throughout this package's tests -- `cmd.script` writing, running and " +
		"removing a real script, `cmd.exec_code`, the background form returning a real pid, and " +
		"the shell path saying so -- on every platform CI builds for, and the estate's own tree " +
		"drives `cmd.run` at 54 call sites. `RunAs` is the part that needs root and it has been " +
		"watched on the macOS leg, where it found an account in more than sixteen groups failing " +
		"as fork/exec (5.120). Three limits: `umask` is exercised by unit tests rather than by " +
		"watching a file's mode on a real run as root; the Windows shell path is built and its " +
		"quoting is unverified against cmd.exe; and there is no single tool to capture here, " +
		"because what this module drives is whatever the caller names",
	},
	"file": {Level: exec.Hardware, Note: "writes, reads, moves, links and removes real files " +
		"on a real filesystem throughout this package's tests, and `patch` drives the real " +
		"`patch` binary -- which is where running it found that an already-applied patch is " +
		"*reversed* rather than refused unless `--forward` is passed (DIVERGENCE 5.63). Two " +
		"limits: nothing here has written a file it does not own, so the `chown` path is " +
		"exercised only where the account already matches, the SELinux context pair is " +
		"not implemented at all rather than implemented and unrun, and `patch` is " +
		"unexercised on Windows because the binary that runtime resolves there -- " +
		"Strawberry Perl's patch 2.5.9 -- aborts on an ordinary unified diff"},
	"ps": {Level: exec.Hardware, Note: "read and signalled against the real process table " +
		"on every platform the suite runs, and there are **three** readers rather than " +
		"two: FreeBSD's libxo JSON, procps' columns, and BusyBox's, which is neither a " +
		"dialect of the others nor able to report %cpu or %mem at all. Each is parsed from " +
		"what the machine's own `ps` printed -- the BusyBox one against Alpine 3.24 in this " +
		"project's lab, where the procps spelling had been failing outright with " +
		"`unrecognized option: w`, and where the sizes arrive abbreviated (`1.1g`) and " +
		"lossy. The percentages come back nil there rather than zero, and `ps.top by: cpu` " +
		"refuses by name rather than ordering on a number that node cannot measure. Also " +
		"and the mutating half is demonstrated against processes the test started and " +
		"marked, killed by pid and by pattern, with test mode shown to change nothing. " +
		"No root is involved, which is the limit worth naming: signalling *another " +
		"account's* process is the case that needs privilege and it has not been run"},
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
		"zone the machine does not have refused (DIVERGENCE 5.35). The macOS `systemsetup` " +
		"branch was driven as root on a macOS 15.7.9 runner (build 24G830) on the `macos` " +
		"leg of `fleet.yml`: a zone set, `/etc/localtime` checked the moment the state " +
		"returned, a second run that changed nothing, and zones the tree has but " +
		"`systemsetup` refuses turned away by the state. Running it found four defects " +
		"(DIVERGENCE 5.131). Not covered: the `timedatectl` branch, which needs systemd " +
		"running, and macOS releases other than that runner's"},

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
		"(DIVERGENCE 5.36). macOS was a third branch nobody had written: it took the " +
		"/etc/hostname path, which a Mac does not read, and converged on it. It now keeps " +
		"the name in `scutil`'s HostName, and renamed a macOS 15.7.9 runner (build 24G830) " +
		"under sudo on the `macos` leg, checked against `scutil --get HostName` directly " +
		"(DIVERGENCE 5.132). Not covered: a Mac with no HostName set, whose `scutil` " +
		"answer has not been captured; `LocalHostName` and `ComputerName`, which the " +
		"module leaves alone; and the other BSDs"},
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
		"against the same unit. The launchd provider was driven against a real launchctl " +
		"on a macOS 15 runner as root, on a LaunchDaemon the test bootstraps: started, " +
		"stopped, restarted, enabled, disabled and listed, each checked against " +
		"`launchctl print` rather than the `launchctl list` the module reads, which found " +
		"restart reporting success while launchd had only scheduled the respawn " +
		"(DIVERGENCE 5.122). The **OpenRC** provider is driven against a real " +
		"`rc-service` and `rc-update` on Alpine 3.24 in the lab, on an init script the test " +
		"installs: started, restarted (checked by the pid changing), stopped, enabled and " +
		"disabled, each read back from `rc-update show` and the pidfile rather than from " +
		"this module -- including a disable that has to clear two runlevels, and a check " +
		"that the sysvinit provider matches that host too and is not the one picked " +
		"(DIVERGENCE 5.124). The FreeBSD rc provider is driven on the `freebsd` leg " +
		"against a real `service(8)` and `sysrc(8)`, on an rc.d script the test installs: " +
		"enabled, started, restarted (checked by the pid changing), stopped and disabled, " +
		"each read back from `sysrc -n` and the pidfile rather than from this module. That " +
		"first run found start and stop doing nothing at all, without error, on any service " +
		"rc.conf had not enabled (DIVERGENCE 5.123). The sysvinit provider is driven on the lab's " +
		"`debian13sysv` row -- a Debian 13 converted to sysvinit and rebooted into it -- " +
		"against a real `service(8)` and `update-rc.d`, on an LSB init script the test " +
		"installs: started, restarted (checked by the pid changing), stopped, enabled and " +
		"disabled, each read back from the runlevel links on disk rather than from this " +
		"module. That run found the boot state being read from /etc/rc3.d on a machine " +
		"that boots to runlevel 2 (DIVERGENCE 5.126). Not covered: the `chkconfig` branch " +
		"of the same provider, which wants a RHEL of the sysvinit era and which no machine " +
		"here has; and launchd's `gui/` and `user/` domains, since every command there " +
		"names `system/`"},

	// `openssl_cert` is the one module here whose *mutating* path costs
	// nothing to demonstrate: it writes a file it is told to write, in a
	// directory the test owns, needing no root and no network. So it is
	// held to the same standard as the modules that needed a virtual
	// machine, and the round trip runs wherever the suite does.

	"openssl_cert": {Level: exec.Hardware, Note: "driven end to end against a real OpenSSL 3.5.6 on " +
		"FreeBSD 15.1: a throwaway CA and leaf built, the chain verified and then refused against a " +
		"different CA with openssl's own numbered reason read back, a real PKCS#12 bundle written by " +
		"this module and read back through openssl, and the wrong passphrase shown to fail -- which is " +
		"what establishes that the right one really is being delivered on standard input rather than " +
		"quietly ignored. A real revocation list is generated and read. That run found a defect: an " +
		"unreadable trust file was being reported as an untrusted certificate, which is a different " +
		"problem with a different fix. Also run against OpenSSL 3.0.2 in FIPS mode on Ubuntu " +
		"22.04, which found the second: PKCS12KDF is not a FIPS-approved derivation and is absent " +
		"from the FIPS provider, so such a host can neither MAC a PKCS#12 bundle nor verify one, " +
		"and both functions now refuse with the reason and the opt-out rather than passing " +
		"openssl's own unexplained wording through. Not covered: LibreSSL, whose `verify` has no " +
		"-show_chain and whose failure wording is its own, and OpenSSL 1.1.1, whose spelling is in " +
		"the fixtures and on no machine here"},

	"journald": {Level: exec.Hardware, Note: "driven against real systemd 255 on Ubuntu 24.04. The reads " +
		"go through `journalctl -o json` / `-N` / `-F` -- the `jls --libxo=json` precedent (5.32), a machine " +
		"format rather than the aligned columns SPEC's sentence is about -- and `query`, `fields`, " +
		"`field_values`, `list_boots` and `disk_usage` were parsed field by field against the host's own " +
		"journal in the ordinary suite, cursor included. The control verbs `rotate`, `flush` and `sync` were " +
		"run over journald's own varlink socket (`io.systemd.Journal.*`) as root, and the `journalctl --sync` " +
		"fallback was shown to take over when the socket was pointed away (DIVERGENCE 5.52). Not covered: " +
		"`vacuum`, which deletes archived journal files and no test has been willing to on a real machine; the " +
		"varlink error path (a service that answers and refuses); and any systemd older than 255, whose varlink " +
		"interface may be absent -- the fallback exists for exactly that and has only been forced by a bad path"},
	"mdadm": {Level: exec.Hardware, Note: "driven end to end against a real mdadm 4.3 on Ubuntu 24.04: a " +
		"RAID1 with a spare built across three loop devices with `create`, then `fail` -> `remove` -> `add` on a " +
		"member with idempotence checked each way, `save_config` writing an ARRAY line while keeping a MAILADDR " +
		"line, and `stop` -- every step checked against a fresh `mdadm --detail` / `--examine` / `/proc/mdstat` " +
		"read, and `create` over an existing array and over a member that already carries a superblock both shown " +
		"to refuse (DIVERGENCE 5.53). The `--detail` parser is written to mdadm's `Label : Value` header and its " +
		"member table, checked against real degraded output (a removed slot, a faulty member). Not covered: " +
		"`grow` (a reshape is too slow for a test), `assemble --scan` (it reads every superblock on the host), " +
		"RAID levels other than 1, and metadata 0.90"},
	"modprobe": {Level: exec.Hardware, Note: "driven against a real kernel on Ubuntu 24.04: `list`, `info` and " +
		"`is_denylisted` read against whatever this host's kernel already has loaded, and the mutating half " +
		"loaded and unloaded `netdevsim`, the kernel's own simulated networking device for testing (it creates " +
		"no interface merely by loading), with idempotence checked on both load and remove, then wrote and " +
		"removed its own modules-load.d and " +
		"modprobe.d files in a redirected directory (DIVERGENCE 5.54). Not covered: a module with real " +
		"dependents refusing `remove` -- `xt_conntrack`'s dependency chain is checked only against a fixture -- " +
		"and `persist_load`'s options file surviving an actual reboot"},
	"udev": {Level: exec.Hardware, Note: "driven against a real udev (systemd 255) on Ubuntu 24.04: `version`, " +
		"`info` and `list` read the host's own device database and agree with each other on a device found by " +
		"one and queried by the other, and `trigger` and `reload_rules` were run as root against a throwaway " +
		"loop device and the real daemon (DIVERGENCE 5.54). Not covered: a device actually created or removed " +
		"as a result of `trigger` -- the loop device already existed -- and any rule this module's own reload " +
		"picked up"},
	"iptables": {Level: exec.Hardware, Note: "driven against a real iptables 1.8.10 (nf_tables backend) " +
		"inside a throwaway network namespace on Ubuntu 24.04, so nothing touched the host firewall: rules " +
		"appended, inserted, checked for idempotence with iptables' own `-C`, and deleted; user chains created, " +
		"a jump added, the chain flushed and removed; a built-in chain's policy set; and the dangerous-flush " +
		"guard shown to refuse a DROP-policy INPUT and a whole-table flush without force (DIVERGENCE 5.51). " +
		"`live_iptables_test.go` runs in the ordinary suite -- the namespace needs no privilege on a kernel with " +
		"unprivileged user namespaces -- and skips where the kernel forbids one. Not covered: ip6tables (the " +
		"argument vector is pinned but nothing has driven it), the `nat`/`mangle`/`raw` tables, and `save` against " +
		"a real `iptables-persistent` layout"},
	"nftables": {Level: exec.Hardware, Note: "driven against a real nft 1.0.9 inside a throwaway network " +
		"namespace on Ubuntu 24.04: tables and chains created and removed, a base chain's policy updated in " +
		"place, rules added idempotently by their comment and removed by comment, `check` run against nft's own " +
		"`--check`, the flush guards shown to refuse a whole-table and whole-ruleset flush and a drop-policy base " +
		"chain without force, and `save` writing a self-contained restore script (DIVERGENCE 5.51). " +
		"`live_nftables_test.go` runs in the ordinary suite and skips where the kernel forbids an unprivileged " +
		"namespace. Not covered: the `ip`/`arp`/`bridge`/`netdev` families beyond `inet`, sets and maps, and a " +
		"body change to a rule whose comment is unchanged -- which this module deliberately does not apply"},
	"quota": {Level: exec.Hardware, Note: "driven against a real ext4 filesystem with quotas " +
		"switched on, on an Ubuntu 24.04 runner: an ext4 image mounted through the loop driver, " +
		"limits set through the real `setquota` and read back through the real " +
		"`repquota -O csv` -- four distinct numbers in four positions, so a transposed pair would " +
		"have shown as a wrong value rather than as two that match -- and `quotaon -p` read in both " +
		"states (DIVERGENCE 5.48). **The BSD half was written from " +
		"repquota.c and never run, and running the tools found two defects no unit test here " +
		"disagreed with.** `quotaon -p` is a Linux option -- FreeBSD's quotaon has none, so " +
		"`quota.get_mode` could not answer at all on four of this fleet's five hosts, against a " +
		"module comment asserting that both platforms had it; state now comes from the kernel's " +
		"MNT_QUOTA mount flag, which is one flag for both kinds and says so. And a BSD `repquota` " +
		"asked about a filesystem missing from fstab prints its reason on stderr and **exits " +
		"zero**, which the table branch read as a report of no quotas. Both are fixed and both " +
		"have unit tests. **The BSD half has now been run**, against a real UFS " +
		"filesystem on a 64 MiB memory disk on this fleet's own FreeBSD 15.1 host -- the premise " +
		"that an all-ZFS fleet had nowhere to make one was wrong, the same way it was wrong for " +
		"the Linux leg. `edquota -e` really sets four distinct limits in four positions and the " +
		"fixed-width `repquota` parser really reads them back, checked by transposing two of them " +
		"on purpose and watching the test catch it; the second run is idempotent; and `mount` " +
		"really prints the `with quotas` that sys/mount.h spells, in both states. That run also " +
		"turned the MNT_QUOTA limitation from a comment into an assertion: switching **one** kind " +
		"off leaves the flag set, because it covers the whole filesystem, so on a BSD " +
		"`quota.get_mode` cannot confirm that `quota.off` for a single kind took effect. Also not covered: ext4's quota feature route, which the " +
		"Linux live leg falls back to and no runner has needed"},

	"lvm": {Level: exec.Hardware, Note: "driven end to end against a real LVM2 2.03 on Ubuntu " +
		"24.04 (kernel 6.18): two loopback block devices labelled with `pvcreate`, a volume " +
		"group built from one and grown onto the other with `vgcreate`/`vgextend`, a logical " +
		"volume carved and grown with `lvcreate`/`lvresize`, and the whole stack torn down " +
		"through `lvremove` and the `vg_absent`/`pv_absent` states -- each step checked against " +
		"a fresh `pvs`/`vgs`/`lvs` read rather than the module's own answer, and the shrink " +
		"guard shown to refuse a smaller size before `lvresize` was called (DIVERGENCE 5.50). " +
		"That run found a defect in the live harness -- a `[]string` device list reaching the " +
		"module as one bracketed argument -- rather than in the module. Not covered: thin pools " +
		"and thin volumes (the argument vectors are pinned but nothing has built one), striping, " +
		"and any filesystem on top of a volume, so `--resizefs` is still an argument only"},

	"apparmor": {Level: exec.Hardware, Note: "driven end to end on a real Ubuntu 24.04 host " +
		"whose apparmor-utils 4.0.1 can parse its own profile tree (no `passt` package, " +
		"so none of DIVERGENCE 5.37's unparseable abstractions/passt is present): 148 real " +
		"profiles read correctly across all four modes, and a throwaway profile loaded, " +
		"moved through complain and back to enforce by name via the real `aa-complain`/ " +
		"`aa-enforce`, disabled with its reboot-persistence symlink checked, and unloaded " +
		"-- each step confirmed against securityfs rather than against what the tool " +
		"printed. `apparmor.mode` converges and predicts correctly in test mode. 5.37's " +
		"finding stands as a fact about that other machine, not about this module: the " +
		"`aa-*` tools remain unusable on any host with that package installed, and " +
		"`apparmor.status` still reports `tools: false` with the reason there rather than " +
		"claiming a binary on PATH can do something it cannot. Also run end to end, all five " +
		"live tests and none skipped, on the GitHub ubuntu-24.04 runner image 20260920.314.1 " +
		"(Fleet run 36042192549, 2026-09-24), once that image's three unparseable profile " +
		"faults were removed by the workflow; the module now detects such a tree by the one " +
		"answer a working tool gives rather than by a list of failures (DIVERGENCE 5.133)"},

	// ---- Read from a real system, mutation never watched ----

	"kmod": {Level: exec.Captured, Note: "the readers were compared against the Salt on " +
		"this project's reference host, function for function: `mod_list` returned the same " +
		"94 loaded modules and `available` the same 1440, from /proc/modules and " +
		"/lib/modules directly rather than by parsing lsmod's columns. The comparison found " +
		"one difference and it is Salt's: Salt normalises hyphens to underscores for " +
		"loadable modules and not for built-in ones, so its `check_available('amba-pl011')` " +
		"is true and `check_available('amba_pl011')` is false for the same module. " +
		"**Nothing has watched this module change anything.** No module has been loaded or " +
		"unloaded by it -- `modprobe` and `modprobe -r` are unrun, on the reasoning that " +
		"unloading a protocol module on the host serving this project's own Salt is not a " +
		"test, it is an outage. The persistence half writes real files, but only ones a " +
		"test made; neither /etc/modules nor /etc/modules-load.d has been touched"},

	"dnsutil": {Level: exec.Captured, Note: "`parse_hosts` was compared against Salt's on " +
		"this host's real /etc/hosts and returned the same eight addresses with the same " +
		"names, and `A` and `AAAA` resolve through the real resolver. The mutating half -- " +
		"`hosts_append` and `hosts_remove` -- has written only files a test made, never the " +
		"machine's own /etc/hosts. It shares the `hosts` module's parser and writer, which " +
		"is where the comment and blank-line handling is demonstrated"},

	"reboot": {Level: exec.Captured, Note: "the readers are demonstrated against the real " +
		"FreeBSD host this was written on: `required` compares freebsd-version's installed " +
		"and running kernels, `scheduled` reads the real process table **through the `ps` " +
		"module's own reader**, and `last_boot` reads kern.boottime. `scheduled` built its " +
		"own `ps` command until Alpine showed why that is wrong -- a second invocation of a " +
		"tool another module already wraps, in a spelling BusyBox refuses, answering " +
		"\"nothing is pending\" on every Alpine node rather than erroring. Before that, " +
		"`scheduled` did **not** read the table at all until a live test drove " +
		"it against a real process, because it asked ps for `-o pid=,command=`, the Linux " +
		"idiom, which FreeBSD's ps parses as a single column headed with the literal string " +
		"`,command=`. It exited 0 and printed bare pids, so the finder matched nothing on " +
		"every FreeBSD node and nothing ever errored. **The FreeBSD cancel path is now " +
		"demonstrated end to end** -- a real ps, a real parse of what it printed, and a real " +
		"SIGTERM to a real process that really died -- against a stand-in named `shutdown` " +
		"rather than a genuine one. The cancel's platform split is held to FreeBSD's own " +
		"shutdown(8) manual source, which ships on every install and is read by a test " +
		"rather than trusted from memory. **The mutating paths have never been watched " +
		"working.** `schedule` and `cancel` need root on a machine that may be taken down, " +
		"and the one live test that drives them is gated behind HALITE_SYSTEM_LIVE=1 *and* " +
		"HALITE_REBOOT_LIVE=1, which no run has yet set, so no genuine `shutdown(8)` has " +
		"been scheduled or countermanded by this module. What that gap cost once is worth " +
		"recording: this module shipped `shutdown -c` as the cancel on both platforms, " +
		"because it is the cancel on Linux and the same flag is listed in FreeBSD's usage " +
		"line -- where it means *power cycle the machine*, and is honoured on any host with " +
		"a BMC the ipmi(4) driver supports, which is what this fleet runs. The flag was " +
		"checked for existence and never for meaning. The only power-cycle in this host's " +
		"entire syslog history is dated the evening the module was written; which " +
		"invocation passed the flag is not recoverable from the logs, and notably the " +
		"module's own argv could not have been it, because bare `shutdown -c` supplies none " +
		"of the mandatory `time` argument shutdown(8) requires and would have exited with " +
		"its usage line instead. So FreeBSD was carrying two defects in one command, and " +
		"neither was reachable by any test that reads a fixture",
	},
	"system": {Level: exec.Captured, Note: "the argv table for `halt`, `poweroff`, " +
		"`shutdown` and `reboot` is derived from the real usage lines of this host's own " +
		"setuid /sbin/shutdown and /rescue/date, not from memory of what they accept, and " +
		"two live tests hold it there by invoking each binary in a form that cannot act: " +
		"`shutdown` with no arguments, which its own mandatory `time` argument makes a " +
		"usage error, and `date --help`, which BSD date refuses. **Nothing has watched " +
		"this module change anything, and that is deliberate rather than an omission.** " +
		"Every mutating path here takes the machine running the test off the network or " +
		"steps its clock: test mode reaches `c.Run` zero times for the four power verbs, " +
		"and no test in this package runs `halt`, `poweroff`, `reboot`, `shutdown`, `init` " +
		"or a `date` that supplies a value, on any host. `set_computer_desc` is the one " +
		"mutating path here that is safe and reversible, and it is unexercised for a " +
		"different reason: it writes systemd's /etc/machine-info and is declared Linux " +
		"only, while the host this module was developed on is FreeBSD",
	},

	"win_service": {Level: exec.Captured, Note: "reads the real service control manager " +
		"through its API on every Windows run and converges against what it finds, but " +
		"nothing has watched this module start, stop or re-type a service"},
	"jail": {Level: exec.Hardware, Note: "the shape of `jls --libxo=json` is checked " +
		"against the real jls on CI's FreeBSD runner, and **the field names inside a jail " +
		"entry are no longer assumed**: every key this module subscripts out of an entry is " +
		"checked against the parameter list `jls -h` publishes, on a real FreeBSD 15.1 host. " +
		"That audit found one it had invented -- it read a `state` field, which is not a jail " +
		"parameter and which no jls has ever printed, against a fixture that supplied " +
		"`\"state\": \"ACTIVE\"` in the module's own spelling -- so every real host reported " +
		"an empty state and every test agreed. State is now derived from `dying`, which is a " +
		"real parameter. **The mutating half is demonstrated too**: on the same host a " +
		"jail was defined, started through `jail.start`, found in a raw `jls` rather than " +
		"through this module's own reader, and stopped through `jail.stop`; and the " +
		"`jail.running` state converged, reported no change on a second run and stopped it " +
		"again. Both were checked by breaking them on purpose. That run found a third defect: " +
		"`jail -e` prints each configured jail's **parameters**, not a list of names, and this " +
		"module split the whole output on the separator and took every field as a name -- so " +
		"`jail.running` answered \"is not defined in jail.conf\" for every jail that was in " +
		"fact defined and **could never start one**. Invisible until now because the fleet's " +
		"own host has no jails in jail.conf, where the empty answer looks correct " +
		"(DIVERGENCE 5.32, 5.66, 5.70). **The three gaps 5.70 named are closed** (5.71): " +
		"`jail.restart` is driven against a real jail and checked by its jid changing, because " +
		"a restart that did nothing would pass any check that only asked whether the jail is up " +
		"afterwards; a jail with an `ip4.addr` of its own starts and its address reads back " +
		"through `jail.show_config`; and a jail with a live process in it is stopped and the " +
		"process goes with it. That last test was wrong first and passed anyway -- `jexec` runs " +
		"a binary from inside the jail rather than from the host, so nothing had ever been " +
		"running in it, and `ps -J` was catching the short-lived `jexec` process itself. Not " +
		"covered: `jail.conf` includes and variables, a jail with a vnet of its own rather than " +
		"an address alias, and `jail -m` to modify a running jail in place. **All five of " +
		"those live tests now run on every `freebsd` leg** rather than only on a host somebody " +
		"raised by hand: they used to skip on a machine with no /etc/jail.conf, which is every " +
		"fresh FreeBSD, so the mutating half ran only where jails already existed " +
		"(DIVERGENCE 5.123)"},
	"mount": {Level: exec.Captured, Note: "reads the real /proc/self/mounts and the real " +
		"`mount` output on the platforms CI runs, and nothing has been mounted or " +
		"unmounted by this module"},
	"acl": {Level: exec.Hardware, Note: "driven against the real getfacl and setfacl on this " +
		"fleet's FreeBSD 15.1 host, and on 14.5 and 15.1 in the lab. The mutating half needs no root -- an ACL is set on a file " +
		"the test owns -- so the round trip runs wherever the suite does. Two things were " +
		"learned from the tool rather than assumed, and both are in the module's comment: " +
		"`setfacl -m` on a new (tag, qualifier) pair inserts at the **front** rather than " +
		"appending, and an existing entry is matched for update by (tag, qualifier, type) " +
		"together -- so a `deny` for an already-`allow`ed user is a second entry, not a " +
		"replacement. The permission and flag column order was derived empirically, each of the " +
		"14 permission letters and 7 flag columns set alone against a scratch file and the real " +
		"output captured, which is what lets the dry run compare canonical forms instead of " +
		"guessing. **This is NFSv4 only**: ZFS is what this fleet has, Linux's tool of the same " +
		"name speaks a different grammar, and a POSIX.1e-shaped entry is refused by name rather " +
		"than misread -- confirmed as root against a real UFS filesystem built on a memory disk " +
		"and mounted with `-o acls`. **The NFSv4 round trip runs on every `freebsd` leg** now, " +
		"rather than only where the temp directory happens to be ZFS: which family a path " +
		"speaks is a property of the filesystem, so the test builds a UFS filesystem mounted " +
		"`-o nfsv4acls` when it needs one, and before that the mutating half ran on one " +
		"machine and in no CI leg (DIVERGENCE 5.123). Not covered: Linux, which needs its own " +
		"captured fixtures and a host to take them from"},
	"tmpfs": {Level: exec.Captured, Note: "read against the real `mount` and `df` on this " +
		"fleet's FreeBSD 15.1 host, both against the tmpfs the host already had and against one " +
		"the test mounted as root and then unmounted, with the reader shown to flip in both " +
		"directions. **It reads and never writes, by design**, so Captured is its ceiling rather " +
		"than a shortfall: mounting a tmpfs is `mount.mount` with a fstype, and a wrapper here " +
		"would duplicate `mount.mounted`'s idempotence and fstab handling for no gain. " +
		"`tmpfs.resize` is deliberately absent too -- Linux's live resize is `mount.remount`, and " +
		"FreeBSD's tmpfs(4) documents `size` only at mount time, so shipping one would mean " +
		"claiming a behaviour nothing here has verified. The unit fixtures are real unedited " +
		"`mount` and `df -Pk` captures that happen to disagree about this host's own tmpfs mount " +
		"point, a Linux-compat artifact, and that disagreement is kept as a test proving the " +
		"join degrades to no usage figures rather than to a wrong number"},
	"at": {Level: exec.Hardware, Note: "driven against the real at/atq/atc/atrm on this " +
		"fleet's FreeBSD 15.1 host, as root: a job scheduled far enough ahead that it never " +
		"fires, found in a real `atq`, its script read back through a real `at -c`, removed, and " +
		"the removal shown idempotent; the `at.present`/`at.absent` pair round-trips an " +
		"identified job without duplicating it. **The queue parser was written without a real " +
		"`atq` to read** -- `at` refuses an unprivileged caller on this host, so it was derived " +
		"from the printf format in the binary itself (`%s\\t%-16s%c%s\\t%ld`), which says the " +
		"job number is the last field. That inference is now confirmed against real output, and " +
		"checked by reading the first field instead on purpose and watching the test fail. Not " +
		"covered: Linux's at, whose argument vector is pinned in the platform table and which no " +
		"leg has run; and a job actually firing, which nothing here waits for"},
	"swap": {Level: exec.Hardware, Note: "driven against the real swapon/swapoff on this " +
		"fleet's FreeBSD 15.1 host: swap made on a 64 MiB memory disk, switched on and off " +
		"through this module, and **confirmed each time through `mount.swaps` rather than " +
		"through this module's own reader** -- a writer and a reader wrong in the same direction " +
		"would otherwise agree with each other. Idempotent in both directions, and the FreeBSD " +
		"refusal of a priority was shown to refuse rather than to drop the argument silently. " +
		"Checked by making swapoff a no-op and watching the test catch it. The host had no swap " +
		"at all to begin with, so the empty reading is exercised too. Not covered: Linux, whose " +
		"`mkswap`/`swapon -p` path is a platform-table row nothing has run; and persistence, " +
		"which is deliberately `mount.mounted`'s job rather than this module's"},
	// `sudo` reads and never writes, which is why `Captured` is its
	// ceiling rather than a shortfall. There is no mutating path to
	// demonstrate: a sudoers file is written by `file.managed`, and what
	// this module adds is the check that runs *before* that write.
	"sudo": {Level: exec.Captured, Note: "driven against the real sudo and visudo 1.9.17p2 on " +
		"this fleet's FreeBSD 15.1 host. `sudo.validate` runs the real `visudo -c` over files a " +
		"test writes -- one the grammar accepts, one it rejects, and one that is not there -- " +
		"and each answer carries visudo's own words; this needs no privilege, which is the " +
		"point of it. `sudo.path` was read **both ways on the same host**: unprivileged it falls " +
		"back to the platform's conventional location and says so, and as root it comes from " +
		"`sudo -V` itself, so neither branch of the fallback is assumed. `sudo.list` was read as " +
		"root against a real account, and a missing account refused. Every assertion was checked " +
		"by breaking the code and watching it fail. **This module writes nothing by design** -- " +
		"no sudoers parser is written here, because a second parser for that grammar would " +
		"eventually disagree with the real one about who may become root. Not covered: Linux, " +
		"where the conventional path differs and no CI leg reads it as root; and Salt's " +
		"`sudo.salt_call`, deliberately not built, because `cmd.run` already takes a `runas` and " +
		"applies it with setuid rather than through a second privilege system"},
	"sysrc": {Level: exec.Captured, Note: "reads real rc.conf through the real `sysrc` on " +
		"CI's FreeBSD runner; nothing has watched this module write one"},
	// `pam` has no tool to drive, which is why its note reads
	// differently from every other one here. PAM is a library the login
	// programs link, not a program this module runs, so there is no
	// output to capture and no command whose spelling could be wrong.
	// The risk moves entirely into the file format, and that is what
	// the live test corpus is.
	"pam": {Level: exec.Captured, Note: "every service file on the machine running the tests " +
		"is parsed and checked against the file rather than against an expectation -- 13 real " +
		"services and 53 real control flags on FreeBSD 15.1, 33 services and 191 flags (420 " +
		"rules through Debian's own @include fan-out) on Ubuntu 24.04, and whatever the Linux " +
		"and macOS CI legs have -- and the sweep is cross-checked against the per-service " +
		"answer. Nothing has watched this module write to a real /etc/pam.d: the mutating half " +
		"runs only against a throwaway tree, deliberately, because a wrong line there locks " +
		"every account out of the node and a test is not a thing to find that out with"},

	"user": {Level: exec.Captured, Note: "reads go through os/user against the real " +
		"account database, and no account has been created, changed or removed on a real " +
		"machine by this module"},

	"snap": {Level: exec.Captured, Note: "read against the real snapd 2.76.3 on Ubuntu " +
		"22.04 and 26.04, which is what the fixtures had never been: they were written " +
		"from the documented columns, and the documentation is wrong about two of them. " +
		"A real `snap list` prints the publisher with **two** asterisks, and it " +
		"**truncates a long channel with U+2026 -- which `--unicode=never` does not " +
		"stop**, and for which `snap list` offers no option at all. `lxd` on 22.04 " +
		"reported `5.0/stable/\u2026` where its channel is `5.0/stable/ubuntu-22.04`, so " +
		"`snap.installed` compared a declared channel against a prefix, never matched, " +
		"and would have run a real refresh on every run while reporting a change every " +
		"time -- a state that cannot converge. The channel is resolved through `snap " +
		"info` now, and `live_snap_test.go` checks every row against what snapd itself " +
		"says rather than against this build's own reader. **The mutating half is still " +
		"unwatched**: install, remove and refresh pull from the store, take a squashfs " +
		"mount and a service, and removal can take data with it, so no test drives them " +
		"on an unattended machine. What is demonstrated is the reading, which is where " +
		"the defect was (DIVERGENCE 5.28)"},

	// ---- Mutated a real Mac, by hand, because no CI leg is one ----

	"mac_keychain": {Level: exec.Hardware, Note: "a real self-signed certificate was imported, " +
		"found, and removed against the real `security` on macOS 27.0 (build 26A5425a). Two " +
		"halves: `live_mac_keychain_root_test.go` round-trips a keychain the test makes -- " +
		"`friendly_name` through the real `openssl`, `install`, `find-certificate`, a second " +
		"`install` that neither errors nor duplicates, `uninstall`, and `uninstall` again on " +
		"what is already gone -- and that half **needs no root**, so a Mac in CI could run " +
		"it; and a second test imports into and removes from the real " +
		"`/Library/Keychains/System.keychain` under sudo, which is the path that makes these " +
		"functions declare root. Running it found two defects (DIVERGENCE 5.118), one of " +
		"which only appears when the caller is root -- which is what a node is. Not covered: " +
		"the `-T` application access list, and `install` into a keychain that is locked"},

	"mac_power": {Level: exec.Hardware, Note: "the setters were driven against the real " +
		"`pmset` on an Apple M1 running macOS 27.0 (build 26A5425a) under sudo, capturing " +
		"the machine's own policy first and restoring every value after -- per power source " +
		"with `pmset -c`/`-b`/`-u`, because the module writes `pmset -a` and a restore " +
		"through it would flatten a laptop's differing AC and battery profiles. Round-tripped: " +
		"`display_sleep` and `wake_on_network` individually, and `computer_sleep`, " +
		"`display_sleep` and `harddisk_sleep` through the combined `set_sleep`. **Weaker for " +
		"one setting**: `set_sleep_on_power_button` is demonstrated only as *refusing " +
		"correctly*. `pmset -g custom` prints `Sleep On Power Button`, this `pmset` documents " +
		"no key to write it and rejects `-a powerbutton` at argument parsing, and the write " +
		"path for that setting is demonstrated nowhere -- on Apple silicon there may be no " +
		"way to demonstrate it (DIVERGENCE 5.116). Not driven at all: `restart_power_failure`, " +
		"and `wake_on_modem`, which this Mac does not report"},

	"mac_user": {Level: exec.Hardware, Note: "an account was created, converged on, changed " +
		"and removed on a real Mac (macOS 27.0, build 26A5425a) under sudo in " +
		"`live_mac_account_test.go`: the `dscl . -create` sequence and `createhomedir` made " +
		"an account this module's own reader then found with the uid, home, shell, real name " +
		"and supplementary group asked for, a second `user.present` with the same spec " +
		"changed nothing, a shell change was seen and then converged, and `user.absent` with " +
		"`purge` removed the record and the home and was then a no-op. Not covered: the " +
		"`system`/IsHidden path, an explicit uid and the `unique` refusal, `usergroup`, and " +
		"group *removal* -- `diffAccount` is append-only by design, so a group dropped from a " +
		"tree's list is never taken off the account (DIVERGENCE 5.115)"},
	"mac_group": {Level: exec.Hardware, Note: "created through the real `dseditgroup` on macOS " +
		"27.0 (build 26A5425a) under sudo, read back with a gid, converged on a second " +
		"`group.present`, then removed and converged again (DIVERGENCE 5.115). Not covered: " +
		"an explicitly requested gid, and the refusal to renumber a group that exists with a " +
		"different one -- which is the branch that protects every file the group owns"},
	"mac_shadow": {Level: exec.Hardware, Note: "`dscl . -passwd` set a real password on a real " +
		"account on macOS 27.0 (build 26A5425a) under sudo, and Open Directory reported the " +
		"account's password as set afterwards where it had not been before (DIVERGENCE " +
		"5.115). The standing limit is not a gap in testing: `info` can report whether a hash " +
		"is present and can never compare one, because dscl does not expose it. The password " +
		"is passed as an argv and is visible in `ps` while the call runs"},

	"mac_defaults": {Level: exec.Hardware, Note: "driven end to end against the real " +
		"`defaults` on macOS 27.0 (build 26A5425a), including the two paths that made " +
		"every mutating function here declare root and that nothing had ever run: a " +
		"write as another account, which is setuid/setgid through the command's " +
		"`RunAs`, checked to have landed in *that* account's preference store and not " +
		"in root's; and a machine-wide domain under `/Library/Preferences`, written, " +
		"read back, and emptied. Running it found two defects a unit test could not " +
		"(DIVERGENCE 5.114). What is still unwatched is `user` naming an account other " +
		"than the invoking one -- it was driven as the account behind `sudo`, which " +
		"exercises the same setuid path but not a second real login"},

	// ---- Never pointed at the tool it drives ----

	"mac_softwareupdate": {Level: exec.Hardware, Note: "**the mutating surface is `--download` " +
		"alone**: `update` and `update_all` are registered and refuse, because installing " +
		"restarts the machine and this project cannot demonstrate an install path on any Mac " +
		"it has (DIVERGENCE 5.117). What is left was demonstrated on 2026-09-18 on macOS " +
		"15.7.9 (build 24G830, arm64), under `sudo` on the `macos` leg of `fleet.yml`: " +
		"`list_available` asked Apple's real service, `mac_softwareupdate.download` fetched " +
		"`Safari27.0SequoiaAuto-27.0` in 20 seconds, and `sw_vers -productVersion` reported " +
		"the same version afterwards -- which is the claim the module is built around, a " +
		"download not being an install. The reads were demonstrated with it: the real " +
		"schedule state and the /Library/Updates index. What is *not* demonstrated is an " +
		"install, deliberately and permanently; nothing here drives one. `ignore`, " +
		"`list_ignored` and `reset_ignored` describe a `softwareupdate` option macOS removed " +
		"and refuse by name"},
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
		// One named predicate, not a substring over prose. `cmd`
		// declares "whatever the command needs", which contains no
		// "root" -- so the module that runs arbitrary code was outside
		// the gate written to catch a module nobody had considered, and
		// had no evidence row at all. DIVERGENCE 5.135.
		if sig.NeedsPrivilege() {
			needsRoot[module] = true
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
