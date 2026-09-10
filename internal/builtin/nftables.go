package builtin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerNFTables installs the `nftables` module of SPEC 15.3's Common
// Linux row and the `nftables` states of SPEC 15.5.
//
// # Not a `firewall` provider, for the same reason `iptables` is not
//
// See iptables.go: `firewall` is the ufw-backed abstraction, and these
// two are the layer under it that a tree reaches for when the
// abstraction was not enough.
//
// # Reading is JSON; writing is nft's own syntax on standard input
//
// `nft -j list ruleset` is a structured document — tables, chains with
// their hook and policy, rules with a handle and any comment. It is read
// as JSON so nothing parses `nft list`'s indented text, whose form is
// nft's reprint of its own parse and not the text it was given
// (DIVERGENCE 5.31, in `pf`).
//
// A rule is *written* by feeding nft's own syntax to `nft -f -` on
// standard input: `add rule inet filter input tcp dport 22 accept`. The
// rule body is passed through untouched. Reassembling nft's expression
// grammar here would be a second parser for the most intricate surface
// either firewall has, and it would drift from nft's — the surface
// §5.31 was about. Standard input rather than the argument vector
// because nft re-tokenises its arguments and a colon in a comment then
// breaks its lexer; a script line does not.
//
// # nftables has no `iptables -C`, so a managed rule carries a comment
//
// There is no "is this rule present" in nft. A rule this module manages
// therefore **must carry a `comment`**, and that comment is its
// identity: `append` adds the rule if no rule in the chain has that
// comment, and leaves it otherwise; `delete` finds it by comment and
// removes it by handle. A change to the body of a rule whose comment is
// unchanged is **not** applied — re-declare with a new comment, or
// delete and re-add. This is stricter than Salt, which appends every
// time, and it is the price of idempotence on a tool that does not
// offer it. A rule with no comment can still be appended through the
// execution module, and the result says it was not idempotent.
//
// # What `flush` refuses without `force`
//
// `flush ruleset` wipes every table on the node, including ones another
// tool or the operator wrote. Flushing a base chain whose policy is
// `drop` strips the rules that were letting traffic through. Both are
// refused unless the call passes `force`.
func registerNFTables(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "version",
				Doc:       "Return the nft version.",
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return nftVersion(c)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "list",
				Doc: "Return the ruleset: every table, chain (with its hook and policy) and rule (with its handle and comment).",
				Params: []signature.Param{
					opt("family", signature.String, "", "Limit to one family: ip, ip6, inet, arp, bridge, netdev. Empty lists them all."),
					opt("table", signature.String, "", "Limit to one table. Requires family."),
					opt("chain", signature.String, "", "Limit to one chain. Requires family and table."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: nftListFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "table_present",
				Doc: "Create a table if it is not there.",
				Params: []signature.Param{
					req("name", signature.String, "The table's name."),
					nftFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return nftTableChange(c, args, true) },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "table_absent",
				Doc: "Remove a table, and everything in it, if it is there.",
				Params: []signature.Param{
					req("name", signature.String, "The table's name."),
					nftFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return nftTableChange(c, args, false) },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "chain_present",
				Doc:        "Create a chain if it is not there. A base chain names a hook; a chain with no hook is a plain jump target.",
				Params:     nftChainParams(),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return nftChainChange(c, args, true) },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "chain_absent",
				Doc: "Remove a chain if it is there.",
				Params: []signature.Param{
					req("name", signature.String, "The chain's name."),
					req("table", signature.String, "The table it is in."),
					nftFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return nftChainChange(c, args, false) },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "set_policy",
				Doc: "Set a base chain's policy.",
				Params: []signature.Param{
					req("name", signature.String, "The base chain."),
					req("table", signature.String, "The table it is in."),
					choice("policy", nil, "The policy.", "accept", "drop"),
					nftFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: nftSetPolicyFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "append",
				Doc:        "Add a rule to the end of a chain. Idempotent when the rule carries a comment.",
				Params:     nftRuleParams(),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return nftAddRule(c, args, "append") },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "insert",
				Doc: "Add a rule at the start of a chain, or at an index. Idempotent when the rule carries a comment.",
				Params: append(nftRuleParams(),
					opt("index", signature.Int, int64(-1), "The 0-based index to insert before. -1 inserts at the start.")),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return nftAddRule(c, args, "insert") },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "delete",
				Doc: "Remove a rule, found by its comment or by an explicit handle.",
				Params: []signature.Param{
					req("table", signature.String, "The table."),
					req("chain", signature.String, "The chain."),
					opt("comment", signature.String, "", "The comment of the rule to remove."),
					opt("handle", signature.Int, int64(0), "The handle of the rule to remove. Use `list` to find it."),
					nftFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: nftDeleteRule,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "flush",
				Doc: "Empty a chain, a table, or the whole ruleset. The last two are refused without force.",
				Params: []signature.Param{
					opt("table", signature.String, "", "The table to flush, or empty with no chain to flush the whole ruleset."),
					opt("chain", signature.String, "", "The chain to flush. Requires table."),
					opt("force", signature.Bool, false, "Flush a whole table, the whole ruleset, or a base chain whose policy is drop."),
					nftFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: nftFlushFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "save",
				Doc: "Write the running ruleset to the file the boot-time restore reads.",
				Params: []signature.Param{
					opt("path", signature.Path, "", "Where to write. Empty uses /etc/nftables.conf."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: nftSaveFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "check",
				Doc: "Validate a rule's syntax without applying it, and report whether a rule with a given comment is in a chain.",
				Params: []signature.Param{
					opt("family", signature.String, "inet", "The family."),
					opt("table", signature.String, "", "The table, for the comment check."),
					opt("chain", signature.String, "", "The chain, for the comment check."),
					opt("rule", signature.String, "", "A rule body to syntax-check."),
					opt("comment", signature.String, "", "A comment to look for in the chain."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: nftCheckFn,
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "table_present",
				Doc: "Ensure a table exists.",
				Params: []signature.Param{
					nameParam("The table's name. Defaults to the state ID."),
					nftFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) { return nftTableState(c, args, true) },
		},
		states.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "table_absent",
				Doc: "Ensure a table does not exist.",
				Params: []signature.Param{
					nameParam("The table's name. Defaults to the state ID."),
					nftFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) { return nftTableState(c, args, false) },
		},
		states.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "chain_present",
				Doc: "Ensure a chain exists, with the hook and policy declared for a base chain.",
				Params: append([]signature.Param{nameParam("The chain's name. Defaults to the state ID.")},
					nftChainParams()[1:]...),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) { return nftChainState(c, args, true) },
		},
		states.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "chain_absent",
				Doc: "Ensure a chain does not exist.",
				Params: []signature.Param{
					nameParam("The chain's name. Defaults to the state ID."),
					req("table", signature.String, "The table it is in."),
					nftFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) { return nftChainState(c, args, false) },
		},
		states.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "set_policy",
				Doc: "Ensure a base chain's policy is as declared.",
				Params: []signature.Param{
					nameParam("The base chain. Defaults to the state ID."),
					req("table", signature.String, "The table it is in."),
					choice("policy", nil, "The policy.", "accept", "drop"),
					nftFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: nftSetPolicyState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "append",
				Doc:        "Ensure a rule, identified by its comment, is present at the end of a chain.",
				Params:     nftRuleStateParams(),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) { return nftRuleState(c, args, "append") },
		},
		states.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "insert",
				Doc: "Ensure a rule, identified by its comment, is present at the start of a chain.",
				Params: append(nftRuleStateParams(),
					opt("index", signature.Int, int64(-1), "The 0-based index to insert before. -1 is the start.")),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) { return nftRuleState(c, args, "insert") },
		},
		states.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "delete",
				Doc: "Ensure a rule, identified by its comment, is not present in a chain.",
				Params: []signature.Param{
					nameParam("A label. Defaults to the state ID."),
					req("table", signature.String, "The table."),
					req("chain", signature.String, "The chain."),
					req("comment", signature.String, "The comment of the rule to remove."),
					nftFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) { return nftRuleState(c, args, "delete") },
		},
		states.Module{
			Sig: signature.Signature{
				Module: "nftables", Function: "flush",
				Doc: "Ensure a chain or table has no rules. A base chain with a drop policy, or a whole table, needs force.",
				Params: []signature.Param{
					nameParam("The chain or table. Defaults to the state ID."),
					opt("table", signature.String, "", "The table, when `name` is a chain."),
					opt("force", signature.Bool, false, "Flush a whole table or a drop-policy base chain."),
					nftFamilyParam(),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: nftFlushState,
		},
	)
}

