package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/doctor"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/nodeevidence"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/version"
)

// evidenceState holds SPEC 25.7's chain for this process.
//
// A pointer on the node, because a job runs against a shallow copy of the
// node and both must write to the same chain -- and because the open is
// done once and its failure has to be remembered rather than retried on
// every record.
type evidenceState struct {
	mu     sync.Mutex
	log    *nodeevidence.Log
	opened bool
	// err is why there is no chain, kept for `doctor` rather than only
	// logged. A node recording nothing while configured to record is the
	// failure that looks like nothing at all, so something has to be able
	// to answer it on demand.
	err error
}

// evidenceOn reports whether this node keeps the record. On by default: a
// record kept only where somebody remembered to turn it on is not a
// record.
func (n *node) evidenceOn() bool { return n.cfg.Bool("evidence", true) }

// evidenceDir is where the chain lives. SPEC 27.3 allocates it a place
// under the state directory.
func (n *node) evidenceDir() string {
	if dir := n.cfg.String("evidence_dir", ""); dir != "" {
		return dir
	}
	return filepath.Join(n.cfg.String("state_dir", config.DefaultStateDir), "evidence")
}

// evidenceLog opens the chain on first use and returns nil when there is
// none.
//
// Lazy, because every `halite-node call` builds a node and almost none of
// them has anything to record: opening from setup would create a
// directory, write a startup record and fail noisily on any machine where
// an operator runs a command as themselves. The agent opens it eagerly
// through startEvidence, which is where a failure is worth a line at
// startup.
func (n *node) evidenceLog() *nodeevidence.Log {
	if n.evidence == nil || !n.evidenceOn() {
		return nil
	}
	n.evidence.mu.Lock()
	defer n.evidence.mu.Unlock()
	if n.evidence.opened {
		return n.evidence.log
	}
	n.evidence.opened = true
	dir := n.evidenceDir()
	log, err := nodeevidence.Open(dir, nodeevidence.Options{
		NodeID:   n.nodeID,
		MaxBytes: n.cfg.Int("evidence_max_bytes", nodeevidence.DefaultMaxBytes),
	})
	if err != nil {
		n.evidence.err = err
		return nil
	}
	n.evidence.log = log
	return log
}

// recordEvidence appends one record, and says so when it cannot.
//
// It never fails a caller. A node that refused to run a job it could not
// record would be a node whose full disk stops the highstate that would
// have cleared the disk, and the chain's own Lost counter is what keeps
// the gap visible: the next record that does get written says how many
// did not.
func (n *node) recordEvidence(kind string, detail map[string]string) {
	log := n.evidenceLog()
	if log == nil {
		if n.evidence != nil && n.evidence.err != nil {
			// Once per process, because the open is attempted once.
			n.evidence.mu.Lock()
			err := n.evidence.err
			n.evidence.err = nil
			n.evidence.mu.Unlock()
			if err != nil {
				n.log.Error("this node is keeping no evidence record",
					"component", "evidence", "dir", n.evidenceDir(), "error", err.Error())
			}
		}
		return
	}
	if err := log.Append(kind, detail); err != nil {
		n.log.Error("an evidence record could not be written",
			"component", "evidence", "kind", kind, "error", err.Error())
		n.metrics.countEvidenceFailure()
		return
	}
	n.metrics.countEvidenceRecord(kind)
}

