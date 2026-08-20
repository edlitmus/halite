package builtin

import (
	"fmt"
	"os"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// HostsPath is the file the hosts module manages. It is a variable so a
// test can point it somewhere harmless.
var HostsPath = "/etc/hosts"

// registerHosts installs the hosts module and the host state.
//
// The file is parsed and rewritten rather than pattern-matched, because
// /etc/hosts is one of the few files where a careless regular expression
// can take a machine off the network.
func registerHosts(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "hosts", Function: "list_hosts",
				Doc:      "Return the hosts file as a mapping of address to names.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				entries, err := readHosts()
				if err != nil {
					return nil, err
				}
				out := value.NewMap(len(entries))
				for _, e := range entries {
					names := make([]any, len(e.Names))
					for i, n := range e.Names {
						names[i] = n
					}
					out.Set(e.Address, names)
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "hosts", Function: "get_ip",
				Doc:      "Return the address the hosts file gives a name, or an empty string.",
				Params:   []signature.Param{req("host", signature.String, "The name to look up.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				entries, err := readHosts()
				if err != nil {
					return nil, err
				}
				want := states.Str(args, "host", "")
				for _, e := range entries {
					for _, n := range e.Names {
						if n == want {
							return e.Address, nil
						}
					}
				}
				return "", nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "hosts", Function: "has_pair",
				Doc: "Report whether the hosts file already maps an address to a name.",
				Params: []signature.Param{
					req("ip", signature.String, "The address."),
					req("alias", signature.String, "The name."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				entries, err := readHosts()
				if err != nil {
					return nil, err
				}
				ip, alias := states.Str(args, "ip", ""), states.Str(args, "alias", "")
				for _, e := range entries {
					if e.Address != ip {
						continue
					}
					for _, n := range e.Names {
						if n == alias {
							return true, nil
						}
					}
				}
				return false, nil
			},
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "host", Function: "present",
				Doc: "Ensure the hosts file maps an address to one or more names.",
				Params: []signature.Param{
					nameParam("The name. Defaults to the state ID."),
					opt("names", signature.List, nil, "Several names for one address."),
					req("ip", signature.String, "The address."),
					opt("clean", signature.Bool, false, "Remove the names from any other address first."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: hostPresent,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "host", Function: "absent",
				Doc: "Ensure the hosts file does not map an address to a name.",
				Params: []signature.Param{
					nameParam("The name. Defaults to the state ID."),
					opt("names", signature.List, nil, "Several names."),
					opt("ip", signature.String, "", "Restrict the removal to one address."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: hostAbsent,
		},
	)
}

// hostEntry is one address and the names mapped to it.
type hostEntry struct {
	Address string
	Names   []string
	// Comment is the trailing comment, preserved so a rewrite does not
	// destroy an operator's note.
	Comment string
	// Raw holds a line that is not an entry — a comment or a blank — so
	// the file's shape survives a rewrite.
	Raw string
}

func readHosts() ([]hostEntry, error) {
	b, err := os.ReadFile(HostsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return parseHosts(string(b)), nil
}

func parseHosts(text string) []hostEntry {
	var out []hostEntry
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out = append(out, hostEntry{Raw: line})
			continue
		}
		body, comment := trimmed, ""
		if i := strings.IndexByte(trimmed, '#'); i >= 0 {
			body, comment = strings.TrimSpace(trimmed[:i]), strings.TrimSpace(trimmed[i+1:])
		}
		fields := strings.Fields(body)
		if len(fields) < 2 {
			out = append(out, hostEntry{Raw: line})
			continue
		}
		out = append(out, hostEntry{Address: fields[0], Names: fields[1:], Comment: comment})
	}
	return out
}

func renderHosts(entries []hostEntry) string {
	var b strings.Builder
	for _, e := range entries {
		if e.Address == "" {
			b.WriteString(e.Raw)
			b.WriteByte('\n')
			continue
		}
		if len(e.Names) == 0 {
			// An address with no names left is dropped rather than
			// written as a dangling line.
			continue
		}
		b.WriteString(e.Address)
		b.WriteByte('\t')
		b.WriteString(strings.Join(e.Names, " "))
		if e.Comment != "" {
			b.WriteString("\t# ")
			b.WriteString(e.Comment)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// hostNames reads the name or names a host state is about.
func hostNames(args *value.Map) []string {
	if names := states.Strings(args, "names"); len(names) > 0 {
		return names
	}
	if n := states.Str(args, "name", ""); n != "" {
		return []string{n}
	}
	return nil
}

func hostPresent(c *exec.Context, args *value.Map) (states.Result, error) {
	ip := states.Str(args, "ip", "")
	names := hostNames(args)
	if ip == "" || len(names) == 0 {
		return states.False("This state needs an address and at least one name."), nil
	}

	entries, err := readHosts()
	if err != nil {
		return states.False(fmt.Sprintf("%s could not be read: %v", HostsPath, err)), nil
	}
	before := renderHosts(entries)

	if states.Bool(args, "clean", false) {
		entries = removeNamesFrom(entries, names, "")
	}

	found := false
	for i := range entries {
		if entries[i].Address != ip {
			continue
		}
		found = true
		for _, n := range names {
			if !containsString(entries[i].Names, n) {
				entries[i].Names = append(entries[i].Names, n)
			}
		}
		break
	}
	if !found {
		entries = append(entries, hostEntry{Address: ip, Names: names})
	}

	after := renderHosts(entries)
	if before == after {
		return states.True(fmt.Sprintf("%s already maps %s to %s.", HostsPath, ip, strings.Join(names, ", "))), nil
	}

	changes := value.MapOf(ip, states.Change(addressNames(parseHosts(before), ip), addressNames(entries, ip)))
	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("%s would map %s to %s.", HostsPath, ip, strings.Join(names, ", ")), changes), nil
	}
	if err := writeAtomic(HostsPath, []byte(after), 0o644); err != nil {
		return states.False(fmt.Sprintf("%s could not be written: %v", HostsPath, err)), nil
	}
	return states.Changed(
		fmt.Sprintf("%s now maps %s to %s.", HostsPath, ip, strings.Join(names, ", ")), changes), nil
}

func hostAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
	names := hostNames(args)
	if len(names) == 0 {
		return states.False("This state needs at least one name."), nil
	}
	entries, err := readHosts()
	if err != nil {
		return states.False(fmt.Sprintf("%s could not be read: %v", HostsPath, err)), nil
	}
	before := renderHosts(entries)
	entries = removeNamesFrom(entries, names, states.Str(args, "ip", ""))
	after := renderHosts(entries)

	if before == after {
		return states.True(fmt.Sprintf("%s already has no entry for %s.", HostsPath, strings.Join(names, ", "))), nil
	}
	changes := value.MapOf("removed", strings.Join(names, ", "))
	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("%s would drop %s.", HostsPath, strings.Join(names, ", ")), changes), nil
	}
	if err := writeAtomic(HostsPath, []byte(after), 0o644); err != nil {
		return states.False(fmt.Sprintf("%s could not be written: %v", HostsPath, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s dropped %s.", HostsPath, strings.Join(names, ", ")), changes), nil
}

// removeNamesFrom drops names from every entry, or from one address when
// restrictTo is set.
func removeNamesFrom(entries []hostEntry, names []string, restrictTo string) []hostEntry {
	out := make([]hostEntry, 0, len(entries))
	for _, e := range entries {
		if e.Address == "" || (restrictTo != "" && e.Address != restrictTo) {
			out = append(out, e)
			continue
		}
		kept := make([]string, 0, len(e.Names))
		for _, n := range e.Names {
			if !containsString(names, n) {
				kept = append(kept, n)
			}
		}
		e.Names = kept
		if len(kept) == 0 {
			continue
		}
		out = append(out, e)
	}
	return out
}

func addressNames(entries []hostEntry, ip string) string {
	for _, e := range entries {
		if e.Address == ip {
			return strings.Join(e.Names, " ")
		}
	}
	return ""
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// readLines reads a file into lines, returning nothing when it is absent.
// Several modules read a system file this way.
func readLines(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}