// ---- parameters ----

var nftFamilies = map[string]bool{
	"ip": true, "ip6": true, "inet": true, "arp": true, "bridge": true, "netdev": true,
}

func nftFamilyParam() signature.Param {
	return choice("family", "inet", "The address family.", "ip", "ip6", "inet", "arp", "bridge", "netdev")
}

func nftChainParams() []signature.Param {
	return []signature.Param{
		req("name", signature.String, "The chain's name."),
		req("table", signature.String, "The table it is in."),
		opt("hook", signature.String, "", "For a base chain: input, output, forward, prerouting, postrouting or ingress. Empty makes a plain chain."),
		opt("type", signature.String, "filter", "For a base chain: filter, nat or route."),
		opt("priority", signature.String, "0", "For a base chain: a number or a keyword such as filter, mangle, dstnat, srcnat."),
		choice("policy", "accept", "For a base chain: the default policy.", "accept", "drop"),
		nftFamilyParam(),
	}
}

func nftRuleParams() []signature.Param {
	return []signature.Param{
		req("table", signature.String, "The table."),
		req("chain", signature.String, "The chain."),
		req("rule", signature.String, "The rule body in nft syntax, such as `tcp dport 22 accept`. Passed through unchanged."),
		opt("comment", signature.String, "", "A comment. Managed rules should carry one; it is the rule's identity for idempotence."),
		nftFamilyParam(),
	}
}

func nftRuleStateParams() []signature.Param {
	return []signature.Param{
		nameParam("A label for the rule. Defaults to the state ID."),
		req("table", signature.String, "The table."),
		req("chain", signature.String, "The chain."),
		req("rule", signature.String, "The rule body in nft syntax."),
		req("comment", signature.String, "The rule's comment, which is its identity. Required for a state."),
		nftFamilyParam(),
	}
}

