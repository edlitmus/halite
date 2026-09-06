package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// NetplanDir is where netplan reads its configuration, as a variable so
// a test can point it somewhere harmless.
var NetplanDir = "/etc/netplan"

// registerNetplan installs the netplan module of SPEC 15.3's Debian row.
//
// Netplan is Ubuntu's network configuration: YAML under /etc/netplan,
// rendered into systemd-networkd or NetworkManager configuration by
// `netplan generate`, and made live by `netplan apply`.
//
// **Applying network configuration is the most dangerous thing in this
// build, and this module does not do it by default.** A firewall rule
// that locks an operator out is recoverable through the console; a
// network configuration that does is recoverable through the console
// only if the machine has one, and a cloud instance often does not.
// There is no equivalent of ufw's `--dry-run` here that proves an
// applied configuration will keep the node reachable, because no such
// thing can exist: whether the node is still reachable is a fact about
// the network and not about the file.
//
// So the state writes the file and validates it, which is safe and
// idempotent, and runs `netplan apply` only when a declaration asks for
// it by name. That leaves the tree honest about what it did — the
// declared configuration is on disk, and it takes effect at the next
// apply or the next boot — rather than converging by doing the thing
// that ends the session.
//
// `netplan try` is not used and is worth saying why. It applies with an
// automatic rollback unless a person confirms at a terminal, which is
// exactly the right tool for a human at a console and useless from a
// state run: with nobody to confirm, it always rolls back, so a state
// built on it would report success for a configuration that was undone
// seconds later.
func registerNetplan(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "netplan", Function: "list",
				Doc:       "Return netplan's configuration files, in the order netplan reads them.",
				TestMode:  signature.TestNotApplicable,
				Platforms: debianOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				files, err := netplanFiles()
				if err != nil {
					return nil, err
				}
				return toAnyList(files), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "netplan", Function: "get",
				Doc: "Return one netplan configuration file, parsed.",
				Params: []signature.Param{
					req("name", signature.String, "The file, with or without the directory and the .yaml suffix."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: debianOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return netplanGet(states.Str(args, "name", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "netplan", Function: "generate",
				Doc: "Validate the configuration and render the backend's files, without making anything live. " +
					"This is the check that a configuration is well formed; it is not a check that it will keep this node reachable.",
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  debianOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				return true, netplanGenerate(c)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "netplan", Function: "apply",
				Doc:     "Make the configuration live. This can make the node unreachable, and nothing here can tell you in advance whether it will.",
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  debianOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				return true, netplanApply(c)
			},
		},
	)

	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "netplan", Function: "managed",
			Doc: "Ensure a netplan configuration file holds the declared configuration.",
			Params: []signature.Param{
				nameParam("The file, with or without the directory and the .yaml suffix. Defaults to the state ID."),
				req("config", signature.Map, "The netplan document, from `network:` down."),
				opt("apply", signature.Bool, false,
					"Run `netplan apply` after writing. Off by default: applying network configuration can make this node unreachable, and no check here can say in advance whether it will."),
			},
			Mutates:    true,
			TestMode:   signature.TestReliable,
			Privileges: []string{"root"},
			Platforms:  debianOnly,
			Section:    "15.3",
		},
		Fn: netplanManaged,
	})
}

// netplanPath resolves a name to a path under the configuration
// directory, accepting the three spellings a tree is written in.
func netplanPath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("this needs the name of a netplan file")
	}
	// A path somewhere else is refused rather than written: netplan reads
	// one directory, and a file outside it is a file that does nothing
	// and looks like it did something.
	//
	// Compared on the directory rather than asked whether it is
	// absolute. `filepath.IsAbs` is a question about the *running*
	// platform — "/etc/netplan/x.yaml" is not absolute on Windows, which
	// has no root without a drive — so a check built on it would refuse
	// on Linux and accept on a machine somebody was writing the tree on.
	if strings.ContainsAny(name, `\/`) &&
		filepath.Clean(filepath.Dir(name)) != filepath.Clean(NetplanDir) {
		return "", fmt.Errorf("%s is outside %s, which is the only directory netplan reads", name, NetplanDir)
	}
	base := filepath.Base(name)
	if !strings.HasSuffix(base, ".yaml") && !strings.HasSuffix(base, ".yml") {
		base += ".yaml"
	}
	return filepath.Join(NetplanDir, base), nil
}

// netplanFiles lists the configuration in the order netplan reads it,
// which is lexical by filename — the reason everything in that
// directory is numbered.
func netplanFiles() ([]string, error) {
	entries, err := os.ReadDir(NetplanDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
			out = append(out, filepath.Join(NetplanDir, name))
		}
	}
	sort.Strings(out)
	return out, nil
}

