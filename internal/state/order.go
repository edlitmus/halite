package state

import (
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/value"
)

// order arranges chunks into their run order: a stable topological sort
// over the requisite graph, with declaration order as the tiebreak.
//
// Two consequences are specified explicitly by SPEC section 11.4 and are
// tested here. A state with no requisites runs in declaration order
// relative to other unconstrained states, which existing trees rely on far
// more than they should. And `order: <int>` and `order: last` set an
// explicit position that is honoured before the tiebreak.
func order(chunks []*Chunk, diags *Diags) []*Chunk {
	n := len(chunks)
	if n == 0 {
		return chunks
	}

	// edges[i] holds the chunks that must run before chunk i.
	edges := make([][]int, n)
	addEdge := func(before, after int) {
		if before == after {
			return
		}
		for _, x := range edges[after] {
			if x == before {
				return
			}
		}
		edges[after] = append(edges[after], before)
	}

	// A chunk that is its own requisite is a cycle of one hop, and Salt
	// refuses it as one. addEdge used to drop the edge without a word, so
	// `require: [test: a]` on `a` ran as if it were not there, and a
	// self-onchanges skipped the state every run. Captured from salt-call
	// --local, 3006.28 and 3008.3, on 2026-10-28: require, watch,
	// onchanges, prereq, the bare-ID form, require_in, `sls:` naming the
	// state's own file and a `*` glob that matches it were each refused as
	// a recursive requisite -- 3008.3 refusing the whole run, as a cycle
	// here does; 3006.28 failing that state and running the rest
	// (DIVERGENCE 5.461). listen constrains no order and was not
	// captured, so it is left alone.
	selfReported := make([]bool, n)

	for i, c := range chunks {
		for _, req := range c.Reqs {
			for _, target := range req.Resolved {
				if target == i && req.Kind != Listen && !selfReported[i] {
					selfReported[i] = true
					diags.Add(c.Pos, c.SLS, c.ID, "requisite cycle: %s", renderCycle(chunks, []int{i, i}))
				}
				switch req.Kind {
				case Prereq:
					// prereq inverts the usual direction: B declares
					// `prereq: A` and runs *before* A. SPEC section 11.5.
					addEdge(i, target)
				case Listen:
					// A listen reaction runs at the end of the run rather
					// than in place, so it constrains nothing here.
				default:
					addEdge(target, i)
				}
			}
		}
	}

	// Priority is the sort key for the ready set: an explicit order first,
	// then declaration order, then expansion order.
	priority := func(i int) (int, int, int, int) {
		c := chunks[i]
		switch c.Opts.OrderMode {
		case OrderExplicit:
			return 0, c.Opts.OrderValue, c.DeclOrder, c.SeqOrder
		case OrderLast:
			return 2, 0, c.DeclOrder, c.SeqOrder
		default:
			return 1, 0, c.DeclOrder, c.SeqOrder
		}
	}
	less := func(a, b int) bool {
		am, av, ad, as := priority(a)
		bm, bv, bd, bs := priority(b)
		switch {
		case am != bm:
			return am < bm
		case av != bv:
			return av < bv
		case ad != bd:
			return ad < bd
		default:
			return as < bs
		}
	}

	indegree := make([]int, n)
	dependents := make([][]int, n)
	for after, befores := range edges {
		indegree[after] = len(befores)
		for _, before := range befores {
			dependents[before] = append(dependents[before], after)
		}
	}

	// The ready set is a binary heap. It was a slice scanned linearly
	// for the best chunk and then closed up with append, on the grounds
	// that "the set is small in practice"; with nothing constraining
	// them every chunk is ready at once, so that was quadratic in the
	// low state: 10,000 chunks took 0.3 s and 40,000 took 10.8 s
	// (DIVERGENCE 5.411).
	//
	// The order must be exactly the one the scan produced, ties
	// included. `less` is not a total order: the chunks of one
	// declaration with several functions share every key. The scan
	// broke such a tie by position in its slice, and that position was
	// the order of entry — the initial set in index order (its sort was
	// stable), then each chunk as it became ready. So each entry carries
	// its arrival number, and the heap breaks a tie on that.
	type entry struct{ idx, seq int }
	var ready []entry
	before := func(a, b entry) bool {
		if less(a.idx, b.idx) {
			return true
		}
		if less(b.idx, a.idx) {
			return false
		}
		return a.seq < b.seq
	}
	push := func(e entry) {
		ready = append(ready, e)
		for i := len(ready) - 1; i > 0; {
			parent := (i - 1) / 2
			if !before(ready[i], ready[parent]) {
				break
			}
			ready[i], ready[parent] = ready[parent], ready[i]
			i = parent
		}
	}
	pop := func() entry {
		top := ready[0]
		last := len(ready) - 1
		ready[0] = ready[last]
		ready = ready[:last]
		for i := 0; ; {
			smallest, l, r := i, 2*i+1, 2*i+2
			if l < len(ready) && before(ready[l], ready[smallest]) {
				smallest = l
			}
			if r < len(ready) && before(ready[r], ready[smallest]) {
				smallest = r
			}
			if smallest == i {
				break
			}
			ready[i], ready[smallest] = ready[smallest], ready[i]
			i = smallest
		}
		return top
	}

	seq := 0
	for i := 0; i < n; i++ {
		if indegree[i] == 0 {
			push(entry{i, seq})
			seq++
		}
	}

	out := make([]*Chunk, 0, n)
	placed := make([]bool, n)
	for len(ready) > 0 {
		idx := pop().idx

		placed[idx] = true
		chunks[idx].RunNum = len(out)
		out = append(out, chunks[idx])

		for _, dep := range dependents[idx] {
			indegree[dep]--
			if indegree[dep] == 0 {
				push(entry{dep, seq})
				seq++
			}
		}
	}

	if len(out) != n {
		reportCycle(chunks, edges, placed, diags)
		// The unplaced chunks are appended in declaration order so that a
		// caller inspecting the low state still sees every chunk. The
		// compilation has already failed.
		var rest []int
		for i := 0; i < n; i++ {
			if !placed[i] {
				rest = append(rest, i)
			}
		}
		sort.SliceStable(rest, func(a, b int) bool { return less(rest[a], rest[b]) })
		for _, i := range rest {
			chunks[i].RunNum = len(out)
			out = append(out, chunks[i])
		}
	}

	remapResolved(chunks, out)
	return out
}

