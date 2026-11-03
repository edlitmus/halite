package target

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/fnmatch"
)

// A glob target became a compiled regexp with 5.501, and nothing bounded
// what that cost: one 1 MiB target allocated some 550 MB, and the regexp
// was kept for the life of the hub. DIVERGENCE 5.558.

// An over-long glob is refused at Compile, before any node is considered,
// wherever in an expression it is.
func TestAnOverLongGlobTargetIsRefused(t *testing.T) {
	long := "web*" + strings.Repeat("a", fnmatch.MaxPatternLen)
	for _, c := range []struct {
		name string
		kind Kind
		expr string
	}{
		{"glob", Glob, long},
		{"compound bare glob", Compound, "db1.prod or " + long},
		{"compound typed glob", Compound, "G@os:Ubuntu and G@os_family:" + long},
		{"grain", Grain, "os:" + long},
		{"pillar", Pillar, "role:" + long},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Compile(c.kind, c.expr, nil)
			var te *Error
			if !errors.As(err, &te) || !strings.Contains(te.Msg, "too long") {
				t.Fatalf("want a target error saying the glob is too long, got %v", err)
			}
			if c.kind == Compound && te.Col == 0 {
				t.Fatalf("the error names no column: %v", err)
			}
		})
	}

	// At the limit it compiles and matches.
	at := "web*" + strings.Repeat("a", fnmatch.MaxPatternLen-4)
	m, err := Compile(Glob, at, nil)
	if err != nil {
		t.Fatalf("a glob of exactly %d bytes: %v", fnmatch.MaxPatternLen, err)
	}
	if m.Match(web1()) {
		t.Fatal("the glob at the limit matched a node it does not describe")
	}
	mustMatch(t, Glob, "web*", web1(), true)
}

// A whole expression is bounded too, because a term costs memory to parse
// whether or not it compiles to anything: measured, some 32 bytes
// allocated a byte of an or-chain of names, so the 64 MiB a request body
// may be was some 2 GB. The bound refuses the expression before it is
// tokenized, for every kind.
func TestAnOverLongExpressionIsRefused(t *testing.T) {
	names := func(n int, sep string) string {
		var b strings.Builder
		for i := 0; b.Len() < n; i++ {
			fmt.Fprintf(&b, "host%07d.example%s", i, sep)
		}
		return b.String()[:n]
	}
	for _, c := range []struct {
		kind Kind
		sep  string
	}{{Compound, " or "}, {List, ","}, {Glob, ""}, {Regex, "|"}, {Grain, ":"}} {
		t.Run(c.kind.String(), func(t *testing.T) {
			_, err := Compile(c.kind, names(MaxExprLen+1, c.sep), nil)
			var te *Error
			if !errors.As(err, &te) || !strings.Contains(te.Msg, "limit") {
				t.Fatalf("an expression of %d bytes: want a length error, got %v", MaxExprLen+1, err)
			}
			if len(err.Error()) > 300 {
				t.Fatalf("the error is %d bytes; it should not quote the expression", len(err.Error()))
			}
		})
	}
	// At the bound, a list of names compiles and matches its members: the
	// long target 5.291 kept is still a target.
	list := names(MaxExprLen-len(",web1.prod"), ",")
	list = list[:strings.LastIndex(list, ",")] + ",web1.prod"
	for len(list) < MaxExprLen {
		list = "," + list
	}
	m, err := Compile(List, list, nil)
	if err != nil {
		t.Fatalf("a list of exactly %d bytes: %v", len(list), err)
	}
	if !m.Match(web1()) || m.Match(db1()) {
		t.Fatal("a list at the bound does not match its members only")
	}
}

// A name is a literal, and a literal costs no regexp, so a long target of
// names is not limited by the glob bound or the budget. This is the shape
// 5.291 held should always compile.
func TestLiteralTermsCostNothingAgainstTheBudget(t *testing.T) {
	var b strings.Builder
	for i := range 20_000 {
		if i > 0 {
			b.WriteString(" or ")
		}
		fmt.Fprintf(&b, "host%05d.example.internal", i)
	}
	b.WriteString(" or web1.prod or G@os:Ubuntu")
	m, err := Compile(Compound, b.String(), nil)
	if err != nil {
		t.Fatalf("%d bytes of literal terms: %v", b.Len(), err)
	}
	if !m.Match(web1()) || m.Match(db1()) {
		t.Fatal("a long literal or-chain does not match its members only")
	}
}

