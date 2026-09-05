//go:build !windows

package builtin

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/value"
)

// persistentEnviron returns the store a unix keeps the environment in.
//
// There is only one, and it is the machine's. A per-user equivalent that
// a login actually reads does not exist portably: ~/.pam_environment was
// the closest thing and pam removed it in 1.5, and a shell rc file is
// read by that shell and by nothing else — not by a desktop session, not
// by a systemd user unit, not by cron. Saying so is better than writing
// to ~/.profile and calling it the user's environment.
func persistentEnviron(scope string) (environStore, error) {
	switch strings.ToLower(scope) {
	case "", "machine", "system":
		return etcEnvironStore{path: EtcEnvironmentPath}, nil
	case "user":
		return nil, fmt.Errorf(
			"a unix has no per-user environment file that a login reads; "+
				"scope: machine writes %s, which every session inherits",
			EtcEnvironmentPath)
	default:
		return nil, fmt.Errorf("scope must be machine or user, not %q", scope)
	}
}

// etcEnvironStore is /etc/environment, which pam_env reads into every
// session on Linux and which the BSDs read through login.conf's setenv.
//
// It is not a shell script: pam_env does not expand anything and does not
// run anything, so a value here is a literal.
type etcEnvironStore struct{ path string }

func (s etcEnvironStore) Name() string { return s.path }

func (s etcEnvironStore) Items() (*value.Map, error) {
	out := value.NewMap(16)
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := parseEnvironLine(line); ok {
			out.Set(k, v)
		}
	}
	return out, nil
}

func (s etcEnvironStore) Set(key, val string) error {
	return s.rewrite(key, val, false)
}

func (s etcEnvironStore) Unset(key string) error {
	return s.rewrite(key, "", true)
}

// Flush has nothing to do: pam_env reads the file at the next login, and
// there is no running thing holding a copy of it to be told.
func (s etcEnvironStore) Flush() {}

// rewrite replaces the variable's line in place, leaving every other line
// — comments included — exactly as it was. An operator's file is not this
// program's to reformat.
func (s etcEnvironStore) rewrite(key, val string, remove bool) error {
	b, err := os.ReadFile(s.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var lines []string
	if len(b) > 0 {
		lines = strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	}

	out := make([]string, 0, len(lines)+1)
	replaced := false
	for _, line := range lines {
		k, _, ok := parseEnvironLine(line)
		if !ok || k != key {
			out = append(out, line)
			continue
		}
		if remove {
			continue
		}
		if replaced {
			// A second line for the same variable is a duplicate the
			// last one wins over; dropping it is what makes the file
			// read back as what was declared.
			continue
		}
		out = append(out, environLine(key, val))
		replaced = true
	}
	if !remove && !replaced {
		out = append(out, environLine(key, val))
	}
	if len(out) == 0 {
		return writeAtomic(s.path, nil, 0o644)
	}
	return writeAtomic(s.path, []byte(strings.Join(out, "\n")+"\n"), 0o644)
}

// parseEnvironLine reads one KEY=value line, in both the bare and the
// quoted spelling that distributions ship, and skips anything else.
func parseEnvironLine(line string) (key, val string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	// Debian's file is read by pam_env, which accepts an `export` prefix
	// so the same file can be sourced by a shell.
	trimmed = strings.TrimPrefix(trimmed, "export ")
	k, v, found := strings.Cut(trimmed, "=")
	if !found {
		return "", "", false
	}
	k = strings.TrimSpace(k)
	if k == "" || strings.ContainsAny(k, " \t") {
		return "", "", false
	}
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		if unquoted, err := strconv.Unquote(v); err == nil {
			return k, unquoted, true
		}
		v = v[1 : len(v)-1]
	}
	return k, v, true
}

// environLine renders a variable, quoting only when the value needs it.
// A value pam_env would split or a shell would reinterpret is quoted; a
// plain one is left plain, because that is how the shipped file reads and
// a rewrite that quotes everything looks like a change to every line.
func environLine(key, val string) string {
	if val != "" && !strings.ContainsAny(val, " \t\"'\\$`#") {
		return key + "=" + val
	}
	return key + "=" + strconv.Quote(val)
}