func nftFamily(args *value.Map) string {
	f := strings.ToLower(strings.TrimSpace(states.Str(args, "family", "inet")))
	if f == "" {
		return "inet"
	}
	return f
}

func nftCheckFamily(f string) error {
	if !nftFamilies[f] {
		return fmt.Errorf("%q is not an nftables family; it is one of ip, ip6, inet, arp, bridge or netdev", f)
	}
	return nil
}

func nftToolPresent(c *exec.Context) error {
	if c.Which("nft") == "" {
		return errors.New(
			"this node has no `nft`; it is in the `nftables` package on Debian and RHEL, " +
				"and is not installed by default on a machine with no packet filter configured")
	}
	return nil
}

// ---- reading ----

func nftVersion(c *exec.Context) (any, error) {
	if err := nftToolPresent(c); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{Argv: []string{"nft", "--version"}})
	if err != nil {
		return nil, fmt.Errorf("`nft --version` could not be run: %w", err)
	}
	// `nftables v1.0.9 (Old Doc Yak #3)`
	out := value.NewMap(2)
	fields := strings.Fields(strings.TrimSpace(res.Stdout))
	if len(fields) >= 2 {
		out.Set("version", strings.TrimPrefix(fields[1], "v"))
	} else {
		out.Set("version", strings.TrimSpace(res.Stdout))
	}
	if i := strings.IndexByte(res.Stdout, '('); i >= 0 {
		out.Set("release", strings.Trim(strings.TrimSpace(res.Stdout[i:]), "()\n"))
	}
	return out, nil
}

// nftRuleset is the parsed `nft -j list` stream.
type nftRuleset struct {
	Tables []nftTable
	Chains []nftChain
	Rules  []nftRule
}

type nftTable struct {
	Family string
	Name   string
}

type nftChain struct {
	Family string
	Table  string
	Name   string
	// Type/Hook/Prio/Policy are set only for a base chain.
	Type   string
	Hook   string
	Prio   string
	Policy string
}

func (ch nftChain) Base() bool { return ch.Hook != "" }

type nftRule struct {
	Family  string
	Table   string
	Chain   string
	Handle  int64
	Comment string
	// Expr is nft's own parsed form, carried through for a caller that
	// wants to inspect a rule without this module reconstructing nft's
	// grammar.
	Expr any
}

// nftListRaw runs `nft -j list` for a scope and returns the decoded
// stream.
func nftListRaw(c *exec.Context, scopeArgs ...string) (*nftRuleset, error) {
	if err := nftToolPresent(c); err != nil {
		return nil, err
	}
	argv := append([]string{"nft", "-j", "list"}, scopeArgs...)
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("`nft -j list` could not be run: %w", err)
	}
	if res.Code != 0 {
		// A scope that names nothing — `list table inet nope` — is an
		// error from nft, and to a caller asking "what is in this table"
		// the honest answer is an empty ruleset, not a failure.
		if strings.Contains(res.Stderr, "does not exist") || strings.Contains(res.Stderr, "No such file") {
			return &nftRuleset{}, nil
		}
		return nil, fmt.Errorf("`%s` exited %d: %s",
			strings.Join(argv, " "), res.Code, strings.TrimSpace(firstLine(res.Stderr)))
	}
	return nftParseList(res.Stdout)
}

// nftParseList decodes the `{"nftables": [ {metainfo}, {table}, {chain},
// {rule}, ... ]}` stream. Each element is a one-key object.
func nftParseList(out string) (*nftRuleset, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, errors.New("nft produced no output; expected a JSON ruleset")
	}
	var doc struct {
		NFTables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return nil, fmt.Errorf("nft's JSON did not parse: %w", err)
	}
	rs := &nftRuleset{}
	for _, item := range doc.NFTables {
		for key, raw := range item {
			switch key {
			case "table":
				var t struct {
					Family string `json:"family"`
					Name   string `json:"name"`
				}
				if err := json.Unmarshal(raw, &t); err != nil {
					return nil, fmt.Errorf("a table entry did not parse: %w", err)
				}
				rs.Tables = append(rs.Tables, nftTable{Family: t.Family, Name: t.Name})
			case "chain":
				var ch struct {
					Family string          `json:"family"`
					Table  string          `json:"table"`
					Name   string          `json:"name"`
					Type   string          `json:"type"`
					Hook   string          `json:"hook"`
					Prio   json.RawMessage `json:"prio"`
					Policy string          `json:"policy"`
				}
				if err := json.Unmarshal(raw, &ch); err != nil {
					return nil, fmt.Errorf("a chain entry did not parse: %w", err)
				}
				rs.Chains = append(rs.Chains, nftChain{
					Family: ch.Family, Table: ch.Table, Name: ch.Name,
					Type: ch.Type, Hook: ch.Hook, Prio: nftPrioString(ch.Prio), Policy: ch.Policy,
				})
			case "rule":
				var ru struct {
					Family  string `json:"family"`
					Table   string `json:"table"`
					Chain   string `json:"chain"`
					Handle  int64  `json:"handle"`
					Comment string `json:"comment"`
					Expr    any    `json:"expr"`
				}
				if err := json.Unmarshal(raw, &ru); err != nil {
					return nil, fmt.Errorf("a rule entry did not parse: %w", err)
				}
				rs.Rules = append(rs.Rules, nftRule{
					Family: ru.Family, Table: ru.Table, Chain: ru.Chain,
					Handle: ru.Handle, Comment: ru.Comment, Expr: ru.Expr,
				})
			}
		}
	}
	return rs, nil
}

