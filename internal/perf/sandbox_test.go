package perf

import (
	"fmt"
	"os"
	"testing"

	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/rendersandbox"
	"github.com/edlitmus/halite/internal/state"
)

// What SPEC 25.4's render sandbox costs, measured against the same tree
// the highstate target is measured with.
//
// The question this answers is the one that decides whether the sandbox
// can ever be the default: a control that doubles the time a highstate
// takes is one an operator turns off, and a number nobody has measured
// is how that argument gets had twice. The comparison is exact, because
// it is the same 500 states over the same 50 files -- only the engine
// changes.
//
// TestMain makes this test binary its own render child, which is the
// same arrangement internal/rendersandbox's tests use: the sandbox
// re-executes the program it is running inside, and the child is
// recognised by the single environment variable the parent sets.

func TestMain(m *testing.M) {
	if os.Getenv("HALITE_RENDER_SANDBOX") == "1" {
		if err := rendersandbox.Serve(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// sandboxedCompiler is the highstate compiler with its rendering moved
// into a child process.
func sandboxedCompiler(tb testing.TB) *state.Compiler {
	tb.Helper()
	s := rendersandbox.New(rendersandbox.Config{Exe: os.Args[0], Stderr: os.Stderr})
	tb.Cleanup(func() { _ = s.Close() })

	c := highstateCompiler()
	c.Config.Engine = s
	return c
}

// BenchmarkHighstateCompileSandboxed is the highstate row again, with
// every file parsed and rendered in the unprivileged child.
//
// It is not the SPEC 30 row itself: the target is stated for a node, and
// a node renders in this process unless an operator turns the sandbox
// on. It is what the operator is choosing between, which is why it sits
// beside the other and not in the targets table.
func BenchmarkHighstateCompileSandboxed(b *testing.B) {
	c := sandboxedCompiler(b)
	if out := c.CompileHighstate(); out.Err() != nil {
		b.Fatalf("the generated state tree does not compile in the sandbox:\n%v", out.Err())
	}

	b.ReportAllocs()
	for b.Loop() {
		out := c.CompileHighstate()
		if len(out.Low) != highstateDecls {
			b.Fatalf("compiled %d chunks, want %d", len(out.Low), highstateDecls)
		}
	}
	reportMillis(b)
}

// TestTheSandboxCompilesTheSameTree is the assertion the benchmark
// cannot make: that the two engines produce the same low state, chunk
// for chunk and argument for argument.
//
// A faster sandbox that compiled something else would look like a win in
// the numbers above, and this is what stops that reading.
func TestTheSandboxCompilesTheSameTree(t *testing.T) {
	sandboxed := sandboxedCompiler(t).CompileHighstate()
	if err := sandboxed.Err(); err != nil {
		t.Fatalf("the sandboxed compilation failed:\n%v", err)
	}
	direct := compileHighstate(t)

	if len(sandboxed.Low) != len(direct.Low) {
		t.Fatalf("the sandbox compiled %d chunks and this process %d", len(sandboxed.Low), len(direct.Low))
	}
	for i := range direct.Low {
		want, got := direct.Low[i], sandboxed.Low[i]
		if want.ID != got.ID || want.State != got.State || want.Fun != got.Fun {
			t.Fatalf("chunk %d is %s.%s %q sandboxed and %s.%s %q here",
				i, got.State, got.Fun, got.ID, want.State, want.Fun, want.ID)
		}
		if a, b := describeArgs(got), describeArgs(want); a != b {
			t.Fatalf("chunk %d (%s) has different arguments:\n  sandboxed: %s\n  in-process: %s",
				i, want.ID, a, b)
		}
		// The positions are what a diagnostic points at, and they cross
		// the boundary encoded rather than as Go values.
		if want.Pos != got.Pos {
			t.Fatalf("chunk %d (%s) is at %s sandboxed and %s here", i, want.ID, got.Pos, want.Pos)
		}
	}
}

func describeArgs(ch *state.Chunk) string {
	if ch.Args == nil {
		return "<none>"
	}
	out := ""
	for _, e := range ch.Args.Entries() {
		out += fmt.Sprintf("%v=%#v;", e.Key, e.Val)
	}
	return out
}

// TestTheSandboxedRenderIsNotSecretlyInProcess guards the measurement
// itself: a benchmark whose engine quietly fell back would report the
// in-process number under the sandbox's name.
func TestTheSandboxedRenderIsNotSecretlyInProcess(t *testing.T) {
	s := rendersandbox.New(rendersandbox.Config{Exe: os.Args[0], Stderr: os.Stderr})
	t.Cleanup(func() { _ = s.Close() })

	c := highstateCompiler()
	c.Config.Engine = s
	if out := c.CompileHighstate(); out.Err() != nil {
		t.Fatalf("compiling: %v", out.Err())
	}
	if s.PID() == 0 || s.PID() == os.Getpid() {
		t.Fatalf("the sandboxed compilation ran in process %d, and this is %d", s.PID(), os.Getpid())
	}
	var _ render.Engine = s
}
