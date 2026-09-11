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
// `Assumed` — `apparmor`, `snap`, and the macOS row (`mac_defaults`,
// `mac_power`, `mac_user`, `mac_group`, `mac_shadow`,
// `mac_softwareupdate`, `mac_keychain`, `mac_assistive`) — says why in its own note, and the release gate refuses to ship while any is. The
// macOS modules each have a live test that reads the real tool, but
// their mutating paths change a Mac's own state and no CI leg is a Mac.
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
		"problem with a different fix. Not covered: LibreSSL, whose `verify` has no -show_chain and " +
		"whose failure wording is its own, and OpenSSL 1.1.1, whose spelling is in the fixtures and " +
		"on no machine here"},

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
		"states (DIVERGENCE 5.48). The BSD fixed-width parser is written to the printf calls in " +
		"FreeBSD 15.1's own usr.sbin/repquota/repquota.c, and **it has not been run**: this project's " +
		"fleet is entirely ZFS and has no UFS filesystem to make one on, so `edquota -e` is still " +
		"checked only as an argument vector. Also not covered: ext4's quota feature route, which the " +
		"live leg falls back to and no runner has needed"},

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
	// `pam` has no tool to drive, which is why its note reads
	// differently from every other one here. PAM is a library the login
	// programs link, not a program this module runs, so there is no
	// output to capture and no command whose spelling could be wrong.
	// The risk moves entirely into the file format, and that is what
	// the live test corpus is.
	"pam": {Level: exec.Captured, Note: "every service file on the machine running the tests " +
		"is parsed and checked against the file rather than against an expectation -- 13 real " +
		"services and 53 real control flags on FreeBSD 15.1, and whatever the Linux and macOS " +
		"CI legs have -- and the sweep is cross-checked against the per-service answer. Nothing " +
		"has watched this module write to a real /etc/pam.d: the mutating half runs only against " +
		"a throwaway tree, deliberately, because a wrong line there locks every account out of " +
		"the node and a test is not a thing to find that out with"},

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
	"mac_power": {Level: exec.Assumed, Note: "the `pmset -g custom` parser was built against " +
		"output captured by hand on macOS 26, and `live_mac_power_test.go` reads the real " +
		"`pmset` -- but the setters run `pmset -a`, which needs root and changes a real Mac's " +
		"power policy, so no test drives them and no CI leg is a Mac. Nothing has watched a " +
		"`set_*` converge"},
	"mac_keychain": {Level: exec.Assumed, Note: "`security list-keychains`, `default-keychain` " +
		"and `find-certificate -a -Z` are read against the real `security` in " +
		"`live_mac_keychain_test.go`, field by field on this host's own keychains. `import` " +
		"and `delete-certificate` change a keychain and need root for a system one, so nothing " +
		"has watched a certificate go in or out; `friendly_name` shells to `openssl` and has " +
		"only been read"},
	"mac_assistive": {Level: exec.Assumed, Note: "`live_mac_assistive_test.go` reads the real " +
		"`access` table of this host's `/Library/Application Support/com.apple.TCC/TCC.db` through " +
		"the real `sqlite3` and parses the Accessibility rows field by field. The writes -- " +
		"`install`, `enable`, `remove` -- go to a database System Integrity Protection makes " +
		"readonly for any process without Full Disk Access, root included, so nothing here has " +
		"watched a grant be added or removed and no CI leg is a Mac with the entitlement"},
	"mac_softwareupdate": {Level: exec.Assumed, Note: "the `softwareupdate --list` parser was " +
		"built against real output captured on macOS 26, and `live_mac_softwareupdate_test.go` " +
		"reads the real schedule state and the downloaded-updates plist. Nothing has installed " +
		"or downloaded an update through it: `softwareupdate --install` needs root, reboots the " +
		"machine, and no CI leg is a Mac. `ignore`, `list_ignored` and `reset_ignored` describe " +
		"a `softwareupdate` option macOS removed and refuse by name"},
	"mac_user": {Level: exec.Assumed, Note: "reads are demonstrated -- `live_mac_user_test.go` " +
		"parses a real `dscl -plist . -read` and `dscl . -list` on this host, and the virtual " +
		"`user.present` predicts a creation in test mode against it. The writes -- the " +
		"`dscl . -create` sequence, `createhomedir`, the recursive home removal -- need root " +
		"and change Open Directory, so nothing has watched an account be created or removed"},
	"mac_group": {Level: exec.Assumed, Note: "`macGroupInfo` reads a real group through " +
		"`dscl -plist . -read` in `live_mac_user_test.go`; the `dseditgroup` writes need root " +
		"and have not been run"},
	"mac_shadow": {Level: exec.Assumed, Note: "`dscl . -passwd` is documented and matches what " +
		"Salt runs, but nothing here has set a real password, and `info` can only ever report " +
		"whether a hash is present, not compare one -- dscl does not expose it"},
	"mac_defaults": {Level: exec.Assumed, Note: "the plist reader and writer were built " +
		"against `defaults export` and `defaults write` output captured by hand on macOS " +
		"26, and `live_mac_defaults_test.go` drives the real `defaults` against a private " +
		"throwaway domain -- but only behind HALITE_SYSTEM_LIVE=1, and no CI leg runs on " +
		"a Mac that writes preferences, so nothing has watched this module converge " +
		"unattended. The `user` path, which becomes another account to reach its domain, " +
		"has not been run at all"},
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
