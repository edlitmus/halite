package builtin

import (
	"fmt"
	"os/user"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerUser installs the user and group modules.
//
// Reading is done through os/user, which reads the account databases the
// platform actually uses — including NSS on Linux and nsswitch on the
// BSDs — rather than parsing /etc/passwd and getting a wrong answer on a
// host with LDAP or a directory service. Writing shells out to the
// platform's own tool, because account creation has platform-specific side
// effects (home directory skeletons, mail spools, shadow entries) that are
// not worth reimplementing.
func registerUser(r *Registries) {
	registerUserExec(r)
	registerUserStates(r)
}

// accountTool describes how one platform creates and modifies accounts.
type accountTool struct {
	// Platform is the GOOS this tool belongs to.
	Platform string
	// AddUser builds the argument vector that creates a user.
	AddUser func(u userSpec) []string
	// ModUser builds the vector that modifies one.
	ModUser func(u userSpec) []string
	// DelUser builds the vector that removes one.
	DelUser func(name string, removeHome bool) []string
	// AddGroup, DelGroup do the same for groups.
	AddGroup func(name string, gid int64, system bool) []string
	DelGroup func(name string) []string
	// Binary is the program the vectors invoke, checked before use.
	Binary string
}

// userSpec is the requested state of one account.
type userSpec struct {
	Name       string
	UID        int64
	GID        int64
	Home       string
	Shell      string
	Comment    string
	Groups     []string
	CreateHome bool
	System     bool
	// Password is a hash, never a plaintext, and never reaches an
	// argument vector. See user_password.go.
	Password string
	// UserGroup asks for a primary group named after the account. Nil
	// means the platform's default, which is what a tree that does not
	// mention it wants.
	UserGroup *bool
	// Unique requires the uid to be unused. False is useradd's -o, and
	// is how an estate gives a second name to uid 0 deliberately.
	Unique bool
	// RemoveGroups makes Groups the account's complete supplementary set,
	// so a membership the tree does not name is removed. False, the
	// default, makes Groups the groups the account must be in and leaves
	// every other membership alone. See diffAccount.
	RemoveGroups bool
}

// freebsdTool drives pw(8), which is FreeBSD's single account tool.
var freebsdTool = accountTool{
	Platform: "freebsd",
	Binary:   "pw",
	AddUser: func(u userSpec) []string {
		argv := []string{"pw", "useradd", "-n", u.Name}
		if u.UID > 0 {
			argv = append(argv, "-u", strconv.FormatInt(u.UID, 10))
			// pw spells the duplicate-uid permission the same way.
			if !u.Unique {
				argv = append(argv, "-o")
			}
		}
		if u.GID > 0 {
			argv = append(argv, "-g", strconv.FormatInt(u.GID, 10))
		}
		if u.Home != "" {
			argv = append(argv, "-d", u.Home)
		}
		if u.Shell != "" {
			argv = append(argv, "-s", u.Shell)
		}
		if u.Comment != "" {
			argv = append(argv, "-c", u.Comment)
		}
		if len(u.Groups) > 0 {
			argv = append(argv, "-G", strings.Join(u.Groups, ","))
		}
		if u.CreateHome {
			argv = append(argv, "-m")
		}
		return argv
	},
	ModUser: func(u userSpec) []string {
		argv := []string{"pw", "usermod", "-n", u.Name}
		if u.UID > 0 {
			argv = append(argv, "-u", strconv.FormatInt(u.UID, 10))
		}
		if u.GID > 0 {
			argv = append(argv, "-g", strconv.FormatInt(u.GID, 10))
		}
		if u.Home != "" {
			argv = append(argv, "-d", u.Home)
		}
		if u.Shell != "" {
			argv = append(argv, "-s", u.Shell)
		}
		if u.Comment != "" {
			argv = append(argv, "-c", u.Comment)
		}
		if len(u.Groups) > 0 {
			argv = append(argv, "-G", strings.Join(u.Groups, ","))
		}
		return argv
	},
	DelUser: func(name string, removeHome bool) []string {
		argv := []string{"pw", "userdel", "-n", name}
		if removeHome {
			argv = append(argv, "-r")
		}
		return argv
	},
	AddGroup: func(name string, gid int64, system bool) []string {
		argv := []string{"pw", "groupadd", "-n", name}
		if gid > 0 {
			argv = append(argv, "-g", strconv.FormatInt(gid, 10))
		}
		// pw has no -r: a system group on FreeBSD is one whose gid is
		// below 1000, which is the caller's choice of gid rather than a
		// flag. Nothing is added, and nothing is silently dropped
		// either, because groupPresent refuses `system` there by name.
		return argv
	},
	DelGroup: func(name string) []string { return []string{"pw", "groupdel", "-n", name} },
}

// linuxTool drives useradd, usermod, userdel, groupadd, and groupdel.
var linuxTool = accountTool{
	Platform: "linux",
	Binary:   "useradd",
	AddUser: func(u userSpec) []string {
		argv := []string{"useradd"}
		if u.UID > 0 {
			argv = append(argv, "-u", strconv.FormatInt(u.UID, 10))
			// -o permits a uid another account already has, which is
			// how a second name for uid 0 is created deliberately. It
			// means nothing without -u, so it is only passed with one.
			if !u.Unique {
				argv = append(argv, "-o")
			}
		}
		if u.GID > 0 {
			argv = append(argv, "-g", strconv.FormatInt(u.GID, 10))
		}
		if u.Home != "" {
			argv = append(argv, "-d", u.Home)
		}
		if u.Shell != "" {
			argv = append(argv, "-s", u.Shell)
		}
		if u.Comment != "" {
			argv = append(argv, "-c", u.Comment)
		}
		if len(u.Groups) > 0 {
			argv = append(argv, "-G", strings.Join(u.Groups, ","))
		}
		if u.System {
			argv = append(argv, "-r")
		}
		if u.UserGroup != nil {
			// -U creates a group named after the account and makes it
			// primary; -N does not. Without either, useradd follows
			// USERGROUPS_ENAB in login.defs.
			if *u.UserGroup {
				argv = append(argv, "-U")
			} else {
				argv = append(argv, "-N")
			}
		}
		if u.CreateHome {
			argv = append(argv, "-m")
		} else {
			argv = append(argv, "-M")
		}
		return append(argv, u.Name)
	},
	ModUser: func(u userSpec) []string {
		argv := []string{"usermod"}
		if u.UID > 0 {
			argv = append(argv, "-u", strconv.FormatInt(u.UID, 10))
			if !u.Unique {
				argv = append(argv, "-o")
			}
		}
		if u.GID > 0 {
			argv = append(argv, "-g", strconv.FormatInt(u.GID, 10))
		}
		if u.Home != "" {
			argv = append(argv, "-d", u.Home)
		}
		if u.Shell != "" {
			argv = append(argv, "-s", u.Shell)
		}
		if u.Comment != "" {
			argv = append(argv, "-c", u.Comment)
		}
		if len(u.Groups) > 0 {
			argv = append(argv, "-G", strings.Join(u.Groups, ","))
		}
		return append(argv, u.Name)
	},
	DelUser: func(name string, removeHome bool) []string {
		argv := []string{"userdel"}
		if removeHome {
			argv = append(argv, "-r")
		}
		return append(argv, name)
	},
	AddGroup: func(name string, gid int64, system bool) []string {
		argv := []string{"groupadd"}
		if system {
			// -r takes the gid from the system range of login.defs,
			// which is what keeps a service group out of the range
			// useradd hands to people.
			argv = append(argv, "-r")
		}
		if gid > 0 {
			argv = append(argv, "-g", strconv.FormatInt(gid, 10))
		}
		return append(argv, name)
	},
	DelGroup: func(name string) []string { return []string{"groupdel", name} },
}

// pickAccountTool chooses the platform's account tool.
func pickAccountTool(c *exec.Context) (accountTool, error) {
	for _, t := range []accountTool{freebsdTool, linuxTool} {
		if t.Platform == runtime.GOOS && c.Which(t.Binary) != "" {
			return t, nil
		}
	}
	// A tool that is present but not this platform's is still better than
	// refusing outright, which is what a minimal container needs.
	for _, t := range []accountTool{freebsdTool, linuxTool} {
		if c.Which(t.Binary) != "" {
			return t, nil
		}
	}
	return accountTool{}, fmt.Errorf(
		"no account management tool was found on this node (%s); halite drives pw on FreeBSD and useradd on Linux", runtime.GOOS)
}

func registerUserExec(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "user", Function: "info",
				Doc:      "Return an account's details, or an empty mapping when it does not exist.",
				Params:   []signature.Param{req("name", signature.String, "The account.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return userInfo(states.Str(args, "name", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "user", Function: "list_users",
				Doc:      "Return the account names this node knows about.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return listAccountNames(), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "group", Function: "info",
				Doc:      "Return a group's details, or an empty mapping when it does not exist.",
				Params:   []signature.Param{req("name", signature.String, "The group.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return groupInfo(states.Str(args, "name", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "user", Function: "chgroups",
				Doc: "Set an account's supplementary groups.",
				Params: []signature.Param{
					req("name", signature.String, "The account."),
					req("groups", signature.List, "The supplementary groups."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				name := states.Str(args, "name", "")
				groups := states.Strings(args, "groups")
				if c.Test {
					return true, nil
				}
				if runtime.GOOS == "darwin" {
					err := macUserSetGroups(c, name, groups, false)
					return err == nil, err
				}
				tool, err := pickAccountTool(c)
				if err != nil {
					return nil, err
				}
				_, err = c.Run(exec.Command{Argv: tool.ModUser(userSpec{Name: name, Groups: groups})})
				return err == nil, err
			},
		},
	)
}

// userInfo reads an account through os/user, so a directory-backed account
// is found the same way the operating system finds it.
func userInfo(name string) (*value.Map, error) {
	u, err := user.Lookup(name)
	if err != nil {
		if _, ok := err.(user.UnknownUserError); ok {
			return value.NewMap(0), nil
		}
		return nil, err
	}
	groups := []any{}
	if ids, err := u.GroupIds(); err == nil {
		var names []string
		for _, id := range ids {
			if g, err := user.LookupGroupId(id); err == nil {
				names = append(names, g.Name)
				continue
			}
			names = append(names, id)
		}
		sort.Strings(names)
		for _, n := range names {
			groups = append(groups, n)
		}
	}
	uid, _ := strconv.ParseInt(u.Uid, 10, 64)
	gid, _ := strconv.ParseInt(u.Gid, 10, 64)
	return value.MapOf(
		"name", u.Username,
		"uid", uid,
		"gid", gid,
		"home", u.HomeDir,
		"fullname", u.Name,
		"groups", groups,
	), nil
}

func groupInfo(name string) (*value.Map, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		if _, ok := err.(user.UnknownGroupError); ok {
			return value.NewMap(0), nil
		}
		return nil, err
	}
	gid, _ := strconv.ParseInt(g.Gid, 10, 64)
	return value.MapOf("name", g.Name, "gid", gid), nil
}

// listAccountNames enumerates the accounts this node knows about.
//
// os/user has no enumeration call, so this reads /etc/passwd directly and
// says so: on a host whose accounts come from a directory service the list
// is the local subset only, which is a documented limitation rather than a
// silent one.
func listAccountNames() []any {
	var out []any
	for _, line := range readLines("/etc/passwd") {
		name, _, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, name)
	}
	if out == nil {
		return []any{}
	}
	return out
}

func registerUserStates(r *Registries) {
	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "user", Function: "present",
				Doc: "Ensure an account exists with the given attributes.",
				Params: []signature.Param{
					nameParam("The account. Defaults to the state ID."),
					opt("uid", signature.Int, nil, "The numeric user id."),
					opt("gid", signature.Any, nil, "The primary group: a numeric id, or the name of an existing group, which Salt also accepts."),
					opt("home", signature.Path, "", "The home directory."),
					opt("shell", signature.Path, "", "The login shell."),
					opt("fullname", signature.String, "", "The comment field."),
					opt("groups", signature.List, nil, "Supplementary groups the account must be in. Other memberships are left alone unless remove_groups is set."),
					opt("remove_groups", signature.Bool, false, "Make groups the complete supplementary set, removing the account from any group not listed. Off by default, unlike Salt, so that a membership added by hand is not taken away by a tree that never named it."),
					opt("createhome", signature.Bool, true, "Create the home directory."),
					opt("system", signature.Bool, false, "Create a system account."),
					opt("password", signature.String, "", "The password hash. Passed to the account tool on standard input, never in an argument vector."),
					opt("usergroup", signature.Bool, nil, "Give the account a primary group named after it. Unset follows the platform default."),
					opt("unique", signature.Bool, true, "Require the uid to be unused. False allows a second account to share one, which is useradd's -o."),
					opt("enforce_password", signature.Bool, true, "Reset the password when the stored hash differs. False sets it only when the account has none, so a rotated password is left alone."),
					opt("mindays", signature.Int, nil, "Minimum days between password changes. Linux only; chage -m."),
					opt("maxdays", signature.Int, nil, "Maximum days between password changes. Linux only; chage -M."),
					opt("warndays", signature.Int, nil, "Days of warning before a password expires. Linux only; chage -W."),
					opt("inactdays", signature.Int, nil, "Days after expiry before the account is locked. Linux only; chage -I."),
					opt("expire", signature.Int, nil, "Account expiry, in days since the epoch. Linux only; chage -E."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: userPresent,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "user", Function: "absent",
				Doc: "Ensure an account does not exist.",
				Params: []signature.Param{
					nameParam("The account. Defaults to the state ID."),
					opt("purge", signature.Bool, false, "Also remove the home directory."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: userAbsent,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "group", Function: "present",
				Doc: "Ensure a group exists, with the members it names.",
				Params: []signature.Param{
					nameParam("The group. Defaults to the state ID."),
					opt("gid", signature.Int, nil, "The numeric group id."),
					opt("system", signature.Bool, false, "Create a system group, from the range the platform reserves for them."),
					opt("members", signature.List, nil, "The accounts the group holds. This is the whole list: anyone not named is removed."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: groupPresent,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "group", Function: "absent",
				Doc:        "Ensure a group does not exist.",
				Params:     []signature.Param{nameParam("The group. Defaults to the state ID.")},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: groupAbsent,
		},
	)
}

// resolveGID reads the `gid` argument, which names a group either way.
//
// Salt's own documentation says so -- "Either a group name or gid can be
// used" -- and it resolves a name through `file.group_to_gid`. This took
// a number only, so the ordinary case of giving a user a primary group
// by name could not be expressed at all: an estate's tree writing
// `gid: postfix` was refused rather than looked up.
//
// A name that no group has is an error naming it, rather than a silent
// zero -- gid 0 is root's, and defaulting to it is the one wrong answer
// that would look like it worked.
func resolveGID(args *value.Map) (int64, error) {
	v, ok := args.Get("gid")
	if !ok || v == nil {
		return 0, nil
	}
	if name, isStr := v.(string); isStr {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			// A group named by its digits is still a gid.
			if n, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
				return n, nil
			}
			g, err := user.LookupGroup(trimmed)
			if err != nil {
				return 0, fmt.Errorf("the group %q named by `gid` does not exist: %w", trimmed, err)
			}
			n, err := strconv.ParseInt(g.Gid, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("the group %q has gid %q, which is not a number", trimmed, g.Gid)
			}
			return n, nil
		}
		return 0, nil
	}
	return states.Int(args, "gid", 0), nil
}

func specFrom(args *value.Map) userSpec {
	gid, _ := resolveGID(args)
	return userSpec{
		Name:       states.Str(args, "name", ""),
		UID:        states.Int(args, "uid", 0),
		GID:        gid,
		Home:       states.Str(args, "home", ""),
		Shell:      states.Str(args, "shell", ""),
		Comment:    states.Str(args, "fullname", ""),
		Groups:     states.Strings(args, "groups"),
		CreateHome: states.Bool(args, "createhome", true),
		System:     states.Bool(args, "system", false),
		Password:   states.Str(args, "password", ""),
		UserGroup:  optionalBool(args, "usergroup"),
		Unique:     states.Bool(args, "unique", true),

		RemoveGroups: states.Bool(args, "remove_groups", false),
	}
}

// optionalBool distinguishes "not mentioned" from "false", which matters
// for an option whose unmentioned behaviour is the platform's default
// rather than either value.
func optionalBool(args *value.Map, name string) *bool {
	v, ok := args.Get(name)
	if !ok || v == nil {
		return nil
	}
	b := value.Truthy(v)
	return &b
}

func userPresent(c *exec.Context, args *value.Map) (states.Result, error) {
	if runtime.GOOS == "darwin" {
		return macUserPresentState(c, args)
	}
	if _, err := resolveGID(args); err != nil {
		return states.False(fmt.Sprintf("%v.", err)), nil
	}
	if why := agingUnsupported(args); why != "" {
		return states.False(why), nil
	}
	spec := specFrom(args)
	if spec.Name == "" {
		return states.False("This state needs an account name."), nil
	}
	if why := removeGroupsUnsupported(spec); why != "" {
		return states.False(why), nil
	}
	tool, err := pickAccountTool(c)
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}

	current, err := userInfo(spec.Name)
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

	// The password is compared separately: it does not live in the
	// account record, and reading it needs the same privilege as setting
	// it. A state that cannot read the stored hash cannot tell whether
	// it converged, and reporting a change every run would be worse than
	// saying so.
	passwordDiffers := false
	if spec.Password != "" {
		stored, _, err := currentHash(spec.Name)
		if err != nil {
			return states.False(fmt.Sprintf(
				"The password for %s could not be compared: %v", spec.Name, err)), nil
		}
		// `enforce_password: false` means "set it if there is none, and
		// otherwise leave whatever is there". An estate uses it where a
		// password is rotated out of band and the tree only seeds it.
		if stored != "" && !states.Bool(args, "enforce_password", true) {
			stored = spec.Password
		}
		if stored != spec.Password {
			passwordDiffers = true
			// The hashes are not reported. What changed is enough, and a
			// job return carrying a hash is a hash in every returner,
			// event bus, and log the estate has.
			changes.Set("password", states.Change("(unchanged)", "(set)"))
		}
	}

	// Password ageing lives in the shadow file rather than the account
	// record, so it is compared separately, like the password.
	wantAging := agingFrom(args)
	var haveAging shadowAging
	if agingRequested(args) && exists {
		var err error
		haveAging, _, err = readAging(spec.Name)
		if err != nil {
			return states.False(fmt.Sprintf(
				"The password ageing for %s could not be read: %v", spec.Name, err)), nil
		}
	}
	if agingRequested(args) {
		diffAging(wantAging, haveAging, changes)
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

	// A change that is only the password does not need the account tool
	// run at all, and running usermod with no attributes to set is a
	// needless write to the passwd database.
	if exists && changes.Len() == 1 && passwordDiffers && agingArgv(spec.Name, wantAging, haveAging) == nil {
		if err := setPassword(c, tool, spec.Name, spec.Password); err != nil {
			return states.False(fmt.Sprintf("The password for %s could not be set: %v", spec.Name, err)), nil
		}
		return states.Changed(fmt.Sprintf("The password for %s was set.", spec.Name), changes), nil
	}

	argv := tool.AddUser(spec)
	if exists {
		// ModUser is handed only what changed about groups. It used to be
		// handed the spec, so any change -- a shell, a comment -- also
		// sent `-G <the tree's list>`, which replaces the whole
		// supplementary set; measured on an Ubuntu runner and on FreeBSD
		// 15.1 (DIVERGENCE 5.138). Now `-G` goes only when the groups
		// differ, and carries the full set the account should end with.
		mod := spec
		mod.Groups = nil
		if changes.Has("groups") {
			mod.Groups, _ = wantedGroups(supplementaryGroups(current), spec.Groups, spec.RemoveGroups)
		}
		argv = tool.ModUser(mod)
	}
	if _, err := c.Run(exec.Command{Argv: argv}); err != nil {
		return states.False(fmt.Sprintf("The account %s could not be %s: %v", spec.Name, verb, err)), nil
	}
	if passwordDiffers || (!exists && spec.Password != "") {
		if err := setPassword(c, tool, spec.Name, spec.Password); err != nil {
			return states.False(fmt.Sprintf(
				"The account %s was %s but its password could not be set: %v", spec.Name, verb, err)), nil
		}
	}
	// Ageing is applied after the account exists, which is why it is not
	// part of the useradd argument vector: chage needs a shadow entry to
	// edit, and a new account has one only once useradd has run.
	if agingRequested(args) {
		if !exists {
			haveAging, _, _ = readAging(spec.Name)
		}
		if err := applyAging(c, spec.Name, wantAging, haveAging); err != nil {
			return states.False(fmt.Sprintf(
				"The account %s was %s but its password ageing could not be set: %v",
				spec.Name, verb, err)), nil
		}
	}
	return states.Changed(fmt.Sprintf("The account %s was %s.", spec.Name, verb), changes), nil
}

// diffAccount records only the attributes the state actually asked for, so
// a state that names a shell and nothing else does not report the home
// directory as a change.
func diffAccount(current *value.Map, spec userSpec, changes *value.Map) {
	if spec.UID > 0 {
		if cur, _ := current.Get("uid"); cur != spec.UID {
			changes.Set("uid", states.Change(cur, spec.UID))
		}
	}
	if spec.GID > 0 {
		if cur, _ := current.Get("gid"); cur != spec.GID {
			changes.Set("gid", states.Change(cur, spec.GID))
		}
	}
	if spec.Home != "" {
		if cur, _ := current.Get("home"); cur != spec.Home {
			changes.Set("home", states.Change(cur, spec.Home))
		}
	}
	if spec.Comment != "" {
		if cur, _ := current.Get("fullname"); cur != spec.Comment {
			changes.Set("fullname", states.Change(cur, spec.Comment))
		}
	}
	if len(spec.Groups) > 0 || spec.RemoveGroups {
		cur, _ := current.Get("groups")
		have := supplementaryGroups(current)
		if final, differs := wantedGroups(have, spec.Groups, spec.RemoveGroups); differs {
			anyFinal := make([]any, len(final))
			for i, g := range final {
				anyFinal[i] = g
			}
			changes.Set("groups", states.Change(cur, anyFinal))
		}
	}
	// The shell is not in os/user's record, so it is read separately; a
	// node whose accounts come from a directory service may not expose it
	// at all, in which case the state cannot tell and does not guess. A
	// reader that already put a shell in the current map — macUserInfo
	// does, from dscl — is preferred over re-reading /etc/passwd.
	if spec.Shell != "" {
		cur := ""
		if s, ok := current.Get("shell"); ok && s != nil {
			cur = value.KeyString(s)
		}
		if cur == "" {
			cur = shellOf(spec.Name)
		}
		if cur != "" && cur != spec.Shell {
			changes.Set("shell", states.Change(cur, spec.Shell))
		}
	}
}

// removeGroupsUnsupported refuses remove_groups with no groups to keep.
//
// That would mean "remove every supplementary group", and `-G` is only
// ever sent with a non-empty list: what `usermod -G ""` and `pw usermod
// -G ""` do to an account has not been run on either platform. So it is
// refused by name rather than done by a path nobody has watched.
func removeGroupsUnsupported(spec userSpec) string {
	if spec.RemoveGroups && len(spec.Groups) == 0 {
		return fmt.Sprintf("%s: remove_groups with no groups would remove the account from every "+
			"supplementary group, which this build does not do; name the groups to keep.", spec.Name)
	}
	return ""
}

// supplementaryGroups is the account's groups without its primary one.
//
// userInfo reads the list from os/user's GroupIds, which includes the
// primary group; `-G` on usermod and pw is the supplementary list, which
// does not. Comparing the two as they stand would make remove_groups
// see the primary group as a membership the tree did not name, on every
// run, and try to remove it.
func supplementaryGroups(current *value.Map) []string {
	var primary string
	if gid, ok := current.Get("gid"); ok {
		if g, err := user.LookupGroupId(value.KeyString(gid)); err == nil {
			primary = g.Name
		}
	}
	cur, _ := current.Get("groups")
	list, _ := cur.([]any)
	var out []string
	for _, g := range list {
		if name := value.KeyString(g); name != primary {
			out = append(out, name)
		}
	}
	return out
}

// wantedGroups is the supplementary set the account should end with, and
// whether that differs from the one it has.
//
// Without remove, it is what the account has plus whatever it was asked
// for and lacks: `groups` names the groups an account must be in. With
// remove, it is exactly what was asked for.
//
// The result is the whole set, not the additions, because that is what
// `usermod -G` and `pw usermod -G` take: both *replace* the supplementary
// list. Handing them the tree's list alone, on a run that changed the
// shell and nothing about groups, was how a hand-added group was
// stripped on Linux and FreeBSD (DIVERGENCE 5.138).
func wantedGroups(have, want []string, remove bool) ([]string, bool) {
	haveSet := map[string]bool{}
	for _, g := range have {
		haveSet[g] = true
	}
	wantSet := map[string]bool{}
	for _, g := range want {
		wantSet[g] = true
	}
	differs := false
	for g := range wantSet {
		if !haveSet[g] {
			differs = true
		}
	}
	if remove {
		for g := range haveSet {
			if !wantSet[g] {
				differs = true
			}
		}
	}
	final := map[string]bool{}
	for g := range wantSet {
		final[g] = true
	}
	if !remove {
		for g := range haveSet {
			final[g] = true
		}
	}
	out := make([]string, 0, len(final))
	for g := range final {
		out = append(out, g)
	}
	sort.Strings(out)
	return out, differs
}

// shellOf reads an account's login shell from /etc/passwd. It returns an
// empty string when the account is not local, which the caller reads as
// "cannot tell".
func shellOf(name string) string {
	for _, line := range readLines("/etc/passwd") {
		fields := strings.Split(line, ":")
		if len(fields) >= 7 && fields[0] == name {
			return fields[6]
		}
	}
	return ""
}

func userAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
	if runtime.GOOS == "darwin" {
		return macUserAbsentState(c, args)
	}
	name := states.Str(args, "name", "")
	tool, err := pickAccountTool(c)
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}
	current, err := userInfo(name)
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
	argv := tool.DelUser(name, states.Bool(args, "purge", false))
	if _, err := c.Run(exec.Command{Argv: argv}); err != nil {
		return states.False(fmt.Sprintf("The account %s could not be removed: %v", name, err)), nil
	}
	return states.Changed(fmt.Sprintf("The account %s was removed.", name), changes), nil
}

func groupPresent(c *exec.Context, args *value.Map) (states.Result, error) {
	if runtime.GOOS == "darwin" {
		return macGroupPresentState(c, args)
	}
	name := states.Str(args, "name", "")
	gid := states.Int(args, "gid", 0)
	system := states.Bool(args, "system", false)
	if system && runtime.GOOS != "linux" {
		return states.False(fmt.Sprintf(
			"This state asks for a system group, which is groupadd's -r and has no equivalent on %s; "+
				"on FreeBSD a system group is one whose gid is below 1000, so give `gid` instead.",
			runtime.GOOS)), nil
	}
	tool, err := pickAccountTool(c)
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}
	current, err := groupInfo(name)
	if err != nil {
		return states.False(fmt.Sprintf("The group %s could not be read: %v", name, err)), nil
	}

	wantMembers, hasMembers := groupMembersRequested(args)

	if current.Len() > 0 {
		if gid > 0 {
			if cur, _ := current.Get("gid"); cur != gid {
				return states.False(fmt.Sprintf(
					"The group %s exists with a different gid; halite does not renumber a group, because every file owned by it would be orphaned.", name)), nil
			}
		}
		if !hasMembers {
			return states.True(fmt.Sprintf("The group %s already exists.", name)), nil
		}
		return reconcileGroupMembers(c, name, wantMembers)
	}

	changes := value.MapOf(name, states.Change(nil, "present"))
	if hasMembers {
		changes.Set("members", states.Change(nil, wantMembers))
	}
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The group %s would be created.", name), changes), nil
	}
	if _, err := c.Run(exec.Command{Argv: tool.AddGroup(name, gid, system)}); err != nil {
		return states.False(fmt.Sprintf("The group %s could not be created: %v", name, err)), nil
	}
	if hasMembers {
		if _, err := reconcileGroupMembers(c, name, wantMembers); err != nil {
			return states.False(fmt.Sprintf(
				"The group %s was created but its members could not be set: %v", name, err)), nil
		}
	}
	return states.Changed(fmt.Sprintf("The group %s was created.", name), changes), nil
}

