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

// registerLocale installs the `locale` execution module of SPEC 15.2 and
// its `locale.system` and `locale.present` states of SPEC 15.5, under
// Salt's names (DIVERGENCE 5.181).
//
// # One locale, two spellings
//
// glibc names a locale's codeset loosely: `en_US.UTF-8`, `en_US.utf8`
// and `en_US.UTF8` are one locale, because glibc normalises the codeset
// (lower case, letters and digits only) before it looks anything up.
// Nothing else here does. Captured on 2026-09-30:
//
//   - `locale -a` prints `en_US.utf8` on Rocky 9.8, Alma 8.10 and
//     Debian 13.
//   - `localectl list-locales` prints `en_US.UTF-8` on Rocky 9 (systemd
//     252) and Debian 13 (257), and `en_US.utf8` -- plus codeset-less
//     names such as `en_AG` -- on Alma 8 (239).
//   - `localectl set-locale LANG=en_GB.utf8` writes `LANG=en_GB.utf8` to
//     /etc/locale.conf verbatim, and `localectl status` then reports
//     that spelling, on all three.
//   - Debian's /etc/locale.gen and /usr/share/i18n/SUPPORTED spell it
//     `en_GB.UTF-8 UTF-8`.
//
// So a state that compared the text it was given with the text a tool
// printed would find `en_US.UTF-8` missing from `locale -a` on every
// host, and a node set to `en_GB.utf8` out of band would be "changed" to
// `en_GB.UTF-8` on every run. Every comparison here goes through
// canonicalLocale, which is glibc's own normalisation applied to the
// codeset alone -- the language, territory and modifier are compared as
// written, because glibc does -- and nothing is ever rewritten to a
// canonical spelling on the node: the operator's spelling is what lands
// in the file.
//
// # Where a locale comes from
//
// The families differ, and this follows each one's own mechanism rather
// than Salt's single one:
//
//   - **Debian** keeps the list of locales to compile in /etc/locale.gen,
//     validated against /usr/share/i18n/SUPPORTED, and compiles them with
//     `locale-gen`. `gen_locale` enables the SUPPORTED line (uncommenting
//     it where it is present commented, as it is for every SUPPORTED
//     locale on Debian 13) and runs `locale-gen --keep-existing`, which
//     compiles only what is not already loadable.
//   - **EL 8 and 9** ship compiled locales as `glibc-langpack-<language>`
//     packages and carry no locale sources to compile from:
//     /usr/share/i18n/locales was empty on both lab hosts. Salt's
//     `localedef` path therefore cannot work there, so `gen_locale`
//     installs the language's langpack through the node's package
//     provider instead.
//
// # Setting it
//
// `localectl set-locale LANG=…`, and only LANG: measured on all three
// hosts, localectl keeps every LC_* variable the call does not name
// (LC_TIME and LC_MESSAGES survived a LANG-only call), so Salt's habit of
// re-sending them all is not needed. A locale the node cannot load is
// refused before localectl is asked, because the families answer it
// differently: EL's localed refuses ("Locale de_DE.UTF-8 not installed,
// refusing"), and Debian's is patched to try to enable its generation,
// which would edit /etc/locale.gen behind the state's back.
//
// # Platforms
//
// Linux only, by declaration. FreeBSD keeps a login's locale in
// login.conf's `:lang=` and `:charset=` capabilities, per login class,
// and has no system-locale setting to set; Salt's module does not
// support it either, and nothing here was run on a BSD.
func registerLocale(r *Registries) {
	r.Exec.Add(localeExecModules()...)
	r.States.Add(localeStateModules()...)
}

// These are the files the Debian mechanism reads, as variables so a
// unit test can point them at a captured copy.
var (
	localeGenPath   = "/etc/locale.gen"
	localeSupported = "/usr/share/i18n/SUPPORTED"
)

