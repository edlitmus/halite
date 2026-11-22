package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/user"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/doctor"
	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/rendersandbox"
)

// requiringNode is a node with the given configuration and a log the test
// can read.
func requiringNode(t *testing.T, body string) (*node, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Load(config.Node, config.LoadOptions{Path: writeConfig(t, dir, "state_dir: "+dir+"\n"+body)})
	if err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	n := &node{cfg: cfg, log: testLogger(t, &logged)}
	t.Cleanup(n.closeRenderSandbox)
	return n, &logged
}

// Required with the sandbox off: nothing is rendered, rather than
// everything being rendered in this process under a setting that says
// the child must be confined. The node says so at startup, at error.
func TestARequirementWithTheSandboxOffRefusesEveryRender(t *testing.T) {
	n, logged := requiringNode(t, "render_sandbox_require: [identity]\n")

	_, err := render.Use(n.renderEngine()).Render([]byte("a: 1\n"), render.Options{File: "top.sls"})
	if !errors.Is(err, rendersandbox.ErrUnmet) {
		t.Fatalf("got %v, want the render refused", err)
	}
	n.reportRenderIsolation()
	if !strings.Contains(logged.String(), `"level":"error"`) || !strings.Contains(logged.String(), "render_sandbox is off") {
		t.Errorf("startup did not say every render will be refused, and why:\n%s", logged)
	}
}

// A misspelt requirement refuses rendering, rather than the node
// rendering unconfined under a list it could not read.
func TestAMisspeltRequirementRefusesEveryRender(t *testing.T) {
	n, _ := requiringNode(t, "render_sandbox: true\nrender_sandbox_require: [identiy]\n")
	_, err := render.Use(n.renderEngine()).Render([]byte("a: 1\n"), render.Options{File: "top.sls"})
	if err == nil || !strings.Contains(err.Error(), `"identiy"`) {
		t.Fatalf("got %v, want the misspelling refused by name", err)
	}
}

// Unmet at startup is said at startup, in the words the child's own
// report produced; met is said too, so an operator can tell the two
// apart without waiting for a highstate.
func TestStartupSaysWhetherTheRequiredControlsAreInForce(t *testing.T) {
	t.Run("unmet", func(t *testing.T) {
		n, logged := requiringNode(t, "render_sandbox: true\nrender_sandbox_require: [identity]\n")
		n.reportRenderIsolation()
		if !strings.Contains(logged.String(), `"level":"error"`) || !strings.Contains(logged.String(), "no render_sandbox_user") {
			t.Errorf("startup did not report the unmet requirement:\n%s", logged)
		}
	})
	t.Run("met", func(t *testing.T) {
		me, err := user.Current()
		if err != nil {
			t.Skipf("this machine cannot name the account the test runs as: %v", err)
		}
		n, logged := requiringNode(t, "render_sandbox: true\nrender_sandbox_user: "+me.Username+"\nrender_sandbox_require: [identity]\n")
		n.reportRenderIsolation()
		if strings.Contains(logged.String(), `"level":"error"`) || !strings.Contains(logged.String(), "in force: identity") {
			t.Errorf("the child runs as %s, as required, and startup did not say the control is in force:\n%s", me.Username, logged)
		}
	})
}

// doctor reports the uid the child reads for itself.
func TestDoctorReportsTheAccountTheRenderChildRunsAs(t *testing.T) {
	n, _ := requiringNode(t, "render_sandbox: true\n")
	res := nodeRenderIsolationCheck(n).Run(context.Background())
	if res.Status != doctor.Pass {
		t.Errorf("status %s: %s", res.Status, res.Detail)
	}
	if want := "uid " + strconv.Itoa(os.Getuid()); !strings.Contains(res.Detail, want) {
		t.Errorf("the detail does not report the child's %s: %s", want, res.Detail)
	}
}
