package builtin

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The `acl` states of SPEC 15.5, under Salt's names and with Salt's
// arguments: `acl.present`, `acl.absent`, `acl.list_present` and
// `acl.list_absent`, each taking `name`, `acl_type`, `acl_name` (or
// `acl_names`), `perms` and `recurse`.
//
// # They are acl.set and acl.remove, called once per path
//
// Nothing here reads getfacl or builds a setfacl argument. A state works
// out which paths it covers and calls aclSetFn or aclRemoveFn on each;
// those read the ACL, take the family's branch, compare in getfacl's own
// spelling, honour test mode and run setfacl. The state's own decisions
// are only the three below, and each exists because the execution
// function cannot make it.
//
// # Salt's arguments, on two families
//
// Salt's state is linux_acl, and its arguments are POSIX.1e's: an
// `acl_type` of user, group, mask or other, optionally prefixed `d:` or
// `default:` for a directory's default ACL; an `acl_name` that is empty
// for the owner's or owning group's own entry; `perms` as rwx letters.
// On a POSIX.1e path -- every Linux filesystem, and UFS mounted
// `-o acls` -- those mean exactly what they mean to Salt.
//
// An NFSv4 path -- ZFS, which is what this fleet's FreeBSD hosts run --
// takes the same arguments in NFSv4's terms: `acl_type` is user, group,
// owner@, group@ or everyone@, `perms` is acl.set's NFSv4 permission
// grammar, and the entry is an `allow`. **`perms: rwx` therefore means
// read_data, write_data and execute there**, which is NFSv4's own
// reading of those letters and less than a POSIX.1e `rwx` grants (that
// also maps to read_attributes, read_acl and others when a mode is
// translated). Nothing translates one family's permissions into the
// other's: which family a path speaks is the filesystem's choice, and a
// tree that manages both says which it means. A `default:` acl_type on
// NFSv4 is refused by name; inheritance there is acl.set's `flags`.
//
// # Recursion is done here, path by path
//
// `recurse` does not hand `setfacl -R` the work, for two measured
// reasons. A default ACL under -R reaches the files below a directory,
// and FreeBSD's setfacl refuses those ("default ACL may only be set on
// a directory") where Linux's skips them silently; and a removal under
// -R fails on FreeBSD at the first path without the entry, where Linux's
// carries on. Walking the tree here and calling set or remove on each
// path gives both platforms one behaviour, and makes "is it converged?"
// a question asked of every path rather than of the top one alone --
// which is what Salt's state does too, by reading `getfacl -R`. Symlinks
// are not followed and not changed, and a default ACL is managed on the
// directories only.
//
// # What is not here
//
// Salt's `force` (wipe the ACL before applying) is not built: it makes
// every run a change. And `acl_name` is compared as getfacl prints it,
// which is the account's name when it has one, so naming an account by
// its numeric id never converges -- getfacl prints the name instead.
func aclStateModules() []states.Module {
	pathDoc := "The file or directory whose ACL is managed. Defaults to the state ID."
	typeDoc := "The entry: user, group, mask or other on a POSIX.1e path, prefixed d: or default: for a directory's default ACL; user, group, owner@, group@ or everyone@ on an NFSv4 one."
	recurseDoc := "Manage every file and directory beneath name too, path by path; a default ACL on the directories only. Symlinks are skipped."
	return []states.Module{
		states.Module{
			Sig: signature.Signature{
				Module: "acl", Function: "present",
				Doc: "Ensure an ACL entry exists with exactly these permissions, in the family the path's filesystem speaks.",
				Params: []signature.Param{
					pathParam(pathDoc),
					req("acl_type", signature.String, typeDoc),
					opt("acl_name", signature.String, "", "The user or group the entry names. Empty is the owner's or owning group's own entry on POSIX.1e, and is what mask, other and the NFSv4 @ tags take."),
					opt("perms", signature.String, "", "POSIX.1e: the letters r, w and x. NFSv4: acl.set's permission grammar (short form, long form or a named set)."),
					opt("recurse", signature.Bool, false, recurseDoc),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Platforms: aclPlatforms, Section: "15.5",
			},
			Fn: aclPresentState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "acl", Function: "absent",
				Doc: "Ensure a named user's or group's ACL entry is not there.",
				Params: []signature.Param{
					pathParam(pathDoc),
					req("acl_type", signature.String, typeDoc),
					opt("acl_name", signature.String, "", "The user or group whose entry is removed. On NFSv4, owner@, group@ and everyone@ take none."),
					opt("perms", signature.String, "", "Accepted because Salt's state takes it, and not used: the entry is removed whatever its permissions."),
					opt("recurse", signature.Bool, false, recurseDoc),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Platforms: aclPlatforms, Section: "15.5",
			},
			Fn: aclAbsentState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "acl", Function: "list_present",
				Doc: "Ensure an ACL entry with these permissions exists for each of several users or groups.",
				Params: []signature.Param{
					pathParam(pathDoc),
					req("acl_type", signature.String, typeDoc),
					req("acl_names", signature.List, "The users or groups, one entry each."),
					opt("perms", signature.String, "", "As acl.present's, given to every entry."),
					opt("recurse", signature.Bool, false, recurseDoc),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Platforms: aclPlatforms, Section: "15.5",
			},
			Fn: aclListPresentState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "acl", Function: "list_absent",
				Doc: "Ensure none of several users or groups has an ACL entry.",
				Params: []signature.Param{
					pathParam(pathDoc),
					req("acl_type", signature.String, typeDoc),
					req("acl_names", signature.List, "The users or groups whose entries are removed."),
					opt("recurse", signature.Bool, false, recurseDoc),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Platforms: aclPlatforms, Section: "15.5",
			},
			Fn: aclListAbsentState,
		},
	}
}

func aclPresentState(c *exec.Context, args *value.Map) (states.Result, error) {
	return aclStateApply(c, args, []string{states.Str(args, "acl_name", "")}, false), nil
}

func aclAbsentState(c *exec.Context, args *value.Map) (states.Result, error) {
	return aclStateApply(c, args, []string{states.Str(args, "acl_name", "")}, true), nil
}

func aclListPresentState(c *exec.Context, args *value.Map) (states.Result, error) {
	names, err := aclStateNames(args)
	if err != nil {
		return states.False(err.Error()), nil
	}
	return aclStateApply(c, args, names, false), nil
}

func aclListAbsentState(c *exec.Context, args *value.Map) (states.Result, error) {
	names, err := aclStateNames(args)
	if err != nil {
		return states.False(err.Error()), nil
	}
	return aclStateApply(c, args, names, true), nil
}

func aclStateNames(args *value.Map) ([]string, error) {
	raw, _ := args.GetString("acl_names")
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, errors.New("The acl_names argument needs at least one user or group.")
	}
	names := make([]string, 0, len(list))
	for _, item := range list {
		name := strings.TrimSpace(fmt.Sprint(item))
		if name == "" {
			return nil, errors.New("The acl_names argument holds an empty name; the owner's own entry is acl.present's, with no acl_name.")
		}
		names = append(names, name)
	}
	return names, nil
}

