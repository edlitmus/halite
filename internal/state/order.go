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

	for i, c := range chunks {
		for _, req := range c.Reqs {
			for _, target := range req.Resolved {
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

	ready := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if indegree[i] == 0 {
			ready = append(ready, i)
		}
	}
	sort.SliceStable(ready, func(a, b int) bool { return less(ready[a], ready[b]) })

	out := make([]*Chunk, 0, n)
	placed := make([]bool, n)
	for len(ready) > 0 {
		// Take the highest-priority ready chunk. The set is small in
		// practice, so a linear scan beats a heap and keeps the ordering
		// obviously stable.
		best := 0
		for i := 1; i < len(ready); i++ {
			if less(ready[i], ready[best]) {
				best = i
			}
		}
		idx := ready[best]
		ready = append(ready[:best], ready[best+1:]...)

		placed[idx] = true
		chunks[idx].RunNum = len(out)
		out = append(out, chunks[idx])

		for _, dep := range dependents[idx] {
			indegree[dep]--
			if indegree[dep] == 0 {
				ready = append(ready, dep)
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