// startEvidence opens the chain for an agent and writes what it is
// starting with.
//
// The configuration record is a digest and the list of files it came
// from, never the settings themselves: see nodeevidence.Digest. It is
// written at startup because that is when a node's configuration can
// change -- nothing here re-reads it while running -- so one record per
// run is a complete account of the changes rather than a sample of them.
func (n *node) startEvidence() {
	if !n.evidenceOn() {
		n.log.Info("this node keeps no local evidence record", "component", "evidence",
			"setting", "evidence")
		return
	}
	log := n.evidenceLog()
	if log == nil {
		n.evidence.mu.Lock()
		err := n.evidence.err
		n.evidence.err = nil
		n.evidence.mu.Unlock()
		// Loud, and not fatal. A node that would not start without its
		// audit log is a node an operator cannot reach to fix the audit
		// log; `doctor` answers the same question on demand.
		n.log.Error("this node is keeping no evidence record and is running anyway",
			"component", "evidence", "dir", n.evidenceDir(), "error", errText(err))
		return
	}
	seq, head := log.Head()
	n.log.Info("keeping an evidence record", "component", "evidence",
		"dir", n.evidenceDir(), "records", seq, "head", head)

	n.recordEvidence(nodeevidence.KindStart, map[string]string{
		"version": version.String(),
	})
	n.recordEvidence(nodeevidence.KindConfig, map[string]string{
		"digest": nodeevidence.Digest(n.cfg.Values),
		"files":  strings.Join(n.cfg.Files, " "),
	})
}

// stopEvidence closes the chain, having said that this run ended.
//
// The record matters more than the tidiness of the descriptor: without
// it, a gap in the chain's timestamps cannot be told from a node that was
// switched off, and those are very different findings.
func (n *node) stopEvidence(reason string) {
	if n.evidence == nil {
		return
	}
	n.evidence.mu.Lock()
	log := n.evidence.log
	n.evidence.mu.Unlock()
	if log == nil {
		return
	}
	n.recordEvidence(nodeevidence.KindStop, map[string]string{"reason": reason})
	if err := log.Close(); err != nil {
		n.log.Warn("the evidence log did not close cleanly",
			"component", "evidence", "error", err.Error())
	}
}

// recordJobAccepted is written before the job is queued, which is the
// only moment worth writing it: a record written afterwards is missing
// exactly the jobs that stopped the node.
func (n *node) recordJobAccepted(j *job.Job, signer string) {
	detail := n.jobDetail(j)
	if signer != "" {
		// Not `claimed_`, unlike the submitter beside it: this node
		// checked this one itself, against a key in its own
		// configuration. The two words are the difference between what
		// the hub asserted and what the node established, and an
		// investigator reading the file needs to be able to tell them
		// apart without knowing how either got there.
		detail["verified_signer"] = signer
	}
	n.recordEvidence(nodeevidence.KindJobAccepted, detail)
}

// recordJobRefused records a job this node would not run. SPEC 6.3 sends
// the refusal back to the operator; this is the half that survives a hub
// that drops it.
func (n *node) recordJobRefused(j *job.Job, reason error) {
	detail := n.jobDetail(j)
	detail["refused"] = n.secrets.Scrub(reason.Error())
	n.recordEvidence(nodeevidence.KindJobRefused, detail)
}

// recordJobResult pairs with the accepted record by jid.
func (n *node) recordJobResult(ret *job.Return) {
	n.recordEvidence(nodeevidence.KindJobResult, map[string]string{
		"jid":         string(ret.JID),
		"fun":         ret.Fun,
		"success":     strconv.FormatBool(ret.Success),
		"retcode":     strconv.Itoa(ret.RetCode),
		"duration_ms": strconv.FormatInt(ret.DurationMS, 10),
	})
}

// recordExtensionChange records a bundle appearing, changing version or
// being refused. An extension is code the node will run, so this belongs
// beside the jobs rather than in a log line.
func (n *node) recordExtensionChange(name, bundleVersion, status, reason string) {
	detail := map[string]string{
		"extension": name,
		"version":   bundleVersion,
		"status":    status,
	}
	if reason != "" {
		detail["reason"] = n.secrets.Scrub(reason)
	}
	n.recordEvidence(nodeevidence.KindExtension, detail)
}

// maxEvidenceField bounds one recorded argument string.
//
// A job's arguments are whatever an operator typed and can be enormous; a
// record is a line in a file somebody has to read. Truncating loses
// information, so the untruncated text is digested alongside it and the
// digest is what a comparison with the hub's record uses.
const maxEvidenceField = 1024