// aclParseType reads Salt's acl_type: an entry tag, optionally prefixed
// for a directory's default ACL. Which tags are valid depends on the
// family, which only the path can say, so a tag of the wrong family is
// left for acl.set and acl.remove to refuse by name.
func aclParseType(raw string) (tag string, isDefault bool, err error) {
	tag = strings.TrimSpace(raw)
	for _, prefix := range []string{"default:", "d:"} {
		if rest, ok := strings.CutPrefix(tag, prefix); ok {
			tag, isDefault = rest, true
			break
		}
	}
	switch tag {
	case "user", "group", "mask", "other", "owner@", "group@", "everyone@":
		return tag, isDefault, nil
	}
	return "", false, fmt.Errorf("The acl_type %q is not an ACL entry: user, group, mask or other "+
		"(prefixed d: or default: for a default ACL) on POSIX.1e, or user, group, owner@, group@ "+
		"or everyone@ on NFSv4.", raw)
}

// aclStateTargets lists the paths a state covers: name alone, or with
// recurse everything beneath it too. Symlinks are left out -- neither
// tool changes a link's own ACL on Linux, and following one would let a
// tree's ACL reach outside it -- and a default ACL is a directory's only.
func aclStateTargets(root string, recurse, dirsOnly bool) ([]string, error) {
	if !recurse {
		return []string{root}, nil
	}
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if dirsOnly && !d.IsDir() {
			return nil
		}
		out = append(out, path)
		return nil
	})
	return out, err
}

