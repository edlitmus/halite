package builtin

// unconformed is every state function with no conformance case, and why.
//
// # What this table is for
//
// `internal/states`'s package comment says the SPEC 11.6 harness is one
// "which every state module must pass". Eighteen of a hundred and
// thirty-two had a case when this table was written, so the sentence was an
// intention rather than a fact, and nothing said which the other hundred
// and fourteen were. An unbounded gap nobody can enumerate is worse than a
// long list: the list can be shortened deliberately, and its entries can be
// argued with.
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
// reasons below are grouped. Three of the groups are reachable with work and
// say so; the rest need a machine nobody minds breaking, which is what
// `TestLive*` and the lab are for.
//
// DIVERGENCE 5.157.
var unconformed = map[string]string{}

func init() {
	// Reachable, and worth doing next: the effect is confined or absent,
	// and only the arrangement is missing.
	reach := "reachable in-process and not yet written: its effect is confined to a path or to nothing"
	for _, n := range []string{
		"test.configurable_test_state", "test.fail_without_changes", "test.nop",
		"test.show_notification", "test.succeed_without_changes",
		"archive.extracted", "x509.certificate_managed", "git.latest",
		"cmd.run", "cmd.script", "cmd.wait", "module.run", "module.wait",
		"ssh_auth.absent", "ssh_auth.present",
		"ssh_known_hosts.absent", "ssh_known_hosts.present",
	} {
		unconformed[n] = reach
	}

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
