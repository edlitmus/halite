package builtin

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// linuxOnly is the restriction a module carries when the thing it
// manages is part of the Linux kernel rather than of a distribution.
//
// It reads the same as `debianOnly` and means something different, which
// is why it is a second name rather than a shared one. `debianOnly`
// narrows to Linux and then asks whether dpkg is there, because dpkg is
// a Debian thing and a RHEL node is Linux too. AppArmor is a Linux LSM:
// there is no other kernel to run it on, so the platform restriction is
// the whole of the restriction and there is no second question.
var linuxOnly = []string{"linux"}

// AppArmorProfilesPath is securityfs's list of loaded profiles and the
// mode each is in. A variable so a test can point it at a fixture.
//
// This is what `aa-status` reads, and reading it directly is deliberate:
// `aa-status` is in the `apparmor` package and its output format is
// meant for a person, while this file is two columns and has been since
// the interface existed. More to the point, the tools that *change* a
// mode are in `apparmor-utils`, which Ubuntu does not install by
// default — so a node can perfectly well be running AppArmor with no
// way to run `aa-status` on it, and a module that could not answer
// "what is enforced here" on such a node would be useless exactly where
// it is most needed.
var AppArmorProfilesPath = "/sys/kernel/security/apparmor/profiles"

// AppArmorEnabledPath is the kernel parameter that says whether the LSM
// is on. It reads `Y` or `N` and needs no privilege, which matters:
// AppArmorProfilesPath is root-only, so without this a node would report
// "AppArmor is off" to any caller that is not root.
var AppArmorEnabledPath = "/sys/module/apparmor/parameters/enabled"

// AppArmorDisableDir holds a symlink per profile file that is to stay
// unloaded across a boot.
var AppArmorDisableDir = "/etc/apparmor.d/disable"

// apparmorModes are the modes a loaded profile can be in.
//
// `disable` is not among them, and that is the distinction this module
// is careful about: enforce, complain, kill and unconfined are states of
// a profile that is *loaded*, and disabling one unloads it. A disabled
// profile therefore has no mode at all — it is absent from
// AppArmorProfilesPath — which is why `apparmor.mode` returns an empty
// string rather than "disable" for it.
var apparmorModes = map[string]bool{
	"enforce": true, "complain": true, "kill": true, "unconfined": true,
}

