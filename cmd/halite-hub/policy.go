package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/policy"
)

var policyUsage = `halite-hub policy — the RBAC of SPEC section 23.5

  policy show                  the roles and bindings, as loaded
  policy test <principal> <target> <function> [args...]
                               evaluate a request and print the rule that
                               matched, or the reason it was denied

policy flags:
  --runner             evaluate the function as a runner rather than a job
  --kwarg <k=v>        an argument to include, repeatable as a comma list
`

// policyPath is where the file lives.
func policyPath(cfg *config.Config) string {
	return cfg.String("policy", filepath.Join(config.DefaultRoot, "policy.yaml"))
}

// loadPolicyFile reads the policy for a command line, where a missing
// file is worth saying out loud rather than treating as an empty one.
func loadPolicyFile(cfg *config.Config) (*policy.Policy, string) {
	path := policyPath(cfg)
	src, err := os.ReadFile(path)
	if err != nil {
		cli.Fatalf("there is no policy at %s, so nothing is authorized; "+
			"`halite-hub keys operator create <name> --admin` writes one", path)
	}
	loaded, warnings, err := policy.Load(src, path)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w.String())
	}
	loaded.ArbitraryCode = arbitraryCodeFunctions()
	return loaded, path
}

func runPolicy(args *cli.Args) int {
	if len(args.Positional) == 0 || args.Positional[0] == "help" {
		fmt.Print(policyUsage)
		return 2
	}
	cfg, err := config.Load(config.Hub, config.LoadOptions{
		Path:         args.Flag("config", ""),
		Root:         args.Flag("root", config.DefaultRoot),
		AllowMissing: true,
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}

	switch args.Positional[0] {
	case "show":
		loaded, path := loadPolicyFile(cfg)
		fmt.Printf("policy %s\n\n", path)
		for role, rules := range loaded.Roles {
			fmt.Printf("role %s\n", role)
			for i, r := range rules {
				if len(r.Runners) > 0 {
					fmt.Printf("  %d. runners %s\n", i, strings.Join(r.Runners, ", "))
				} else {
					fmt.Printf("  %d. %s on %s\n", i, strings.Join(r.Functions, ", "), r.Target)
				}
				for fn, a := range r.Args {
					fmt.Printf("     %s: allow_sls=%v deny_sls=%v allow_mods=%v deny_kwargs=%v\n",
						fn, a.AllowSLS, a.DenySLS, a.AllowMods, a.DenyKwargs)
				}
			}
		}
		fmt.Println()
		for _, b := range loaded.Bindings {
			fmt.Printf("bind %s -> %s\n", b.Principal, strings.Join(b.Roles, ", "))
		}
		names := make([]string, 0, len(loaded.ArbitraryCode))
		for name := range loaded.ArbitraryCode {
			names = append(names, name)
		}
		fmt.Printf("\nnever granted by a wildcard: %s\n", strings.Join(sorted(names), ", "))
		return 0

	case "test":
		if len(args.Positional) < 4 {
			cli.Fatalf("policy test takes a principal, a target, and a function: " +
				"halite-hub policy test 'cert:CN=alice' 'web*.prod' state.apply")
		}
		loaded, _ := loadPolicyFile(cfg)
		kwargs := map[string]any{}
		for _, pair := range strings.Split(args.Flag("kwarg", ""), ",") {
			if k, v, ok := strings.Cut(pair, "="); ok {
				kwargs[k] = v
			}
		}
		req := policy.Request{
			Principal: args.Positional[1],
			Target:    args.Positional[2],
			Fun:       args.Positional[3],
			Arg:       args.Positional[4:],
			Kwarg:     kwargs,
			Runner:    args.Bool("runner", false),
		}
		decision := loaded.Authorize(req)
		fmt.Println(decision.String())
		if decision.Allowed {
			return 0
		}
		// Non-zero so a policy can be tested in CI rather than in
		// production, which is what SPEC 23.5 asks this command for.
		return 1
	}
	fmt.Fprintf(os.Stderr, "halite-hub policy: unknown subcommand %q\n\n%s", args.Positional[0], policyUsage)
	return 2
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// bootstrapAdmin writes the administrator binding SPEC 23.5 names, so
// that a hub with a policy of nothing can be given a first operator
// without hand-editing YAML.
//
// It refuses to touch an existing policy: merging into a file an
// operator has written is how a grant appears that nobody asked for.
func bootstrapAdmin(cfg *config.Config, name string) (string, error) {
	path := policyPath(cfg)
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s already exists; add the binding by hand:\n\n%s",
			path, adminStanza(name))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(adminPolicy(name)), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func adminStanza(name string) string {
	return fmt.Sprintf(`  - principal: '%s'
    roles: ['administrator']
`, pki.Principal(name))
}

// adminPolicy is the bootstrap file. The administrator role names the
// arbitrary-code functions explicitly, because a wildcard does not
// grant them and pretending otherwise here would teach the wrong shape.
func adminPolicy(name string) string {
	var arbitrary []string
	for fn := range arbitraryCodeFunctions() {
		arbitrary = append(arbitrary, "        - '"+fn+"'")
	}
	arbitrary = sorted(arbitrary)
	return fmt.Sprintf(`# halite RBAC. SPEC section 23.5: deny by default, one rule names both a
# target and the functions permitted against it, and a request must
# match one rule entirely.
#
# Written by `+"`halite-hub keys operator create %s --admin`"+`. It grants one
# operator everything, which is a bootstrap and not a destination:
# narrow it, and add roles for the people who do not need all of it.
roles:
  administrator:
    - target: '*'
      functions:
        - '*'
        # A wildcard never grants a function that runs arbitrary code,
        # so each one is named. Removing a line here removes the
        # permission; that is the point of them being separate.
%s
    - runners: ['*']

bindings:
%s`, name, strings.Join(arbitrary, "\n"), adminStanza(name))
}