// canonicalLocale is glibc's _nl_normalize_codeset applied to the codeset
// of a locale name: letters lower-cased, digits kept, everything else
// dropped, and an all-digit result prefixed `iso`. `en_US.UTF-8` and
// `en_US.utf8` both become `en_US.utf8`; `de_DE.ISO-8859-15@euro`
// becomes `de_DE.iso885915@euro`; `en_US`, with no codeset, is left
// alone, because it is a different locale (ISO-8859-1 on both families).
func canonicalLocale(name string) string {
	name = strings.TrimSpace(name)
	base, modifier, hasModifier := strings.Cut(name, "@")
	lang, codeset, hasCodeset := strings.Cut(base, ".")
	if hasCodeset {
		var b strings.Builder
		digitsOnly := true
		for _, r := range codeset {
			switch {
			case r >= 'a' && r <= 'z':
				b.WriteRune(r)
				digitsOnly = false
			case r >= 'A' && r <= 'Z':
				b.WriteRune(r + ('a' - 'A'))
				digitsOnly = false
			case r >= '0' && r <= '9':
				b.WriteRune(r)
			}
		}
		codeset = b.String()
		if digitsOnly {
			codeset = "iso" + codeset
		}
		base = lang + "." + codeset
	}
	if hasModifier {
		return base + "@" + modifier
	}
	return base
}

func sameLocale(a, b string) bool {
	return a != "" && b != "" && canonicalLocale(a) == canonicalLocale(b)
}

// localeLanguage is the part of a locale name a langpack is named after:
// `de` for `de_DE.UTF-8`, `sr` for `sr_RS@latin`.
func localeLanguage(name string) string {
	end := strings.IndexAny(name, "_.@")
	if end < 0 {
		return name
	}
	return name[:end]
}

// localeName refuses a name that would not reach localectl, locale-gen or
// a package name as one plain word.
func localeName(args *value.Map, key string) (string, error) {
	name := strings.TrimSpace(states.Str(args, key, ""))
	if name == "" {
		return "", fmt.Errorf("a locale name is required")
	}
	if strings.HasPrefix(name, "-") || strings.ContainsAny(name, " \t\r\n=\"'`$;:/\\") {
		return "", fmt.Errorf("%q is not a locale name", name)
	}
	return name, nil
}

// localeListAvail is Salt's `list_avail`: `locale -a`, one per line.
func localeListAvail(c *exec.Context) ([]string, error) {
	if c.Which("locale") == "" {
		return nil, fmt.Errorf("locale(1) is not on this node's PATH")
	}
	res, err := c.Run(exec.Command{Argv: []string{"locale", "-a"}, IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("locale -a exited %d: %s", res.Code, firstLine(res.Stderr))
	}
	return nonEmptyLines(res.Stdout), nil
}

func localeAvail(c *exec.Context, name string) (bool, error) {
	avail, err := localeListAvail(c)
	if err != nil {
		return false, err
	}
	for _, a := range avail {
		if sameLocale(a, name) {
			return true, nil
		}
	}
	return false, nil
}

// parseLocalectlStatus reads the System Locale block of `localectl
// status`: the first assignment follows the label, and each further one
// is a line of its own, indented under it, with no label. Captured with
// three variables set:
//
//	System Locale: LANG=en_GB.UTF-8
//	               LC_TIME=en_US.UTF-8
//	               LC_MESSAGES=C.UTF-8
//	    VC Keymap: us
//
// Alma 8's systemd 239 indents the whole block three columns further,
// which is why nothing here counts columns.
func parseLocalectlStatus(out string) map[string]string {
	vars := map[string]string{}
	inBlock := false
	for _, line := range strings.Split(out, "\n") {
		text := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(text, "System Locale:"); ok {
			inBlock = true
			text = strings.TrimSpace(rest)
		} else if !inBlock {
			continue
		} else if label, _, isLabel := strings.Cut(text, ":"); isLabel && !strings.Contains(label, "=") {
			break
		}
		// Anything that is not NAME=value -- `n/a` when nothing is set --
		// contributes no variable.
		if k, v, ok := strings.Cut(text, "="); ok && k != "" && !strings.ContainsAny(k, " \t") {
			vars[k] = v
		}
	}
	return vars
}

// localeGet is Salt's `get_locale`: the system LANG, or "" when none is
// set.
func localeGet(c *exec.Context) (string, error) {
	if c.Which("localectl") == "" {
		return "", fmt.Errorf("reading the system locale needs localectl, which is not on this node's PATH")
	}
	res, err := c.Run(exec.Command{Argv: []string{"localectl", "status"}, IgnoreExitCode: true})
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", fmt.Errorf("localectl status exited %d: %s", res.Code, firstLine(res.Stderr+res.Stdout))
	}
	return parseLocalectlStatus(res.Stdout)["LANG"], nil
}

