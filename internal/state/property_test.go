package state

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// SPEC section 31 names three properties of the compiler: the topological
// sort is stable, requisite resolution terminates, and — with the file
// server's containment and the parser's freedom from panics — these are
// what hold the run order trustworthy.
//
// They are checked over generated graphs, because a hand-written case
// tests the shape its author already had in mind.

// randomChunks builds a low state of n chunks with random requisites among
// them. Cycles are not excluded: a compiler that only terminates on
// well-formed input has not proved anything.
func randomChunks(rnd *rand.Rand, n int) []*Chunk {
	kinds := []ReqKind{
		Require, RequireAny, Watch, WatchAny, OnChanges, OnChangesAny,
		OnFail, OnFailAny, OnFailAll, Prereq, Use, Listen,
	}
	modules := []string{"file", "pkg", "service", "cmd"}

	chunks := make([]*Chunk, n)
	for i := range chunks {
		chunks[i] = &Chunk{
			ID:        fmt.Sprintf("state%d", i),
			SLS:       fmt.Sprintf("sls%d", i%3),
			Env:       "base",
			State:     modules[rnd.Intn(len(modules))],
			Fun:       "managed",
			Name:      fmt.Sprintf("/tmp/state%d", i),
			Args:      value.NewMap(0),
			DeclOrder: i,
		}
	}
	for i, c := range chunks {
		for r := rnd.Intn(4); r > 0; r-- {
			target := rnd.Intn(n)
			ref := ReqRef{ID: chunks[target].ID}
			switch rnd.Intn(4) {
			case 0:
				ref.State = chunks[target].State
			case 1:
				ref.SLS = chunks[target].SLS
				ref.ID = ""
			case 2:
				// A reference to something that is not there, which a real
				// tree produces constantly during editing.
				ref.ID = fmt.Sprintf("missing%d", rnd.Intn(5))
			}
			c.Reqs = append(c.Reqs, Req{Kind: kinds[rnd.Intn(len(kinds))], Refs: []ReqRef{ref}})
		}
		_ = i
	}
	return chunks
}

