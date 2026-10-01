package builtin

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The `sudo` states of SPEC 15.5: `sudo.present` and `sudo.absent`, each
// managing one sudoers drop-in.
//
// # Why this shape, when Salt has no sudo state at all
//
// SPEC 15.5 names `sudo` in its list of core states and says nothing
// else about it, and Salt has no core state of that name to copy: a Salt
// tree manages sudoers with `file.managed` and, if it is careful, a
// `check_cmd: visudo -cf`. So the shape here is chosen, and chosen to be
// the smallest one that does what `file.managed` cannot. That is the
// argument `sudo.go` already makes for the execution module: a sudoers
// file is text, and the text decides who may become root, and a
// malformed one does not degrade -- sudo refuses to run at all, for
// everybody, including the account that would fix it.
//
// A state that owned the main sudoers file would put the whole policy in
// one state's hands and make every tree that touches it fight over it.
// A **drop-in** in the directory the sudoers file includes is one rule
// set with one owner, which is what a tree means when it says "give this
// group this". So `sudo.present` takes a file name and the text, and
// `sudo.absent` takes the file name. No sudoers grammar is parsed: every
// question about the text is visudo's.
//
// # What makes it safer than file.managed with a check
//
//   - The text is checked by `visudo -c -f -` on standard input **before
//     anything is written**, in test mode too, so a prediction of "would
//     change" is a prediction about text sudo accepts.
//   - The file is written to a temporary name in the same directory and
//     renamed into place. That name starts with a dot, and sudo skips any
//     name containing one -- measured on both hosts below -- so sudo never
//     reads a half-written rule.
//   - It is owned by root, mode 0440. Both hosts' visudo -c called a 0644
//     drop-in "bad permissions, should be mode 0440" and exited 1, while
//     sudo itself read it: the two disagree, and 0440 satisfies both.
//   - Once it is in place, `visudo -c` checks **the whole policy**, which
//     catches what the text alone cannot: an alias defined elsewhere is
//     only a warning to the stand-alone check. visudo lists every file
//     it parsed, so this also proves sudo reads the drop-in at all. If
//     either is not so, the previous text is put back (root, 0440) or the
//     new file removed, and the state fails, saying what visudo said --
//     and, by asking visudo once more, whether the policy passes again
//     without it or was failing for some other file's reason all along.
//
// The policy is not checked *before* the write as well, though that
// looks safer, because the commonest thing for it to be complaining
// about is this very drop-in: a mode drifted to 0640 makes `visudo -c`
// fail on this file, and a pre-check refused the one write that would
// have fixed it. Found on the first live run.
//
// # The name, and the directory
//
// sudo skips a drop-in whose name contains a `.` or ends in `~` --
// `halite-cap.b` and `halite-cap-e~` were both left out of visudo's
// listing and of `sudo -l` on both hosts -- so such a name is refused,
// rather than written and silently ignored. The directory is the one the
// node's sudoers file names in an `@includedir` (or the older
// `#includedir`) line, read from the file `sudo -V` says sudo reads:
// /usr/local/etc/sudoers.d on FreeBSD and /etc/sudoers.d on Debian, both
// read off the hosts. That line-level read finds the directory and
// nothing else; whether sudo really reads the file is visudo's answer
// afterwards, not this one's.
//
// Measured on FreeBSD 15.1 with sudo 1.9.17p2 and Debian 13 with sudo
// 1.9.16p2, as root, on 2026-09-30 (DIVERGENCE 5.184).
func sudoStateModules() []states.Module {
	dirDoc := "The drop-in directory. Defaults to the one the node's sudoers file includes with @includedir."
	return []states.Module{
		states.Module{
			Sig: signature.Signature{
				Module: "sudo", Function: "present",
				Doc: "Ensure a sudoers drop-in holds exactly this text, checked by visudo before it is installed and with the whole policy after.",
				Params: []signature.Param{
					nameParam("The drop-in's file name, with no `.` and not ending in `~`, which sudo would skip. Defaults to the state ID."),
					req("contents", signature.String, "The sudoers text. A final newline is added if it has none."),
					opt("dir", signature.Path, "", dirDoc),
				},
				Mutates: true, TestMode: signature.TestReliable, Privileges: []string{"root"},
				Platforms: unixOnly, Section: "15.5",
			},
			Fn: sudoPresentState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "sudo", Function: "absent",
				Doc: "Ensure a sudoers drop-in does not exist.",
				Params: []signature.Param{
					nameParam("The drop-in's file name. Defaults to the state ID."),
					opt("dir", signature.Path, "", dirDoc),
				},
				Mutates: true, TestMode: signature.TestReliable, Privileges: []string{"root"},
				Platforms: unixOnly, Section: "15.5",
			},
			Fn: sudoAbsentState,
		},
	}
}

// sudoDropInMode is what both hosts' visudo -c demands of an included
// file; sudo itself is laxer, and the stricter of the two is the one
// that keeps `visudo -c` -- this state's own post-check -- passing.
const sudoDropInMode = 0o440

