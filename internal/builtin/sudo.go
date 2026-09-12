package builtin

import (
	"errors"
	"fmt"
	"runtime"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerSudo installs the `sudo` module of SPEC 15.2.
//
// # It is not Salt's `sudo` module, and that is deliberate
//
// Salt's module of this name has one function, `sudo.salt_call`, which
// runs a call on the node as another account by shelling out to `sudo`.
// This
// build already does that better: `cmd.run` takes a `runas` and applies
// it with setuid and setgid directly, with no second privilege system in
// the path and no sudoers rule required to make it work. Reimplementing
// `salt_call` here would add a way to do something this build can
// already do, by a route that can fail for reasons the caller cannot
// see. It is not built, and this comment is the record of that decision.
//
// # What it does instead is the thing that file.managed cannot
//
// A sudoers file is text, and `file.managed` writes text. What it cannot
// do is tell you whether the text is *valid* before it becomes the file
// that decides who may become root. A malformed sudoers does not
// degrade -- sudo refuses to run at all, for everybody, and the account
// that could fix it is the one that just stopped working. That is the
// worst shape of failure this project recognises: a change that removes
// the means of undoing itself.
//
// `sudo.validate` is therefore the function the rest of this module
// exists for. It runs the real `visudo -c`, which is sudo's own parser
// at sudo's own grammar version, against a file that is not yet
// installed.
//
// **No sudoers parser is written here.** sudoers has includes, aliases,
// host and runas specifications, and a grammar with a version number of
// its own -- `sudo -V` on this host reports grammar version 50. A second
// parser for it in this module would disagree with the real one
// eventually, and would do so about who may become root. Every question
// this module answers is answered by asking sudo or visudo.
//
// # The path is asked for, not assumed
//
// The estate's own Salt tree branches in Jinja to find the sudoers file:
//
//	{%- if grains['os'] != "FreeBSD" %}
//	/etc/sudoers:
//	{%- else %}
//	/usr/local/etc/sudoers:
//	{%- endif %}
//
// which is right for those two platforms and wrong for any build that
// was configured with a different prefix. `sudo -V` reports the path
// sudo will actually read:
//
//	Sudoers path: /usr/local/etc/sudoers
//
// so `sudo.path` asks. The catch is that sudo prints its plugin
// configuration only to root; an unprivileged caller gets the version
// lines and nothing else. So there is a fallback to the platform's
// conventional location, and the answer says which route produced it,
// because a silent fallback is a defect that hides twice.
func registerSudo(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "sudo", Function: "version",
				Doc:       "Return the version of sudo on this node, or an empty string if it has none.",
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return sudoVersion(c) },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "sudo", Function: "path",
				Doc: "Return the sudoers file this node's sudo reads, and how that was determined.",
				// Reading `sudo -V` fully needs root, and the answer
				// says so rather than the function refusing: the
				// fallback is useful and the caller is told it is one.
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return sudoPath(c) },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "sudo", Function: "validate",
				Doc: "Check a sudoers file with the real `visudo -c`, without installing it.",
				Params: []signature.Param{
					opt("path", signature.Path, "", "The file to check; defaults to the node's own sudoers."),
				},
				// It changes nothing, which is the entire point: this is
				// what a tree calls *before* it writes the file.
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return sudoValidate(c, states.Str(args, "path", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "sudo", Function: "list",
				Doc: "Return what sudo says an account may run, as sudo itself reports it.",
				Params: []signature.Param{
					req("name", signature.String, "The account to ask about."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  unixOnly,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return sudoList(c, states.Str(args, "name", ""))
			},
		},
	)
}

