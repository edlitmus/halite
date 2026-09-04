package builtin

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerTimezone installs the timezone module of SPEC sections 15.2 and
// 15.5.
//
// The zone is named differently on the two families and this build does
// not pretend otherwise. A unix names it the way the tz database does —
// `America/Los_Angeles` — and Windows names it the way its own registry
// does — `Pacific Standard Time`. Salt carries a translation table of
// several hundred rows between the two; this does not, because the table
// is generated from CLDR data that moves, and one that has gone stale
// sets the wrong zone silently. `timezone.list_zones` returns the names
// the node in hand will actually accept, and a state naming a zone the
// node does not have is refused with that list one command away.
func registerTimezone(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "timezone", Function: "get_zone",
				Doc:      "Return this node's time zone.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return currentZone(c)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "timezone", Function: "set_zone",
				Doc: "Set this node's time zone.",
				Params: []signature.Param{
					req("timezone", signature.String, "The zone, named as this platform names it."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				want := states.Str(args, "timezone", "")
				if want == "" {
					return nil, fmt.Errorf("set_zone needs a zone")
				}
				if c.Test {
					return want, nil
				}
				if err := setZone(c, want); err != nil {
					return nil, err
				}
				return want, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "timezone", Function: "zone_compare",
				Doc: "Report whether this node's time zone is the one named.",
				Params: []signature.Param{
					req("timezone", signature.String, "The zone to compare against."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				have, err := currentZone(c)
				if err != nil {
					return nil, err
				}
				return sameZone(have, states.Str(args, "timezone", "")), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "timezone", Function: "list_zones",
				Doc:      "Return the time zone names this node will accept.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return listZones(c)
			},
		},
	)

	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "timezone", Function: "system",
			Doc: "Ensure this node's time zone is the one named.",
			Params: []signature.Param{
				nameParam("The zone, named as this platform names it. Defaults to the state ID."),
			},
			Mutates:    true,
			TestMode:   signature.TestReliable,
			Privileges: []string{"root"},
			Section:    "15.5",
		},
		Fn: timezoneSystem,
	})
}

func timezoneSystem(c *exec.Context, args *value.Map) (states.Result, error) {
	want := states.Str(args, "name", "")
	if want == "" {
		return states.False("This state needs a time zone."), nil
	}

	have, err := currentZone(c)
	if err != nil {
		return states.False(fmt.Sprintf("This node's time zone could not be read: %v", err)), nil
	}
	if sameZone(have, want) {
		return states.True(fmt.Sprintf("The time zone is already %s.", have)), nil
	}

	// A zone the node does not have is a state that cannot succeed, and
	// saying so in test mode is the point of test mode: the tool itself
	// would only say it on the real run.
	if zones, lerr := listZones(c); lerr == nil && len(zones) > 0 && !knownZone(zones, want) {
		return states.False(fmt.Sprintf(
			"This node has no time zone named %s; timezone.list_zones names the ones it has.", want)), nil
	}

	changes := value.MapOf("timezone", states.Change(have, want))
	if c.Test {
		return states.WouldChange(fmt.Sprintf(
			"The time zone would be set to %s, from %s.", want, have), changes), nil
	}
	if err := setZone(c, want); err != nil {
		return states.False(fmt.Sprintf("The time zone could not be set to %s: %v", want, err)), nil
	}
	return states.Changed(fmt.Sprintf("The time zone was set to %s, from %s.", want, have), changes), nil
}