// localeSet is Salt's `set_locale`: true without acting when the system
// LANG is already the locale in any spelling, an error for a locale the
// node cannot load, and otherwise localectl followed by reading it back.
func localeSet(c *exec.Context, want string) (bool, error) {
	have, err := localeGet(c)
	if err != nil {
		return false, err
	}
	if sameLocale(have, want) {
		return true, nil
	}
	ok, err := localeAvail(c, want)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, fmt.Errorf("this node has no locale %s; locale.present or locale.gen_locale makes one available", want)
	}
	if c.Test {
		return true, nil
	}
	res, err := c.Run(exec.Command{Argv: []string{"localectl", "set-locale", "LANG=" + want}, IgnoreExitCode: true})
	if err != nil {
		return false, err
	}
	after, err := localeGet(c)
	if err != nil {
		return false, err
	}
	if !sameLocale(after, want) {
		return false, fmt.Errorf("localectl set-locale LANG=%s exited %d and the system locale reads %q: %s",
			want, res.Code, after, firstLine(res.Stderr+res.Stdout))
	}
	return true, nil
}

// supportedEntry finds a locale's line in Debian's SUPPORTED list:
// `de_DE.UTF-8 UTF-8`. Matched canonically on the name, and on the
// charmap too when the caller gave one.
func supportedEntry(name, charmap string) (string, bool, error) {
	b, err := os.ReadFile(localeSupported)
	if err != nil {
		return "", false, err
	}
	for _, line := range nonEmptyLines(string(b)) {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if !sameLocale(fields[0], name) {
			continue
		}
		if charmap != "" && canonicalLocale("x."+fields[1]) != canonicalLocale("x."+charmap) {
			continue
		}
		return fields[0] + " " + fields[1], true, nil
	}
	return "", false, nil
}

// enableLocaleGen returns /etc/locale.gen with the entry enabled, and
// whether that changed anything. A line already enabling the locale, in
// any spelling, is left as it is; a commented line naming exactly the
// entry is uncommented in place, so the file keeps its order; anything
// else is appended.
func enableLocaleGen(content, entry string) (string, bool) {
	want := strings.Fields(entry)
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 1 && !strings.HasPrefix(fields[0], "#") && sameLocale(fields[0], want[0]) {
			return content, false
		}
	}
	for i, line := range lines {
		text := strings.TrimSpace(line)
		if !strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(text, "#")))
		if len(fields) == 2 && fields[0] == want[0] && fields[1] == want[1] {
			lines[i] = entry
			return strings.Join(lines, "\n"), true
		}
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + entry + "\n", true
}

// localeScheme names how this node makes a locale available.
func localeScheme(c *exec.Context) string {
	if _, err := os.Stat(localeGenPath); err == nil && c.Which("locale-gen") != "" {
		return "locale-gen"
	}
	// rpm alone is not enough: Debian and Ubuntu package an rpm binary, and
	// there the langpack would be handed to apt under a name it has never
	// heard of.
	if c.Which("rpm") != "" && (c.Which("dnf") != "" || c.Which("yum") != "") {
		return "langpack"
	}
	return ""
}

// localeGen is Salt's `gen_locale`, in each family's own mechanism.
func localeGen(c *exec.Context, name, charmap string) (bool, error) {
	switch localeScheme(c) {
	case "locale-gen":
		return localeGenDebian(c, name, charmap)
	case "langpack":
		return localeGenLangpack(c, name)
	}
	return false, fmt.Errorf("this node has neither /etc/locale.gen with locale-gen nor rpm's glibc langpacks, " +
		"the two ways this build makes a locale available")
}

