package main

import (
	"crypto/x509"
	"errors"
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
	"github.com/edlitmus/halite/internal/fileperm"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/nodeevidence"
	"github.com/edlitmus/halite/internal/pki"
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
	// anchor is the reporter's state, under mu. See anchor.go.
	anchor anchorState
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
		if kind == nodeevidence.KindAnchorReceipt && errors.Is(err, nodeevidence.ErrClosed) {
			// A report that was in flight when the agent stopped: the hub
			// answered, and the chain was closed before the receipt was
			// filed. Not a lost record. The receipt vouches for a head
			// that is already in the chain and already on the hub, and
			// the next run's first report is answered with a later
			// receipt that covers it. Counting it would page
			// HaliteNodeEvidenceNotWritten on a clean shutdown.
			//
			// Only receipts. A job's result arriving after the close is
			// a job with no entry, which is exactly what the counter is
			// for, and it still takes this function's other path.
			n.log.Debug("an anchor receipt arrived after the evidence log closed; it is not filed",
				"component", "evidence", "seq", detail[nodeevidence.ReceiptAnchoredSeq])
			return
		}
		n.log.Error("an evidence record could not be written",
			"component", "evidence", "kind", kind, "error", err.Error())
		n.metrics.countEvidenceFailure()
		return
	}
	n.metrics.countEvidenceRecord(kind)
}

// registerEvidenceMetrics exports whether the chain is open, for a node
// that is configured to keep one and for no other.
//
// The gauge is absent, not zero, on a node with `evidence: false`. That
// is the point of registering it conditionally: 0 means "this node is
// supposed to be keeping a record and is not", which is the one state no
// other signal reports. A log that fails to open is logged once and
// counts nothing, so halite_node_evidence_failures_total stays at zero,
// and no record is attempted, so the records counter never moves: the
// node runs jobs with no record and every evidence alert is quiet
// (DIVERGENCE 5.236 named this as an accepted cost; 5.238 closes it). A
// node that turned evidence off on purpose has nothing to report, and an
// alert on 0 never sees it.
//
// It reads the state without opening anything. evidenceLog opens on
// first use, and a scrape that created the directory would be a read with
// a side effect.
func (n *node) registerEvidenceMetrics() {
	if n.evidence == nil || !n.evidenceOn() {
		return
	}
	// Each kind at 0 from the start, so that the first record of a kind is
	// an increase and not a series born at 1. Without it a node's first job
	// ever writes job.accepted and job.result as new series, every kind's
	// increase() reads 0, and HaliteNodeEvidenceStopped fires on a node that
	// recorded the job correctly (DIVERGENCE 5.239).
	n.metrics.declareEvidenceKinds(nodeevidence.Kinds)
	n.metrics.gauge("halite_node_evidence_log_open",
		"1 when this node has its evidence chain open for writing; 0 when it is configured "+
			"to keep one and is not, because the log failed to open. Absent when evidence is off.",
		func() float64 {
			n.evidence.mu.Lock()
			defer n.evidence.mu.Unlock()
			if n.evidence.log != nil {
				return 1
			}
			return 0
		})
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
	// After the result is on disk, so the head reported is the one that
	// holds it. Asynchronous: see anchor.go.
	n.requestAnchor()
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
	if err := fileperm.WriteFile(probe, nil, 0o600); err != nil {
		return err
	}
	return os.Remove(probe)
}