func netplanGet(name string) (*value.Map, error) {
	path, err := netplanPath(name)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	doc, _, err := yaml.Parse(raw, yaml.DefaultOptions(path))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m, _ := doc.(*value.Map)
	if m == nil {
		return value.NewMap(0), nil
	}
	return m, nil
}

func netplanGenerate(c *exec.Context) error {
	if err := haveDpkg(c, "netplan"); err != nil {
		return err
	}
	res, err := c.Run(exec.Command{Argv: []string{"netplan", "generate"}, IgnoreExitCode: true})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s", strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return nil
}

func netplanApply(c *exec.Context) error {
	if err := haveDpkg(c, "netplan"); err != nil {
		return err
	}
	res, err := c.Run(exec.Command{Argv: []string{"netplan", "apply"}, IgnoreExitCode: true})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s", strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return nil
}

// netplanManaged writes one configuration file and validates it.
func netplanManaged(c *exec.Context, args *value.Map) (states.Result, error) {
	path, err := netplanPath(states.Str(args, "name", ""))
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}
	config := states.Mapping(args, "config")
	if config == nil || config.Len() == 0 {
		return states.False(fmt.Sprintf("%s: this state needs `config`.", filepath.Base(path))), nil
	}
	if _, ok := config.Get("network"); !ok {
		// Every netplan document is rooted at `network:`. A document
		// without it renders to nothing and netplan does not complain,
		// so a tree that omitted it would apply an empty configuration
		// and report success.
		return states.False(fmt.Sprintf(
			"%s: a netplan document is rooted at `network:`, and this one is not; "+
				"without it netplan reads an empty configuration and says nothing.",
			filepath.Base(path))), nil
	}

	want := yaml.Encode(config, yaml.EncodeOptions{Indent: 2})
	have, readErr := os.ReadFile(filepath.Clean(path))
	unchanged := readErr == nil && string(have) == want

	changes := value.NewMap(2)
	if !unchanged {
		changes.Set(path, states.Change(netplanSummary(string(have), readErr), "the declared configuration"))
	}

	// The mode is part of the state rather than a detail. netplan
	// refuses to read a configuration file that others can read, because
	// it can hold a wireless passphrase, and a file left at 0644 is a
	// warning on every run at best.
	modeWrong := false
	if info, err := os.Stat(path); err == nil && info.Mode().Perm() != 0o600 {
		modeWrong = true
		changes.Set(path+" mode", states.Change(info.Mode().Perm().String(), "-rw-------"))
	}

	if changes.Len() == 0 {
		return states.True(fmt.Sprintf("%s already holds the declared configuration.", path)), nil
	}
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be written.%s", path, netplanApplyNote(args)), changes), nil
	}

	if err := os.MkdirAll(NetplanDir, 0o755); err != nil {
		return states.False(fmt.Sprintf("%s could not be created: %v", NetplanDir, err)), nil
	}
	if !unchanged {
		if err := writeAtomic(path, []byte(want), 0o600); err != nil {
			return states.False(fmt.Sprintf("%s could not be written: %v", path, err)), nil
		}
	} else if modeWrong {
		if err := os.Chmod(path, 0o600); err != nil {
			return states.False(fmt.Sprintf("%s's mode could not be set: %v", path, err)), nil
		}
	}

	// Validated after writing, because that is the only way netplan will
	// look at it, and a configuration that does not render is one an
	// operator wants to know about before they apply it rather than
	// after.
	if err := netplanGenerate(c); err != nil {
		return states.False(fmt.Sprintf(
			"%s was written and netplan will not render it: %v. The file is on disk and nothing has been applied.",
			path, err)), nil
	}

	if !states.Bool(args, "apply", false) {
		return states.Changed(fmt.Sprintf(
			"%s was written and validated. It is not live: `netplan apply` or a reboot makes it so, "+
				"and this state does not apply unless asked, because applying network configuration can end the session that asked.",
			path), changes), nil
	}
	if err := netplanApply(c); err != nil {
		return states.False(fmt.Sprintf(
			"%s was written and validated, and `netplan apply` failed: %v", path, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s was written, validated and applied.", path), changes), nil
}

func netplanApplyNote(args *value.Map) string {
	if states.Bool(args, "apply", false) {
		return " It would then be applied, which can make this node unreachable."
	}
	return " It would not be applied; `apply: true` asks for that."
}

// netplanSummary describes what is there now, without printing a file
// that may carry a wireless passphrase into a change set.
func netplanSummary(have string, readErr error) any {
	if readErr != nil {
		return nil
	}
	lines := strings.Count(strings.TrimRight(have, "\n"), "\n") + 1
	return fmt.Sprintf("%d line(s)", lines)
}