// jobDetail is what the record holds about a job.
//
// The principal is recorded as claimed rather than as fact. A node
// authenticates its hub and nothing beyond it, so `submitter` is the hub's
// assertion about a person, and a hub that has been taken over can write
// anything there. Its value is the comparison: the hub's job cache holds
// the same two fields, and a disagreement is the finding.
func (n *node) jobDetail(j *job.Job) map[string]string {
	detail := map[string]string{
		"jid":   string(j.JID),
		"fun":   j.Fun,
		"nonce": j.Nonce,
	}
	if j.Env != "" {
		detail["env"] = j.Env
	}
	if !j.Expires.IsZero() {
		detail["expires"] = j.Expires.UTC().Format("2006-01-02T15:04:05.000000Z07:00")
	}
	if j.Submitter != "" {
		detail["claimed_submitter"] = j.Submitter
	}
	if j.OnBehalfOf != "" {
		detail["claimed_on_behalf_of"] = j.OnBehalfOf
	}
	if args := n.argumentText(j); args != "" {
		detail["arg_digest"] = nodeevidence.DigestString(args)
		detail["arg"] = truncate(args, maxEvidenceField)
	}
	return detail
}

// argumentText renders a job's arguments as one scrubbed string.
//
// Scrubbed through the node's redactor, because SPEC 26.1 applies
// redaction at the sink and this is a sink: a pillar value that reached a
// job's arguments must not be written to a file whose whole purpose is to
// be read by somebody else later.
func (n *node) argumentText(j *job.Job) string {
	parts := make([]string, 0, len(j.Arg)+len(j.Kwarg))
	parts = append(parts, j.Arg...)
	keys := make([]string, 0, len(j.Kwarg))
	for k := range j.Kwarg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, j.Kwarg[k]))
	}
	if len(parts) == 0 {
		return ""
	}
	return n.secrets.Scrub(strings.Join(parts, " "))
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("… (%d bytes in all)", len(s))
}

func errText(err error) string {
	if err == nil {
		return "the chain could not be opened"
	}
	return err.Error()
}

// nodeEvidenceCheck gathers what `doctor` reports about the chain.
//
// It reads the tail rather than verifying: verification walks every
// segment, which is the right cost for an investigation and the wrong one
// for a command an operator runs while something else is already broken.
func nodeEvidenceCheck(n *node) doctor.Check {
	state := doctor.EvidenceState{
		Enabled: n.evidenceOn(),
		Dir:     n.evidenceDir(),
	}
	if !state.Enabled {
		return doctor.EvidenceChain(state)
	}
	// Deliberately not evidenceLog(): a diagnostic must not create the
	// directory it is reporting on, or it would turn "nothing is being
	// recorded" into a pass on the run that was asked to find out.
	//
	// The usability probe is the one thing this cannot read. `doctor` is
	// a different process from the agent, so it cannot see that the
	// agent's open failed; what it can do is ask the same question the
	// agent asked -- can this account write here -- of a directory that
	// already exists. It creates nothing: a directory that is absent is
	// reported as an empty record, which is what a node that has never
	// run an agent has.
	state.OpenErr = evidenceDirUsable(state.Dir)
	last, segments, err := nodeevidence.Tail(state.Dir)
	state.Segments = segments
	state.ReadErr = err
	if last != nil {
		state.Last = &doctor.EvidenceRecord{
			Seq:  last.Seq,
			Kind: last.Kind,
			TS:   last.TS,
			Lost: last.Lost,
		}
	}
	return doctor.EvidenceChain(state)
}

// evidenceDirUsable reports why the chain's directory cannot be written,
// or nil.
//
// Only for a directory that is already there. The probe file is created
// and removed, which is what OpenNodeCache and nodeevidence.Open both do
// at startup and for the same reason: a directory that exists is not a
// directory this account can use, and the symptom of getting that wrong
// is an audit record that quietly stops.
func evidenceDirUsable(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		// Absent is not a failure here. It is a node whose agent has not
		// run, which the check reports as an empty record.
		return nil
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	probe := filepath.Join(dir, ".doctor")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		return err
	}
	return os.Remove(probe)
}

