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

// registerChattr installs the `chattr` module SPEC 15.3's RHEL row names:
// a file's filesystem attributes, read through `lsattr` and changed
// through `chattr` from e2fsprogs. Salt spells these `file.lsattr` and
// `file.chattr`; SPEC gives them a module of their own, and the function
// names here say what each one does -- `get`, `add`, `remove` -- rather
// than borrowing the tools' names for a module that is already called
// after one of them.
//
// There is no `chattr` state. SPEC 15.5 lists the state modules and this
// is not one of them; `module.run` reaches `add` and `remove`, and both
// are already idempotent and honest under test mode, which is what a
// state would have added.
//
// # Letters, not columns
//
// lsattr prints one column per attribute its e2fsprogs knows, so the
// width is a fact about the version: 20 characters from e2fsprogs 1.45.6
// on AlmaLinux 8.10 and 22 from 1.46.5 on Rocky 9.8, for the same file
// on the same filesystem. Reading a letter's position would be reading
// the version. Each attribute has its own letter, so `get` reports the
// set of letters present and ignores where they sat.
//
// # Why `=` is not offered, and `e` is refused
//
// `chattr -e` succeeds with exit 0 on both lab hosts and lsattr then
// reports the extents flag gone -- ext4 migrates the file back to block
// maps. A "set exactly these attributes" operation, which is what
// chattr's `=` is, would therefore do that to every ext4 file it was
// pointed at unless the caller remembered to name `e`. So this module
// adds and removes named letters only, and refuses `e` either way: a
// file's on-disk layout is not an attribute anybody should flip from a
// state tree by accident.
//
// # And why every change is read back
//
// On Rocky 9.8, `chattr +c` and `chattr +x` both exit 0 on ext4 and
// lsattr then shows them set; on AlmaLinux 8.10, `+x` is not in that
// e2fsprogs' vocabulary at all and chattr prints only its usage line.
// The exit status is not a reliable account of what changed, so after
// every change lsattr is asked again and the call fails if the
// attributes it was asked for are not what the filesystem now reports.
func registerChattr(r *Registries) {
	paths := req("paths", signature.List, "One or more paths.")
	attributes := req("attributes", signature.String,
		"The attribute letters, such as `i` (immutable) or `ai` (append-only and immutable).")
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "chattr", Function: "get",
				Doc: "Return each path's filesystem attributes as a list of lsattr letters. " +
					"A directory reports its own attributes, not its contents'.",
				Params:    []signature.Param{paths},
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				out := value.NewMap(4)
				for _, path := range states.Strings(args, "paths") {
					letters, err := chattrGet(c, path)
					if err != nil {
						return nil, err
					}
					out.Set(path, lettersToList(letters))
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "chattr", Function: "add",
				Doc: "Set attributes on each path, such as `i` (immutable) or `a` (append-only). " +
					"Paths that already have them are left alone.",
				Params:     []signature.Param{paths, attributes},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return chattrChange(c, states.Strings(args, "paths"), states.Str(args, "attributes", ""), true)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "chattr", Function: "remove",
				Doc:        "Clear attributes from each path. Paths that do not have them are left alone.",
				Params:     []signature.Param{paths, attributes},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return chattrChange(c, states.Strings(args, "paths"), states.Str(args, "attributes", ""), false)
			},
		},
	)
}

// chattrSettable is the union of the letters chattr's own usage line
// offers on the two lab hosts -- `aAcCdDeijPsStTuF` on 1.45.6 and
// `aAcCdDeijPsStTuFx` on 1.46.5 -- less `e`, for the reason the package
// comment gives. A letter one version knows and the other does not (`x`)
// is passed through and left to chattr to refuse, which it does.
const chattrSettable = "aAcCdDijPsStTuFx"

func validateChattrLetters(attributes string) (string, error) {
	if attributes == "" {
		return "", fmt.Errorf("no attributes named; give letters such as `i` or `ai`")
	}
	seen := map[rune]bool{}
	var letters []rune
	for _, r := range attributes {
		if r == 'e' {
			return "", fmt.Errorf("attribute `e` (extents) is refused: removing it really does migrate " +
				"an ext4 file back to block maps, and it is a layout rather than a policy")
		}
		if !strings.ContainsRune(chattrSettable, r) {
			return "", fmt.Errorf("%q is not an attribute chattr sets; it takes letters from %s", r, chattrSettable)
		}
		if !seen[r] {
			seen[r] = true
			letters = append(letters, r)
		}
	}
	return string(letters), nil
}

