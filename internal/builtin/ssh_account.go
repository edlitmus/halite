package builtin

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/fileperm"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// sshAccountFile is a named account's own ~/.ssh/<name>, reached through
// its home directory as an os.Root.
//
// **The account controls ~/.ssh, and root writes into it** (issue 321,
// M-30). ssh_auth and ssh_known_hosts, run as root for `user: deploy`,
// created ~deploy/.ssh, read the file in it, wrote a new one and gave both
// to deploy, all by path. deploy can replace ~/.ssh with a link, say to
// /etc: root then read whatever /etc/authorized_keys was into the file it
// wrote, put a file of that name in /etc, and chowned it to deploy, on
// every highstate. An os.Root on the home directory resolves every
// component inside it and refuses to leave it, so the link is refused
// rather than followed, and a swap between the check and the write cannot
// lead the write elsewhere either. A ~/.ssh or a file in it that is a link
// is refused by name, even one that stays inside the home: sshd does not
// follow it for another account's keys either, and refusing it is simpler
// to reason about than deciding which links are safe.
//
// Only for the default path of a named account. A `config` path is one
// the operator chose, and a state that names no account is the caller's
// own file. Windows keeps its owner model and its path writes.
type sshAccountFile struct {
	home string
	name string // "authorized_keys" or "known_hosts"
	uid  int
	gid  int
}

// sshAccountFileFor answers with the account file path is, or nil when
// path is not a named account's default one.
func sshAccountFileFor(args *value.Map, path string) (*sshAccountFile, error) {
	account := states.Str(args, "user", "")
	if account == "" || states.Str(args, "config", "") != "" || runtime.GOOS == "windows" {
		return nil, nil
	}
	u, err := user.Lookup(account)
	if err != nil {
		return nil, fmt.Errorf("the account %q could not be resolved: %w", account, err)
	}
	if filepath.Dir(filepath.Dir(path)) != filepath.Clean(u.HomeDir) || filepath.Base(filepath.Dir(path)) != ".ssh" {
		return nil, nil
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return nil, fmt.Errorf("the account %q has a non-numeric uid %q", account, u.Uid)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return nil, fmt.Errorf("the account %q has a non-numeric gid %q", account, u.Gid)
	}
	return &sshAccountFile{home: filepath.Clean(u.HomeDir), name: filepath.Base(path), uid: uid, gid: gid}, nil
}

func (f *sshAccountFile) rel() string { return filepath.Join(".ssh", f.name) }

// noLink refuses a name inside the root that is a symbolic link, and
// reports whether it exists.
func noLink(root *os.Root, home, rel string) (bool, error) {
	info, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		to, _ := root.Readlink(rel)
		return true, fmt.Errorf("%s is a symbolic link (to %s), and a file this node writes as root for the account is not written through one",
			filepath.Join(home, rel), to)
	}
	return true, nil
}

// lines reads the file through the root, as readLines does by path.
func (f *sshAccountFile) lines() ([]string, error) {
	root, err := os.OpenRoot(f.home)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if _, err := noLink(root, f.home, ".ssh"); err != nil {
		return nil, err
	}
	exists, err := noLink(root, f.home, f.rel())
	if err != nil || !exists {
		return nil, err
	}
	b, err := root.ReadFile(f.rel())
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n"), nil
}

// write replaces the file through the root and gives it, and ~/.ssh, to
// the account: a temporary file in ~/.ssh, set to the mode and the
// account, then renamed over the old one, all by names inside the root.
func (f *sshAccountFile) write(data []byte, mode os.FileMode) error {
	root, err := os.OpenRoot(f.home)
	if err != nil {
		return err
	}
	defer root.Close()
	dirExists, err := noLink(root, f.home, ".ssh")
	if err != nil {
		return err
	}
	if !dirExists {
		// sshd refuses a .ssh that is group or world writable, so the
		// mode is set rather than left to the umask.
		if err := root.Mkdir(".ssh", 0o700); err != nil {
			return err
		}
		if err := changeEntryIn(root, filepath.Join(f.home, ".ssh"), ".ssh",
			entryChange{mode: 0o700, setMode: true, uid: -1, gid: -1}); err != nil {
			return err
		}
	}
	if _, err := noLink(root, f.home, f.rel()); err != nil {
		return err
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	tmp := filepath.Join(".ssh", "."+f.name+".halite-"+hex.EncodeToString(suffix))
	out, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	// The mode and the owner are set on the descriptor, not by name. The
	// temporary file sits in ~/.ssh, which the account owns, so between
	// the close and a chmod by name the account could replace it with a
	// hard link to /etc/spwd.db, and the chown would hand that file
	// to the account. DIVERGENCE 5.558.
	_, werr := out.Write(data)
	if werr == nil {
		werr = out.Chmod(mode)
	}
	if werr == nil {
		werr = out.Chown(f.uid, f.gid)
	}
	if werr == nil {
		werr = out.Sync()
	}
	if cerr := out.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = root.Rename(tmp, f.rel())
	}
	if werr != nil {
		_ = root.Remove(tmp)
		return werr
	}
	// ~/.ssh is a name in the account's home, which the account can
	// replace with a hard link as easily as a file in it.
	return changeEntryIn(root, filepath.Join(f.home, ".ssh"), ".ssh",
		entryChange{uid: f.uid, gid: f.gid})
}

// sshReadLines is readLines for an ssh file: through the account's root
// when the file is a named account's own, by path otherwise.
func sshReadLines(args *value.Map, path string) ([]string, error) {
	f, err := sshAccountFileFor(args, path)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return readLines(path), nil
	}
	return f.lines()
}

// sshWriteFile writes an ssh file and gives it to its account: through
// the account's root when it is a named account's own, by path and
// giveToAccount otherwise.
func sshWriteFile(args *value.Map, path string, data []byte, mode os.FileMode) error {
	f, err := sshAccountFileFor(args, path)
	if err != nil {
		return err
	}
	if f != nil {
		return f.write(data, mode)
	}
	if err := fileperm.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("%s could not be created: %v", filepath.Dir(path), err)
	}
	if err := writeAtomic(path, data, mode); err != nil {
		return fmt.Errorf("%s could not be written: %v", path, err)
	}
	return giveToAccount(args, path)
}
