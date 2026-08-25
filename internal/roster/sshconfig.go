package roster

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ParseSSHConfig derives a roster from `~/.ssh/config`.
//
// SPEC 21.2's `sshconfig` backend. An estate that has already written
// down how to reach its machines should not have to write it again, and
// the connection details are ssh's business anyway — this reads the
// file only to learn the *names*, and lets ssh itself resolve what they
// mean at connection time.
//
// A pattern with a wildcard is skipped: `Host *` is settings for every
// host, not a machine to connect to, and treating it as one would put a
// target called `*` in every roster.
func ParseSSHConfig(src []byte, file string) (*Roster, error) {
	out := &Roster{}
	scanner := bufio.NewScanner(strings.NewReader(string(src)))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)

	var current []string
	settings := map[string]map[string]string{}

	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, rest := splitSSHOption(text)
		switch strings.ToLower(key) {
		case "host":
			current = nil
			for _, pattern := range strings.Fields(rest) {
				if strings.ContainsAny(pattern, "*?!") {
					continue
				}
				current = append(current, pattern)
				if _, seen := settings[pattern]; !seen {
					settings[pattern] = map[string]string{}
					out.Targets = append(out.Targets, Target{ID: pattern})
				}
			}
		case "match":
			// A conditional block. Its settings apply to whatever ssh
			// decides at connection time, which this reader cannot
			// know, so nothing here is attributed to a host.
			current = nil
		default:
			for _, host := range current {
				settings[host][strings.ToLower(key)] = rest
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}

	for i := range out.Targets {
		target := &out.Targets[i]
		fields := settings[target.ID]
		if host := fields["hostname"]; host != "" {
			target.Host = host
		}
		if user := fields["user"]; user != "" {
			target.User = user
		}
		if port := fields["port"]; port != "" {
			if n, err := strconv.Atoi(port); err == nil {
				target.Port = n
			}
		}
		if jump := fields["proxyjump"]; jump != "" {
			target.ProxyJump = jump
		}
		if key := fields["identityfile"]; key != "" {
			target.Priv = key
		}
		target.applyDefaults()
	}
	return out, nil
}

// ReadSSHConfig reads the operator's ssh config.
func ReadSSHConfig(path string) (*Roster, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("finding the ssh config: %w", err)
		}
		path = filepath.Join(home, ".ssh", "config")
	}
	src, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return ParseSSHConfig(src, path)
}

// splitSSHOption splits `Key value` and `Key=value`, both of which ssh
// accepts.
func splitSSHOption(line string) (string, string) {
	if key, rest, ok := strings.Cut(line, "="); ok {
		if !strings.Contains(strings.TrimSpace(key), " ") {
			return strings.TrimSpace(key), strings.TrimSpace(rest)
		}
	}
	key, rest, _ := strings.Cut(line, " ")
	return strings.TrimSpace(key), strings.TrimSpace(rest)
}