// aclStateApply runs acl.set (or acl.remove) on every path and name the
// state covers, and makes one result of the answers.
func aclStateApply(c *exec.Context, args *value.Map, names []string, remove bool) states.Result {
	path := states.Str(args, "name", "")
	if path == "" {
		return states.False("An ACL state needs a path.")
	}
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return states.False(fmt.Sprintf("The path %s does not exist, so it has no ACL to manage.", path))
		}
		return states.False(fmt.Sprintf("The path %s could not be read: %v", path, err))
	}
	tag, isDefault, err := aclParseType(states.Str(args, "acl_type", ""))
	if err != nil {
		return states.False(err.Error())
	}
	recurse := states.Bool(args, "recurse", false)
	targets, err := aclStateTargets(path, recurse, isDefault)
	if err != nil {
		return states.False(fmt.Sprintf("The tree under %s could not be walked: %v", path, err))
	}

	verb, fn := "set", aclSetFn
	if remove {
		verb, fn = "removed", aclRemoveFn
	}
	changes := value.NewMap(0)
	for _, target := range targets {
		for _, name := range names {
			callArgs := value.MapOf("name", target, "tag", tag, "qualifier", name, "default", isDefault)
			if !remove {
				callArgs.Set("perms", states.Str(args, "perms", ""))
			}
			out, err := fn(c, callArgs)
			if err != nil {
				res := states.False(fmt.Sprintf("The ACL entry %s for %q on %s could not be %s: %v",
					states.Str(args, "acl_type", ""), name, target, verb, err))
				// What earlier paths already changed is still a change,
				// and a failure that hid it would be reporting less than
				// happened.
				res.Changes = changes
				return res
			}
			result, _ := out.(*value.Map)
			if changed, _ := result.GetString("changed"); changed != true {
				continue
			}
			change, _ := result.GetString("changes")
			aclMergeChange(changes, target, change)
		}
	}

	what := fmt.Sprintf("%s entr%s for %s on %s", states.Str(args, "acl_type", ""),
		aclPluralY(len(names)), strings.Join(names, ", "), path)
	if recurse {
		what += fmt.Sprintf(" and the %d paths beneath it", len(targets)-1)
	}
	if changes.Len() == 0 {
		if remove {
			return states.True(fmt.Sprintf("The ACL %s %s already absent.", what, aclIsAre(len(names))))
		}
		return states.True(fmt.Sprintf("The ACL %s already %s as declared.", what, aclIsAre(len(names))))
	}
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The ACL %s would be %s on %d path%s.", what, verb,
			changes.Len(), aclPluralS(changes.Len())), changes)
	}
	return states.Changed(fmt.Sprintf("The ACL %s %s %s on %d path%s.", what, aclWasWere(len(names)), verb,
		changes.Len(), aclPluralS(changes.Len())), changes)
}

// aclMergeChange files one execution function's change under the path
// it was made to, merging when two names changed the same path.
func aclMergeChange(changes *value.Map, path string, change any) {
	inner, _ := change.(*value.Map)
	if inner == nil {
		return
	}
	existing, _ := changes.GetString(path)
	if prior, ok := existing.(*value.Map); ok {
		for _, e := range inner.Entries() {
			prior.Set(value.KeyString(e.Key), e.Val)
		}
		return
	}
	changes.Set(path, inner)
}

func aclIsAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func aclWasWere(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

func aclPluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