// nftPrioString renders a priority, which nft emits as a number or, for
// a named priority, a string.
func nftPrioString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return strconv.FormatInt(n, 10)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.Trim(string(raw), `"`)
}

func nftListFn(c *exec.Context, args *value.Map) (any, error) {
	family := strings.TrimSpace(states.Str(args, "family", ""))
	table := strings.TrimSpace(states.Str(args, "table", ""))
	chain := strings.TrimSpace(states.Str(args, "chain", ""))
	var scope []string
	switch {
	case chain != "":
		if family == "" || table == "" {
			return nil, errors.New("a chain scope needs family and table too")
		}
		scope = []string{"chain", family, table, chain}
	case table != "":
		if family == "" {
			return nil, errors.New("a table scope needs family too")
		}
		scope = []string{"table", family, table}
	case family != "":
		scope = []string{"ruleset", family}
	default:
		scope = []string{"ruleset"}
	}
	rs, err := nftListRaw(c, scope...)
	if err != nil {
		return nil, err
	}
	return nftRulesetValue(rs), nil
}

func nftRulesetValue(rs *nftRuleset) *value.Map {
	tables := make([]any, 0, len(rs.Tables))
	for _, t := range rs.Tables {
		tables = append(tables, value.MapOf("family", t.Family, "name", t.Name))
	}
	chains := make([]any, 0, len(rs.Chains))
	for _, ch := range rs.Chains {
		m := value.NewMap(7)
		m.Set("family", ch.Family)
		m.Set("table", ch.Table)
		m.Set("name", ch.Name)
		m.Set("base", ch.Base())
		if ch.Base() {
			m.Set("type", ch.Type)
			m.Set("hook", ch.Hook)
			m.Set("priority", ch.Prio)
			m.Set("policy", ch.Policy)
		}
		chains = append(chains, m)
	}
	rules := make([]any, 0, len(rs.Rules))
	for _, ru := range rs.Rules {
		m := value.NewMap(6)
		m.Set("family", ru.Family)
		m.Set("table", ru.Table)
		m.Set("chain", ru.Chain)
		m.Set("handle", ru.Handle)
		m.Set("comment", nilIfEmpty(ru.Comment))
		m.Set("expr", ru.Expr)
		rules = append(rules, m)
	}
	out := value.NewMap(3)
	out.Set("tables", tables)
	out.Set("chains", chains)
	out.Set("rules", rules)
	return out
}

// ---- finders ----

func nftFindTable(rs *nftRuleset, family, name string) bool {
	for _, t := range rs.Tables {
		if t.Family == family && t.Name == name {
			return true
		}
	}
	return false
}

func nftFindChain(rs *nftRuleset, family, table, name string) *nftChain {
	for i := range rs.Chains {
		ch := rs.Chains[i]
		if ch.Family == family && ch.Table == table && ch.Name == name {
			return &rs.Chains[i]
		}
	}
	return nil
}

func nftFindRuleByComment(rs *nftRuleset, family, table, chain, comment string) *nftRule {
	if comment == "" {
		// An empty comment is not an identity; it would otherwise match
		// every rule that was written without one.
		return nil
	}
	for i := range rs.Rules {
		ru := rs.Rules[i]
		if ru.Family == family && ru.Table == table && ru.Chain == chain && ru.Comment == comment {
			return &rs.Rules[i]
		}
	}
	return nil
}

// ---- writing ----

// nftApply feeds a script to `nft -f -`.
func nftApply(c *exec.Context, script string) error {
	if err := nftToolPresent(c); err != nil {
		return err
	}
	res, err := c.Run(exec.Command{Argv: []string{"nft", "-f", "-"}, Stdin: script, IgnoreExitCode: true})
	if err != nil {
		return fmt.Errorf("`nft -f -` could not be run: %w", err)
	}
	if res.Code != 0 {
		return fmt.Errorf("nft refused the change (exit %d): %s\n  script: %s",
			res.Code, strings.TrimSpace(res.Stderr+res.Stdout), strings.TrimSpace(script))
	}
	return nil
}

func nftMutateResult(c *exec.Context, changed bool, comment string, change *value.Map, warnings ...string) *value.Map {
	out := value.NewMap(4)
	out.Set("changed", changed)
	if c.Test && changed {
		out.Set("comment", comment+" Nothing was changed: this was a test run.")
	} else {
		out.Set("comment", comment)
	}
	if change != nil {
		out.Set("changes", change)
	}
	if len(warnings) > 0 {
		ws := make([]any, len(warnings))
		for i, w := range warnings {
			ws[i] = w
		}
		out.Set("warnings", ws)
	}
	return out
}

