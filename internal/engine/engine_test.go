package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/modules"
	"github.com/edlitmus/halite/internal/sls"
)

func run(t *testing.T, states []sls.State) []StateResult {
	t.Helper()
	ctx := &modules.Ctx{Grains: map[string]any{"os": "test"}}
	return Run(ctx, states)
}

// onchanges: the dependent runs only when the referenced state changed.
func TestOnChanges(t *testing.T) {
	target := filepath.Join(t.TempDir(), "f")
	states := []sls.State{
		{ID: "make_file", Module: "file", Fn: "managed",
			Args: map[string]any{"name": target, "contents": "x"}},
		{ID: "react", Module: "cmd", Fn: "run",
			Args:      map[string]any{"name": "echo reacted"},
			OnChanges: []sls.Ref{{Module: "file", ID: "make_file"}}},
	}

	// First run: file is created, so the cmd fires.
	r := run(t, states)
	if !r[1].Res.Changed {
		t.Fatalf("first run: onchanges should fire: %+v", r[1].Res)
	}
	// Second run: file unchanged, cmd skipped.
	r = run(t, states)
	if r[1].Res.Changed || !r[1].Res.Ok {
		t.Fatalf("second run: onchanges should skip: %+v", r[1].Res)
	}
}

// prereq: the declaring state runs first, and only when the target would
// make changes.
func TestPrereq(t *testing.T) {
	target := filepath.Join(t.TempDir(), "f")
	// Note: order here mimics post-sort order (prereq state first).
	states := []sls.State{
		{ID: "before", Module: "cmd", Fn: "run",
			Args:   map[string]any{"name": "echo before"},
			Prereq: []sls.Ref{{Module: "file", ID: "make_file"}}},
		{ID: "make_file", Module: "file", Fn: "managed",
			Args: map[string]any{"name": target, "contents": "x"}},
	}

	// First run: make_file would change, so "before" runs, then make_file.
	r := run(t, states)
	if !r[0].Res.Changed {
		t.Fatalf("first run: prereq state should run: %+v", r[0].Res)
	}
	if !r[1].Res.Changed {
		t.Fatalf("first run: target should change: %+v", r[1].Res)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "x\n" {
		t.Fatalf("target content wrong: %q %v", b, err)
	}

	// Second run: target would not change, "before" is skipped.
	r = run(t, states)
	if r[0].Res.Changed {
		t.Fatalf("second run: prereq state should skip: %+v", r[0].Res)
	}
	if r[1].Res.Changed {
		t.Fatalf("second run: target should be unchanged: %+v", r[1].Res)
	}
}

// The prereq dry run must not mutate the system.
func TestPrereqDryRunHasNoSideEffects(t *testing.T) {
	target := filepath.Join(t.TempDir(), "f")
	states := []sls.State{
		{ID: "before", Module: "cmd", Fn: "run",
			Args:   map[string]any{"name": "true"},
			Prereq: []sls.Ref{{Module: "file", ID: "make_file"}}},
		{ID: "make_file", Module: "file", Fn: "managed",
			Args: map[string]any{"name": target, "contents": "x"}},
	}
	_ = run(t, states[:1]) // only the prereq state; target never actually runs
	if _, err := os.Stat(target); err == nil {
		t.Fatal("dry run created the target file")
	}
}
