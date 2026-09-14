package builtin

import (
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `defaults.merge`, and the in-place semantics that are the whole point.
//
// Every formula's map.jinja carries the same line:
//
//	{% do salt['defaults.merge'](defaults['salt'], lookup) %}
//
// and `{% do %}` discards the return value. An implementation that
// merged into a new mapping would leave the template with its defaults
// unmerged, correctly, silently, and with no error anywhere. That is the
// failure this test exists for; the merge itself is the easy half.
func TestDefaultsMergeMutatesTheDestination(t *testing.T) {
	r := New()
	c := &exec.Context{}

	dest := value.MapOf("pkg", "nginx", "config", value.MapOf("level", "info"))
	src := value.MapOf("config", value.MapOf("level", "debug", "extra", true))

	// The caller keeps its reference, exactly as a template does.
	if _, err := r.Exec.Call(c, "defaults.merge", value.MapOf("dest", dest, "src", src)); err != nil {
		t.Fatalf("defaults.merge: %v", err)
	}

	cfg, ok := dest.GetString("config")
	if !ok {
		t.Fatal("the destination lost its config key")
	}
	m, ok := cfg.(*value.Map)
	if !ok {
		t.Fatalf("config is %T, want a map", cfg)
	}
	if got, _ := m.GetString("level"); got != "debug" {
		t.Errorf("the merge did not reach the caller's mapping: level = %v, want debug", got)
	}
	if got, _ := m.GetString("extra"); got != true {
		t.Errorf("a new key did not reach the caller's mapping: extra = %v", got)
	}
	// A key the source did not mention survives, which is what makes it
	// a merge rather than a replace.
	if got, _ := dest.GetString("pkg"); got != "nginx" {
		t.Errorf("the merge dropped an untouched key: pkg = %v", got)
	}
}

// `in_place: false` is the other half of the contract: the caller's
// mapping is left alone and the merged copy comes back.
func TestDefaultsMergeCanLeaveTheDestinationAlone(t *testing.T) {
	r := New()
	c := &exec.Context{}

	dest := value.MapOf("level", "info")
	out, err := r.Exec.Call(c, "defaults.merge", value.MapOf(
		"dest", dest, "src", value.MapOf("level", "debug"), "in_place", false,
	))
	if err != nil {
		t.Fatalf("defaults.merge: %v", err)
	}
	if got, _ := dest.GetString("level"); got != "info" {
		t.Errorf("in_place: false still changed the destination: level = %v", got)
	}
	m, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("the return is %T, want a map", out)
	}
	if got, _ := m.GetString("level"); got != "debug" {
		t.Errorf("the returned copy was not merged: level = %v", got)
	}
}

// An in-place merge with nothing to merge into cannot have an effect, so
// it is refused rather than reported as success. Salt raises here too.
func TestDefaultsMergeRefusesANullDestinationInPlace(t *testing.T) {
	r := New()
	c := &exec.Context{}
	if _, err := r.Exec.Call(c, "defaults.merge", value.MapOf(
		"dest", nil, "src", value.MapOf("a", 1),
	)); err == nil {
		t.Error("an in-place merge into null was accepted")
	}
}

// `defaults.update` applies the defaults to each value of the mapping,
// which is what gives every node in a group the group's settings.
func TestDefaultsUpdateAppliesToEachValue(t *testing.T) {
	r := New()
	c := &exec.Context{}

	nodes := value.MapOf(
		"host01", value.MapOf("index", "foo"),
		"host02", value.MapOf("index", "bar"),
	)
	if _, err := r.Exec.Call(c, "defaults.update", value.MapOf(
		"dest", nodes, "src", value.MapOf("enabled", true),
	)); err != nil {
		t.Fatalf("defaults.update: %v", err)
	}
	for _, name := range []string{"host01", "host02"} {
		v, _ := nodes.GetString(name)
		m, ok := v.(*value.Map)
		if !ok {
			t.Fatalf("%s is %T, want a map", name, v)
		}
		if got, _ := m.GetString("enabled"); got != true {
			t.Errorf("%s did not receive the defaults: enabled = %v", name, got)
		}
		if _, ok := m.GetString("index"); !ok {
			t.Errorf("%s lost its own key", name)
		}
	}
}

// deepcopy exists so a formula can merge into a copy without disturbing
// the mapping it copied.
func TestDefaultsDeepcopyIsIndependent(t *testing.T) {
	r := New()
	c := &exec.Context{}

	orig := value.MapOf("config", value.MapOf("level", "info"))
	out, err := r.Exec.Call(c, "defaults.deepcopy", value.MapOf("source", orig))
	if err != nil {
		t.Fatalf("defaults.deepcopy: %v", err)
	}
	copied := out.(*value.Map)
	if _, err := r.Exec.Call(c, "defaults.merge", value.MapOf(
		"dest", copied, "src", value.MapOf("config", value.MapOf("level", "debug")),
	)); err != nil {
		t.Fatalf("merging into the copy: %v", err)
	}
	cfg, _ := orig.GetString("config")
	if got, _ := cfg.(*value.Map).GetString("level"); got != "info" {
		t.Errorf("merging into the copy changed the original: level = %v", got)
	}
}