func nftTableChange(c *exec.Context, args *value.Map, present bool) (any, error) {
	family := nftFamily(args)
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if name == "" {
		return nil, errors.New("a table must be named")
	}
	if err := nftCheckFamily(family); err != nil {
		return nil, err
	}
	rs, err := nftListRaw(c, "ruleset")
	if err != nil {
		return nil, err
	}
	exists := nftFindTable(rs, family, name)

	if present {
		if exists {
			return nftMutateResult(c, false, fmt.Sprintf("table %s %s already exists.", family, name), nil), nil
		}
		change := value.MapOf(family+" "+name, states.Change(nil, "present"))
		if c.Test {
			return nftMutateResult(c, true, fmt.Sprintf("table %s %s would be created.", family, name), change), nil
		}
		if err := nftApply(c, fmt.Sprintf("add table %s %s\n", family, name)); err != nil {
			return nil, err
		}
		return nftMutateResult(c, true, fmt.Sprintf("table %s %s was created.", family, name), change), nil
	}

	if !exists {
		return nftMutateResult(c, false, fmt.Sprintf("table %s %s does not exist.", family, name), nil), nil
	}
	change := value.MapOf(family+" "+name, states.Change("present", nil))
	if c.Test {
		return nftMutateResult(c, true, fmt.Sprintf("table %s %s would be removed, with everything in it.", family, name), change), nil
	}
	if err := nftApply(c, fmt.Sprintf("delete table %s %s\n", family, name)); err != nil {
		return nil, err
	}
	return nftMutateResult(c, true, fmt.Sprintf("table %s %s was removed.", family, name), change), nil
}

// nftChainSpec renders a base chain's `{ ... }` body, or "" for a plain
// chain.
func nftChainSpec(args *value.Map) (string, error) {
	hook := strings.ToLower(strings.TrimSpace(states.Str(args, "hook", "")))
	if hook == "" {
		return "", nil
	}
	typ := strings.TrimSpace(states.Str(args, "type", "filter"))
	prio := strings.TrimSpace(states.Str(args, "priority", "0"))
	policy := strings.TrimSpace(states.Str(args, "policy", "accept"))
	if policy != "accept" && policy != "drop" {
		return "", fmt.Errorf("a base chain's policy is accept or drop, not %q", policy)
	}
	return fmt.Sprintf("{ type %s hook %s priority %s ; policy %s ; }", typ, hook, prio, policy), nil
}

func nftChainChange(c *exec.Context, args *value.Map, present bool) (any, error) {
	family := nftFamily(args)
	table := strings.TrimSpace(states.Str(args, "table", ""))
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if table == "" || name == "" {
		return nil, errors.New("a chain needs a name and a table")
	}
	if err := nftCheckFamily(family); err != nil {
		return nil, err
	}
	rs, err := nftListRaw(c, "ruleset")
	if err != nil {
		return nil, err
	}
	if !nftFindTable(rs, family, table) {
		return nil, fmt.Errorf("table %s %s does not exist", family, table)
	}
	existing := nftFindChain(rs, family, table, name)

	if present {
		spec, err := nftChainSpec(args)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			// A base chain's policy is the one thing `add chain` will
			// change in place; a hook or type change is nft's to refuse.
			if spec != "" && existing.Base() {
				want := strings.TrimSpace(states.Str(args, "policy", "accept"))
				if existing.Policy == want {
					return nftMutateResult(c, false, fmt.Sprintf("chain %s %s %s already exists as declared.", family, table, name), nil), nil
				}
				change := value.MapOf(chainRef(family, table, name)+" policy", states.Change(existing.Policy, want))
				if c.Test {
					return nftMutateResult(c, true, fmt.Sprintf("chain %s %s %s policy would become %s.", family, table, name, want), change), nil
				}
				if err := nftApply(c, fmt.Sprintf("add chain %s %s %s %s\n", family, table, name, spec)); err != nil {
					return nil, err
				}
				return nftMutateResult(c, true, fmt.Sprintf("chain %s %s %s policy became %s.", family, table, name, want), change), nil
			}
			return nftMutateResult(c, false, fmt.Sprintf("chain %s %s %s already exists.", family, table, name), nil), nil
		}
		change := value.MapOf(chainRef(family, table, name), states.Change(nil, "present"))
		if c.Test {
			return nftMutateResult(c, true, fmt.Sprintf("chain %s %s %s would be created.", family, table, name), change), nil
		}
		line := fmt.Sprintf("add chain %s %s %s", family, table, name)
		if spec != "" {
			line += " " + spec
		}
		if err := nftApply(c, line+"\n"); err != nil {
			return nil, err
		}
		return nftMutateResult(c, true, fmt.Sprintf("chain %s %s %s was created.", family, table, name), change), nil
	}

	if existing == nil {
		return nftMutateResult(c, false, fmt.Sprintf("chain %s %s %s does not exist.", family, table, name), nil), nil
	}
	change := value.MapOf(chainRef(family, table, name), states.Change("present", nil))
	if c.Test {
		return nftMutateResult(c, true, fmt.Sprintf("chain %s %s %s would be removed.", family, table, name), change), nil
	}
	if err := nftApply(c, fmt.Sprintf("delete chain %s %s %s\n", family, table, name)); err != nil {
		return nil, err
	}
	return nftMutateResult(c, true, fmt.Sprintf("chain %s %s %s was removed.", family, table, name), change), nil
}

func chainRef(family, table, name string) string { return family + " " + table + " " + name }

