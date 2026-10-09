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

func loadHubYAML(t *testing.T, body string) *config.Config {
	t.Helper()
	base := t.TempDir()
	conf := filepath.Join(base, "hub.yaml")
	if err := os.WriteFile(conf, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.Hub, config.LoadOptions{Path: conf, Root: base, AllowMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

const awsSource = "ext_pillar:\n  - aws_secrets_manager:\n      region: us-east-1\n"

// A hub that names external pillar sources and has no pillar roots never
// runs them: it compiles no pillar, so the sources were never read, and a
// misconfigured one started without complaint. It is refused now, and
// `doctor` says so too. DIVERGENCE 5.258.
func TestExternalPillarWithoutPillarRootsIsRefused(t *testing.T) {
	err := extPillarWithoutRoots(loadHubYAML(t, awsSource))
	if err == nil || !strings.Contains(err.Error(), "pillar_roots") {
		t.Fatalf("ext_pillar with no pillar_roots gave %v", err)
	}

	res := hubPillarCheck(loadHubYAML(t, awsSource), redact.New().Add).Run(context.Background())
	if res.Status != doctor.Fail || !strings.Contains(res.Detail, "pillar_roots") {
		t.Errorf("doctor on ext_pillar with no pillar_roots: %s %q", res.Status, res.Detail)
	}
}

// What is still allowed: roots with sources, roots without, neither, and
// an empty source list, which names nothing to lose.
func TestExternalPillarWithRootsOrNothingIsAllowed(t *testing.T) {
	root := filepath.ToSlash(t.TempDir())
	for name, body := range map[string]string{
		"roots and sources": "pillar_roots:\n  base:\n    - " + root + "\n" + awsSource,
		"roots alone":       "pillar_roots:\n  base:\n    - " + root + "\n",
		"neither":           "log_level: info\n",
		"an empty list":     "ext_pillar: []\n",
	} {
		if err := extPillarWithoutRoots(loadHubYAML(t, body)); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}