// The globs and regular expressions of one expression share a budget of
// maxPatternBytes. Each glob alone is within MaxPatternLen, so without the
// budget an expression of many of them cost what one long one did.
func TestAnExpressionsPatternsShareABudget(t *testing.T) {
	glob := "web*" + strings.Repeat("z", 96) // 100 bytes, not a literal
	chain := func(n int, term string) string {
		return strings.TrimSuffix(strings.Repeat(term+" or ", n), " or ")
	}
	under := maxPatternBytes / len(glob)
	if _, err := Compile(Compound, chain(under, glob), nil); err != nil {
		t.Fatalf("%d bytes of globs: %v", under*len(glob), err)
	}
	for _, c := range []struct{ name, expr string }{
		{"globs", chain(under+1, glob)},
		{"grain globs", chain(under+1, "G@os:"+glob)},
		{"regexes", chain(under+1, "E@^"+strings.Repeat("z", 99))},
		{"grain regexes", chain(under+1, "P@os:^"+strings.Repeat("z", 99))},
		{"mixed", chain(under/2, glob) + " or " + chain(under-under/2+1, "E@^"+strings.Repeat("z", 99))},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Compile(Compound, c.expr, nil)
			var te *Error
			if !errors.As(err, &te) || !strings.Contains(te.Msg, "budget") {
				t.Fatalf("want a budget error, got %v", err)
			}
			if te.Col == 0 {
				t.Fatalf("the error names no column: %v", err)
			}
			if len(err.Error()) > len(c.expr)+300 {
				t.Fatalf("the error is %d bytes", len(err.Error()))
			}
		})
	}
}

// A grain or pillar target is read at every colon (5.537), and each reading
// is a pattern of its own. `G@a:a:...:*` with n colons was n globs, of
// lengths 1 to 2n, so the cost was the square of the length: every reading
// counts against the budget.
func TestEveryReadingOfAGrainTargetCountsAgainstTheBudget(t *testing.T) {
	expr := strings.Repeat("a:", 300) + "*"
	_, err := Compile(Grain, expr, nil)
	var te *Error
	if !errors.As(err, &te) || !strings.Contains(te.Msg, "budget") {
		t.Fatalf("a %d-byte grain target with 300 readings: want a budget error, got %v", len(expr), err)
	}
	// A MAC address, the case 5.537 was about, is nowhere near it.
	if _, err := Compile(Grain, "hwaddr_interfaces:eth0:aa:bb:cc:dd:ee:*", nil); err != nil {
		t.Fatal(err)
	}
}

// A nodegroup's expansion is part of the expression that names it, and
// spends the same budget.
func TestANodegroupSpendsTheSameBudget(t *testing.T) {
	glob := "web*" + strings.Repeat("z", 96)
	n := maxPatternBytes/len(glob)/2 + 1
	half := strings.TrimSuffix(strings.Repeat(glob+" or ", n), " or ")
	groups := Nodegroups{"half": half}
	if _, err := Compile(Compound, "N@half", groups); err != nil {
		t.Fatalf("one half: %v", err)
	}
	_, err := Compile(Compound, "N@half or "+half, groups)
	var te *Error
	if !errors.As(err, &te) || !strings.Contains(te.Msg, "budget") {
		t.Fatalf("two halves: want a budget error, got %v", err)
	}
}

const heldHelperEnv = "HALITE_TARGET_HELD_HELPER"

// TestHeldMemoryHelper is the child for TestATargetsGlobsAreNotKept.
func TestHeldMemoryHelper(t *testing.T) {
	if os.Getenv(heldHelperEnv) == "" {
		t.Skip("child process for TestATargetsGlobsAreNotKept; runs only when that test starts it")
	}
	debug.SetMemoryLimit(512 << 20)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	// 300 distinct targets, each one glob of the dearest measured shape
	// at the length limit, compiled, matched and let go, as a stream of
	// jobs would. Some 120 KB each compiled.
	for i := range 300 {
		expr := fmt.Sprintf("%04d", i) + strings.Repeat("*a", fnmatch.MaxPatternLen/2-2)
		m, err := Compile(Glob, expr, nil)
		if err != nil {
			t.Fatal(err)
		}
		m.Match(web1())
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	fmt.Printf("HELD: %d\n", int64(after.HeapAlloc)-int64(before.HeapAlloc))
}

// A target's globs live as long as its Matcher and no longer. Before this
// they went into a map that never forgot them; a bounded cache would hold
// some megabytes of them. The heap is measured in a child, so that what
// the old code kept is not kept by this process.
func TestATargetsGlobsAreNotKept(t *testing.T) {
	if os.Getenv(heldHelperEnv) != "" {
		t.Skip("in the child")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHeldMemoryHelper$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), heldHelperEnv+"=1", "GOMAXPROCS=2")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	i := strings.Index(string(out), "HELD: ")
	if i < 0 {
		t.Fatalf("the child reported nothing:\n%s", out)
	}
	var held int64
	fmt.Sscanf(string(out[i:]), "HELD: %d", &held)
	// Nothing should be held. 1 MB is room for the runtime; one cached
	// glob of this shape is a tenth of it, and a cache of fnmatch's
	// bound holds several megabytes.
	if held > 1<<20 {
		t.Fatalf("300 distinct glob targets, let go, left %d KB held", held>>10)
	}
	t.Logf("held after 300 distinct glob targets: %d KB", held>>10)
}
