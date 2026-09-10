package builtin

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// mac_assistive, the last of SPEC section 15.3's macOS row, and Salt's
// `assistive`. It manages which applications may drive the machine
// through the Accessibility API — the list System Settings shows under
// Privacy & Security ▸ Accessibility. SPEC 15.5 names no state for it,
// so it ships as execution functions: a tree that wants an application
// present reaches `install` from `module.run` with an `unless` that
// calls `installed` or `enabled`.
//
// # It writes TCC.db directly, and that database is SIP-protected
//
// The grants live in the `access` table of
// `/Library/Application Support/com.apple.TCC/TCC.db`, keyed by the
// service string `kTCCServiceAccessibility`. There is no supported tool
// that edits it — `tccutil` only *resets* entries — so this module does
// what Salt's does and drives `sqlite3(1)` against the file.
//
// On a modern macOS that database is protected by System Integrity
// Protection. Reading it is allowed; writing it is refused for every
// process that has not been granted Full Disk Access, root included, and
// the refusal comes back from `sqlite3` as "attempt to write a readonly
// database" or "unable to open database file". `install`, `enable` and
// `remove` translate that into an error that names Full Disk Access,
// because the underlying message does not. A tree that runs this is
// expected to have granted the agent binary Full Disk Access out of
// band; there is no way for the module to grant it to itself.
//
// # It targets the modern schema, where Salt's module does not
//
// Salt's `assistive` still queries an `allowed` column and writes `1` or
// `0` to it. That column was renamed `auth_value` in macOS 10.15 and the
// values it takes are an enumeration, not a flag: `0` is denied, `2` is
// allowed. Salt's module therefore fails to read anything on any macOS
// from Catalina on. This one reads and writes `auth_value` with the `2`
// / `0` meaning, matching what System Settings itself records
// (`auth_reason` 4, "user set", `auth_version` 1), so it works on the
// versions Salt's does not and does not work on the ones predating the
// rename — which are all long out of support. DIVERGENCE 5.46.
//
// # client_type is inferred
//
// An `access` row is keyed by a client that is either a bundle
// identifier (`com.example.app`, `client_type` 0) or an absolute path to
// an executable (`/usr/local/bin/thing`, `client_type` 1). A leading `/`
// is what tells them apart, and is what this module keys on. Salt
// branches on a `.app` suffix instead, which a bundle identifier never
// has and a path to the binary inside a bundle also never has.
func registerMacAssistive(r *Registries) {
	appID := req("app_id", signature.String,
		"A bundle identifier (`com.example.app`) or an absolute path to an executable.")

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_assistive", Function: "list",
				Doc:       "Return every Accessibility grant, each as {client, client_type, enabled}.",
				Returns:   "a list of maps, one per client on the Accessibility list",
				TestMode:  signature.TestNotApplicable,
				Platforms: macOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				rows, err := macAssistiveList(c)
				if err != nil {
					return nil, err
				}
				out := make([]any, len(rows))
				for i, row := range rows {
					out[i] = map[string]any{
						"client":      row.Client,
						"client_type": row.ClientType,
						"enabled":     row.Enabled,
					}
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_assistive", Function: "installed",
				Doc:       "Report whether a client has an Accessibility entry at all, enabled or not.",
				Params:    []signature.Param{appID},
				Returns:   "true when the client is on the Accessibility list",
				TestMode:  signature.TestNotApplicable,
				Platforms: macOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				row, err := macAssistiveFind(c, states.Str(args, "app_id", ""))
				if err != nil {
					return nil, err
				}
				return row != nil, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_assistive", Function: "enabled",
				Doc:       "Report whether a client's Accessibility entry is present and allowed.",
				Params:    []signature.Param{appID},
				Returns:   "true when the client is on the list and its entry is allowed",
				TestMode:  signature.TestNotApplicable,
				Platforms: macOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				row, err := macAssistiveFind(c, states.Str(args, "app_id", ""))
				if err != nil {
					return nil, err
				}
				return row != nil && row.Enabled, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_assistive", Function: "install",
				Doc: "Add a client to the Accessibility list, or set the state of one already there. " +
					"Needs Full Disk Access as well as root; see the note above.",
				Params: []signature.Param{
					appID,
					opt("enable", signature.Bool, true, "Whether the new entry is allowed. Pass false to add it denied."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  macOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := macAssistiveInstall(c, states.Str(args, "app_id", ""), states.Bool(args, "enable", true))
				return err == nil, err
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_assistive", Function: "enable",
				Doc: "Set whether a client already on the Accessibility list is allowed. " +
					"Does nothing to a client that is not on the list.",
				Params: []signature.Param{
					appID,
					opt("enabled", signature.Bool, true, "The state to set: allowed when true, denied when false."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  macOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := macAssistiveEnable(c, states.Str(args, "app_id", ""), states.Bool(args, "enabled", true))
				return err == nil, err
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_assistive", Function: "remove",
				Doc:        "Delete a client's Accessibility entry. Tolerates a client that is not on the list.",
				Params:     []signature.Param{appID},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  macOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := macAssistiveRemove(c, states.Str(args, "app_id", ""))
				return err == nil, err
			},
		},
	)
}

// macAssistiveDB is the system TCC database. Accessibility is a
// system-wide service and has no per-user database.
const macAssistiveDB = "/Library/Application Support/com.apple.TCC/TCC.db"

// macAssistiveService is the row key every Accessibility grant carries.
const macAssistiveService = "kTCCServiceAccessibility"

// assistiveRow is one client's entry in the `access` table.
type assistiveRow struct {
	Client     string
	ClientType int
	// Enabled is auth_value == 2, the "allowed" value of the enumeration
	// that replaced the old boolean `allowed` column.
	Enabled bool
}

func macSqlite3Bin(c *exec.Context) string { return c.Which("sqlite3") }

func macAssistiveRequire(c *exec.Context) error {
	if macSqlite3Bin(c) == "" {
		return fmt.Errorf("mac_assistive: `sqlite3` was not found; this build's macOS Accessibility module needs it to read TCC.db")
	}
	return nil
}

// macAssistiveSQL runs one statement against TCC.db and returns stdout.
// A write that SIP refuses comes back here as a non-zero exit with a
// message about a readonly database or a database that will not open;
// macAssistiveWriteError turns that into something an operator can act
// on.
func macAssistiveSQL(c *exec.Context, sql string) (string, error) {
	if err := macAssistiveRequire(c); err != nil {
		return "", err
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"sqlite3", macAssistiveDB, sql},
		IgnoreExitCode: true,
	})
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", fmt.Errorf("sqlite3 TCC.db: %s", firstLine(res.Stderr+res.Stdout))
	}
	return res.Stdout, nil
}

