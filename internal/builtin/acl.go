package builtin

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// aclPlatforms names the platforms this module has been run against.
//
// It said "freebsd" alone until there was a Linux host to capture from,
// rather than a man page to read: Linux's getfacl and setfacl are a
// different program from a different package (`acl`, 2.3.2 on the
// Debian 13 host this was captured on), speaking POSIX.1e only. An NFSv4
// ACL on Linux belongs to nfs4-acl-tools, a different pair of programs,
// and is not reached here.
var aclPlatforms = []string{"freebsd", "linux"}

// registerACL installs the `acl` module of SPEC 15.2.
//
// # Two ACL families share one pair of tools
//
// FreeBSD's getfacl(1) and setfacl(1) read and write both NFSv4 ACLs
// (what ZFS uses, and UFS mounted `-o nfsv4acls`) and POSIX.1e ACLs
// (UFS mounted `-o acls`). Linux's speak POSIX.1e only. The two families
// are unrelated grammars: an NFSv4 entry is
// `tag:qualifier:perms:flags:type`, five colon-fields ending in `allow`
// or `deny`; a POSIX.1e entry is `tag:qualifier:perms`, three fields, no
// flags and no type. `getfacl -d` on an NFSv4 path answers "there are no
// default entries in NFSv4 ACLs" -- captured verbatim -- which is
// FreeBSD's own way of saying the two are not interchangeable.
//
// **Which family a path speaks is read from what getfacl prints for it**,
// never inferred from the operating system: two paths on one FreeBSD
// host can differ, and the lab's FreeBSD 15.1 host had a UFS root with
// neither ACL option, where setfacl answers "Operation not supported".
// Every function that reads an ACL reads the family with it, and set
// and remove take the branch that family's grammar needs.
//
// # The POSIX.1e half, and why it waited
//
// This build first parsed NFSv4 only, and refused a three-field entry by
// name rather than guess at one: there was no POSIX.1e filesystem to
// capture from. Both POSIX.1e grammars are now captured -- FreeBSD
// 15.1's on a UFS memory disk mounted `-o acls`, Linux's on Debian 13's
// ext4 and tmpfs, in testdata/acl -- and **they are not the same grammar
// either**. Each difference below was measured on those two hosts, and
// each is a branch here rather than an assumption (DIVERGENCE 5.184):
//
//   - The effective-rights comment after a masked entry is
//     `<tab>#effective:r--` on Linux and `<tab><tab># effective: r--` on
//     FreeBSD. On both, anything after a `#` on an entry line is a
//     comment, and that is how it is read.
//   - Linux prints the default ACL in the same listing as the access
//     ACL, each line prefixed `default:`; FreeBSD prints it only for
//     `getfacl -d`. Both print it, unprefixed, for `getfacl -d` on a
//     directory, so that is the one way it is read, and a `default:` line
//     in the main listing is skipped.
//   - Both add to a default ACL with `setfacl -d -m entry`. When the
//     directory has no default ACL yet, Linux fills in the three required
//     entries itself, copied from the access ACL; FreeBSD refuses the
//     same call with "acl_calc_mask() failed: Invalid argument". So set
//     supplies those three, copied from the access ACL, which has FreeBSD
//     do what Linux does on its own. Both refuse a default ACL on a file.
//   - To remove an entry, FreeBSD needs the three-field form with an
//     empty permission field, `user:nobody:`, and refuses `user:nobody`
//     ("Invalid argument"); Linux accepts both and refuses a permission
//     in that field. `user:nobody:` is what is sent on both. Removing an
//     entry that is not there exits 0 on Linux and 1 on FreeBSD ("cannot
//     remove non-existent ACL entry"), so it is never sent: the ACL is
//     read first.
//   - Linux's setfacl takes an octal digit (`u:nobody:7`) and a
//     conditional `X`; FreeBSD's refuses both, with the misleading
//     "acl_calc_mask() failed". Permissions are therefore the letters
//     `r`, `w`, `x` and `-`, which both read the same way.
//   - **Linux's `getfacl -h` is its help flag**: it prints the usage and
//     exits 0, so a reader asking it not to follow a symlink is handed a
//     usage text to parse. Linux keeps no ACL on a symlink, so
//     follow_symlink=false is refused there by name.
//
// # What is not here
//
// `-k` (delete a whole default ACL) and `-n` (skip mask recalculation)
// are not exposed. A POSIX.1e ACL is kept in the tool's own canonical
// order, so `position` means nothing to one, and neither do `flags` or a
// `deny` type: those three are NFSv4's, and refused by name on a
// POSIX.1e path rather than dropped.
//
// # What `set` had to learn from the tool rather than assume
//
// `setfacl -m tag:qualifier:...` does not append: tested live, adding
// a genuinely new tag/qualifier pair inserts it at position zero, in
// front of the mandatory owner@/group@/everyone@ triple. And it
// matches an *existing* entry to update in place by the three-tuple
// (tag, qualifier, type) together — changing only the type turns "add
// a deny for this user" into a second, separate entry rather than a
// replacement of the allow one, also verified live. set uses `-m` for
// both cases because both are what a caller wants: update if the
// exact (tag, qualifier, type) already exists, insert once if it does
// not. `-a position` is offered only when a caller names a `position`
// and no matching entry exists yet, for the cases position in the
// evaluation order actually matters, such as putting a deny ahead of
// an allow.
//
// # Comparing without a canonical-form library
//
// getfacl always prints permissions and flags in one fixed column
// order, never the order they were set in. Building `set`'s
// idempotence check meant learning that order rather than assuming
// it: every one of the 14 permission letters and every one of the 7
// flag columns (5 letters, 2 always-blank columns) in
// canonicalPerms/canonicalFlags was set alone against a scratch file
// and read back, live, to find its position — see acl_test.go's
// fixtures for the captured output each one produced. A caller's
// short form, long form, or (for permissions) named set is converted
// to that same column order before comparing, which is what makes
// `set` able to report "unchanged" instead of re-running setfacl on
// every state run. A POSIX.1e permission field is `rwx` with a `-` in
// each absent column on both platforms, and is compared the same way.
//
// # The states
//
// `acl.present`, `acl.absent`, `acl.list_present` and `acl.list_absent`
// are in acl_state.go. They decide nothing on their own: every
// comparison and every setfacl call they make is set's or remove's.
func registerACL(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "acl", Function: "get",
				Doc: "Return the ACL of a file or directory: its family (nfsv4 or posix1e), owner, group, every entry in order, and a POSIX.1e directory's default entries.",
				Params: []signature.Param{
					req("name", signature.Path, "The file or directory to read."),
					opt("follow_symlink", signature.Bool, true, "Follow a symlink rather than reading the ACL of the link itself. FreeBSD only: Linux keeps no ACL on a symlink."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: aclPlatforms,
				Section:   "15.2",
			},
			Fn: aclGetFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "acl", Function: "is_extended",
				Doc: "Report whether the ACL is more than the trivial one implied by the file's mode.",
				Params: []signature.Param{
					req("name", signature.Path, "The file or directory to read."),
					opt("follow_symlink", signature.Bool, true, "Follow a symlink rather than reading the ACL of the link itself."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: aclPlatforms,
				Section:   "15.2",
			},
			Fn: aclIsExtendedFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "acl", Function: "set",
				Doc: "Add or update one ACL entry, in whichever family the path's filesystem speaks.",
				Params: []signature.Param{
					req("name", signature.Path, "The file or directory to change."),
					choice("tag", "user", "Who the entry names. user and group are both families'; mask and other are POSIX.1e's; owner@, group@ and everyone@ are NFSv4's.", "user", "group", "mask", "other", "owner@", "group@", "everyone@"),
					opt("qualifier", signature.String, "", "The user or group name (or numeric id). Required for NFSv4 user and group; on POSIX.1e, empty means the owning user or group."),
					req("perms", signature.String, "The permissions. NFSv4: short form (rwp...), long form (read_data/write_data...), or a named set (full_set, modify_set, read_set, write_set). POSIX.1e: the letters r, w, x and -."),
					opt("flags", signature.String, "", "NFSv4 inheritance flags: short form (fd...) or long form (file_inherit/dir_inherit...). Only meaningful on a directory."),
					choice("type", "allow", "NFSv4: whether the entry grants or denies its permissions. POSIX.1e has no deny.", "allow", "deny"),
					opt("position", signature.Int, int64(-1), "NFSv4: insert a brand-new entry at this position (counting from zero) instead of the front. Ignored when an entry already matches tag, qualifier and type."),
					opt("default", signature.Bool, false, "POSIX.1e: change the directory's default ACL rather than its access ACL."),
					opt("recursive", signature.Bool, false, "Apply to every file and directory beneath name too. Not with default: a recursive default ACL reaches files, which FreeBSD's setfacl refuses."),
				},
				Mutates:   true,
				TestMode:  signature.TestReliable,
				Platforms: aclPlatforms,
				Section:   "15.2",
			},
			Fn: aclSetFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "acl", Function: "remove",
				Doc: "Take every ACL entry matching a tag (and, for user and group, a qualifier) out.",
				Params: []signature.Param{
					req("name", signature.Path, "The file or directory to change."),
					choice("tag", "user", "Who the entry names. On POSIX.1e only a named user or group entry can be removed.", "user", "group", "owner@", "group@", "everyone@"),
					opt("qualifier", signature.String, "", "The user or group name (or numeric id). Required for user and group, refused for owner@, group@ and everyone@."),
					choice("type", "", "NFSv4: only remove entries of this type; empty removes both allow and deny.", "", "allow", "deny"),
					opt("default", signature.Bool, false, "POSIX.1e: remove from the directory's default ACL rather than its access ACL."),
					opt("recursive", signature.Bool, false, "NFSv4: apply to every file and directory beneath name too."),
				},
				Mutates:   true,
				TestMode:  signature.TestReliable,
				Platforms: aclPlatforms,
				Section:   "15.2",
			},
			Fn: aclRemoveFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "acl", Function: "wipe",
				Doc: "Remove every ACL entry except the trivial one implied by the file's mode.",
				Params: []signature.Param{
					req("name", signature.Path, "The file or directory to change."),
					opt("recursive", signature.Bool, false, "Apply to every file and directory beneath name too."),
				},
				Mutates:   true,
				TestMode:  signature.TestReliable,
				Platforms: aclPlatforms,
				Section:   "15.2",
			},
			Fn: aclWipeFn,
		},
	)
	r.States.Add(aclStateModules()...)
}

