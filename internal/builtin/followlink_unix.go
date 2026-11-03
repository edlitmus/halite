//go:build unix

package builtin

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// linkTrusted says whether path, owned by uid, is is one only this node
// could have made or changed: root, and the account the node runs as,
// which as root is the same thing.
//
// It is not the account a state names with `user:`. That is the obvious
// third candidate and the wrong one: the account a home directory or an
// app directory was handed to is exactly the account that can plant the
// link, and trusting it would turn `file.managed` with `user: deploy`
// into a way for deploy to have root give it any file on the machine.
//
// A variable so that a test, which cannot be root, can make files it made
// look like another account's. The path is for that, and only that: what
// is trusted is decided by the owner, never by where a file is.
var linkTrusted = func(path string, uid uint32) bool {
	return uid == 0 || uid == uint32(os.Geteuid())
}

// walked is one directory on the way to a link: its path as resolved, its
// stat, and, when an account other than root could change what is at or
// below it, why.
type walked struct {
	path   string
	info   os.FileInfo
	unsafe string
}

// resolveTrusted resolves path, an existing symbolic link, the way
// filepath.EvalSymlinks does, a component at a time, and refuses any link
// on the way that another account could have made or redirected.
//
// A link is followed only when it, and every directory on the way to it,
// is owned by root (or the account this node runs as) and sits in a
// directory nobody else can write -- or in a sticky one, where nobody
// else can rename or remove it. That is Linux's fs.protected_symlinks
// rule, which only covers sticky world-writable directories, extended to
// the ordinary ones: a home directory, or an app directory handed over
// with `user:`, is not sticky, and its owner can put a link in it
// whatever the kernel's setting. DIVERGENCE 5.558.
//
// **Every link is checked, not only the one named.** A root-owned
// `/etc/app.conf -> /srv/app/app.conf` with /srv/app the app account's is
// a planted link one step removed: the account makes app.conf a link to
// /etc/shadow and the first link leads root there.
//
// What comes *after* the last link is not checked, and that is on
// purpose. A trusted link is the operator's own; following it is the same
// as having named the file it points to, and a file named directly in a
// directory an account can write is what file states have always written
// (with the rename writeAtomic does, which replaces whatever is there
// rather than writing through it).
//
// Because everything up to and including the last link is in directories
// only root can change, nothing can be swapped under the check between
// here and the write. Group write is tolerated only for group 0 (root on
// Linux, wheel on the BSDs), whose members can become root anyway.
func resolveTrusted(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rootInfo, err := os.Lstat("/")
	if err != nil {
		return "", err
	}
	root := walked{path: "/", info: rootInfo, unsafe: componentUnsafe("/", rootInfo, rootInfo)}
	stack := []walked{root}
	rest := splitPath(abs)
	for hops := 0; len(rest) > 0; {
		name := rest[0]
		rest = rest[1:]
		cur := stack[len(stack)-1]
		if name == ".." {
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		next := filepath.Join(cur.path, name)
		info, err := os.Lstat(next)
		if err != nil {
			to, _ := os.Readlink(path)
			return "", fmt.Errorf("%s is a symbolic link to %s, which does not resolve: %w", path, to, err)
		}
		why := cur.unsafe
		if why == "" {
			why = componentUnsafe(next, info, cur.info)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			stack = append(stack, walked{path: next, info: info, unsafe: why})
			continue
		}
		if hops++; hops > 255 {
			return "", fmt.Errorf("%s: %w", path, syscall.ELOOP)
		}
		to, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if why != "" {
			return "", untrustedLink(path, next, to, why)
		}
		if filepath.IsAbs(to) {
			stack = []walked{root}
		}
		rest = append(splitPath(to), rest...)
	}
	return stack[len(stack)-1].path, nil
}

// componentUnsafe says why another account could change what is at path,
// whose stat is info, in the directory whose stat is parent; or "".
func componentUnsafe(path string, info, parent os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	pst, pok := parent.Sys().(*syscall.Stat_t)
	if !ok || !pok {
		return fmt.Sprintf("the owner of %s could not be read", path)
	}
	what := "it"
	if info.IsDir() {
		what = path + ", a directory on the way to it,"
	}
	if !linkTrusted(path, uint32(st.Uid)) {
		return fmt.Sprintf("%s belongs to uid %d", what, st.Uid)
	}
	if parent.Mode()&os.ModeSticky != 0 {
		return ""
	}
	perm := parent.Mode().Perm()
	if perm&0o002 != 0 || (perm&0o020 != 0 && pst.Gid != 0) {
		return fmt.Sprintf("%s is in %s, which accounts other than its owner can write", what, filepath.Dir(path))
	}
	return ""
}

// untrustedLink is the refusal, named the way noLink names its own
// (ssh_account.go): the path the state was given, and the link that
// failed when that is a different one further along.
func untrustedLink(path, link, to, why string) error {
	where := fmt.Sprintf("%s is a symbolic link to %s,", path, to)
	if a, err := os.Lstat(path); err != nil || !sameLink(a, link) {
		where = fmt.Sprintf("%s leads through %s, a symbolic link to %s,", path, link, to)
	}
	return fmt.Errorf("%s and it was not followed: %s, so another account could have made it point anywhere. "+
		"A file this node writes as root is not written through such a link; name the file it should change instead",
		where, why)
}

func sameLink(a fs.FileInfo, link string) bool {
	b, err := os.Lstat(link)
	return err == nil && os.SameFile(a, b)
}

func splitPath(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" && s != "." {
			out = append(out, s)
		}
	}
	return out
}