// remapResolved rewrites every requisite's resolved indices to positions
// in the ordered slice.
//
// Resolution happens before ordering, so the indices it produces are
// positions in the declaration-ordered slice. The runner indexes its
// results by run position. Those two agree only while ordering moves
// nothing, which is why the bug this prevents showed up first under
// prereq: it is the one requisite that puts a chunk *before* its target.
// The contract, from here on, is that Resolved indexes the ordered slice.
func remapResolved(before, after []*Chunk) {
	position := make(map[*Chunk]int, len(after))
	for i, c := range after {
		position[c] = i
	}
	for _, c := range after {
		for ri := range c.Reqs {
			resolved := c.Reqs[ri].Resolved
			for j, old := range resolved {
				if old < 0 || old >= len(before) {
					continue
				}
				if newPos, ok := position[before[old]]; ok {
					resolved[j] = newPos
				}
			}
		}
	}
}

// reportCycle finds one requisite cycle and prints it as a path, which is
// what Salt does not do: its recursion message names nothing.
func reportCycle(chunks []*Chunk, edges [][]int, placed []bool, diags *Diags) {
	// The unplaced set is exactly the set involved in, or downstream of, a
	// cycle. Depth-first search inside it finds a concrete cycle.
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := make([]int, len(chunks))
	var stack []int

	var walk func(int) []int
	walk = func(i int) []int {
		color[i] = grey
		stack = append(stack, i)
		for _, before := range edges[i] {
			if placed[before] {
				continue
			}
			if color[before] == grey {
				// Found it: the cycle is the stack from `before` onward.
				for s := range stack {
					if stack[s] == before {
						cyc := append([]int{}, stack[s:]...)
						return append(cyc, before)
					}
				}
			}
			if color[before] == white {
				if cyc := walk(before); cyc != nil {
					return cyc
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[i] = black
		return nil
	}

	for i := range chunks {
		if placed[i] || color[i] != white {
			continue
		}
		stack = stack[:0]
		if cyc := walk(i); cyc != nil {
			diags.Add(chunks[cyc[0]].Pos, chunks[cyc[0]].SLS, chunks[cyc[0]].ID,
				"requisite cycle: %s", renderCycle(chunks, cyc))
			return
		}
	}

	// A cycle exists but the search did not name it, which should not
	// happen; report the unresolved set rather than saying nothing.
	var names []string
	for i, c := range chunks {
		if !placed[i] {
			names = append(names, c.Describe())
		}
	}
	diags.Add(value.Pos{}, "", "", "requisite cycle among: %s", strings.Join(names, ", "))
}

// renderCycle prints a cycle the way an operator can follow it, naming the
// requisite kind on each hop.
func renderCycle(chunks []*Chunk, cyc []int) string {
	var b strings.Builder
	for i := 0; i < len(cyc)-1; i++ {
		from, to := chunks[cyc[i]], chunks[cyc[i+1]]
		if i > 0 {
			b.WriteString(" -> ")
		}
		fmt.Fprintf(&b, "%s -> %s", from.ID, kindBetween(from, to))
	}
	fmt.Fprintf(&b, " -> %s", chunks[cyc[len(cyc)-1]].ID)
	return b.String()
}

// kindBetween names the requisite that links two chunks, for the cycle
// message.
func kindBetween(from, to *Chunk) string {
	for _, req := range from.Reqs {
		if requisiteTargets(req, to) {
			return req.Kind.String()
		}
	}
	return "requires"
}

func requisiteTargets(req Req, to *Chunk) bool {
	for _, ref := range req.Refs {
		if ref.ID == to.ID {
			return true
		}
		if ref.SLS != "" && ref.SLS == to.SLS {
			return true
		}
	}
	return false
}
