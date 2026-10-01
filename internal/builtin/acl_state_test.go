package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The acl states against recorded getfacl answers from testdata/acl. The
// paths are real, in a temporary directory, because the state walks them
// and asks which are directories; the ACLs are the captures.
//
// The function is called directly rather than through Registry.Call,
// which refuses a state outside its platforms -- and the point of a
// captured fixture is that both hosts' answers can be read anywhere.

func aclStateCall(t *testing.T, c *exec.Context, name string, args *value.Map) states.Result {
	t.Helper()
	r := &Registries{Exec: exec.NewRegistry(), States: states.NewRegistry()}
	registerACL(r)
	mod, ok := r.States.Lookup(name)
	if !ok {
		t.Fatalf("%s is not registered", name)
	}
	res, err := mod.Fn(c, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// aclTree makes root/{f, sub/, sub/g} and a symlink root/link -> f.
func aclTree(t *testing.T) string {
	t.Helper()
	root := aclScratchDir(t)
	if err := os.WriteFile(filepath.Join(root, "f"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "g"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("f", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestPresentPredictsInTestModeAndRunsNothing(t *testing.T) {
	path := filepath.Join(aclTree(t), "f")
	c := aclContext(map[string]exec.Result{"getfacl " + path: {Stdout: aclCapture(t, "debian13", "trivial-file")}})
	c.Test = true
	res := aclStateCall(t, c, "acl.present", aclArgs("name", path, "acl_type", "user", "acl_name", "nobody", "perms", "rwx"))
	if res.Result != nil || !res.HasChanges() {
		t.Fatalf("test mode = %+v, want a nil result with the change predicted", res)
	}
	if cmds := aclSetfaclCommands(c); len(cmds) != 0 {
		t.Errorf("test mode ran setfacl: %v", cmds)
	}
}

func TestPresentAppliesOnceAndThenFindsItInPlace(t *testing.T) {
	path := filepath.Join(aclTree(t), "f")
	c := aclContext(map[string]exec.Result{"getfacl " + path: {Stdout: aclCapture(t, "freebsd15", "trivial-file")}})
	res := aclStateCall(t, c, "acl.present", aclArgs("name", path, "acl_type", "user", "acl_name", "nobody", "perms", "rwx"))
	if !res.Succeeded() || res.Result == nil || !res.HasChanges() {
		t.Fatalf("apply = %+v", res)
	}
	if cmds := aclSetfaclCommands(c); len(cmds) != 1 || cmds[0] != "setfacl -m user:nobody:rwx "+path {
		t.Errorf("setfacl calls = %v", cmds)
	}

	again := aclContext(map[string]exec.Result{"getfacl " + path: {Stdout: aclCapture(t, "freebsd15", "named-user")}})
	res = aclStateCall(t, again, "acl.present", aclArgs("name", path, "acl_type", "user", "acl_name", "nobody", "perms", "rwx"))
	if res.Result == nil || !*res.Result || res.HasChanges() {
		t.Errorf("a second run against the entry in place = %+v, want success with no change", res)
	}
}

// recurse covers every path beneath, asks each one, skips the symlink,
// and changes only the paths that need it.
func TestPresentRecursesPathByPathAndSkipsSymlinks(t *testing.T) {
	root := aclTree(t)
	c := aclContext(map[string]exec.Result{
		"getfacl " + root:                         {Stdout: aclCapture(t, "debian13", "default-dir")},
		"getfacl " + filepath.Join(root, "f"):     {Stdout: aclCapture(t, "debian13", "named-user")},
		"getfacl " + filepath.Join(root, "sub"):   {Stdout: aclCapture(t, "debian13", "default-dir")},
		"getfacl " + filepath.Join(root, "sub/g"): {Stdout: aclCapture(t, "debian13", "trivial-file")},
	})
	res := aclStateCall(t, c, "acl.present",
		aclArgs("name", root, "acl_type", "user", "acl_name", "nobody", "perms", "rwx", "recurse", true))
	if !res.Succeeded() || res.Changes.Len() != 3 {
		t.Fatalf("recurse = %+v, want three paths changed (f already had the entry)", res)
	}
	ran := strings.Join(c.Runner.(*exec.RecordingRunner).RanCommands(), "\n")
	if strings.Contains(ran, string(filepath.Separator)+"link") {
		t.Errorf("the symlink was read or changed:\n%s", ran)
	}
	if strings.Contains(ran, "-R") {
		t.Errorf("setfacl -R was used; the state recurses itself:\n%s", ran)
	}
}

// A default ACL with recurse is managed on the directories alone.
func TestADefaultACLRecursesOverDirectoriesOnly(t *testing.T) {
	root := aclTree(t)
	c := aclContext(map[string]exec.Result{
		"getfacl " + root:                          {Stdout: aclCapture(t, "freebsd15", "default-dir")},
		"getfacl -d " + root:                       {Stdout: aclCapture(t, "freebsd15", "default-none")},
		"getfacl " + filepath.Join(root, "sub"):    {Stdout: aclCapture(t, "freebsd15", "default-dir")},
		"getfacl -d " + filepath.Join(root, "sub"): {Stdout: aclCapture(t, "freebsd15", "default-only")},
	})
	res := aclStateCall(t, c, "acl.present",
		aclArgs("name", root, "acl_type", "default:user", "acl_name", "nobody", "perms", "r-x", "recurse", true))
	if !res.Succeeded() || res.Changes.Len() != 1 {
		t.Fatalf("recurse = %+v, want the top directory alone changed", res)
	}
	want := "setfacl -d -m user::rwx,group::r-x,other::r-x,user:nobody:r-x " + root
	if cmds := aclSetfaclCommands(c); len(cmds) != 1 || cmds[0] != want {
		t.Errorf("setfacl calls = %v, want [%s]", cmds, want)
	}
	for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.HasSuffix(ran, "/f") || strings.HasSuffix(ran, "/g") {
			t.Errorf("a file was asked about its default ACL: %s", ran)
		}
	}
}

func TestAbsentRemovesTheEntryAndIsThenSatisfied(t *testing.T) {
	path := filepath.Join(aclTree(t), "f")
	c := aclContext(map[string]exec.Result{"getfacl " + path: {Stdout: aclCapture(t, "debian13", "named-user")}})
	res := aclStateCall(t, c, "acl.absent", aclArgs("name", path, "acl_type", "user", "acl_name", "nobody"))
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("absent = %+v", res)
	}
	if cmds := aclSetfaclCommands(c); len(cmds) != 1 || cmds[0] != "setfacl -x user:nobody: "+path {
		t.Errorf("setfacl calls = %v", cmds)
	}
	again := aclContext(map[string]exec.Result{"getfacl " + path: {Stdout: aclCapture(t, "debian13", "trivial-file")}})
	res = aclStateCall(t, again, "acl.absent", aclArgs("name", path, "acl_type", "user", "acl_name", "nobody"))
	if res.Result == nil || !*res.Result || res.HasChanges() {
		t.Errorf("absent against no entry = %+v", res)
	}
}

func TestListPresentGivesEveryNameAnEntryAndFilesThemUnderOnePath(t *testing.T) {
	path := filepath.Join(aclTree(t), "f")
	c := aclContext(map[string]exec.Result{"getfacl " + path: {Stdout: aclCapture(t, "debian13", "trivial-file")}})
	res := aclStateCall(t, c, "acl.list_present",
		aclArgs("name", path, "acl_type", "user", "acl_names", []any{"nobody", "daemon"}, "perms", "r"))
	if !res.Succeeded() || res.Changes.Len() != 1 {
		t.Fatalf("list_present = %+v, want one path's changes", res)
	}
	inner, _ := res.Changes.GetString(path)
	if m, ok := inner.(*value.Map); !ok || m.Len() != 2 {
		t.Errorf("changes for %s = %v, want both names", path, inner)
	}
	if cmds := aclSetfaclCommands(c); len(cmds) != 2 {
		t.Errorf("setfacl calls = %v, want one per name", cmds)
	}
}

func TestAStateRefusesWhatItCannotManage(t *testing.T) {
	path := filepath.Join(aclTree(t), "f")
	for label, call := range map[string]struct {
		fn   string
		args *value.Map
	}{
		"a path that is not there": {"acl.present", aclArgs("name", path+"-missing", "acl_type", "user", "acl_name", "nobody", "perms", "r")},
		"an unknown acl_type":      {"acl.present", aclArgs("name", path, "acl_type", "owner", "acl_name", "nobody", "perms", "r")},
		"an empty acl_names":       {"acl.list_present", aclArgs("name", path, "acl_type", "user", "acl_names", []any{}, "perms", "r")},
		"a default on NFSv4":       {"acl.present", aclArgs("name", path, "acl_type", "d:user", "acl_name", "games", "perms", "r")},
	} {
		c := aclContext(map[string]exec.Result{"getfacl " + path: {Stdout: aclRealCapture}})
		res := aclStateCall(t, c, call.fn, call.args)
		if !res.Failed() {
			t.Errorf("%s: %+v, want a refusal", label, res)
		}
		if err := states.CommentIsASentence(res.Comment); err != nil {
			t.Errorf("%s: %v", label, err)
		}
		if cmds := aclSetfaclCommands(c); len(cmds) != 0 {
			t.Errorf("%s ran setfacl: %v", label, cmds)
		}
	}
}

// The NFSv4 branch is acl.set's, with Salt's argument names.
func TestPresentOnAnNFSv4PathSetsAnAllowEntry(t *testing.T) {
	path := filepath.Join(aclTree(t), "f")
	c := aclContext(map[string]exec.Result{"getfacl " + path: {Stdout: aclTrivialCapture}})
	res := aclStateCall(t, c, "acl.present", aclArgs("name", path, "acl_type", "user", "acl_name", "games", "perms", "rwx"))
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("present = %+v", res)
	}
	if cmds := aclSetfaclCommands(c); len(cmds) != 1 || cmds[0] != "setfacl -m user:games:rwx::allow "+path {
		t.Errorf("setfacl calls = %v", cmds)
	}
}
