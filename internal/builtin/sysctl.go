package builtin

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// SysctlConfPath is where a persisted sysctl setting is written. It is a
// variable so a test can point it somewhere harmless.
var SysctlConfPath = defaultSysctlConf()

func defaultSysctlConf() string {
	if runtime.GOOS == "linux" {
		return "/etc/sysctl.d/99-halite.conf"
	}
	return "/etc/sysctl.conf"
}

// registerSysctl installs the sysctl module.
//
// A sysctl has two states that a configuration management system has to
// keep straight: the value the kernel is running with now, and the value
// it will come up with after a reboot. `sysctl.present` manages both,
// which is the only setting that is actually durable.
func registerSysctl(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "sysctl", Function: "get",
				Doc:      "Return a kernel parameter's running value.",
				Params:   []signature.Param{req("name", signature.String, "The parameter.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return sysctlGet(c, states.Str(args, "name", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "sysctl", Function: "show",
				Doc:      "Return every kernel parameter and its running value.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				res, err := c.Run(exec.Command{Argv: []string{"sysctl", "-a"}, IgnoreExitCode: true})
				if err != nil {
					return nil, err
				}
				out := value.NewMap(512)
				for _, line := range strings.Split(res.Stdout, "\n") {
					// Linux writes `key = value`, the BSDs write `key: value`.
					if k, v, ok := strings.Cut(line, ": "); ok {
						out.Set(strings.TrimSpace(k), strings.TrimSpace(v))
						continue
					}
					if k, v, ok := strings.Cut(line, " = "); ok {
						out.Set(strings.TrimSpace(k), strings.TrimSpace(v))
					}
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "sysctl", Function: "assign",
				Doc: "Set a kernel parameter's running value, without persisting it.",
				Params: []signature.Param{
					req("name", signature.String, "The parameter."),
					req("value", signature.String, "The value."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				name, val := states.Str(args, "name", ""), states.Str(args, "value", "")
				if c.Test {
					return val, nil
				}
				if err := sysctlAssign(c, name, val); err != nil {
					return nil, err
				}
				return val, nil
			},
		},
	)

	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "sysctl", Function: "present",
			Doc: "Ensure a kernel parameter has a value now and after a reboot.",
			Params: []signature.Param{
				nameParam("The parameter. Defaults to the state ID."),
				req("value", signature.String, "The value."),
				opt("config", signature.Path, "", "Where to persist it; defaults to the platform's sysctl configuration."),
			},
			Mutates:    true,
			TestMode:   signature.TestReliable,
			Privileges: []string{"root"},
			Section:    "15.5",
		},
		Fn: sysctlPresent,
	})
}

func sysctlGet(c *exec.Context, name string) (string, error) {
	res, err := c.Run(exec.Command{Argv: []string{"sysctl", "-n", name}, IgnoreExitCode: true})
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", fmt.Errorf("sysctl %s: %s", name, firstLine(res.Stderr))
	}
	return strings.TrimSpace(res.Stdout), nil
}

func sysctlAssign(c *exec.Context, name, val string) error {
	// Both spellings exist: BSD sysctl takes `name=value`, Linux's takes
	// `-w name=value`. The BSD form works on Linux too.
	_, err := c.Run(exec.Command{Argv: []string{"sysctl", "-w", name + "=" + val}})
	if err == nil {
		return nil
	}
	_, err = c.Run(exec.Command{Argv: []string{"sysctl", name + "=" + val}})
	return err
}

func sysctlPresent(c *exec.Context, args *value.Map) (states.Result, error) {
	name := states.Str(args, "name", "")
	want := states.Str(args, "value", "")
	confPath := states.Str(args, "config", "")
	if confPath == "" {
		confPath = SysctlConfPath
	}
	if name == "" || want == "" {
		return states.False("This state needs a parameter name and a value."), nil
	}

	running, runErr := sysctlGet(c, name)
	// A parameter the kernel does not have is a state that cannot
	// succeed, and saying so beats writing a configuration line that will
	// never take effect.
	if runErr != nil {
		return states.False(fmt.Sprintf(
			"The kernel has no parameter %s on this node: %v", name, runErr)), nil
	}

	persisted, confText := sysctlConfValue(confPath, name)
	runningDiffers := normaliseSysctl(running) != normaliseSysctl(want)
	persistedDiffers := normaliseSysctl(persisted) != normaliseSysctl(want)

	if !runningDiffers && !persistedDiffers {
		return states.True(fmt.Sprintf(
			"%s is already %s, now and after a reboot.", name, want)), nil
	}

	changes := value.NewMap(2)
	if runningDiffers {
		changes.Set(name, states.Change(running, want))
	}
	if persistedDiffers {
		changes.Set("config", states.Change(persisted, want))
	}

	describe := func() string {
		switch {
		case runningDiffers && persistedDiffers:
			return fmt.Sprintf("%s was set to %s and persisted in %s.", name, want, confPath)
		case runningDiffers:
			return fmt.Sprintf("%s was set to %s; %s already had it.", name, want, confPath)
		default:
			return fmt.Sprintf("%s was already %s; it is now persisted in %s.", name, want, confPath)
		}
	}
	if c.Test {
		return states.WouldChange(strings.Replace(describe(), " was ", " would be ", 1), changes), nil
	}

	if runningDiffers {
		if err := sysctlAssign(c, name, want); err != nil {
			return states.False(fmt.Sprintf("%s could not be set: %v", name, err)), nil
		}
	}
	if persistedDiffers {
		if err := writeSysctlConf(confPath, confText, name, want); err != nil {
			return states.False(fmt.Sprintf("%s could not be persisted in %s: %v", name, confPath, err)), nil
		}
	}
	return states.Changed(describe(), changes), nil
}

// normaliseSysctl collapses the whitespace a kernel uses between the
// fields of a multi-value parameter, so that "1 2 3" and "1\t2\t3" are the
// same value rather than a change reported on every run.
func normaliseSysctl(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// sysctlConfValue reads the persisted value and returns the file's text.
func sysctlConfValue(path, name string) (string, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	text := string(b)
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		k, v, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(k) != name {
			continue
		}
		return strings.TrimSpace(v), text
	}
	return "", text
}

// writeSysctlConf rewrites the setting in place, or appends it.
func writeSysctlConf(path, text, name, want string) error {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}
	replaced := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if k, _, ok := strings.Cut(trimmed, "="); ok && strings.TrimSpace(k) == name {
			lines[i] = name + "=" + want
			replaced = true
			break
		}
	}
	if !replaced {
		lines = append(lines, name+"="+want)
	}
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return err
	}
	return writeAtomic(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
