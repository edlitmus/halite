package builtin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerIPTables installs the `iptables` module of SPEC 15.3's Common
// Linux row and the `iptables` states of SPEC 15.5.
//
// # Why this is not a `firewall` provider
//
// `firewall` is a virtual module and on Linux its provider is `ufw`,
// which is itself a front end for iptables or nftables. `iptables` is
// the layer under that: the escape hatch a tree reaches for when it
// needs a raw rule ufw has no spelling for — a `nat` table entry, a
// `mark`, a jump to a hand-built chain. Wiring it as a second `firewall`
// provider would make `firewall.allow` ambiguous on a node that has
// both, and would put an abstraction over a tool whose whole reason to
// be here is that the abstraction was not enough. The relationship is
// `dpkg` to `pkg`, not `pf` to `firewall`.
//
// # Idempotence is asked of iptables, not computed from a listing
//
// `iptables -C` checks whether a rule already exists, using the exact
// matching iptables itself uses — so `append` and `delete` put the
// question to the tool rather than reproducing its rule matching against
// the text of `iptables-save`. That text is *also* read, by `get_rules`,
// but only to report; nothing decides whether to write from it. This is
// the trap DIVERGENCE 5.31 found in `pf`, which reprints rules from its
// own parsed form so no rule ever matched the text it was given.
//
// # What `flush` will not do without being told twice
//
// Flushing a built-in chain whose policy is `DROP` or `REJECT` drops the
// rule that was letting the operator in, and the node goes off the
// network mid-run. `flush` refuses that, and refuses a whole-table flush,
// unless the call passes `force`. A user-defined chain has no policy and
// nothing depends on it being reachable, so flushing one is allowed.
//
// # IPv4 and IPv6 are different tools
//
// `family: ipv6` drives `ip6tables`; everything else is the same. A rule
// is not shared between them — SPEC's world has both, and a state that
// wrote one when it meant the other would be a firewall with a hole in
// the half nobody checked.
func registerIPTables(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "version",
				Doc:       "Return the iptables version and which backend (nf_tables or legacy) it drives.",
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return iptablesVersion(c, iptablesFamily(args))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "get_rules",
				Doc: "Return every table, chain, chain policy and rule, read from iptables-save.",
				Params: []signature.Param{
					iptablesFamilyParam(),
					opt("table", signature.String, "", "Limit the report to one table, such as nat. Empty reports them all."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return iptablesGetRules(c, iptablesFamily(args), strings.TrimSpace(states.Str(args, "table", "")))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "check",
				Doc:        "Report whether a rule is already present in a chain.",
				Params:     iptablesRuleFnParams(),
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				spec, err := iptablesSpecFromArgs(args)
				if err != nil {
					return nil, err
				}
				present, _, err := iptablesRulePresent(c, iptablesFamily(args), iptablesTable(args),
					states.Str(args, "chain", ""), spec)
				if err != nil {
					return nil, err
				}
				return present, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "append",
				Doc:        "Add a rule to the end of a chain, if it is not already there.",
				Params:     iptablesRuleFnParams(),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return iptablesAddRule(c, args, "append")
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "insert",
				Doc: "Add a rule at a position in a chain, if it is not already there.",
				Params: append(iptablesRuleFnParams(),
					opt("position", signature.Int, int64(1), "The 1-based position to insert at.")),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return iptablesAddRule(c, args, "insert")
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "delete",
				Doc:        "Remove a rule from a chain, if it is there.",
				Params:     iptablesRuleFnParams(),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: iptablesDeleteRule,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "set_policy",
				Doc: "Set the default policy of a built-in chain.",
				Params: []signature.Param{
					req("chain", signature.String, "A built-in chain: INPUT, OUTPUT, FORWARD, PREROUTING or POSTROUTING."),
					choice("policy", nil, "The policy.", "ACCEPT", "DROP"),
					iptablesTableParam(),
					iptablesFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: iptablesSetPolicyFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "get_policy",
				Doc: "Return a built-in chain's default policy.",
				Params: []signature.Param{
					req("chain", signature.String, "A built-in chain."),
					iptablesTableParam(),
					iptablesFamilyParam(),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				pol, err := iptablesChainPolicy(c, iptablesFamily(args), iptablesTable(args), states.Str(args, "chain", ""))
				if err != nil {
					return nil, err
				}
				if pol == "" {
					return nil, fmt.Errorf("%q is not a built-in chain, so it has no policy", states.Str(args, "chain", ""))
				}
				return pol, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "new_chain",
				Doc: "Create a user-defined chain, if it is not already there.",
				Params: []signature.Param{
					req("chain", signature.String, "The chain's name."),
					iptablesTableParam(),
					iptablesFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return iptablesChainChange(c, args, true)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "delete_chain",
				Doc: "Remove an empty, unreferenced user-defined chain, if it is there.",
				Params: []signature.Param{
					req("chain", signature.String, "The chain's name."),
					iptablesTableParam(),
					iptablesFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return iptablesChainChange(c, args, false)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "flush",
				Doc: "Remove every rule from a chain, or from a whole table. Refuses to strip a built-in chain that is holding traffic out unless force is set.",
				Params: []signature.Param{
					opt("chain", signature.String, "", "The chain to flush. Empty flushes every chain in the table."),
					opt("force", signature.Bool, false, "Flush a built-in chain whose policy is not ACCEPT, or a whole table."),
					iptablesTableParam(),
					iptablesFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: iptablesFlushFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "save",
				Doc: "Write the running ruleset to the file the boot-time restore reads.",
				Params: []signature.Param{
					opt("path", signature.Path, "", "Where to write. Empty picks the distribution's own path."),
					iptablesFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: iptablesSaveFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "build_rule",
				Doc:       "Assemble a rule specification from its parts, the way a tree that does not want to write iptables syntax by hand can.",
				Params:    iptablesBuildRuleParams(),
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return strings.Join(iptablesBuildRule(args), " "), nil
			},
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "chain_present",
				Doc: "Ensure a user-defined chain exists.",
				Params: []signature.Param{
					nameParam("The chain's name. Defaults to the state ID."),
					iptablesTableParam(),
					iptablesFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return iptablesChainState(c, args, true)
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "chain_absent",
				Doc: "Ensure a user-defined chain does not exist. It is flushed first.",
				Params: []signature.Param{
					nameParam("The chain's name. Defaults to the state ID."),
					iptablesTableParam(),
					iptablesFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return iptablesChainState(c, args, false)
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "append",
				Doc:        "Ensure a rule is present at the end of a chain.",
				Params:     iptablesRuleStateParams(),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return iptablesRuleState(c, args, "append")
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "insert",
				Doc: "Ensure a rule is present at a position in a chain.",
				Params: append(iptablesRuleStateParams(),
					opt("position", signature.Int, int64(1), "The 1-based position.")),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return iptablesRuleState(c, args, "insert")
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "delete",
				Doc:        "Ensure a rule is not present in a chain.",
				Params:     iptablesRuleStateParams(),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return iptablesRuleState(c, args, "delete")
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "set_policy",
				Doc: "Ensure a built-in chain's default policy is as declared.",
				Params: []signature.Param{
					nameParam("The chain. Defaults to the state ID."),
					choice("policy", nil, "The policy.", "ACCEPT", "DROP"),
					iptablesTableParam(),
					iptablesFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: iptablesSetPolicyState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "iptables", Function: "flush",
				Doc: "Ensure a chain has no rules. Refuses a built-in chain that is holding traffic out unless force is set.",
				Params: []signature.Param{
					nameParam("The chain. Defaults to the state ID."),
					opt("force", signature.Bool, false, "Flush a built-in chain whose policy is not ACCEPT."),
					iptablesTableParam(),
					iptablesFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: iptablesFlushState,
		},
	)
}

// ---- parameters ----

func iptablesFamilyParam() signature.Param {
	return choice("family", "ipv4", "Which stack: ipv4 drives iptables, ipv6 drives ip6tables.", "ipv4", "ipv6")
}

func iptablesTableParam() signature.Param {
	return choice("table", "filter", "The table.", "filter", "nat", "mangle", "raw", "security")
}

func iptablesRuleParam() signature.Param {
	return opt("rule", signature.Any, nil,
		"The rule specification, as a string (`-p tcp --dport 22 -j ACCEPT`) or a list of tokens. "+
			"Its own build_rule parts may be given instead.")
}

// iptablesRuleFnParams is the parameter set the rule execution functions
// share: a chain, the rule (literal or built from parts), and the
// table/family.
func iptablesRuleFnParams() []signature.Param {
	out := []signature.Param{
		req("chain", signature.String, "The chain, such as INPUT."),
		iptablesRuleParam(),
	}
	out = append(out, iptablesBuildRuleParams()...)
	out = append(out, iptablesTableParam(), iptablesFamilyParam())
	return out
}

// iptablesRuleStateParams is the parameter set the rule states share:
// a chain, the rule (literal or built from parts), and the table/family.
func iptablesRuleStateParams() []signature.Param {
	out := []signature.Param{
		nameParam("A label for the rule, or the rule itself. Defaults to the state ID."),
		req("chain", signature.String, "The chain, such as INPUT."),
		iptablesRuleParam(),
	}
	out = append(out, iptablesBuildRuleParams()...)
	out = append(out, iptablesTableParam(), iptablesFamilyParam())
	return out
}

func iptablesBuildRuleParams() []signature.Param {
	return []signature.Param{
		opt("protocol", signature.String, "", "tcp, udp, icmp, or a number."),
		opt("dport", signature.String, "", "Destination port or range."),
		opt("sport", signature.String, "", "Source port or range."),
		opt("source", signature.String, "", "Source address or network."),
		opt("destination", signature.String, "", "Destination address or network."),
		opt("in_interface", signature.String, "", "Incoming interface."),
		opt("out_interface", signature.String, "", "Outgoing interface."),
		opt("ctstate", signature.String, "", "Conntrack state, such as RELATED,ESTABLISHED."),
		opt("match", signature.List, nil, "Extra `-m` modules to load."),
		opt("comment", signature.String, "", "A comment recorded with the rule."),
		opt("jump", signature.String, "", "The target: ACCEPT, DROP, REJECT, a chain name."),
		opt("extra", signature.String, "", "Anything else, appended verbatim."),
	}
}

// ---- family and tools ----

func iptablesFamily(args *value.Map) string {
	f := strings.ToLower(strings.TrimSpace(states.Str(args, "family", "ipv4")))
	if f == "" {
		return "ipv4"
	}
	return f
}

func iptablesTable(args *value.Map) string {
	t := strings.TrimSpace(states.Str(args, "table", "filter"))
	if t == "" {
		return "filter"
	}
	return t
}

// iptablesBin is the command for a family, as a pure function so a test
// pins both without either tool on the box.
func iptablesBin(family string) (string, error) {
	switch family {
	case "ipv4", "":
		return "iptables", nil
	case "ipv6":
		return "ip6tables", nil
	}
	return "", fmt.Errorf("%q is not an address family; it is ipv4 or ipv6", family)
}

func iptablesSaveBin(family string) (string, error) {
	bin, err := iptablesBin(family)
	if err != nil {
		return "", err
	}
	return bin + "-save", nil
}

func iptablesToolsPresent(c *exec.Context, family string) error {
	bin, err := iptablesBin(family)
	if err != nil {
		return err
	}
	for _, tool := range []string{bin, bin + "-save"} {
		if c.Which(tool) == "" {
			return fmt.Errorf(
				"this node has no `%s`; it is in the `iptables` package on Debian and RHEL, "+
					"and is not installed by default on a machine with no packet filter configured", tool)
		}
	}
	return nil
}

// ---- reading ----

func iptablesVersion(c *exec.Context, family string) (any, error) {
	if err := iptablesToolsPresent(c, family); err != nil {
		return nil, err
	}
	bin, _ := iptablesBin(family)
	res, err := c.Run(exec.Command{Argv: []string{bin, "--version"}})
	if err != nil {
		return nil, fmt.Errorf("`%s --version` could not be run: %w", bin, err)
	}
	// `iptables v1.8.10 (nf_tables)`
	out := value.NewMap(2)
	text := strings.TrimSpace(res.Stdout)
	fields := strings.Fields(text)
	if len(fields) >= 2 {
		out.Set("version", strings.TrimPrefix(fields[1], "v"))
	} else {
		out.Set("version", text)
	}
	backend := "legacy"
	if strings.Contains(text, "nf_tables") {
		backend = "nf_tables"
	}
	out.Set("backend", backend)
	return out, nil
}

// iptablesRuleset is the whole parsed output of iptables-save.
type iptablesRuleset struct {
	// Tables maps a table name to its chains.
	Tables map[string]*iptablesTableData
}

type iptablesTableData struct {
	Chains map[string]*iptablesChainData
	// Order preserves the chain order iptables-save printed, which is the
	// evaluation-adjacent order a reader expects.
	Order []string
}

type iptablesChainData struct {
	// Policy is ACCEPT/DROP for a built-in chain, "" for a user chain.
	Policy string
	// Rules are the `-A` bodies, without the leading `-A CHAIN`.
	Rules []string
}

// iptablesParseSave reads iptables-save's restore format.
//
//	*filter
//	:INPUT ACCEPT [0:0]
//	:my-chain - [0:0]
//	-A INPUT -p tcp -m tcp --dport 22 -j ACCEPT
//	COMMIT
//
// A `:` line names a chain and its policy (`-` for a user chain); an
// `-A` line is a rule in the chain it names. The packet and byte counts
// in brackets are dropped — they are runtime state, not configuration,
// and a reader comparing two saves would see them differ on every busy
// node.
func iptablesParseSave(text string) (*iptablesRuleset, error) {
	rs := &iptablesRuleset{Tables: map[string]*iptablesTableData{}}
	var current *iptablesTableData
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "*"):
			name := strings.TrimPrefix(line, "*")
			current = &iptablesTableData{Chains: map[string]*iptablesChainData{}}
			rs.Tables[name] = current
		case line == "COMMIT":
			current = nil
		case strings.HasPrefix(line, ":"):
			if current == nil {
				return nil, fmt.Errorf("iptables-save has a chain line outside a table: %q", line)
			}
			fields := strings.Fields(strings.TrimPrefix(line, ":"))
			if len(fields) < 2 {
				return nil, fmt.Errorf("iptables-save chain line %q is malformed", line)
			}
			policy := fields[1]
			if policy == "-" {
				policy = ""
			}
			current.Chains[fields[0]] = &iptablesChainData{Policy: policy}
			current.Order = append(current.Order, fields[0])
		case strings.HasPrefix(line, "-A "):
			if current == nil {
				return nil, fmt.Errorf("iptables-save has a rule outside a table: %q", line)
			}
			rest := strings.TrimPrefix(line, "-A ")
			chain, body, ok := strings.Cut(rest, " ")
			if !ok {
				return nil, fmt.Errorf("iptables-save rule %q names no chain body", line)
			}
			ch := current.Chains[chain]
			if ch == nil {
				ch = &iptablesChainData{}
				current.Chains[chain] = ch
				current.Order = append(current.Order, chain)
			}
			ch.Rules = append(ch.Rules, strings.TrimSpace(body))
		}
	}
	return rs, nil
}

func iptablesReadRuleset(c *exec.Context, family string) (*iptablesRuleset, error) {
	if err := iptablesToolsPresent(c, family); err != nil {
		return nil, err
	}
	saveBin, _ := iptablesSaveBin(family)
	res, err := c.Run(exec.Command{Argv: []string{saveBin}, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("`%s` could not be run: %w", saveBin, err)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("`%s` exited %d: %s", saveBin, res.Code, strings.TrimSpace(firstLine(res.Stderr)))
	}
	return iptablesParseSave(res.Stdout)
}

func iptablesGetRules(c *exec.Context, family, tableFilter string) (any, error) {
	rs, err := iptablesReadRuleset(c, family)
	if err != nil {
		return nil, err
	}
	tables := value.NewMap(len(rs.Tables))
	names := make([]string, 0, len(rs.Tables))
	for name := range rs.Tables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if tableFilter != "" && name != tableFilter {
			continue
		}
		td := rs.Tables[name]
		chains := value.NewMap(len(td.Chains))
		for _, chainName := range td.Order {
			ch := td.Chains[chainName]
			rules := make([]any, len(ch.Rules))
			for i, ruleBody := range ch.Rules {
				rules[i] = ruleBody
			}
			entry := value.NewMap(3)
			entry.Set("policy", nilIfEmpty(ch.Policy))
			entry.Set("builtin", ch.Policy != "")
			entry.Set("rules", rules)
			chains.Set(chainName, entry)
		}
		td2 := value.NewMap(1)
		td2.Set("chains", chains)
		tables.Set(name, td2)
	}
	out := value.NewMap(2)
	out.Set("family", family)
	out.Set("tables", tables)
	return out, nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// iptablesChainPolicy returns a built-in chain's policy, or "" if the
// chain is not built in (or not present).
func iptablesChainPolicy(c *exec.Context, family, table, chain string) (string, error) {
	rs, err := iptablesReadRuleset(c, family)
	if err != nil {
		return "", err
	}
	td := rs.Tables[table]
	if td == nil || td.Chains[chain] == nil {
		return "", nil
	}
	return td.Chains[chain].Policy, nil
}

func iptablesChainExists(c *exec.Context, family, table, chain string) (bool, string, error) {
	rs, err := iptablesReadRuleset(c, family)
	if err != nil {
		return false, "", err
	}
	td := rs.Tables[table]
	if td == nil || td.Chains[chain] == nil {
		return false, "", nil
	}
	return true, td.Chains[chain].Policy, nil
}

// ---- rule specifications ----

// iptablesSpecFromArgs resolves the rule the caller meant: a literal
// `rule` (string or token list) if given, otherwise one assembled from
// the build_rule parts.
func iptablesSpecFromArgs(args *value.Map) ([]string, error) {
	if v, ok := args.Get("rule"); ok && v != nil {
		switch t := v.(type) {
		case string:
			toks := splitRuleSpec(t)
			if len(toks) == 0 {
				return nil, errors.New("the rule is empty")
			}
			return toks, nil
		case []any:
			out := make([]string, 0, len(t))
			for _, e := range t {
				out = append(out, value.KeyString(e))
			}
			if len(out) == 0 {
				return nil, errors.New("the rule is empty")
			}
			return out, nil
		case []string:
			if len(t) == 0 {
				return nil, errors.New("the rule is empty")
			}
			return t, nil
		}
	}
	built := iptablesBuildRule(args)
	if len(built) == 0 {
		return nil, errors.New("no rule was given: pass `rule`, or the parts build_rule takes")
	}
	return built, nil
}

// iptablesBuildRule assembles a rule spec from its parts, in a fixed
// order so that `iptables -C` sees the same string every run.
func iptablesBuildRule(args *value.Map) []string {
	var out []string
	add := func(flag, val string) {
		if strings.TrimSpace(val) != "" {
			out = append(out, flag, val)
		}
	}
	add("-i", states.Str(args, "in_interface", ""))
	add("-o", states.Str(args, "out_interface", ""))
	add("-s", states.Str(args, "source", ""))
	add("-d", states.Str(args, "destination", ""))
	if proto := strings.TrimSpace(states.Str(args, "protocol", "")); proto != "" {
		out = append(out, "-p", proto)
	}
	for _, m := range states.Strings(args, "match") {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, "-m", m)
		}
	}
	if ct := strings.TrimSpace(states.Str(args, "ctstate", "")); ct != "" {
		out = append(out, "-m", "conntrack", "--ctstate", ct)
	}
	add("--sport", states.Str(args, "sport", ""))
	add("--dport", states.Str(args, "dport", ""))
	if comment := strings.TrimSpace(states.Str(args, "comment", "")); comment != "" {
		out = append(out, "-m", "comment", "--comment", comment)
	}
	if extra := strings.TrimSpace(states.Str(args, "extra", "")); extra != "" {
		out = append(out, splitRuleSpec(extra)...)
	}
	if jump := strings.TrimSpace(states.Str(args, "jump", "")); jump != "" {
		out = append(out, "-j", jump)
	}
	return out
}

// splitRuleSpec tokenises a rule string, keeping a double-quoted run —
// `--comment "ssh from bastion"` — as one token with its quotes
// stripped. iptables' own argv is what this has to reproduce, and a
// naive Fields() split would hand it `"ssh` and `bastion"` as two
// arguments and a match it never made.
func splitRuleSpec(s string) []string {
	var (
		toks []string
		cur  strings.Builder
		q    bool
		has  bool
	)
	flush := func() {
		if has {
			toks = append(toks, cur.String())
			cur.Reset()
			has = false
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			q = !q
			has = true
		case (r == ' ' || r == '\t') && !q:
			flush()
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	flush()
	return toks
}

// iptablesRulePresent asks iptables -C whether a rule is in a chain.
func iptablesRulePresent(c *exec.Context, family, table, chain string, spec []string) (bool, []string, error) {
	if err := iptablesToolsPresent(c, family); err != nil {
		return false, nil, err
	}
	if strings.TrimSpace(chain) == "" {
		return false, nil, errors.New("a chain must be named")
	}
	bin, _ := iptablesBin(family)
	argv := append([]string{bin, "-t", table, "-C", chain}, spec...)
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return false, argv, fmt.Errorf("`%s -C` could not be run: %w", bin, err)
	}
	switch res.Code {
	case 0:
		return true, argv, nil
	case 1:
		// The rule is not there — or the spec is malformed, which iptables
		// reports the same way. If it is malformed, -A would fail too, so
		// this is left for the write to surface with iptables' own words.
		return false, argv, nil
	default:
		return false, argv, fmt.Errorf("`%s` exited %d: %s",
			exec.Command{Argv: argv}.String(), res.Code, strings.TrimSpace(res.Stderr+res.Stdout))
	}
}

// ---- writing: rules ----

func iptablesAddRule(c *exec.Context, args *value.Map, mode string) (any, error) {
	family := iptablesFamily(args)
	table := iptablesTable(args)
	chain := strings.TrimSpace(states.Str(args, "chain", ""))
	spec, err := iptablesSpecFromArgs(args)
	if err != nil {
		return nil, err
	}
	present, _, err := iptablesRulePresent(c, family, table, chain, spec)
	if err != nil {
		return nil, err
	}
	shown := strings.Join(spec, " ")
	if present {
		return iptablesMutateResult(c, false, fmt.Sprintf("`%s` is already in %s/%s.", shown, table, chain), nil), nil
	}

	bin, _ := iptablesBin(family)
	var argv []string
	switch mode {
	case "insert":
		pos := states.Int(args, "position", 1)
		argv = append([]string{bin, "-t", table, "-I", chain, strconv.FormatInt(pos, 10)}, spec...)
	default:
		argv = append([]string{bin, "-t", table, "-A", chain}, spec...)
	}
	change := value.MapOf(shown, states.Change(nil, "present in "+table+"/"+chain))
	if c.Test {
		return iptablesMutateResult(c, true, fmt.Sprintf("`%s` would be added to %s/%s.", shown, table, chain), change), nil
	}
	if err := iptablesRun(c, argv); err != nil {
		return nil, err
	}
	return iptablesMutateResult(c, true, fmt.Sprintf("`%s` was added to %s/%s.", shown, table, chain), change), nil
}

func iptablesDeleteRule(c *exec.Context, args *value.Map) (any, error) {
	family := iptablesFamily(args)
	table := iptablesTable(args)
	chain := strings.TrimSpace(states.Str(args, "chain", ""))
	spec, err := iptablesSpecFromArgs(args)
	if err != nil {
		return nil, err
	}
	present, _, err := iptablesRulePresent(c, family, table, chain, spec)
	if err != nil {
		return nil, err
	}
	shown := strings.Join(spec, " ")
	if !present {
		return iptablesMutateResult(c, false, fmt.Sprintf("`%s` is not in %s/%s.", shown, table, chain), nil), nil
	}
	bin, _ := iptablesBin(family)
	argv := append([]string{bin, "-t", table, "-D", chain}, spec...)
	change := value.MapOf(shown, states.Change("present in "+table+"/"+chain, nil))
	if c.Test {
		return iptablesMutateResult(c, true, fmt.Sprintf("`%s` would be removed from %s/%s.", shown, table, chain), change), nil
	}
	if err := iptablesRun(c, argv); err != nil {
		return nil, err
	}
	return iptablesMutateResult(c, true, fmt.Sprintf("`%s` was removed from %s/%s.", shown, table, chain), change), nil
}

// ---- writing: chains ----

func iptablesChainChange(c *exec.Context, args *value.Map, create bool) (any, error) {
	family := iptablesFamily(args)
	table := iptablesTable(args)
	chain := strings.TrimSpace(states.Str(args, "chain", ""))
	if chain == "" {
		return nil, errors.New("a chain must be named")
	}
	exists, policy, err := iptablesChainExists(c, family, table, chain)
	if err != nil {
		return nil, err
	}
	if create && exists {
		return iptablesMutateResult(c, false, fmt.Sprintf("chain %s/%s already exists.", table, chain), nil), nil
	}
	if !create && !exists {
		return iptablesMutateResult(c, false, fmt.Sprintf("chain %s/%s does not exist.", table, chain), nil), nil
	}
	if !create && policy != "" {
		return nil, fmt.Errorf("%s/%s is a built-in chain and cannot be deleted", table, chain)
	}

	bin, _ := iptablesBin(family)
	flag := "-N"
	verb, past := "created", "was created"
	if !create {
		flag, verb, past = "-X", "removed", "was removed"
	}
	change := value.MapOf(table+"/"+chain, states.Change(!create, create))
	if c.Test {
		return iptablesMutateResult(c, true, fmt.Sprintf("chain %s/%s would be %s.", table, chain, verb), change), nil
	}
	if err := iptablesRun(c, []string{bin, "-t", table, flag, chain}); err != nil {
		return nil, err
	}
	return iptablesMutateResult(c, true, fmt.Sprintf("chain %s/%s %s.", table, chain, past), change), nil
}

// ---- writing: policy ----

var iptablesBuiltinChains = map[string]bool{
	"INPUT": true, "OUTPUT": true, "FORWARD": true, "PREROUTING": true, "POSTROUTING": true,
}

func iptablesSetPolicyFn(c *exec.Context, args *value.Map) (any, error) {
	res, _, err := iptablesSetPolicy(c, args)
	return res, err
}

func iptablesSetPolicy(c *exec.Context, args *value.Map) (*value.Map, bool, error) {
	family := iptablesFamily(args)
	table := iptablesTable(args)
	chain := strings.TrimSpace(states.Str(args, "chain", ""))
	if chain == "" {
		chain = strings.TrimSpace(states.Str(args, "name", ""))
	}
	policy := strings.ToUpper(strings.TrimSpace(states.Str(args, "policy", "")))
	if policy != "ACCEPT" && policy != "DROP" {
		return nil, false, fmt.Errorf("a chain policy is ACCEPT or DROP, not %q", policy)
	}
	if !iptablesBuiltinChains[chain] {
		return nil, false, fmt.Errorf("%q is not a built-in chain, so it has no policy to set", chain)
	}
	current, err := iptablesChainPolicy(c, family, table, chain)
	if err != nil {
		return nil, false, err
	}
	if current == policy {
		return iptablesMutateResult(c, false, fmt.Sprintf("%s/%s policy is already %s.", table, chain, policy), nil), false, nil
	}
	bin, _ := iptablesBin(family)
	change := value.MapOf(table+"/"+chain+" policy", states.Change(nilIfEmpty(current), policy))
	if c.Test {
		return iptablesMutateResult(c, true, fmt.Sprintf("%s/%s policy would be set to %s.", table, chain, policy), change), true, nil
	}
	if err := iptablesRun(c, []string{bin, "-t", table, "-P", chain, policy}); err != nil {
		return nil, false, err
	}
	return iptablesMutateResult(c, true, fmt.Sprintf("%s/%s policy was set to %s.", table, chain, policy), change), true, nil
}

// ---- writing: flush ----

func iptablesFlushFn(c *exec.Context, args *value.Map) (any, error) {
	family := iptablesFamily(args)
	table := iptablesTable(args)
	chain := strings.TrimSpace(states.Str(args, "chain", ""))
	force := states.Bool(args, "force", false)

	rs, err := iptablesReadRuleset(c, family)
	if err != nil {
		return nil, err
	}
	td := rs.Tables[table]
	if td == nil {
		return iptablesMutateResult(c, false, fmt.Sprintf("table %s has no chains to flush.", table), nil), nil
	}

	if chain == "" && !force {
		return nil, fmt.Errorf(
			"flushing every chain in %s at once opens or closes the whole table; name a chain, or pass force", table)
	}
	if chain != "" {
		ch := td.Chains[chain]
		if ch == nil {
			return iptablesMutateResult(c, false, fmt.Sprintf("chain %s/%s does not exist.", table, chain), nil), nil
		}
		if ch.Policy != "" && ch.Policy != "ACCEPT" && !force {
			return nil, fmt.Errorf(
				"%s/%s is a built-in chain with policy %s: flushing it drops the rules that are letting traffic through "+
					"and the node goes off the network. Pass force if that is what you mean", table, chain, ch.Policy)
		}
		if len(ch.Rules) == 0 {
			return iptablesMutateResult(c, false, fmt.Sprintf("chain %s/%s is already empty.", table, chain), nil), nil
		}
	}

	bin, _ := iptablesBin(family)
	argv := []string{bin, "-t", table, "-F"}
	target := "every chain in " + table
	removed := 0
	if chain != "" {
		argv = append(argv, chain)
		target = table + "/" + chain
		removed = len(td.Chains[chain].Rules)
	} else {
		for _, ch := range td.Chains {
			removed += len(ch.Rules)
		}
	}
	change := value.MapOf(target, states.Change(fmt.Sprintf("%d rule(s)", removed), "empty"))
	if c.Test {
		return iptablesMutateResult(c, true, fmt.Sprintf("%s would be flushed (%d rule(s)).", target, removed), change), nil
	}
	if err := iptablesRun(c, argv); err != nil {
		return nil, err
	}
	return iptablesMutateResult(c, true, fmt.Sprintf("%s was flushed (%d rule(s)).", target, removed), change), nil
}

// ---- writing: save ----

// IPTablesSavePaths is where each family's ruleset is written for the
// boot-time restore. A variable so a test can redirect it.
var IPTablesSavePaths = map[string]string{
	"ipv4": "/etc/iptables/rules.v4",
	"ipv6": "/etc/iptables/rules.v6",
}

func iptablesSaveFn(c *exec.Context, args *value.Map) (any, error) {
	family := iptablesFamily(args)
	if err := iptablesToolsPresent(c, family); err != nil {
		return nil, err
	}
	path := strings.TrimSpace(states.Str(args, "path", ""))
	if path == "" {
		var err error
		path, err = iptablesDefaultSavePath(family)
		if err != nil {
			return nil, err
		}
	}
	saveBin, _ := iptablesSaveBin(family)
	res, err := c.Run(exec.Command{Argv: []string{saveBin}, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("`%s` could not be run: %w", saveBin, err)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("`%s` exited %d: %s", saveBin, res.Code, strings.TrimSpace(firstLine(res.Stderr)))
	}
	want := res.Stdout

	existing, _ := os.ReadFile(path)
	if string(existing) == want {
		return iptablesMutateResult(c, false, fmt.Sprintf("%s already holds the running ruleset.", path), nil), nil
	}
	change := value.MapOf(path, states.Change("stale or absent", "the running ruleset"))
	if c.Test {
		return iptablesMutateResult(c, true, fmt.Sprintf("the running ruleset would be written to %s.", path), change), nil
	}
	if err := atomicfile.Write(path, []byte(want), 0o600); err != nil {
		return nil, fmt.Errorf("%s could not be written: %w", path, err)
	}
	return iptablesMutateResult(c, true, fmt.Sprintf("the running ruleset was written to %s.", path), change), nil
}

// iptablesDefaultSavePath picks the distribution's own restore file, or
// says which package owns it.
func iptablesDefaultSavePath(family string) (string, error) {
	if p := IPTablesSavePaths[family]; p != "" {
		if _, err := os.Stat(filepath.Dir(p)); err == nil {
			return p, nil
		}
	}
	// RHEL's iptables-services keeps them here instead.
	rhel := map[string]string{"ipv4": "/etc/sysconfig/iptables", "ipv6": "/etc/sysconfig/ip6tables"}[family]
	if rhel != "" {
		if _, err := os.Stat(filepath.Dir(rhel)); err == nil {
			return rhel, nil
		}
	}
	return "", fmt.Errorf(
		"this node has no directory for a saved ruleset: install `iptables-persistent` (Debian) or " +
			"`iptables-services` (RHEL), or pass an explicit `path`")
}

// ---- shared write helper ----

func iptablesRun(c *exec.Context, argv []string) error {
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return fmt.Errorf("%s could not be run: %w", argv[0], err)
	}
	if res.Code != 0 {
		return fmt.Errorf("`%s` exited %d: %s", exec.Command{Argv: argv}.String(), res.Code,
			strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return nil
}

func iptablesMutateResult(c *exec.Context, changed bool, comment string, change *value.Map) *value.Map {
	out := value.NewMap(3)
	out.Set("changed", changed)
	if c.Test && changed {
		out.Set("comment", comment+" Nothing was changed: this was a test run.")
	} else {
		out.Set("comment", comment)
	}
	if change != nil {
		out.Set("changes", change)
	}
	return out
}

// ---- states ----

func iptablesChainState(c *exec.Context, args *value.Map, present bool) (states.Result, error) {
	family := iptablesFamily(args)
	table := iptablesTable(args)
	chain := strings.TrimSpace(states.Str(args, "name", ""))
	if chain == "" {
		return states.False("a chain must be named"), nil
	}
	exists, policy, err := iptablesChainExists(c, family, table, chain)
	if err != nil {
		return states.False(err.Error()), nil
	}
	if policy != "" {
		return states.False(fmt.Sprintf("%s/%s is a built-in chain; this state manages user-defined chains", table, chain)), nil
	}

	if present {
		if exists {
			return states.True(fmt.Sprintf("chain %s/%s exists.", table, chain)), nil
		}
		changes := value.MapOf(table+"/"+chain, states.Change(nil, "present"))
		if c.Test {
			return states.WouldChange(fmt.Sprintf("chain %s/%s would be created.", table, chain), changes), nil
		}
		bin, _ := iptablesBin(family)
		if err := iptablesRun(c, []string{bin, "-t", table, "-N", chain}); err != nil {
			return states.False(err.Error()), nil
		}
		return states.Changed(fmt.Sprintf("chain %s/%s was created.", table, chain), changes), nil
	}

	if !exists {
		return states.True(fmt.Sprintf("chain %s/%s does not exist.", table, chain)), nil
	}
	changes := value.MapOf(table+"/"+chain, states.Change("present", nil))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("chain %s/%s would be flushed and removed.", table, chain), changes), nil
	}
	bin, _ := iptablesBin(family)
	if err := iptablesRun(c, []string{bin, "-t", table, "-F", chain}); err != nil {
		return states.False(err.Error()), nil
	}
	if err := iptablesRun(c, []string{bin, "-t", table, "-X", chain}); err != nil {
		return states.False(fmt.Sprintf("the chain was flushed but not removed (it may still be a jump target): %v", err)), nil
	}
	return states.Changed(fmt.Sprintf("chain %s/%s was removed.", table, chain), changes), nil
}

func iptablesRuleState(c *exec.Context, args *value.Map, mode string) (states.Result, error) {
	family := iptablesFamily(args)
	table := iptablesTable(args)
	chain := strings.TrimSpace(states.Str(args, "chain", ""))
	if chain == "" {
		return states.False("a chain must be named"), nil
	}
	spec, err := iptablesRuleStateSpec(args)
	if err != nil {
		return states.False(err.Error()), nil
	}
	present, _, err := iptablesRulePresent(c, family, table, chain, spec)
	if err != nil {
		return states.False(err.Error()), nil
	}
	shown := strings.Join(spec, " ")
	bin, _ := iptablesBin(family)

	if mode == "delete" {
		if !present {
			return states.True(fmt.Sprintf("`%s` is not in %s/%s.", shown, table, chain)), nil
		}
		changes := value.MapOf(shown, states.Change("present", nil))
		if c.Test {
			return states.WouldChange(fmt.Sprintf("`%s` would be removed from %s/%s.", shown, table, chain), changes), nil
		}
		if err := iptablesRun(c, append([]string{bin, "-t", table, "-D", chain}, spec...)); err != nil {
			return states.False(err.Error()), nil
		}
		return states.Changed(fmt.Sprintf("`%s` was removed from %s/%s.", shown, table, chain), changes), nil
	}

	if present {
		return states.True(fmt.Sprintf("`%s` is already in %s/%s.", shown, table, chain)), nil
	}
	changes := value.MapOf(shown, states.Change(nil, "present"))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("`%s` would be added to %s/%s.", shown, table, chain), changes), nil
	}
	var argv []string
	if mode == "insert" {
		pos := states.Int(args, "position", 1)
		argv = append([]string{bin, "-t", table, "-I", chain, strconv.FormatInt(pos, 10)}, spec...)
	} else {
		argv = append([]string{bin, "-t", table, "-A", chain}, spec...)
	}
	if err := iptablesRun(c, argv); err != nil {
		return states.False(err.Error()), nil
	}
	return states.Changed(fmt.Sprintf("`%s` was added to %s/%s.", shown, table, chain), changes), nil
}

// iptablesRuleStateSpec is iptablesSpecFromArgs for a state, where the
// rule may also be the state's `name` when nothing else supplies it.
func iptablesRuleStateSpec(args *value.Map) ([]string, error) {
	if v, ok := args.Get("rule"); ok && v != nil {
		return iptablesSpecFromArgs(args)
	}
	if built := iptablesBuildRule(args); len(built) > 0 {
		return built, nil
	}
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if toks := splitRuleSpec(name); len(toks) > 1 {
		// The state's ID is the rule itself, which is how a one-line
		// declaration reads: `-p tcp --dport 22 -j ACCEPT:`.
		return toks, nil
	}
	return nil, errors.New("no rule was given: pass `rule`, the build_rule parts, or write the rule as the state ID")
}

func iptablesSetPolicyState(c *exec.Context, args *value.Map) (states.Result, error) {
	out, changed, err := iptablesSetPolicy(c, args)
	if err != nil {
		return states.False(err.Error()), nil
	}
	comment, _ := out.GetString("comment")
	cs, _ := out.GetString("changes")
	changes, _ := cs.(*value.Map)
	if !changed {
		return states.True(comment.(string)), nil
	}
	if c.Test {
		return states.WouldChange(comment.(string), changes), nil
	}
	return states.Changed(comment.(string), changes), nil
}

func iptablesFlushState(c *exec.Context, args *value.Map) (states.Result, error) {
	// The state's name is the chain.
	if _, ok := args.Get("chain"); !ok {
		args.Set("chain", states.Str(args, "name", ""))
	}
	out, err := iptablesFlushFn(c, args)
	if err != nil {
		return states.False(err.Error()), nil
	}
	m := out.(*value.Map)
	changed, _ := m.GetString("changed")
	comment, _ := m.GetString("comment")
	cs, _ := m.GetString("changes")
	changes, _ := cs.(*value.Map)
	if changed != true {
		return states.True(comment.(string)), nil
	}
	if c.Test {
		return states.WouldChange(comment.(string), changes), nil
	}
	return states.Changed(comment.(string), changes), nil
}
