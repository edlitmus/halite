package modules

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
)

func init() {
	register("user.present", userPresent)
	register("user.absent", userAbsent)
	register("group.present", groupPresent)
	register("group.absent", groupAbsent)
}

// passwdEntry is a parsed /etc/passwd line (FreeBSD and Linux).
type passwdEntry struct {
	uid, gid, gecos, home, shell string
	found                        bool
}

func readPasswd(name string) passwdEntry {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return passwdEntry{}
	}
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return passwdEntry{}
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) >= 7 && f[0] == name {
			return passwdEntry{uid: f[2], gid: f[3], gecos: f[4], home: f[5], shell: f[6], found: true}
		}
	}
	return passwdEntry{}
}

func userExists(name string) bool {
	switch runtime.GOOS {
	case "freebsd":
		_, _, rc, err := run("pw", "usershow", name)
		return err == nil && rc == 0
	case "windows":
		_, _, rc, err := run("net", "user", name)
		return err == nil && rc == 0
	default: // linux, darwin
		_, _, rc, err := run("id", "-u", name)
		return err == nil && rc == 0
	}
}

func currentGroups(name string) []string {
	out, _, rc, err := run("id", "-Gn", name)
	if err != nil || rc != 0 {
		return nil
	}
	return strings.Fields(out)
}

// mergeGroups unions current supplementary groups with wanted ones.
func mergeGroups(current, want []string) (merged []string, added []string) {
	seen := map[string]bool{}
	for _, g := range current {
		if !seen[g] {
			seen[g] = true
			merged = append(merged, g)
		}
	}
	for _, g := range want {
		if !seen[g] {
			seen[g] = true
			merged = append(merged, g)
			added = append(added, g)
		}
	}
	sort.Strings(added)
	return
}

// userPresent ensures a user exists with the given attributes. On FreeBSD
// this drives pw(8); on Linux useradd/usermod; macOS and Windows get
// existence management only.
//
//	deploy:
//	  user.present:
//	    - uid: 1050
//	    - shell: /bin/sh
//	    - groups:
//	      - wheel
//	    - createhome: true
func userPresent(c *Ctx, id string, args map[string]any) Result {
	name := Str(args, "name", id)
	uid := Str(args, "uid", "")
	shell := Str(args, "shell", "")
	home := Str(args, "home", "")
	gecos := Str(args, "gecos", "")
	groups := List(args, "groups")
	createHome := Bool(args, "createhome", true)
	system := Bool(args, "system", false)

	exists := userExists(name)

	if !exists {
		if c.Test {
			return resWould(fmt.Sprintf("user %s would be created", name))
		}
		var argv []string
		switch runtime.GOOS {
		case "freebsd":
			argv = []string{"pw", "useradd", name}
			if uid != "" {
				argv = append(argv, "-u", uid)
			}
			if shell != "" {
				argv = append(argv, "-s", shell)
			}
			if home != "" {
				argv = append(argv, "-d", home)
			}
			if gecos != "" {
				argv = append(argv, "-c", gecos)
			}
			if len(groups) > 0 {
				argv = append(argv, "-G", strings.Join(groups, ","))
			}
			if createHome {
				argv = append(argv, "-m")
			}
		case "darwin":
			argv = []string{"sysadminctl", "-addUser", name}
			if gecos != "" {
				argv = append(argv, "-fullName", gecos)
			}
			if shell != "" {
				argv = append(argv, "-shell", shell)
			}
			if home != "" {
				argv = append(argv, "-home", home)
			}
		case "windows":
			argv = []string{"net", "user", name, "/add"}
		default: // linux
			argv = []string{"useradd"}
			if uid != "" {
				argv = append(argv, "-u", uid)
			}
			if shell != "" {
				argv = append(argv, "-s", shell)
			}
			if home != "" {
				argv = append(argv, "-d", home)
			}
			if gecos != "" {
				argv = append(argv, "-c", gecos)
			}
			if len(groups) > 0 {
				argv = append(argv, "-G", strings.Join(groups, ","))
			}
			if createHome {
				argv = append(argv, "-m")
			} else {
				argv = append(argv, "-M")
			}
			if system {
				argv = append(argv, "-r")
			}
			argv = append(argv, name)
		}
		if err := svcExec(argv...); err != nil {
			return resFail("create user %s: %v", name, err)
		}
		return resChanged(fmt.Sprintf("user %s created", name), map[string]string{"created": name})
	}

	// Drift detection (FreeBSD/Linux via /etc/passwd; groups via id -Gn).
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return resOK(fmt.Sprintf("user %s exists (attribute management not supported on %s)", name, runtime.GOOS))
	}
	pw := readPasswd(name)
	type mod struct{ flag, val, label string }
	var mods []mod
	if uid != "" && pw.found && pw.uid != uid {
		mods = append(mods, mod{"-u", uid, "uid " + pw.uid + " -> " + uid})
	}
	if shell != "" && pw.found && pw.shell != shell {
		mods = append(mods, mod{"-s", shell, "shell " + pw.shell + " -> " + shell})
	}
	if home != "" && pw.found && pw.home != home {
		mods = append(mods, mod{"-d", home, "home " + pw.home + " -> " + home})
	}
	if gecos != "" && pw.found && pw.gecos != gecos {
		mods = append(mods, mod{"-c", gecos, "gecos updated"})
	}
	var groupsCSV string
	var added []string
	if len(groups) > 0 {
		merged, add := mergeGroups(currentGroups(name), groups)
		if len(add) > 0 {
			groupsCSV = strings.Join(merged, ",")
			added = add
		}
	}
	if len(mods) == 0 && groupsCSV == "" {
		return resOK(fmt.Sprintf("user %s is in the correct state", name))
	}
	if c.Test {
		var what []string
		for _, m := range mods {
			what = append(what, m.label)
		}
		if len(added) > 0 {
			what = append(what, "groups += "+strings.Join(added, ","))
		}
		return resWould(fmt.Sprintf("user %s would be updated: %s", name, strings.Join(what, "; ")))
	}
	changes := map[string]string{}
	for _, m := range mods {
		var argv []string
		if runtime.GOOS == "freebsd" {
			argv = []string{"pw", "usermod", name, m.flag, m.val}
		} else {
			argv = []string{"usermod", m.flag, m.val, name}
		}
		if err := svcExec(argv...); err != nil {
			return resFail("modify user %s: %v", name, err)
		}
		changes[m.flag] = m.label
	}
	if groupsCSV != "" {
		var argv []string
		if runtime.GOOS == "freebsd" {
			argv = []string{"pw", "usermod", name, "-G", groupsCSV}
		} else {
			argv = []string{"usermod", "-G", groupsCSV, name}
		}
		if err := svcExec(argv...); err != nil {
			return resFail("modify groups for %s: %v", name, err)
		}
		changes["groups"] = "added " + strings.Join(added, ",")
	}
	return resChanged(fmt.Sprintf("user %s updated", name), changes)
}

