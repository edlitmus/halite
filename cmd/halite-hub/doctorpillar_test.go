package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/doctor"
	"github.com/edlitmus/halite/internal/redact"
)

// hubDoctorPillar runs the hub's pillar check against a tree written
// here, with the hub configuration written here, which is the only way
// to tell whether the check compiles the way the hub itself does.
func hubDoctorPillar(t *testing.T, hubYAML, top string, files map[string]string) doctor.Result {
	t.Helper()
	secrets := redact.New()
	base := t.TempDir()
	pillarRoot := filepath.Join(base, "pillar")
	if err := os.MkdirAll(pillarRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pillarRoot, "top.sls"), []byte(top), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		full := filepath.Join(pillarRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(base, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(base, "etc", "hub.yaml")
	body := "pillar_roots:\n  base:\n    - " + filepath.ToSlash(pillarRoot) + "\n" + hubYAML
	if err := os.WriteFile(conf, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.Hub, config.LoadOptions{Path: conf, Root: base, AllowMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	return hubPillarCheck(cfg, secrets.Add).Run(context.Background())
}

// A pillar top branches on a grain -- that is what a top file is for --
// and the estate's does it with `salt['grains.get']`. The check built a
// compiler with no dispatcher, so that call was undefined and the check
// reported a broken pillar on every estate with a top file like this
// one. The hub compiles it perfectly well when a node asks.
func TestTheDoctorsPillarCheckHasASaltDispatcher(t *testing.T) {
	res := hubDoctorPillar(t, "",
		"base:\n  '*':\n    - common\n    - repo.{{ salt['grains.get']('envtype', 'dev') }}\n",
		map[string]string{
			"common.sls":    "java_home: /usr/lib/jdk17\n",
			"repo/dev.sls":  "repo: dev\n",
			"repo/prod.sls": "repo: prod\n",
		})

	if res.Status != doctor.Pass {
		t.Fatalf("status = %v, detail %q", res.Status, res.Detail)
	}
}

// And it judges targeting against the allowlist this hub serves with.
// An estate that has deliberately widened `pillar_trusted_grains` --
// the lab hub names six grains beyond SPEC 12.4's default -- had every
// one of those reported as unsafe, because the check was reading the
// default instead of the configuration beside it.
func TestTheDoctorsPillarCheckUsesTheConfiguredTrustedGrains(t *testing.T) {
	const top = "base:\n  'G@roles:web':\n    - common\n"
	files := map[string]string{"common.sls": "java_home: /usr/lib/jdk17\n"}

	// `roles` is not in SPEC 12.4's default allowlist.
	strict := hubDoctorPillar(t, "", top, files)
	if strict.Status != doctor.Fail {
		t.Fatalf("an untrusted grain was accepted: %v %q", strict.Status, strict.Detail)
	}
	if !strings.Contains(strict.Detail, "roles") {
		t.Errorf("the detail does not name the grain: %q", strict.Detail)
	}

	// The same tree, on a hub that has allowed it.
	widened := hubDoctorPillar(t, "pillar_trusted_grains:\n  - id\n  - roles\n", top, files)
	if widened.Status != doctor.Pass {
		t.Fatalf("the configured allowlist was ignored: %v %q", widened.Status, widened.Detail)
	}
}
