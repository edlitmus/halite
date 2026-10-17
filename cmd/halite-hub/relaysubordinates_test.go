package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/config"
)

func hubConfigFrom(t *testing.T, src string) (*config.Config, error) {
	t.Helper()
	base := t.TempDir()
	conf := filepath.Join(base, "hub.yaml")
	if err := os.WriteFile(conf, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return config.Load(config.Hub, config.LoadOptions{Path: conf, Root: base, AllowMissing: true})
}

// relay_subordinates is refused at load when it is not a mapping of
// relay ids to lists of globs: a relay whose entry did not parse would
// claim nothing, and that would be found when its nodes went missing.
func TestRelaySubordinatesIsReadOrRefusedAtLoad(t *testing.T) {
	good, err := hubConfigFrom(t, "relay_subordinates:\n  relay1.example: ['*.dmz', 'web1.lab']\n")
	if err != nil {
		t.Fatal(err)
	}
	got, err := relaySubordinatesFrom(good)
	if err != nil || strings.Join(got["relay1.example"], ",") != "*.dmz,web1.lab" {
		t.Errorf("read %v, %v", got, err)
	}
	for _, src := range []string{
		"relay_subordinates:\n  relay1.example: '*.dmz'\n",
		"relay_subordinates:\n  relay1.example: [1]\n",
		"relay_subordinates:\n  relay1.example: ['']\n",
	} {
		cfg, err := hubConfigFrom(t, src)
		if err != nil {
			continue
		}
		if _, err := relaySubordinatesFrom(cfg); err == nil {
			t.Errorf("%q was accepted", src)
		}
	}
}
