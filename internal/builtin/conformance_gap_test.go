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
	// One entry of its own, because its reason is neither of the two below:
	// the state is reachable and its effect is confined to a path, but the
	// harness cannot stage a *change* for it on any machine available.
	unconformed["win_dacl.owner"] = "reachable and confined to a path, but no change can be staged: " +
		"an owner change needs a trustee Windows will accept as an owner, and on the runners " +
		"available it refused both Administrators and SYSTEM with ERROR_INVALID_OWNER. " +
		"The only assignable owner left is the account that " +
		"already owns the file, which is no change. win_dacl_windows_test.go drives SetOwner directly."

	// The `firewall` module's four states have cases against `ufw`, and the
	// `pf` provider has none. Not an entry -- the functions are covered -- but
	// worth a line where somebody counting will read it: `pf`'s SetDefault
	// refuses by design, since pf has no per-direction default policy, so a
	// `firewall.enabled` case on FreeBSD would exercise `pfctl -e` and nothing
	// more; and enabling a packet filter on a host reachable only over SSH to
	// cover one line is a trade this suite should not make unasked. pf's rule
	// path rewrites a whole anchor, which is different enough to want a case of
	// its own rather than sharing ufw's.

	// Changes the machine the suite runs on. The harness applies twice,
	// for real, and this project's development host is a node the fleet
	// manages -- so these belong to `TestLive*` and the lab, where the
	// machine is disposable.
	machine := "changes the machine the suite runs on; the harness applies for real, so this " +
		"belongs to a TestLive case on a disposable host"
	for _, n := range []string{
		"apparmor.mode",
		"debconf.set", "hostname.system",
		"jail.running", "netplan.managed",
		"pkg.latest", "pkg.purged", "pkgrepo.absent", "pkgrepo.managed", "reboot.scheduled",
		"snap.installed", "snap.removed", "timezone.system",
	} {
		unconformed[n] = machine
	}
}
