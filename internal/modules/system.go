package modules

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func init() {
	register("timezone.system", timezoneSystem)
	register("locale.system", localeSystem)
}

// timezoneSystem sets the system timezone.
//
//	America/Los_Angeles:
//	  timezone.system
//
// The zone name is the tzdata one, and it has to exist in the zoneinfo
// database: a typo that left the host on UTC would be a silent success.
func timezoneSystem(c *Ctx, id string, args map[string]any) Result {
	zone := Str(args, "name", id)
	if zone == "" {
		return resFail("timezone.system needs a zone name")
	}
	if runtime.GOOS == "windows" {
		return resFail("timezone.system is not implemented on Windows")
	}
	zoneFile := filepath.Join("/usr/share/zoneinfo", zone)
	if _, err := os.Stat(zoneFile); err != nil {
		return resFail("unknown timezone %q (no %s)", zone, zoneFile)
	}

	current := currentTimezone()
	if current == zone {
		return resOK(fmt.Sprintf("timezone is %s", zone))
	}
	if c.Test {
		return resWould(fmt.Sprintf("timezone would change from %q to %s", current, zone))
	}
	if err := setTimezone(zone, zoneFile); err != nil {
		return resFail("%v", err)
	}
	return resChanged(fmt.Sprintf("timezone set to %s", zone),
		map[string]string{"timezone": orUnknown(current) + " -> " + zone})
}

// currentTimezone reads the zone the host is on, by whichever of the three
// mechanisms this platform uses.
func currentTimezone() string {
	if has("timedatectl") {
		if out, ok := pkgQuery("timedatectl", "show", "-p", "Timezone", "--value"); ok && out != "" {
			return out
		}
	}
	// FreeBSD records the name it copied; Linux without systemd usually has
	// /etc/timezone; everything else has the symlink.
	for _, path := range []string{"/var/db/zoneinfo", "/etc/timezone"} {
		if body := strings.TrimSpace(readFile(path)); body != "" {
			return body
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, zone, found := strings.Cut(target, "zoneinfo/"); found {
			return zone
		}
	}
	return ""
}

// setTimezone applies the zone. timedatectl owns the setting where it
// exists; elsewhere the zoneinfo file is installed directly, which is what
// FreeBSD's tzsetup and Debian's dpkg-reconfigure both come down to.
func setTimezone(zone, zoneFile string) error {
	if has("timedatectl") {
		if _, err := pkgRun("timedatectl", "set-timezone", zone); err != nil {
			return fmt.Errorf("timedatectl: %w", err)
		}
		return nil
	}
	body, err := os.ReadFile(zoneFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", zoneFile, err)
	}
	if err := atomicWrite("/etc/localtime", body, 0o644); err != nil {
		return fmt.Errorf("write /etc/localtime: %w", err)
	}
	// Record the name too, so the next run compares names rather than
	// guessing from file contents.
	record := "/etc/timezone"
	if runtime.GOOS == "freebsd" {
		record = "/var/db/zoneinfo"
	}
	if err := atomicWrite(record, []byte(zone+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", record, err)
	}
	return nil
}

// localeSystem sets the system locale.
//
//	en_US.UTF-8:
//	  locale.system
//
// Linux only: FreeBSD has no single system-wide locale setting — it is
// login.conf and shell profiles — and pretending otherwise would write a
// file nothing reads.
func localeSystem(c *Ctx, id string, args map[string]any) Result {
	locale := Str(args, "name", id)
	if locale == "" {
		return resFail("locale.system needs a locale name")
	}
	if runtime.GOOS != "linux" {
		return resFail("locale.system is implemented on Linux only (%s has no single system locale)", runtime.GOOS)
	}
	key := Str(args, "key", "LANG")

	path := localeConfPath()
	current := localeValue(readFile(path), key)
	if current == locale {
		return resOK(fmt.Sprintf("%s is %s", key, locale))
	}
	if c.Test {
		return resWould(fmt.Sprintf("%s would change from %q to %s", key, current, locale))
	}

	if has("localectl") {
		if _, err := pkgRun("localectl", "set-locale", key+"="+locale); err != nil {
			return resFail("localectl: %v", err)
		}
	} else if err := writeLocaleConf(path, key, locale); err != nil {
		return resFail("%v", err)
	}
	return resChanged(fmt.Sprintf("%s set to %s", key, locale),
		map[string]string{key: orUnknown(current) + " -> " + locale})
}

// localeConfPath is where this distribution keeps the system locale.
func localeConfPath() string {
	if _, err := os.Stat("/etc/default/locale"); err == nil {
		return "/etc/default/locale" // Debian and its derivatives
	}
	return "/etc/locale.conf"
}

// localeValue reads one KEY=value from a locale configuration file.
func localeValue(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		name, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found || name != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"`)
	}
	return ""
}

// writeLocaleConf sets one key, keeping the other lines as they are.
func writeLocaleConf(path, key, value string) error {
	lines := splitLines([]byte(readFile(path)))
	replaced := false
	for i, line := range lines {
		if name, _, found := strings.Cut(strings.TrimSpace(line), "="); found && name == key {
			lines[i] = key + "=" + value
			replaced = true
		}
	}
	if !replaced {
		lines = append(lines, key+"="+value)
	}
	if err := atomicWrite(path, joinLines(lines), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func orUnknown(value string) string {
	if value == "" {
		return "(unset)"
	}
	return value
}