func haveChattrTools(c *exec.Context, tool string) error {
	if c.Which(tool) == "" {
		return fmt.Errorf("%s is not installed; the chattr module needs e2fsprogs", tool)
	}
	return nil
}

// chattrGet reads one path's attributes. `-d` is not optional: without
// it lsattr on a directory lists the directory's *contents*, one line
// each, and the directory itself not at all -- measured on both hosts --
// so an answer about a directory would silently be an answer about
// whatever was first inside it. `--` because a path may begin with `-`.
func chattrGet(c *exec.Context, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("an empty path was given")
	}
	if err := haveChattrTools(c, "lsattr"); err != nil {
		return "", err
	}
	res, err := c.Run(exec.Command{Argv: []string{"lsattr", "-d", "--", path}, IgnoreExitCode: true})
	if err != nil {
		return "", fmt.Errorf("`lsattr` could not be run on this node: %w", err)
	}
	if res.Code != 0 {
		// lsattr's own words, which name the cause: "No such file or
		// directory while trying to stat", or, for a filesystem with no
		// attributes, "Inappropriate ioctl for device" on AlmaLinux 8 and
		// "Operation not supported" on Rocky 9 for the same /proc.
		return "", fmt.Errorf("%s", strings.TrimSpace(firstLine(res.Stderr+res.Stdout)))
	}
	return parseLsattr(path, res.Stdout)
}

// parseLsattr reads `lsattr -d` output for one path: the flag column,
// one space, then the path exactly as it was given -- spaces and all.
func parseLsattr(path, stdout string) (string, error) {
	for _, line := range strings.Split(stdout, "\n") {
		flags, rest, ok := strings.Cut(line, " ")
		if !ok || rest != path {
			continue
		}
		var letters []rune
		for _, r := range flags {
			switch {
			case r == '-':
			case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
				letters = append(letters, r)
			default:
				return "", fmt.Errorf("lsattr printed %q for %s, which is not a flag column", flags, path)
			}
		}
		sort.Slice(letters, func(i, j int) bool { return letters[i] < letters[j] })
		return string(letters), nil
	}
	return "", fmt.Errorf("lsattr printed nothing about %s: %q", path, strings.TrimSpace(stdout))
}

func lettersToList(letters string) []any {
	out := make([]any, 0, len(letters))
	for _, r := range letters {
		out = append(out, string(r))
	}
	return out
}

// withLetters returns have plus or minus the named letters, sorted, which
// is what lsattr should report after the change.
func withLetters(have, named string, add bool) string {
	set := map[rune]bool{}
	for _, r := range have {
		set[r] = true
	}
	for _, r := range named {
		set[r] = add
	}
	var out []rune
	for r, on := range set {
		if on {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return string(out)
}

// chattrChange adds or removes letters and reports only the paths that
// moved, as {old, new} lists of letters. Under test mode nothing is run
// past the reads, and the prediction is the same arithmetic the real
// change is checked against afterwards.
func chattrChange(c *exec.Context, paths []string, attributes string, add bool) (*value.Map, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no paths given")
	}
	letters, err := validateChattrLetters(attributes)
	if err != nil {
		return nil, err
	}
	if err := haveChattrTools(c, "chattr"); err != nil {
		return nil, err
	}
	op := "-"
	if add {
		op = "+"
	}
	changes := value.NewMap(len(paths))
	for _, path := range paths {
		before, err := chattrGet(c, path)
		if err != nil {
			return nil, err
		}
		want := withLetters(before, letters, add)
		if want == before {
			continue
		}
		if !c.Test {
			res, err := c.Run(exec.Command{
				Argv:           []string{"chattr", op + letters, "--", path},
				IgnoreExitCode: true,
			})
			if err != nil {
				return nil, fmt.Errorf("`chattr` could not be run on this node: %w", err)
			}
			if res.Code != 0 {
				return nil, fmt.Errorf("chattr %s%s %s: %s", op, letters, path,
					strings.TrimSpace(firstLine(res.Stderr+res.Stdout)))
			}
			after, err := chattrGet(c, path)
			if err != nil {
				return nil, err
			}
			if after != want {
				return nil, fmt.Errorf("chattr %s%s %s exited 0 but lsattr now reports %q, not %q",
					op, letters, path, after, want)
			}
		}
		changes.Set(path, states.Change(lettersToList(before), lettersToList(want)))
	}
	return changes, nil
}