// groupMembersRequested reads `members`, distinguishing "not mentioned"
// from "empty". An empty list is a request to have no members, which is
// different from saying nothing about them.
func groupMembersRequested(args *value.Map) ([]string, bool) {
	v, ok := args.Get("members")
	if !ok || v == nil {
		return nil, false
	}
	return states.Strings(args, "members"), true
}

// reconcileGroupMembers makes a group's membership exactly the list
// given.
//
// `members` is the whole list, not an addition: Salt's own
// documentation distinguishes it from `addusers` and `delusers` that
// way, and a tree that writes `members: [root]` on `wheel` means that
// nobody else is in wheel. Treating it as an addition would leave an
// account in a privileged group that the tree had just been edited to
// remove, which is the failure worth being exact about.
//
// The list is the group's own member list -- supplementary membership --
// and it is changed with the tool that edits a group's member list
// directly, which differs by platform (DIVERGENCE 5.151):
//
//   - Linux: gpasswd(1).
//   - FreeBSD: `pw groupmod -m` and `-d`. FreeBSD has no gpasswd, and
//     this used to refuse there with "needs gpasswd(1)" -- on the
//     platform that carries most of production.
//   - macOS: `dseditgroup -o edit -a` and `-d`, read back from Open
//     Directory's GroupMembership. macGroupPresentState did not read
//     `members` at all, and reported every list as already in place.
func reconcileGroupMembers(c *exec.Context, name string, want []string) (states.Result, error) {
	have, err := groupMembersOf(c, name)
	if err != nil {
		return states.False(fmt.Sprintf("The members of %s could not be read: %v", name, err)), nil
	}

	add, remove := membershipDiff(have, want)
	if len(add) == 0 && len(remove) == 0 {
		return states.True(fmt.Sprintf("The group %s already has exactly those members.", name)), nil
	}
	changes := value.MapOf("members", states.Change(have, want))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The members of %s would be set.", name), changes), nil
	}
	addArgv, removeArgv, why := groupMemberTools(c)
	if why != "" {
		return states.False(fmt.Sprintf("Setting the members of %s %s.", name, why)), nil
	}
	for _, u := range add {
		if _, err := c.Run(exec.Command{Argv: addArgv(u, name)}); err != nil {
			return states.False(fmt.Sprintf("%s could not be added to %s: %v", u, name, err)), nil
		}
	}
	for _, u := range remove {
		if _, err := c.Run(exec.Command{Argv: removeArgv(u, name)}); err != nil {
			return states.False(fmt.Sprintf("%s could not be removed from %s: %v", u, name, err)), nil
		}
	}
	return states.Changed(fmt.Sprintf("The members of %s were set.", name), changes), nil
}

