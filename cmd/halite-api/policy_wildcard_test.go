package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/policy"
)

// The API authorizes the operator before it forwards to the hub under
// its own certificate (internal/api authorize), so its reading of a
// wildcard has to be the hub's. It loaded the policy and never told it
// which functions run arbitrary code, so `functions: ['*']` granted
// `cmd.run` here: an operator with a wildcard role reached cmd.run
// whenever the API's own role named it, which an API that serves
// anybody's cmd.run has to. And an extension function the hub does not
// ship is arbitrary code on the node whatever either of them knows.
func TestTheAPIsWildcardIsTheHubs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(`
roles:
  everything:
    - target: '*'
      functions: ['*']
bindings:
  - principal: 'cert:CN=root'
    roles: ['everything']
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.API, config.LoadOptions{Root: t.TempDir(), AllowMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	loaded := loadPolicy(&service{cfg: cfg}, &cli.Args{Flags: map[string]string{"policy": path}})
	for _, fun := range []string{"cmd.run", "cmd.script", "myext.exec"} {
		if d := loaded.Authorize(policy.Request{Principal: "cert:CN=root", Target: "*", Fun: fun}); d.Allowed {
			t.Errorf("the API's policy let a wildcard grant %s", fun)
		}
	}
	if d := loaded.Authorize(policy.Request{Principal: "cert:CN=root", Target: "*", Fun: "test.ping"}); !d.Allowed {
		t.Errorf("a wildcard should grant test.ping: %s", d.Reason)
	}
}