func sudoPresentState(c *exec.Context, args *value.Map) (states.Result, error) {
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if err := sudoCheckDropInName(name, true); err != nil {
		return states.False(err.Error()), nil
	}
	if c.Which("sudo") == "" || c.Which("visudo") == "" {
		return states.False("This node has no sudo and visudo, so it has no sudoers policy to add to."), nil
	}
	dir, err := sudoDropInDir(c, states.Str(args, "dir", ""))
	if err != nil {
		return states.False(err.Error()), nil
	}
	target := filepath.Join(dir, name)
	contents := states.Str(args, "contents", "")
	if !strings.HasSuffix(contents, "\n") {
		contents += "\n"
	}

	ok, said, err := sudoValidateText(c, contents)
	if err != nil {
		return states.False(err.Error()), nil
	}
	if !ok {
		return states.False(fmt.Sprintf("The sudoers text for %s was refused by visudo, so nothing was written: %s", target, said)), nil
	}

	old, oldMode, existed, err := sudoReadDropIn(target)
	if err != nil {
		return states.False(err.Error()), nil
	}
	if existed && bytes.Equal(old, []byte(contents)) && oldMode == "root:0:0440" {
		return states.True(fmt.Sprintf("The sudoers drop-in %s already holds this text, owned by root with mode 0440.", target)), nil
	}

	changes := value.NewMap(2)
	if !existed {
		changes.Set(target, states.Change(nil, contents))
	} else {
		if !bytes.Equal(old, []byte(contents)) {
			changes.Set(target, states.Change(string(old), contents))
		}
		if oldMode != "root:0:0440" {
			changes.Set("mode", states.Change(oldMode, "root:0:0440"))
		}
	}
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The sudoers drop-in %s would be written; visudo accepts its text.", target), changes), nil
	}

	if err := sudoWriteDropIn(target, []byte(contents)); err != nil {
		return states.False(fmt.Sprintf("The sudoers drop-in %s could not be written: %v", target, err)), nil
	}
	ok, said, err = sudoRunVisudo(c, []string{"visudo", "-c"}, "")
	if err == nil && ok && sudoListsFile(said, target) {
		return states.Changed(fmt.Sprintf("The sudoers drop-in %s was written, and visudo -c accepts the whole policy with it.", target), changes), nil
	}

	why := said
	switch {
	case err != nil:
		why = err.Error()
	case ok:
		why = "visudo -c did not list it among the files it parsed, so sudo does not read it: " + said
	}
	if restoreErr := sudoRestoreDropIn(target, old, existed); restoreErr != nil {
		return states.False(fmt.Sprintf("The sudoers drop-in %s was written and the policy then failed visudo -c (%s), "+
			"and putting the previous file back failed too: %v. Run visudo -c now.", target, why, restoreErr)), nil
	}
	if okAfter, saidAfter, err := sudoRunVisudo(c, []string{"visudo", "-c"}, ""); err == nil && !okAfter {
		return states.False(fmt.Sprintf("The sudoers drop-in %s was written and taken back out, because with it the policy "+
			"failed visudo -c (%s) -- and the policy still fails without it, so another file is at fault: %s",
			target, why, saidAfter)), nil
	}
	return states.False(fmt.Sprintf("The sudoers drop-in %s was written and taken back out, because with it the policy "+
		"failed visudo -c: %s", target, why)), nil
}

func sudoAbsentState(c *exec.Context, args *value.Map) (states.Result, error) {
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if err := sudoCheckDropInName(name, false); err != nil {
		return states.False(err.Error()), nil
	}
	if c.Which("sudo") == "" {
		return states.False("This node has no sudo, so it has no sudoers policy to remove a drop-in from."), nil
	}
	dir, err := sudoDropInDir(c, states.Str(args, "dir", ""))
	if err != nil {
		return states.False(err.Error()), nil
	}
	target := filepath.Join(dir, name)
	old, _, existed, err := sudoReadDropIn(target)
	if err != nil {
		return states.False(err.Error()), nil
	}
	if !existed {
		return states.True(fmt.Sprintf("The sudoers drop-in %s is already absent.", target)), nil
	}
	changes := value.MapOf(target, states.Change(string(old), nil))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The sudoers drop-in %s would be removed.", target), changes), nil
	}
	if err := os.Remove(target); err != nil {
		return states.False(fmt.Sprintf("The sudoers drop-in %s could not be removed: %v", target, err)), nil
	}
	return states.Changed(fmt.Sprintf("The sudoers drop-in %s was removed.", target), changes), nil
}