// sameZone compares two zone names the way the platform that issued them
// does. Windows matches its zone names without regard to case; a tz
// database name is a path on a case-sensitive filesystem and is not.
func sameZone(a, b string) bool {
	if b == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func knownZone(zones []string, want string) bool {
	for _, z := range zones {
		if sameZone(z, want) {
			return true
		}
	}
	return false
}

// These are the files a unix keeps the answer in, as variables so that a
// test can point them at a tree it built.
var (
	zoneinfoDir     = "/usr/share/zoneinfo"
	localtimePath   = "/etc/localtime"
	etcTimezonePath = "/etc/timezone"
	// FreeBSD copies the zone file to /etc/localtime rather than linking
	// it, so the name is only recoverable from here.
	zoneNamePath = "/var/db/zoneinfo"
)

// currentZone returns the zone this node is running in.
func currentZone(c *exec.Context) (string, error) {
	if runtime.GOOS == "windows" {
		return windowsZone(c)
	}
	// The symlink is definitive where there is one: it is what the C
	// library reads, so it cannot disagree with the running zone.
	if target, err := os.Readlink(localtimePath); err == nil {
		if name := zoneFromPath(target); name != "" {
			return name, nil
		}
	}
	// FreeBSD, where /etc/localtime is a copy and this file is the name.
	if b, err := os.ReadFile(zoneNamePath); err == nil {
		if name := strings.TrimSpace(string(b)); name != "" {
			return name, nil
		}
	}
	// Debian, where this file is written alongside the link.
	if b, err := os.ReadFile(etcTimezonePath); err == nil {
		if name := strings.TrimSpace(string(b)); name != "" {
			return name, nil
		}
	}
	if c.Which("timedatectl") != "" {
		res, err := c.Run(exec.Command{
			Argv:           []string{"timedatectl", "show", "-p", "Timezone", "--value"},
			IgnoreExitCode: true,
		})
		if err == nil && res.Code == 0 {
			if name := strings.TrimSpace(res.Stdout); name != "" {
				return name, nil
			}
		}
	}
	// Nothing named the zone, so report the abbreviation the process is
	// running under. It is not a name set_zone would accept, and a state
	// comparing against it decides it has to act — which is the safe
	// direction to be wrong in.
	name, _ := time.Now().Zone()
	return name, nil
}

func windowsZone(c *exec.Context) (string, error) {
	res, err := c.Run(exec.Command{Argv: []string{"tzutil", "/g"}, IgnoreExitCode: true})
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", fmt.Errorf("tzutil /g: %s", firstLine(res.Stderr+res.Stdout))
	}
	return strings.TrimSpace(res.Stdout), nil
}

// zoneFromPath recovers a zone name from a path into the tz database.
func zoneFromPath(p string) string {
	p = filepath.ToSlash(p)
	if i := strings.LastIndex(p, "/zoneinfo/"); i >= 0 {
		return p[i+len("/zoneinfo/"):]
	}
	return ""
}

// setZone sets the running zone, and the zone the node will come back up
// in, which on every platform here are the same act.
func setZone(c *exec.Context, want string) error {
	switch runtime.GOOS {
	case "windows":
		res, err := c.Run(exec.Command{Argv: []string{"tzutil", "/s", want}, IgnoreExitCode: true})
		if err != nil {
			return err
		}
		if res.Code != 0 {
			return fmt.Errorf("%s", firstLine(res.Stderr+res.Stdout))
		}
		return nil
	case "darwin":
		_, err := c.Run(exec.Command{Argv: []string{"systemsetup", "-settimezone", want}})
		return err
	}
	// systemd's tool does the link, the /etc/timezone file and the
	// running clock together, and is the only supported way to do it on
	// a systemd node: a link written behind its back is reverted.
	if c.Which("timedatectl") != "" {
		res, err := c.Run(exec.Command{
			Argv:           []string{"timedatectl", "set-timezone", want},
			IgnoreExitCode: true,
		})
		if err != nil {
			return err
		}
		if res.Code == 0 {
			return nil
		}
		// timedatectl fails inside a container with no systemd running,
		// where writing the files directly is both possible and correct.
		if !strings.Contains(res.Stderr, "Failed to connect") &&
			!strings.Contains(res.Stderr, "not been booted") {
			return fmt.Errorf("timedatectl: %s", firstLine(res.Stderr))
		}
	}
	return linkZone(want)
}

