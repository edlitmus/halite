package builtin

import (
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerSnap installs the snap module of SPEC 15.3's Debian row, and
// the `snap.installed` and `snap.removed` states.
//
// It exists for the same reason `dpkg` does: the virtual `pkg` module
// is deliberately not this. snapd keeps its own database, so a snap is
// invisible to `pkg.list_pkgs` on the node it is running on — and on
// Ubuntu that is not a corner case. `lxd`, `core`, `firefox` and
// `chromium` are snaps on a stock 24.04, and an estate that manages
// packages and cannot see them is managing half a node.
//
// **Two things about snapd shape everything here.**
//
// First, snapd updates snaps by itself, four times a day by default,
// and that cannot be turned off — only deferred, up to 60 days at a
// time. So a version is not a thing a state can hold. `snap.installed`
// manages presence and the tracked channel and refuses to take a
// version, because a state that took one would report a change on the
// run after snapd moved it, then again, then again, for as long as
// anybody left it in the tree. DIVERGENCE 2.3 records that as a
// deliberate difference from what an operator coming from `pkg` expects.
//
// Second, `--classic` turns confinement off. A snap that needs it will
// not install without it, and the obvious convenience — noticing that
// and passing the flag — would quietly convert a confined install into
// an unconfined one on the strength of the store's say-so. It is
// declared in the tree or the install fails, and the failure says what
// the flag means.
func registerSnap(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "snap", Function: "list",
				Doc:       "Return every installed snap, with its version, revision and tracked channel.",
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				snaps, err := snapList(c)
				if err != nil {
					return nil, err
				}
				out := value.NewMap(len(snaps))
				for _, name := range sortedSnapNames(snaps) {
					out.Set(name, snaps[name].asMap())
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "snap", Function: "installed",
				Doc: "Return one snap's details, or nothing if it is not installed.",
				Params: []signature.Param{
					req("name", signature.String, "The snap."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				snaps, err := snapList(c)
				if err != nil {
					return nil, err
				}
				s, ok := snaps[states.Str(args, "name", "")]
				if !ok {
					return nil, nil
				}
				return s.asMap(), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "snap", Function: "list_upgrades",
				Doc:        "Return the snaps with a newer revision waiting in their channel.",
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return snapListUpgrades(c)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "snap", Function: "install",
				Doc: "Install a snap.",
				Params: []signature.Param{
					req("name", signature.String, "The snap."),
					opt("channel", signature.String, "", "The channel to track, such as latest/stable or 5.0/stable."),
					opt("classic", signature.Bool, false, "Install with classic confinement, which means no confinement. Required for a snap that asks for it; never inferred."),
					opt("devmode", signature.Bool, false, "Install in developer mode, which also means no confinement."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				o := snapOptionsFrom(args)
				name := strings.TrimSpace(states.Str(args, "name", ""))
				if name == "" {
					return nil, fmt.Errorf("snap.install needs a snap name")
				}
				if c.Test {
					return true, nil
				}
				return true, snapInstall(c, name, o)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "snap", Function: "remove",
				Doc: "Remove a snap.",
				Params: []signature.Param{
					req("name", signature.String, "The snap."),
					opt("purge", signature.Bool, false, "Discard the snapshot of the snap's data that snapd otherwise keeps."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				name := strings.TrimSpace(states.Str(args, "name", ""))
				if name == "" {
					return nil, fmt.Errorf("snap.remove needs a snap name")
				}
				if c.Test {
					return true, nil
				}
				return true, snapRemove(c, name, states.Bool(args, "purge", false))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "snap", Function: "refresh",
				Doc: "Refresh a snap, or every snap, and switch the tracked channel if one is given.",
				Params: []signature.Param{
					opt("name", signature.String, "", "The snap. Every snap if omitted."),
					opt("channel", signature.String, "", "Switch to this channel."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				return true, snapRefresh(c,
					strings.TrimSpace(states.Str(args, "name", "")),
					strings.TrimSpace(states.Str(args, "channel", "")))
			},
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "snap", Function: "installed",
				Doc: "Ensure a snap is installed, and tracking the channel it should.",
				Params: []signature.Param{
					nameParam("The snap. Defaults to the state ID."),
					opt("channel", signature.String, "", "The channel to track. Left alone if omitted."),
					opt("classic", signature.Bool, false, "Install with classic confinement, which means no confinement. Required for a snap that asks for it; never inferred."),
					opt("devmode", signature.Bool, false, "Install in developer mode, which also means no confinement."),
					// Declared so that it can be refused with a reason.
					// Leaving it out would have the signature answer
					// "is not a parameter of this function", which reads
					// as a typo — and a tree carrying `version:` from a
					// `pkg.installed` state is not a typo but an
					// assumption that does not survive snapd.
					opt("version", signature.String, "", "Refused, with the reason. snapd refreshes snaps by itself and no state can hold a version; track a channel instead."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: snapInstalledState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "snap", Function: "removed",
				Doc: "Ensure a snap is not installed.",
				Params: []signature.Param{
					nameParam("The snap. Defaults to the state ID."),
					opt("purge", signature.Bool, false, "Discard the snapshot of the snap's data that snapd otherwise keeps."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: snapRemovedState,
		},
	)
}

// snapInfo is one row of `snap list`.
type snapInfo struct {
	Name      string
	Version   string
	Revision  string
	Channel   string
	Publisher string
	Notes     string
}

func (s snapInfo) asMap() *value.Map {
	return value.MapOf(
		"version", s.Version,
		"revision", s.Revision,
		"channel", s.Channel,
		"publisher", s.Publisher,
		"notes", s.Notes,
		// Broken out because they are the two an operator asks about,
		// and reading them out of a comma-separated notes field is the
		// sort of thing a tree should not have to do.
		"classic", snapHasNote(s.Notes, "classic"),
		"disabled", snapHasNote(s.Notes, "disabled"),
	)
}

// snapHasNote reads one flag out of `snap list`'s Notes column, which is
// a comma-separated list and is `-` when there is nothing in it.
func snapHasNote(notes, want string) bool {
	for _, n := range strings.Split(notes, ",") {
		if strings.TrimSpace(n) == want {
			return true
		}
	}
	return false
}

// snapOptions are the confinement and channel choices an install carries.
type snapOptions struct {
	Channel string
	Classic bool
	Devmode bool
}

func snapOptionsFrom(args *value.Map) snapOptions {
	return snapOptions{
		Channel: strings.TrimSpace(states.Str(args, "channel", "")),
		Classic: states.Bool(args, "classic", false),
		Devmode: states.Bool(args, "devmode", false),
	}
}

// args renders the flags, in a fixed order so a test can assert the
// command rather than a set.
func (o snapOptions) args() []string {
	var out []string
	if o.Channel != "" {
		out = append(out, "--channel="+o.Channel)
	}
	if o.Classic {
		out = append(out, "--classic")
	}
	if o.Devmode {
		out = append(out, "--devmode")
	}
	return out
}

func haveSnap(c *exec.Context) error {
	if c.Which("snap") == "" {
		return fmt.Errorf("snapd is not installed on this node; the snap module needs it")
	}
	return nil
}

// snapList reads `snap list`.
func snapList(c *exec.Context) (map[string]snapInfo, error) {
	if err := haveSnap(c); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"snap", "list", "--color=never", "--unicode=never"},
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	// A node with snapd installed and nothing installed through it says
	// so on stderr and exits non-zero, which is not a failure: it is an
	// empty list, and treating it as an error would make `snap.list`
	// fail on exactly the node where the answer is least surprising.
	if res.Code != 0 {
		if strings.Contains(res.Stderr, "No snaps are installed") {
			return map[string]snapInfo{}, nil
		}
		return nil, fmt.Errorf("snap list: %s", firstLine(res.Stderr+res.Stdout))
	}
	return parseSnapList(res.Stdout), nil
}

// parseSnapList reads the table `snap list` prints:
//
//	Name    Version   Rev    Tracking       Publisher   Notes
//	core22  20240408  1380   latest/stable  canonical*  base
//	lxd     5.0.3     28373  5.0/stable     canonical*  -
//
// The columns are read from the header rather than assumed by position.
// snapd has changed this table — `Tracking` used to be `Channel`, and
// `Publisher` and `Notes` arrived later — so a parser that took the
// fourth field as the channel would read a publisher as one on an older
// node and report every snap as tracking `canonical*`.
//
// Splitting on whitespace is safe here in a way it is not for most
// tables: a snap name is `[a-z0-9-]`, a revision is a number or `x1`,
// and a channel, publisher and notes field each contain no spaces. What
// is not safe is the column *order*, which is why the header decides it.
func parseSnapList(stdout string) map[string]snapInfo {
	out := map[string]snapInfo{}
	var columns []string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if columns == nil {
			// The first non-empty line is the header, whatever it says.
			if len(fields) < 2 || !strings.EqualFold(fields[0], "Name") {
				// Not a table at all. Anything before the header is
				// snapd's own noise and is skipped rather than parsed.
				continue
			}
			columns = fields
			continue
		}
		if len(fields) != len(columns) {
			// A row that does not line up with the header is not a row
			// this understands. Guessing which column is missing is how
			// a version becomes a channel.
			continue
		}
		s := snapInfo{}
		for i, col := range columns {
			switch strings.ToLower(col) {
			case "name":
				s.Name = fields[i]
			case "version":
				s.Version = fields[i]
			case "rev", "revision":
				s.Revision = fields[i]
			case "tracking", "channel":
				s.Channel = fields[i]
			case "publisher", "developer":
				s.Publisher = fields[i]
			case "notes":
				s.Notes = fields[i]
			}
		}
		if s.Name != "" {
			out[s.Name] = s
		}
	}
	return out
}

func sortedSnapNames(snaps map[string]snapInfo) []string {
	out := make([]string, 0, len(snaps))
	for name := range snaps {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// snapListUpgrades reads `snap refresh --list`, which changes nothing.
func snapListUpgrades(c *exec.Context) (*value.Map, error) {
	if err := haveSnap(c); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"snap", "refresh", "--list", "--color=never", "--unicode=never"},
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("snap refresh --list: %s", firstLine(res.Stderr+res.Stdout))
	}
	// "All snaps up to date." is the answer, not a table.
	out := value.NewMap(8)
	for _, s := range parseSnapList(res.Stdout) {
		out.Set(s.Name, value.MapOf(
			"version", s.Version, "revision", s.Revision,
			"channel", s.Channel, "publisher", s.Publisher))
	}
	return out, nil
}

// snapInstall installs one snap.
//
// A snap that requires classic confinement and was not declared as such
// fails, and snapd says so in a way that is worth passing through rather
// than replacing: the message names the flag. What this adds is what the
// flag means, because "rerun with --classic" reads like a formality and
// is not one.
func snapInstall(c *exec.Context, name string, o snapOptions) error {
	if err := haveSnap(c); err != nil {
		return err
	}
	argv := append([]string{"snap", "install"}, o.args()...)
	argv = append(argv, name)
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		msg := firstLine(res.Stderr + res.Stdout)
		if strings.Contains(res.Stderr+res.Stdout, "--classic") && !o.Classic {
			return fmt.Errorf("%s: %s. Classic confinement is not confinement: "+
				"a classic snap runs with the host's own filesystem and devices, "+
				"so it is declared in the tree with `classic: true` rather than "+
				"applied because the store asked for it", name, msg)
		}
		return fmt.Errorf("%s: %s", name, msg)
	}
	return nil
}

func snapRemove(c *exec.Context, name string, purge bool) error {
	if err := haveSnap(c); err != nil {
		return err
	}
	argv := []string{"snap", "remove"}
	if purge {
		argv = append(argv, "--purge")
	}
	argv = append(argv, name)
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s: %s", name, firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

func snapRefresh(c *exec.Context, name, channel string) error {
	if err := haveSnap(c); err != nil {
		return err
	}
	argv := []string{"snap", "refresh"}
	if channel != "" {
		argv = append(argv, "--channel="+channel)
	}
	if name != "" {
		argv = append(argv, name)
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("snap refresh: %s", firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

// snapInstalledState converges presence and channel, and nothing else.
func snapInstalledState(c *exec.Context, args *value.Map) (states.Result, error) {
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if name == "" {
		return states.False("This state needs a snap name."), nil
	}
	// A version is refused rather than ignored. snapd refreshes snaps on
	// its own and cannot be stopped from doing so, so a state holding a
	// version would report a change on the run after every automatic
	// refresh, for as long as it stayed in the tree. Ignoring the
	// argument would be worse: the tree would say a version is pinned
	// and nothing would be.
	if v := strings.TrimSpace(states.Str(args, "version", "")); v != "" {
		return states.False(fmt.Sprintf(
			"%s: this state does not take a version, and %q was given. snapd refreshes "+
				"snaps by itself, four times a day by default, and that cannot be turned "+
				"off — only deferred, up to 60 days at a time. A version here would be "+
				"reported as drifting after the first automatic refresh and on every run "+
				"after it. Track a channel instead: `channel: latest/stable`.",
			name, v)), nil
	}
	o := snapOptionsFrom(args)

	snaps, err := snapList(c)
	if err != nil {
		return states.False(fmt.Sprintf("%s: the installed snaps could not be read: %v", name, err)), nil
	}
	current, installed := snaps[name]

	if !installed {
		changes := value.MapOf(name, states.Change(nil, snapWanted(o)))
		if c.Test {
			return states.WouldChange(fmt.Sprintf("%s would be installed%s.", name, snapChannelPhrase(o.Channel)), changes), nil
		}
		if err := snapInstall(c, name, o); err != nil {
			return states.False(fmt.Sprintf("%s could not be installed: %v", name, err)), nil
		}
		return states.Changed(fmt.Sprintf("%s was installed%s.", name, snapChannelPhrase(o.Channel)), changes), nil
	}

	// Installed. The only thing left that a state can hold is the
	// channel, and only if the tree named one.
	if o.Channel == "" || current.Channel == o.Channel {
		return states.True(fmt.Sprintf("%s is installed at %s, tracking %s.",
			name, current.Version, current.Channel)), nil
	}
	changes := value.MapOf(name, states.Change(current.Channel, o.Channel))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would switch from %s to %s.",
			name, current.Channel, o.Channel), changes), nil
	}
	if err := snapRefresh(c, name, o.Channel); err != nil {
		return states.False(fmt.Sprintf("%s could not switch to %s: %v", name, o.Channel, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s switched from %s to %s.",
		name, current.Channel, o.Channel), changes), nil
}

func snapWanted(o snapOptions) string {
	if o.Channel != "" {
		return o.Channel
	}
	return "installed"
}

func snapChannelPhrase(channel string) string {
	if channel == "" {
		return ""
	}
	return ", tracking " + channel
}

// snapRemovedState removes a snap, and says what it did with the data.
//
// snapd keeps a snapshot of a removed snap's data unless told not to,
// and reinstalling restores it. That is a reasonable default for a
// laptop and a surprise on a managed node: a state that removed a snap
// and a later state that installed it again would get the old data back
// rather than a fresh install. The comment says which happened, every
// time, rather than leaving it to whoever reads `purge`.
func snapRemovedState(c *exec.Context, args *value.Map) (states.Result, error) {
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if name == "" {
		return states.False("This state needs a snap name."), nil
	}
	purge := states.Bool(args, "purge", false)

	snaps, err := snapList(c)
	if err != nil {
		return states.False(fmt.Sprintf("%s: the installed snaps could not be read: %v", name, err)), nil
	}
	current, installed := snaps[name]
	if !installed {
		return states.True(fmt.Sprintf("%s is not installed.", name)), nil
	}

	fate := "a snapshot of its data is kept, and reinstalling it restores that data"
	if purge {
		fate = "its data is discarded"
	}
	changes := value.MapOf(name, states.Change(current.Version, nil))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be removed; %s.", name, fate), changes), nil
	}
	if err := snapRemove(c, name, purge); err != nil {
		return states.False(fmt.Sprintf("%s could not be removed: %v", name, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s was removed; %s.", name, fate), changes), nil
}
