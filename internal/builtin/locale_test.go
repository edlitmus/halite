package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// Fixtures are real output captured on 2026-09-30 from Rocky Linux 9.8
// (systemd 252), AlmaLinux 8.10 (systemd 239) and Debian 13 (systemd 257).

// `localectl status` with LANG, LC_TIME and LC_MESSAGES set (Rocky 9; Debian
// 13 is the same block with an `X11 Model` line after it).
const localectlRocky9Sample = "System Locale: LANG=en_GB.UTF-8\n" +
	"               LC_TIME=en_US.UTF-8\n" +
	"               LC_MESSAGES=C.UTF-8\n" +
	"    VC Keymap: us\n" +
	"   X11 Layout: us\n"

// The same on Alma 8, indented three columns further.
const localectlAlma8Sample = "   System Locale: LANG=en_GB.UTF-8\n" +
	"                  LC_TIME=en_US.UTF-8\n" +
	"                  LC_MESSAGES=C.UTF-8\n" +
	"       VC Keymap: us\n" +
	"      X11 Layout: us\n"

// The image default on Debian 13.
const localectlDebianSample = "System Locale: LANG=en_US.UTF-8\n" +
	"    VC Keymap: (unset)\n" +
	"   X11 Layout: us\n" +
	"    X11 Model: pc105\n"

// Lines of Debian 13's /etc/locale.gen and /usr/share/i18n/SUPPORTED.
const localeGenDebianSample = "# This file lists locales that you wish to have built.\n" +
	"# de_DE ISO-8859-1\n" +
	"# de_DE.UTF-8 UTF-8\n" +
	"# de_DE@euro ISO-8859-15\n" +
	"en_US.UTF-8 UTF-8\n"

const supportedDebianSample = "de_DE.UTF-8 UTF-8\n" +
	"de_DE ISO-8859-1\n" +
	"de_DE@euro ISO-8859-15\n" +
	"en_US.UTF-8 UTF-8\n" +
	"en_US ISO-8859-1\n" +
	"en_US.ISO-8859-15 ISO-8859-15\n"

func TestCanonicalLocaleIsGlibcsCodesetNormalisation(t *testing.T) {
	for in, want := range map[string]string{
		"en_US.UTF-8":            "en_US.utf8", // what you write
		"en_US.utf8":             "en_US.utf8", // what `locale -a` prints on all three hosts
		"en_GB.ISO-8859-15":      "en_GB.iso885915",
		"en_GB.iso885915":        "en_GB.iso885915",
		"de_DE.ISO-8859-15@euro": "de_DE.iso885915@euro",
		"de_DE@euro":             "de_DE@euro",
		"en_US":                  "en_US", // a different locale, not a spelling
		"C.UTF-8":                "C.utf8",
		"x.8859":                 "x.iso8859",
	} {
		if got := canonicalLocale(in); got != want {
			t.Errorf("canonicalLocale(%q) = %q, want %q", in, got, want)
		}
	}
	if sameLocale("en_US", "en_US.UTF-8") || sameLocale("en_us.UTF-8", "en_US.UTF-8") || sameLocale("", "") {
		t.Error("a codeset-less name, a different territory case, or nothing matched")
	}
}

func TestLocalectlStatusIsReadOnEitherIndentation(t *testing.T) {
	for name, out := range map[string]string{"rocky9": localectlRocky9Sample, "alma8": localectlAlma8Sample} {
		got := parseLocalectlStatus(out)
		if len(got) != 3 || got["LANG"] != "en_GB.UTF-8" || got["LC_TIME"] != "en_US.UTF-8" || got["LC_MESSAGES"] != "C.UTF-8" {
			t.Errorf("%s: %v", name, got)
		}
	}
	if got := parseLocalectlStatus(localectlDebianSample); len(got) != 1 || got["LANG"] != "en_US.UTF-8" {
		t.Errorf("debian: %v", got)
	}
	if got := parseLocalectlStatus("System Locale: n/a\n    VC Keymap: us\n"); len(got) != 0 {
		t.Errorf("an unset locale read as %v", got)
	}
}

