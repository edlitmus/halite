package builtin

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// mac_softwareupdate, SPEC section 15.3's macOS row.
//
// It drives `softwareupdate(8)`. SPEC 15.5 names no state for it, so it
// ships as execution functions; a tree that wants updates applied on a
// schedule reaches them from a `schedule` job or a reactor.
//
// **Three of Salt's functions describe a mechanism macOS removed.**
// `softwareupdate --ignore`, `--reset-ignored` and the per-update ignore
// list were deprecated years ago and are gone from the binary on current
// macOS — `softwareupdate --ignore` now answers "unrecognized option".
// The deferral of updates is an MDM control now (`com.apple.SoftwareUpdate`
// via a configuration profile), not a `softwareupdate` one. `ignore`,
// `list_ignored` and `reset_ignored` are registered so a tree carrying
// them from Salt gets that explanation rather than "unknown function",
// which reads as a typo — the same choice `win_registry`'s absent state
// and `apt_key` make.
//
// **The check schedule is a preference, not a subcommand.** `softwareupdate
// --schedule on|off` is also gone; `--schedule` with no argument still
// reports the state. Writing it means `AutomaticCheckEnabled` under
// `/Library/Preferences/com.apple.SoftwareUpdate`, which is what
// `schedule_enable` does — through the same path `mac_defaults` writes.
func registerMacSoftwareUpdate(r *Registries) {
	recommended := opt("recommended", signature.Bool, false,
		"Only the updates macOS marks Recommended.")
	restart := opt("restart", signature.Bool, false,
		"Pass -R, so the machine restarts or shuts down if an update needs it.")

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_softwareupdate", Function: "list_available",
				Doc: "Return the available updates as a mapping of label to version. " +
					"This scans Apple's update service unless `no_scan` is set.",
				Params: []signature.Param{
					recommended,
					opt("no_scan", signature.Bool, false, "Use the last scan's results instead of contacting Apple."),
				},
				Returns:   "a mapping of update label to version string",
				TestMode:  signature.TestNotApplicable,
				Platforms: macOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				ups, err := macSoftwareUpdateList(c, states.Bool(args, "no_scan", false))
				if err != nil {
					return nil, err
				}
				onlyRec := states.Bool(args, "recommended", false)
				out := value.NewMap(len(ups))
				for _, u := range ups {
					if onlyRec && !u.Recommended {
						continue
					}
					out.Set(u.Label, u.Version)
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_softwareupdate", Function: "update_available",
				Doc:       "Report whether an update with the given label is available.",
				Params:    []signature.Param{req("name", signature.String, "The update label.")},
				TestMode:  signature.TestNotApplicable,
				Platforms: macOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				ups, err := macSoftwareUpdateList(c, false)
				if err != nil {
					return nil, err
				}
				name := states.Str(args, "name", "")
				for _, u := range ups {
					if u.Label == name {
						return true, nil
					}
				}
				return false, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_softwareupdate", Function: "list_downloads",
				Doc:       "Return the labels of updates already downloaded to /Library/Updates.",
				TestMode:  signature.TestNotApplicable,
				Platforms: macOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return macSoftwareUpdateDownloads()
			},
		},
		macSoftwareUpdateActionModule("download", []string{"--download"}, recommended, restart, false),
		macSoftwareUpdateActionModule("download_all", []string{"--download", "--all"}, recommended, restart, true),
		macSoftwareUpdateActionModule("update", []string{"--install"}, recommended, restart, false),
		macSoftwareUpdateActionModule("update_all", []string{"--install", "--all"}, recommended, restart, true),
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_softwareupdate", Function: "schedule_enabled",
				Doc:       "Report whether macOS is set to check for updates automatically.",
				TestMode:  signature.TestNotApplicable,
				Platforms: macOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return macSoftwareUpdateScheduleEnabled(c)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_softwareupdate", Function: "schedule_enable",
				Doc: "Turn the automatic update check on or off. This writes " +
					"AutomaticCheckEnabled under /Library/Preferences/com.apple.SoftwareUpdate, " +
					"because `softwareupdate --schedule on|off` is gone from current macOS.",
				Params: []signature.Param{
					req("enabled", signature.Bool, "on or off."),
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
				err := macDefaultsWrite(c, macSoftwareUpdateDomain, "AutomaticCheckEnabled",
					"bool", states.Bool(args, "enabled", false), "")
				return err == nil, err
			},
		},
		macSoftwareUpdateGoneModule("ignore",
			"add an update to the per-update ignore list"),
		macSoftwareUpdateGoneModule("list_ignored",
			"read the per-update ignore list"),
		macSoftwareUpdateGoneModule("reset_ignored",
			"clear the per-update ignore list"),
	)
}

const macSoftwareUpdateDomain = "/Library/Preferences/com.apple.SoftwareUpdate"

// macSoftwareUpdateGoneModule is a function whose Salt behaviour macOS
// removed. It registers so the name resolves, and answers with why.
func macSoftwareUpdateGoneModule(fn, what string) exec.Module {
	return exec.Module{
		Sig: signature.Signature{
			Module: "mac_softwareupdate", Function: fn,
			Doc: "Refused: this would " + what + ", which `softwareupdate --ignore` did on " +
				"older macOS. That option was removed; deferring updates is an MDM control now.",
			TestMode:  signature.TestNotApplicable,
			Platforms: macOnly,
			Section:   "15.3",
		},
		Fn: func(c *exec.Context, args *value.Map) (any, error) {
			return nil, fmt.Errorf(
				"mac_softwareupdate.%s: `softwareupdate --ignore` and `--reset-ignored` were "+
					"removed from macOS; the per-update ignore list is gone. Deferring or "+
					"withholding an update is done through an MDM configuration profile "+
					"(com.apple.SoftwareUpdate) now, which is outside what this module drives",
				fn)
		},
	}
}

