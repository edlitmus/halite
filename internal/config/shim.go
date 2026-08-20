// Package config loads halite's configuration and translates a Salt
// configuration into it.
//
// The compatibility shim of SPEC sections 2.3 and 28.3 lives here. It is a
// table, it emits one warning per translated key per process start, and it
// carries a removal date. It is a defined, dated, removable surface, not
// an exception to the lexicon policy.
//
// One key is refused rather than translated. `auto_accept: True` has no
// equivalent and no configuration key produces it, because silently
// reproducing it would undo the enrollment rules of SPEC section 7.3.
package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/value"
)

// ShimRemovalVersion is the release in which the compatibility shim goes
// away. A shim without a removal date is permanent; SPEC section 33
// question 2 asks for the date, and this is the placeholder that makes the
// question visible in the code rather than only in the document.
const ShimRemovalVersion = "2.0.0"

// Rename is one Salt configuration key and what it becomes.
type Rename struct {
	Salt   string
	Halite string
	// Note explains a translation that is not a plain rename.
	Note string
}

// Refusal is a Salt key the shim declines to translate.
type Refusal struct {
	Salt   string
	Reason string
}

// renames is the mapping table. Both the shim and the generated
// documentation read it, so they cannot drift apart.
var renames = []Rename{
	{Salt: "master", Halite: "hub"},
	{Salt: "master_port", Halite: "hub_port"},
	{Salt: "master_finger", Halite: "hub_fingerprint"},
	{Salt: "master_type", Halite: "hub_type"},
	{Salt: "master_alive_interval", Halite: "hub_alive_interval"},
	{Salt: "master_tries", Halite: "hub_tries"},
	{Salt: "id", Halite: "node_id"},
	{Salt: "minion_id_caching", Halite: "node_id_caching"},
	{Salt: "minion_id_lowercase", Halite: "node_id_lowercase"},
	{Salt: "minion_id_remove_domain", Halite: "node_id_remove_domain"},
	{Salt: "minion_data_cache", Halite: "node_data_cache"},
	{Salt: "state_whitelist", Halite: "state_allowlist"},
	{Salt: "state_blacklist", Halite: "state_denylist"},
	{Salt: "gitfs_saltenv_whitelist", Halite: "gitfs_env_allowlist"},
	{Salt: "gitfs_saltenv_blacklist", Halite: "gitfs_env_denylist"},
	{Salt: "saltenv", Halite: "env", Note: "saltenv remains a permanent alias in templates and targeting"},
	{Salt: "environment", Halite: "env"},
	{Salt: "saltenv_whitelist", Halite: "env_allowlist"},
	{Salt: "saltenv_blacklist", Halite: "env_denylist"},
	{Salt: "minion_blackout", Halite: "quiesce"},
	{Salt: "minion_blackout_whitelist", Halite: "quiesce_allowlist"},
	{Salt: "syndic_master", Halite: "relay_upstream"},
	{Salt: "syndic_master_port", Halite: "relay_upstream_port"},
	{Salt: "order_masters", Halite: "accept_relays"},
	{Salt: "publisher_acl", Halite: "policy", Note: "translated into RBAC rules; review the result"},
	{Salt: "publisher_acl_blacklist", Halite: "policy", Note: "translated into RBAC rules; review the result"},
	{Salt: "external_auth", Halite: "policy", Note: "translated into RBAC rules; review the result"},
	{Salt: "peer", Halite: "policy", Note: "peer access is expressed in RBAC and is deny by default"},
	{Salt: "peer_run", Halite: "policy", Note: "peer access is expressed in RBAC and is deny by default"},
	{Salt: "client_acl", Halite: "policy", Note: "translated into RBAC rules; review the result"},
	{Salt: "master_job_cache", Halite: "job_cache"},
	{Salt: "cachedir", Halite: "cache_dir"},
	{Salt: "pki_dir", Halite: "pki_dir"},
	{Salt: "sock_dir", Halite: "socket_dir"},
	{Salt: "conf_file", Halite: "config_file"},
	{Salt: "jinja_trim_blocks", Halite: "template_trim_blocks"},
	{Salt: "jinja_lstrip_blocks", Halite: "template_lstrip_blocks"},
	{Salt: "renderer", Halite: "renderer"},
	{Salt: "top_file_merging_strategy", Halite: "top_file_merging_strategy"},
	{Salt: "pillar_source_merging_strategy", Halite: "pillar_source_merging_strategy"},
	{Salt: "pillar_merge_lists", Halite: "pillar_merge_lists"},
	{Salt: "pillar_opts", Halite: "pillar_opts"},
	{Salt: "log_level_logfile", Halite: "log_level_file"},
	{Salt: "hash_type", Halite: "hash_type"},
	{Salt: "fileserver_backend", Halite: "fileserver_backend"},
	{Salt: "file_roots", Halite: "file_roots"},
	{Salt: "pillar_roots", Halite: "pillar_roots"},
	{Salt: "ext_pillar", Halite: "ext_pillar"},
	{Salt: "nodegroups", Halite: "nodegroups"},
	{Salt: "mine_functions", Halite: "mine_functions"},
	{Salt: "mine_interval", Halite: "mine_interval"},
	{Salt: "schedule", Halite: "schedule"},
	{Salt: "beacons", Halite: "beacons"},
	{Salt: "reactor", Halite: "reactor"},
	{Salt: "grains", Halite: "grains"},
	{Salt: "returner", Halite: "returner"},
	{Salt: "startup_states", Halite: "startup_states"},
	{Salt: "failhard", Halite: "failhard"},
	{Salt: "test", Halite: "test"},
}

