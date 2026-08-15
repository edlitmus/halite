package modules

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func init() {
	register("file.append", fileAppend)
	register("file.prepend", filePrepend)
	register("file.comment", fileComment)
	register("file.uncomment", fileUncomment)
}

// editFile is the shared body of every state that changes part of a file
// rather than managing the whole of it: read, transform, and write back
// atomically, reporting a line diff.
//
// The transform sees the current contents and whether the file is there,
// and returns what the file should hold plus a phrase describing the
// change. Returning the contents unchanged means the state is satisfied.
func editFile(c *Ctx, id string, args map[string]any,
	transform func(current []byte, exists bool) (updated []byte, change string, err error)) Result {
	return editPath(c, Str(args, "name", id), args, transform)
}

// editPath is editFile against a path the state names some other way — the
// host states edit /etc/hosts, whose `name` is a hostname.
func editPath(c *Ctx, name string, args map[string]any,
	transform func(current []byte, exists bool) (updated []byte, change string, err error)) Result {
	current, readErr := os.ReadFile(name)
	exists := readErr == nil
	if !exists && !os.IsNotExist(readErr) {
		return resFail("read %s: %v", name, readErr)
	}
	if !exists {
		current = nil
	}

	updated, change, err := transform(current, exists)
	if err != nil {
		return resFail("%v", err)
	}
	if updated == nil || bytes.Equal(current, updated) {
		return resOK(fmt.Sprintf("%s is already in the wanted state", name))
	}
	if c.Test {
		return resWould(fmt.Sprintf("%s: %s", name, change))
	}

	mode, err := editMode(args, name, exists)
	if err != nil {
		return resFail("%v", err)
	}
	if Bool(args, "makedirs", false) {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return resFail("mkdir %s: %v", filepath.Dir(name), err)
		}
	}
	// The owner is read before the write, because atomicWrite renames a
	// file this process created: without restoring it, editing one line of
	// a user's dotfile as root would hand the file to root.
	uid, gid, hadOwner := -1, -1, false
	if exists {
		uid, gid, hadOwner = statOwner(name)
	}
	if err := atomicWrite(name, updated, mode); err != nil {
		return resFail("write %s: %v", name, err)
	}
	if hadOwner {
		if err := chown(name, uid, gid); err != nil {
			return resFail("chown %s: %v", name, err)
		}
	}
	if err := applyEditOwner(name, args); err != nil {
		return resFail("%v", err)
	}

	changes := map[string]string{name: change}
	if Bool(args, "show_diff", true) {
		if diff := lineDiff(current, updated); diff != "" {
			changes["diff"] = diff
		}
	}
	return resChanged(fmt.Sprintf("%s: %s", name, change), changes)
}

// editMode keeps an existing file's permissions unless the state names
// them: an edit is not the place to change a mode by accident.
func editMode(args map[string]any, name string, exists bool) (os.FileMode, error) {
	if mode, err := parseModeArg(args, "mode"); err != nil {
		return 0, err
	} else if mode != 0 {
		return mode, nil
	}
	if exists {
		if st, err := os.Stat(name); err == nil {
			return st.Mode().Perm(), nil
		}
	}
	return 0o644, nil
}

// applyEditOwner sets ownership when the state asks for it.
func applyEditOwner(name string, args map[string]any) error {
	userName, groupName := Str(args, "user", ""), Str(args, "group", "")
	if userName == "" && groupName == "" {
		return nil
	}
	uid, gid, err := resolveOwner(userName, groupName)
	if err != nil {
		return err
	}
	if err := setOwner(name, uid, gid, Bool(args, FollowSymlinksArg, false)); err != nil {
		return err
	}
	return nil
}

// withoutArg returns the arguments with one key removed, for a state whose
// spelling of an argument differs from the shared machinery's.
func withoutArg(args map[string]any, key string) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		if k != key {
			out[k] = v
		}
	}
	return out
}

