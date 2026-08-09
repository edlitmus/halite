package modules

import (
	"fmt"
	"runtime"
	"strings"
)

func init() {
	register("cron.present", cronPresent)
	register("cron.absent", cronAbsent)
}

// cronEntry builds the marker comment and crontab line for a state.
// Entries are identified by a "# halite: <identifier>" marker on the
// preceding line, so the command can change without orphaning the entry.
func cronEntry(id string, args map[string]any) (marker, entry string) {
	sched := []string{
		Str(args, "minute", "*"),
		Str(args, "hour", "*"),
		Str(args, "daymonth", "*"),
		Str(args, "month", "*"),
		Str(args, "dayweek", "*"),
	}
	command := Str(args, "name", id)
	ident := Str(args, "identifier", id)
	return "# halite: " + ident, strings.Join(sched, " ") + " " + command
}

func readCrontab(user string) (string, bool) {
	argv := []string{"crontab", "-l"}
	if user != "" {
		argv = []string{"crontab", "-u", user, "-l"}
	}
	out, _, rc, err := run(argv[0], argv[1:]...)
	if err != nil || rc != 0 {
		return "", false // no crontab yet
	}
	return out, true
}

func writeCrontab(user, content string) error {
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	argv := []string{"crontab", "-"}
	if user != "" {
		argv = []string{"crontab", "-u", user, "-"}
	}
	_, errOut, rc, err := runIn(content, argv[0], argv[1:]...)
	if err != nil {
		return err
	}
	if rc != 0 {
		return fmt.Errorf("crontab exited %d: %s", rc, strings.TrimSpace(errOut))
	}
	return nil
}

// cronPresent ensures a crontab entry exists.
//
//	converge:
//	  cron.present:
//	    - name: /usr/local/bin/halite apply /usr/local/etc/halite/base.sls
//	    - minute: "*/30"
//	    - user: root
func cronPresent(c *Ctx, id string, args map[string]any) Result {
	if runtime.GOOS == "windows" {
		return resFail("cron is not supported on Windows (scheduled tasks: planned)")
	}
	if !has("crontab") {
		return resFail("crontab binary not found")
	}
	user := Str(args, "user", "")
	marker, entry := cronEntry(id, args)

	current, _ := readCrontab(user)
	lines := []string{}
	if current != "" {
		lines = strings.Split(strings.TrimRight(current, "\n"), "\n")
	}

	// Find existing managed entry.
	for i, l := range lines {
		if strings.TrimSpace(l) == marker {
			if i+1 < len(lines) && lines[i+1] == entry {
				return resOK(fmt.Sprintf("cron entry %q is present", marker))
			}
			if c.Test {
				return resWould(fmt.Sprintf("cron entry %q would be updated", marker))
			}
			old := ""
			if i+1 < len(lines) {
				old = lines[i+1]
				lines[i+1] = entry
			} else {
				lines = append(lines, entry)
			}
			if err := writeCrontab(user, strings.Join(lines, "\n")); err != nil {
				return resFail("%v", err)
			}
			return resChanged("cron entry updated", map[string]string{"old": old, "new": entry})
		}
	}
	if c.Test {
		return resWould(fmt.Sprintf("cron entry %q would be added", marker))
	}
	lines = append(lines, marker, entry)
	if err := writeCrontab(user, strings.Join(lines, "\n")); err != nil {
		return resFail("%v", err)
	}
	return resChanged("cron entry added", map[string]string{"new": entry})
}

func cronAbsent(c *Ctx, id string, args map[string]any) Result {
	if runtime.GOOS == "windows" {
		return resFail("cron is not supported on Windows")
	}
	if !has("crontab") {
		return resFail("crontab binary not found")
	}
	user := Str(args, "user", "")
	marker, _ := cronEntry(id, args)

	current, ok := readCrontab(user)
	if !ok || current == "" {
		return resOK("no crontab, entry already absent")
	}
	lines := strings.Split(strings.TrimRight(current, "\n"), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == marker {
			if c.Test {
				return resWould(fmt.Sprintf("cron entry %q would be removed", marker))
			}
			end := i + 2
			if end > len(lines) {
				end = len(lines)
			}
			out := append(append([]string{}, lines[:i]...), lines[end:]...)
			if err := writeCrontab(user, strings.Join(out, "\n")); err != nil {
				return resFail("%v", err)
			}
			return resChanged("cron entry removed", map[string]string{"removed": marker})
		}
	}
	return resOK(fmt.Sprintf("cron entry %q already absent", marker))
}
