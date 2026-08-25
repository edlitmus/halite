package roster

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// ReadAnsible reads an Ansible inventory.
//
// SPEC 21.2 names this backend for a stated reason: many estates have
// an inventory already, and asking them to write the same list of
// machines a second time to try this is asking them not to try it.
//
// Both forms, because Ansible accepts both and an estate has whichever
// it started with: the INI form with `[group]` headers and
// `host key=value` lines, and the YAML form with `all: hosts:`.
func ReadAnsible(path string) (*Roster, error) {
	if path == "" {
		return nil, fmt.Errorf("the ansible roster needs an inventory path")
	}
	src, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("reading the inventory: %w", err)
	}
	if looksLikeYAML(path, src) {
		return parseAnsibleYAML(src, path)
	}
	return parseAnsibleINI(src, path)
}

func looksLikeYAML(path string, src []byte) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yml", ".yaml":
		return true
	case ".ini":
		return false
	}
	// No extension to go on. An inventory in the INI form starts a
	// group with `[`, and the YAML form does not.
	for _, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return !strings.HasPrefix(trimmed, "[")
	}
	return false
}

// parseAnsibleINI reads the `[group]` form.
func parseAnsibleINI(src []byte, file string) (*Roster, error) {
	out := &Roster{}
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(src)))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)

	skip := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			// `[group:vars]` and `[group:children]` are not lists of
			// hosts, and reading them as one produces targets called
			// `ansible_user=deploy`.
			header := strings.Trim(line, "[]")
			skip = strings.Contains(header, ":")
			continue
		}
		if skip {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if seen[name] {
			continue
		}
		seen[name] = true

		target := Target{ID: name}
		for _, field := range fields[1:] {
			key, val, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			applyAnsibleVar(&target, key, strings.Trim(val, `"'`))
		}
		target.applyDefaults()
		out.Targets = append(out.Targets, target)
	}
	return out, scanner.Err()
}

// parseAnsibleYAML reads the `all: hosts:` form.
func parseAnsibleYAML(src []byte, file string) (*Roster, error) {
	doc, _, err := yaml.Parse(src, yaml.Options{File: file})
	if err != nil {
		return nil, err
	}
	root, ok := doc.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("%s is not an inventory", file)
	}
	out := &Roster{}
	seen := map[string]bool{}
	collectAnsibleHosts(root, out, seen)
	return out, nil
}

// collectAnsibleHosts walks the group tree gathering hosts.
//
// Recursive, because an inventory nests groups under `children` and a
// reader that only looked at the top would find the machines of an
// estate that keeps everything in one group and none of an estate that
// does not.
func collectAnsibleHosts(m *value.Map, out *Roster, seen map[string]bool) {
	for _, entry := range m.Entries() {
		key := value.KeyString(entry.Key)
		switch key {
		case "hosts":
			hosts, ok := entry.Val.(*value.Map)
			if !ok {
				continue
			}
			for _, host := range hosts.Entries() {
				name := value.KeyString(host.Key)
				if name == "" || seen[name] {
					continue
				}
				seen[name] = true
				target := Target{ID: name}
				if vars, ok := host.Val.(*value.Map); ok {
					for _, v := range vars.Entries() {
						applyAnsibleVar(&target, value.KeyString(v.Key), value.KeyString(v.Val))
					}
				}
				target.applyDefaults()
				out.Targets = append(out.Targets, target)
			}
		case "children", "all":
			if child, ok := entry.Val.(*value.Map); ok {
				collectAnsibleHosts(child, out, seen)
			}
		case "vars":
			// Group variables, which are not hosts.
		default:
			if child, ok := entry.Val.(*value.Map); ok {
				collectAnsibleHosts(child, out, seen)
			}
		}
	}
}

// applyAnsibleVar maps the inventory variables that mean something
// here.
//
// Only the connection ones. An inventory carries a great deal that is
// Ansible's business — `ansible_python_interpreter` most of all, which
// this design exists not to need.
func applyAnsibleVar(t *Target, key, val string) {
	switch key {
	case "ansible_host", "ansible_ssh_host":
		t.Host = val
	case "ansible_port", "ansible_ssh_port":
		if n, err := strconv.Atoi(val); err == nil {
			t.Port = n
		}
	case "ansible_user", "ansible_ssh_user":
		t.User = val
	case "ansible_ssh_private_key_file":
		t.Priv = val
	case "ansible_become":
		t.Sudo = val == "true" || val == "yes" || val == "True"
	case "ansible_become_user":
		t.SudoUser = val
	}
}