func TestEnableLocaleGenUncommentsInPlaceOnce(t *testing.T) {
	got, changed := enableLocaleGen(localeGenDebianSample, "de_DE.UTF-8 UTF-8")
	if !changed || !strings.Contains(got, "\nde_DE.UTF-8 UTF-8\n# de_DE@euro") {
		t.Fatalf("not uncommented in place:\n%s", got)
	}
	if strings.Count(got, "de_DE.UTF-8 UTF-8") != 1 {
		t.Errorf("named twice:\n%s", got)
	}
	again, changed := enableLocaleGen(got, "de_DE.UTF-8 UTF-8")
	if changed || again != got {
		t.Error("a second enable changed the file")
	}
	// Already enabled in the other spelling is enabled.
	if _, changed := enableLocaleGen("en_US.utf8 UTF-8\n", "en_US.UTF-8 UTF-8"); changed {
		t.Error("en_US.utf8 was not recognised as en_US.UTF-8")
	}
	// Absent altogether: appended.
	if got, changed := enableLocaleGen("en_US.UTF-8 UTF-8", "fr_FR.UTF-8 UTF-8"); !changed || got != "en_US.UTF-8 UTF-8\nfr_FR.UTF-8 UTF-8\n" {
		t.Errorf("append = %q", got)
	}
}

func TestSupportedEntryMatchesEitherSpelling(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SUPPORTED")
	if err := os.WriteFile(path, []byte(supportedDebianSample), 0o644); err != nil {
		t.Fatal(err)
	}
	old := localeSupported
	localeSupported = path
	t.Cleanup(func() { localeSupported = old })

	for in, want := range map[string]string{
		"de_DE.utf8":  "de_DE.UTF-8 UTF-8",
		"de_DE.UTF-8": "de_DE.UTF-8 UTF-8",
		"de_DE":       "de_DE ISO-8859-1",
		"de_DE@euro":  "de_DE@euro ISO-8859-15",
	} {
		got, ok, err := supportedEntry(in, "")
		if err != nil || !ok || got != want {
			t.Errorf("supportedEntry(%q) = %q, %v, %v; want %q", in, got, ok, err, want)
		}
	}
	if _, ok, _ := supportedEntry("xx_YY.UTF-8", ""); ok {
		t.Error("xx_YY.UTF-8 was found")
	}
}

func TestLocaleNamesThatAreNotOneWordAreRefused(t *testing.T) {
	for _, bad := range []string{"-a", "en US", "LANG=x", "x;y", "a/b", ""} {
		if _, err := localeName(value.MapOf("locale", bad), "locale"); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestLocaleSystemTestModeRunsNoSetter(t *testing.T) {
	runner := &exec.RecordingRunner{Responses: map[string]exec.Result{
		"localectl status": {Stdout: localectlDebianSample},
		"locale -a":        {Stdout: "C\nC.utf8\nen_GB.utf8\nen_US.utf8\nPOSIX\n"},
	}}
	c := newCtx(true)
	c.Runner = runner
	c.Lookup = func(name string) string { return "/usr/bin/" + name }
	res, err := localeSystemState(c, value.MapOf("name", "en_GB.UTF-8"))
	if err != nil || res.Changes == nil || res.Changes.Len() == 0 {
		t.Fatalf("test mode = %+v, %v", res, err)
	}
	for _, cmd := range runner.RanCommands() {
		if strings.Contains(cmd, "set-locale") {
			t.Errorf("test mode ran %q", cmd)
		}
	}
	// And the same locale in the other spelling is no change at all.
	res, _ = localeSystemState(c, value.MapOf("name", "en_US.utf8"))
	if res.Changes != nil && res.Changes.Len() != 0 {
		t.Errorf("en_US.utf8 over en_US.UTF-8 reported %v", res.Changes.Entries())
	}
}