// groupMembersOf is a group's supplementary member list, from where this
// platform keeps it.
func groupMembersOf(c *exec.Context, name string) ([]string, error) {
	if runtime.GOOS == "darwin" {
		info, err := macGroupInfo(c, name)
		if err != nil {
			return nil, err
		}
		list, _ := info.Get("members")
		items, _ := list.([]any)
		out := make([]string, 0, len(items))
		for _, m := range items {
			out = append(out, value.KeyString(m))
		}
		sort.Strings(out)
		return out, nil
	}
	return groupMemberNames(name)
}

// groupMemberTools returns how this platform adds an account to a
// group's member list and removes one, or why it cannot.
func groupMemberTools(c *exec.Context) (add, remove func(user, group string) []string, why string) {
	switch runtime.GOOS {
	case "darwin":
		return func(u, g string) []string { return []string{"dseditgroup", "-o", "edit", "-a", u, "-t", "user", g} },
			func(u, g string) []string { return []string{"dseditgroup", "-o", "edit", "-d", u, "-t", "user", g} }, ""
	case "freebsd":
		if c.Which("pw") == "" {
			return nil, nil, "needs pw(8), which is not on this node's PATH"
		}
		return func(u, g string) []string { return []string{"pw", "groupmod", g, "-m", u} },
			func(u, g string) []string { return []string{"pw", "groupmod", g, "-d", u} }, ""
	}
	if c.Which("gpasswd") == "" {
		return nil, nil, "needs gpasswd(1), which is not on this node's PATH"
	}
	return func(u, g string) []string { return []string{"gpasswd", "-a", u, g} },
		func(u, g string) []string { return []string{"gpasswd", "-d", u, g} }, ""
}

// membershipDiff reports who to add and who to remove, as sets.
func membershipDiff(have, want []string) (add, remove []string) {
	in := func(list []string, s string) bool {
		for _, v := range list {
			if v == s {
				return true
			}
		}
		return false
	}
	for _, w := range want {
		if !in(have, w) {
			add = append(add, w)
		}
	}
	for _, h := range have {
		if !in(want, h) {
			remove = append(remove, h)
		}
	}
	return add, remove
}

func groupAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
	if runtime.GOOS == "darwin" {
		return macGroupAbsentState(c, args)
	}
	name := states.Str(args, "name", "")
	tool, err := pickAccountTool(c)
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}
	current, err := groupInfo(name)
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
	if _, err := c.Run(exec.Command{Argv: tool.DelGroup(name)}); err != nil {
		return states.False(fmt.Sprintf("The group %s could not be removed: %v", name, err)), nil
	}
	return states.Changed(fmt.Sprintf("The group %s was removed.", name), changes), nil
}