// macSoftwareUpdateActionModule builds a download/install function. `all`
// means it takes no label.
func macSoftwareUpdateActionModule(fn string, verb []string, recommended, restart signature.Param, all bool) exec.Module {
	params := []signature.Param{}
	if !all {
		params = append(params, req("name", signature.String, "The update label, as `list_available` returns it."))
	} else {
		params = append(params, recommended)
	}
	params = append(params, restart)

	doc := "Download update <name> without installing it."
	switch fn {
	case "download_all":
		doc = "Download every available update without installing."
	case "update":
		doc = "Install update <name>."
	case "update_all":
		doc = "Install every available update."
	}

	return exec.Module{
		Sig: signature.Signature{
			Module: "mac_softwareupdate", Function: fn,
			Doc:        doc,
			Params:     params,
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
			argv := append([]string{"softwareupdate"}, verb...)
			if all {
				if states.Bool(args, "recommended", false) {
					argv = append(argv, "--recommended")
				}
			} else {
				name := states.Str(args, "name", "")
				if name == "" {
					return nil, fmt.Errorf("mac_softwareupdate.%s needs an update label", fn)
				}
				argv = append(argv, name)
			}
			if states.Bool(args, "restart", false) {
				argv = append(argv, "--restart")
			}
			err := macRun(c, argv, strings.Join(argv, " "))
			return err == nil, err
		},
	}
}

// softwareUpdateEntry is one row of `softwareupdate --list`.
type softwareUpdateEntry struct {
	Label       string
	Title       string
	Version     string
	Recommended bool
	Action      string
}

// macSoftwareUpdateList runs `softwareupdate --list` and parses it.
func macSoftwareUpdateList(c *exec.Context, noScan bool) ([]softwareUpdateEntry, error) {
	if c.Which("softwareupdate") == "" {
		return nil, fmt.Errorf("mac_softwareupdate: `softwareupdate` was not found")
	}
	argv := []string{"softwareupdate", "--list"}
	if noScan {
		argv = append(argv, "--no-scan")
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	// `softwareupdate --list` exits 0 with "No new software available."
	// when there is nothing, and non-zero only on a real failure such as
	// no network. Treat a non-zero exit with no parsed rows as an error.
	entries := parseSoftwareUpdateList(res.Stdout + "\n" + res.Stderr)
	if res.Code != 0 && len(entries) == 0 {
		return nil, fmt.Errorf("softwareupdate --list: %s", firstLine(res.Stderr+res.Stdout))
	}
	return entries, nil
}

// parseSoftwareUpdateList reads the label/detail pairs out of
// `softwareupdate --list` output, in both the label-prefixed form
// current macOS prints and the older bare-star form.
func parseSoftwareUpdateList(out string) []softwareUpdateEntry {
	var entries []softwareUpdateEntry
	lines := strings.Split(out, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "* ") {
			continue
		}
		label := strings.TrimSpace(strings.TrimPrefix(line, "*"))
		label = strings.TrimSpace(strings.TrimPrefix(label, "Label:"))
		e := softwareUpdateEntry{Label: label}
		if i+1 < len(lines) {
			for _, field := range strings.Split(strings.TrimSpace(lines[i+1]), ",") {
				k, v, ok := strings.Cut(field, ":")
				if !ok {
					continue
				}
				k, v = strings.TrimSpace(k), strings.TrimSpace(v)
				switch strings.ToLower(k) {
				case "title":
					e.Title = v
				case "version":
					e.Version = v
				case "recommended":
					e.Recommended = strings.EqualFold(v, "YES")
				case "action":
					e.Action = v
				}
			}
		}
		entries = append(entries, e)
	}
	return entries
}

// macSoftwareUpdateDownloads reads the labels already staged in
// /Library/Updates.
func macSoftwareUpdateDownloads() ([]any, error) {
	b, err := os.ReadFile("/Library/Updates/index.plist")
	if err != nil {
		return []any{}, nil
	}
	return parseUpdatesIndex(b)
}

// parseUpdatesIndex reads the ProductPaths keys out of the
// /Library/Updates index plist.
func parseUpdatesIndex(b []byte) ([]any, error) {
	v, err := parsePlist(b)
	if err != nil {
		return nil, fmt.Errorf("/Library/Updates/index.plist: %w", err)
	}
	m, ok := v.(*value.Map)
	if !ok {
		return []any{}, nil
	}
	pm, ok := func() (*value.Map, bool) { p, _ := m.Get("ProductPaths"); x, ok := p.(*value.Map); return x, ok }()
	if !ok {
		return []any{}, nil
	}
	names := pm.StringKeys()
	sort.Strings(names)
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = n
	}
	return out, nil
}

// macSoftwareUpdateScheduleEnabled parses `softwareupdate --schedule`,
// whose read form still works.
func macSoftwareUpdateScheduleEnabled(c *exec.Context) (bool, error) {
	if c.Which("softwareupdate") == "" {
		return false, fmt.Errorf("mac_softwareupdate: `softwareupdate` was not found")
	}
	res, err := c.Run(exec.Command{Argv: []string{"softwareupdate", "--schedule"}, IgnoreExitCode: true})
	if err != nil {
		return false, err
	}
	if res.Code != 0 {
		return false, fmt.Errorf("softwareupdate --schedule: %s", firstLine(res.Stderr+res.Stdout))
	}
	low := strings.ToLower(res.Stdout)
	switch {
	case strings.Contains(low, "turned on"), strings.Contains(low, " on\n"), strings.HasSuffix(strings.TrimSpace(low), " on"):
		return true, nil
	default:
		return false, nil
	}
}
