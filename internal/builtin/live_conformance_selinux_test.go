package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The seven `selinux.*` states through SPEC 11.6's harness, against a
// running policy.
//
// Each brings its own subject, under names live_selinux_test.go does not
// use, and puts back what its Setup changed: the case's own fcontext
// rule and directory, udp/18998, and httpd_can_network_connect returned
// to off. `selinux.mode` is the exception that cannot have a subject of
// its own -- there is one running mode -- so its Setup makes the node
// Permissive and the state under test makes it Enforcing again, which
// is also the direction its Cleanup writes. The node is never left
// permissive by a case that finished, and the config file is never
// touched: the state refuses rather than write it.
//
// `selinux.boolean` is runtime-only here. The persistent path is driven
// in TestLiveSelinuxBooleanRuntimeAndPersistent, whose guard removes the
// local record setsebool -P leaves; a Cleanup here cannot report
// whether it managed to.

const (
	conformanceSelinuxDir  = "/srv/" + liveConformancePrefix + "-selinux"
	conformanceSelinuxSpec = conformanceSelinuxDir + "(/.*)?"
	conformanceSelinuxPort = "udp/18998"
)

func conformanceSelinuxCases() []liveCase {
	root := liveRoot()
	run := func(argv ...string) string {
		res, err := root.Run(hexec.Command{Argv: argv, IgnoreExitCode: true})
		if err != nil {
			return err.Error()
		}
		return res.Stdout
	}
	unavailable := func(c *hexec.Context) string {
		if ok, why := selinuxEnabled(); !ok {
			return why
		}
		return ""
	}
	localRule := func() (string, error) {
		for _, r := range parseSemanageFcontexts(run("semanage", "fcontext", "-l", "-C")) {
			if r.Spec == conformanceSelinuxSpec {
				return "rule " + r.Filetype + " " + r.Context, nil
			}
		}
		return "no rule", nil
	}
	dropRule := func() {
		if s, _ := localRule(); s != "no rule" {
			run("semanage", "fcontext", "-d", conformanceSelinuxSpec)
		}
	}
	addRule := func() error {
		if s, _ := localRule(); s == "no rule" {
			run("semanage", "fcontext", "-a", "-t", "public_content_t", conformanceSelinuxSpec)
		}
		if s, _ := localRule(); s == "no rule" {
			return fmt.Errorf("the setup could not add the rule for %s", conformanceSelinuxSpec)
		}
		return nil
	}
	localPort := func() (string, error) {
		typ, ok := selinuxPortIn(parseSemanagePorts(run("semanage", "port", "-l", "-C")), "udp", "18998")
		if !ok {
			return "no port rule", nil
		}
		return "udp/18998 " + typ, nil
	}
	dropPort := func() {
		if s, _ := localPort(); s != "no port rule" {
			run("semanage", "port", "-d", "-p", "udp", "18998")
		}
	}
	enforce := filepath.Join(SELinuxFSPath, "enforce")
	common := func(lc liveCase) liveCase {
		lc.platforms = []string{"linux"}
		lc.needs = append([]string{"semanage", "setsebool", "restorecon"}, lc.needs...)
		lc.unavailable = unavailable
		return lc
	}

	return []liveCase{
		common(liveCase{Conformance: states.Conformance{
			Name: "selinux.mode",
			Args: value.MapOf("name", "enforcing"),
			Probe: func() (string, error) {
				return selinuxGetenforce()
			},
			Setup: func() error { return os.WriteFile(enforce, []byte("0"), 0o644) },
			Cleanup: func() {
				_ = os.WriteFile(enforce, []byte("1"), 0o644)
			},
		}}),
		common(liveCase{Conformance: states.Conformance{
			Name: "selinux.boolean",
			Args: value.MapOf("name", liveSelinuxBoolean, "value", "on"),
			Probe: func() (string, error) {
				return strings.TrimSpace(run("getsebool", liveSelinuxBoolean)), nil
			},
			Setup:   func() error { run("setsebool", liveSelinuxBoolean, "off"); return nil },
			Cleanup: func() { run("setsebool", liveSelinuxBoolean, "off") },
		}}),
		common(liveCase{Conformance: states.Conformance{
			Name:    "selinux.fcontext_policy_present",
			Args:    value.MapOf("name", conformanceSelinuxSpec, "sel_type", "public_content_t"),
			Probe:   localRule,
			Setup:   func() error { dropRule(); return nil },
			Cleanup: dropRule,
		}}),
		common(liveCase{Conformance: states.Conformance{
			Name:    "selinux.fcontext_policy_absent",
			Args:    value.MapOf("name", conformanceSelinuxSpec),
			Probe:   localRule,
			Setup:   addRule,
			Cleanup: dropRule,
		}}),
		common(liveCase{Conformance: states.Conformance{
			Name: "selinux.fcontext_policy_applied",
			Args: value.MapOf("name", conformanceSelinuxDir, "recursive", true),
			Probe: func() (string, error) {
				return run("stat", "-c", "%n %C", conformanceSelinuxDir, filepath.Join(conformanceSelinuxDir, "file")), nil
			},
			// The rule says public_content_t; the files are made, then
			// chcon'd to something else, so there is a relabel to do.
			Setup: func() error {
				if err := addRule(); err != nil {
					return err
				}
				if err := os.MkdirAll(conformanceSelinuxDir, 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(conformanceSelinuxDir, "file"), []byte("x"), 0o644); err != nil {
					return err
				}
				run("chcon", "-R", "-t", "var_t", conformanceSelinuxDir)
				return nil
			},
			Cleanup: func() { _ = os.RemoveAll(conformanceSelinuxDir); dropRule() },
		}}),
		common(liveCase{Conformance: states.Conformance{
			Name:    "selinux.port_policy_present",
			Args:    value.MapOf("name", conformanceSelinuxPort, "sel_type", "http_port_t"),
			Probe:   localPort,
			Setup:   func() error { dropPort(); return nil },
			Cleanup: dropPort,
		}}),
		common(liveCase{Conformance: states.Conformance{
			Name:  "selinux.port_policy_absent",
			Args:  value.MapOf("name", conformanceSelinuxPort),
			Probe: localPort,
			Setup: func() error {
				if s, _ := localPort(); s == "no port rule" {
					run("semanage", "port", "-a", "-t", "http_port_t", "-p", "udp", "18998")
				}
				return nil
			},
			Cleanup: dropPort,
		}}),
	}
}
