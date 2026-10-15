package target

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

// The compound parser and the tree it builds were both recursive with
// nothing bounding the recursion but the input. A Go stack overflow is a
// fatal error, not a panic: net/http cannot recover it, so one target
// expression from one API caller killed the hub (DIVERGENCE 5.290).
//
// These tests drive the hostile shapes in a child process with a small
// stack, because the failure being guarded against ends the process that
// suffers it. In the child the stack is capped at 4 MiB, so the old code
// dies after some tens of thousands of levels rather than after the
// gigabyte a default Go stack allows: the inputs below stay under a
// megabyte and the test does not need the machine to find out.

const stackHelperEnv = "HALITE_TARGET_STACK_HELPER"

// hostileShapes are expressions that the old code could not survive with
// a 4 MiB stack. Each is under a megabyte. want is what the fixed code
// must do with it: refuse with a nesting error, or compile and evaluate.
var hostileShapes = map[string]struct {
	expr      func() string
	wantError bool
	wantMatch bool
}{
	// 200,000 `(` is how H-12 was reproduced; it recursed through
	// parseOr, parseAnd, parseNot and parsePrimary per level.
	"parens": {
		expr: func() string {
			return strings.Repeat("(", 200_000) + "web1.prod" + strings.Repeat(")", 200_000)
		},
		wantError: true,
	},
	// `not` recursed through parseNot alone, and then again at match
	// time through a closure per `not`.
	"nots": {
		expr:      func() string { return strings.Repeat("not ", 200_000) + "web1.prod" },
		wantError: true,
	},
	// A flat chain does not recurse in the parser, which loops, but the
	// tree it built did: ((a or b) or c) or ... is one closure inside
	// the next, so Match recursed once per term. This is the shape a
	// long hand-built target actually has, which is why it must compile
	// and evaluate rather than be refused. No term matches, so every one
	// is evaluated.
	"or-chain": {
		expr:      func() string { return strings.TrimSuffix(strings.Repeat("nomatch or ", 200_000), " or ") },
		wantMatch: false,
	},
	// Every term matches, so every one is evaluated.
	"and-chain": {
		expr:      func() string { return strings.TrimSuffix(strings.Repeat("web1.prod and ", 200_000), " and ") },
		wantMatch: true,
	},
}

// TestCompoundStackHelper is the child. It does nothing in a normal run.
func TestCompoundStackHelper(t *testing.T) {
	name := os.Getenv(stackHelperEnv)
	if name == "" {
		t.Skip("child process for TestHostileTargetsDoNotOverflowTheStack; runs only when that test starts it")
	}
	shape, ok := hostileShapes[name]
	if !ok {
		t.Fatalf("no shape %q", name)
	}
	debug.SetMaxStack(4 << 20)
	expr := shape.expr()

	m, err := Compile(Compound, expr, nil)
	if shape.wantError {
		var te *Error
		if !errors.As(err, &te) || !strings.Contains(te.Msg, "nests deeper than") {
			t.Fatalf("want a nesting error, got %v", err)
		}
		fmt.Println("SURVIVED: refused")
		return
	}
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if got := m.Match(web1()); got != shape.wantMatch {
		t.Fatalf("Match = %v, want %v", got, shape.wantMatch)
	}
	fmt.Println("SURVIVED: evaluated")
}

func TestHostileTargetsDoNotOverflowTheStack(t *testing.T) {
	if os.Getenv(stackHelperEnv) != "" {
		t.Skip("in the child")
	}
	for name := range hostileShapes {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCompoundStackHelper$", "-test.count=1", "-test.v")
			cmd.Env = append(os.Environ(), stackHelperEnv+"="+name)
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "SURVIVED") {
				// Keep the tail: a stack overflow prints the whole
				// goroutine trace, which is mostly the same frame.
				s := string(out)
				if i := strings.Index(s, "fatal error"); i >= 0 {
					s = s[i:min(len(s), i+600)]
				} else if len(s) > 2000 {
					s = s[len(s)-2000:]
				}
				t.Fatalf("shape %q did not survive (%v):\n%s", name, err, s)
			}
		})
	}
}

// TestCompoundNestingLimitEdge holds the limit at both sides: a target at
// the bound compiles and evaluates, and one level more is an ordinary
// error, for both ways of nesting.
func TestCompoundNestingLimitEdge(t *testing.T) {
	cases := []struct {
		name string
		expr func(levels int) string
		// match is what the expression at the limit says about web1.
		match bool
	}{
		{"parens", func(n int) string {
			return strings.Repeat("( ", n) + "web1.prod" + strings.Repeat(" )", n)
		}, true},
		// An even count of `not` leaves the sense unchanged.
		{"nots", func(n int) string { return strings.Repeat("not ", n) + "web1.prod" }, true},
		{"mixed", func(n int) string {
			return strings.Repeat("not ( ", n/2) + "web1.prod" + strings.Repeat(" )", n/2)
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := Compile(Compound, c.expr(maxCompoundDepth), nil)
			if err != nil {
				t.Fatalf("at the limit (%d): %v", maxCompoundDepth, err)
			}
			if got := m.Match(web1()); got != c.match {
				t.Fatalf("at the limit: Match = %v, want %v", got, c.match)
			}
			// One `not` in front is exactly one level more, whatever
			// the shape inside it.
			_, err = Compile(Compound, "not "+c.expr(maxCompoundDepth), nil)
			var te *Error
			if !errors.As(err, &te) || !strings.Contains(te.Msg, "nests deeper than") {
				t.Fatalf("over the limit: want a nesting error, got %v", err)
			}
			if te.Col == 0 {
				t.Fatalf("over the limit: the error names no column: %v", err)
			}
		})
	}
}

// TestLongFlatTargetsStillCompile is the case a length cap would have
// broken: a list of node names written as a compound, as long as a large
// estate's. It must compile, match its members, and not match others.
func TestLongFlatTargetsStillCompile(t *testing.T) {
	var b strings.Builder
	for i := range 5000 {
		if i > 0 {
			b.WriteString(" or ")
		}
		fmt.Fprintf(&b, "L@host%05d.example.internal", i)
	}
	b.WriteString(" or web1.prod")
	m, err := Compile(Compound, b.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match(web1()) {
		t.Fatal("the last term of a long or-chain did not match")
	}
	if m.Match(db1()) {
		t.Fatal("a long or-chain matched a node it does not name")
	}
	if got := len(m.Terms()); got != 5001 {
		t.Fatalf("Terms() = %d, want 5001", got)
	}
}
