// Package chaos is SPEC 31's chaos layer: the registry of the scenarios
// the specification names, what each is defined to do, and the guard
// that holds the two to each other.
//
// The layer was entirely absent until 2026-09-06 — `grep -i chaos` over
// the tree returned nothing — and the argument for building it is not
// that SPEC asks for it. It is that three defects of one shape were
// found in a fortnight, all three by accident:
//
//   - the webhook returner's spool named files by a timestamp that is
//     not unique, so a backlog went upstream out of order (4.9);
//   - the relay spool did the same and silently overwrote, losing a
//     return in the mechanism whose whole purpose is not to (4.9);
//   - the hub's job record was read, changed and written back by two
//     goroutines, so a queued job could be delivered to a node twice
//     (4.11).
//
// Each is correct read one operation at a time and wrong when two
// arrive together. None of them had a test with two of anything in it.
// The first two were found by inspection while looking at something
// else; the third was an intermittent CI failure written off as a flake
// for a week. That is not a run of bad luck, it is the absence of this
// layer, and the fourth defect of the shape is presumably in the tree
// now.
//
// # What a scenario is
//
// SPEC 31's Chaos row names eight and says each must have "a defined,
// tested, documented behaviour". Three words, and the order matters:
// the behaviour is decided first and written down here, then a test
// holds the build to it. A scenario whose Behaviour reads "it depends"
// has not been through the first step.
//
// Registering one here does not make it true. `chaos_test.go` checks
// that every scenario SPEC names is registered, that nothing is
// registered that SPEC does not name, and that each has a test that
// says it exercises it. What none of that checks is whether the
// behaviour written down is the behaviour anybody wants.
package chaos

// A Scenario is one row of SPEC 31's chaos layer.
type Scenario struct {
	// Key is the stable identifier a test names itself with. It never
	// changes once written, because it is what ties a test to a row.
	Key string
	// Spec is SPEC 31's own wording, matched against the document.
	Spec string
	// Behaviour is what this build is defined to do. It is the part
	// that has to be decided rather than discovered, and it is what a
	// reader consults when the answer to "what happens if" matters more
	// than the code.
	Behaviour string
	// Limit is what the test for this scenario does not establish.
	// Every one of these has something: a scenario tested in a lab is
	// not a scenario survived on an estate, and saying which is which
	// is the difference between a suite and a reassurance.
	Limit string
}

// The scenario keys. Referenced by the tests that exercise them.
const (
	HubRestartMidJob      = "hub-restart-mid-job"
	NetworkPartition      = "network-partition"
	DiskFull              = "disk-full"
	ClockSkew             = "clock-skew"
	CertificateExpiry     = "certificate-expiry-mid-run"
	ExtensionHang         = "extension-hang"
	EventBusAtRetention   = "event-bus-at-retention-limit"
	ReactorQueueOverflow  = "reactor-queue-overflow"
	ConcurrentBookkeeping = "concurrent-bookkeeping"
)