// ---- reading ----

// The two families, as acl.get names them.
const (
	aclFamilyNFSv4 = "nfsv4"
	aclFamilyPOSIX = "posix1e"
)

// aclEntry is one line of an ACL, in the fields getfacl and setfacl
// agree on. A POSIX.1e entry leaves flags and typ empty: that family has
// neither, and an empty typ is how the family of an entry is told.
type aclEntry struct {
	tag, qualifier, permissions, flags, typ string
}

func (e aclEntry) posix() bool { return e.typ == "" }

func (e aclEntry) asMap() *value.Map {
	return value.MapOf(
		"tag", e.tag,
		"qualifier", nilIfEmpty(e.qualifier),
		"permissions", e.permissions,
		"flags", nilIfEmpty(e.flags),
		"type", nilIfEmpty(e.typ),
	)
}

// aclFamilyOf names the family a parsed ACL is in. Every ACL getfacl
// prints has at least the three required entries, so an empty list is
// not one getfacl produced; it is reported as NFSv4, the family whose
// readers were here first, and nothing branches on it.
func aclFamilyOf(entries []aclEntry) string {
	if len(entries) > 0 && entries[0].posix() {
		return aclFamilyPOSIX
	}
	return aclFamilyNFSv4
}

func aclToolPresent(c *exec.Context, tool string) error {
	if c.Which(tool) == "" {
		return fmt.Errorf("this node has no `%s`; it ships in FreeBSD's base system, and on Linux in the `acl` package", tool)
	}
	return nil
}

