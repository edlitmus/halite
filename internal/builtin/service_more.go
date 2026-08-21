package builtin

import (
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The rest of the service module's execution functions, SPEC section 15.2.
//
// Two optional interfaces sit beside serviceProvider rather than in it.
// Not every init system can list its services, and only systemd can mask
// one; folding either into the main interface would make the other
// providers implement a stub that lies. A provider that cannot answer says
// so, and the error names the init system.

// serviceLister is implemented by an init system that can enumerate its
// services.
type serviceLister interface {
	List(c *exec.Context) ([]string, error)
}

// serviceMasker is implemented by systemd, and only by systemd. Masking
// points a unit at /dev/null so that nothing can start it, deliberately
// or by dependency, and no other init system here has the concept.
type serviceMasker interface {
	Mask(c *exec.Context, name string) error
	Unmask(c *exec.Context, name string) error
	Masked(c *exec.Context, name string) (bool, error)
}

func registerServiceMore(r *Registries) {
	listServices := func(c *exec.Context) ([]string, error) {
		p, err := pickServiceProvider(c)
		if err != nil {
			return nil, err
		}
		l, ok := p.(serviceLister)
		if !ok {
			return nil, fmt.Errorf("the %s provider cannot list services", p.Name())
		}
		return l.List(c)
	}

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "service", Function: "get_all",
				Doc:      "Return every service the init system knows about, sorted.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				names, err := listServices(c)
				if err != nil {
					return nil, err
				}
				out := make([]any, len(names))
				for i, n := range names {
					out[i] = n
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "service", Function: "available",
				Doc: "Report whether the init system knows about a service.",
				Params: []signature.Param{
					req("name", signature.String, "The service."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				names, err := listServices(c)
				if err != nil {
					return nil, err
				}
				return serviceKnown(names, states.Str(args, "name", "")), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "service", Function: "missing",
				Doc: "Report whether the init system does not know about a service. The inverse of available, which trees write both ways.",
				Params: []signature.Param{
					req("name", signature.String, "The service."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				names, err := listServices(c)
				if err != nil {
					return nil, err
				}
				return !serviceKnown(names, states.Str(args, "name", "")), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "service", Function: "disabled",
				Doc: "Report whether a service is disabled at boot. The inverse of enabled.",
				Params: []signature.Param{
					req("name", signature.String, "The service."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickServiceProvider(c)
				if err != nil {
					return nil, err
				}
				enabled, err := p.Enabled(c, states.Str(args, "name", ""))
				if err != nil {
					return nil, err
				}
				return !enabled, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "service", Function: "force_reload",
				Doc: "Reload a service, restarting it if it has no reload.",
				Params: []signature.Param{
					req("name", signature.String, "The service."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickServiceProvider(c)
				if err != nil {
					return nil, err
				}
				name := states.Str(args, "name", "")
				if err := p.Reload(c, name); err == nil {
					return true, nil
				}
				// A service with no reload is the common case, and a tree
				// asking for force_reload wants the configuration picked
				// up either way. That is what the word "force" is doing.
				if err := p.Restart(c, name); err != nil {
					return nil, err
				}
				return true, nil
			},
		},
		maskModule(r, "mask", "Mask a service, so nothing can start it.",
			func(m serviceMasker, c *exec.Context, name string) error { return m.Mask(c, name) }),
		maskModule(r, "unmask", "Unmask a service.",
			func(m serviceMasker, c *exec.Context, name string) error { return m.Unmask(c, name) }),
		exec.Module{
			Sig: signature.Signature{
				Module: "service", Function: "masked",
				Doc: "Report whether a service is masked.",
				Params: []signature.Param{
					req("name", signature.String, "The service."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				m, err := pickMasker(c)
				if err != nil {
					return nil, err
				}
				return m.Masked(c, states.Str(args, "name", ""))
			},
		},
	)
}

func maskModule(r *Registries, name, doc string,
	run func(serviceMasker, *exec.Context, string) error) exec.Module {
	return exec.Module{
		Sig: signature.Signature{
			Module: "service", Function: name,
			Doc: doc,
			Params: []signature.Param{
				req("name", signature.String, "The service."),
			},
			Mutates:  true,
			TestMode: signature.TestReliable,
			Section:  "15.2",
		},
		Fn: func(c *exec.Context, args *value.Map) (any, error) {
			m, err := pickMasker(c)
			if err != nil {
				return nil, err
			}
			if err := run(m, c, states.Str(args, "name", "")); err != nil {
				return nil, err
			}
			return true, nil
		},
	}
}

func pickMasker(c *exec.Context) (serviceMasker, error) {
	p, err := pickServiceProvider(c)
	if err != nil {
		return nil, err
	}
	m, ok := p.(serviceMasker)
	if !ok {
		return nil, fmt.Errorf(
			"masking is a systemd concept and this node runs %s; "+
				"to stop a service starting here, disable it", p.Name())
	}
	return m, nil
}

// serviceKnown compares without the suffix systemd adds, so a tree
// naming `nginx` matches `nginx.service` and the other way round.
func serviceKnown(names []string, want string) bool {
	want = strings.TrimSuffix(strings.TrimSpace(want), ".service")
	for _, n := range names {
		if strings.TrimSuffix(n, ".service") == want {
			return true
		}
	}
	return false
}

// ---- provider implementations ----

func (freebsdRCProvider) List(c *exec.Context) ([]string, error) {
	// `service -l` lists the scripts in the rc directories, one per line.
	res, err := c.Run(exec.Command{Argv: []string{"service", "-l"}})
	if err != nil {
		return nil, err
	}
	return sortedLines(res.Stdout), nil
}

func (systemdProvider) List(c *exec.Context) ([]string, error) {
	res, err := c.Run(exec.Command{
		Argv: []string{"systemctl", "list-unit-files", "--type=service", "--no-legend", "--no-pager", "--plain"},
	})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ln := range strings.Split(res.Stdout, "\n") {
		if f := strings.Fields(ln); len(f) > 0 {
			out = append(out, f[0])
		}
	}
	sort.Strings(out)
	return out, nil
}

func (systemdProvider) Mask(c *exec.Context, name string) error {
	return systemctl(c, "mask", name)
}

func (systemdProvider) Unmask(c *exec.Context, name string) error {
	return systemctl(c, "unmask", name)
}

func (systemdProvider) Masked(c *exec.Context, name string) (bool, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"systemctl", "is-enabled", name},
		IgnoreExitCode: true,
	})
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(res.Stdout) == "masked", nil
}

func (sysvProvider) List(c *exec.Context) ([]string, error) {
	res, err := c.Run(exec.Command{Argv: []string{"ls", "-1", "/etc/init.d"}, IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	return sortedLines(res.Stdout), nil
}

func (launchdProvider) List(c *exec.Context) ([]string, error) {
	res, err := c.Run(exec.Command{Argv: []string{"launchctl", "list"}})
	if err != nil {
		return nil, err
	}
	var out []string
	for i, ln := range strings.Split(res.Stdout, "\n") {
		// The first line is a header: PID, Status, Label.
		if i == 0 {
			continue
		}
		f := strings.Fields(ln)
		if len(f) >= 3 {
			out = append(out, f[2])
		}
	}
	sort.Strings(out)
	return out, nil
}

func sortedLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
	}
	sort.Strings(out)
	return out
}
