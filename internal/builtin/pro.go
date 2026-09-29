package builtin

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerPro installs the `pro` module of SPEC 15.3's Debian/Ubuntu row:
// "Ubuntu Pro attach, FIPS enablement, USG". It drives Canonical's own
// `pro` client (`ubuntu-advantage-tools`) rather than a certificate or a
// grain, because attachment, entitlement and per-service state all live
// behind that one binary and nowhere else — there is no file to read
// that says what a contract entitles a host to.
//
// # What this settles, and what it does not
//
// plan.md §7 item 11 answers the design question this module existed to
// close: a Pro-enabled FIPS node and a `GOFIPS140` build are two separate
// claims, and `doctor`'s FIPS check (internal/doctor/fips.go) already
// says so by name. This module is where an operator — or a state, through
// `module.run` — asks the first claim directly: `pro.status`'s `services`
// entry for `fips`/`fips-updates` is the node's own attachment answering
// for itself, rather than a grain inferring it from a kernel file that
// two different channels can both satisfy.
//
// # Reads are demonstrated here; the mutating half is not
//
// `pro status`, `pro api <endpoint>` and `pro --version` need no
// privilege and change nothing, and this build's own development host is
// itself Ubuntu Pro-attached — `pro.status`, `pro.is_attached` and
// `pro.version` were run against it for real, and `TestLiveProReads` runs
// them again in the ordinary suite. `pro.attach`, `pro.detach`,
// `pro.enable` and `pro.disable` are written to the client's own
// documented `--format json` argument grammar and its common result
// envelope (`result`/`errors`/`warnings`, the same shape every `pro
// status`/`pro api` response demonstrated here carries), and each argv
// builder is a pure function pinned by a test — but none has been run.
// Attaching, detaching or toggling an entitlement changes what this
// real host is allowed to install and patch, which is not a thing to
// discover was wrong by trying it on the host this suite runs on. So the
// module arrives the way every new one does: `Assumed` for the half
// nobody has watched change a machine, which holds the release gate red
// on `pro` alone until a host volunteers to be attached, detached, or
// have a service flipped, on purpose, by someone who can undo it.
//
// # Why Ubuntu rather than Linux
//
// `pro` ships only on Ubuntu; Debian does not package
// `ubuntu-advantage-tools` at all. The signature narrows to `debianOnly`
// (a `GOOS` check, same as `dpkg`'s) because that is the axis a
// signature has, and every function then asks whether the tool is
// actually on the node — the refusal names `pro`, not the platform.
func registerPro(r *Registries) {
	services := req("services", signature.List, "The Ubuntu Pro service name(s), such as `esm-infra`, `fips` or `usg`.")

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "pro", Function: "version",
				Doc:       "Return the installed `pro` client's version.",
				TestMode:  signature.TestNotApplicable,
				Platforms: debianOnly,
				Section:   "15.3",
			},
			Fn: proVersionFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pro", Function: "status",
				Doc: "Return this node's Ubuntu Pro status: whether it is attached, its contract, " +
					"and every service's entitlement and state.",
				TestMode:  signature.TestNotApplicable,
				Platforms: debianOnly,
				Section:   "15.3",
			},
			Fn: proStatusFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pro", Function: "is_attached",
				Doc:       "Report whether this node is attached to an Ubuntu Pro subscription.",
				TestMode:  signature.TestNotApplicable,
				Platforms: debianOnly,
				Section:   "15.3",
			},
			Fn: proIsAttachedFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pro", Function: "attach",
				Doc: "Attach this node to an Ubuntu Pro subscription with a contract token from " +
					"https://ubuntu.com/pro/dashboard.",
				Params: []signature.Param{
					req("token", signature.String, "The contract token."),
					opt("no_auto_enable", signature.Bool, false,
						"Do not enable the subscription's recommended services automatically."),
				},
				Mutates:    true,
				TestMode:   signature.TestUnreliable,
				Privileges: []string{"root"},
				Platforms:  debianOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return proRun(c, proAttachArgv(args))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pro", Function: "detach",
				Doc:        "Detach this node from its Ubuntu Pro subscription.",
				Mutates:    true,
				TestMode:   signature.TestUnreliable,
				Privileges: []string{"root"},
				Platforms:  debianOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return proRun(c, proDetachArgv())
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pro", Function: "enable",
				Doc: "Enable one or more Ubuntu Pro services on this node, such as `fips`, " +
					"`esm-infra` or `usg`.",
				Params: []signature.Param{
					services,
					opt("variant", signature.String, "", "The variant to use when enabling the service."),
					opt("access_only", signature.Bool, false,
						"Do not auto-install packages. Valid for cc-eal, cis and realtime-kernel."),
					opt("beta", signature.Bool, false, "Allow a beta service to be enabled."),
				},
				Mutates:    true,
				TestMode:   signature.TestUnreliable,
				Privileges: []string{"root"},
				Platforms:  debianOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return proRun(c, proEnableArgv(args))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pro", Function: "disable",
				Doc: "Disable one or more Ubuntu Pro services on this node.",
				Params: []signature.Param{
					services,
					opt("purge", signature.Bool, false,
						"Also remove or downgrade the packages the service installed. Experimental."),
				},
				Mutates:    true,
				TestMode:   signature.TestUnreliable,
				Privileges: []string{"root"},
				Platforms:  debianOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return proRun(c, proDisableArgv(args))
			},
		},
	)
}