// linkZone points /etc/localtime at the zone file by hand, which is what
// a node without systemd needs, and what a container gets.
func linkZone(want string) error {
	src := filepath.Join(zoneinfoDir, filepath.FromSlash(want))
	info, err := os.Stat(src)
	if err != nil || info.IsDir() {
		return fmt.Errorf("no time zone named %s under %s", want, zoneinfoDir)
	}

	// Replace it the way it was already held: FreeBSD ships a copy, and
	// turning that into a symlink is a change to the node nobody asked
	// for and one its own tzsetup would undo.
	wasLink := true
	if fi, err := os.Lstat(localtimePath); err == nil {
		wasLink = fi.Mode()&os.ModeSymlink != 0
	}
	if wasLink {
		if err := replaceSymlink(localtimePath, src); err != nil {
			return err
		}
	} else {
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := writeAtomic(localtimePath, b, 0o644); err != nil {
			return err
		}
	}

	// Both of these are caches of the name kept beside the zone data,
	// and are only rewritten where the platform already keeps one:
	// creating either on a node that has neither would be inventing a
	// file that nothing on it reads.
	for _, path := range []string{zoneNamePath, etcTimezonePath} {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := writeAtomic(path, []byte(want+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// replaceSymlink swaps the link atomically, so that a node interrupted
// mid-change has either the old zone or the new one and never no zone at
// all — /etc/localtime missing is UTC, silently.
func replaceSymlink(path, target string) error {
	tmp := path + ".halite-tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// listZones returns the zone names this node accepts.
func listZones(c *exec.Context) ([]string, error) {
	if runtime.GOOS == "windows" {
		return windowsZones(c)
	}
	if zones, err := zoneinfoNames(); err == nil && len(zones) > 0 {
		return zones, nil
	}
	if c.Which("timedatectl") != "" {
		res, err := c.Run(exec.Command{
			Argv:           []string{"timedatectl", "list-timezones", "--no-pager"},
			IgnoreExitCode: true,
		})
		if err == nil && res.Code == 0 {
			return nonEmptyLines(res.Stdout), nil
		}
	}
	return nil, fmt.Errorf("this node has no time zone database under %s", zoneinfoDir)
}

// windowsZones reads tzutil's listing, which alternates a display name
// with the identifier the tool takes back, separated by blank lines. The
// display name is localised and the identifier is not, so only the
// identifiers are returned.
func windowsZones(c *exec.Context) ([]string, error) {
	res, err := c.Run(exec.Command{Argv: []string{"tzutil", "/l"}, IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("tzutil /l: %s", firstLine(res.Stderr+res.Stdout))
	}
	var out []string
	for i, line := range nonEmptyLines(res.Stdout) {
		// The identifier is the second of each pair. A display name is
		// always parenthesised with its offset; an identifier never is.
		if i%2 == 1 && !strings.HasPrefix(line, "(") {
			out = append(out, line)
		}
	}
	return out, nil
}

// zoneinfoNames walks the tz database. The files beside the zones — the
// alternate trees and the plain-text tables — are excluded by name
// rather than by content, because reading several thousand files to
// decide would cost more than the rest of the state put together.
func zoneinfoNames() ([]string, error) {
	skipTop := map[string]bool{"posix": true, "right": true}
	skipFile := map[string]bool{
		"posixrules": true, "localtime": true, "Factory": true,
		"leapseconds": true, "leap-seconds.list": true, "SECURITY": true,
	}
	var out []string
	err := filepath.WalkDir(zoneinfoDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := zoneRelative(path)
		if name == "" {
			return nil
		}
		if skipTop[strings.SplitN(name, "/", 2)[0]] {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		// A zone name never carries a dot, and every table beside the
		// zones does: zone.tab, zone1970.tab, iso3166.tab, tzdata.zi.
		if d.IsDir() || skipFile[d.Name()] || strings.Contains(d.Name(), ".") {
			return nil
		}
		out = append(out, name)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func zoneRelative(path string) string {
	rel, err := filepath.Rel(zoneinfoDir, path)
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