// registerAppArmor installs the apparmor module of SPEC 15.2 and 15.3's
// Debian row, and the `apparmor.mode` state of 15.5.
//
// The thing most often got wrong about AppArmor is that **a profile is
// not named after its file.** `/etc/apparmor.d/usr.sbin.tcpdump`
// contains a profile named `/usr/sbin/tcpdump`, and
// `/etc/apparmor.d/usr.bin.man` contains three — `man`,
// `man_filter` and `man_groff`. So the file is not the profile, one
// file is not one profile, and a module that treated the two as
// interchangeable would report a profile as absent while it was
// enforcing.
//
// Everything here that identifies a profile therefore takes the profile
// name, which is what securityfs lists and what the `aa-*` tools accept.
// The one function that takes a file says so in its parameter, because
// `apparmor_parser` genuinely does operate on files.
func registerAppArmor(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "apparmor", Function: "status",
				Doc:       "Report whether AppArmor is on, how many profiles are in each mode, and whether a mode can be changed on this node.",
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return apparmorStatus(c)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "apparmor", Function: "list_profiles",
				Doc:        "Return every loaded profile and the mode it is in.",
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				profiles, err := apparmorProfiles()
				if err != nil {
					return nil, err
				}
				out := value.NewMap(len(profiles))
				for _, name := range sortedProfileNames(profiles) {
					out.Set(name, profiles[name])
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "apparmor", Function: "mode",
				Doc: "Return one profile's mode, or an empty string if it is not loaded.",
				Params: []signature.Param{
					req("name", signature.String, "The profile, such as /usr/sbin/tcpdump. Not the file it lives in."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				profiles, err := apparmorProfiles()
				if err != nil {
					return nil, err
				}
				return profiles[states.Str(args, "name", "")], nil
			},
		},
	)

	// The three mode changes. One function each rather than one taking a
	// mode, because that is how the tools are spelled and how a tree
	// reads; the state takes the mode, where a variable belongs.
	for _, m := range []struct {
		fn, tool, doc string
	}{
		{"enforce", "aa-enforce", "Put a profile into enforce mode, so violations are denied."},
		{"complain", "aa-complain", "Put a profile into complain mode, so violations are logged and allowed."},
		{"disable", "aa-disable", "Unload a profile and keep it unloaded across a reboot."},
	} {
		fn, tool := m.fn, m.tool
		r.Exec.Add(exec.Module{
			Sig: signature.Signature{
				Module: "apparmor", Function: fn,
				Doc: m.doc,
				Params: []signature.Param{
					req("name", signature.String, "The profile, such as /usr/sbin/tcpdump. Not the file it lives in."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				name := strings.TrimSpace(states.Str(args, "name", ""))
				if name == "" {
					return nil, fmt.Errorf("apparmor.%s needs a profile name", fn)
				}
				if c.Test {
					return true, nil
				}
				return true, apparmorRunTool(c, tool, name)
			},
		})
	}

	r.Exec.Add(exec.Module{
		Sig: signature.Signature{
			Module: "apparmor", Function: "reload",
			Doc: "Reload a profile file, so an edit to it takes effect.",
			Params: []signature.Param{
				req("path", signature.Path, "The profile *file*, such as /etc/apparmor.d/usr.sbin.tcpdump. One file can hold several profiles and is not named after any of them."),
			},
			Mutates: true, TestMode: signature.TestReliable,
			Privileges: []string{"root"},
			Platforms:  linuxOnly,
			Section:    "15.2",
		},
		Fn: func(c *exec.Context, args *value.Map) (any, error) {
			path := strings.TrimSpace(states.Str(args, "path", ""))
			if path == "" {
				return nil, fmt.Errorf("apparmor.reload needs a profile file")
			}
			if c.Test {
				return true, nil
			}
			if c.Which("apparmor_parser") == "" {
				return nil, fmt.Errorf("apparmor_parser is not installed; it is in the apparmor package")
			}
			res, err := c.Run(exec.Command{
				Argv:           []string{"apparmor_parser", "--replace", path},
				IgnoreExitCode: true,
			})
			if err != nil {
				return nil, err
			}
			if res.Code != 0 {
				return nil, fmt.Errorf("%s: %s", path, firstLine(res.Stderr+res.Stdout))
			}
			return true, nil
		},
	})

	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "apparmor", Function: "mode",
			Doc: "Ensure a profile is in a mode.",
			Params: []signature.Param{
				nameParam("The profile, such as /usr/sbin/tcpdump. Defaults to the state ID, and is not the file the profile lives in."),
				req("mode", signature.String, "enforce, complain, or disable."),
			},
			Mutates:    true,
			TestMode:   signature.TestReliable,
			Privileges: []string{"root"},
			Platforms:  linuxOnly,
			Section:    "15.5",
		},
		Fn: apparmorModeState,
	})
}

// apparmorStatus answers without needing root or apparmor-utils, as far
// as it can, and says which half it could not answer.
//
// The enabled flag and the profile list come from different files with
// different permissions, and an unprivileged caller can read the first
// and not the second. Reporting "AppArmor is off" in that case would be
// wrong in the way that matters most — an operator checking whether a
// node is confined, and being told no when it is.
func apparmorStatus(c *exec.Context) (*value.Map, error) {
	enabled, why := apparmorEnabled()
	out := value.NewMap(6)
	out.Set("enabled", enabled)
	if !enabled {
		out.Set("reason", why)
		return out, nil
	}

	profiles, err := apparmorProfiles()
	if err != nil {
		// Not an error: the node is confined and this caller cannot see
		// the detail. Saying so is more use than failing.
		out.Set("profiles_readable", false)
		out.Set("reason", err.Error())
		return out, nil
	}
	out.Set("profiles_readable", true)
	counts := value.NewMap(len(apparmorModes))
	for mode := range apparmorModes {
		counts.Set(mode, 0)
	}
	for _, mode := range profiles {
		n := 0
		if v, ok := counts.Get(mode); ok {
			n, _ = v.(int)
		}
		counts.Set(mode, n+1)
	}
	out.Set("profiles", len(profiles))
	out.Set("modes", counts)
	// Whether a mode can be changed on this node at all, which is a
	// different question from whether AppArmor is running.
	//
	// Asked by running one, not by looking on PATH. That distinction
	// cost nothing to write and turns out to be the whole of the field:
	// on Ubuntu 24.04 the `aa-*` tools are installed, are on PATH, and
	// cannot run — apparmor-utils 4.0.1 parses every profile under
	// /etc/apparmor.d with its own Python parser before doing anything,
	// and it cannot read the profile set Ubuntu itself ships. `tools:
	// true` there was an answer an operator would have acted on.
	// DIVERGENCE 5.37.
	usable, why := apparmorToolsUsable(c)
	out.Set("tools", usable)
	if !usable && why != "" {
		out.Set("tools_reason", why)
	}
	return out, nil
}

