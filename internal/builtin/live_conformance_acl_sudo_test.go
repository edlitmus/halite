package builtin

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The acl and sudo states, through SPEC 11.6's harness.
//
// # acl
//
// Each case works on a file of its own, on a filesystem that speaks
// POSIX.1e: a directory under the temporary directory on Linux, and on
// FreeBSD a UFS memory disk mounted `-o acls`, made in the first Setup
// and destroyed in Cleanup -- a FreeBSD root is UFS without the option
// as often as not, and a CI VM's is. Setup and the probe use getfacl and
// setfacl directly rather than the module, so the harness's "test mode
// changed nothing" is the tool's answer and not the module's.
//
// # sudo
//
// One drop-in, `halitecf-sudo`, in the directory the node's sudoers
// includes, granting `nobody` a path that does not exist. Setup puts it
// in or takes it out with the module's own atomic writer -- the only
// writer here that makes it root's and 0440, which visudo demands --
// and Cleanup removes it.

const aclConformanceUser = "nobody"

// aclConformanceScratch is a POSIX.1e directory made on first use.
type aclConformanceScratch struct {
	dir, unit string
}

func (s *aclConformanceScratch) ensure(c *hexec.Context) (string, error) {
	if s.dir != "" {
		return s.dir, nil
	}
	dir, err := os.MkdirTemp("", "halitecf-acl-")
	if err != nil {
		return "", err
	}
	s.dir = dir
	if runtime.GOOS != "freebsd" {
		return dir, nil
	}
	res, err := c.Run(hexec.Command{Argv: []string{"mdconfig", "-a", "-t", "swap", "-s", "32m"}})
	if err != nil {
		return "", err
	}
	s.unit = strings.TrimSpace(res.Stdout)
	if _, err := c.Run(hexec.Command{Argv: []string{"newfs", "-U", "/dev/" + s.unit}}); err != nil {
		return "", err
	}
	if _, err := c.Run(hexec.Command{Argv: []string{"mount", "-o", "acls", "/dev/" + s.unit, dir}}); err != nil {
		return "", err
	}
	return dir, nil
}

func (s *aclConformanceScratch) release(c *hexec.Context) {
	if s.dir == "" {
		return
	}
	if s.unit != "" {
		_, _ = c.Run(hexec.Command{Argv: []string{"umount", s.dir}, IgnoreExitCode: true})
		_, _ = c.Run(hexec.Command{Argv: []string{"mdconfig", "-d", "-u", s.unit}, IgnoreExitCode: true})
	}
	_ = os.RemoveAll(s.dir)
	s.dir, s.unit = "", ""
}

// aclConformanceCase builds one acl case. prepare puts the file into the
// "not yet applied" state with setfacl.
func aclConformanceCase(name string, args *value.Map, prepare []string) liveCase {
	root := liveRoot()
	scratch := &aclConformanceScratch{}
	file := func() string { return filepath.Join(scratch.dir, "f") }
	needs := []string{"getfacl", "setfacl"}
	if runtime.GOOS == "freebsd" {
		needs = append(needs, "mdconfig", "newfs", "mount", "umount")
	}
	return liveCase{
		platforms: []string{"linux", "freebsd"},
		needs:     needs,
		Conformance: states.Conformance{
			Name: name,
			Args: args,
			Setup: func() error {
				dir, err := scratch.ensure(root)
				if err != nil {
					return err
				}
				path := filepath.Join(dir, "f")
				if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
					if err := os.WriteFile(path, []byte("halitecf\n"), 0o644); err != nil {
						return err
					}
				}
				args.Set("name", path)
				// IgnoreExitCode: FreeBSD's setfacl -x exits 1 for an
				// entry that is already gone, and that is the state
				// being asked for.
				_, err = root.Run(hexec.Command{Argv: append(append([]string{"setfacl"}, prepare...), path), IgnoreExitCode: true})
				return err
			},
			Probe: func() (string, error) {
				res, err := root.Run(hexec.Command{Argv: []string{"getfacl", file()}})
				return res.Stdout, err
			},
			Cleanup: func() { scratch.release(root) },
		},
	}
}

func aclConformanceCases() []liveCase {
	return []liveCase{
		aclConformanceCase("acl.present",
			value.MapOf("acl_type", "user", "acl_name", aclConformanceUser, "perms", "rwx"),
			[]string{"-x", "user:" + aclConformanceUser + ":"}),
		aclConformanceCase("acl.absent",
			value.MapOf("acl_type", "user", "acl_name", aclConformanceUser),
			[]string{"-m", "user:" + aclConformanceUser + ":rwx"}),
		aclConformanceCase("acl.list_present",
			value.MapOf("acl_type", "user", "acl_names", []any{aclConformanceUser, "daemon"}, "perms", "r"),
			[]string{"-b"}),
		aclConformanceCase("acl.list_absent",
			value.MapOf("acl_type", "user", "acl_names", []any{aclConformanceUser, "daemon"}),
			[]string{"-m", "user:" + aclConformanceUser + ":r,user:daemon:r"}),
	}
}

const sudoConformanceName = "halitecf-sudo"

func sudoConformanceCases() []liveCase {
	root := liveRoot()
	rule := "nobody ALL=(root) NOPASSWD: /nonexistent/halitecf-sudo\n"
	target := func() (string, error) {
		dir, err := sudoDropInDir(root, "")
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, sudoConformanceName), nil
	}
	probe := func() (string, error) {
		path, err := target()
		if err != nil {
			return "", err
		}
		data, mode, existed, err := sudoReadDropIn(path)
		if !existed {
			return "absent", err
		}
		return mode + "\n" + string(data), err
	}
	remove := func() {
		if path, err := target(); err == nil {
			_ = os.Remove(path)
		}
	}
	unavailable := func(c *hexec.Context) string {
		if _, err := sudoDropInDir(c, ""); err != nil {
			return err.Error()
		}
		return ""
	}
	return []liveCase{
		{
			platforms:   []string{"linux", "freebsd"},
			needs:       []string{"sudo", "visudo"},
			unavailable: unavailable,
			Conformance: states.Conformance{
				Name:    "sudo.present",
				Args:    value.MapOf("name", sudoConformanceName, "contents", rule),
				Setup:   func() error { remove(); return nil },
				Probe:   probe,
				Cleanup: remove,
			},
		},
		{
			platforms:   []string{"linux", "freebsd"},
			needs:       []string{"sudo", "visudo"},
			unavailable: unavailable,
			Conformance: states.Conformance{
				Name: "sudo.absent",
				Args: value.MapOf("name", sudoConformanceName),
				Setup: func() error {
					path, err := target()
					if err != nil {
						return err
					}
					return sudoWriteDropIn(path, []byte(rule))
				},
				Probe:   probe,
				Cleanup: remove,
			},
		},
	}
}
