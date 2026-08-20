package target

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// SPEC section 31 names "targeting is monotonic under grain addition" as a
// property. It is what makes a target expression safe to reason about: a
// node that matches `G@role:web` must still match it after an unrelated
// grain appears, or a fleet's membership shifts every time a node learns
// something new about itself.
//
// The property holds for positive expressions. It cannot hold for a
// negated one — `not G@role:web` is monotonically *decreasing* by
// construction — so the generator below produces only positive
// expressions, and the negated case is asserted separately as the
// documented exception.

var grainNames = []string{"os", "os_family", "role", "env", "kernel", "virtual", "cpuarch"}

var grainValues = map[string][]string{
	"os":        {"FreeBSD", "Ubuntu", "Debian", "RedHat"},
	"os_family": {"FreeBSD", "Debian", "RedHat"},
	"role":      {"web", "db", "cache"},
	"env":       {"prod", "staging", "dev"},
	"kernel":    {"FreeBSD", "Linux"},
	"virtual":   {"physical", "kvm", "jail"},
	"cpuarch":   {"amd64", "arm64"},
}

func randomNode(rnd *rand.Rand, grainCount int) Node {
	g := value.NewMap(grainCount)
	names := append([]string{}, grainNames...)
	rnd.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
	for i := 0; i < grainCount && i < len(names); i++ {
		vals := grainValues[names[i]]
		g.Set(names[i], vals[rnd.Intn(len(vals))])
	}
	return Node{
		ID:     fmt.Sprintf("node%d.example.com", rnd.Intn(50)),
		Grains: g,
		Pillar: value.NewMap(0),
	}
}

// randomPositiveExpr builds a compound expression with no negation.
func randomPositiveExpr(rnd *rand.Rand, depth int) string {
	if depth <= 0 || rnd.Intn(3) == 0 {
		switch rnd.Intn(4) {
		case 0:
			name := grainNames[rnd.Intn(len(grainNames))]
			vals := grainValues[name]
			return "G@" + name + ":" + vals[rnd.Intn(len(vals))]
		case 1:
			name := grainNames[rnd.Intn(len(grainNames))]
			return "G@" + name + ":*"
		case 2:
			return fmt.Sprintf("node%d.example.com", rnd.Intn(50))
		default:
			return "node*"
		}
	}
	op := " and "
	if rnd.Intn(2) == 0 {
		op = " or "
	}
	left := randomPositiveExpr(rnd, depth-1)
	right := randomPositiveExpr(rnd, depth-1)
	if rnd.Intn(3) == 0 {
		return "(" + left + op + right + ")"
	}
	return left + op + right
}

// addGrain returns a copy of the node with one more grain, without
// disturbing any grain it already had.
func addGrain(rnd *rand.Rand, n Node) Node {
	g := value.NewMap(n.Grains.Len() + 1)
	for _, e := range n.Grains.Entries() {
		g.Set(e.Key, e.Val)
	}
	for _, name := range grainNames {
		if g.Has(name) {
			continue
		}
		vals := grainValues[name]
		g.Set(name, vals[rnd.Intn(len(vals))])
		break
	}
	// A grain outside the known set, which is what a custom grain is.
	if g.Len() == n.Grains.Len() {
		g.Set(fmt.Sprintf("custom%d", rnd.Intn(100)), "value")
	}
	return Node{ID: n.ID, Grains: g, Pillar: n.Pillar}
}

func TestTargetingIsMonotonicUnderGrainAddition(t *testing.T) {
	rnd := rand.New(rand.NewSource(23))
	for i := 0; i < 20000; i++ {
		expr := randomPositiveExpr(rnd, 3)
		m, err := CompileAuto(expr, nil)
		if err != nil {
			continue
		}
		node := randomNode(rnd, rnd.Intn(4))
		if !m.Match(node) {
			continue
		}
		// The node matched. Adding grains must not take that away, however
		// many are added.
		grown := node
		for step := 0; step < 5; step++ {
			grown = addGrain(rnd, grown)
			if !m.Match(grown) {
				t.Fatalf("%q matched a node with grains %v but stopped matching after they grew to %v",
					expr, node.Grains.StringKeys(), grown.Grains.StringKeys())
			}
		}
	}
}

// The exception, asserted rather than assumed: negation is monotonically
// decreasing, so a target using it is not safe to reason about the same
// way. This is a property of what `not` means, not a defect.
func TestNegationIsTheDocumentedExceptionToMonotonicity(t *testing.T) {
	m, err := CompileAuto("node1.example.com and not G@role:web", nil)
	if err != nil {
		t.Fatal(err)
	}
	bare := Node{ID: "node1.example.com", Grains: value.NewMap(0), Pillar: value.NewMap(0)}
	if !m.Match(bare) {
		t.Fatal("the node should match before the grain appears")
	}
	withRole := Node{
		ID:     "node1.example.com",
		Grains: value.MapOf("role", "web"),
		Pillar: value.NewMap(0),
	}
	if m.Match(withRole) {
		t.Error("a negated target that still matched after its grain appeared is a defect in the matcher, not in the property")
	}
}

// A matcher is a pure predicate: the same node gives the same answer every
// time, and matching one node never changes the answer for another. A
// matcher that cached across nodes would break both.
func TestMatchingIsPureAcrossNodes(t *testing.T) {
	rnd := rand.New(rand.NewSource(29))
	for i := 0; i < 2000; i++ {
		expr := randomPositiveExpr(rnd, 3)
		m, err := CompileAuto(expr, nil)
		if err != nil {
			continue
		}
		nodes := make([]Node, 5)
		first := make([]bool, len(nodes))
		for j := range nodes {
			nodes[j] = randomNode(rnd, rnd.Intn(5))
			first[j] = m.Match(nodes[j])
		}
		// Interleave the same nodes in a different order.
		for j := len(nodes) - 1; j >= 0; j-- {
			if got := m.Match(nodes[j]); got != first[j] {
				t.Fatalf("%q matched node %d as %v then %v", expr, j, first[j], got)
			}
		}
	}
}

// A node ID match must not depend on grains at all, since an ID is
// assigned rather than discovered.
func TestGlobMatchingIgnoresGrainsEntirely(t *testing.T) {
	m, err := CompileAuto("web*.prod", nil)
	if err != nil {
		t.Fatal(err)
	}
	rnd := rand.New(rand.NewSource(31))
	for i := 0; i < 1000; i++ {
		n := randomNode(rnd, rnd.Intn(7))
		n.ID = "web1.prod"
		if !m.Match(n) {
			t.Fatalf("a glob on the ID stopped matching because of grains %v", n.Grains.StringKeys())
		}
	}
}