func userAbsent(c *Ctx, id string, args map[string]any) Result {
	name := Str(args, "name", id)
	purge := Bool(args, "purge", false) // also remove home directory
	if !userExists(name) {
		return resOK(fmt.Sprintf("user %s is already absent", name))
	}
	if c.Test {
		return resWould(fmt.Sprintf("user %s would be removed", name))
	}
	var argv []string
	switch runtime.GOOS {
	case "freebsd":
		argv = []string{"pw", "userdel", name}
		if purge {
			argv = append(argv, "-r")
		}
	case "darwin":
		argv = []string{"sysadminctl", "-deleteUser", name}
	case "windows":
		argv = []string{"net", "user", name, "/delete"}
	default:
		argv = []string{"userdel"}
		if purge {
			argv = append(argv, "-r")
		}
		argv = append(argv, name)
	}
	if err := svcExec(argv...); err != nil {
		return resFail("remove user %s: %v", name, err)
	}
	return resChanged(fmt.Sprintf("user %s removed", name), map[string]string{"removed": name})
}

func groupExists(name string) bool {
	switch runtime.GOOS {
	case "freebsd":
		_, _, rc, err := run("pw", "groupshow", name)
		return err == nil && rc == 0
	case "darwin":
		_, _, rc, err := run("dseditgroup", "-o", "read", name)
		return err == nil && rc == 0
	case "windows":
		_, _, rc, err := run("net", "localgroup", name)
		return err == nil && rc == 0
	default:
		_, _, rc, err := run("getent", "group", name)
		return err == nil && rc == 0
	}
}

func groupPresent(c *Ctx, id string, args map[string]any) Result {
	name := Str(args, "name", id)
	gid := Str(args, "gid", "")
	if groupExists(name) {
		return resOK(fmt.Sprintf("group %s exists", name))
	}
	if c.Test {
		return resWould(fmt.Sprintf("group %s would be created", name))
	}
	var argv []string
	switch runtime.GOOS {
	case "freebsd":
		argv = []string{"pw", "groupadd", name}
		if gid != "" {
			argv = append(argv, "-g", gid)
		}
	case "darwin":
		argv = []string{"dseditgroup", "-o", "create", name}
	case "windows":
		argv = []string{"net", "localgroup", name, "/add"}
	default:
		argv = []string{"groupadd"}
		if gid != "" {
			argv = append(argv, "-g", gid)
		}
		argv = append(argv, name)
	}
	if err := svcExec(argv...); err != nil {
		return resFail("create group %s: %v", name, err)
	}
	return resChanged(fmt.Sprintf("group %s created", name), map[string]string{"created": name})
}

func groupAbsent(c *Ctx, id string, args map[string]any) Result {
	name := Str(args, "name", id)
	if !groupExists(name) {
		return resOK(fmt.Sprintf("group %s is already absent", name))
	}
	if c.Test {
		return resWould(fmt.Sprintf("group %s would be removed", name))
	}
	var argv []string
	switch runtime.GOOS {
	case "freebsd":
		argv = []string{"pw", "groupdel", name}
	case "darwin":
		argv = []string{"dseditgroup", "-o", "delete", name}
	case "windows":
		argv = []string{"net", "localgroup", name, "/delete"}
	default:
		argv = []string{"groupdel", name}
	}
	if err := svcExec(argv...); err != nil {
		return resFail("remove group %s: %v", name, err)
	}
	return resChanged(fmt.Sprintf("group %s removed", name), map[string]string{"removed": name})
}