// readACL runs getfacl and parses its answer. It is the one place both
// the reading functions and the mutating ones (which all need to see
// the ACL before deciding whether there is anything to change) go
// through.
func readACL(c *exec.Context, path string, followSymlink bool) (owner, group string, entries []aclEntry, err error) {
	if err := aclToolPresent(c, "getfacl"); err != nil {
		return "", "", nil, err
	}
	argv := []string{"getfacl"}
	if !followSymlink {
		if runtime.GOOS == "linux" {
			// Linux's -h is --help, and exits 0 having printed the usage.
			return "", "", nil, errors.New("a symlink carries no ACL of its own on Linux, and its getfacl " +
				"has no flag to read one: -h prints the usage. Read the target with follow_symlink left on")
		}
		argv = append(argv, "-h")
	}
	argv = append(argv, path)
	res, runErr := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if runErr != nil {
		return "", "", nil, fmt.Errorf("getfacl could not be run: %w", runErr)
	}
	if res.Code != 0 {
		return "", "", nil, fmt.Errorf("getfacl could not read %s: %s", path, strings.TrimSpace(firstLine(res.Stderr)))
	}
	owner, group, entries, err = parseACLOutput(res.Stdout)
	if err != nil {
		return "", "", nil, fmt.Errorf("%s: %w", path, err)
	}
	return owner, group, entries, nil
}

// readDefaultACL reads a POSIX.1e directory's default ACL with
// `getfacl -d`, which prints it unprefixed on both platforms. A
// directory with none prints only the header, which reads as no entries.
//
// Only asked of a directory: FreeBSD's `getfacl -d` on a file fails
// with "Invalid argument", where Linux's prints an empty listing.
func readDefaultACL(c *exec.Context, path string) ([]aclEntry, error) {
	if err := aclToolPresent(c, "getfacl"); err != nil {
		return nil, err
	}
	res, runErr := c.Run(exec.Command{Argv: []string{"getfacl", "-d", path}, IgnoreExitCode: true})
	if runErr != nil {
		return nil, fmt.Errorf("getfacl could not be run: %w", runErr)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("getfacl could not read the default ACL of %s: %s", path, strings.TrimSpace(firstLine(res.Stderr)))
	}
	_, _, entries, err := parseACLOutput(res.Stdout)
	if err != nil {
		return nil, fmt.Errorf("%s's default ACL: %w", path, err)
	}
	return entries, nil
}

// parseACLOutput reads getfacl's whole answer: the "# owner:"/"#
// group:" header comments and every entry line. Kept apart from
// readACL so a test can feed it a captured run without executing
// getfacl itself.
func parseACLOutput(output string) (owner, group string, entries []aclEntry, err error) {
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "# owner:"):
			owner = strings.TrimSpace(strings.TrimPrefix(line, "# owner:"))
		case strings.HasPrefix(line, "# group:"):
			group = strings.TrimSpace(strings.TrimPrefix(line, "# group:"))
		case strings.HasPrefix(line, "#"):
			// "# file: ..." and anything else commented; not needed here.
		case strings.HasPrefix(line, "default:"):
			// Linux lists a directory's default ACL here too, prefixed;
			// FreeBSD never does. It is read with `getfacl -d` on both,
			// so that both read it the same way.
			continue
		default:
			entry, entryErr := parseACLEntryLine(line)
			if entryErr != nil {
				return "", "", nil, entryErr
			}
			if len(entries) > 0 && entry.posix() != entries[0].posix() {
				return "", "", nil, fmt.Errorf("getfacl printed NFSv4 and POSIX.1e entries in one ACL, "+
					"which no filesystem measured does; %q does not match the entries before it", line)
			}
			entries = append(entries, entry)
		}
	}
	return owner, group, entries, nil
}