// apparmorToolsUsable reports whether a mode can actually be changed
// here, and why not when it cannot.
//
// The probe is an invocation against a profile that does not exist. That
// is deliberate: the tools parse the whole profile tree *before* looking
// for the profile named, so a tree they cannot read fails at the parse
// and a tree they can read fails at the lookup. The two are told apart
// by which error comes back, and nothing on the machine is changed
// either way.
func apparmorToolsUsable(c *exec.Context) (bool, string) {
	if c.Which("aa-enforce") == "" {
		return false, "the aa-* tools are not installed; they are in apparmor-utils, " +
			"which Ubuntu does not install by default"
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"aa-enforce", apparmorProbeProfile},
		IgnoreExitCode: true,
	})
	if err != nil {
		return false, err.Error()
	}
	out := res.Stderr + res.Stdout
	// A tree the tools cannot parse. The wording differs between
	// versions and between the rules they choke on, so the shapes are
	// matched rather than one message.
	for _, broken := range []string{"cannot have a source", "Can't parse", "Traceback", "Include file"} {
		if strings.Contains(out, broken) {
			return false, "the aa-* tools cannot parse this node's profile tree, so no mode " +
				"can be changed on it by any means: " + firstLine(out)
		}
	}
	return true, ""
}

// apparmorProbeProfile is a name no machine has, used to ask the tools
// whether they work without changing anything.
const apparmorProbeProfile = "halite-probe-does-not-exist"

// apparmorEnabled reads the kernel parameter, and returns why not.
func apparmorEnabled() (bool, string) {
	b, err := os.ReadFile(AppArmorEnabledPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, "this kernel has no AppArmor: " + AppArmorEnabledPath + " does not exist"
		}
		return false, err.Error()
	}
	// The parameter is a single character, `Y` or `N`.
	if strings.EqualFold(strings.TrimSpace(string(b)), "Y") {
		return true, ""
	}
	return false, "AppArmor is built into this kernel and is not enabled"
}

// apparmorProfiles reads securityfs.
func apparmorProfiles() (map[string]string, error) {
	b, err := os.ReadFile(AppArmorProfilesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s does not exist; AppArmor is not running on this node",
				AppArmorProfilesPath)
		}
		if os.IsPermission(err) {
			return nil, fmt.Errorf("%s is readable only by root, and this is not root",
				AppArmorProfilesPath)
		}
		return nil, err
	}
	return parseAppArmorProfiles(string(b)), nil
}

// parseAppArmorProfiles reads securityfs's two columns:
//
//	/usr/bin/man (enforce)
//	man_filter (enforce)
//	/usr/sbin/tcpdump (complain)
//
// The mode is taken from the trailing parenthesised group rather than by
// splitting the line on whitespace. A profile name is whatever the
// profile declared — a child profile is written `parent//child`, and a
// name may contain spaces — so a whitespace split truncates such a name
// and stores something else under it. A state naming the real profile is
// then told it is not loaded while it is enforcing, which is the
// direction of wrong that matters here.
//
// The group is taken from the end for the same reason and one step
// further out: a name containing " (" would otherwise take the mode from
// inside itself. No such name is known to exist, so that half is
// defensive rather than demonstrated; the whitespace half is what the
// fixture in apparmor_test.go actually distinguishes.
func parseAppArmorProfiles(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" {
			continue
		}
		open := strings.LastIndex(line, " (")
		if open < 0 || !strings.HasSuffix(line, ")") {
			continue
		}
		name := strings.TrimSpace(line[:open])
		mode := line[open+2 : len(line)-1]
		if name == "" || mode == "" {
			continue
		}
		out[name] = mode
	}
	return out
}