// sudoVersion reports the version, or "" on a node without sudo.
//
// Absent is an answer rather than an error. sudo is not part of a base
// FreeBSD or a minimal Linux, and a tree asking "is sudo here" should
// get "no" rather than a failure it has to interpret.
func sudoVersion(c *exec.Context) (any, error) {
	if c.Which("sudo") == "" {
		return "", nil
	}
	res, err := c.Run(exec.Command{Argv: []string{"sudo", "-V"}, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("sudo could not be run on this node: %w", err)
	}
	// The first line is `Sudo version 1.9.17p2`. Every other line
	// depends on whether the caller is root, so only this one is read.
	first := strings.TrimSpace(firstLine(res.Stdout))
	if v, ok := strings.CutPrefix(first, "Sudo version "); ok {
		return strings.TrimSpace(v), nil
	}
	return nil, fmt.Errorf(
		"`sudo -V` did not begin with a version line; it printed %q", first)
}

// sudoDefaultPath is the conventional sudoers location per platform.
//
// Only a fallback. It is what the package managers put there, not what
// this node's sudo necessarily reads: a sudo built with a different
// --sysconfdir reads somewhere else entirely, and only sudo knows.
func sudoDefaultPath(goos string) (string, bool) {
	switch goos {
	case "freebsd", "openbsd", "netbsd", "dragonfly", "darwin":
		// The BSDs install sudo from ports or packages under
		// /usr/local, and darwin's own is in /etc.
		if goos == "darwin" {
			return "/etc/sudoers", true
		}
		return "/usr/local/etc/sudoers", true
	case "linux":
		return "/etc/sudoers", true
	}
	return "", false
}

// sudoPath reports the sudoers file in effect and how it was found.
func sudoPath(c *exec.Context) (any, error) {
	if c.Which("sudo") == "" {
		return nil, errors.New("this node has no `sudo`, so it reads no sudoers file")
	}
	out := value.NewMap(3)

	res, err := c.Run(exec.Command{Argv: []string{"sudo", "-V"}, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("sudo could not be run on this node: %w", err)
	}
	if path, ok := sudoPathFromVersion(res.Stdout); ok {
		out.Set("path", path)
		out.Set("source", "sudo")
		out.Set("comment", "reported by `sudo -V`, which is the file this node's sudo will read")
		return out, nil
	}

	path, ok := sudoDefaultPath(runtime.GOOS)
	if !ok {
		return nil, fmt.Errorf(
			"`sudo -V` did not report a sudoers path and this build knows no conventional "+
				"location for %s. Reading the plugin configuration needs root; from an "+
				"unprivileged account sudo prints its version and nothing else", runtime.GOOS)
	}
	out.Set("path", path)
	out.Set("source", "convention")
	out.Set("comment", "`sudo -V` did not report a path, which is what it does for a caller that "+
		"is not root, so this is the conventional location for "+runtime.GOOS+" and not a "+
		"statement about this node's sudo. Ask again as root to have it confirmed")
	return out, nil
}

// sudoPathFromVersion pulls the sudoers path out of `sudo -V`.
//
// The line is `Sudoers path: /usr/local/etc/sudoers`, printed among the
// plugin's configuration, which sudo shows only to root.
func sudoPathFromVersion(out string) (string, bool) {
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Sudoers path:")
		if !ok {
			continue
		}
		if rest = strings.TrimSpace(rest); rest != "" {
			return rest, true
		}
	}
	return "", false
}

// sudoValidate runs visudo's own check over a file.
//
// An invalid file is a *result*, not an error: asking "is this sudoers
// valid" and being told "no, at line 1 column 9" is the function working
// exactly as intended, and a tree needs to branch on it rather than
// abort. An error is reserved for not being able to ask at all.
func sudoValidate(c *exec.Context, path string) (any, error) {
	if c.Which("visudo") == "" {
		return nil, errors.New(
			"this node has no `visudo`; it ships with sudo and is what validates a sudoers file. " +
				"This build does not parse sudoers itself, because a second parser for it would " +
				"eventually disagree with the real one about who may become root")
	}
	argv := []string{"visudo", "-c"}
	if path = strings.TrimSpace(path); path != "" {
		argv = append(argv, "-f", path)
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("visudo could not be run on this node: %w", err)
	}
	said := strings.TrimSpace(res.Stdout + res.Stderr)
	out := value.NewMap(4)
	out.Set("valid", res.Code == 0)
	out.Set("comment", said)
	out.Set("checked", path)
	if path == "" {
		out.Set("checked", "this node's own sudoers")
	}
	return out, nil
}

// sudoList reports what sudo grants an account.
//
// `sudo -l -U <name>` is sudo answering about its own policy, which is
// the only answer worth having: it accounts for includes, aliases and
// the order rules are matched in, none of which reading the file would.
//
// An unknown account is an error rather than an empty list, because
// "this user may run nothing" and "there is no such user" are different
// facts and a tree that confuses them would report a missing account as
// a correctly locked-down one.
func sudoList(c *exec.Context, name string) (any, error) {
	if name = strings.TrimSpace(name); name == "" {
		return nil, errors.New("sudo.list needs an account name")
	}
	if c.Which("sudo") == "" {
		return nil, errors.New("this node has no `sudo`")
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"sudo", "-l", "-U", name},
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, fmt.Errorf("sudo could not be run on this node: %w", err)
	}
	said := strings.TrimSpace(res.Stdout + res.Stderr)
	if res.Code != 0 {
		if strings.Contains(strings.ToLower(said), "unknown user") {
			return nil, fmt.Errorf("there is no account called %q on this node", name)
		}
		return nil, fmt.Errorf("`sudo -l -U %s` exited %d: %s", name, res.Code, firstLine(said))
	}
	rules := sudoParseList(res.Stdout)
	out := value.NewMap(3)
	out.Set("name", name)
	out.Set("rules", rules)
	out.Set("raw", said)
	return out, nil
}

// sudoParseList reads the indented rule lines out of `sudo -l -U`.
//
// The output is a sentence followed by the rules, one per line and
// indented:
//
//	User ed may run the following commands on beastie:
//	    (ALL) ALL
//
// Only the indentation is relied on, because the sentence is localised
// and the rules are not. `raw` is returned alongside so that a caller
// this reader disappoints still has what sudo said.
func sudoParseList(out string) []any {
	var rules []any
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			continue
		}
		rules = append(rules, strings.TrimSpace(line))
	}
	return rules
}
