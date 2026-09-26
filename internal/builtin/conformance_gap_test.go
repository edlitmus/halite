package builtin

// unconformed is every state function with no conformance case, and why.
//
// # What this table is for
//
// `internal/states`'s package comment used to say the SPEC 11.6 harness is
// one "which every state module must pass". Eighteen of a hundred and
// thirty-two had a case when this table was written, so the sentence was an
// intention rather than a fact, and nothing said which the other hundred
// and fourteen were. An unbounded gap nobody can enumerate is worse than a
// long list: the list can be shortened deliberately, and its entries can be
// argued with.
//
// It has been shortened once since. The seventeen that said "reachable
// in-process and not yet written" are gone, which took thirty-five of a
// hundred and thirty-two to a case and left ninety-seven here. Six of the
// seventeen needed the harness widened rather than the case written:
// `test.nop`, `cmd.wait` and their kind change nothing at all, and every
// phase of the harness assumed a state with work to do. See
// `states.Conformance.Unchanging`. The accounting subtest counts those six
// rather than trusting this sentence, because this sentence said eleven
// first.
//
// # Why most of these cannot simply be written
//
// The harness *applies* the state, twice, for real. That is the point of it
// — idempotence is not observable any other way — and it is also the limit.
// A case can exist for a function whose whole effect lands inside a
// directory the test owns. `file.recurse` turned out to qualify once a
// local `fileserver.Fetcher` was recognised as a file server; `pkg.installed`
// and `service.running` never will, because the machine running the suite
// is, on this project, a host the fleet manages.
//
// So the honest split is by *what the function touches*, and that is how the
// two remaining reasons below are grouped. The first is reachable with work
// and says what the work is; the second needs a machine nobody minds
// breaking, which is what `TestLive*` and the lab are for.
//
// DIVERGENCE 5.157.
var unconformed = map[string]string{}

func init() {
	// Writes a file the node itself reads, so a case needs the node's own
	// roots redirected rather than a bare temp directory.
	node := "applies to this node's own configuration, so a case needs its roots redirected first"
	for _, n := range []string{
		"grains.absent", "grains.present", "environ.setenv",
		"beacon.absent", "beacon.present", "schedule.absent", "schedule.present",
		"event.send",
		"saltutil.sync_all", "saltutil.sync_beacons", "saltutil.sync_grains",
		"saltutil.sync_modules", "saltutil.sync_renderers", "saltutil.sync_returners",
		"saltutil.sync_states",
	} {
		unconformed[n] = node
	}

	// Changes the machine the suite runs on. The harness applies twice,
	// for real, and this project's development host is a node the fleet
	// manages -- so these belong to `TestLive*` and the lab, where the
	// machine is disposable.
	machine := "changes the machine the suite runs on; the harness applies for real, so this " +
		"belongs to a TestLive case on a disposable host"
	for _, n := range []string{
		"apparmor.mode", "at.absent", "at.present", "cron.absent", "cron.present",
		"debconf.set", "firewall.absent", "firewall.allowed", "firewall.denied",
		"firewall.enabled", "gem.installed", "gem.removed", "group.absent",
		"group.present", "host.absent", "host.present", "hostname.system",
		"iptables.append", "iptables.chain_absent", "iptables.chain_present",
		"iptables.delete", "iptables.flush", "iptables.insert", "iptables.set_policy",
		"jail.running", "kmod.absent", "kmod.present",
		"lvm.lv_absent", "lvm.lv_present", "lvm.pv_absent", "lvm.pv_present",
		"lvm.vg_absent", "lvm.vg_present",
		"mac_defaults.absent", "mac_defaults.write", "mount.mounted", "mount.unmounted",
		"netplan.managed",
		"nftables.append", "nftables.chain_absent", "nftables.chain_present",
		"nftables.delete", "nftables.flush", "nftables.insert", "nftables.set_policy",
		"nftables.table_absent", "nftables.table_present",
		"npm.installed", "npm.removed", "pip.installed", "pip.removed",
		"pkg.installed", "pkg.latest", "pkg.purged", "pkg.removed",
		"pkgrepo.absent", "pkgrepo.managed", "reboot.scheduled",
		"service.dead", "service.disabled", "service.enabled", "service.running",
		"snap.installed", "snap.removed", "sysctl.present",
		"sysrc.absent", "sysrc.managed", "sysrc.present", "timezone.system",

		"user.absent", "user.present",
		"win_dacl.absent", "win_dacl.inherit", "win_dacl.owner", "win_dacl.present",
		"win_service.start_type", "win_task.absent", "win_task.present",
		"zfs.absent", "zfs.filesystem_present", "zpool.absent", "zpool.present",
	} {
		unconformed[n] = machine
	}
}