func nftSetPolicyFn(c *exec.Context, args *value.Map) (any, error) {
	family := nftFamily(args)
	table := strings.TrimSpace(states.Str(args, "table", ""))
	name := strings.TrimSpace(states.Str(args, "name", ""))
	policy := strings.TrimSpace(states.Str(args, "policy", ""))
	if table == "" || name == "" {
		return nil, errors.New("set_policy needs a chain name and a table")
	}
	if policy != "accept" && policy != "drop" {
		return nil, fmt.Errorf("a policy is accept or drop, not %q", policy)
	}
	rs, err := nftListRaw(c, "ruleset")
	if err != nil {
		return nil, err
	}
	ch := nftFindChain(rs, family, table, name)
	if ch == nil {
		return nil, fmt.Errorf("chain %s does not exist", chainRef(family, table, name))
	}
	if !ch.Base() {
		return nil, fmt.Errorf("chain %s is not a base chain, so it has no policy", chainRef(family, table, name))
	}
	if ch.Policy == policy {
		return nftMutateResult(c, false, fmt.Sprintf("chain %s policy is already %s.", chainRef(family, table, name), policy), nil), nil
	}
	prio := ch.Prio
	if prio == "" {
		prio = "0"
	}
	spec := fmt.Sprintf("{ type %s hook %s priority %s ; policy %s ; }", ch.Type, ch.Hook, prio, policy)
	change := value.MapOf(chainRef(family, table, name)+" policy", states.Change(ch.Policy, policy))
	if c.Test {
		return nftMutateResult(c, true, fmt.Sprintf("chain %s policy would become %s.", chainRef(family, table, name), policy), change), nil
	}
	if err := nftApply(c, fmt.Sprintf("add chain %s %s %s %s\n", family, table, name, spec)); err != nil {
		return nil, err
	}
	return nftMutateResult(c, true, fmt.Sprintf("chain %s policy became %s.", chainRef(family, table, name), policy), change), nil
}

// nftRuleLine builds one `add rule` / `insert rule` script line.
func nftRuleLine(verb, family, table, chain, body, comment string, index int64) string {
	var b strings.Builder
	b.WriteString(verb)
	b.WriteString(" rule ")
	b.WriteString(family + " " + table + " " + chain + " ")
	if verb == "insert" && index >= 0 {
		fmt.Fprintf(&b, "index %d ", index)
	}
	b.WriteString(strings.TrimSpace(body))
	if comment != "" {
		fmt.Fprintf(&b, " comment %q", comment)
	}
	b.WriteString("\n")
	return b.String()
}

func nftAddRule(c *exec.Context, args *value.Map, mode string) (any, error) {
	family := nftFamily(args)
	table := strings.TrimSpace(states.Str(args, "table", ""))
	chain := strings.TrimSpace(states.Str(args, "chain", ""))
	body := strings.TrimSpace(states.Str(args, "rule", ""))
	comment := strings.TrimSpace(states.Str(args, "comment", ""))
	if table == "" || chain == "" {
		return nil, errors.New("a rule needs a table and a chain")
	}
	if body == "" {
		return nil, errors.New("the rule body is empty")
	}
	if err := nftCheckFamily(family); err != nil {
		return nil, err
	}

	var warns []string
	if comment != "" {
		rs, err := nftListRaw(c, "chain", family, table, chain)
		if err != nil {
			return nil, err
		}
		if nftFindRuleByComment(rs, family, table, chain, comment) != nil {
			return nftMutateResult(c, false,
				fmt.Sprintf("a rule with comment %q is already in %s.", comment, chainRef(family, table, chain)), nil), nil
		}
	} else {
		warns = append(warns,
			"this rule has no comment, so it was appended without an idempotence check; a second run adds it again")
	}

	index := int64(-1)
	if mode == "insert" {
		index = states.Int(args, "index", -1)
	}
	line := nftRuleLine(map[string]string{"append": "add", "insert": "insert"}[mode], family, table, chain, body, comment, index)
	shown := strings.TrimSpace(body)
	if comment != "" {
		shown += " (" + comment + ")"
	}
	change := value.MapOf(shown, states.Change(nil, "present in "+chainRef(family, table, chain)))
	if c.Test {
		return nftMutateResult(c, true, fmt.Sprintf("`%s` would be added to %s.", strings.TrimSpace(body), chainRef(family, table, chain)), change, warns...), nil
	}
	if err := nftApply(c, line); err != nil {
		return nil, err
	}
	return nftMutateResult(c, true, fmt.Sprintf("`%s` was added to %s.", strings.TrimSpace(body), chainRef(family, table, chain)), change, warns...), nil
}

