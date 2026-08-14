package modules

import (
	"fmt"
	"runtime"
	"strings"
)

func init() {
	register("host.present", hostPresent)
	register("host.absent", hostAbsent)
}

// hostPresent ensures a hostname resolves to an address in the hosts file.
//
//	db1:
//	  host.present:
//	    - ip: 10.0.0.1
//	    - names:
//	      - db1
//	      - db1.internal
//
// Names are added to the line that already carries the address, so an
// entry with several names stays one line — which is what the file's
// readers expect, and what a second run has to leave alone.
func hostPresent(c *Ctx, id string, args map[string]any) Result {
	ip := Str(args, "ip", "")
	if ip == "" {
		return resFail("host.present needs an ip")
	}
	wanted := hostNames(id, args)
	clean := Bool(args, "clean", false)

	return editPath(c, hostsFile(args), args, func(current []byte, _ bool) ([]byte, string, error) {
		entries := parseHosts(splitLines(current))
		added, moved := 0, 0
		for _, name := range wanted {
			var change hostChange
			entries, change = addHostName(entries, ip, name, clean)
			switch change {
			case hostAdded:
				added++
			case hostMoved:
				moved++
			}
		}
		if added == 0 && moved == 0 {
			return current, "", nil
		}
		change := fmt.Sprintf("%s: %d name(s) added", ip, added)
		if moved > 0 {
			change += fmt.Sprintf(", %d moved from another address", moved)
		}
		return joinLines(renderHosts(entries)), change, nil
	})
}

// hostAbsent removes a hostname from the hosts file, and the line with it
// when nothing else is left on it.
func hostAbsent(c *Ctx, id string, args map[string]any) Result {
	wanted := hostNames(id, args)
	onlyIP := Str(args, "ip", "")

	return editPath(c, hostsFile(args), args, func(current []byte, _ bool) ([]byte, string, error) {
		entries := parseHosts(splitLines(current))
		removed := 0
		for _, name := range wanted {
			removed += removeHostName(entries, name, onlyIP)
		}
		if removed == 0 {
			return current, "", nil
		}
		return joinLines(renderHosts(entries)), fmt.Sprintf("%d name(s) removed", removed), nil
	})
}

// hostNames is the list of hostnames a state manages: `names`, or the
// single `name`.
func hostNames(id string, args map[string]any) []string {
	if names := List(args, "names"); len(names) > 0 {
		return names
	}
	return []string{Str(args, "name", id)}
}

// hostsFile is the file to edit: the platform's, or `config` when the
// state names one.
func hostsFile(args map[string]any) string {
	if path := Str(args, "config", ""); path != "" {
		return path
	}
	if runtime.GOOS == "windows" {
		return `C:\Windows\System32\drivers\etc\hosts`
	}
	return "/etc/hosts"
}

// hostEntry is one line of the hosts file. Lines that are not entries —
// comments, blanks — are kept verbatim in raw, so editing one address
// leaves the rest of the file exactly as it was.
type hostEntry struct {
	ip      string
	names   []string
	comment string
	raw     string // set for a line that is not an address entry
}

func (e hostEntry) isEntry() bool { return e.raw == "" }

// parseHosts reads the file into entries, keeping everything it does not
// understand.
func parseHosts(lines []string) []*hostEntry {
	out := make([]*hostEntry, 0, len(lines))
	for _, line := range lines {
		body, comment := line, ""
		if i := strings.IndexByte(line, '#'); i >= 0 {
			body, comment = line[:i], line[i:]
		}
		fields := strings.Fields(body)
		if len(fields) < 2 {
			out = append(out, &hostEntry{raw: line})
			continue
		}
		out = append(out, &hostEntry{ip: fields[0], names: fields[1:], comment: comment})
	}
	return out
}

// renderHosts writes entries back, dropping any left with no names.
func renderHosts(entries []*hostEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.isEntry() {
			out = append(out, e.raw)
			continue
		}
		if len(e.names) == 0 {
			continue
		}
		line := e.ip + "\t" + strings.Join(e.names, " ")
		if e.comment != "" {
			line += " " + e.comment
		}
		out = append(out, line)
	}
	return out
}

// hostChange is what adding a name to the file amounted to.
type hostChange int

const (
	hostUnchanged hostChange = iota
	hostAdded
	hostMoved
)

// addHostName puts one name on the line for ip, appending a new line when
// the address is not in the file yet. With clean, the name is taken off any
// other address first — a hostname that resolves to two addresses is
// usually a leftover, but removing it is destructive enough to be asked
// for.
func addHostName(entries []*hostEntry, ip, name string, clean bool) ([]*hostEntry, hostChange) {
	change := hostUnchanged
	if clean {
		for _, e := range entries {
			if e.isEntry() && e.ip != ip && hasName(e.names, name) {
				e.names = dropName(e.names, name)
				change = hostMoved
			}
		}
	}
	for _, e := range entries {
		if !e.isEntry() || e.ip != ip {
			continue
		}
		if hasName(e.names, name) {
			return entries, change
		}
		e.names = append(e.names, name)
		if change == hostUnchanged {
			change = hostAdded
		}
		return entries, change
	}
	if change == hostUnchanged {
		change = hostAdded
	}
	return append(entries, &hostEntry{ip: ip, names: []string{name}}), change
}

// removeHostName takes a name off every entry, or off one address when the
// state named it.
func removeHostName(entries []*hostEntry, name, onlyIP string) int {
	removed := 0
	for _, e := range entries {
		if !e.isEntry() || !hasName(e.names, name) {
			continue
		}
		if onlyIP != "" && e.ip != onlyIP {
			continue
		}
		e.names = dropName(e.names, name)
		removed++
	}
	return removed
}

func hasName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func dropName(names []string, drop string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != drop {
			out = append(out, n)
		}
	}
	return out
}