// refusals are the keys the shim declines to be helpful about.
var refusals = []Refusal{
	{
		Salt: "auto_accept",
		Reason: "halite has no equivalent and no configuration key produces one. " +
			"Set enrollment_mode to token or attested, both of which are accountable. SPEC section 7.3.",
	},
	{
		Salt:   "open_mode",
		Reason: "there is no unauthenticated mode. SPEC section 6.1.",
	},
	{
		Salt:   "transport",
		Reason: "the transport is HTTP/2 over mutual TLS 1.3 and is not configurable. SPEC section 6.1.",
	},
	{
		Salt:   "permissive_pki_access",
		Reason: "key material modes are fixed. SPEC section 7.1.",
	},
}

// Renames returns the mapping table, sorted, for documentation generation
// and for the migration report.
func Renames() []Rename {
	out := make([]Rename, len(renames))
	copy(out, renames)
	sort.Slice(out, func(i, j int) bool { return out[i].Salt < out[j].Salt })
	return out
}

// Refusals returns the refused keys and why.
func Refusals() []Refusal {
	out := make([]Refusal, len(refusals))
	copy(out, refusals)
	sort.Slice(out, func(i, j int) bool { return out[i].Salt < out[j].Salt })
	return out
}

var renameIndex = func() map[string]Rename {
	m := make(map[string]Rename, len(renames))
	for _, r := range renames {
		m[r.Salt] = r
	}
	return m
}()

var refusalIndex = func() map[string]Refusal {
	m := make(map[string]Refusal, len(refusals))
	for _, r := range refusals {
		m[r.Salt] = r
	}
	return m
}()

// ShimResult records what the shim did to one configuration.
type ShimResult struct {
	// Translated maps each Salt key that was renamed to its new name.
	Translated []Rename
	// Refused lists keys the shim declined to honour.
	Refused []Refusal
	// Unknown lists keys that are neither halite keys nor in the table.
	Unknown []string
}

// Warnings renders the result as the deprecation lines the process emits
// once at start.
func (s ShimResult) Warnings() []string {
	var out []string
	for _, r := range s.Translated {
		msg := fmt.Sprintf("configuration key %q is the Salt name; halite calls it %q. The compatibility shim is removed in %s.",
			r.Salt, r.Halite, ShimRemovalVersion)
		if r.Note != "" {
			msg += " " + r.Note + "."
		}
		out = append(out, msg)
	}
	for _, u := range s.Unknown {
		out = append(out, fmt.Sprintf("configuration key %q is not recognised and was ignored", u))
	}
	return out
}

// Err reports the refusals as an error, since a refused key means the
// operator asked for something the design does not provide and must be
// told rather than have it silently dropped.
func (s ShimResult) Err() error {
	if len(s.Refused) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("this configuration uses keys halite refuses to translate:")
	for _, r := range s.Refused {
		fmt.Fprintf(&b, "\n  %s: %s", r.Salt, r.Reason)
	}
	return fmt.Errorf("%s", b.String())
}

// ApplyShim rewrites Salt configuration keys into halite's names, in
// place on a copy, and reports what it did. A key already in halite's
// vocabulary passes through untouched.
//
// A key that maps onto `policy` is not merged into the RBAC policy here:
// that translation needs review, so the shim records the key under
// `legacy_acl` and the migration tool of SPEC section 28.5 produces a
// draft policy from it.
func ApplyShim(in *value.Map, known func(string) bool) (*value.Map, ShimResult) {
	var res ShimResult
	out := value.NewMap(in.Len())
	legacy := value.NewMap(0)

	for _, e := range in.Entries() {
		key, ok := e.Key.(string)
		if !ok {
			out.SetAt(e.Key, e.Val, e.KeyPos, e.ValPos)
			continue
		}

		if r, refused := refusalIndex[key]; refused {
			res.Refused = append(res.Refused, r)
			continue
		}

		// A key halite already knows wins over a rename that happens to
		// share its spelling, so `file_roots` does not report itself as
		// translated.
		if known != nil && known(key) {
			out.SetAt(e.Key, e.Val, e.KeyPos, e.ValPos)
			continue
		}

		r, renamed := renameIndex[key]
		if !renamed {
			res.Unknown = append(res.Unknown, key)
			out.SetAt(e.Key, e.Val, e.KeyPos, e.ValPos)
			continue
		}
		if r.Halite == r.Salt {
			out.SetAt(e.Key, e.Val, e.KeyPos, e.ValPos)
			continue
		}

		res.Translated = append(res.Translated, r)
		if r.Halite == "policy" {
			legacy.SetAt(key, e.Val, e.KeyPos, e.ValPos)
			continue
		}
		if out.Has(r.Halite) {
			// The halite spelling was also present; it wins, and the Salt
			// spelling is reported rather than silently dropped.
			continue
		}
		out.SetAt(r.Halite, e.Val, e.KeyPos, e.ValPos)
	}

	if legacy.Len() > 0 {
		out.Set("legacy_acl", legacy)
	}
	return out, res
}
