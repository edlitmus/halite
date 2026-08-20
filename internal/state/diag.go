// Package state is halite's state compiler: SLS declarations in, an
// ordered list of executable low chunks out.
//
// The compiler reproduces Salt's semantics including the parts that are
// surprising, because a tree that has been in production for years depends
// on the surprising parts. Where it departs, it departs on purpose and the
// departure is named in a comment and in SPEC section 11.
//
// One departure is worth stating up front: every error is collected and
// reported together. Salt reports the first and stops, which makes fixing
// a large tree an iterative grind. SPEC section 11.2 step 10.
package state

import (
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/value"
)

// Diag is one compilation error or warning.
type Diag struct {
	// Pos is where in a source file the problem is, when it is known.
	Pos value.Pos
	// SLS is the dotted SLS name the problem came from.
	SLS string
	// ID is the state ID, when the problem belongs to one.
	ID  string
	Msg string
	// Related points at a second position that explains the first, such
	// as the other definition of a duplicate ID.
	Related []Related
	// Warning marks a diagnostic that does not stop the compilation.
	Warning bool
}

// Related is a supporting position.
type Related struct {
	Pos value.Pos
	Msg string
}

func (d Diag) Error() string { return d.String() }

func (d Diag) String() string {
	var b strings.Builder
	switch {
	case !d.Pos.IsZero():
		b.WriteString(d.Pos.String())
	case d.SLS != "":
		b.WriteString(d.SLS)
	default:
		b.WriteString("<state>")
	}
	if d.ID != "" {
		fmt.Fprintf(&b, " [%s]", d.ID)
	}
	b.WriteString(": ")
	b.WriteString(d.Msg)
	for _, r := range d.Related {
		fmt.Fprintf(&b, "\n    %s: %s", r.Pos, r.Msg)
	}
	return b.String()
}

// Diags is a collection of diagnostics, ordered for reporting.
type Diags []Diag

// Add appends an error.
func (d *Diags) Add(pos value.Pos, sls, id, format string, args ...any) {
	*d = append(*d, Diag{Pos: pos, SLS: sls, ID: id, Msg: fmt.Sprintf(format, args...)})
}

// Warn appends a warning.
func (d *Diags) Warn(pos value.Pos, sls, id, format string, args ...any) {
	*d = append(*d, Diag{Pos: pos, SLS: sls, ID: id, Msg: fmt.Sprintf(format, args...), Warning: true})
}

// AddRelated appends an error with a supporting position.
func (d *Diags) AddRelated(pos value.Pos, sls, id string, related []Related, format string, args ...any) {
	*d = append(*d, Diag{Pos: pos, SLS: sls, ID: id, Msg: fmt.Sprintf(format, args...), Related: related})
}

// Errors returns only the diagnostics that stop a compilation.
func (d Diags) Errors() Diags {
	var out Diags
	for _, x := range d {
		if !x.Warning {
			out = append(out, x)
		}
	}
	return out
}

// Warnings returns only the diagnostics that do not.
func (d Diags) Warnings() Diags {
	var out Diags
	for _, x := range d {
		if x.Warning {
			out = append(out, x)
		}
	}
	return out
}

// HasErrors reports whether the compilation failed.
func (d Diags) HasErrors() bool {
	for _, x := range d {
		if !x.Warning {
			return true
		}
	}
	return false
}

// Sorted orders diagnostics by file and line, so a report reads top to
// bottom through the tree.
func (d Diags) Sorted() Diags {
	out := make(Diags, len(d))
	copy(out, d)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Pos.File != b.Pos.File {
			return a.Pos.File < b.Pos.File
		}
		if a.Pos.Line != b.Pos.Line {
			return a.Pos.Line < b.Pos.Line
		}
		return a.Msg < b.Msg
	})
	return out
}

// Err renders the errors as a single error, or nil when there are none.
func (d Diags) Err() error {
	errs := d.Errors().Sorted()
	if len(errs) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "state compilation failed with %d error(s):", len(errs))
	for _, e := range errs {
		b.WriteString("\n  ")
		b.WriteString(e.String())
	}
	return fmt.Errorf("%s", b.String())
}
