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
	AddGroup func(name string, gid int64) []string
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
}

// freebsdTool drives pw(8), which is FreeBSD's single account tool.
var freebsdTool = accountTool{
	Platform: "freebsd",
	Binary:   "pw",
	AddUser: func(u userSpec) []string {
		argv := []string{"pw", "useradd", "-n", u.Name}
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
	AddGroup: func(name string, gid int64) []string {
		argv := []string{"pw", "groupadd", "-n", name}
		if gid > 0 {
			argv = append(argv, "-g", strconv.FormatInt(gid, 10))
		}
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
	AddGroup: func(name string, gid int64) []string {
		argv := []string{"groupadd"}
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
				tool, err := pickAccountTool(c)
				if err != nil {
					return nil, err
				}
				spec := userSpec{Name: states.Str(args, "name", ""), Groups: states.Strings(args, "groups")}
				if c.Test {
					return true, nil
				}
				_, err = c.Run(exec.Command{Argv: tool.ModUser(spec)})
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
					opt("gid", signature.Int, nil, "The numeric primary group id."),
					opt("home", signature.Path, "", "The home directory."),
					opt("shell", signature.Path, "", "The login shell."),
					opt("fullname", signature.String, "", "The comment field."),
					opt("groups", signature.List, nil, "Supplementary groups."),
					opt("createhome", signature.Bool, true, "Create the home directory."),
					opt("system", signature.Bool, false, "Create a system account."),
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
				Doc: "Ensure a group exists.",
				Params: []signature.Param{
					nameParam("The group. Defaults to the state ID."),
					opt("gid", signature.Int, nil, "The numeric group id."),
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

func specFrom(args *value.Map) userSpec {
	return userSpec{
		Name:       states.Str(args, "name", ""),
		UID:        states.Int(args, "uid", 0),
		GID:        states.Int(args, "gid", 0),
		Home:       states.Str(args, "home", ""),
		Shell:      states.Str(args, "shell", ""),
		Comment:    states.Str(args, "fullname", ""),
		Groups:     states.Strings(args, "groups"),
		CreateHome: states.Bool(args, "createhome", true),
		System:     states.Bool(args, "system", false),
	}
}

func userPresent(c *exec.Context, args *value.Map) (states.Result, error) {
	spec := specFrom(args)
	if spec.Name == "" {
		return states.False("This state needs an account name."), nil
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

	argv := tool.AddUser(spec)
	if exists {
		argv = tool.ModUser(spec)
	}
	if _, err := c.Run(exec.Command{Argv: argv}); err != nil {
		return states.False(fmt.Sprintf("The account %s could not be %s: %v", spec.Name, verb, err)), nil
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
	if len(spec.Groups) > 0 {
		cur, _ := current.Get("groups")
		have := map[string]bool{}
		if list, ok := cur.([]any); ok {
			for _, g := range list {
				have[value.KeyString(g)] = true
			}
		}
		var missing []string
		for _, g := range spec.Groups {
			if !have[g] {
				missing = append(missing, g)
			}
		}
		if len(missing) > 0 {
			changes.Set("groups", states.Change(cur, spec.Groups))
		}
	}
	// The shell is not in os/user's record, so it is read separately; a
	// node whose accounts come from a directory service may not expose it
	// at all, in which case the state cannot tell and does not guess.
	if spec.Shell != "" {
		if cur := shellOf(spec.Name); cur != "" && cur != spec.Shell {
			changes.Set("shell", states.Change(cur, spec.Shell))
		}
	}
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
	name := states.Str(args, "name", "")
	gid := states.Int(args, "gid", 0)
	tool, err := pickAccountTool(c)
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}
	current, err := groupInfo(name)
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
	if _, err := c.Run(exec.Command{Argv: tool.AddGroup(name, gid)}); err != nil {
		return states.False(fmt.Sprintf("The group %s could not be created: %v", name, err)), nil
	}
	return states.Changed(fmt.Sprintf("The group %s was created.", name), changes), nil
}

func groupAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
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
