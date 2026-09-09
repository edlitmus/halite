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

// mac_user, mac_group and mac_shadow: SPEC section 15.3's macOS account
// row.
//
// macOS keeps accounts in Open Directory, not in `/etc/passwd`, and the
// `useradd`/`pw` model the other platforms share does not reach it. So
// these drive `dscl(1)` and `dseditgroup(1)`, the way Salt's `mac_user`
// and `mac_group` do, and the virtual `user` and `group` states branch
// to the helpers here on darwin — which is what finally gives
// `user.present` and `group.present` something to reach on a Mac.
//
// **Reads go through `dscl -plist . -read`.** The plain `dscl -read`
// output wraps a multi-word value onto a continuation line and cannot be
// told from two values; the plist form is unambiguous, and mac_defaults
// already carries a reader for it.
//
// **Writes are a sequence, not one command.** `dscl . -create` sets one
// attribute per call, so making a user is six of them. `macUserCreate`
// runs them in order and stops at the first failure, and the comment on
// each says what it is for.
//
// **There is no offline password hash.** macOS stores a
// SALTED-SHA512-PBKDF2 dictionary in a binary plist inside Open
// Directory; there is no `/etc/shadow` line and no portable hash a tree
// can carry. `mac_shadow.set_password` therefore takes a plaintext, as
// Salt's does and as `dscl . -passwd` requires, and `user.present`
// refuses a `password:` on darwin rather than pretend a Linux crypt hash
// means anything here.

var macAccountOnly = macOnly // the three modules share it; named for the reader

// ---- dscl plumbing ----

// dsRun runs one directory-service command with IgnoreExitCode set, so a
// non-zero exit is data to inspect rather than an error from c.Run —
// `dscl -read` exits 56 for a record that is not there, which every
// caller here has to tell apart from a real failure.
func dsRun(c *exec.Context, argv ...string) (exec.Result, error) {
	return c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
}

// dsRecordNotFound is dscl's answer for a path that is not there.
func dsRecordNotFound(res exec.Result) bool {
	s := res.Stderr + res.Stdout
	return strings.Contains(s, "eDSRecordNotFound") || strings.Contains(s, "-14136")
}

// dsclRead reads one Directory Services record as a map of the standard
// attribute name (without the `dsAttrTypeStandard:` prefix) to its
// values. The second return is whether the record exists at all.
func dsclRead(c *exec.Context, path string) (map[string][]string, bool, error) {
	if c.Which("dscl") == "" {
		return nil, false, fmt.Errorf("mac_user: `dscl` was not found; this build's macOS account modules need it")
	}
	res, err := dsRun(c, "dscl", "-plist", ".", "-read", path)
	if err != nil {
		return nil, false, err
	}
	if res.Code != 0 {
		if dsRecordNotFound(res) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("dscl -read %s: %s", path, firstLine(res.Stderr+res.Stdout))
	}
	v, err := parsePlist([]byte(res.Stdout))
	if err != nil {
		return nil, false, fmt.Errorf("dscl -read %s: %w", path, err)
	}
	m, ok := v.(*value.Map)
	if !ok {
		return nil, false, fmt.Errorf("dscl -read %s: top level is not a dict", path)
	}
	out := map[string][]string{}
	for _, k := range m.StringKeys() {
		attr := k
		if i := strings.LastIndex(k, ":"); i >= 0 {
			attr = k[i+1:]
		}
		raw, _ := m.Get(k)
		switch t := raw.(type) {
		case []any:
			for _, e := range t {
				out[attr] = append(out[attr], value.KeyString(e))
			}
		case nil:
		default:
			out[attr] = append(out[attr], value.KeyString(t))
		}
	}
	return out, true, nil
}

func dsFirst(rec map[string][]string, attr string) string {
	if v := rec[attr]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// dsclList runs `dscl . -list <path>` and returns the names, sorted.
func dsclList(c *exec.Context, path string) ([]any, error) {
	if c.Which("dscl") == "" {
		return nil, fmt.Errorf("mac_user: `dscl` was not found")
	}
	res, err := dsRun(c, "dscl", ".", "-list", path)
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("dscl -list %s: %s", path, firstLine(res.Stderr+res.Stdout))
	}
	var names []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			names = append(names, f[0])
		}
	}
	sort.Strings(names)
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = n
	}
	return out, nil
}

