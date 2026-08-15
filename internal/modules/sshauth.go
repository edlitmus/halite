package modules

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

func init() {
	register("ssh_auth.present", sshAuthPresent)
	register("ssh_auth.absent", sshAuthAbsent)
}

// sshAuthPresent ensures a public key is in a user's authorized_keys.
//
//	ed@laptop:
//	  ssh_auth.present:
//	    - user: ed
//	    - enc: ssh-ed25519
//	    - name: AAAAC3NzaC1lZDI1NTE5AAAAI...
//	    - options: no-agent-forwarding
//
// The key body identifies the entry, so options, encoding, and comment can
// be changed without orphaning it — and a key that is already there under
// a different comment is rewritten rather than duplicated.
func sshAuthPresent(c *Ctx, id string, args map[string]any) Result {
	entry, path, err := sshAuthTarget(id, args)
	if err != nil {
		return resFail("%v", err)
	}
	lines, err := readLines(path)
	if err != nil {
		return resFail("%v", err)
	}
	updated, action := upsertAuthorizedKey(lines, entry)
	if action == "" {
		return resOK(fmt.Sprintf("key %s is already authorized for %s", entry.fingerprintish(), entry.user))
	}
	if c.Test {
		return resWould(fmt.Sprintf("key %s would be %s for %s", entry.fingerprintish(), action, entry.user))
	}
	if err := writeAuthorizedKeys(path, updated, entry); err != nil {
		return resFail("%v", err)
	}
	return resChanged(fmt.Sprintf("key %s %s for %s", entry.fingerprintish(), action, entry.user),
		map[string]string{path: action})
}

// sshAuthAbsent removes a public key from a user's authorized_keys.
func sshAuthAbsent(c *Ctx, id string, args map[string]any) Result {
	entry, path, err := sshAuthTarget(id, args)
	if err != nil {
		return resFail("%v", err)
	}
	lines, err := readLines(path)
	if err != nil {
		return resFail("%v", err)
	}
	kept := make([]string, 0, len(lines))
	removed := 0
	for _, line := range lines {
		if keyBody(line) == entry.key {
			removed++
			continue
		}
		kept = append(kept, line)
	}
	if removed == 0 {
		return resOK(fmt.Sprintf("key %s is not authorized for %s", entry.fingerprintish(), entry.user))
	}
	if c.Test {
		return resWould(fmt.Sprintf("key %s would be removed for %s", entry.fingerprintish(), entry.user))
	}
	if err := writeAuthorizedKeys(path, kept, entry); err != nil {
		return resFail("%v", err)
	}
	return resChanged(fmt.Sprintf("key %s removed for %s", entry.fingerprintish(), entry.user),
		map[string]string{path: "removed"})
}

// authorizedKey is one authorized_keys entry.
type authorizedKey struct {
	user    string
	uid     int
	gid     int
	enc     string
	key     string
	comment string
	options string
}

// line renders the entry the way sshd reads it.
func (k authorizedKey) line() string {
	parts := make([]string, 0, 4)
	if k.options != "" {
		parts = append(parts, k.options)
	}
	parts = append(parts, k.enc, k.key)
	if k.comment != "" {
		parts = append(parts, k.comment)
	}
	return strings.Join(parts, " ")
}

// fingerprintish is a short, safe way to name a key in output: the last
// eight characters of its body. halite does not hash keys — this is for
// reading a log, not for verifying one.
func (k authorizedKey) fingerprintish() string {
	if len(k.key) <= 8 {
		return k.key
	}
	return "…" + k.key[len(k.key)-8:]
}

