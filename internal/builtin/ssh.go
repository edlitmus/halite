package builtin

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerSSH installs ssh_auth, which manages authorized_keys.
//
// The file is parsed into records rather than pattern-matched, because a
// careless edit to authorized_keys either locks an operator out or leaves
// a key behind that should have gone. The comparison is on the key blob
// itself, not on the whole line, so re-running a state whose comment
// changed does not rewrite the file.
func registerSSH(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "ssh", Function: "auth_keys",
				Doc:      "Return an account's authorized keys, keyed by the key blob.",
				Params:   []signature.Param{opt("user", signature.String, "", "Whose keys; defaults to the caller's.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				path, err := authorizedKeysPath(states.Str(args, "user", ""), "")
				if err != nil {
					return nil, err
				}
				keys := parseAuthorizedKeys(readLines(path))
				out := value.NewMap(len(keys))
				for _, k := range keys {
					if k.Raw != "" {
						continue
					}
					out.Set(k.Key, value.MapOf(
						"enc", k.Type,
						"comment", k.Comment,
						"options", toAnyList(k.Options),
					))
				}
				return out, nil
			},
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "ssh_auth", Function: "present",
				Doc: "Ensure a public key is in an account's authorized_keys.",
				Params: []signature.Param{
					nameParam("The key blob, or the whole key line. Defaults to the state ID."),
					opt("user", signature.String, "", "Whose authorized_keys; defaults to the caller's."),
					opt("enc", signature.String, "", "The key type, such as ssh-ed25519."),
					opt("comment", signature.String, "", "The trailing comment."),
					opt("options", signature.List, nil, "Leading options, such as no-pty."),
					opt("config", signature.Path, "", "An authorized_keys path other than the default."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"the target account, or root"},
				Section:    "15.5",
			},
			Fn: sshAuthPresent,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "ssh_auth", Function: "absent",
				Doc: "Ensure a public key is not in an account's authorized_keys.",
				Params: []signature.Param{
					nameParam("The key blob. Defaults to the state ID."),
					opt("user", signature.String, "", "Whose authorized_keys; defaults to the caller's."),
					opt("config", signature.Path, "", "An authorized_keys path other than the default."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"the target account, or root"},
				Section:    "15.5",
			},
			Fn: sshAuthAbsent,
		},
	)
}

// authKey is one authorized_keys record.
type authKey struct {
	Options []string
	Type    string
	Key     string
	Comment string
	// Raw holds a comment or blank line, preserved verbatim.
	Raw string
}

func (k authKey) String() string {
	if k.Raw != "" {
		return k.Raw
	}
	var parts []string
	if len(k.Options) > 0 {
		parts = append(parts, strings.Join(k.Options, ","))
	}
	parts = append(parts, k.Type, k.Key)
	if k.Comment != "" {
		parts = append(parts, k.Comment)
	}
	return strings.Join(parts, " ")
}

// parseAuthorizedKeys reads the file into records.
func parseAuthorizedKeys(lines []string) []authKey {
	var out []authKey
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out = append(out, authKey{Raw: line})
			continue
		}
		k, ok := parseAuthKeyLine(trimmed)
		if !ok {
			out = append(out, authKey{Raw: line})
			continue
		}
		out = append(out, k)
	}
	return out
}

// parseAuthKeyLine splits one line. The type is found by looking for the
// first field that looks like a key type, because the options field ahead
// of it may contain spaces inside quotes.
func parseAuthKeyLine(line string) (authKey, bool) {
	fields := strings.Fields(line)
	for i, f := range fields {
		if !looksLikeKeyType(f) {
			continue
		}
		if i+1 >= len(fields) {
			return authKey{}, false
		}
		k := authKey{Type: f, Key: fields[i+1]}
		if i > 0 {
			k.Options = strings.Split(strings.Join(fields[:i], " "), ",")
		}
		if i+2 < len(fields) {
			k.Comment = strings.Join(fields[i+2:], " ")
		}
		return k, true
	}
	return authKey{}, false
}

func looksLikeKeyType(s string) bool {
	switch {
	case strings.HasPrefix(s, "ssh-"),
		strings.HasPrefix(s, "ecdsa-sha2-"),
		strings.HasPrefix(s, "sk-ssh-"),
		strings.HasPrefix(s, "sk-ecdsa-"):
		return true
	}
	return false
}