// textLines returns the state's `text` argument as lines. A list is one
// entry per line, a string is split on newlines, so both spellings Salt
// accepts mean the same thing.
func textLines(id string, args map[string]any) []string {
	var out []string
	for _, entry := range List(args, "text") {
		out = append(out, strings.Split(strings.TrimRight(entry, "\n"), "\n")...)
	}
	if len(out) == 0 {
		out = strings.Split(strings.TrimRight(Str(args, "name", id), "\n"), "\n")
	}
	return out
}

// fileAppend ensures each line of text is present, adding what is missing
// at the end of the file.
//
//	/etc/rc.conf:
//	  file.append:
//	    - text:
//	      - 'nginx_enable="YES"'
//
// A line already somewhere in the file is left where it is: appending is
// about presence, not position.
func fileAppend(c *Ctx, id string, args map[string]any) Result {
	return addLines(c, id, args, false)
}

// filePrepend is file.append at the top of the file.
func filePrepend(c *Ctx, id string, args map[string]any) Result {
	return addLines(c, id, args, true)
}

func addLines(c *Ctx, id string, args map[string]any, atTop bool) Result {
	wanted := textLines(id, args)
	return editFile(c, id, args, func(current []byte, _ bool) ([]byte, string, error) {
		lines := splitLines(current)
		var missing []string
		for _, want := range wanted {
			if !containsLine(lines, want) && !containsLine(missing, want) {
				missing = append(missing, want)
			}
		}
		if len(missing) == 0 {
			return current, "", nil
		}
		if atTop {
			lines = append(missing, lines...)
		} else {
			lines = append(lines, missing...)
		}
		where := "appended"
		if atTop {
			where = "prepended"
		}
		return joinLines(lines), fmt.Sprintf("%d line(s) %s", len(missing), where), nil
	})
}

// fileComment comments out every line matching a regular expression.
//
//	/etc/ssh/sshd_config:
//	  file.comment:
//	    - regex: ^PermitRootLogin yes
func fileComment(c *Ctx, id string, args map[string]any) Result {
	return commentLines(c, id, args, true)
}

// fileUncomment removes the comment character from lines whose remainder
// matches the regular expression.
func fileUncomment(c *Ctx, id string, args map[string]any) Result {
	return commentLines(c, id, args, false)
}

func commentLines(c *Ctx, id string, args map[string]any, comment bool) Result {
	char := Str(args, "char", "#")
	pattern := Str(args, "regex", "")
	if pattern == "" {
		return resFail("file.comment needs a regex")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return resFail("invalid regex %q: %v", pattern, err)
	}
	return editFile(c, id, args, func(current []byte, exists bool) ([]byte, string, error) {
		if !exists {
			return nil, "", fmt.Errorf("%s does not exist", Str(args, "name", id))
		}
		lines := splitLines(current)
		touched := 0
		for i, line := range lines {
			if comment {
				// An already-commented line must not be commented twice, so
				// the match is tried against the line as it stands.
				if strings.HasPrefix(strings.TrimSpace(line), char) || !re.MatchString(line) {
					continue
				}
				lines[i] = char + line
				touched++
				continue
			}
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, char) {
				continue
			}
			body := strings.TrimPrefix(trimmed, char)
			if !re.MatchString(body) {
				continue
			}
			lines[i] = body
			touched++
		}
		if touched == 0 {
			return current, "", nil
		}
		verb := "commented"
		if !comment {
			verb = "uncommented"
		}
		return joinLines(lines), fmt.Sprintf("%d line(s) %s", touched, verb), nil
	})
}

// splitLines splits file contents into lines, dropping the trailing empty
// element a final newline produces.
func splitLines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
}

// joinLines is splitLines reversed: a trailing newline is restored, since
// a config file without one is a nuisance to the next tool that reads it.
func joinLines(lines []string) []byte {
	if len(lines) == 0 {
		return nil
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}