// sshAuthTarget builds the entry and resolves the authorized_keys path for
// the user it belongs to.
func sshAuthTarget(id string, args map[string]any) (authorizedKey, string, error) {
	name := Str(args, "user", "")
	if name == "" {
		return authorizedKey{}, "", fmt.Errorf("ssh_auth needs a user")
	}
	raw := strings.TrimSpace(Str(args, "name", id))
	if raw == "" {
		return authorizedKey{}, "", fmt.Errorf("ssh_auth needs a key")
	}
	entry := authorizedKey{
		user:    name,
		enc:     Str(args, "enc", "ssh-rsa"),
		comment: Str(args, "comment", ""),
		options: strings.Join(List(args, "options"), ","),
	}
	// The key may be given bare, or as a whole authorized_keys line: take
	// whichever fields the line carries.
	if parsed, ok := splitAuthorizedKey(raw); ok {
		entry.key = parsed.key
		entry.enc = parsed.enc
		if entry.comment == "" {
			entry.comment = parsed.comment
		}
		if entry.options == "" {
			entry.options = parsed.options
		}
	} else {
		entry.key = strings.Fields(raw)[0]
	}

	u, err := user.Lookup(name)
	if err != nil {
		return authorizedKey{}, "", fmt.Errorf("user %s: %w", name, err)
	}
	entry.uid, _ = strconv.Atoi(u.Uid)
	entry.gid, _ = strconv.Atoi(u.Gid)
	path := Str(args, "config", "")
	if path == "" {
		path = filepath.Join(u.HomeDir, ".ssh", "authorized_keys")
	}
	return entry, path, nil
}

// upsertAuthorizedKey returns the file's lines with the entry in place, and
// what had to happen: "added", "updated", or "" when it was already right.
func upsertAuthorizedKey(lines []string, entry authorizedKey) ([]string, string) {
	want := entry.line()
	for i, line := range lines {
		if keyBody(line) != entry.key {
			continue
		}
		if strings.TrimSpace(line) == want {
			return lines, ""
		}
		out := append([]string{}, lines...)
		out[i] = want
		return out, "updated"
	}
	return append(append([]string{}, lines...), want), "added"
}

// splitAuthorizedKey parses "[options] <type> <body> [comment]", the shape
// sshd reads. The type name is what anchors the parse: everything before it
// is options, the field after it is the key body, and the rest is comment.
func splitAuthorizedKey(line string) (authorizedKey, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return authorizedKey{}, false
	}
	fields := strings.Fields(line)
	for i, f := range fields {
		if !isKeyType(f) || i+1 >= len(fields) {
			continue
		}
		return authorizedKey{
			options: strings.Join(fields[:i], " "),
			enc:     f,
			key:     fields[i+1],
			comment: strings.Join(fields[i+2:], " "),
		}, true
	}
	return authorizedKey{}, false
}

// keyBody returns the base64 body of an authorized_keys line, which is what
// identifies the key. Comments, options, and the type name are not it.
func keyBody(line string) string {
	parsed, ok := splitAuthorizedKey(line)
	if !ok {
		return ""
	}
	return parsed.key
}

// refuseAuthorizedKeysSymlink checks the two paths this state writes
// through. MkdirAll is happy with a symlink to an existing directory and
// chown follows it, so the check has to happen before either runs.
func refuseAuthorizedKeysSymlink(dir, path string) error {
	for _, candidate := range []string{dir, path} {
		info, err := os.Lstat(candidate)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(candidate)
		if err != nil {
			target = "its target"
		}
		return fmt.Errorf("%s is a symlink to %s: refusing to write authorized keys through a link, because the account it belongs to controls this path",
			candidate, target)
	}
	return nil
}

// isKeyType reports whether a field is an SSH public key type name.
func isKeyType(field string) bool {
	for _, prefix := range []string{"ssh-", "ecdsa-sha2-", "sk-ssh-", "sk-ecdsa-"} {
		if strings.HasPrefix(field, prefix) {
			return true
		}
	}
	return false
}

// writeAuthorizedKeys writes the file with the modes sshd insists on, and
// the ownership of the user it belongs to.
func writeAuthorizedKeys(path string, lines []string, entry authorizedKey) error {
	dir := filepath.Dir(path)
	// The account being granted access owns this directory, so a link here
	// is either a mistake or an attempt to have root chown and write
	// somewhere else — `ln -s /etc ~/.ssh` and wait for a highstate. There
	// is no configuration that makes following one safe, so unlike the
	// file states this has no opt-in.
	if err := refuseAuthorizedKeysSymlink(dir, path); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := chown(dir, entry.uid, entry.gid); err != nil {
		return fmt.Errorf("chown %s: %w", dir, err)
	}
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	if err := atomicWrite(path, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := chown(path, entry.uid, entry.gid); err != nil {
		return fmt.Errorf("chown %s: %w", path, err)
	}
	return nil
}

// readLines reads a file into lines, treating a missing file as empty.
func readLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}
