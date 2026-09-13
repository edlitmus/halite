package builtin

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// aclPlatforms names the one platform this module has a real tool to run
// against.
//
// getfacl and setfacl also exist on Linux, but they are a different
// program from a different package (acl(1) from the acl-utils family)
// with a different grammar for the same idea — no `owner@`/`group@`, no
// `:allow`/`:deny` type field, a `-p` flag FreeBSD's version does not
// have. Building a second parser from the Linux man page rather than a
// captured run is exactly the mistake this project's ledger keeps
// finding: DIVERGENCE-shaped code written from a tool's own spelling
// instead of what it actually printed. Until this build runs on a
// Linux host and captures what its getfacl says, this module says only
// "freebsd" rather than a platform list that reads as tested and is
// not.
var aclPlatforms = []string{"freebsd"}

// registerACL installs the `acl` module of SPEC 15.2.
//
// # Two ACL families share one pair of tools, and this module speaks one of them
//
// FreeBSD's getfacl(1) and setfacl(1) read and write both NFSv4 ACLs
// (what ZFS uses — every mounted filesystem on the host this was built
// against) and POSIX.1e ACLs (what UFS uses, with the right mount
// option). The two are unrelated grammars: an NFSv4 entry is
// `tag:qualifier:perms:flags:type`, five colon-fields ending in
// `allow` or `deny`; a POSIX.1e entry is `tag:qualifier:perms`, three
// fields, no flags and no type. `getfacl -d` on an NFSv4 path answers
// "there are no default entries in NFSv4 ACLs" — captured verbatim
// from this host — which is FreeBSD's own way of saying the two
// families are not interchangeable.
//
// This build parses NFSv4 only. Every fixture in acl_test.go was
// captured from a real `getfacl`/`setfacl` run against a ZFS path on
// this host, because there was one to run against. There was no UFS
// filesystem reachable without root to make a POSIX.1e ACL to test
// against, and a parser built from the setfacl(1) EXAMPLES section
// instead of a captured run is the fixture-from-imagination mistake
// this project's ledger names four separate times. So rather than
// guess, parseACLEntryLine recognises the three-field shape on sight
// and refuses it by name: "this is a POSIX.1e ACL entry", not a parse
// error that looks like a bug. Extending this module to POSIX.1e is
// future work for whoever next has root on a UFS host.
//
// # What is not here
//
// Default ACLs (`getfacl -d` / `setfacl -d`) are POSIX.1e-only, per
// the tool's own answer quoted above, so a module that speaks NFSv4
// only has no default ACL to manage. `-k` (delete default entries) and
// `-n` (skip mask recalculation) are POSIX.1e options for the same
// reason and are not exposed either.
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
// every state run.
//
// # No states
//
// This is the exec module SPEC 15.2 names. A `acl.present`/`acl.absent`
// pair belongs with whoever wires SPEC 15.5's state list — it was not
// asked for here and folding it in unasked would be exactly the scope
// creep the file list for this task was drawn to prevent.
func registerACL(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "acl", Function: "get",
				Doc: "Return the NFSv4 ACL of a file or directory: its owner, its group, and every entry in evaluation order.",
				Params: []signature.Param{
					req("name", signature.Path, "The file or directory to read."),
					opt("follow_symlink", signature.Bool, true, "Follow a symlink rather than reading the ACL of the link itself."),
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
				Doc: "Add or update one NFSv4 ACL entry.",
				Params: []signature.Param{
					req("name", signature.Path, "The file or directory to change."),
					choice("tag", "user", "Who the entry names.", "user", "group", "owner@", "group@", "everyone@"),
					opt("qualifier", signature.String, "", "The user or group name (or numeric id). Required for user and group, refused for owner@, group@ and everyone@."),
					req("perms", signature.String, "The permissions: short form (rwp...), long form (read_data/write_data...), or a named set (full_set, modify_set, read_set, write_set)."),
					opt("flags", signature.String, "", "Inheritance flags: short form (fd...) or long form (file_inherit/dir_inherit...). Only meaningful on a directory."),
					choice("type", "allow", "Whether the entry grants or denies its permissions.", "allow", "deny"),
					opt("position", signature.Int, int64(-1), "Insert a brand-new entry at this position (counting from zero) instead of the front. Ignored when an entry already matches tag, qualifier and type."),
					opt("recursive", signature.Bool, false, "Apply to every file and directory beneath name too."),
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
				Doc: "Take every NFSv4 ACL entry matching a tag (and, for user and group, a qualifier) out.",
				Params: []signature.Param{
					req("name", signature.Path, "The file or directory to change."),
					choice("tag", "user", "Who the entry names.", "user", "group", "owner@", "group@", "everyone@"),
					opt("qualifier", signature.String, "", "The user or group name (or numeric id). Required for user and group, refused for owner@, group@ and everyone@."),
					choice("type", "", "Only remove entries of this type; empty removes both allow and deny.", "", "allow", "deny"),
					opt("recursive", signature.Bool, false, "Apply to every file and directory beneath name too."),
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
}

// ---- reading ----

// aclEntry is one line of an NFSv4 ACL, in the fields getfacl and
// setfacl agree on.
type aclEntry struct {
	tag, qualifier, permissions, flags, typ string
}

func (e aclEntry) asMap() *value.Map {
	return value.MapOf(
		"tag", e.tag,
		"qualifier", nilIfEmpty(e.qualifier),
		"permissions", e.permissions,
		"flags", e.flags,
		"type", e.typ,
	)
}

func aclToolPresent(c *exec.Context, tool string) error {
	if c.Which(tool) == "" {
		return fmt.Errorf("this node has no `%s`; it ships in FreeBSD's base system and every install has it", tool)
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
		default:
			entry, entryErr := parseACLEntryLine(line)
			if entryErr != nil {
				return "", "", nil, entryErr
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
// fields and never ends in either word — see registerACL's doc comment
// for why this build reports that shape by name instead of parsing it.
func parseACLEntryLine(line string) (aclEntry, error) {
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
		return aclEntry{}, fmt.Errorf(
			"%q is a POSIX.1e ACL entry; this build manages NFSv4 ACLs only, the kind ZFS uses, "+
				"and refuses to guess at a POSIX.1e entry rather than parse it wrong", line)
	default:
		return aclEntry{}, fmt.Errorf("an ACL entry could not be read: %q", line)
	}
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
	list := make([]any, len(entries))
	for i, e := range entries {
		list[i] = e.asMap()
	}
	return value.MapOf("owner", owner, "group", group, "entries", list), nil
}

func aclIsExtendedFn(c *exec.Context, args *value.Map) (any, error) {
	path := states.Str(args, "name", "")
	if path == "" {
		return nil, errors.New("an ACL needs a path")
	}
	if err := aclToolPresent(c, "getfacl"); err != nil {
		return nil, err
	}
	argv := []string{"getfacl", "-sq"}
	if !states.Bool(args, "follow_symlink", true) {
		argv = append(argv, "-h")
	}
	argv = append(argv, path)
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("getfacl could not be run: %w", err)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("getfacl could not read %s: %s", path, strings.TrimSpace(firstLine(res.Stderr)))
	}
	// -s prints nothing at all for a file whose ACL is exactly what its
	// mode already implies (acl_is_trivial_np(3)), and the full listing
	// otherwise. Verified live in both states; see acl_test.go.
	return strings.TrimSpace(res.Stdout) != "", nil
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
	case named && qualifier == "":
		return fmt.Errorf("tag %q needs a qualifier naming the user or group", tag)
	case !named && qualifier != "":
		return fmt.Errorf("tag %q takes no qualifier; owner@, group@ and everyone@ name no one else", tag)
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
// replacement of the first.
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

func aclSetFn(c *exec.Context, args *value.Map) (any, error) {
	path := states.Str(args, "name", "")
	if path == "" {
		return nil, errors.New("an ACL entry needs a path")
	}
	tag := states.Str(args, "tag", "user")
	qualifier := states.Str(args, "qualifier", "")
	permsRaw := states.Str(args, "perms", "")
	flagsRaw := states.Str(args, "flags", "")
	typ := states.Str(args, "type", "allow")
	position := states.Int(args, "position", -1)
	recursive := states.Bool(args, "recursive", false)

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

	_, _, current, err := readACL(c, path, true)
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

	if err := aclToolPresent(c, "setfacl"); err != nil {
		return nil, err
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
	res, runErr := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if runErr != nil {
		return nil, fmt.Errorf("setfacl could not be run: %w", runErr)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("setfacl could not change %s: %s", path, strings.TrimSpace(firstLine(res.Stderr)))
	}
	return aclMutateResult(c, true, fmt.Sprintf("%s's %s entry for %s was set to %s.", path, typ, target, after), change), nil
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

	if err := aclValidateTagQualifier(tag, qualifier); err != nil {
		return nil, err
	}

	_, _, current, err := readACL(c, path, true)
	if err != nil {
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

	if err := aclToolPresent(c, "setfacl"); err != nil {
		return nil, err
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
		res, runErr := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
		if runErr != nil {
			return nil, fmt.Errorf("setfacl could not be run: %w", runErr)
		}
		if res.Code != 0 {
			return nil, fmt.Errorf("setfacl could not remove entry %d from %s: %s", positions[i], path, strings.TrimSpace(firstLine(res.Stderr)))
		}
	}
	return aclMutateResult(c, true, fmt.Sprintf("%d ACL entr%s for %s removed from %s.", len(positions), aclPluralY(len(positions)), target, path), change), nil
}

func aclWipeFn(c *exec.Context, args *value.Map) (any, error) {
	path := states.Str(args, "name", "")
	if path == "" {
		return nil, errors.New("an ACL needs a path")
	}
	recursive := states.Bool(args, "recursive", false)

	if err := aclToolPresent(c, "getfacl"); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{Argv: []string{"getfacl", "-sq", path}, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("getfacl could not be run: %w", err)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("getfacl could not read %s: %s", path, strings.TrimSpace(firstLine(res.Stderr)))
	}
	if strings.TrimSpace(res.Stdout) == "" {
		return aclMutateResult(c, false, fmt.Sprintf("%s already has only the trivial ACL implied by its mode.", path), nil), nil
	}

	change := value.MapOf("acl", states.Change("extended", "trivial"))
	comment := fmt.Sprintf("%s's ACL would be reduced to the trivial one implied by its mode.", path)
	if c.Test {
		return aclMutateResult(c, true, comment, change), nil
	}

	if err := aclToolPresent(c, "setfacl"); err != nil {
		return nil, err
	}
	argv := []string{"setfacl", "-b"}
	if recursive {
		argv = append(argv, "-R")
	}
	argv = append(argv, path)
	res, runErr := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if runErr != nil {
		return nil, fmt.Errorf("setfacl could not be run: %w", runErr)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("setfacl could not wipe the ACL on %s: %s", path, strings.TrimSpace(firstLine(res.Stderr)))
	}
	return aclMutateResult(c, true, fmt.Sprintf("%s's ACL was reduced to the trivial one implied by its mode.", path), change), nil
}