// parseACLEntryLine reads one entry of `getfacl`'s output.
//
// An NFSv4 entry is 4 colon-fields for the three tags that take no
// qualifier (owner@, group@, everyone@) or 5 for the two that do (user,
// group), always ending in "allow" or "deny". A POSIX.1e entry is 3
// fields, `tag:qualifier:rwx`, where the tag is user, group, mask or
// other and the qualifier is empty for the owner, the owning group,
// the mask and other. Anything after a `#` is getfacl's effective-rights
// comment, spelled differently on Linux and FreeBSD and read by neither.
func parseACLEntryLine(line string) (aclEntry, error) {
	if i := strings.IndexByte(line, '#'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	fields := strings.Split(line, ":")
	switch len(fields) {
	case 4:
		tag, perms, flags, typ := fields[0], fields[1], fields[2], fields[3]
		if !strings.HasSuffix(tag, "@") || (typ != "allow" && typ != "deny") {
			return aclEntry{}, fmt.Errorf("an ACL entry could not be read: %q", line)
		}
		return aclEntry{tag: tag, permissions: perms, flags: flags, typ: typ}, nil
	case 5:
		tag, qualifier, perms, flags, typ := fields[0], fields[1], fields[2], fields[3], fields[4]
		if (tag != "user" && tag != "group") || (typ != "allow" && typ != "deny") {
			return aclEntry{}, fmt.Errorf("an ACL entry could not be read: %q", line)
		}
		return aclEntry{tag: tag, qualifier: qualifier, permissions: perms, flags: flags, typ: typ}, nil
	case 3:
		tag, qualifier, perms := fields[0], fields[1], fields[2]
		switch tag {
		case "user", "group":
		case "mask", "other":
			if qualifier != "" {
				return aclEntry{}, fmt.Errorf("a POSIX.1e %s entry names no one, and this one names %q: %q", tag, qualifier, line)
			}
		default:
			return aclEntry{}, fmt.Errorf("a POSIX.1e ACL entry could not be read: %q", line)
		}
		if !aclPOSIXPermField(perms) {
			return aclEntry{}, fmt.Errorf("a POSIX.1e ACL entry's permissions could not be read: %q", line)
		}
		return aclEntry{tag: tag, qualifier: qualifier, permissions: perms}, nil
	default:
		return aclEntry{}, fmt.Errorf("an ACL entry could not be read: %q", line)
	}
}

// aclPOSIXPermField reports whether s is the three-column `rwx` field
// both platforms' getfacl print, a `-` in each absent column.
func aclPOSIXPermField(s string) bool {
	return len(s) == 3 &&
		(s[0] == 'r' || s[0] == '-') &&
		(s[1] == 'w' || s[1] == '-') &&
		(s[2] == 'x' || s[2] == '-')
}

// aclIsDir answers whether a path is a directory, for the two places
// that need it: a default ACL exists only on one, and both tools refuse
// one on a file.
func aclIsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func aclGetFn(c *exec.Context, args *value.Map) (any, error) {
	path := states.Str(args, "name", "")
	if path == "" {
		return nil, errors.New("an ACL needs a path")
	}
	owner, group, entries, err := readACL(c, path, states.Bool(args, "follow_symlink", true))
	if err != nil {
		return nil, err
	}
	family := aclFamilyOf(entries)
	out := value.MapOf("family", family, "owner", owner, "group", group, "entries", aclEntryList(entries))
	if family == aclFamilyPOSIX && aclIsDir(path) {
		defaults, err := readDefaultACL(c, path)
		if err != nil {
			return nil, err
		}
		out.Set("default_entries", aclEntryList(defaults))
	}
	return out, nil
}

func aclEntryList(entries []aclEntry) []any {
	list := make([]any, len(entries))
	for i, e := range entries {
		list[i] = e.asMap()
	}
	return list
}

func aclIsExtendedFn(c *exec.Context, args *value.Map) (any, error) {
	path := states.Str(args, "name", "")
	if path == "" {
		return nil, errors.New("an ACL needs a path")
	}
	return aclExtendedMark(c, path, states.Bool(args, "follow_symlink", true))
}

// aclExtendedMark reports whether a file's ACL says anything its mode
// does not.
//
// # Why this does not ask getfacl
//
// `getfacl -s` answers exactly this question, and **it was added in
// FreeBSD 15**. On 14 it is not an option at all -- `getfacl [-dhnqv]`
// -- so the call failed with `getfacl: illegal option -- s` for every
// path on every 14 host, including the POSIX.1e paths this module then
// refused by name, whose refusal arrived as that instead. DIVERGENCE
// 5.113.
//
// `ls` marks a file carrying such an ACL with a `+` after the mode, on
// both releases, from the same acl_is_trivial_np(3) the flag uses.
// Checked against `getfacl -s` on 15.1 across a trivial file, an
// extended file, a symlink to each, an extended directory and a path
// with a space in it: they agreed on all six, and on 14.5 the mark
// answers where the flag cannot. GNU ls on Debian 13 marks an ACL the
// same way (`-rw-r-xr--+`, captured).
//
// # Why this does not read the ACL first
//
// Every other function here goes through readACL. This one must not:
// it never parses an entry, so the family split does not reach it, and
// the mark is the same mark whichever family the filesystem speaks. A
// POSIX.1e path carrying a mask entry gets a real answer --
// `live_acl_test.go` asserts exactly that, and an earlier cut of this
// fix failed it on both releases by reading the ACL for its errors.
//
// # Why the entries cannot answer instead
//
// Counting them looks like it would work and does not. A file whose
// `owner@` permissions have been widened carries the same three
// canonical entries a trivial file carries -- owner@, group@,
// everyone@, all allow -- and is extended. Measured on both releases,
// which is the only reason this is not a count.
//
// # Why following a symlink resolves it first
//
// ls computes the mark on the path it is handed and does not move it
// across a symlink, **not even under -L**: a link to an extended file
// lists unmarked while `getfacl`, which follows by default, calls it
// extended. So `follow_symlink` resolves with realpath(1) and marks
// the target, which is what makes the two agree.
func aclExtendedMark(c *exec.Context, path string, follow bool) (bool, error) {
	target := path
	if follow {
		res, err := c.Run(exec.Command{Argv: []string{"realpath", path}, IgnoreExitCode: true})
		if err != nil {
			return false, fmt.Errorf("realpath could not be run: %w", err)
		}
		if res.Code != 0 {
			return false, fmt.Errorf("realpath could not resolve %s: %s",
				path, strings.TrimSpace(firstLine(res.Stderr)))
		}
		if resolved := strings.TrimSpace(res.Stdout); resolved != "" {
			target = resolved
		}
	}
	res, err := c.Run(exec.Command{Argv: []string{"ls", "-ld", target}, IgnoreExitCode: true})
	if err != nil {
		return false, fmt.Errorf("ls could not be run: %w", err)
	}
	if res.Code != 0 {
		return false, fmt.Errorf("ls could not read %s: %s",
			target, strings.TrimSpace(firstLine(res.Stderr)))
	}
	mode, _, _ := strings.Cut(strings.TrimSpace(res.Stdout), " ")
	return strings.HasSuffix(mode, "+"), nil
}

// ---- comparing ----
//
// getfacl always prints permissions and flags in one column order,
// never the order a caller set them in, so telling "unchanged" from
// "changed" means putting a caller's short form, long form or named
// set into that same order first. nfsv4PermLetters and nfsv4FlagLetters
// are that order, each position confirmed by setting it alone against
// a scratch file and reading back what getfacl printed — see
// acl_test.go's captured fixtures. A flag column holding 0 is one of
// the two this host's getfacl always prints as "-": every flag entry
// tried, alone and combined, left them blank.

var nfsv4PermLetters = []byte("rwxpDdaARWcCos")

var nfsv4PermWords = []string{
	"read_data", "write_data", "execute", "append_data", "delete_child", "delete",
	"read_attributes", "write_attributes", "read_xattr", "write_xattr",
	"read_acl", "write_acl", "write_owner", "synchronize",
}

// nfsv4PermSets are setfacl(1)'s four named permission sets, each
// expanded to the letters it turned on when tried live.
var nfsv4PermSets = map[string]string{
	"full_set":   "rwxpDdaARWcCos",
	"modify_set": "rwxpDdaARWcs",
	"read_set":   "raRc",
	"write_set":  "wpAW",
}

var nfsv4FlagLetters = []byte{'f', 'd', 'i', 'n', 0, 0, 'I'}

var nfsv4FlagWords = []string{
	"file_inherit", "dir_inherit", "inherit_only", "no_propagate", "", "", "inherited",
}

func canonicalPerms(input string) (string, error) {
	return canonicalACLField(input, nfsv4PermLetters, nfsv4PermWords, nfsv4PermSets)
}

func canonicalFlags(input string) (string, error) {
	return canonicalACLField(input, nfsv4FlagLetters, nfsv4FlagWords, nil)
}

// canonicalPOSIXPerms turns a caller's POSIX.1e permissions -- any of
// r, w and x in any order, with or without `-` -- into the `rwx` column
// form both platforms' getfacl print. Empty is no permission at all.
//
// An octal digit and `X` are refused rather than translated: Linux's
// setfacl takes both, FreeBSD's refuses both (measured), and `X` is not
// a permission at all but a rule about when to grant one, so there is
// no column form to compare it against.
func canonicalPOSIXPerms(input string) (string, error) {
	out := []byte("---")
	for _, ch := range []byte(strings.TrimSpace(input)) {
		switch ch {
		case 'r':
			out[0] = 'r'
		case 'w':
			out[1] = 'w'
		case 'x':
			out[2] = 'x'
		case '-':
		default:
			return "", fmt.Errorf("%q is not a POSIX.1e permission; give the letters r, w and x "+
				"(FreeBSD's setfacl refuses an octal digit and X, so this build refuses them on both)", string(ch))
		}
	}
	return string(out), nil
}

// canonicalACLField turns a caller's short form, long form, or (for
// permissions) named set into the fixed column order getfacl prints,
// so that two spellings of the same permissions compare equal.
func canonicalACLField(input string, letters []byte, words []string, sets map[string]string) (string, error) {
	on := make([]bool, len(letters))
	trimmed := strings.TrimSpace(input)
	switch {
	case trimmed == "":
		// No permission or flag named; every column stays blank.
	case sets[trimmed] != "":
		for _, ch := range []byte(sets[trimmed]) {
			on[indexOfLetter(letters, ch)] = true
		}
	case strings.Contains(trimmed, "/"):
		for _, word := range strings.Split(trimmed, "/") {
			word = strings.TrimSpace(word)
			i := indexOfWord(words, word)
			if i < 0 {
				return "", fmt.Errorf("%q is not a permission or flag this build knows", word)
			}
			on[i] = true
		}
	default:
		for _, ch := range []byte(trimmed) {
			i := indexOfLetter(letters, ch)
			if i < 0 {
				return "", fmt.Errorf("%q is not a permission or flag letter this build knows", string(ch))
			}
			on[i] = true
		}
	}
	out := make([]byte, len(letters))
	for i := range out {
		if on[i] {
			out[i] = letters[i]
		} else {
			out[i] = '-'
		}
	}
	return string(out), nil
}

func indexOfLetter(letters []byte, ch byte) int {
	for i, l := range letters {
		if l == ch {
			return i
		}
	}
	return -1
}

func indexOfWord(words []string, w string) int {
	for i, ww := range words {
		if ww != "" && ww == w {
			return i
		}
	}
	return -1
}

// ---- writing ----

// aclValidateTagQualifier enforces the one rule every NFSv4 entry
// follows: user and group name someone, and the three "@" tags never
// do — setfacl itself refuses a qualifier on owner@/group@/everyone@.
func aclValidateTagQualifier(tag, qualifier string) error {
	named := tag == "user" || tag == "group"
	switch {
	case tag == "mask" || tag == "other":
		return fmt.Errorf("tag %q is POSIX.1e's, and this path's ACL is NFSv4: owner@, group@ and everyone@ are its equivalents", tag)
	case named && qualifier == "":
		return fmt.Errorf("tag %q needs a qualifier naming the user or group", tag)
	case !named && qualifier != "":
		return fmt.Errorf("tag %q takes no qualifier; owner@, group@ and everyone@ name no one else", tag)
	}
	return nil
}

// aclValidatePOSIXEntry is aclValidateTagQualifier's POSIX.1e twin, and
// refuses by name every argument that only means something to NFSv4,
// rather than dropping it.
func aclValidatePOSIXEntry(tag, qualifier, flags, typ string, position int64) error {
	switch tag {
	case "user", "group":
	case "mask", "other":
		if qualifier != "" {
			return fmt.Errorf("a POSIX.1e %s entry names no one; drop the qualifier %q", tag, qualifier)
		}
	default:
		return fmt.Errorf("tag %q is NFSv4's, and this path's ACL is POSIX.1e: its tags are user, group, mask and other", tag)
	}
	if strings.TrimSpace(flags) != "" {
		return errors.New("this path's ACL is POSIX.1e, which has no inheritance flags; a directory's default ACL is its equivalent")
	}
	if typ != "" && typ != "allow" {
		return errors.New("this path's ACL is POSIX.1e, which has no deny entries")
	}
	if position >= 0 {
		return errors.New("this path's ACL is POSIX.1e, which keeps its entries in an order of its own; position is NFSv4's")
	}
	return nil
}

func aclQualifierSuffix(qualifier string) string {
	if qualifier == "" {
		return ""
	}
	return ":" + qualifier
}

// aclEntrySpec renders one entry the way setfacl's -a/-m take it.
func aclEntrySpec(tag, qualifier, perms, flags, typ string) string {
	if qualifier == "" {
		return fmt.Sprintf("%s:%s:%s:%s", tag, perms, flags, typ)
	}
	return fmt.Sprintf("%s:%s:%s:%s:%s", tag, qualifier, perms, flags, typ)
}

// findACLEntry matches the way setfacl -m itself matches: by tag,
// qualifier and type together, confirmed live — a deny entry for a
// user who already has an allow entry is a second entry, not a
// replacement of the first. A POSIX.1e entry's type is empty, so the
// same match is by tag and qualifier there.
func findACLEntry(entries []aclEntry, tag, qualifier, typ string) *aclEntry {
	for i := range entries {
		if entries[i].tag == tag && entries[i].qualifier == qualifier && entries[i].typ == typ {
			return &entries[i]
		}
	}
	return nil
}

func aclMutateResult(c *exec.Context, changed bool, comment string, change *value.Map) *value.Map {
	out := value.NewMap(3)
	out.Set("changed", changed)
	if c.Test && changed {
		out.Set("comment", comment+" Nothing was changed: this was a test run.")
	} else {
		out.Set("comment", comment)
	}
	if change != nil {
		out.Set("changes", change)
	}
	return out
}

// runSetfacl runs one setfacl and turns a refusal into an error that
// carries the tool's own words.
func runSetfacl(c *exec.Context, argv []string, path, doing string) error {
	if err := aclToolPresent(c, "setfacl"); err != nil {
		return err
	}
	res, runErr := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if runErr != nil {
		return fmt.Errorf("setfacl could not be run: %w", runErr)
	}
	if res.Code != 0 {
		return fmt.Errorf("setfacl could not %s %s: %s", doing, path, strings.TrimSpace(firstLine(res.Stderr)))
	}
	return nil
}

func aclSetFn(c *exec.Context, args *value.Map) (any, error) {
	path := states.Str(args, "name", "")
	if path == "" {
		return nil, errors.New("an ACL entry needs a path")
	}
	_, _, current, err := readACL(c, path, true)
	if err != nil {
		return nil, err
	}
	if aclFamilyOf(current) == aclFamilyPOSIX {
		return aclSetPOSIX(c, path, args, current)
	}

	tag := states.Str(args, "tag", "user")
	qualifier := states.Str(args, "qualifier", "")
	permsRaw := states.Str(args, "perms", "")
	flagsRaw := states.Str(args, "flags", "")
	typ := states.Str(args, "type", "allow")
	position := states.Int(args, "position", -1)
	recursive := states.Bool(args, "recursive", false)

	if states.Bool(args, "default", false) {
		return nil, errors.New("this path's ACL is NFSv4, which has no default ACL: " +
			"inheritance is set with flags (file_inherit, dir_inherit) instead")
	}
	if err := aclValidateTagQualifier(tag, qualifier); err != nil {
		return nil, err
	}
	wantPerms, err := canonicalPerms(permsRaw)
	if err != nil {
		return nil, err
	}
	wantFlags, err := canonicalFlags(flagsRaw)
	if err != nil {
		return nil, err
	}

	existing := findACLEntry(current, tag, qualifier, typ)

	target := tag + aclQualifierSuffix(qualifier)
	var changed bool
	var before string
	switch existing {
	case nil:
		changed = true
		before = "absent"
	default:
		// existing.permissions and existing.flags are already the exact
		// column-ordered strings getfacl prints — the same alphabet
		// canonicalPerms/canonicalFlags build from a caller's short form,
		// long form or named set — so they compare directly against
		// wantPerms/wantFlags rather than through canonicalPerms again,
		// which would read their '-' padding as an unknown letter.
		changed = existing.permissions != wantPerms || existing.flags != wantFlags
		before = fmt.Sprintf("permissions %s, flags %s", existing.permissions, existing.flags)
	}

	if !changed {
		return aclMutateResult(c, false, fmt.Sprintf("%s already has %s %s with permissions %s.", path, target, typ, permsRaw), nil), nil
	}

	after := fmt.Sprintf("permissions %s, flags %s", permsRaw, flagsRaw)
	change := value.NewMap(1)
	change.Set(target, states.Change(before, after))
	comment := fmt.Sprintf("%s's %s entry for %s would be set to %s.", path, typ, target, after)
	if c.Test {
		return aclMutateResult(c, true, comment, change), nil
	}

	entrySpec := aclEntrySpec(tag, qualifier, permsRaw, flagsRaw, typ)
	argv := []string{"setfacl"}
	if recursive {
		argv = append(argv, "-R")
	}
	if existing == nil && position >= 0 {
		argv = append(argv, "-a", strconv.FormatInt(position, 10), entrySpec)
	} else {
		argv = append(argv, "-m", entrySpec)
	}
	argv = append(argv, path)
	if err := runSetfacl(c, argv, path, "change"); err != nil {
		return nil, err
	}
	return aclMutateResult(c, true, fmt.Sprintf("%s's %s entry for %s was set to %s.", path, typ, target, after), change), nil
}

// aclSetPOSIX is set's POSIX.1e branch. The comparison is the same --
// the entry getfacl prints, against the caller's permissions in getfacl's
// own column form -- and the differences are in what is sent, each of
// which registerACL's comment lists with the capture behind it.
func aclSetPOSIX(c *exec.Context, path string, args *value.Map, access []aclEntry) (any, error) {
	tag := states.Str(args, "tag", "user")
	qualifier := states.Str(args, "qualifier", "")
	permsRaw := states.Str(args, "perms", "")
	isDefault := states.Bool(args, "default", false)
	recursive := states.Bool(args, "recursive", false)

	if err := aclValidatePOSIXEntry(tag, qualifier, states.Str(args, "flags", ""),
		states.Str(args, "type", "allow"), states.Int(args, "position", -1)); err != nil {
		return nil, err
	}
	want, err := canonicalPOSIXPerms(permsRaw)
	if err != nil {
		return nil, err
	}

	current := access
	which := "access"
	if isDefault {
		if recursive {
			return nil, errors.New("a recursive default ACL reaches the files under a directory, which " +
				"FreeBSD's setfacl refuses (\"default ACL may only be set on a directory\"); the acl.present " +
				"state recurses itself and sets defaults on directories only")
		}
		if !aclIsDir(path) {
			return nil, fmt.Errorf("%s is not a directory, and only a directory has a default ACL", path)
		}
		if current, err = readDefaultACL(c, path); err != nil {
			return nil, err
		}
		which = "default"
	}

	target := tag + ":" + qualifier
	existing := findACLEntry(current, tag, qualifier, "")
	before := "absent"
	if existing != nil {
		if existing.permissions == want {
			return aclMutateResult(c, false, fmt.Sprintf("%s's %s ACL already has %s:%s.", path, which, target, want), nil), nil
		}
		before = existing.permissions
	}

	change := value.NewMap(1)
	change.Set(aclChangeKey(isDefault, target), states.Change(before, want))
	if c.Test {
		return aclMutateResult(c, true, fmt.Sprintf("%s's %s ACL entry %s would be set to %s.", path, which, target, want), change), nil
	}

	spec := target + ":" + want
	argv := []string{"setfacl"}
	if recursive {
		argv = append(argv, "-R")
	}
	if isDefault {
		argv = append(argv, "-d")
		if len(current) == 0 {
			spec = aclSeedDefault(access, tag, qualifier) + spec
		}
	}
	argv = append(argv, "-m", spec, path)
	if err := runSetfacl(c, argv, path, "change"); err != nil {
		return nil, err
	}
	return aclMutateResult(c, true, fmt.Sprintf("%s's %s ACL entry %s was set to %s.", path, which, target, want), change), nil
}

// aclSeedDefault supplies the three entries a default ACL cannot exist
// without, copied from the access ACL, for a directory that has no
// default ACL yet. Linux's setfacl does exactly this by itself; FreeBSD's
// refuses the call instead ("acl_calc_mask() failed"), both measured, so
// this is what makes the two give the same answer. The entry being set
// is left out of the seed when it is one of the three, so that it is not
// named twice in one call.
func aclSeedDefault(access []aclEntry, tag, qualifier string) string {
	var seed strings.Builder
	for _, base := range []string{"user", "group", "other"} {
		if base == tag && qualifier == "" {
			continue
		}
		if e := findACLEntry(access, base, "", ""); e != nil {
			seed.WriteString(base + "::" + e.permissions + ",")
		}
	}
	return seed.String()
}

func aclChangeKey(isDefault bool, target string) string {
	if isDefault {
		return "default:" + target
	}
	return target
}

func aclPluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func aclRemoveFn(c *exec.Context, args *value.Map) (any, error) {
	path := states.Str(args, "name", "")
	if path == "" {
		return nil, errors.New("an ACL entry needs a path")
	}
	tag := states.Str(args, "tag", "user")
	qualifier := states.Str(args, "qualifier", "")
	typ := states.Str(args, "type", "")
	recursive := states.Bool(args, "recursive", false)

	_, _, current, err := readACL(c, path, true)
	if err != nil {
		return nil, err
	}
	if aclFamilyOf(current) == aclFamilyPOSIX {
		return aclRemovePOSIX(c, path, args, current)
	}
	if states.Bool(args, "default", false) {
		return nil, errors.New("this path's ACL is NFSv4, which has no default ACL")
	}
	if err := aclValidateTagQualifier(tag, qualifier); err != nil {
		return nil, err
	}

	var positions []int
	for i, e := range current {
		if e.tag == tag && e.qualifier == qualifier && (typ == "" || e.typ == typ) {
			positions = append(positions, i)
		}
	}

	target := tag + aclQualifierSuffix(qualifier)
	if len(positions) == 0 {
		return aclMutateResult(c, false, fmt.Sprintf("%s has no %s entry for %s.", path, firstNonEmpty(typ, "ACL"), target), nil), nil
	}

	change := value.NewMap(1)
	change.Set(target, states.Change(fmt.Sprintf("%d entr%s", len(positions), aclPluralY(len(positions))), 0))
	comment := fmt.Sprintf("%d ACL entr%s for %s would be removed from %s.", len(positions), aclPluralY(len(positions)), target, path)
	if c.Test {
		return aclMutateResult(c, true, comment, change), nil
	}

	// High to low: removing an earlier index shifts every later one down
	// by one, and removing this list back to front means each index this
	// function found is still the right one when its turn comes.
	for i := len(positions) - 1; i >= 0; i-- {
		argv := []string{"setfacl"}
		if recursive {
			argv = append(argv, "-R")
		}
		argv = append(argv, "-x", strconv.Itoa(positions[i]), path)
		if err := runSetfacl(c, argv, path, fmt.Sprintf("remove entry %d from", positions[i])); err != nil {
			return nil, err
		}
	}
	return aclMutateResult(c, true, fmt.Sprintf("%d ACL entr%s for %s removed from %s.", len(positions), aclPluralY(len(positions)), target, path), change), nil
}

// aclRemovePOSIX is remove's POSIX.1e branch: one named user or group
// entry, from the access ACL or a directory's default one.
//
// The owner, owning group, other and mask entries are not removable in
// the sense a caller means -- the first three are required, and the mask
// is recalculated or refused while a named entry needs it -- so they are
// refused here rather than handed to setfacl to refuse in two different
// words.
func aclRemovePOSIX(c *exec.Context, path string, args *value.Map, access []aclEntry) (any, error) {
	tag := states.Str(args, "tag", "user")
	qualifier := states.Str(args, "qualifier", "")
	isDefault := states.Bool(args, "default", false)

	if (tag != "user" && tag != "group") || qualifier == "" {
		return nil, fmt.Errorf("on a POSIX.1e ACL only a named user or group entry can be removed; "+
			"%s:%s is one of the entries every such ACL has", tag, qualifier)
	}
	if typ := states.Str(args, "type", ""); typ != "" && typ != "allow" {
		return nil, errors.New("this path's ACL is POSIX.1e, which has no deny entries")
	}
	if states.Bool(args, "recursive", false) {
		return nil, errors.New("a recursive POSIX.1e removal fails on FreeBSD at the first path " +
			"without the entry (\"cannot remove non-existent ACL entry\"), where Linux's carries on; " +
			"the acl.absent state recurses itself and removes only what is there")
	}

	current := access
	which := "access"
	if isDefault {
		if !aclIsDir(path) {
			return aclMutateResult(c, false, fmt.Sprintf("%s is not a directory, so it has no default ACL to remove %s:%s from.", path, tag, qualifier), nil), nil
		}
		var err error
		if current, err = readDefaultACL(c, path); err != nil {
			return nil, err
		}
		which = "default"
	}

	target := tag + ":" + qualifier
	existing := findACLEntry(current, tag, qualifier, "")
	if existing == nil {
		return aclMutateResult(c, false, fmt.Sprintf("%s's %s ACL has no entry for %s.", path, which, target), nil), nil
	}

	change := value.NewMap(1)
	change.Set(aclChangeKey(isDefault, target), states.Change(existing.permissions, "absent"))
	if c.Test {
		return aclMutateResult(c, true, fmt.Sprintf("%s's %s ACL entry %s would be removed.", path, which, target), change), nil
	}
	argv := []string{"setfacl"}
	if isDefault {
		argv = append(argv, "-d")
	}
	argv = append(argv, "-x", target+":", path)
	if err := runSetfacl(c, argv, path, "remove "+target+" from"); err != nil {
		return nil, err
	}
	return aclMutateResult(c, true, fmt.Sprintf("%s's %s ACL entry %s was removed.", path, which, target), change), nil
}

func aclWipeFn(c *exec.Context, args *value.Map) (any, error) {
	path := states.Str(args, "name", "")
	if path == "" {
		return nil, errors.New("an ACL needs a path")
	}
	recursive := states.Bool(args, "recursive", false)

	// Asked through aclExtendedMark, not `getfacl -s`.
	//
	// **`getfacl -s` was added in FreeBSD 15.** On 14 it is not an option
	// at all -- the usage is `getfacl [-dhnqv]` -- so this call failed with
	//
	//	getfacl: illegal option -- s
	//
	// for every path on every 14 host, which made `acl.wipe` unusable
	// there. That is 5.113 exactly, and 5.113 was *fixed*: the whole
	// explanation of it sits three hundred and ninety lines above this, on
	// aclExtendedMark, which exists because of it. The fix reached
	// `acl.is_extended` and not its sibling, and nothing noticed for want
	// of a FreeBSD 14 in the loop -- the live ACL test failed there for
	// months and the failure was read as the machine rather than the code.
	//
	// So the question is asked once, in one place, and both callers use it.
	// A second implementation of "is this ACL extended" is what produced
	// the divergence; removing it is the fix rather than correcting the
	// flags. DIVERGENCE 5.113.
	extended, err := aclExtendedMark(c, path, true)
	if err != nil {
		return nil, err
	}
	if !extended {
		return aclMutateResult(c, false, fmt.Sprintf("%s already has only the trivial ACL implied by its mode.", path), nil), nil
	}

	change := value.MapOf("acl", states.Change("extended", "trivial"))
	comment := fmt.Sprintf("%s's ACL would be reduced to the trivial one implied by its mode.", path)
	if c.Test {
		return aclMutateResult(c, true, comment, change), nil
	}

	// `-b -n`, not `-b`. On a FreeBSD POSIX.1e file `setfacl -b` keeps the
	// mask entry: the ACL still lists `mask::r--`, `ls` still marks it `+`,
	// and this function reported a change on every run without ever
	// leaving a trivial ACL. `-n` (do not recalculate the mask) with `-b`
	// drops it. Measured on FreeBSD 15.1 on UFS `-o acls`, UFS
	// `-o nfsv4acls` and ZFS, and on Debian 13's setfacl 2.3.2, whose `-b`
	// already removed the mask: `-b -n` left the trivial ACL on all four.
	// DIVERGENCE 5.185.
	argv := []string{"setfacl", "-b", "-n"}
	if recursive {
		argv = append(argv, "-R")
	}
	argv = append(argv, path)
	if err := runSetfacl(c, argv, path, "wipe the ACL on"); err != nil {
		return nil, err
	}
	return aclMutateResult(c, true, fmt.Sprintf("%s's ACL was reduced to the trivial one implied by its mode.", path), change), nil
}