// macAssistiveList reads every Accessibility grant.
func macAssistiveList(c *exec.Context) ([]assistiveRow, error) {
	out, err := macAssistiveSQL(c, "SELECT client, client_type, auth_value FROM access "+
		"WHERE service = '"+macAssistiveService+"' ORDER BY client")
	if err != nil {
		return nil, err
	}
	var rows []assistiveRow
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		// sqlite3's default output separates columns with '|'. A client
		// is a bundle id or a path, neither of which contains one.
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue
		}
		ct, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		av, _ := strconv.Atoi(strings.TrimSpace(parts[2]))
		rows = append(rows, assistiveRow{
			Client:     parts[0],
			ClientType: ct,
			Enabled:    av == 2,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Client < rows[j].Client })
	return rows, nil
}

// macAssistiveFind returns the row for one client, or nil when it has no
// entry.
func macAssistiveFind(c *exec.Context, appID string) (*assistiveRow, error) {
	if appID == "" {
		return nil, fmt.Errorf("mac_assistive: an app_id is required")
	}
	rows, err := macAssistiveList(c)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Client == appID {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// macAssistiveClientType is 1 for an absolute path to a binary, 0 for a
// bundle identifier. The leading slash is the whole of the test.
func macAssistiveClientType(appID string) int {
	if strings.HasPrefix(appID, "/") {
		return 1
	}
	return 0
}

// sqlQuote escapes a value for a single-quoted SQL string literal. The
// clients this takes are bundle ids and paths, but a caller controls the
// string and a stray quote should not change the statement.
func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// macAssistiveWriteError recognises the ways SIP refuses a write to
// TCC.db and rewrites them to name the cause.
func macAssistiveWriteError(verb string, err error) error {
	msg := err.Error()
	for _, sip := range []string{"readonly database", "unable to open database", "authorization denied", "not authorized"} {
		if strings.Contains(msg, sip) {
			return fmt.Errorf("mac_assistive.%s: TCC.db refused the write (%s). It is protected by "+
				"System Integrity Protection; the process needs Full Disk Access as well as root", verb, msg)
		}
	}
	return fmt.Errorf("mac_assistive.%s: %w", verb, err)
}

func macAssistiveInstall(c *exec.Context, appID string, enable bool) error {
	if appID == "" {
		return fmt.Errorf("mac_assistive.install: an app_id is required")
	}
	authValue := 0
	if enable {
		authValue = 2
	}
	// A named-column INSERT OR REPLACE: the columns left out are either
	// nullable or carry a schema default (last_modified, boot_uuid). The
	// auth_reason 4 / auth_version 1 pair is what System Settings writes
	// for a grant a person set by hand.
	sql := fmt.Sprintf("INSERT OR REPLACE INTO access "+
		"(service, client, client_type, auth_value, auth_reason, auth_version, indirect_object_identifier) "+
		"VALUES ('%s', %s, %d, %d, 4, 1, 'UNUSED')",
		macAssistiveService, sqlQuote(appID), macAssistiveClientType(appID), authValue)
	if _, err := macAssistiveSQL(c, sql); err != nil {
		return macAssistiveWriteError("install", err)
	}
	return nil
}

func macAssistiveEnable(c *exec.Context, appID string, enabled bool) error {
	if appID == "" {
		return fmt.Errorf("mac_assistive.enable: an app_id is required")
	}
	authValue := 0
	if enabled {
		authValue = 2
	}
	sql := fmt.Sprintf("UPDATE access SET auth_value = %d WHERE service = '%s' AND client = %s",
		authValue, macAssistiveService, sqlQuote(appID))
	if _, err := macAssistiveSQL(c, sql); err != nil {
		return macAssistiveWriteError("enable", err)
	}
	return nil
}

func macAssistiveRemove(c *exec.Context, appID string) error {
	if appID == "" {
		return fmt.Errorf("mac_assistive.remove: an app_id is required")
	}
	sql := fmt.Sprintf("DELETE FROM access WHERE service = '%s' AND client = %s",
		macAssistiveService, sqlQuote(appID))
	if _, err := macAssistiveSQL(c, sql); err != nil {
		return macAssistiveWriteError("remove", err)
	}
	return nil
}