func sortedProfileNames(profiles map[string]string) []string {
	out := make([]string, 0, len(profiles))
	for name := range profiles {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// apparmorRunTool runs one of the aa-* tools against a profile.
//
// The tools are in `apparmor-utils`, which is not part of a default
// Ubuntu install, so the package is named rather than reported as a
// missing binary. An operator told the package name installs it; one
// told "aa-enforce: not found" goes looking.
func apparmorRunTool(c *exec.Context, tool, name string) error {
	if c.Which(tool) == "" {
		return fmt.Errorf("%s is not installed; it is in the apparmor-utils package, "+
			"which a default Ubuntu install does not include", tool)
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{tool, name},
		IgnoreExitCode: true,
	})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s %s: %s", tool, name, firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

// apparmorModeState converges one profile.
func apparmorModeState(c *exec.Context, args *value.Map) (states.Result, error) {
	name := strings.TrimSpace(states.Str(args, "name", ""))
	want := strings.TrimSpace(strings.ToLower(states.Str(args, "mode", "")))
	if name == "" {
		return states.False("This state needs a profile name."), nil
	}
	switch want {
	case "enforce", "complain", "disable":
	case "":
		return states.False(fmt.Sprintf("%s: this state needs a `mode`: enforce, complain or disable.", name)), nil
	default:
		return states.False(fmt.Sprintf(
			"%s: %q is not a mode this state sets; it takes enforce, complain or disable. "+
				"`kill` and `unconfined` are modes a profile can be in and are set in the "+
				"profile itself, not by a tool.", name, want)), nil
	}

	if enabled, why := apparmorEnabled(); !enabled {
		return states.False(fmt.Sprintf("%s cannot be managed: %s", name, why)), nil
	}
	profiles, err := apparmorProfiles()
	if err != nil {
		return states.False(fmt.Sprintf("%s's mode could not be read: %v", name, err)), nil
	}
	current, loaded := profiles[name]

	if want == "disable" {
		// A profile that is not loaded is already doing what disabling
		// it achieves, and `aa-disable` on one fails. The two ways to
		// get here are different and this does not pretend to tell them
		// apart: the profile was disabled, or it was never installed.
		// Both are named, because "not loaded" is a claim about the
		// kernel and the operator's question is usually about the file.
		if !loaded {
			return states.True(fmt.Sprintf(
				"%s is not loaded, which is what disabling it does. Either it is already "+
					"disabled or no profile by that name is installed; this reads the kernel's "+
					"loaded profiles and cannot tell those apart.", name)), nil
		}
		changes := value.MapOf(name, states.Change(current, "disabled"))
		if c.Test {
			return states.WouldChange(fmt.Sprintf(
				"%s would be unloaded and kept unloaded across a reboot.", name), changes), nil
		}
		if err := apparmorRunTool(c, "aa-disable", name); err != nil {
			return states.False(fmt.Sprintf("%s could not be disabled: %v", name, err)), nil
		}
		return states.Changed(fmt.Sprintf(
			"%s was unloaded, and a symlink in %s keeps it unloaded across a reboot.",
			name, AppArmorDisableDir), changes), nil
	}

	// A profile that is not loaded cannot be put into a mode. Saying
	// which of the two spellings the operator may have used is worth a
	// line, because it is the mistake this module was written around.
	if !loaded {
		return states.False(fmt.Sprintf(
			"%s is not a loaded profile, so it cannot be put into %s mode. A profile is "+
				"named by what it attaches to rather than by its file: "+
				"/etc/apparmor.d/usr.sbin.tcpdump holds a profile named /usr/sbin/tcpdump. "+
				"`apparmor.list_profiles` shows what this node has loaded.", name, want)), nil
	}
	if current == want {
		return states.True(fmt.Sprintf("%s is already in %s mode.", name, want)), nil
	}

	changes := value.MapOf(name, states.Change(current, want))
	if c.Test {
		return states.WouldChange(fmt.Sprintf(
			"%s would move from %s to %s mode.", name, current, want), changes), nil
	}
	if err := apparmorRunTool(c, "aa-"+want, name); err != nil {
		return states.False(fmt.Sprintf("%s could not be put into %s mode: %v", name, want, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s moved from %s to %s mode.", name, current, want), changes), nil
}
