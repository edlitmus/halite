package config

import "sort"

// InertKeys are the settings this build parses, validates, accepts — and
// then does nothing with.
//
// Every one of them was waived in the unread-key audit with a reason
// naming a phase that has since shipped: "phase 2: there is no job
// cache" on a build where phase 2 landed. The waiver stopped being true
// and nothing said so, and because `IsKnownKey` accepts the name, an
// operator who set one got silence rather than a warning. Silence from a
// configuration loader reads as acceptance.
//
// The value is what an operator actually gets, in place of what they
// asked for. The SPEC section is not repeated here: the key table
// already carries one, and two of them would drift.
var InertKeys = map[string]string{
	"tracing":           "no spans are emitted, and nothing is exported",
	"ext_pillar_fail":   "external pillar is not built, so there is no failure for this to govern",
	"pillar_cache_disk": "pillar is never written to disk; the encrypted cache needs the encryption stack, which this build does not have",
	"legacy_acl":        "the preserved Salt ACL is kept for you to read and is never consulted; convert it into `policy`",
	"parallel_jobs":     "a node runs the jobs it is given without limiting how many at once",
	"socket_dir":        "this build opens no unix sockets",
	"quiesce":           "a node accepts jobs whatever this says",
	"quiesce_allowlist": "a node accepts jobs whatever this says",
	"startup_states":    "a node applies nothing of its own at startup",
	"job_cache":         "the job cache backend is not selectable; the hub keeps returns on disk",
	"node_data_cache":   "the node data cache backend is not selectable; the hub keeps it on disk",
	"hub_type":          "the first hub in the list is used, and no other is tried",
}

// InertWarning is one setting that was asked for and will not happen.
type InertWarning struct {
	// Setting is the key as the operator wrote it.
	Setting string
	// Effect is what they get instead.
	Effect string
	// Section is where SPEC describes what they asked for.
	Section string
}

// InertWarnings reports every inert setting this configuration sets, in
// order, so a service can say so at startup rather than leaving an
// operator to infer it from behaviour that never changes.
func (c *Config) InertWarnings() []InertWarning {
	var names []string
	for name := range InertKeys {
		if _, ok := c.Get(name); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	out := make([]InertWarning, 0, len(names))
	for _, name := range names {
		out = append(out, InertWarning{
			Setting: name,
			Effect:  InertKeys[name],
			Section: sectionOf(name),
		})
	}
	return out
}

// sectionOf reads the SPEC section out of the key table, which already
// carries one for every setting.
func sectionOf(name string) string {
	for _, k := range Keys {
		if k.Name == name {
			return k.Section
		}
	}
	return ""
}

// UnreadKeys are settings this build declares and does not read, for a
// reason other than being inert.
//
// The distinction from InertKeys is what an operator gets. An inert key
// is a request the services refuse out loud at startup. These are keys
// whose absence is not worth a warning: two wait on a phase that has not
// shipped, and three are settings whose single value is the only one
// this build has, so there is nothing to warn about.
//
// Kept here rather than in the audit's own test file so that one place
// says why a key is unread, and so the delivered-phase guard can read
// it. A reason naming a phase that has since shipped is an excuse that
// expired, which is how twelve settings came to be accepted in silence.
var UnreadKeys = map[string]string{
	"job_signer_keys":       "phase 6: detached job signing",
	"require_job_signature": "phase 6: detached job signing",

	"log_level_file": "SPEC 26.1's per-sink level; the file sink takes the global one",
	"regex_engine":   "re2 is the only engine, so the setting has one value",
	"node_id_source": "the resolution order of SPEC 7.2 is implemented; naming one source is not",
}