// dsclCreate sets one attribute, overwriting whatever was there.
func dsclCreate(c *exec.Context, path, attr string, values ...string) error {
	argv := append([]string{"dscl", ".", "-create", path}, append([]string{attr}, values...)...)
	if attr == "" {
		argv = []string{"dscl", ".", "-create", path}
	}
	res, err := dsRun(c, argv...)
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("dscl -create %s %s: %s", path, attr, firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

func dsclDelete(c *exec.Context, path string) error {
	res, err := dsRun(c, "dscl", ".", "-delete", path)
	if err != nil {
		return err
	}
	if res.Code != 0 && !dsRecordNotFound(res) {
		return fmt.Errorf("dscl -delete %s: %s", path, firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

// ---- reading an account or a group ----

// macUserInfo returns an account in the same shape userInfo does, so the
// state diff is the same on every platform.
func macUserInfo(c *exec.Context, name string) (*value.Map, error) {
	rec, ok, err := dsclRead(c, "/Users/"+name)
	if err != nil || !ok {
		return value.NewMap(0), err
	}
	uid, _ := strconv.ParseInt(dsFirst(rec, "UniqueID"), 10, 64)
	gid, _ := strconv.ParseInt(dsFirst(rec, "PrimaryGroupID"), 10, 64)
	groups, _ := macUserGroups(c, name)
	return value.MapOf(
		"name", name,
		"uid", uid,
		"gid", gid,
		"home", dsFirst(rec, "NFSHomeDirectory"),
		"shell", dsFirst(rec, "UserShell"),
		"fullname", dsFirst(rec, "RealName"),
		"groups", groups,
	), nil
}

// macUserGroups is the supplementary groups an account is a direct
// member of, by GroupMembership. Nested membership is not resolved,
// which matches `dscl` and what a state manages.
func macUserGroups(c *exec.Context, name string) ([]any, error) {
	res, err := dsRun(c, "dscl", ".", "-search", "/Groups", "GroupMembership", name)
	if err != nil {
		return []any{}, err
	}
	if res.Code != 0 {
		return []any{}, nil
	}
	seen := map[string]bool{}
	var names []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		// `dscl -search` prints "<group>\t\tGroupMembership = (...)"; the
		// group name is the first field of the record's first line.
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && f[0] != "" && !seen[f[0]] {
			seen[f[0]] = true
			names = append(names, f[0])
		}
	}
	sort.Strings(names)
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = n
	}
	return out, nil
}

func macGroupInfo(c *exec.Context, name string) (*value.Map, error) {
	rec, ok, err := dsclRead(c, "/Groups/"+name)
	if err != nil || !ok {
		return value.NewMap(0), err
	}
	gid, _ := strconv.ParseInt(dsFirst(rec, "PrimaryGroupID"), 10, 64)
	members := []any{}
	for _, m := range rec["GroupMembership"] {
		members = append(members, m)
	}
	return value.MapOf("name", name, "gid", gid, "members", members), nil
}

// ---- creating and changing an account ----

func macUserShellDefault() string { return "/bin/zsh" }

// macUserCreate makes an account with `dscl . -create`, one attribute at
// a time, stopping at the first failure.
func macUserCreate(c *exec.Context, spec userSpec) error {
	path := "/Users/" + spec.Name
	uid := spec.UID
	if uid <= 0 {
		n, err := macNextID(c, "/Users", "UniqueID", spec.System)
		if err != nil {
			return err
		}
		uid = n
	}
	gid := spec.GID
	if gid <= 0 {
		gid = 20 // staff, the macOS default primary group
	}
	home := spec.Home
	if home == "" {
		home = "/Users/" + spec.Name
	}
	shell := spec.Shell
	if shell == "" {
		shell = macUserShellDefault()
	}
	real := spec.Comment
	if real == "" {
		real = spec.Name
	}

	steps := []struct {
		attr string
		vals []string
	}{
		{"", nil}, // the record itself
		{"RealName", []string{real}},
		{"UniqueID", []string{strconv.FormatInt(uid, 10)}},
		{"PrimaryGroupID", []string{strconv.FormatInt(gid, 10)}},
		{"UserShell", []string{shell}},
		{"NFSHomeDirectory", []string{home}},
	}
	if spec.System {
		steps = append(steps, struct {
			attr string
			vals []string
		}{"IsHidden", []string{"1"}})
	}
	for _, s := range steps {
		if err := dsclCreate(c, path, s.attr, s.vals...); err != nil {
			return err
		}
	}
	for _, g := range spec.Groups {
		if err := macGroupAddMember(c, g, spec.Name); err != nil {
			return err
		}
	}
	if spec.CreateHome && !spec.System && c.Which("createhomedir") != "" {
		res, err := dsRun(c, "createhomedir", "-c", "-u", spec.Name)
		if err != nil {
			return err
		}
		if res.Code != 0 {
			return fmt.Errorf("createhomedir -u %s: %s", spec.Name, firstLine(res.Stderr+res.Stdout))
		}
	}
	return nil
}

// macNextID returns the lowest free id at or above the range's base.
func macNextID(c *exec.Context, path, attr string, system bool) (int64, error) {
	res, err := dsRun(c, "dscl", ".", "-list", path, attr)
	if err != nil {
		return 0, err
	}
	used := map[int64]bool{}
	for _, line := range strings.Split(res.Stdout, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if n, err := strconv.ParseInt(f[len(f)-1], 10, 64); err == nil {
			used[n] = true
		}
	}
	base, ceil := int64(501), int64(9000)
	if system {
		base, ceil = 200, 400
	}
	for n := base; n < ceil; n++ {
		if !used[n] {
			return n, nil
		}
	}
	return 0, fmt.Errorf("mac_user: no free id in %d..%d", base, ceil)
}

// macUserChange sets one attribute of an existing account.
func macUserChange(c *exec.Context, name, attr, val string) error {
	return dsclCreate(c, "/Users/"+name, attr, val)
}

// macUserDelete removes an account, and its home only when asked and only
// when the path is a plausible home under /Users or /private/var.
func macUserDelete(c *exec.Context, name string, removeHome bool) error {
	var home string
	if removeHome {
		if rec, ok, _ := dsclRead(c, "/Users/"+name); ok {
			home = dsFirst(rec, "NFSHomeDirectory")
		}
	}
	if err := dsclDelete(c, "/Users/"+name); err != nil {
		return err
	}
	if removeHome && macSafeHome(home) {
		res, err := dsRun(c, "rm", "-rf", home)
		if err != nil {
			return err
		}
		if res.Code != 0 {
			return fmt.Errorf("removing %s: %s", home, firstLine(res.Stderr+res.Stdout))
		}
	}
	return nil
}

// macSafeHome guards the recursive delete: a home directory is under
// /Users or /private/var and is not one of those roots itself.
func macSafeHome(home string) bool {
	home = strings.TrimRight(home, "/")
	if home == "" {
		return false
	}
	for _, root := range []string{"/Users", "/private/var", "/var"} {
		if strings.HasPrefix(home, root+"/") && len(strings.Trim(strings.TrimPrefix(home, root+"/"), "/")) > 0 {
			return true
		}
	}
	return false
}

// ---- group writes ----

func macGroupCreate(c *exec.Context, name string, gid int64) error {
	argv := []string{"dseditgroup", "-o", "create"}
	if gid > 0 {
		argv = append(argv, "-i", strconv.FormatInt(gid, 10))
	}
	argv = append(argv, name)
	return macRun(c, argv, "dseditgroup -o create "+name)
}

func macGroupDelete(c *exec.Context, name string) error {
	return macRun(c, []string{"dseditgroup", "-o", "delete", name}, "dseditgroup -o delete "+name)
}

func macGroupAddMember(c *exec.Context, group, user string) error {
	return macRun(c, []string{"dseditgroup", "-o", "edit", "-a", user, "-t", "user", group},
		"dseditgroup -o edit -a "+user+" "+group)
}

func macGroupDelMember(c *exec.Context, group, user string) error {
	return macRun(c, []string{"dseditgroup", "-o", "edit", "-d", user, "-t", "user", group},
		"dseditgroup -o edit -d "+user+" "+group)
}

func macRun(c *exec.Context, argv []string, what string) error {
	if c.Which(argv[0]) == "" {
		return fmt.Errorf("mac account modules need `%s`, which was not found", argv[0])
	}
	res, err := dsRun(c, argv...)
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s: %s", what, firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

// ---- the execution modules ----

func registerMacUser(r *Registries) {
	uname := req("name", signature.String, "The account.")
	gname := req("name", signature.String, "The group.")

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_user", Function: "info",
				Doc:       "Return an account's details, or an empty mapping when it does not exist.",
				Params:    []signature.Param{uname},
				Returns:   "name, uid, gid, home, shell, fullname and the supplementary groups",
				TestMode:  signature.TestNotApplicable,
				Platforms: macAccountOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return macUserInfo(c, states.Str(args, "name", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_user", Function: "list_users",
				Doc:       "Return every account name Open Directory knows on this node, sorted.",
				TestMode:  signature.TestNotApplicable,
				Platforms: macAccountOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return dsclList(c, "/Users")
			},
		},
		macUserAddModule(uname),
		macUserDeleteModule(uname),
		macUserChangeModule("chuid", "UniqueID", signature.Int, "The numeric user id."),
		macUserChangeModule("chgid", "PrimaryGroupID", signature.Int, "The numeric primary group id."),
		macUserChangeModule("chshell", "UserShell", signature.String, "The login shell."),
		macUserChangeModule("chhome", "NFSHomeDirectory", signature.Path, "The home directory. The directory itself is not moved."),
		macUserChangeModule("chfullname", "RealName", signature.String, "The full name, which is the RealName attribute."),
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_user", Function: "chgroups",
				Doc: "Set an account's supplementary groups. Without `append`, membership " +
					"of a group not in the list is removed.",
				Params: []signature.Param{
					uname,
					req("groups", signature.List, "The supplementary groups."),
					opt("append", signature.Bool, false, "Add to the current groups rather than replacing them."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  macAccountOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := macUserSetGroups(c, states.Str(args, "name", ""),
					states.Strings(args, "groups"), states.Bool(args, "append", false))
				return err == nil, err
			},
		},
	)

	// ---- mac_group ----
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_group", Function: "info",
				Doc:       "Return a group's details, or an empty mapping when it does not exist.",
				Params:    []signature.Param{gname},
				Returns:   "name, gid and the direct members",
				TestMode:  signature.TestNotApplicable,
				Platforms: macAccountOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return macGroupInfo(c, states.Str(args, "name", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_group", Function: "list_groups",
				Doc:       "Return every group name on this node, sorted.",
				TestMode:  signature.TestNotApplicable,
				Platforms: macAccountOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return dsclList(c, "/Groups")
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_group", Function: "members",
				Doc:       "Return a group's direct members, sorted.",
				Params:    []signature.Param{gname},
				TestMode:  signature.TestNotApplicable,
				Platforms: macAccountOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				g, err := macGroupInfo(c, states.Str(args, "name", ""))
				if err != nil {
					return nil, err
				}
				m, _ := g.Get("members")
				if m == nil {
					return []any{}, nil
				}
				return m, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_group", Function: "add",
				Doc: "Create a group.",
				Params: []signature.Param{
					gname,
					opt("gid", signature.Int, nil, "The numeric group id. Assigned if omitted."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"}, Platforms: macAccountOnly, Section: "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := macGroupCreate(c, states.Str(args, "name", ""), states.Int(args, "gid", 0))
				return err == nil, err
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_group", Function: "delete",
				Doc:     "Remove a group.",
				Params:  []signature.Param{gname},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"}, Platforms: macAccountOnly, Section: "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := macGroupDelete(c, states.Str(args, "name", ""))
				return err == nil, err
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_group", Function: "chgid",
				Doc: "Change a group's numeric id. Files owned by the old id are not " +
					"rewritten, so this is a rare thing to want.",
				Params:  []signature.Param{gname, req("gid", signature.Int, "The new numeric group id.")},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"}, Platforms: macAccountOnly, Section: "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := dsclCreate(c, "/Groups/"+states.Str(args, "name", ""),
					"PrimaryGroupID", strconv.FormatInt(states.Int(args, "gid", 0), 10))
				return err == nil, err
			},
		},
		macGroupMemberModule("adduser", true),
		macGroupMemberModule("deluser", false),
	)

	// ---- mac_shadow ----
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_shadow", Function: "info",
				Doc: "Return what can be read about an account's password state: its name, " +
					"and whether a password is set. The day-count policy fields Salt's " +
					"`shadow.info` carries on Linux are `pwpolicy`'s on macOS and are not read here.",
				Params:    []signature.Param{uname},
				Returns:   "a mapping with name and passwd",
				TestMode:  signature.TestNotApplicable,
				Platforms: macAccountOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return macShadowInfo(c, states.Str(args, "name", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_shadow", Function: "set_password",
				Doc: "Set an account's password. macOS keeps no offline hash, so this takes " +
					"a plaintext and runs `dscl . -passwd`, as Salt's does — which means the " +
					"plaintext is briefly in the process table, because macOS offers no " +
					"standard-input path for it.",
				Params: []signature.Param{
					uname,
					req("password", signature.String, "The new password, in plaintext."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"}, Platforms: macAccountOnly, Section: "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := macShadowSetPassword(c, states.Str(args, "name", ""), states.Str(args, "password", ""))
				return err == nil, err
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_shadow", Function: "del_password",
				Doc:     "Disable password login by setting the account's Password to `*`.",
				Params:  []signature.Param{uname},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"}, Platforms: macAccountOnly, Section: "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := dsclCreate(c, "/Users/"+states.Str(args, "name", ""), "Password", "*")
				return err == nil, err
			},
		},
	)
}

func macUserAddModule(uname signature.Param) exec.Module {
	return exec.Module{
		Sig: signature.Signature{
			Module: "mac_user", Function: "add",
			Doc: "Create an account with `dscl . -create`.",
			Params: []signature.Param{
				uname,
				opt("uid", signature.Int, nil, "The numeric user id. Assigned above 500 if omitted."),
				opt("gid", signature.Int, nil, "The primary group id. Defaults to 20, staff."),
				opt("home", signature.Path, "", "The home directory. Defaults to /Users/<name>."),
				opt("shell", signature.Path, "", "The login shell. Defaults to /bin/zsh."),
				opt("fullname", signature.String, "", "The RealName attribute. Defaults to the account name."),
				opt("groups", signature.List, nil, "Supplementary groups to add the account to."),
				opt("createhome", signature.Bool, true, "Create the home directory with `createhomedir`."),
				opt("system", signature.Bool, false, "A hidden account with a low id and no home."),
			},
			Mutates: true, TestMode: signature.TestReliable,
			Privileges: []string{"root"}, Platforms: macAccountOnly, Section: "15.3",
		},
		Fn: func(c *exec.Context, args *value.Map) (any, error) {
			if c.Test {
				return true, nil
			}
			err := macUserCreate(c, specFrom(args))
			return err == nil, err
		},
	}
}

func macUserDeleteModule(uname signature.Param) exec.Module {
	return exec.Module{
		Sig: signature.Signature{
			Module: "mac_user", Function: "delete",
			Doc: "Remove an account.",
			Params: []signature.Param{
				uname,
				opt("remove", signature.Bool, false, "Also remove the home directory, when it is under /Users or /var."),
			},
			Mutates: true, TestMode: signature.TestReliable,
			Privileges: []string{"root"}, Platforms: macAccountOnly, Section: "15.3",
		},
		Fn: func(c *exec.Context, args *value.Map) (any, error) {
			if c.Test {
				return true, nil
			}
			err := macUserDelete(c, states.Str(args, "name", ""), states.Bool(args, "remove", false))
			return err == nil, err
		},
	}
}

func macUserChangeModule(fn, attr string, t signature.Type, doc string) exec.Module {
	return exec.Module{
		Sig: signature.Signature{
			Module: "mac_user", Function: fn,
			Doc: "Change an account's " + attr + " attribute.",
			Params: []signature.Param{
				req("name", signature.String, "The account."),
				req("value", t, doc),
			},
			Mutates: true, TestMode: signature.TestReliable,
			Privileges: []string{"root"}, Platforms: macAccountOnly, Section: "15.3",
		},
		Fn: func(c *exec.Context, args *value.Map) (any, error) {
			if c.Test {
				return true, nil
			}
			err := macUserChange(c, states.Str(args, "name", ""), attr, states.Str(args, "value", ""))
			return err == nil, err
		},
	}
}

func macGroupMemberModule(fn string, add bool) exec.Module {
	verb := "Add an account to"
	if !add {
		verb = "Remove an account from"
	}
	return exec.Module{
		Sig: signature.Signature{
			Module: "mac_group", Function: fn,
			Doc: verb + " a group.",
			Params: []signature.Param{
				req("name", signature.String, "The group."),
				req("username", signature.String, "The account."),
			},
			Mutates: true, TestMode: signature.TestReliable,
			Privileges: []string{"root"}, Platforms: macAccountOnly, Section: "15.3",
		},
		Fn: func(c *exec.Context, args *value.Map) (any, error) {
			if c.Test {
				return true, nil
			}
			g, u := states.Str(args, "name", ""), states.Str(args, "username", "")
			var err error
			if add {
				err = macGroupAddMember(c, g, u)
			} else {
				err = macGroupDelMember(c, g, u)
			}
			return err == nil, err
		},
	}
}

// macUserSetGroups converges an account's supplementary groups.
func macUserSetGroups(c *exec.Context, name string, want []string, appendOnly bool) error {
	have, err := macUserGroups(c, name)
	if err != nil {
		return err
	}
	haveSet := map[string]bool{}
	for _, g := range have {
		haveSet[value.KeyString(g)] = true
	}
	wantSet := map[string]bool{}
	for _, g := range want {
		wantSet[g] = true
		if !haveSet[g] {
			if err := macGroupAddMember(c, g, name); err != nil {
				return err
			}
		}
	}
	if appendOnly {
		return nil
	}
	for _, g := range have {
		gs := value.KeyString(g)
		if !wantSet[gs] {
			if err := macGroupDelMember(c, gs, name); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- mac_shadow ----

func macShadowInfo(c *exec.Context, name string) (*value.Map, error) {
	rec, ok, err := dsclRead(c, "/Users/"+name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return value.NewMap(0), nil
	}
	passwd := "unknown"
	switch {
	case dsFirst(rec, "Password") == "*" || dsFirst(rec, "Password") == "":
		if len(rec["AuthenticationAuthority"]) > 0 || hasShadowHash(c, name) {
			passwd = "set"
		} else {
			passwd = "*"
		}
	default:
		passwd = "set"
	}
	return value.MapOf("name", name, "passwd", passwd), nil
}

// hasShadowHash reports whether Open Directory holds a password hash for
// the account. It needs root; unprivileged, dscl answers "No such key"
// whether or not one is there, so the answer is "unknown" and this
// returns false without claiming otherwise.
func hasShadowHash(c *exec.Context, name string) bool {
	res, err := dsRun(c, "dscl", ".", "-read", "/Users/"+name, "ShadowHashData")
	if err != nil {
		return false
	}
	return res.Code == 0 && strings.Contains(res.Stdout, "ShadowHashData")
}

func macShadowSetPassword(c *exec.Context, name, password string) error {
	if name == "" {
		return fmt.Errorf("mac_shadow.set_password needs an account name")
	}
	return macRun(c, []string{"dscl", ".", "-passwd", "/Users/" + name, password},
		"dscl -passwd /Users/"+name)
}

// ---- the virtual user/group states on darwin ----
//
// The state functions in user.go branch here on darwin. userInfo through
// os/user already works on a Mac, but the writes do not, so these mirror
// userPresent/userAbsent/groupPresent/groupAbsent with the dscl helpers
// in place of the account tool.

func macUserPresentState(c *exec.Context, args *value.Map) (states.Result, error) {
	spec := specFrom(args)
	if spec.Name == "" {
		return states.False("This state needs an account name."), nil
	}
	if spec.Password != "" {
		return states.False(fmt.Sprintf(
			"%s: a password hash cannot be applied on macOS — Open Directory keeps a "+
				"SALTED-SHA512-PBKDF2 dictionary, not a crypt hash a tree can carry. Set the "+
				"password out of band, or with `mac_shadow.set_password` and a plaintext.",
			spec.Name)), nil
	}

	current, err := macUserInfo(c, spec.Name)
	if err != nil {
		return states.False(fmt.Sprintf("The account %s could not be read: %v", spec.Name, err)), nil
	}
	exists := current.Len() > 0

	changes := value.NewMap(6)
	if !exists {
		changes.Set(spec.Name, states.Change(nil, "present"))
	} else {
		diffAccount(current, spec, changes)
	}
	if changes.Len() == 0 {
		return states.True(fmt.Sprintf("The account %s is already in the requested state.", spec.Name)), nil
	}
	verb := "updated"
	if !exists {
		verb = "created"
	}
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The account %s would be %s.", spec.Name, verb), changes), nil
	}

	if !exists {
		if err := macUserCreate(c, spec); err != nil {
			return states.False(fmt.Sprintf("The account %s could not be created: %v", spec.Name, err)), nil
		}
		return states.Changed(fmt.Sprintf("The account %s was created.", spec.Name), changes), nil
	}
	if err := macApplyUserChanges(c, spec, changes); err != nil {
		return states.False(fmt.Sprintf("The account %s could not be updated: %v", spec.Name, err)), nil
	}
	return states.Changed(fmt.Sprintf("The account %s was updated.", spec.Name), changes), nil
}

// macApplyUserChanges writes only the attributes diffAccount flagged.
func macApplyUserChanges(c *exec.Context, spec userSpec, changes *value.Map) error {
	set := func(attr, val string) error { return macUserChange(c, spec.Name, attr, val) }
	if changes.Has("uid") {
		if err := set("UniqueID", strconv.FormatInt(spec.UID, 10)); err != nil {
			return err
		}
	}
	if changes.Has("gid") {
		if err := set("PrimaryGroupID", strconv.FormatInt(spec.GID, 10)); err != nil {
			return err
		}
	}
	if changes.Has("home") {
		if err := set("NFSHomeDirectory", spec.Home); err != nil {
			return err
		}
	}
	if changes.Has("shell") {
		if err := set("UserShell", spec.Shell); err != nil {
			return err
		}
	}
	if changes.Has("fullname") {
		if err := set("RealName", spec.Comment); err != nil {
			return err
		}
	}
	if changes.Has("groups") {
		if err := macUserSetGroups(c, spec.Name, spec.Groups, true); err != nil {
			return err
		}
	}
	return nil
}

func macUserAbsentState(c *exec.Context, args *value.Map) (states.Result, error) {
	name := states.Str(args, "name", "")
	current, err := macUserInfo(c, name)
	if err != nil {
		return states.False(fmt.Sprintf("The account %s could not be read: %v", name, err)), nil
	}
	if current.Len() == 0 {
		return states.True(fmt.Sprintf("The account %s is already absent.", name)), nil
	}
	changes := value.MapOf(name, states.Change("present", nil))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The account %s would be removed.", name), changes), nil
	}
	if err := macUserDelete(c, name, states.Bool(args, "purge", false)); err != nil {
		return states.False(fmt.Sprintf("The account %s could not be removed: %v", name, err)), nil
	}
	return states.Changed(fmt.Sprintf("The account %s was removed.", name), changes), nil
}

func macGroupPresentState(c *exec.Context, args *value.Map) (states.Result, error) {
	name := states.Str(args, "name", "")
	gid := states.Int(args, "gid", 0)
	current, err := macGroupInfo(c, name)
	if err != nil {
		return states.False(fmt.Sprintf("The group %s could not be read: %v", name, err)), nil
	}
	if current.Len() > 0 {
		if gid <= 0 {
			return states.True(fmt.Sprintf("The group %s already exists.", name)), nil
		}
		if cur, _ := current.Get("gid"); cur == gid {
			return states.True(fmt.Sprintf("The group %s already exists with gid %d.", name, gid)), nil
		}
		return states.False(fmt.Sprintf(
			"The group %s exists with a different gid; halite does not renumber a group, because every file owned by it would be orphaned.", name)), nil
	}
	changes := value.MapOf(name, states.Change(nil, "present"))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The group %s would be created.", name), changes), nil
	}
	if err := macGroupCreate(c, name, gid); err != nil {
		return states.False(fmt.Sprintf("The group %s could not be created: %v", name, err)), nil
	}
	return states.Changed(fmt.Sprintf("The group %s was created.", name), changes), nil
}

func macGroupAbsentState(c *exec.Context, args *value.Map) (states.Result, error) {
	name := states.Str(args, "name", "")
	current, err := macGroupInfo(c, name)
	if err != nil {
		return states.False(fmt.Sprintf("The group %s could not be read: %v", name, err)), nil
	}
	if current.Len() == 0 {
		return states.True(fmt.Sprintf("The group %s is already absent.", name)), nil
	}
	changes := value.MapOf(name, states.Change("present", nil))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The group %s would be removed.", name), changes), nil
	}
	if err := macGroupDelete(c, name); err != nil {
		return states.False(fmt.Sprintf("The group %s could not be removed: %v", name, err)), nil
	}
	return states.Changed(fmt.Sprintf("The group %s was removed.", name), changes), nil
}