// authorizedKeysPath resolves where an account's keys live.
func authorizedKeysPath(username, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	var u *user.User
	var err error
	if username == "" {
		u, err = user.Current()
	} else {
		u, err = user.Lookup(username)
	}
	if err != nil {
		return "", fmt.Errorf("the account %q could not be resolved: %w", username, err)
	}
	if u.HomeDir == "" {
		return "", fmt.Errorf("the account %q has no home directory", u.Username)
	}
	return filepath.Join(u.HomeDir, ".ssh", "authorized_keys"), nil
}

// keyBlobFrom reads the key out of a state's `name`, which may be the bare
// blob or a whole key line pasted from somewhere.
func keyBlobFrom(args *value.Map) authKey {
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if k, ok := parseAuthKeyLine(name); ok {
		// Explicit arguments still win over what the line carried.
		if enc := states.Str(args, "enc", ""); enc != "" {
			k.Type = enc
		}
		if comment := states.Str(args, "comment", ""); comment != "" {
			k.Comment = comment
		}
		if opts := states.Strings(args, "options"); len(opts) > 0 {
			k.Options = opts
		}
		return k
	}
	return authKey{
		Type:    states.Str(args, "enc", "ssh-rsa"),
		Key:     name,
		Comment: states.Str(args, "comment", ""),
		Options: states.Strings(args, "options"),
	}
}

func sshAuthPresent(c *exec.Context, args *value.Map) (states.Result, error) {
	want := keyBlobFrom(args)
	if want.Key == "" {
		return states.False("This state needs a public key."), nil
	}
	path, err := authorizedKeysPath(states.Str(args, "user", ""), states.Str(args, "config", ""))
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}

	existing := parseAuthorizedKeys(readLines(path))
	for i, k := range existing {
		if k.Key != want.Key {
			continue
		}
		// The key is there. Only the options and the comment can differ,
		// and neither is a reason to rewrite unless the state named them.
		if sameKeyDetails(k, want, args) {
			return states.True(fmt.Sprintf("The key is already present in %s.", path)), nil
		}
		existing[i] = want
		return writeAuthKeys(c, path, existing, value.MapOf(
			shortKey(want.Key), states.Change(k.String(), want.String())),
			fmt.Sprintf("The key's options were updated in %s.", path))
	}

	existing = append(existing, want)
	return writeAuthKeys(c, path, existing, value.MapOf(
		shortKey(want.Key), states.Change(nil, "present")),
		fmt.Sprintf("The key was added to %s.", path))
}

// sameKeyDetails reports whether the stored record already matches what
// the state asked for, considering only the fields it named.
func sameKeyDetails(have, want authKey, args *value.Map) bool {
	if len(states.Strings(args, "options")) > 0 && strings.Join(have.Options, ",") != strings.Join(want.Options, ",") {
		return false
	}
	if states.Str(args, "comment", "") != "" && have.Comment != want.Comment {
		return false
	}
	return true
}

func sshAuthAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
	want := keyBlobFrom(args)
	path, err := authorizedKeysPath(states.Str(args, "user", ""), states.Str(args, "config", ""))
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}

	existing := parseAuthorizedKeys(readLines(path))
	kept := make([]authKey, 0, len(existing))
	found := false
	for _, k := range existing {
		if k.Key == want.Key && k.Raw == "" {
			found = true
			continue
		}
		kept = append(kept, k)
	}
	if !found {
		return states.True(fmt.Sprintf("The key is already absent from %s.", path)), nil
	}
	return writeAuthKeys(c, path, kept, value.MapOf(
		shortKey(want.Key), states.Change("present", nil)),
		fmt.Sprintf("The key was removed from %s.", path))
}

// writeAuthKeys renders and writes the file, creating ~/.ssh with the mode
// sshd insists on.
func writeAuthKeys(c *exec.Context, path string, keys []authKey, changes *value.Map, comment string) (states.Result, error) {
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k.String())
		b.WriteByte('\n')
	}
	if c.Test {
		return states.WouldChange(strings.Replace(comment, " was ", " would be ", 1), changes), nil
	}
	// sshd refuses an authorized_keys file that is group or world
	// writable, and refuses a .ssh directory that is either, so both modes
	// are set rather than left to the umask.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return states.False(fmt.Sprintf("%s could not be created: %v", filepath.Dir(path), err)), nil
	}
	if err := writeAtomic(path, []byte(b.String()), 0o600); err != nil {
		return states.False(fmt.Sprintf("%s could not be written: %v", path, err)), nil
	}
	return states.Changed(comment, changes), nil
}

// shortKey renders a key blob for a change set without printing the whole
// thing.
func shortKey(key string) string {
	if len(key) <= 20 {
		return key
	}
	return key[:12] + "..." + key[len(key)-8:]
}
