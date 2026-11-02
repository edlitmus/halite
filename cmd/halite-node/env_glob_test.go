package main

import (
	"testing"

	"github.com/edlitmus/halite/internal/config"
)

func envControls(t *testing.T, yaml string) *config.Config {
	t.Helper()
	cfg, err := config.Load(config.Node, config.LoadOptions{Path: writeConfig(t, t.TempDir(), yaml)})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The environment controls read their patterns as Salt's glob does. With
// path.Match, `[!b]*` denied `base` and permitted `dev`, and an entry that
// did not parse denied nothing. DIVERGENCE 5.551.
func TestEnvironmentControlsReadPatternsAsFnmatch(t *testing.T) {
	deny := envControls(t, "env_denylist: ['[!b]*', 'stage[']\n")
	for env, wantRefused := range map[string]bool{
		"base":   false, // starts with b: the negated set does not match it
		"dev":    true,  // does not start with b
		"stage[": true,  // an unclosed [ is a literal one
		"bstage": false,
	} {
		err := checkEnvPermitted(deny, env)
		if (err != nil) != wantRefused {
			t.Errorf("env_denylist ['[!b]*','stage[']: %q refused=%v (%v), want %v", env, err != nil, err, wantRefused)
		}
	}

	allow := envControls(t, "env_allowlist: ['[!p]*', 'pr-?']\n")
	for env, wantPermitted := range map[string]bool{
		"base": true, "dev": true, "prod": false, "pr-1": true, "pr-12": false,
	} {
		err := checkEnvPermitted(allow, env)
		if (err == nil) != wantPermitted {
			t.Errorf("env_allowlist ['[!p]*','pr-?']: %q permitted=%v (%v), want %v", env, err == nil, err, wantPermitted)
		}
	}

	// A control that names nothing refuses nothing.
	if err := checkEnvPermitted(envControls(t, "log_level: info\n"), "anything"); err != nil {
		t.Errorf("no controls refused an environment: %v", err)
	}
}
