package roster

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// ParseFlat reads the YAML roster of SPEC 21.2, which is the default
// and the one an estate already has.
//
// A mapping of target name to its settings:
//
//	web1.example:
//	  host: 10.0.0.4
//	  user: deploy
//	  sudo: true
//
// A bare string is the host, which is the shorthand Salt accepts.
func ParseFlat(src []byte, file string) (*Roster, error) {
	doc, warnings, err := yaml.Parse(src, yaml.Options{File: file})
	if err != nil {
		return nil, err
	}
	root, ok := doc.(*value.Map)
	if !ok {
		if doc == nil {
			// An empty roster is a roster with no targets, not an
			// error: an estate that has not filled one in yet should
			// get "no targets matched" rather than a parse failure.
			return &Roster{}, nil
		}
		return nil, fmt.Errorf("%s: a roster is a mapping of target to settings, not %s",
			file, value.TypeName(doc))
	}

	out := &Roster{}
	// A duplicate key or a YAML 1.1 boolean in a roster is worth
	// saying: `sudo: yes` reading as the string "yes" rather than true
	// is a run that silently does not use sudo.
	for _, w := range warnings {
		out.Warnings = append(out.Warnings, w.String())
	}
	for _, entry := range root.Entries() {
		id := value.KeyString(entry.Key)
		if id == "" {
			return nil, fmt.Errorf("%s: a target with no name", file)
		}
		target, warnings, err := targetFrom(id, entry.Val, file)
		if err != nil {
			return nil, err
		}
		out.Warnings = append(out.Warnings, warnings...)
		out.Targets = append(out.Targets, target)
	}
	return out, nil
}

// ReadFlat reads a roster file.
func ReadFlat(path string) (*Roster, error) {
	src, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("reading the roster: %w", err)
	}
	return ParseFlat(src, path)
}

// targetFrom reads one entry.
func targetFrom(id string, raw any, file string) (Target, []string, error) {
	target := Target{ID: id}
	var warnings []string

	switch v := raw.(type) {
	case nil:
		// `web1.example:` with nothing under it, which means "connect
		// to it by its own name".
	case string:
		target.Host = v
	case *value.Map:
		for _, e := range v.Entries() {
			key := value.KeyString(e.Key)
			switch key {
			case "host":
				target.Host = value.KeyString(e.Val)
			case "port":
				port, err := intFrom(e.Val)
				if err != nil {
					return Target{}, nil, fmt.Errorf("%s: %s: port: %w", file, id, err)
				}
				target.Port = port
			case "user":
				target.User = value.KeyString(e.Val)
			case "passwd", "password":
				target.Password = value.KeyString(e.Val)
				warnings = append(warnings, fmt.Sprintf(
					"%s: %s has a password in the roster. SPEC 21.2 discourages it: a roster "+
						"usually lives in the state tree, and a password there is a password "+
						"in whatever distributes the tree. Use a key or an agent.", file, id))
			case "priv":
				target.Priv = value.KeyString(e.Val)
			case "priv_passwd":
				target.PrivPasswd = value.KeyString(e.Val)
			case "sudo":
				target.Sudo = value.Truthy(e.Val)
			case "sudo_user":
				target.SudoUser = value.KeyString(e.Val)
			case "tty":
				target.TTY = value.Truthy(e.Val)
			case "timeout":
				d, err := durationFrom(e.Val)
				if err != nil {
					return Target{}, nil, fmt.Errorf("%s: %s: timeout: %w", file, id, err)
				}
				target.Timeout = d
			case "thin_dir":
				target.ThinDir = value.KeyString(e.Val)
			case "node_opts", "minion_opts": // lexicon:allow
				opts, ok := e.Val.(*value.Map)
				if !ok {
					return Target{}, nil, fmt.Errorf("%s: %s: %s is a mapping, not %s",
						file, id, key, value.TypeName(e.Val))
				}
				target.NodeOpts = opts
			case "set_path":
				target.SetPath = value.KeyString(e.Val)
			case "tunnel":
				target.Tunnel = value.Truthy(e.Val)
			case "identities_only":
				target.IdentitiesOnly = value.Truthy(e.Val)
			case "proxy_jump":
				target.ProxyJump = value.KeyString(e.Val)
			case "grains":
				grains, ok := e.Val.(*value.Map)
				if !ok {
					return Target{}, nil, fmt.Errorf("%s: %s: grains is a mapping, not %s",
						file, id, value.TypeName(e.Val))
				}
				target.Grains = grains
			default:
				// A misspelt roster field is a setting that does
				// nothing — `sudo_usr` leaves the run as root — and
				// this project refuses those everywhere else.
				return Target{}, nil, fmt.Errorf(
					"%s: %s: %q is not a roster field; SPEC 21.2 names host, port, user, "+
						"passwd, priv, priv_passwd, sudo, sudo_user, tty, timeout, thin_dir, "+
						"node_opts, set_path, tunnel, identities_only, proxy_jump, and grains",
					file, id, key)
			}
		}
	default:
		return Target{}, nil, fmt.Errorf("%s: %s is %s, and a target is a host or a mapping",
			file, id, value.TypeName(raw))
	}

	target.applyDefaults()
	return target, warnings, nil
}

func intFrom(v any) (int, error) {
	switch t := v.(type) {
	case int64:
		return int(t), nil
	case int:
		return t, nil
	case string:
		var n int
		if _, err := fmt.Sscanf(t, "%d", &n); err != nil {
			return 0, fmt.Errorf("%q is not a number", t)
		}
		return n, nil
	}
	return 0, fmt.Errorf("%s is not a number", value.TypeName(v))
}

func durationFrom(v any) (time.Duration, error) {
	switch t := v.(type) {
	case int64:
		// Salt's roster spells a timeout as bare seconds.
		return time.Duration(t) * time.Second, nil
	case string:
		d, err := time.ParseDuration(strings.TrimSpace(t))
		if err != nil {
			return 0, fmt.Errorf("%q is not a duration", t)
		}
		return d, nil
	}
	return 0, fmt.Errorf("%s is not a duration", value.TypeName(v))
}
