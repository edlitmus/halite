package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A declared setting that nothing reads is the defect this project keeps
// finding in itself: `cmd_default_shell` was one, the per-state `timeout`
// was one, and the `salt` dispatcher was the same shape a layer down.
// Each was plumbed the whole way and never populated, so a tree could
// set it, `--help` could document it, the configuration reference could
// list it, and nothing would happen.
//
// This walks the other way: every key in Keys must be mentioned somewhere
// outside keys.go, or be listed below with the reason it is not.
//
// The list is enforced in both directions. A key that starts being read
// must come off it, because a stale entry here hides the next real one.
var unreadKeys = map[string]string{
	// Phase 2, still: what the transport carries rather than the
	// transport itself. Enrollment, the key store, and the subscribe
	// stream are built, so their settings have come off this list.
	"tracing":                     "phase 2: no spans are emitted yet",
	"metrics_listen":              "phase 2: no metrics endpoint yet",
	"event_tag_compat":            "phase 2: no events are emitted yet",
	"ext_pillar_fail":             "phase 2: external pillar is a hub concern",
	"file_ignore_glob":            "phase 2: the file server is a hub concern",
	"file_ignore_regex":           "phase 2: the file server is a hub concern",
	"fileserver_follow_symlinks":  "phase 2: the file server is a hub concern",
	"gitfs_base":                  "phase 5: gitfs",
	"gitfs_verify_signatures":     "phase 5: gitfs",
	"job_signer_keys":             "phase 6: detached job signing",
	"require_job_signature":       "phase 6: detached job signing",
	"extension_require_signature": "phase 5: bridged extensions",
	"pillar_cache_disk":           "phase 2: the node caches pillar from a hub",

	// Read today, and not yet acted on. Each is a live gap rather than a
	// phase boundary, and DIVERGENCE says so.
	"log_level_file": "SPEC 26.1's per-sink level; the file sink takes the global one",
	"regex_engine":   "re2 is the only engine, so the setting has one value",
	"node_id_source": "the resolution order of SPEC 7.2 is implemented; naming one source is not",
	"hash_type":      "phase 2: the file server compares digests; nothing here does",
	"policy":         "phase 2: RBAC is a hub concern",
	// These two are read through rootsFrom, which takes the key as an
	// argument rather than as a literal beside the accessor. The check
	// is deliberately strict; an exception with a reason is better than
	// a looser rule that lets a real one through.
	"file_roots":              "read through rootsFrom, which takes the key as an argument",
	"pillar_roots":            "read through rootsFrom, which takes the key as an argument",
	"extension_trust_keys":    "phase 5: bridged extensions",
	"grains_refresh_interval": "phase 2: a long-running node re-collects; a one-shot run does not",
	"legacy_acl":              "phase 2: RBAC is a hub concern",
	"parallel_jobs":           "phase 2: there is one job at a time",
	"reactor":                 "phase 3: the automation loop",
	"socket_dir":              "phase 2: there are no sockets",
	"quiesce":                 "phase 2: there are no jobs to refuse",
	"quiesce_allowlist":       "phase 2: there are no jobs to refuse",
	"accept_relays":           "phase 5: relays",
	"relay_upstream":          "phase 5: relays",
	"relay_upstream_port":     "phase 5: relays",
	"beacons":                 "phase 3: the automation loop",
	"schedule":                "phase 3: the scheduler",
	"mine_functions":          "phase 3: the mine",
	"mine_interval":           "phase 3: the mine",
	"returner":                "phase 4: returners",
	"startup_states":          "phase 2: a node with a hub applies at startup",
	"ext_pillar":              "phase 2: external pillar is a hub concern",
	"fileserver_backend":      "phase 2: the file server is a hub concern",
	"gitfs_env_allowlist":     "phase 5: gitfs",
	"gitfs_env_denylist":      "phase 5: gitfs",
	"job_cache":               "phase 2: there is no job cache",
	"node_data_cache":         "phase 2: the hub caches node data",
	"hub_type":                "phase 2: nothing dials a hub yet",
}

func TestEveryDeclaredKeyIsReadOrRecorded(t *testing.T) {
	root := ".."
	var body strings.Builder
	err := filepath.Walk(filepath.Join(root, ".."), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		// keys.go declares them and shim.go names the halite spelling a
		// Salt key maps onto. Neither reads one, and counting either as
		// a use is how `log_level_file` looked read when nothing reads
		// it.
		//
		// Tests are excluded for the same reason and it is the sharper
		// one: a test may set a key to prove the loader carries it,
		// which says nothing about whether anything acts on it.
		// `log_level` looked read because a configuration test mentions
		// it, and nothing in the node has ever consulted it.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		switch filepath.Base(path) {
		case "keys.go", "shim.go":
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		body.Write(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	text := body.String()

	var unread []string
	for _, k := range Keys {
		mentioned := readSomewhere(text, k.Name)
		reason, recorded := unreadKeys[k.Name]
		switch {
		case mentioned && recorded:
			t.Errorf("%q is read now, and is still recorded as unread (%q). Remove its row: "+
				"a stale entry hides the next setting that does nothing.", k.Name, reason)
		case !mentioned && !recorded:
			unread = append(unread, k.Name)
		}
	}
	sort.Strings(unread)
	for _, k := range unread {
		t.Errorf("%q is declared, documented, and read by nothing. Either act on it or "+
			"add it to unreadKeys with the reason.", k)
	}

	for name := range unreadKeys {
		if _, ok := keyIndex[name]; !ok {
			t.Errorf("unreadKeys names %q, which is not a declared setting", name)
		}
	}
}

// readSomewhere reports whether a key is passed to a configuration
// accessor anywhere.
//
// Looking for the bare string was wrong twice. A key mentioned in a test
// looked read, and a test that proves the loader carries a key says
// nothing about whether anything acts on it. And a *module parameter* of
// the same name looked like a read of the setting: `hash_type` is
// declared on `file.managed` and is a configuration key, and neither was
// read by anything.
func readSomewhere(text, key string) bool {
	for _, accessor := range []string{
		"String", "Bool", "OptionalBool", "Int", "Map", "StringSlice", "Roots", "Duration", "Get",
	} {
		if strings.Contains(text, fmt.Sprintf(".%s(%q", accessor, key)) {
			return true
		}
	}
	return false
}
