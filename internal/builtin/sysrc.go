package builtin

import (
	"fmt"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerSysrc installs the sysrc module, which manages FreeBSD's
// /etc/rc.conf. SPEC section 15.2 lists it in the Core set.
//
// rc.conf is a shell fragment, and editing it with a pattern is how a
// FreeBSD host loses its network configuration. sysrc(8) parses and
// rewrites it correctly, including the quoting, so this module drives that
// rather than the file.
func registerSysrc(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "sysrc", Function: "get",
				Doc: "Return an rc.conf setting, or an empty string when it is not set.",
				Params: []signature.Param{
					req("name", signature.String, "The setting."),
					opt("file", signature.Path, "", "An rc.conf other than the default."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: []string{"freebsd"},
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				v, _, err := sysrcGet(c, states.Str(args, "name", ""), states.Str(args, "file", ""))
				return v, err
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "sysrc", Function: "show",
				Doc: "Return every rc.conf setting.",
				Params: []signature.Param{
					opt("file", signature.Path, "", "An rc.conf other than the default."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: []string{"freebsd"},
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				argv := []string{"sysrc", "-a"}
				if f := states.Str(args, "file", ""); f != "" {
					argv = append([]string{"sysrc", "-f", f, "-a"}, nil...)
				}
				res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
				if err != nil {
					return nil, err
				}
				out := value.NewMap(64)
				for _, line := range strings.Split(res.Stdout, "\n") {
					if k, v, ok := strings.Cut(line, ": "); ok {
						out.Set(strings.TrimSpace(k), strings.TrimSpace(v))
					}
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "sysrc", Function: "set",
				Doc: "Set an rc.conf setting.",
				Params: []signature.Param{
					req("name", signature.String, "The setting."),
					req("value", signature.String, "The value."),
					opt("file", signature.Path, "", "An rc.conf other than the default."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  []string{"freebsd"},
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				name, val := states.Str(args, "name", ""), states.Str(args, "value", "")
				if c.Test {
					return val, nil
				}
				return val, sysrcSet(c, name, val, states.Str(args, "file", ""))
			},
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "sysrc", Function: "present",
				Doc: "Ensure an rc.conf setting has a value.",
				Params: []signature.Param{
					nameParam("The setting. Defaults to the state ID."),
					req("value", signature.String, "The value."),
					opt("file", signature.Path, "", "An rc.conf other than the default."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  []string{"freebsd"},
				Section:    "15.5",
			},
			Fn: sysrcPresent,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "sysrc", Function: "absent",
				Doc: "Ensure an rc.conf setting is not present.",
				Params: []signature.Param{
					nameParam("The setting. Defaults to the state ID."),
					opt("file", signature.Path, "", "An rc.conf other than the default."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  []string{"freebsd"},
				Section:    "15.5",
			},
			Fn: sysrcAbsent,
		},
	)
}

// sysrcArgv builds an argument vector, honouring an alternate file.
func sysrcArgv(file string, rest ...string) []string {
	argv := []string{"sysrc"}
	if file != "" {
		argv = append(argv, "-f", file)
	}
	return append(argv, rest...)
}

// sysrcGet returns the setting's value and whether it is set at all.
func sysrcGet(c *exec.Context, name, file string) (string, bool, error) {
	res, err := c.Run(exec.Command{
		Argv:           sysrcArgv(file, "-n", name),
		IgnoreExitCode: true,
	})
	if err != nil {
		return "", false, err
	}
	if res.Code != 0 {
		return "", false, nil
	}
	return strings.TrimRight(res.Stdout, "\n"), true, nil
}

func sysrcSet(c *exec.Context, name, val, file string) error {
	_, err := c.Run(exec.Command{Argv: sysrcArgv(file, name+"="+val)})
	return err
}

func sysrcPresent(c *exec.Context, args *value.Map) (states.Result, error) {
	name := states.Str(args, "name", "")
	want := states.Str(args, "value", "")
	file := states.Str(args, "file", "")
	if name == "" {
		return states.False("This state needs an rc.conf setting name."), nil
	}
	if c.Which("sysrc") == "" {
		return states.False("sysrc was not found on this node; the sysrc module manages FreeBSD's rc.conf."), nil
	}

	current, set, err := sysrcGet(c, name, file)
	if err != nil {
		return states.False(fmt.Sprintf("%s could not be read: %v", name, err)), nil
	}
	if set && current == want {
		return states.True(fmt.Sprintf("%s is already %s in rc.conf.", name, want)), nil
	}

	var old any
	if set {
		old = current
	}
	changes := value.MapOf(name, states.Change(old, want))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be set to %s in rc.conf.", name, want), changes), nil
	}
	if err := sysrcSet(c, name, want, file); err != nil {
		return states.False(fmt.Sprintf("%s could not be set: %v", name, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s was set to %s in rc.conf.", name, want), changes), nil
}

func sysrcAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
	name := states.Str(args, "name", "")
	file := states.Str(args, "file", "")
	if c.Which("sysrc") == "" {
		return states.False("sysrc was not found on this node; the sysrc module manages FreeBSD's rc.conf."), nil
	}

	current, set, err := sysrcGet(c, name, file)
	if err != nil {
		return states.False(fmt.Sprintf("%s could not be read: %v", name, err)), nil
	}
	if !set {
		return states.True(fmt.Sprintf("%s is already absent from rc.conf.", name)), nil
	}
	changes := value.MapOf(name, states.Change(current, nil))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be removed from rc.conf.", name), changes), nil
	}
	if _, err := c.Run(exec.Command{Argv: sysrcArgv(file, "-x", name)}); err != nil {
		return states.False(fmt.Sprintf("%s could not be removed: %v", name, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s was removed from rc.conf.", name), changes), nil
}