// proToolPresent refuses by the tool's own name, the way `dpkg`'s
// functions do, rather than by the platform: a Debian node genuinely has
// no `pro` to be missing, and that is a different fact from "this is not
// Ubuntu".
func proToolPresent(c *exec.Context) error {
	if c.Which("pro") == "" {
		return errors.New(
			"this node has no `pro`; ubuntu-advantage-tools ships on Ubuntu and Debian does not package it")
	}
	return nil
}

func proVersionFn(c *exec.Context, args *value.Map) (any, error) {
	if err := proToolPresent(c); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{Argv: []string{"pro", "--version"}})
	if err != nil {
		return nil, fmt.Errorf("`pro --version` could not be run on this node: %w", err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// proStatusFn returns `pro status --format json` as-is, lifted into the
// value model rather than narrowed to a handful of named fields: the
// document nests contract, account and per-service detail SPEC 15.3
// names no fixed shape for, and a caller after one field reaches it the
// same way `pillar.get`'s callers reach into pillar, with a colon path.
func proStatusFn(c *exec.Context, args *value.Map) (any, error) {
	doc, _, err := proStatus(c)
	if err != nil {
		return nil, err
	}
	return value.FromJSON(doc), nil
}

// proStatus runs `pro status --format json` and returns both the decoded
// document and its raw bytes, so a caller that only wants one field (the
// enable/disable diff below) does not pay for the whole value-model
// conversion.
func proStatus(c *exec.Context) (map[string]any, string, error) {
	if err := proToolPresent(c); err != nil {
		return nil, "", err
	}
	res, err := c.Run(exec.Command{Argv: []string{"pro", "status", "--format", "json"}})
	if err != nil {
		return nil, "", fmt.Errorf("`pro status` could not be run on this node: %w", err)
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return nil, "", fmt.Errorf("`pro status --format json` produced no output; expected JSON")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return nil, "", fmt.Errorf("`pro status --format json`'s output did not parse: %w", err)
	}
	return doc, out, nil
}

// proIsAttachedFn asks the client's own API rather than re-deriving the
// answer from `pro status`'s `attached` field: `u.pro.status.is_attached.v1`
// is the endpoint the client documents for exactly this question, and it
// is one field to trust rather than a whole status document to parse for
// one boolean.
func proIsAttachedFn(c *exec.Context, args *value.Map) (any, error) {
	if err := proToolPresent(c); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{Argv: []string{"pro", "api", "u.pro.status.is_attached.v1"}})
	if err != nil {
		return nil, fmt.Errorf("`pro api u.pro.status.is_attached.v1` could not be run on this node: %w", err)
	}
	attached, err := proParseIsAttached(res.Stdout)
	if err != nil {
		return nil, err
	}
	return attached, nil
}

// proAPIEnvelope is the common wrapper every `pro api` and `--format
// json` response carries, captured from a real `pro api
// u.pro.status.is_attached.v1` and a real `pro status --format json` on
// this build's own Ubuntu Pro-attached development host, both against
// client version 37.2ubuntu~24.04.1.
type proAPIEnvelope struct {
	Result string `json:"result"`
	Errors []any  `json:"errors"`
}

func proParseIsAttached(out string) (bool, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return false, fmt.Errorf("`pro api u.pro.status.is_attached.v1` produced no output; expected JSON")
	}
	var doc struct {
		proAPIEnvelope
		Data struct {
			Attributes struct {
				IsAttached bool `json:"is_attached"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return false, fmt.Errorf("`pro api u.pro.status.is_attached.v1`'s output did not parse: %w", err)
	}
	if doc.Result != "success" {
		return false, fmt.Errorf("`pro api u.pro.status.is_attached.v1` did not succeed: %v", doc.Errors)
	}
	return doc.Data.Attributes.IsAttached, nil
}

// ---- the mutating half: argument vectors pinned by a test, and never run ----

// proAttachArgv builds `pro attach`'s argument vector. `--assume-yes` is
// not one of its flags — `pro attach --help` does not list it, unlike
// every other mutating subcommand here, because attach's own prompt is
// the browser flow a bare token skips.
func proAttachArgv(args *value.Map) []string {
	argv := []string{"pro", "attach", "--format", "json"}
	if states.Bool(args, "no_auto_enable", false) {
		argv = append(argv, "--no-auto-enable")
	}
	if token := states.Str(args, "token", ""); token != "" {
		argv = append(argv, token)
	}
	return argv
}

func proDetachArgv() []string {
	return []string{"pro", "detach", "--format", "json", "--assume-yes"}
}

func proEnableArgv(args *value.Map) []string {
	argv := []string{"pro", "enable", "--format", "json", "--assume-yes"}
	if states.Bool(args, "access_only", false) {
		argv = append(argv, "--access-only")
	}
	if states.Bool(args, "beta", false) {
		argv = append(argv, "--beta")
	}
	if variant := states.Str(args, "variant", ""); variant != "" {
		argv = append(argv, "--variant", variant)
	}
	return append(argv, states.Strings(args, "services")...)
}

func proDisableArgv(args *value.Map) []string {
	argv := []string{"pro", "disable", "--format", "json", "--assume-yes"}
	if states.Bool(args, "purge", false) {
		argv = append(argv, "--purge")
	}
	return append(argv, states.Strings(args, "services")...)
}

// proRun runs one of the mutating subcommands and reads its `--format
// json` envelope. Never exercised against a real client — see the
// package comment — so a caller gets exactly what the client's JSON
// said, with no attempt to translate `errors` into anything friendlier
// than the client's own words.
func proRun(c *exec.Context, argv []string) (any, error) {
	if err := proToolPresent(c); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("`%s` could not be run on this node: %w",
			exec.Command{Argv: argv}.String(), err)
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return nil, fmt.Errorf("`%s` exited %d with no output: %s",
			exec.Command{Argv: argv}.String(), res.Code, strings.TrimSpace(firstLine(res.Stderr)))
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return nil, fmt.Errorf("`%s`'s output did not parse: %w", exec.Command{Argv: argv}.String(), err)
	}
	if result, _ := doc["result"].(string); result != "success" {
		return nil, fmt.Errorf("`%s` did not succeed: %v", exec.Command{Argv: argv}.String(), doc["errors"])
	}
	return value.FromJSON(doc), nil
}
