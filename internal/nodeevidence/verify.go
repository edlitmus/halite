package nodeevidence

import (
	"encoding/json"
	"fmt"
	"path/filepath"
)

// Break is one place the chain does not hold up.
type Break struct {
	Segment string
	// Line is 1-based within the segment, because that is what an
	// investigator needs to look at the record itself.
	Line   int
	Seq    uint64
	Reason string
}

func (b Break) String() string {
	where := fmt.Sprintf("%s line %d", filepath.Base(b.Segment), b.Line)
	if b.Seq > 0 {
		where += fmt.Sprintf(" (record %d)", b.Seq)
	}
	return where + ": " + b.Reason
}

// Result is what a verification found.
type Result struct {
	Dir      string
	Segments []string
	Records  uint64
	First    *Record
	Last     *Record
	// Lost is the total the chain declares it could not write: the sum
	// of the Lost counters, which is the node's own account of its
	// holes.
	Lost int
	// UnknownSchemas lists record schemas this build cannot canonicalise,
	// in the order first seen. Their contents are unverified and their
	// links are still checked, which is the difference between reading a
	// chain a newer build wrote and finding a chain that has been
	// tampered with. Reporting the first as the second would make a
	// rollback look like an incident.
	UnknownSchemas []string
	Breaks         []Break
}

// OK reports whether the chain holds.
func (r *Result) OK() bool { return len(r.Breaks) == 0 }

// Verify walks every segment in chain order and checks each record's
// hash against its contents and its link against the record before it.
//
// The two checks catch different things and both are needed. A hash that
// does not recompute is a record whose contents were changed in place. A
// Prev that does not match is a record inserted, removed or reordered --
// and that is the check that makes the log append-only in any useful
// sense, because deleting the last five records of a chain leaves every
// remaining hash perfectly valid.
//
// It reads; it never repairs. A verifier that fixed what it found would
// destroy the thing it was asked about.
func Verify(dir string) (*Result, error) {
	segments, err := Segments(dir)
	if err != nil {
		return nil, err
	}
	res := &Result{Dir: dir, Segments: segments}
	seenSchema := map[string]bool{}

	var prev *Record
	for _, segment := range segments {
		partial, err := scanLines(segment, func(lineNo int, line []byte) error {
			var r Record
			if err := json.Unmarshal(line, &r); err != nil {
				res.Breaks = append(res.Breaks, Break{
					Segment: segment, Line: lineNo,
					Reason: "this line is not a readable evidence record: " + err.Error(),
				})
				// prev is left alone: the next record's Prev is checked
				// against the last record that was readable, so one
				// corrupt line is one break rather than two.
				return nil
			}
			res.Records++

			if r.Schema != Schema {
				if !seenSchema[r.Schema] {
					seenSchema[r.Schema] = true
					res.UnknownSchemas = append(res.UnknownSchemas, r.Schema)
				}
			} else {
				want, err := hashOf(r)
				if err != nil {
					return err
				}
				if want != r.Hash {
					res.Breaks = append(res.Breaks, Break{
						Segment: segment, Line: lineNo, Seq: r.Seq,
						Reason: "the contents of this record do not match its hash, so it has been altered",
					})
				}
			}

			switch {
			case prev == nil:
				// One break rather than two for the same fact. A chain
				// that starts later than record 1 and follows a hash
				// that is not here is one thing -- its earlier segments
				// are gone -- and reporting it twice reads like two
				// problems. It is deliberate when an operator has
				// archived them, which docs/operations.md says to do,
				// and the message is phrased so that such a reader can
				// tell it is the answer to what they did.
				if r.Prev != "" || r.Seq != 1 {
					res.Breaks = append(res.Breaks, Break{
						Segment: segment, Line: lineNo, Seq: r.Seq,
						Reason: fmt.Sprintf(
							"this chain begins at record %d, following %s, so the records before it "+
								"are not here; they were archived, or they were removed",
							r.Seq, orNothing(r.Prev)),
					})
				}
			default:
				if r.Prev != prev.Hash {
					res.Breaks = append(res.Breaks, Break{
						Segment: segment, Line: lineNo, Seq: r.Seq,
						Reason: fmt.Sprintf(
							"this record follows %s and the record before it is %s, so a record has been removed, inserted or reordered",
							r.Prev, prev.Hash),
					})
				}
				if r.Seq != prev.Seq+1 {
					res.Breaks = append(res.Breaks, Break{
						Segment: segment, Line: lineNo, Seq: r.Seq,
						Reason: fmt.Sprintf("this record is numbered %d and follows record %d", r.Seq, prev.Seq),
					})
				}
			}

			res.Lost += r.Lost
			if res.First == nil {
				first := r
				res.First = &first
			}
			last := r
			res.Last = &last
			prev = &last
			return nil
		})
		if err != nil {
			return nil, err
		}
		if partial > 0 {
			res.Breaks = append(res.Breaks, Break{
				Segment: segment,
				Reason: fmt.Sprintf(
					"the last %d bytes are an incomplete record, so this segment was cut short", partial),
			})
		}
	}
	return res, nil
}

// Tail reads the last record of the chain and how many segments hold it,
// without verifying anything.
//
// For a diagnostic that has to answer "is this node recording, and when
// did it last record something" in bounded time. Verify reads every
// segment, which is the right cost for an investigation and the wrong one
// for `doctor` on a node with years of history.
func Tail(dir string) (*Record, int, error) {
	segments, err := Segments(dir)
	if err != nil {
		return nil, 0, err
	}
	for i := len(segments) - 1; i >= 0; i-- {
		last, _, err := lastRecord(segments[i])
		if err != nil {
			return nil, len(segments), err
		}
		if last != nil {
			return last, len(segments), nil
		}
	}
	return nil, len(segments), nil
}

// orNothing renders an absent previous hash for a message.
func orNothing(hash string) string {
	if hash == "" {
		return "nothing"
	}
	return hash
}