// Scenarios is the registry.
//
// The first eight are SPEC 31's, in the order the specification lists
// them. The ninth is not in SPEC and is here anyway — see its entry.
var Scenarios = []Scenario{
	{
		Key:  HubRestartMidJob,
		Spec: "hub restart mid-job",
		Behaviour: "A batched job is recorded before delivery with the whole node set, " +
			"so a hub that stops part way through has on disk both what it has done and " +
			"what it has not. `Resume` picks the job up and delivers only the remainder; " +
			"a node already delivered to is not sent the job again. A job whose window " +
			"closed while the hub was down is not resumed at all — it is refused as " +
			"expired, because the hazard SPEC 9.5 names is an instruction arriving long " +
			"after the operator who issued it stopped expecting it.",
		Limit: "The restart is a new Server over the same job cache, and the interrupted " +
			"state is written rather than produced by stopping a hub — because stopping " +
			"one drains: `Serve` waits for the batch goroutine, so a graceful stop always " +
			"leaves a finished batch and never a half-done one. What is not covered is a " +
			"kill between the write and the fsync, which is the case that decides whether " +
			"the record on disk is the one this assumes.",
	},
	{
		Key:  NetworkPartition,
		Spec: "network partition",
		Behaviour: "A node whose stream drops is disconnected from the hub's point of " +
			"view within the read deadline, and a job dispatched while it is away is " +
			"reported against it as absent rather than delivered. With `offline: queue` " +
			"it is spooled and delivered when the node comes back, exactly once. A " +
			"return that was in flight when the partition began is filed if it arrives " +
			"and the job is otherwise unaffected: returns are idempotent by " +
			"(jid, node, chunk), so the node retrying after the partition heals does not " +
			"produce two.",
		Limit: "The partition here is a closed connection. A half-open one — packets " +
			"dropped with no RST, which is what a firewall change actually does — is " +
			"the case that needs the read deadline to fire, and this suite reaches it " +
			"only through the deadline's own unit test.",
	},
	{
		Key:  DiskFull,
		Spec: "disk full",
		Behaviour: "A write that fails is reported, not swallowed. The job cache " +
			"refuses the record rather than reporting a job dispatched that has no " +
			"record, so an operator gets an error at submission instead of a job that " +
			"cannot be resumed or audited. A failure to write a *bookkeeping* update " +
			"after delivery is logged as a warning and does not fail the job, because " +
			"the instruction has already gone out and pretending otherwise would be a " +
			"second lie.",
		Limit: "A directory that cannot be created is the injected failure, which is " +
			"the same error class as ENOSPC and is not ENOSPC. A filesystem that " +
			"accepts the write and fails at fsync is not covered.",
	},
	{
		Key:  ClockSkew,
		Spec: "clock skew",
		Behaviour: "Expiry is decided by the hub's clock alone. A job's window is " +
			"written as an absolute time when the job is created, so a hub whose clock " +
			"jumps forward settles jobs early and a node's disagreement about the time " +
			"changes nothing. A queued job whose window closed while its node was away " +
			"is not delivered when the node returns: it is dropped, with an event and a " +
			"warning naming the expiry, because a node coming back to find nothing " +
			"happened and no reason why is the failure SPEC 9.5 is explicit about.",
		Limit: "Skew between hub and node is not modelled, because nothing in the " +
			"protocol asks a node what time it thinks it is. If that changes, this " +
			"scenario is where the new answer goes.",
	},
	{
		Key:  CertificateExpiry,
		Spec: "certificate expiry mid-run",
		Behaviour: "An expired client certificate is refused at the TLS handshake, so a " +
			"node whose certificate expires while it is connected keeps its existing " +
			"connection — TLS does not re-check mid-connection — and is refused on its " +
			"next one. The refusal names expiry rather than reporting a generic " +
			"handshake failure, because the fix is a renewal and an operator told " +
			"`bad certificate` goes looking for the wrong thing.",
		Limit: "That an established connection survives its own certificate's expiry is " +
			"a property of TLS rather than a decision this build made, and the test " +
			"records it rather than enforcing it.",
	},
	{
		Key:  ExtensionHang,
		Spec: "extension hang",
		Behaviour: "A call that does not answer within its timeout fails rather than " +
			"waiting, and the extension process is killed. The caller is given back the " +
			"reason it was killed, and the dead process is not reused: a later call on " +
			"it is refused rather than written into a stream the extension has lost " +
			"track of. This is the whole reason SPEC 24.2 makes an extension a process — " +
			"a hung extension cannot hang the agent.",
		Limit: "That the kill reaches a *grandchild* is not established here. The child " +
			"is given its own process group on unix and a job object on Windows " +
			"precisely so that it does, and nothing in this scenario starts a grandchild " +
			"and then checks that it died. Nor could anything cover an extension wedged " +
			"in uninterruptible I/O, which no signal reaches.",
	},
	{
		Key:  EventBusAtRetention,
		Spec: "event bus at retention limit",
		Behaviour: "The bus prunes whole segments from the oldest end, by age and by " +
			"total size, whichever binds first, and never the segment being written. " +
			"Appending never blocks and never fails because the bus is full — the limit " +
			"is enforced by discarding history, not by refusing the present, because a " +
			"bus that stopped accepting events at a threshold would go blind exactly " +
			"when an estate was busiest. " +
			"A reader whose offset is in a pruned segment is **not** told. `Read` skips " +
			"to the oldest segment that still exists and returns events from there with " +
			"no indication that anything was missed; the scenario's test measured 380 " +
			"events silently skipped. That is what this build does and it is written " +
			"here because it is what this build does — it is a gap rather than a " +
			"decision, and DIVERGENCE 4.12 argues it the other way. A malformed offset " +
			"is refused, so the silence is about a well-formed offset whose data has " +
			"gone rather than about parsing.",
		Limit: "Pruning is by segment, so the limit is honoured to within one segment " +
			"rather than exactly. A bus configured with a MaxBytes smaller than one " +
			"segment keeps one segment. Nothing here establishes what a *reactor* does " +
			"with the skip, which is where the consequence actually lands.",
	},
	{
		Key:  ReactorQueueOverflow,
		Spec: "reactor queue overflow",
		Behaviour: "The queue is bounded and drops the oldest, not the newest. A drop " +
			"is counted in `halite_reactor_dropped_total`, logged with the tag, and " +
			"emitted as `halite/reactor/overflow` — so the loss is visible in three " +
			"places rather than inferred from a reaction that did not happen. The " +
			"reactor keeps running: an overflow slows a reaction down, it does not stop " +
			"the hub.",
		Limit: "Dropping the oldest is right for the state-of-the-world events a " +
			"reactor mostly watches and wrong for an audit trail. Nothing here decides " +
			"which a given reactor is, and SPEC does not either.",
	},
	{
		Key: ConcurrentBookkeeping,
		// Deliberately not one of SPEC 31's eight, and deliberately
		// first among equals: it is the shape all three of the defects
		// in this package's own doc comment had, and the specification
		// does not name it. Chaos is not only the machine misbehaving.
		// Two correct operations arriving together is the cheapest way
		// to be wrong in a system built out of goroutines, and it is
		// the one this build has been wrong about three times.
		Spec: "",
		Behaviour: "Any record two goroutines can change is changed through a " +
			"read-modify-write that is atomic against other writers, and the mutator " +
			"is given the record as it is on disk rather than a copy read earlier. Any " +
			"file named by a timestamp carries a sequence number as well, because a " +
			"clock is not a source of unique names. Where a mechanism has neither, that " +
			"is a defect of this shape waiting to be found.",
		Limit: "This is a shape rather than a scenario, so a test can only demonstrate " +
			"it in the places somebody has looked. It cannot report where nobody has.",
	},
}

// Lookup returns a scenario by key.
func Lookup(key string) (Scenario, bool) {
	for _, s := range Scenarios {
		if s.Key == key {
			return s, true
		}
	}
	return Scenario{}, false
}

// Keys returns every registered key, in registry order.
func Keys() []string {
	out := make([]string, len(Scenarios))
	for i, s := range Scenarios {
		out[i] = s.Key
	}
	return out
}

// Exercises names the scenario a test covers, and is how a test is tied
// to a row of this registry.
//
// It takes no `*testing.T` on purpose: a package that imports `testing`
// puts test flags into anything that links it, and this one is imported
// by tests in four other packages. It returns the Scenario so a test can
// log the behaviour it is checking, which makes the failure output say
// what was supposed to happen rather than only what did.
//
// An unknown key panics. There is no useful way to continue: the test
// claims to cover something that is not in the registry, and the guard
// in chaos_test.go would report the scenario as uncovered while a test
// for it appears to pass.
func Exercises(key string) Scenario {
	s, ok := Lookup(key)
	if !ok {
		panic("chaos: no scenario " + key + "; the key must be one of the constants in this package")
	}
	return s
}
