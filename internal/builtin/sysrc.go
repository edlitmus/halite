package builtin

import (
	"fmt"
	"strings"
	"unicode"

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
				if err := sysrcValid(name, val); err != nil {
					return nil, err
				}
				if c.Test {
					return val, nil
				}
				return val, sysrcSet(c, name, val, states.Str(args, "file", ""))
			},
		},
	)

	// Salt spells this one `managed`, and a FreeBSD tree written against
	// Salt says `sysrc.managed`. `present` is kept beside it because it
	// is the name every other state module in this build uses for the
	// same idea, and because the two are the same function.
	sysrcSetParams := func() []signature.Param {
		return []signature.Param{
			nameParam("The setting. Defaults to the state ID."),
			req("value", signature.String, "The value."),
			opt("file", signature.Path, "", "An rc.conf other than the default."),
		}
	}
	sysrcSetSig := func(function, doc string) signature.Signature {
		return signature.Signature{
			Module: "sysrc", Function: function,
			Doc:        doc,
			Params:     sysrcSetParams(),
			Mutates:    true,
			TestMode:   signature.TestReliable,
			Privileges: []string{"root"},
			Platforms:  []string{"freebsd"},
			Section:    "15.5",
		}
	}

	r.States.Add(
		states.Module{
			Sig: sysrcSetSig("managed", "Ensure an rc.conf setting has a value. Salt's name for it."),
			Fn:  sysrcPresent,
		},
		states.Module{
			Sig: sysrcSetSig("present", "Ensure an rc.conf setting has a value."),
			Fn:  sysrcPresent,
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
		// Only sysrc's own "unknown variable" means unset. Any non-zero
		// exit used to, and on beastie (FreeBSD 15.1) an rc.conf with a
		// syntax error answers every read with exit 1 and "Syntax error:
		// Unterminated quoted string" -- a variable the file does set
		// included -- so sysrc.get said unset and sysrc.absent said
		// already absent with the line still there (issue 321, section
		// 5). A file that does not exist answers "unknown variable" too,
		// which is right: nothing in it is set.
		msg := strings.TrimSpace(res.Stderr)
		if strings.Contains(msg, "unknown variable") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("sysrc could not read %s: %s", name, firstLine(msg))
	}
	return strings.TrimRight(res.Stdout, "\n"), true, nil
}

func sysrcSet(c *exec.Context, name, val, file string) error {
	if err := sysrcValid(name, val); err != nil {
		return err
	}
	_, err := c.Run(exec.Command{Argv: sysrcArgv(file, name+"="+val)})
	return err
}

// sysrcValid refuses a setting that sysrc would write as code.
//
// **rc.conf is a shell script, sourced as root at every boot, and sysrc
// does not escape what it writes into it** (issue 321, M-45). Measured on
// beastie (FreeBSD 15.1) against a scratch file: `sysrc 'bar=x"; echo
// pwned; "'` wrote `bar="x"; echo pwned; ""` and sourcing it printed
// "pwned"; `baz=$(id)` ran id; a newline in a value put a second line in
// the file. So a value holding `"`, `$`, a backquote, a backslash or a
// control character is refused rather than handed over, and so is a name
// that is not a shell variable's, which a leading `-` would make an
// option to sysrc. Values a tree writes into rc.conf -- YES, NO, flags,
// addresses, paths -- need none of these.
func sysrcValid(name, val string) error {
	if !isShellName(name) {
		return fmt.Errorf("%q is not an rc.conf setting name: rc.conf variables are shell names", name)
	}
	if strings.ContainsAny(val, "\"$`\\") || strings.IndexFunc(val, unicode.IsControl) >= 0 {
		return fmt.Errorf("the value for %s holds a quote, `$`, a backquote, a backslash or a control character; "+
			"rc.conf is sourced as root at boot and sysrc does not escape it, so it would run as code", name)
	}
	return nil
}

func sysrcPresent(c *exec.Context, args *value.Map) (states.Result, error) {
	name := states.Str(args, "name", "")
	want := states.Str(args, "value", "")
	file := states.Str(args, "file", "")
	if name == "" {
		return states.False("This state needs an rc.conf setting name."), nil
	}
	if err := sysrcValid(name, want); err != nil {
		return states.False(err.Error()), nil
	}
	if c.Which("sysrc") == "" {
		return states.False("Sysrc was not found on this node; the sysrc module manages FreeBSD's rc.conf."), nil
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
		return states.False("Sysrc was not found on this node; the sysrc module manages FreeBSD's rc.conf."), nil
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
