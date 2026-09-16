package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// grainStateContext stands in for the node: it holds the file the state
// owns, and the grains a run started with.
type grainStateContext struct {
	held      *value.Map
	collected *value.Map
	written   int
	reloaded  int
}

func (g *grainStateContext) context(test bool) *exec.Context {
	return &exec.Context{
		Test:   test,
		Grains: g.collected,
		LoadConfig: func(kind string) (*value.Map, error) {
			return g.held, nil
		},
		SaveConfig: func(kind string, running *value.Map) (string, error) {
			g.held = running
			g.written++
			return "/etc/halite/grains.d/99-runtime.yaml", nil
		},
		ReloadConfig: func(kind string) error {
			g.reloaded++
			return nil
		},
	}
}

func newGrainStateContext() *grainStateContext {
	return &grainStateContext{held: value.NewMap(0), collected: value.NewMap(0)}
}

func TestGrainsPresentSetsAndConverges(t *testing.T) {
	g := newGrainStateContext()

	res, err := grainsPresent(g.context(false), value.MapOf("name", "role", "value", "web"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("the first run should change: %+v", res)
	}
	if got, _ := g.held.Get("role"); got != "web" {
		t.Errorf("the file holds %v, want web", got)
	}
	if g.reloaded == 0 {
		t.Error("the grains were written and never re-read, so the run that set " +
			"them cannot see them")
	}

	// The second run reads the grain from the collected set, which is
	// where it lands once the node has re-read.
	g.collected.Set("role", "web")
	res, err = grainsPresent(g.context(false), value.MapOf("name", "role", "value", "web"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() || res.HasChanges() {
		t.Errorf("the second run should converge: %+v", res)
	}
}

// A nested name creates the mappings it needs and does not flatten the
// file.
func TestGrainsPresentWritesANestedName(t *testing.T) {
	g := newGrainStateContext()
	g.held.Set("role", "web")

	if _, err := grainsPresent(g.context(false),
		value.MapOf("name", "site:region", "value", "eu-west")); err != nil {
		t.Fatal(err)
	}

	site, ok := g.held.Get("site")
	if !ok {
		t.Fatal("site was not created")
	}
	m, ok := site.(*value.Map)
	if !ok {
		t.Fatalf("site is %T, want a mapping", site)
	}
	if got, _ := m.Get("region"); got != "eu-west" {
		t.Errorf("site:region = %v, want eu-west", got)
	}
	if got, _ := g.held.Get("role"); got != "web" {
		t.Error("setting one grain discarded another in the same file")
	}
}

// Test mode changes nothing on disk.
func TestGrainsStatesInTestModeWriteNothing(t *testing.T) {
	g := newGrainStateContext()

	res, err := grainsPresent(g.context(true), value.MapOf("name", "role", "value", "web"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil {
		t.Error("test mode should leave the result undecided, which is what a " +
			"would-change is")
	}
	if !res.HasChanges() {
		t.Error("test mode should still report what would change")
	}
	if g.written != 0 {
		t.Errorf("test mode wrote the file %d times", g.written)
	}
}

// A grain that came from the platform is not this state's to remove.
//
// The check reads the file the state owns, before writing. Reading
// c.Grains afterwards does not work — it is the snapshot the job started
// with, and reloading updates the node's grains rather than that copy,
// so every successful removal reported itself as a failure.
// `grains.absent` is Salt's, and Salt's does not delete by default: it
// sets the value to null. Every expectation here was captured from the
// Salt on this project's reference host, against a throwaway config so
// the live one was not disturbed.
//
// This state used to refuse any grain the node had not set for itself,
// which is what an estate's `grains.absent: node_exporter` hit -- a
// grain its static file defines as null already, where Salt answers
// "Grain is already set" and this answered with a failure.
func TestGrainsAbsentFollowsSalt(t *testing.T) {
	t.Run("a grain that does not exist is a success", func(t *testing.T) {
		g := newGrainStateContext()
		res, err := grainsAbsent(g.context(false), value.MapOf("name", "nothing"))
		if err != nil {
			t.Fatal(err)
		}
		if !res.Succeeded() || res.HasChanges() {
			t.Fatalf("%+v", res)
		}
		if !strings.Contains(res.Comment, "does not exist") {
			t.Errorf("comment = %q", res.Comment)
		}
	})

	t.Run("a grain already null is already in the state asked for", func(t *testing.T) {
		g := newGrainStateContext()
		g.collected.Set("node_exporter", nil)

		res, err := grainsAbsent(g.context(false), value.MapOf("name", "node_exporter"))
		if err != nil {
			t.Fatal(err)
		}
		if !res.Succeeded() {
			t.Fatalf("the estate's own case failed: %+v", res)
		}
		if res.HasChanges() {
			t.Errorf("it reported a change for a grain already null: %+v", res.Changes)
		}
		if g.written != 0 {
			t.Errorf("it wrote the file %d times with nothing to do", g.written)
		}
	})

	t.Run("a platform grain is set to null rather than refused", func(t *testing.T) {
		g := newGrainStateContext()
		g.collected.Set("kernel", "FreeBSD")

		res, err := grainsAbsent(g.context(false), value.MapOf("name", "kernel"))
		if err != nil {
			t.Fatal(err)
		}
		if !res.Succeeded() || !res.HasChanges() {
			t.Fatalf("%+v", res)
		}
		// The node's own file wins over the one underneath it, so a null
		// written here is what makes the grain null.
		v, ok := g.held.Get("kernel")
		if !ok || v != nil {
			t.Errorf("held = %v, %v; want an explicit null", v, ok)
		}
		if !strings.Contains(res.Comment, "set to None") {
			t.Errorf("comment = %q", res.Comment)
		}
	})

	t.Run("destructive deletes what this node owns", func(t *testing.T) {
		g := newGrainStateContext()
		g.held.Set("role", "web")
		g.collected.Set("role", "web")

		res, err := grainsAbsent(g.context(false),
			value.MapOf("name", "role", "destructive", true))
		if err != nil {
			t.Fatal(err)
		}
		if !res.Succeeded() || !res.HasChanges() {
			t.Fatalf("%+v", res)
		}
		if _, still := g.held.Get("role"); still {
			t.Error("the grain is still in the file")
		}
		if !strings.Contains(res.Comment, "was deleted") {
			t.Errorf("comment = %q", res.Comment)
		}
	})

	t.Run("the default sets null rather than deleting", func(t *testing.T) {
		g := newGrainStateContext()
		g.held.Set("role", "web")
		g.collected.Set("role", "web")

		if _, err := grainsAbsent(g.context(false), value.MapOf("name", "role")); err != nil {
			t.Fatal(err)
		}
		v, ok := g.held.Get("role")
		if !ok {
			t.Fatal("the default deleted the grain; Salt's sets it to null")
		}
		if v != nil {
			t.Errorf("held role = %v, want null", v)
		}
	})

	t.Run("a list or a mapping needs force", func(t *testing.T) {
		for _, v := range []any{[]any{"a"}, value.MapOf("k", "v")} {
			g := newGrainStateContext()
			g.collected.Set("structured", v)

			res, err := grainsAbsent(g.context(false), value.MapOf("name", "structured"))
			if err != nil {
				t.Fatal(err)
			}
			if res.Succeeded() {
				t.Errorf("%T was cleared without force", v)
			}
			if !strings.Contains(res.Comment, "force") {
				t.Errorf("the refusal does not name the argument: %q", res.Comment)
			}

			res, err = grainsAbsent(g.context(false),
				value.MapOf("name", "structured", "force", true))
			if err != nil {
				t.Fatal(err)
			}
			if !res.Succeeded() {
				t.Errorf("force did not clear %T: %q", v, res.Comment)
			}
		}
	})

	t.Run("test mode writes nothing and says which it would do", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			args       *value.Map
			wantPhrase string
		}{
			{"default", value.MapOf("name", "role"), "set to be deleted (None)"},
			{"destructive", value.MapOf("name", "role", "destructive", true), "is set to be deleted"},
		} {
			g := newGrainStateContext()
			g.held.Set("role", "web")
			g.collected.Set("role", "web")

			res, err := grainsAbsent(g.context(true), tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if !res.HasChanges() {
				t.Errorf("%s: test mode predicted no change", tc.name)
			}
			if g.written != 0 {
				t.Errorf("%s: test mode wrote the file", tc.name)
			}
			if !strings.Contains(res.Comment, tc.wantPhrase) {
				t.Errorf("%s: comment = %q, want %q in it", tc.name, res.Comment, tc.wantPhrase)
			}
		}
	})

	t.Run("it converges", func(t *testing.T) {
		g := newGrainStateContext()
		g.held.Set("role", "web")
		g.collected.Set("role", "web")

		if _, err := grainsAbsent(g.context(false), value.MapOf("name", "role")); err != nil {
			t.Fatal(err)
		}
		// The next run sees what the write produced.
		g.collected.Set("role", nil)
		res, err := grainsAbsent(g.context(false), value.MapOf("name", "role"))
		if err != nil {
			t.Fatal(err)
		}
		if !res.Succeeded() || res.HasChanges() {
			t.Errorf("the second run should converge: %+v", res)
		}
	})
}

// A one-shot command line has nowhere to persist a grain, and says so
// rather than reporting a change that does not survive.
func TestGrainsPresentWithNowhereToWriteSaysSo(t *testing.T) {
	res, err := grainsPresent(&exec.Context{Grains: value.NewMap(0)},
		value.MapOf("name", "role", "value", "web"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Error("a context with no way to persist should fail")
	}
}