func nftDeleteRule(c *exec.Context, args *value.Map) (any, error) {
	family := nftFamily(args)
	table := strings.TrimSpace(states.Str(args, "table", ""))
	chain := strings.TrimSpace(states.Str(args, "chain", ""))
	comment := strings.TrimSpace(states.Str(args, "comment", ""))
	handle := states.Int(args, "handle", 0)
	if table == "" || chain == "" {
		return nil, errors.New("a rule needs a table and a chain")
	}
	if comment == "" && handle == 0 {
		return nil, errors.New("name the rule to remove by its comment or its handle")
	}
	if err := nftCheckFamily(family); err != nil {
		return nil, err
	}

	if handle == 0 {
		rs, err := nftListRaw(c, "chain", family, table, chain)
		if err != nil {
			return nil, err
		}
		ru := nftFindRuleByComment(rs, family, table, chain, comment)
		if ru == nil {
			return nftMutateResult(c, false,
				fmt.Sprintf("no rule with comment %q is in %s.", comment, chainRef(family, table, chain)), nil), nil
		}
		handle = ru.Handle
	}
	label := comment
	if label == "" {
		label = fmt.Sprintf("handle %d", handle)
	}
	change := value.MapOf(label, states.Change("present in "+chainRef(family, table, chain), nil))
	if c.Test {
		return nftMutateResult(c, true, fmt.Sprintf("the rule %s would be removed from %s.", label, chainRef(family, table, chain)), change), nil
	}
	if err := nftApply(c, fmt.Sprintf("delete rule %s %s %s handle %d\n", family, table, chain, handle)); err != nil {
		return nil, err
	}
	return nftMutateResult(c, true, fmt.Sprintf("the rule %s was removed from %s.", label, chainRef(family, table, chain)), change), nil
}

func nftFlushFn(c *exec.Context, args *value.Map) (any, error) {
	family := nftFamily(args)
	table := strings.TrimSpace(states.Str(args, "table", ""))
	chain := strings.TrimSpace(states.Str(args, "chain", ""))
	force := states.Bool(args, "force", false)
	if chain != "" && table == "" {
		return nil, errors.New("flushing a chain needs its table")
	}

	rs, err := nftListRaw(c, "ruleset")
	if err != nil {
		return nil, err
	}

	switch {
	case chain != "":
		ch := nftFindChain(rs, family, table, chain)
		if ch == nil {
			return nftMutateResult(c, false, fmt.Sprintf("chain %s does not exist.", chainRef(family, table, chain)), nil), nil
		}
		if ch.Base() && ch.Policy == "drop" && !force {
			return nil, fmt.Errorf(
				"chain %s is a base chain with policy drop: flushing it strips the rules that are letting traffic through. "+
					"Pass force if that is what you mean", chainRef(family, table, chain))
		}
		n := nftRuleCount(rs, family, table, chain)
		if n == 0 {
			return nftMutateResult(c, false, fmt.Sprintf("chain %s is already empty.", chainRef(family, table, chain)), nil), nil
		}
		change := value.MapOf(chainRef(family, table, chain), states.Change(fmt.Sprintf("%d rule(s)", n), "empty"))
		if c.Test {
			return nftMutateResult(c, true, fmt.Sprintf("chain %s would be flushed (%d rule(s)).", chainRef(family, table, chain), n), change), nil
		}
		if err := nftApply(c, fmt.Sprintf("flush chain %s %s %s\n", family, table, chain)); err != nil {
			return nil, err
		}
		return nftMutateResult(c, true, fmt.Sprintf("chain %s was flushed (%d rule(s)).", chainRef(family, table, chain), n), change), nil

	case table != "":
		if !force {
			return nil, fmt.Errorf("flushing a whole table empties every chain in it; pass force")
		}
		if !nftFindTable(rs, family, table) {
			return nftMutateResult(c, false, fmt.Sprintf("table %s %s does not exist.", family, table), nil), nil
		}
		change := value.MapOf(family+" "+table, states.Change("rules", "empty"))
		if c.Test {
			return nftMutateResult(c, true, fmt.Sprintf("table %s %s would be flushed.", family, table), change), nil
		}
		if err := nftApply(c, fmt.Sprintf("flush table %s %s\n", family, table)); err != nil {
			return nil, err
		}
		return nftMutateResult(c, true, fmt.Sprintf("table %s %s was flushed.", family, table), change), nil

	default:
		if !force {
			return nil, errors.New("flushing the whole ruleset wipes every table on the node, including ones this build did not write; pass force")
		}
		change := value.MapOf("ruleset", states.Change("present", "empty"))
		if c.Test {
			return nftMutateResult(c, true, "the whole ruleset would be flushed.", change), nil
		}
		if err := nftApply(c, "flush ruleset\n"); err != nil {
			return nil, err
		}
		return nftMutateResult(c, true, "the whole ruleset was flushed.", change), nil
	}
}

func nftRuleCount(rs *nftRuleset, family, table, chain string) int {
	n := 0
	for _, ru := range rs.Rules {
		if ru.Family == family && ru.Table == table && ru.Chain == chain {
			n++
		}
	}
	return n
}

// NFTablesSavePath is where `save` writes. A variable so a test can
// redirect it.
var NFTablesSavePath = "/etc/nftables.conf"

