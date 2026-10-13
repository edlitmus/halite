package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// A node with no pillarenv asks for every environment's pillar with
// all_envs, and the hub merges them; a node that names one, or a node
// older than the field, gets that environment alone. DIVERGENCE 5.286.
func TestAPillarRequestForAllEnvironmentsMergesThem(t *testing.T) {
	l := newLab(t)
	roots := map[string][]string{}
	for env, files := range map[string]map[string]string{
		"base": {"top.sls": "base:\n  '*':\n    - common\n", "common.sls": "common_key: from-base\n"},
		"ref":  {"top.sls": "ref:\n  '*':\n    - refapp\n", "refapp.sls": "ref_key: from-ref\n"},
	} {
		dir := t.TempDir()
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		roots[env] = []string{dir}
	}
	l.server.Pillar = &PillarOptions{
		Roots:        fileserver.NewRoots(roots),
		Registry:     builtin.New().Exec,
		ConfigValues: value.NewMap(0),
	}
	node := l.enrolled(t, "web1.example")

	ask := func(req transport.PillarRequest) *value.Map {
		t.Helper()
		req.NodeID, req.Grains = "web1.example", json.RawMessage(`{}`)
		res, err := node.Pillar(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := value.DecodeJSON(res.Pillar)
		if err != nil {
			t.Fatal(err)
		}
		return decoded.(*value.Map)
	}

	all := ask(transport.PillarRequest{Env: "base", AllEnvs: true})
	for k, want := range map[string]string{"common_key": "from-base", "ref_key": "from-ref"} {
		if got, _ := all.Get(k); got != want {
			t.Errorf("all_envs: %s = %v, want %q; pillar %v", k, got, want, all)
		}
	}
	one := ask(transport.PillarRequest{Env: "base"})
	if _, ok := one.Get("ref_key"); ok {
		t.Errorf("a request without all_envs got ref's pillar: %v", one)
	}
	if got, _ := one.Get("common_key"); got != "from-base" {
		t.Errorf("a request without all_envs lost base's pillar: %v", one)
	}
}
