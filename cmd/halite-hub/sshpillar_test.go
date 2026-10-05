package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
)

// sshPillarHub is a hub with a pillar tree and a flat roster of three
// agentless targets: web1 has role web, db1 is FreeBSD by its roster
// grains and so has platform bsd, broken1's pillar will not compile.
func sshPillarHub(t *testing.T) (*hubContext, *cli.Args) {
	t.Helper()
	root := t.TempDir()
	pillarRoot := filepath.Join(root, "pillar")
	if err := os.MkdirAll(pillarRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(pillarRoot, "top.sls"), `base:
  'web*':
    - web
  'os:FreeBSD':
    - match: grain
    - bsd
  'broken*':
    - broken
`)
	write(filepath.Join(pillarRoot, "web.sls"), "role: web\n")
	write(filepath.Join(pillarRoot, "bsd.sls"), "platform: bsd\n")
	write(filepath.Join(pillarRoot, "broken.sls"), "role: [this does not close\n")
	write(filepath.Join(root, "roster"), `web1.example:
  host: 192.0.2.1
  grains: {os: Linux}
db1.example:
  host: 192.0.2.2
  grains: {os: FreeBSD}
broken1.example:
  host: 192.0.2.3
  grains: {os: Linux}
`)
	write(filepath.Join(root, "hub.yaml"), "pillar_roots:\n  base:\n    - "+pillarRoot+"\n"+
		"pillar_trusted_grains: [os]\n")
	args := &cli.Args{Flags: map[string]string{"root": root}}
	return openHubForConfig(args), args
}

// Agentless targeting by pillar reads each target's pillar, compiled on
// the hub from the grains its roster entry attached -- the pillar the
// target would be sent.
//
// It read none. `-I platform:bsd` selected nothing, and `not I@role:web`
// selected the web hosts with everything else: an operator excluding
// the web tier from an agentless run ran it there. A target whose pillar
// will not compile has no answer and refuses the run, naming it, as the
// enrolled fleet's dispatch does (DIVERGENCE 5.204).
func TestAnAgentlessPillarTargetReadsEachTargetsCompiledPillar(t *testing.T) {
	h, args := sshPillarHub(t)
	ids := func(kind, expr string) ([]string, error) {
		targets, err := sshTargets(h, args, kind, expr)
		var out []string
		for _, t := range targets {
			out = append(out, t.ID)
		}
		sort.Strings(out)
		return out, err
	}

	for _, tc := range []struct {
		kind, expr, want string
	}{
		{"C", "L@web1.example,db1.example and I@platform:bsd", "db1.example"},
		{"C", "L@web1.example,db1.example and not I@role:web", "db1.example"},
	} {
		got, err := ids(tc.kind, tc.expr)
		if err != nil {
			t.Errorf("-%s %q: %v", tc.kind, tc.expr, err)
			continue
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("-%s %q selected %v, want [%s]", tc.kind, tc.expr, got, tc.want)
		}
	}

	for _, tc := range []struct{ kind, expr string }{
		{"C", "not I@role:db"},
		{"I", "role:web"},
	} {
		got, err := ids(tc.kind, tc.expr)
		if err == nil {
			t.Errorf("-%s %q selected %v with a target's pillar uncompilable", tc.kind, tc.expr, got)
			continue
		}
		if !strings.Contains(err.Error(), "broken1.example") {
			t.Errorf("-%s %q: the refusal does not name the target: %v", tc.kind, tc.expr, err)
		}
	}

	// Every target the expression has to read the pillar of is
	// decided, so one that cannot be holds up the run; one decided
	// before its pillar is read does not.
	got, err := ids("C", "L@db1.example or I@role:nobody")
	if err == nil {
		t.Errorf("a target set including the undecidable broken1 was not refused: %v", got)
	}
	got, err = ids("C", "L@web1.example and I@role:web")
	if err != nil || len(got) != 1 || got[0] != "web1.example" {
		t.Errorf("a target that never reads broken1's pillar gave %v, %v", got, err)
	}
}

// With no pillar tree there is nothing to target against, and the
// expression is refused rather than matched against an empty pillar.
func TestAnAgentlessPillarTargetOnAHubWithNoPillarIsRefused(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "roster"), []byte("web1.example: 192.0.2.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := &cli.Args{Flags: map[string]string{"root": root}}
	h := openHubForConfig(args)
	got, err := sshTargets(h, args, "C", "not I@role:db")
	if err == nil {
		t.Fatalf("a pillar target on a hub with no pillar selected %d target(s)", len(got))
	}
	if !strings.Contains(err.Error(), "pillar_roots") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}