// runVerifyEvidence is SPEC 25.7's `halite-node verify-evidence`.
//
// Exits non-zero on a break, so that a monitoring job can run it. It
// changes nothing, including a chain it finds broken: a verifier that
// repaired what it found would destroy the thing it was asked about.
//
// Three checks, of which the first was the whole command until the
// anchor existed. The chain against itself, which finds editing. The
// chain against the hub's record (`--anchors`), which finds rewriting --
// the one thing a chain that verifies cannot rule out. And the receipts
// the chain holds against the CA certificate, always, because a receipt
// that does not verify, or names a hash the chain no longer has at that
// number, is a break whether or not anybody fetched the hub's file.
func runVerifyEvidence(args *cli.Args) int {
	n := setup(args)
	dir := n.evidenceDir()

	anchorsPath := args.Flag("anchors", "")
	if anchorsPath == "true" {
		cli.Usagef("--anchors needs the file `halite-hub evidence anchors <node>` printed")
	}
	var anchors []nodeevidence.Anchor
	if anchorsPath != "" {
		f, err := os.Open(anchorsPath)
		if err != nil {
			cli.Fatalf("%v", err)
		}
		anchors, err = nodeevidence.ReadAnchors(f)
		f.Close()
		if err != nil {
			cli.Fatalf("%s: %v", anchorsPath, err)
		}
	}

	res, err := nodeevidence.Verify(dir)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	caPath, ca, caErr := evidenceCA(n, args)
	check, err := nodeevidence.CheckAnchors(dir, anchorsPath, anchors, ca)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	ok := res.OK() && check.OK()

	if n.format == cli.JSON || n.format == cli.YAML {
		n.out(verifyValue(res, check, anchorsPath, caPath, caErr))
		if ok {
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
	if anchorsPath != "" {
		fmt.Printf("anchors:   %d accepted, %d conflict(s), from %s\n",
			check.Anchored, len(check.Conflicts), anchorsPath)
	}
	// Only for a chain that holds receipts: a chain from before the
	// anchor existed has none, and a line about a certificate it does
	// not need would read as a problem.
	switch {
	case check.Found > 0 && caErr == nil:
		fmt.Printf("receipts:  %d checked against %s\n", check.Receipts, caPath)
	case check.Found > 0:
		fmt.Printf("receipts:  %d, none checked; the CA certificate could not be read: %v\n", check.Found, caErr)
	}
	if res.Lost > 0 {
		fmt.Printf("\n%d record(s) could not be written and the chain says so; those jobs have no entry.\n",
			res.Lost)
	}
	for _, schema := range res.UnknownSchemas {
		fmt.Printf("\nRecords written with schema %s cannot be checked by this build; their links\nare verified and their contents are not. That is a newer halite, not a broken chain.\n",
			schema)
	}
	for _, note := range check.Unchecked {
		fmt.Printf("\nNot checked: %s.\n", note)
	}
	if len(check.Conflicts) > 0 {
		fmt.Printf("\nThe hub recorded %d conflict(s): heads this node reported that contradicted\n"+
			"heads it had reported before. The chain was rewritten, reset or rolled back.\n",
			len(check.Conflicts))
		for _, c := range check.Conflicts {
			fmt.Printf("  line %d at %s: record %d as %s, where the hub had record %d as %s\n",
				c.Line, c.Received, c.Seq, c.Hash, c.PriorSeq, c.PriorHash)
		}
	}
	if res.Records == 0 && ok {
		fmt.Print("\nThere is no record here. An agent writes one when it starts, so this is a\n" +
			"node that has not run one since `evidence` was turned on.\n")
		return 0
	}
	if ok {
		fmt.Print("\nThe chain holds: every record's contents match its hash and every record\n" +
			"follows the one before it.\n")
		if anchorsPath != "" && check.Anchored > 0 {
			fmt.Print("\nIt also agrees with the hub: every head the hub accepted from this node is\n" +
				"still the record at that number. What that does not establish: records\n" +
				"written after the last head the hub accepted are not anchored, and a node\n" +
				"and hub compromised together can agree on anything.\n")
		} else {
			fmt.Print("\nWhat that does not establish: anything with root on this node can rewrite\n" +
				"the whole chain. The hub keeps the heads this node reported; check the chain\n" +
				"against them with --anchors and the file `halite-hub evidence anchors`\n" +
				"prints for this node.\n")
		}
		return 0
	}
	breaks := append(append([]nodeevidence.Break(nil), res.Breaks...), check.Breaks...)
	if len(breaks) > 0 {
		fmt.Printf("\n%d break(s):\n", len(breaks))
		for _, b := range breaks {
			fmt.Printf("  %s\n", b)
		}
	}
	return 1
}

// evidenceCA reads the certificate a receipt is checked against: the
// enrollment CA, found where hubClient finds it, so that verify-evidence
// and the agent cannot be looking at two different files.
//
// `--ca-file` rather than a flag of its own. It already means "the hub's
// CA" on this program, and two flags naming one certificate, differing
// by a suffix, is a pair somebody types the wrong half of.
func evidenceCA(n *node, args *cli.Args) (string, *x509.Certificate, error) {
	path := args.Flag("ca-file", n.cfg.String("hub_ca_file", ""))
	if path == "" || path == "true" {
		files := pki.Files{Dir: args.Flag("pki-dir", n.cfg.PathUnderRoot("pki_dir", "pki"))}
		path = files.Path(pki.CACertFile)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return path, nil, err
	}
	cert, err := pki.DecodeCert(raw)
	if err != nil {
		return path, nil, fmt.Errorf("%s: %w", path, err)
	}
	return path, cert, nil
}

// verifyValue renders a verification for `--out json` or `yaml`, so that
// a monitoring job can read it rather than parse the text.
func verifyValue(res *nodeevidence.Result, check *nodeevidence.AnchorCheck, anchorsPath, caPath string, caErr error) *value.Map {
	out := value.NewMap(8)
	out.Set("directory", res.Dir)
	out.Set("segments", int64(len(res.Segments)))
	out.Set("records", int64(res.Records))
	out.Set("ok", res.OK() && check.OK())
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
	if anchorsPath != "" {
		out.Set("anchors_file", anchorsPath)
		out.Set("anchors_checked", int64(check.Anchored))
	}
	out.Set("ca_file", caPath)
	if caErr != nil && check.Found > 0 {
		out.Set("ca_error", caErr.Error())
	}
	out.Set("receipts", int64(check.Found))
	out.Set("receipts_checked", int64(check.Receipts))
	conflicts := make([]any, 0, len(check.Conflicts))
	for _, c := range check.Conflicts {
		entry := value.NewMap(6)
		entry.Set("line", int64(c.Line))
		entry.Set("received", c.Received)
		entry.Set("record", int64(c.Seq))
		entry.Set("hash", c.Hash)
		entry.Set("prior_record", int64(c.PriorSeq))
		entry.Set("prior_hash", c.PriorHash)
		conflicts = append(conflicts, entry)
	}
	out.Set("conflicts", conflicts)
	if len(check.Unchecked) > 0 {
		unchecked := make([]any, len(check.Unchecked))
		for i, s := range check.Unchecked {
			unchecked[i] = s
		}
		out.Set("unchecked", unchecked)
	}
	breaks := make([]any, 0, len(res.Breaks)+len(check.Breaks))
	for _, b := range append(append([]nodeevidence.Break(nil), res.Breaks...), check.Breaks...) {
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