// Property: resolution and ordering always terminate, whatever the graph.
// A cycle, a self-reference, or a reference to nothing must come back as a
// diagnostic rather than a hang.
func TestRequisiteResolutionAlwaysTerminates(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		rnd := rand.New(rand.NewSource(7))
		for i := 0; i < 2000; i++ {
			chunks := randomChunks(rnd, 1+rnd.Intn(30))
			diags := &Diags{}
			resolveRequisites(chunks, diags)
			out := order(chunks, diags)
			// Ordering is a permutation: nothing may be dropped or
			// duplicated, or a state silently never runs.
			if len(out) != len(chunks) {
				t.Errorf("ordering %d chunks produced %d", len(chunks), len(out))
				return
			}
			seen := map[*Chunk]bool{}
			for _, c := range out {
				if seen[c] {
					t.Error("ordering produced the same chunk twice")
					return
				}
				seen[c] = true
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("resolution or ordering did not terminate")
	}
}

// A self-referential requisite is the smallest cycle and the one an
// operator writes by accident.
func TestSelfReferenceTerminates(t *testing.T) {
	c := &Chunk{ID: "a", SLS: "s", State: "file", Fun: "managed", Name: "a", Args: value.NewMap(0)}
	c.Reqs = []Req{{Kind: Require, Refs: []ReqRef{{ID: "a"}}}}
	chunks := []*Chunk{c}
	diags := &Diags{}
	resolveRequisites(chunks, diags)
	if got := order(chunks, diags); len(got) != 1 {
		t.Errorf("ordering = %v", got)
	}
}

// Property: the sort is stable. The same low state ordered twice gives the
// same order, and — the part that matters to a tree — chunks with no
// requisite between them stay in declaration order.
func TestTopologicalSortIsStable(t *testing.T) {
	rnd := rand.New(rand.NewSource(11))
	for i := 0; i < 500; i++ {
		n := 1 + rnd.Intn(25)
		seed := rnd.Int63()

		var runs [][]string
		for run := 0; run < 3; run++ {
			r := rand.New(rand.NewSource(seed))
			chunks := randomChunks(r, n)
			diags := &Diags{}
			resolveRequisites(chunks, diags)
			ordered := order(chunks, diags)
			ids := make([]string, len(ordered))
			for j, c := range ordered {
				ids[j] = c.ID
			}
			runs = append(runs, ids)
		}
		for run := 1; run < len(runs); run++ {
			if !sameOrder(runs[0], runs[run]) {
				t.Fatalf("the same low state ordered differently:\n  %v\n  %v", runs[0], runs[run])
			}
		}
	}
}

// SPEC section 11.4: states with no requisites run in declaration order.
// Existing trees rely on this far more than they should, so it is the
// property most likely to be noticed if it breaks.
func TestUnconstrainedChunksKeepDeclarationOrder(t *testing.T) {
	rnd := rand.New(rand.NewSource(13))
	for i := 0; i < 500; i++ {
		n := 2 + rnd.Intn(20)
		chunks := make([]*Chunk, n)
		for j := range chunks {
			chunks[j] = &Chunk{
				ID: fmt.Sprintf("s%d", j), SLS: "sls", Env: "base",
				State: "file", Fun: "managed", Name: fmt.Sprintf("/tmp/%d", j),
				Args: value.NewMap(0), DeclOrder: j,
			}
		}
		diags := &Diags{}
		resolveRequisites(chunks, diags)
		ordered := order(chunks, diags)
		for j, c := range ordered {
			if c.DeclOrder != j {
				ids := make([]string, len(ordered))
				for k, o := range ordered {
					ids[k] = o.ID
				}
				t.Fatalf("unconstrained chunks were reordered: %v", ids)
			}
		}
	}
}

// A requisite is a promise about order, and it is the promise the whole
// state system rests on: if `require` does not put its target first, every
// tree using it is silently wrong.
func TestARequisitePutsItsTargetFirst(t *testing.T) {
	rnd := rand.New(rand.NewSource(17))
	for i := 0; i < 500; i++ {
		n := 2 + rnd.Intn(15)
		chunks := make([]*Chunk, n)
		for j := range chunks {
			chunks[j] = &Chunk{
				ID: fmt.Sprintf("s%d", j), SLS: "sls", Env: "base",
				State: "file", Fun: "managed", Name: fmt.Sprintf("/tmp/%d", j),
				Args: value.NewMap(0), DeclOrder: j,
			}
		}
		// A chain of requisites in a random direction, which is acyclic by
		// construction: each edge points from a higher index to a lower.
		type edge struct{ before, after int }
		var edges []edge
		for j := 1; j < n; j++ {
			if rnd.Intn(2) == 0 {
				continue
			}
			before := rnd.Intn(j)
			chunks[j].Reqs = append(chunks[j].Reqs, Req{
				Kind: Require, Refs: []ReqRef{{ID: chunks[before].ID}},
			})
			edges = append(edges, edge{before: before, after: j})
		}

		diags := &Diags{}
		resolveRequisites(chunks, diags)
		ordered := order(chunks, diags)

		pos := map[string]int{}
		for j, c := range ordered {
			pos[c.ID] = j
		}
		for _, e := range edges {
			b, a := chunks[e.before].ID, chunks[e.after].ID
			if pos[b] >= pos[a] {
				t.Fatalf("%s requires %s but runs at %d, before %s at %d", a, b, pos[a], b, pos[b])
			}
		}
	}
}

// SPEC section 11.5: prereq inverts the direction. It is the one requisite
// that puts a chunk *before* its target, and the indices the runner uses
// have to survive that.
func TestPrereqInvertsTheOrderAndKeepsIndicesValid(t *testing.T) {
	rnd := rand.New(rand.NewSource(19))
	for i := 0; i < 300; i++ {
		n := 2 + rnd.Intn(12)
		chunks := make([]*Chunk, n)
		for j := range chunks {
			chunks[j] = &Chunk{
				ID: fmt.Sprintf("s%d", j), SLS: "sls", Env: "base",
				State: "file", Fun: "managed", Name: fmt.Sprintf("/tmp/%d", j),
				Args: value.NewMap(0), DeclOrder: j,
			}
		}
		src, dst := rnd.Intn(n), rnd.Intn(n)
		if src == dst {
			continue
		}
		chunks[src].Reqs = []Req{{Kind: Prereq, Refs: []ReqRef{{ID: chunks[dst].ID}}}}

		diags := &Diags{}
		resolveRequisites(chunks, diags)
		ordered := order(chunks, diags)

		pos := map[string]int{}
		for j, c := range ordered {
			pos[c.ID] = j
		}
		if pos[chunks[src].ID] >= pos[chunks[dst].ID] {
			t.Fatalf("prereq did not put s%d before s%d", src, dst)
		}

		// Every resolved index must point into the ordered slice, since
		// that is what the runner indexes its results by.
		for _, c := range ordered {
			for _, req := range c.Reqs {
				for _, idx := range req.Resolved {
					if idx < 0 || idx >= len(ordered) {
						t.Fatalf("%s has a resolved index %d outside 0..%d", c.ID, idx, len(ordered))
					}
				}
			}
		}
	}
}

func sameOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
