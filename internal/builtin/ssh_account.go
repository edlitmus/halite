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

// sshAccountFile is a file in a named account's home, ~/.ssh/<name> by
// default, reached through the home directory as an os.Root.
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
// **Any path inside the account's home, named or not** (issue 321, A10).
// The root was first taken only when no `config` was given, on the
// reasoning that a `config` path is one the operator chose. But the
// operator chose a name, not what the account has since put at it:
// `config: /home/deploy/.ssh/authorized_keys`, the default file spelled
// out, was written by path through a ~/.ssh the account had made a link,
// and root wrote, and chowned, a file wherever deploy pointed it. So the
// decision is made by where the path lands. A path in the home -- as
// passwd spells the home, or through another name for it, such as /home
// on a host where it is a link to /usr/home -- goes through the root, and
// every component of it inside the home is refused if it is a link. What
// `config` still decides is whose the directory is: only the default
// ~/.ssh is handed to the account, as giveToAccount does by path.
//
// A home of / is the exception: the account does not own /, and every
// absolute path is in it, so only the default ~/.ssh is taken through
// the root there; /etc on macOS is a link, and refusing it would refuse
// every `config` such an account was given. A path outside the home is
// written by path, as before. A state that names no account is the
// caller's own file. Windows keeps its owner model and its path writes.
type sshAccountFile struct {
	home   string
	rel    string // the file, relative to home: ".ssh/authorized_keys" by default
	uid    int
	gid    int
	ownDir bool // the default path: ~/.ssh is the account's, and given to it
}

// sshAccountFileFor answers with the account file path is, or nil when
// path is not inside a named account's home.
func sshAccountFileFor(args *value.Map, path string) (*sshAccountFile, error) {
	account := states.Str(args, "user", "")
	if account == "" || runtime.GOOS == "windows" {
		return nil, nil
	}
	u, err := user.Lookup(account)
	if err != nil {
		return nil, fmt.Errorf("the account %q could not be resolved: %w", account, err)
	}
	home := filepath.Clean(u.HomeDir)
	defaultPath := states.Str(args, "config", "") == ""
	rel, ok := pathInHome(home, path)
	if !ok || (home == string(filepath.Separator) && !defaultPath) {
		return nil, nil
	}
	if defaultPath && filepath.Dir(rel) != ".ssh" {
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
	return &sshAccountFile{home: home, rel: rel, uid: uid, gid: gid, ownDir: defaultPath}, nil
}

// pathInHome answers with path relative to home, when path is inside it.
//
// The plain case is the lexical one. Otherwise each directory on the way
// down from / is compared with the home by identity, and the shallowest
// that is the home wins: a component above the home is in a directory the
// account does not own, so a link there is root's; anything below it is
// then resolved inside the root, where a link the account made is refused
// rather than followed. Taking the deepest match instead would let an
// account's own link back to its home stand in for the home and hide
// itself from that refusal. Nothing here is trusted beyond choosing the
// root: the write opens the home by its passwd name, not by the prefix
// that matched, so a link swapped after this check cannot move it.
func pathInHome(home, path string) (string, bool) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	if rel, err := filepath.Rel(home, path); err == nil && filepath.IsLocal(rel) {
		return rel, true
	}
	hi, err := os.Stat(home)
	if err != nil {
		return "", false
	}
	var prefixes []string
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		prefixes = append(prefixes, p)
		if p == filepath.Dir(p) {
			break
		}
	}
	for i := len(prefixes) - 1; i >= 0; i-- {
		if info, err := os.Stat(prefixes[i]); err == nil && os.SameFile(info, hi) {
			rel, err := filepath.Rel(prefixes[i], path)
			return rel, err == nil && filepath.IsLocal(rel)
		}
	}
	return "", false
}

// dirs lists the directories between the home and the file, shallowest
// first: [".ssh"] for the default path.
func (f *sshAccountFile) dirs() []string {
	var out []string
	for d := filepath.Dir(f.rel); d != "."; d = filepath.Dir(d) {
		out = append([]string{d}, out...)
	}
	return out
}

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
	for _, d := range f.dirs() {
		exists, err := noLink(root, f.home, d)
		if err != nil || !exists {
			return nil, err
		}
	}
	exists, err := noLink(root, f.home, f.rel)
	if err != nil || !exists {
		return nil, err
	}
	b, err := root.ReadFile(f.rel)
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
	for _, d := range f.dirs() {
		exists, err := noLink(root, f.home, d)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		// sshd refuses a .ssh that is group or world writable, so the
		// mode is set rather than left to the umask.
		if err := root.Mkdir(d, 0o700); err != nil {
			return err
		}
		if err := changeEntryIn(root, filepath.Join(f.home, d), d,
			entryChange{mode: 0o700, setMode: true, uid: -1, gid: -1}); err != nil {
			return err
		}
	}
	if _, err := noLink(root, f.home, f.rel); err != nil {
		return err
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	dir := filepath.Dir(f.rel)
	tmp := filepath.Join(dir, "."+filepath.Base(f.rel)+".halite-"+hex.EncodeToString(suffix))
	out, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	// The mode and the owner are set on the descriptor, not by name. The
	// temporary file sits in ~/.ssh (or another directory of the home),
	// which the account owns, so between
	// the close and a chmod by name the account could replace it with a
	// hard link to /etc/spwd.db, and the chown would hand that file
	// to the account. DIVERGENCE 5.560.
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
		werr = root.Rename(tmp, f.rel)
	}
	if werr != nil {
		_ = root.Remove(tmp)
		return werr
	}
	if !f.ownDir {
		return nil
	}
	// ~/.ssh is a name in the account's home, which the account can
	// replace with a hard link as easily as a file in it.
	return changeEntryIn(root, filepath.Join(f.home, dir), dir,
		entryChange{uid: f.uid, gid: f.gid})
}

// sshReadLines is readLines for an ssh file: through the account's root
// when the file is inside a named account's home, by path otherwise.
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
// the account's root when it is inside a named account's home, by path
// and giveToAccount otherwise.
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