// runVerifyEvidence is SPEC 25.7's `halite-node verify-evidence`.
//
// Exits non-zero on a break, so that a monitoring job can run it. It
// changes nothing, including a chain it finds broken: a verifier that
// repaired what it found would destroy the thing it was asked about.
func runVerifyEvidence(args *cli.Args) int {
	n := setup(args)
	dir := n.evidenceDir()
	res, err := nodeevidence.Verify(dir)
	if err != nil {
		cli.Fatalf("%v", err)
	}

	if n.format == cli.JSON || n.format == cli.YAML {
		n.out(verifyValue(res))
		if res.OK() {
			return 0
		}
		return 1
	}

	fmt.Printf("halite-node verify-evidence — %s\n\n", n.nodeID)
	fmt.Printf("directory: %s\n", dir)
	fmt.Printf("segments:  %d\n", len(res.Segments))
	fmt.Printf("records:   %d\n", res.Records)
	if res.First != nil && res.Last != nil {
		fmt.Printf("from:      record %d at %s\n", res.First.Seq, res.First.TS)
		fmt.Printf("to:        record %d at %s\n", res.Last.Seq, res.Last.TS)
		fmt.Printf("head:      %s\n", res.Last.Hash)
	}
	if res.Lost > 0 {
		fmt.Printf("\n%d record(s) could not be written and the chain says so; those jobs have no entry.\n",
			res.Lost)
	}
	for _, schema := range res.UnknownSchemas {
		fmt.Printf("\nRecords written with schema %s cannot be checked by this build; their links\nare verified and their contents are not. That is a newer halite, not a broken chain.\n",
			schema)
	}
	if res.Records == 0 {
		fmt.Print("\nThere is no record here. An agent writes one when it starts, so this is a\n" +
			"node that has not run one since `evidence` was turned on.\n")
		return 0
	}
	if res.OK() {
		fmt.Print("\nThe chain holds: every record's contents match its hash and every record\n" +
			"follows the one before it.\n")
		fmt.Print("\nWhat that does not establish: anything with root on this node can rewrite\n" +
			"the whole chain. Keeping the head hash above somewhere this node cannot\n" +
			"reach is what makes that detectable.\n")
		return 0
	}
	fmt.Printf("\n%d break(s):\n", len(res.Breaks))
	for _, b := range res.Breaks {
		fmt.Printf("  %s\n", b)
	}
	return 1
}

// verifyValue renders a verification for `--out json` or `yaml`, so that
// a monitoring job can read it rather than parse the text.
func verifyValue(res *nodeevidence.Result) *value.Map {
	out := value.NewMap(8)
	out.Set("directory", res.Dir)
	out.Set("segments", int64(len(res.Segments)))
	out.Set("records", int64(res.Records))
	out.Set("ok", res.OK())
	out.Set("lost", int64(res.Lost))
	if res.First != nil {
		out.Set("first_seq", int64(res.First.Seq))
		out.Set("first_ts", res.First.TS)
	}
	if res.Last != nil {
		out.Set("last_seq", int64(res.Last.Seq))
		out.Set("last_ts", res.Last.TS)
		out.Set("head", res.Last.Hash)
	}
	if len(res.UnknownSchemas) > 0 {
		schemas := make([]any, len(res.UnknownSchemas))
		for i, s := range res.UnknownSchemas {
			schemas[i] = s
		}
		out.Set("unverified_schemas", schemas)
	}
	breaks := make([]any, 0, len(res.Breaks))
	for _, b := range res.Breaks {
		entry := value.NewMap(4)
		entry.Set("segment", filepath.Base(b.Segment))
		entry.Set("line", int64(b.Line))
		if b.Seq > 0 {
			entry.Set("record", int64(b.Seq))
		}
		entry.Set("reason", b.Reason)
		breaks = append(breaks, entry)
	}
	out.Set("breaks", breaks)
	return out
}
