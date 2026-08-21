package builtin

import (
	"fmt"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerPkgVersion installs the version comparison functions of SPEC
// section 15.2.
//
// These answer the question a `pkg.latest` state asks a few hundred times
// in a run, which is why the Debian and RPM algorithms are implemented
// rather than shelled out to: one process per comparison is the
// difference between a highstate that takes a second and one that takes a
// minute. FreeBSD is the exception, and it is deliberate — its ordering
// lives in libpkg rather than in a specification, so the `pkg` binary is
// asked rather than guessed at.
func registerPkgVersion(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "version_cmp",
				Doc: "Compare two package versions, returning -1, 0, or 1.",
				Params: []signature.Param{
					req("pkg1", signature.String, "The first version."),
					req("pkg2", signature.String, "The second version."),
					choice("scheme", "auto", "Which ordering to use. `auto` follows the node's os_family.",
						"auto", "debian", "rpm", "freebsd"),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return versionCompare(c, args, states.Str(args, "pkg1", ""), states.Str(args, "pkg2", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pkg", Function: "upgrade_available",
				Doc: "Report whether a newer version of a package is available than the one installed.",
				Params: []signature.Param{
					req("name", signature.String, "The package."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				name := states.Str(args, "name", "")
				installed, err := c.Call("pkg.version", value.MapOf("name", name))
				if err != nil {
					return nil, err
				}
				latest, err := c.Call("pkg.latest_version", value.MapOf("name", name))
				if err != nil {
					return nil, err
				}
				have, want := value.KeyString(installed), value.KeyString(latest)
				if have == "" || want == "" {
					return false, nil
				}
				cmp, err := versionCompare(c, args, have, want)
				if err != nil {
					return nil, err
				}
				return cmp.(int64) < 0, nil
			},
		},
	)
}

// versionCompare picks the ordering and applies it.
func versionCompare(c *exec.Context, args *value.Map, a, b string) (any, error) {
	family := ""
	if c.Grains != nil {
		if v, ok := c.Grains.Get("os_family"); ok {
			family = value.KeyString(v)
		}
	}
	scheme, err := versionScheme(states.Str(args, "scheme", "auto"), family)
	if err != nil {
		return nil, err
	}
	switch scheme {
	case "debian":
		return int64(CompareDebian(a, b)), nil
	case "rpm":
		return int64(CompareRPM(a, b)), nil
	case "freebsd":
		return compareFreeBSD(c, a, b)
	}
	return nil, fmt.Errorf("unknown version comparison scheme %q", scheme)
}

// compareFreeBSD asks pkg(8).
//
// FreeBSD's ordering is libpkg's, and libpkg is the specification: there
// is no published algorithm to transcribe the way there is for dpkg and
// rpm. Asking the tool is the honest answer, and it is what makes this
// one verifiable on a FreeBSD node rather than merely plausible.
func compareFreeBSD(c *exec.Context, a, b string) (any, error) {
	if c.Which("pkg") == "" {
		return nil, fmt.Errorf("pkg was not found on this node; FreeBSD version comparison is libpkg's and needs the binary")
	}
	res, err := c.Run(exec.Command{Argv: []string{"pkg", "version", "-t", a, b}, IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	switch strings.TrimSpace(res.Stdout) {
	case "<":
		return int64(-1), nil
	case "=":
		return int64(0), nil
	case ">":
		return int64(1), nil
	}
	return nil, fmt.Errorf("pkg version -t answered %q, which is not one of <, =, or >",
		strings.TrimSpace(res.Stdout))
}