func localeGenDebian(c *exec.Context, name, charmap string) (bool, error) {
	entry, found, err := supportedEntry(name, charmap)
	if err != nil {
		return false, err
	}
	if !found {
		return false, fmt.Errorf("%s names no locale %s", localeSupported, name)
	}
	b, err := os.ReadFile(localeGenPath)
	if err != nil {
		return false, err
	}
	updated, changed := enableLocaleGen(string(b), entry)
	avail, err := localeAvail(c, name)
	if err != nil {
		return false, err
	}
	if !changed && avail {
		return true, nil
	}
	if c.Test {
		return true, nil
	}
	if changed {
		mode := os.FileMode(0o644)
		if fi, err := os.Stat(localeGenPath); err == nil {
			mode = fi.Mode().Perm()
		}
		if err := writeAtomic(localeGenPath, []byte(updated), mode); err != nil {
			return false, err
		}
	}
	res, err := c.Run(exec.Command{Argv: []string{"locale-gen", "--keep-existing"}, IgnoreExitCode: true})
	if err != nil {
		return false, err
	}
	if ok, err := localeAvail(c, name); err != nil || !ok {
		return false, fmt.Errorf("locale-gen exited %d and %s is not loadable: %s",
			res.Code, name, firstLine(res.Stderr+res.Stdout))
	}
	return true, nil
}

func localeGenLangpack(c *exec.Context, name string) (bool, error) {
	if ok, err := localeAvail(c, name); err != nil || ok {
		return ok, err
	}
	pkg := "glibc-langpack-" + localeLanguage(name)
	res, err := c.Run(exec.Command{Argv: []string{"rpm", "-q", pkg}, IgnoreExitCode: true})
	if err != nil {
		return false, err
	}
	if res.Code == 0 {
		return false, fmt.Errorf("%s is installed and has no locale %s", pkg, name)
	}
	if c.Test {
		return true, nil
	}
	p, err := pickPkgProvider(c)
	if err != nil {
		return false, err
	}
	if err := p.Install(c, []string{pkg}, nil, false); err != nil {
		return false, fmt.Errorf("installing %s: %w", pkg, err)
	}
	ok, err := localeAvail(c, name)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, fmt.Errorf("%s was installed and has no locale %s", pkg, name)
	}
	return true, nil
}

func localeExecModules() []exec.Module {
	loc := req("locale", signature.String, "The locale, such as `en_US.UTF-8`. Any spelling of the codeset glibc accepts.")
	read := func(function, doc string, params []signature.Param, fn exec.Func) exec.Module {
		return exec.Module{
			Sig: signature.Signature{
				Module: "locale", Function: function, Doc: doc, Params: params,
				TestMode: signature.TestNotApplicable, Platforms: linuxOnly, Section: "15.2",
			},
			Fn: fn,
		}
	}
	mutate := func(function, doc string, params []signature.Param, fn exec.Func) exec.Module {
		return exec.Module{
			Sig: signature.Signature{
				Module: "locale", Function: function, Doc: doc, Params: params,
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"}, Platforms: linuxOnly, Section: "15.2",
			},
			Fn: fn,
		}
	}
	return []exec.Module{
		read("list_avail", "Return the locales this node can load, as `locale -a` spells them (`en_US.utf8`).", nil,
			func(c *exec.Context, args *value.Map) (any, error) {
				avail, err := localeListAvail(c)
				if err != nil {
					return nil, err
				}
				out := make([]any, len(avail))
				for i, a := range avail {
					out[i] = a
				}
				return out, nil
			}),
		read("get_locale", "Return the system LANG from `localectl status`, spelled as it was set; empty when none is.", nil,
			func(c *exec.Context, args *value.Map) (any, error) { return localeGet(c) }),
		read("avail", "Report whether this node can load a locale, comparing codesets the way glibc does, "+
			"so `en_US.UTF-8` is found as `locale -a`'s `en_US.utf8`.",
			[]signature.Param{loc},
			func(c *exec.Context, args *value.Map) (any, error) {
				name, err := localeName(args, "locale")
				if err != nil {
					return nil, err
				}
				return localeAvail(c, name)
			}),
		mutate("set_locale", "Set the system LANG with `localectl set-locale`, leaving every LC_* variable as it "+
			"is. True without acting when LANG is already the locale in any spelling; refused for a locale the "+
			"node cannot load.",
			[]signature.Param{loc},
			func(c *exec.Context, args *value.Map) (any, error) {
				name, err := localeName(args, "locale")
				if err != nil {
					return nil, err
				}
				return localeSet(c, name)
			}),
		mutate("gen_locale", "Make a locale loadable. On Debian, enable its /usr/share/i18n/SUPPORTED line in "+
			"/etc/locale.gen and run `locale-gen --keep-existing`; on EL, install `glibc-langpack-<language>`, "+
			"since EL ships no locale sources to compile. Returns whether the locale is loadable afterwards.",
			[]signature.Param{loc, opt("charmap", signature.String, "", "Debian only: the charmap, when SUPPORTED lists the locale under more than one.")},
			func(c *exec.Context, args *value.Map) (any, error) {
				name, err := localeName(args, "locale")
				if err != nil {
					return nil, err
				}
				return localeGen(c, name, states.Str(args, "charmap", ""))
			}),
	}
}