// sudoCheckDropInName refuses a name that is not a plain file name and,
// for a drop-in being written, one sudo would skip without a word.
func sudoCheckDropInName(name string, writing bool) error {
	switch {
	case name == "":
		return errors.New("A sudoers drop-in needs a file name.")
	case name == "." || name == ".." || strings.ContainsAny(name, "/\x00"):
		return fmt.Errorf("The sudoers drop-in name %q is not a plain file name.", name)
	case writing && strings.Contains(name, "."):
		return fmt.Errorf("The sudoers drop-in name %q contains a `.`, and sudo skips every such file in an "+
			"included directory; it would be written and never read.", name)
	case writing && strings.HasSuffix(name, "~"):
		return fmt.Errorf("The sudoers drop-in name %q ends in `~`, and sudo skips every such file in an "+
			"included directory; it would be written and never read.", name)
	}
	return nil
}

// sudoDropInDir finds the directory a drop-in goes in: the one given, or
// the one the node's sudoers file includes.
func sudoDropInDir(c *exec.Context, given string) (string, error) {
	if given = strings.TrimSpace(given); given != "" {
		if !filepath.IsAbs(given) {
			return "", fmt.Errorf("The drop-in directory %q is not an absolute path.", given)
		}
		return given, nil
	}
	found, err := sudoPath(c)
	if err != nil {
		return "", fmt.Errorf("The node's sudoers file could not be found: %v", err)
	}
	m, _ := found.(*value.Map)
	pathAny, _ := m.GetString("path")
	sudoers, _ := pathAny.(string)
	data, err := os.ReadFile(sudoers)
	if err != nil {
		return "", fmt.Errorf("The node's sudoers file %s could not be read to find its drop-in directory: %v", sudoers, err)
	}
	dirs := sudoIncludeDirs(string(data), filepath.Dir(sudoers))
	switch len(dirs) {
	case 0:
		return "", fmt.Errorf("The node's sudoers file %s includes no drop-in directory, so a drop-in would not be "+
			"read; name one with dir, or add an @includedir line.", sudoers)
	case 1:
		return dirs[0], nil
	}
	return "", fmt.Errorf("The node's sudoers file %s includes %d drop-in directories (%s); name the one meant with dir.",
		sudoers, len(dirs), strings.Join(dirs, ", "))
}

// sudoIncludeDirs reads the `@includedir` and `#includedir` lines out of
// sudoers text, and nothing else. A relative directory is relative to
// the including file's own directory, which is how sudo 1.9 reads one.
func sudoIncludeDirs(text, base string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		var rest string
		var ok bool
		if rest, ok = strings.CutPrefix(line, "@includedir"); !ok {
			if rest, ok = strings.CutPrefix(line, "#includedir"); !ok {
				continue
			}
		}
		if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
			continue
		}
		dir := strings.Trim(strings.TrimSpace(rest), `"`)
		if dir == "" {
			continue
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(base, dir)
		}
		out = append(out, dir)
	}
	return out
}

// sudoReadDropIn reads a drop-in and describes its ownership and mode as
// one string, so that "is it what was asked for" is one comparison.
func sudoReadDropIn(path string) (data []byte, mode string, existed bool, err error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, fmt.Errorf("The sudoers drop-in %s could not be read: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, "", false, fmt.Errorf("The sudoers drop-in %s is not a regular file; this state will not replace it.", path)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, "", false, fmt.Errorf("The sudoers drop-in %s could not be read: %v", path, err)
	}
	owner := value.NewMap(4)
	addOwnership(owner, path, info)
	uid, _ := owner.GetString("uid")
	gid, _ := owner.GetString("gid")
	ownerName := "unknown"
	if u, ok := uid.(int64); ok && u == 0 {
		ownerName = "root"
	} else if ok {
		ownerName = "uid " + strconv.FormatInt(u, 10)
	}
	gidText := "?"
	if g, ok := gid.(int64); ok {
		gidText = strconv.FormatInt(g, 10)
	}
	return data, fmt.Sprintf("%s:%s:%04o", ownerName, gidText, info.Mode().Perm()), true, nil
}

// sudoWriteDropIn installs a drop-in under a temporary name in the same
// directory -- one with a leading dot, which sudo does not read -- owned
// by root and mode 0440 before it is renamed into place.
func sudoWriteDropIn(path string, data []byte) error {
	return atomicfile.WritePrepared(path, data, sudoDropInMode, func(tmp string) error {
		return os.Chown(tmp, 0, 0)
	})
}

// sudoRestoreDropIn puts back what was there before a write the policy
// check refused: the previous text, as root and 0440 -- the mode visudo
// demands, whatever the file had -- or no file at all.
func sudoRestoreDropIn(path string, old []byte, existed bool) error {
	if !existed {
		return os.Remove(path)
	}
	return sudoWriteDropIn(path, old)
}

// sudoListsFile reports whether visudo -c named a file among the ones it
// parsed -- "<path>: parsed OK", one line each, on both hosts.
func sudoListsFile(said, path string) bool {
	for _, line := range strings.Split(said, "\n") {
		if strings.TrimSpace(line) == path+": parsed OK" {
			return true
		}
	}
	return false
}
