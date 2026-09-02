package builtin

import (
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
func TestGrainsAbsentRefusesAGrainItDoesNotOwn(t *testing.T) {
	g := newGrainStateContext()
	g.collected.Set("kernel", "FreeBSD")

	res, err := grainsAbsent(g.context(false), value.MapOf("name", "kernel"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Error("removing a platform grain should fail rather than report a " +
			"change the next run finds undone")
	}
	if g.written != 0 {
		t.Errorf("the file was written %d times for a grain the state does not own", g.written)
	}
}

func TestGrainsAbsentRemovesWhatItOwns(t *testing.T) {
	g := newGrainStateContext()
	g.held.Set("role", "web")
	g.collected.Set("role", "web")

	res, err := grainsAbsent(g.context(false), value.MapOf("name", "role"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("removing an owned grain should change: %+v", res)
	}
	if _, still := g.held.Get("role"); still {
		t.Error("the grain is still in the file")
	}

	// Converges: gone from both.
	g.collected = value.NewMap(0)
	res, err = grainsAbsent(g.context(false), value.MapOf("name", "role"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() || res.HasChanges() {
		t.Errorf("the second removal should converge: %+v", res)
	}
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
