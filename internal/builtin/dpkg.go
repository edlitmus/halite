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

// debianOnly is the platform restriction the dpkg module carries.
//
// GOOS is the wrong axis for it and it is the only one a signature has:
// dpkg is a Debian thing rather than a Linux thing, and a RHEL node is
// linux too. So the signature narrows it to Linux and every function
// then asks whether the tool is actually there, which is the question
// that matters. The refusal names dpkg rather than the platform,
// because "this is not a Debian system" is what the operator needs and
// "this is not linux" would be false on the node in front of them.
var debianOnly = []string{"linux"}

// registerDpkg installs the dpkg module of SPEC section 15.3's Debian
// row.
//
// It is the low-level half, and it exists because the virtual `pkg`
// module is deliberately not this. `pkg.list_pkgs` reports what is
// installed, which means it filters dpkg's answer down to the packages
// whose status ends in "installed" — a package left half-configured by
// an interrupted upgrade is not installed, so `pkg` does not mention it,
// and a `pkg.installed` state on it is a no-op forever while apt refuses
// to do anything else until it is resolved.
//
// That is the right answer for a state and the wrong one for an operator
// at three in the morning. `dpkg.list_pkgs` reports the status dpkg
// holds, whatever it is, and the rest of this module answers the other
// questions the high-level one has no business having: which package
// owns this file, what is in this .deb, what has been held back, and
// which files have changed since they were unpacked.
func registerDpkg(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "dpkg", Function: "list_pkgs",
				Doc: "Return every package dpkg knows, with its version and its status — including the ones `pkg.list_pkgs` omits because they are not installed.",
				Params: []signature.Param{
					opt("names", signature.List, nil, "Limit to these packages; defaults to all of them."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: debianOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return dpkgListPkgs(c, states.Strings(args, "names"))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "dpkg", Function: "info",
				Doc: "Return the control fields dpkg holds for a package.",
				Params: []signature.Param{
					req("name", signature.String, "The package."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: debianOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return dpkgInfo(c, states.Str(args, "name", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "dpkg", Function: "file_list",
				Doc: "Return the paths a package installed.",
				Params: []signature.Param{
					req("name", signature.String, "The package."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: debianOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return dpkgFileList(c, states.Str(args, "name", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "dpkg", Function: "search",
				Doc: "Return the packages that own a path.",
				Params: []signature.Param{
					req("path", signature.String, "The path, or a pattern dpkg understands."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: debianOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return dpkgSearch(c, states.Str(args, "path", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "dpkg", Function: "bin_pkg_info",
				Doc: "Return the control fields of a .deb file, without installing it.",
				Params: []signature.Param{
					req("path", signature.Path, "The .deb file."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: debianOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return dpkgBinPkgInfo(c, states.Str(args, "path", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "dpkg", Function: "get_selections",
				Doc: "Return each package's selection — install, hold, deinstall or purge.",
				Params: []signature.Param{
					opt("pattern", signature.String, "", "Limit to packages matching this pattern."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: debianOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return dpkgGetSelections(c, states.Str(args, "pattern", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "dpkg", Function: "set_selections",
				Doc: "Set packages' selections, which is how a package is held at its current version.",
				Params: []signature.Param{
					req("selections", signature.Map, "Package name to selection: install, hold, deinstall or purge."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  debianOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return dpkgSetSelections(c, states.Mapping(args, "selections"))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "dpkg", Function: "verify",
				Doc: "Return the installed files whose contents no longer match what the package shipped.",
				Params: []signature.Param{
					opt("name", signature.String, "", "One package; defaults to every one."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: debianOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return dpkgVerify(c, states.Str(args, "name", ""))
			},
		},
	)
}

// haveDpkg refuses on a Linux node that is not a Debian one.
//
// The tool rather than the grain, because a grain says what the node
// calls itself and this says whether the command exists. They agree
// almost always, and when they do not the tool is the one that decides
// whether the next line works.
func haveDpkg(c *exec.Context, tool string) error {
	if c.Which(tool) == "" {
		return fmt.Errorf("%s is not installed; the dpkg module needs a Debian-family system", tool)
	}
	return nil
}

// dpkgStatus is the three words dpkg keeps against every package.
type dpkgStatus struct {
	// Want is what an administrator asked for: install, hold, deinstall,
	// purge or unknown.
	Want string
	// Error is `ok` unless dpkg has a problem recorded, in which case
	// this is the whole reason the package needs attention.
	Error string
	// State is where the package actually got to: installed,
	// half-configured, half-installed, config-files, unpacked, and so on.
	State string
}

func parseDpkgStatus(field string) dpkgStatus {
	parts := strings.Fields(field)
	var s dpkgStatus
	if len(parts) > 0 {
		s.Want = parts[0]
	}
	if len(parts) > 1 {
		s.Error = parts[1]
	}
	if len(parts) > 2 {
		s.State = parts[2]
	}
	return s
}

// dpkgListPkgs reports what dpkg holds, whatever state it is in.
//
// The status is reported rather than filtered on, which is the whole
// difference from `pkg.list_pkgs`. `installed` is the ordinary answer;
// `config-files` is a package removed but not purged, whose
// configuration is still on the disk; `half-configured` and
// `half-installed` are an interrupted run, and apt will refuse to do
// anything else until one of those is resolved.
func dpkgListPkgs(c *exec.Context, names []string) (*value.Map, error) {
	if err := haveDpkg(c, "dpkg-query"); err != nil {
		return nil, err
	}
	// The architecture travels with the name because a multi-arch system
	// can hold two packages that differ only by it, and reporting one
	// `libc6` when there are two is a listing that cannot be acted on.
	argv := []string{"dpkg-query", "-W",
		"-f=${Package}\\t${Architecture}\\t${Version}\\t${Status}\\n"}
	argv = append(argv, names...)

	res, err := c.Run(exec.Command{Argv: argv, Env: aptEnv(), IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	// A name dpkg has never heard of is an error and a non-zero exit,
	// and the packages it *did* find are still on stdout. Reporting
	// those and letting the caller notice the absent one is more use
	// than failing the whole call.
	return parseDpkgList(res.Stdout), nil
}

// parseDpkgList reads the listing.
//
// Separate from the call that produces it, and so is every other parser
// here. The tool check in front of each function asks the real
// filesystem, so on a machine with no dpkg — which is every machine this
// is developed on — a test that went through the module function would
// never reach the parsing. The parsing is the part with the defects in
// it, so it is a function that takes a string.
func parseDpkgList(stdout string) *value.Map {
	out := value.NewMap(256)
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 4 {
			continue
		}
		status := parseDpkgStatus(fields[3])
		out.Set(fields[0], value.MapOf(
			"architecture", fields[1],
			"version", fields[2],
			"want", status.Want,
			"error", status.Error,
			"state", status.State,
			"installed", status.State == "installed",
		))
	}
	return out
}

// dpkgInfo returns the control fields for one installed package.
func dpkgInfo(c *exec.Context, name string) (*value.Map, error) {
	if name == "" {
		return nil, fmt.Errorf("dpkg.info needs a package name")
	}
	if err := haveDpkg(c, "dpkg-query"); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"dpkg-query", "-s", name},
		Env:            aptEnv(),
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("%s", firstLine(res.Stderr+res.Stdout))
	}
	return parseControlFields(res.Stdout), nil
}

// parseControlFields reads RFC 822-ish control output.
//
// A continuation line begins with a space and belongs to the field
// above it, which is how Description carries its long form. Joining
// those rather than dropping them is what makes `Description` and
// `Conffiles` readable.
func parseControlFields(text string) *value.Map {
	out := value.NewMap(24)
	field := ""
	var body []string
	flush := func() {
		if field == "" {
			return
		}
		out.Set(field, strings.TrimRight(strings.Join(body, "\n"), "\n"))
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			trimmed := strings.TrimSpace(line)
			// A lone dot is how the control format spells a blank line
			// inside a field.
			if trimmed == "." {
				trimmed = ""
			}
			body = append(body, trimmed)
			continue
		}
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		flush()
		field = strings.TrimSpace(name)
		body = []string{strings.TrimSpace(rest)}
	}
	flush()
	return out
}

// dpkgFileList returns the paths a package installed.
func dpkgFileList(c *exec.Context, name string) ([]any, error) {
	if name == "" {
		return nil, fmt.Errorf("dpkg.file_list needs a package name")
	}
	if err := haveDpkg(c, "dpkg-query"); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"dpkg-query", "-L", name},
		Env:            aptEnv(),
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("%s", firstLine(res.Stderr+res.Stdout))
	}
	return parseDpkgFileList(res.Stdout), nil
}

func parseDpkgFileList(stdout string) []any {
	var out []any
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		// dpkg lists the directories a package owns as well as its
		// files, and prints `package diverts others to: ...` lines for
		// a diverted path. Neither is a path this package installed.
		if line == "" || strings.Contains(line, " diverts ") || strings.HasPrefix(line, "locally diverted") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// dpkgSearch returns the packages that own a path.
func dpkgSearch(c *exec.Context, path string) (*value.Map, error) {
	if path == "" {
		return nil, fmt.Errorf("dpkg.search needs a path")
	}
	if err := haveDpkg(c, "dpkg-query"); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"dpkg-query", "-S", path},
		Env:            aptEnv(),
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	// No owner is an answer rather than a failure: "nothing installed
	// this file" is what an operator asked, and it is the interesting
	// half of the question.
	if res.Code != 0 {
		return value.NewMap(0), nil
	}
	return parseDpkgSearch(res.Stdout), nil
}

func parseDpkgSearch(stdout string) *value.Map {
	out := value.NewMap(4)
	for _, line := range strings.Split(stdout, "\n") {
		owners, file, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		file = strings.TrimSpace(file)
		// Several packages can own one path, and dpkg lists them comma
		// separated on the left of the colon.
		for _, owner := range strings.Split(owners, ",") {
			owner = strings.TrimSpace(owner)
			if owner == "" {
				continue
			}
			existing, _ := out.Get(owner)
			list, _ := existing.([]any)
			out.Set(owner, append(list, file))
		}
	}
	return out
}

// dpkgBinPkgInfo reads a .deb without installing it.
func dpkgBinPkgInfo(c *exec.Context, path string) (*value.Map, error) {
	if path == "" {
		return nil, fmt.Errorf("dpkg.bin_pkg_info needs a path to a .deb")
	}
	if err := haveDpkg(c, "dpkg-deb"); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"dpkg-deb", "-f", path},
		Env:            aptEnv(),
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("%s: %s", path, firstLine(res.Stderr+res.Stdout))
	}
	return parseControlFields(res.Stdout), nil
}

// dpkgGetSelections reports what each package is selected for.
func dpkgGetSelections(c *exec.Context, pattern string) (*value.Map, error) {
	if err := haveDpkg(c, "dpkg"); err != nil {
		return nil, err
	}
	argv := []string{"dpkg", "--get-selections"}
	if pattern != "" {
		argv = append(argv, pattern)
	}
	res, err := c.Run(exec.Command{Argv: argv, Env: aptEnv(), IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(256)
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		out.Set(fields[0], fields[1])
	}
	return out, nil
}

// dpkgSetSelections holds packages, or releases them.
//
// A hold is how an estate pins a package that apt would otherwise
// upgrade, and it is the one part of this module that changes anything.
// The selections go in on stdin, which is the interface dpkg offers, and
// they are sorted so that two runs of the same request send the same
// bytes.
func dpkgSetSelections(c *exec.Context, want *value.Map) (*value.Map, error) {
	if want == nil || want.Len() == 0 {
		return nil, fmt.Errorf("dpkg.set_selections needs at least one package")
	}

	// The arguments are checked before the tool is, so that a selection
	// nobody has is a mistake on every machine rather than only on a
	// Debian one. The machine somebody writes the state on is usually
	// not the machine it runs on, and "pinned is not a selection" is
	// worth hearing there rather than at the far end.
	names := want.StringKeys()
	sort.Strings(names)
	var lines []string
	for _, name := range names {
		raw, _ := want.Get(name)
		selection, _ := raw.(string)
		switch selection {
		case "install", "hold", "deinstall", "purge":
		default:
			return nil, fmt.Errorf("%s: %q is not a selection; dpkg takes install, hold, deinstall or purge",
				name, selection)
		}
		lines = append(lines, name+" "+selection)
	}

	if err := haveDpkg(c, "dpkg"); err != nil {
		return nil, err
	}

	// What it was, so the caller can see what actually moved.
	before, err := dpkgGetSelections(c, "")
	if err != nil {
		return nil, err
	}
	if c.Test {
		return dpkgSelectionDiff(before, want, names), nil
	}
	if _, err := c.Run(exec.Command{
		Argv:  []string{"dpkg", "--set-selections"},
		Env:   aptEnv(),
		Stdin: strings.Join(lines, "\n") + "\n",
	}); err != nil {
		return nil, err
	}
	after, err := dpkgGetSelections(c, "")
	if err != nil {
		return nil, err
	}
	return dpkgSelectionDiff(before, after, names), nil
}

// dpkgSelectionDiff reports only the packages whose selection moved.
func dpkgSelectionDiff(before, after *value.Map, names []string) *value.Map {
	out := value.NewMap(len(names))
	for _, name := range names {
		wasRaw, _ := before.Get(name)
		nowRaw, _ := after.Get(name)
		was, _ := wasRaw.(string)
		now, _ := nowRaw.(string)
		if was == now {
			continue
		}
		out.Set(name, states.Change(was, now))
	}
	return out
}

// dpkgVerify reports files whose contents have changed since they were
// unpacked.
//
// dpkg exits non-zero when it finds differences, which is the answer
// rather than a failure — the same shape `unless` has.
func dpkgVerify(c *exec.Context, name string) (*value.Map, error) {
	if err := haveDpkg(c, "dpkg"); err != nil {
		return nil, err
	}
	argv := []string{"dpkg", "--verify"}
	if name != "" {
		argv = append(argv, name)
	}
	res, err := c.Run(exec.Command{Argv: argv, Env: aptEnv(), IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(8)
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		// `??5?????? c /etc/thing.conf` — the flags, an optional file
		// type, then the path. Only the last field is the path, and
		// everything before it says what differs.
		if len(fields) < 2 {
			continue
		}
		path := fields[len(fields)-1]
		out.Set(path, value.MapOf(
			"flags", fields[0],
			"configuration", len(fields) > 2 && fields[1] == "c",
		))
	}
	return out, nil
}