func localeStateModules() []states.Module {
	return []states.Module{
		{
			Sig: signature.Signature{
				Module: "locale", Function: "system",
				Doc: "Ensure the system LANG is the locale named, in any spelling of its codeset.",
				Params: []signature.Param{
					nameParam("The locale, such as `en_US.UTF-8`. Defaults to the state ID."),
				},
				Mutates: true, TestMode: signature.TestReliable, Privileges: []string{"root"},
				Platforms: linuxOnly, Section: "15.5",
			},
			Fn: localeSystemState,
		},
		{
			Sig: signature.Signature{
				Module: "locale", Function: "present",
				Doc: "Ensure a locale is loadable, generating it on Debian or installing its langpack on EL.",
				Params: []signature.Param{
					nameParam("The locale, such as `de_DE.UTF-8`. Defaults to the state ID."),
				},
				Mutates: true, TestMode: signature.TestReliable, Privileges: []string{"root"},
				Platforms: linuxOnly, Section: "15.5",
			},
			Fn: localePresentState,
		},
	}
}

func localeSystemState(c *exec.Context, args *value.Map) (states.Result, error) {
	want, err := localeName(args, "name")
	if err != nil {
		return states.False(err.Error()), nil
	}
	have, err := localeGet(c)
	if err != nil {
		return states.False(fmt.Sprintf("The system locale could not be read: %v", err)), nil
	}
	if sameLocale(have, want) {
		return states.True(fmt.Sprintf("The system locale is already %s.", have)), nil
	}
	ok, err := localeAvail(c, want)
	if err != nil {
		return states.False(fmt.Sprintf("The available locales could not be listed: %v", err)), nil
	}
	if !ok {
		return states.False(fmt.Sprintf(
			"This node has no locale %s, so it cannot be the system locale; locale.present makes one available.", want)), nil
	}
	changes := value.MapOf("locale", states.Change(have, want))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The system locale would be set to %s, from %q.", want, have), changes), nil
	}
	if _, err := localeSet(c, want); err != nil {
		return states.False(fmt.Sprintf("The system locale could not be set to %s: %v", want, err)), nil
	}
	return states.Changed(fmt.Sprintf("The system locale was set to %s, from %q.", want, have), changes), nil
}

func localePresentState(c *exec.Context, args *value.Map) (states.Result, error) {
	want, err := localeName(args, "name")
	if err != nil {
		return states.False(err.Error()), nil
	}
	ok, err := localeAvail(c, want)
	if err != nil {
		return states.False(fmt.Sprintf("The available locales could not be listed: %v", err)), nil
	}
	if ok {
		return states.True(fmt.Sprintf("The locale %s is already present.", want)), nil
	}
	changes := value.MapOf("locale", states.Change("(absent)", want))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The locale %s would be generated.", want), changes), nil
	}
	if _, err := localeGen(c, want, ""); err != nil {
		return states.False(fmt.Sprintf("The locale %s could not be generated: %v", want, err)), nil
	}
	return states.Changed(fmt.Sprintf("The locale %s was generated.", want), changes), nil
}