func nftSaveFn(c *exec.Context, args *value.Map) (any, error) {
	if err := nftToolPresent(c); err != nil {
		return nil, err
	}
	path := strings.TrimSpace(states.Str(args, "path", ""))
	if path == "" {
		path = NFTablesSavePath
	}
	res, err := c.Run(exec.Command{Argv: []string{"nft", "list", "ruleset"}, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("`nft list ruleset` could not be run: %w", err)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("`nft list ruleset` exited %d: %s", res.Code, strings.TrimSpace(firstLine(res.Stderr)))
	}
	// The restore file flushes first so a boot-time reload is not additive
	// on top of whatever an initramfs left.
	want := "#!/usr/sbin/nft -f\n\nflush ruleset\n\n" + strings.TrimRight(res.Stdout, "\n") + "\n"

	existing, _ := os.ReadFile(path)
	if string(existing) == want {
		return nftMutateResult(c, false, fmt.Sprintf("%s already holds the running ruleset.", path), nil), nil
	}
	change := value.MapOf(path, states.Change("stale or absent", "the running ruleset"))
	if c.Test {
		return nftMutateResult(c, true, fmt.Sprintf("the running ruleset would be written to %s.", path), change), nil
	}
	if err := atomicfile.Write(path, []byte(want), 0o600); err != nil {
		return nil, fmt.Errorf("%s could not be written: %w", path, err)
	}
	return nftMutateResult(c, true, fmt.Sprintf("the running ruleset was written to %s.", path), change), nil
}

func nftCheckFn(c *exec.Context, args *value.Map) (any, error) {
	if err := nftToolPresent(c); err != nil {
		return nil, err
	}
	out := value.NewMap(2)

	if body := strings.TrimSpace(states.Str(args, "rule", "")); body != "" {
		family := nftFamily(args)
		table := strings.TrimSpace(states.Str(args, "table", ""))
		chain := strings.TrimSpace(states.Str(args, "chain", ""))
		if table == "" || chain == "" {
			return nil, errors.New("a syntax check needs a table and a chain for nft to resolve the rule against")
		}
		res, err := c.Run(exec.Command{
			Argv:           []string{"nft", "--check", "-f", "-"},
			Stdin:          fmt.Sprintf("add rule %s %s %s %s\n", family, table, chain, body),
			IgnoreExitCode: true,
		})
		if err != nil {
			return nil, fmt.Errorf("`nft --check` could not be run: %w", err)
		}
		out.Set("valid", res.Code == 0)
		if res.Code != 0 {
			out.Set("error", strings.TrimSpace(res.Stderr+res.Stdout))
		}
	}

	if comment := strings.TrimSpace(states.Str(args, "comment", "")); comment != "" {
		family := nftFamily(args)
		table := strings.TrimSpace(states.Str(args, "table", ""))
		chain := strings.TrimSpace(states.Str(args, "chain", ""))
		if table == "" || chain == "" {
			return nil, errors.New("a comment check needs a table and a chain")
		}
		rs, err := nftListRaw(c, "chain", family, table, chain)
		if err != nil {
			return nil, err
		}
		out.Set("present", nftFindRuleByComment(rs, family, table, chain, comment) != nil)
	}
	return out, nil
}

// ---- states ----

func nftTableState(c *exec.Context, args *value.Map, present bool) (states.Result, error) {
	if _, ok := args.Get("name"); !ok {
		args.Set("name", states.Str(args, "name", ""))
	}
	out, err := nftTableChange(c, args, present)
	return nftStateFromMutate(c, out, err)
}

func nftChainState(c *exec.Context, args *value.Map, present bool) (states.Result, error) {
	out, err := nftChainChange(c, args, present)
	return nftStateFromMutate(c, out, err)
}

func nftSetPolicyState(c *exec.Context, args *value.Map) (states.Result, error) {
	out, err := nftSetPolicyFn(c, args)
	return nftStateFromMutate(c, out, err)
}

func nftRuleState(c *exec.Context, args *value.Map, mode string) (states.Result, error) {
	if strings.TrimSpace(states.Str(args, "comment", "")) == "" {
		return states.False("a comment is required: it is the rule's identity, and without one a state cannot be idempotent"), nil
	}
	var (
		out any
		err error
	)
	if mode == "delete" {
		out, err = nftDeleteRule(c, args)
	} else {
		out, err = nftAddRule(c, args, mode)
	}
	return nftStateFromMutate(c, out, err)
}

func nftFlushState(c *exec.Context, args *value.Map) (states.Result, error) {
	// The state's `name` is the chain, unless it looks like a bare table
	// and `table` was not given.
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if strings.TrimSpace(states.Str(args, "table", "")) == "" {
		// name is a table.
		args.Set("table", name)
	} else if _, ok := args.Get("chain"); !ok {
		args.Set("chain", name)
	}
	out, err := nftFlushFn(c, args)
	return nftStateFromMutate(c, out, err)
}

// nftStateFromMutate turns an execution-style result map into a
// states.Result, honouring test mode.
func nftStateFromMutate(c *exec.Context, out any, err error) (states.Result, error) {
	if err != nil {
		return states.False(err.Error()), nil
	}
	m, ok := out.(*value.Map)
	if !ok {
		return states.False("the module returned an unexpected shape"), nil
	}
	changed, _ := m.GetString("changed")
	cv, _ := m.GetString("comment")
	comment, _ := cv.(string)
	if comment == "" {
		comment = "done"
	}
	csv, _ := m.GetString("changes")
	changes, _ := csv.(*value.Map)
	res := states.True(comment)
	if changed == true {
		if c.Test {
			res = states.WouldChange(comment, changes)
		} else {
			res = states.Changed(comment, changes)
		}
	}
	if wv, ok := m.GetString("warnings"); ok {
		if list, ok := wv.([]any); ok {
			for _, w := range list {
				res.Warnings = append(res.Warnings, value.KeyString(w))
			}
		}
	}
	return res, nil
}
