package builtin

import (
	"context"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// recordingMine keeps what was published and how.
type recordingMine struct {
	published []map[string]exec.MineValue
	replace   []bool
}

func (m *recordingMine) Publish(functions map[string]exec.MineValue, replace bool) error {
	m.published = append(m.published, functions)
	m.replace = append(m.replace, replace)
	return nil
}

func (m *recordingMine) Fetch(string, string, string) (*value.Map, error) {
	return value.NewMap(0), nil
}

// testDispatch lets the context call the execution modules, which is
// how `mine.update` computes what it publishes. Without it every mine
// function fails and the published set is silently empty -- which is
// how the first version of these tests passed while asserting nothing.
type testDispatch struct{ r *exec.Registry }

func (d testDispatch) Call(c *exec.Context, name string, args *value.Map) (any, error) {
	return d.r.Call(c, name, args)
}

func (d testDispatch) Has(name string) bool { return d.r.Has(name) }

func (d testDispatch) CallPositional(c *exec.Context, name string, args []any, kwargs *value.Map) (any, error) {
	return d.r.CallPositional(c, name, args, kwargs)
}

// mineCtx is a node with a mine and a `mine_functions` configuration.
func mineCtx(t *testing.T, mine exec.MineAccess, functions *value.Map) *exec.Context {
	t.Helper()
	cfg := value.NewMap(1)
	if functions != nil {
		cfg.Set("mine_functions", functions)
	}
	return &exec.Context{
		Ctx:      context.Background(),
		Grains:   value.MapOf("os", "Ubuntu", "os_family", "Debian"),
		Pillar:   value.NewMap(0),
		Config:   cfg,
		NodeID:   "web1.prod",
		Env:      "base",
		Mine:     mine,
		Runner:   &exec.RecordingRunner{},
		Dispatch: testDispatch{New().Exec},
	}
}

func callMineUpdate(t *testing.T, c *exec.Context, args *value.Map) any {
	t.Helper()
	r := New()
	out, err := r.Exec.Call(c, "mine.update", args)
	if err != nil {
		t.Fatalf("mine.update: %v", err)
	}
	return out
}

// `mine.update` merges by default, which is Salt's `clear=False`:
// `masterapi._mine` fetches what the node already published and calls
// `data.update(new_data)` over it. Replacing by default was the tidier
// rule and the wrong one -- it made a `mine_functions` refresh destroy
// every entry it did not name.
func TestMineUpdateMergesByDefault(t *testing.T) {
	m := &recordingMine{}
	c := mineCtx(t, m, value.MapOf("test.ping", nil))

	callMineUpdate(t, c, value.NewMap(0))

	if len(m.replace) != 1 {
		t.Fatalf("published %d times", len(m.replace))
	}
	if m.replace[0] {
		t.Error("mine.update replaced the node's mine instead of merging over it")
	}
}

// And `clear` asks for the old behaviour by name.
func TestMineUpdateClearReplaces(t *testing.T) {
	m := &recordingMine{}
	c := mineCtx(t, m, value.MapOf("test.ping", nil))

	callMineUpdate(t, c, value.MapOf("clear", true))

	if len(m.replace) != 1 || !m.replace[0] {
		t.Errorf("clear did not replace: %v", m.replace)
	}
}

// The tree this was found in writes `salt['mine.update'](”)`, and Salt
// takes it: `if not clear` is Python truthiness and an empty string is
// false. A declared Bool is coerced before the function runs and refuses
// it, which is why `clear` is read for truth rather than for type.
func TestMineUpdateTakesSaltsLooseArgument(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arg     any
		replace bool
	}{
		{"the estate's empty string", "", false},
		{"a non-empty string", "yes", true},
		{"zero", int64(0), false},
		{"one", int64(1), true},
		{"false", false, false},
		{"true", true, true},
	} {
		m := &recordingMine{}
		c := mineCtx(t, m, value.MapOf("test.ping", nil))
		callMineUpdate(t, c, value.MapOf("clear", tc.arg))
		if len(m.replace) != 1 {
			t.Fatalf("%s: published %d times", tc.name, len(m.replace))
		}
		if m.replace[0] != tc.replace {
			t.Errorf("%s: replace = %v, want %v", tc.name, m.replace[0], tc.replace)
		}
	}
}

// `mine_functions` refreshes a subset on its own schedule, which is the
// case the argument exists for.
func TestMineUpdateRefreshesOnlyWhatItIsGiven(t *testing.T) {
	m := &recordingMine{}
	c := mineCtx(t, m, value.MapOf("test.ping", nil, "test.echo", value.MapOf("text", "a")))

	callMineUpdate(t, c, value.MapOf("mine_functions", value.MapOf("test.ping", nil)))

	if len(m.published) != 1 {
		t.Fatalf("published %d times", len(m.published))
	}
	if _, ok := m.published[0]["test.ping"]; !ok {
		t.Errorf("the named function was not refreshed: %v", keysOfMine(m.published[0]))
	}
	if _, ok := m.published[0]["test.echo"]; ok {
		t.Errorf("a function that was not named was refreshed: %v", keysOfMine(m.published[0]))
	}
	// And it merged, so what it did not name is still served.
	if m.replace[0] {
		t.Error("a subset refresh replaced the whole mine")
	}
}

// Salt also takes a bare list of names, which is how a scheduled
// refresh is usually written.
func TestMineUpdateTakesAListOfFunctions(t *testing.T) {
	m := &recordingMine{}
	c := mineCtx(t, m, value.MapOf("test.ping", nil, "test.echo", value.MapOf("text", "a")))

	callMineUpdate(t, c, value.MapOf("mine_functions", []any{"test.ping"}))

	if len(m.published) != 1 {
		t.Fatalf("published %d times", len(m.published))
	}
	if _, ok := m.published[0]["test.ping"]; !ok {
		t.Errorf("the listed function was not refreshed: %v", keysOfMine(m.published[0]))
	}
	if _, ok := m.published[0]["test.echo"]; ok {
		t.Errorf("an unlisted function was refreshed: %v", keysOfMine(m.published[0]))
	}
}

// Nothing to publish is not the same as publishing nothing. Salt
// returns early, and with `clear` set an empty set would otherwise take
// the node's whole mine with it -- on a node whose `mine_functions` had
// simply not been read.
func TestMineUpdateWithNothingConfiguredPublishesNothing(t *testing.T) {
	m := &recordingMine{}
	c := mineCtx(t, m, nil)

	callMineUpdate(t, c, value.MapOf("clear", true))

	if len(m.published) != 0 {
		t.Errorf("an empty configuration published %v", m.published)
	}
}

func keysOfMine(m map[string]exec.MineValue) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
