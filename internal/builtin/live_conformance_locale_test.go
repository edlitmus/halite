package builtin

import (
	"os"
	"strings"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// `locale.system` and `locale.present`, through SPEC 11.6's harness.
//
// locale.system changes the system LANG, which no case can confine to
// something of its own, so it records /etc/locale.conf the first time
// Setup runs and writes those bytes back in Cleanup. Setup puts the node
// on en_US.UTF-8 and the state asks for en_GB.UTF-8, which every lab
// image measured carries (EL's glibc-langpack-en, Debian's locales-all).
//
// locale.present needs a locale the node does not have. On EL that is
// de_DE.UTF-8 without glibc-langpack-de, and Setup removes the langpack
// so each phase starts without it -- which is only safe because the case
// declines to run at all on a node that had the langpack before it
// started. On Debian with locales-all every SUPPORTED locale is already
// loadable, so there is nothing for the state to do and the case says so
// rather than passing vacuously.

func localeConformanceCases() []liveCase {
	root := liveRoot()
	var savedConf []byte
	saveConf := func() {
		if savedConf == nil {
			savedConf, _ = os.ReadFile("/etc/locale.conf")
		}
	}
	readConf := func() (string, error) {
		b, err := os.ReadFile("/etc/locale.conf")
		return strings.TrimSpace(string(b)), err
	}
	rpmHas := func(pkg string) bool {
		res, err := root.Run(hexec.Command{Argv: []string{"rpm", "-q", pkg}, IgnoreExitCode: true})
		return err == nil && res.Code == 0
	}
	const langpack = "glibc-langpack-de"
	var langpackAtStart *bool

	return []liveCase{
		{
			platforms: []string{"linux"},
			needs:     []string{"localectl", "locale"},
			unavailable: func(c *hexec.Context) string {
				if _, err := localeGet(c); err != nil {
					return "localectl cannot reach systemd-localed here: " + err.Error()
				}
				if ok, _ := localeAvail(c, "en_GB.UTF-8"); !ok {
					return "this node cannot load en_GB.UTF-8, the locale the case sets"
				}
				return ""
			},
			Conformance: states.Conformance{
				Name: "locale.system",
				Args: value.MapOf("name", "en_GB.UTF-8"),
				Setup: func() error {
					saveConf()
					_, err := root.Run(hexec.Command{Argv: []string{"localectl", "set-locale", "LANG=en_US.UTF-8"}})
					return err
				},
				Probe: readConf,
				Cleanup: func() {
					if savedConf != nil {
						_ = restoreLocaleConf(root, savedConf)
					}
				},
			},
		},
		{
			platforms: []string{"linux"},
			needs:     []string{"locale", "rpm"},
			unavailable: func(c *hexec.Context) string {
				if localeScheme(c) != "langpack" {
					return "not an rpm-langpack node; on Debian with locales-all every SUPPORTED locale is already loadable, so locale.present has nothing to generate"
				}
				if langpackAtStart == nil {
					had := rpmHas(langpack)
					langpackAtStart = &had
				}
				if *langpackAtStart {
					return langpack + " was installed before the case ran, and Setup would have to remove it"
				}
				return ""
			},
			Conformance: states.Conformance{
				Name: "locale.present",
				Args: value.MapOf("name", "de_DE.UTF-8"),
				Setup: func() error {
					if rpmHas(langpack) {
						_, err := root.Run(hexec.Command{Argv: []string{"rpm", "-e", langpack}})
						return err
					}
					return nil
				},
				Probe: func() (string, error) {
					ok, err := localeAvail(root, "de_DE.UTF-8")
					if ok {
						return "de_DE.UTF-8 loadable", err
					}
					return "de_DE.UTF-8 absent", err
				},
				Cleanup: func() {
					if rpmHas(langpack) {
						_, _ = root.Run(hexec.Command{Argv: []string{"rpm", "-e", langpack}})
					}
				},
			},
		},
	}
}

// restoreLocaleConf puts /etc/locale.conf back to the bytes it had and
// makes systemd-localed read them. Writing the file alone is not enough
// on Alma 8's systemd 239: with the bytes back, `localectl status` went on
// reporting what had been set -- LC_TIME included -- until localed exited
// idle and started again (measured; Rocky 9's 252 and Debian 13's 257
// reported the restored file at once). Handing localed the original
// assignments first does not do it either, because set-locale keeps every
// variable it is not given. So the bytes go back, comments and all, and
// localed is restarted, which is harmless: it holds no state but the file.
func restoreLocaleConf(c *hexec.Context, saved []byte) error {
	if err := os.WriteFile("/etc/locale.conf", saved, 0o644); err != nil {
		return err
	}
	_, err := c.Run(hexec.Command{Argv: []string{"systemctl", "try-restart", "systemd-localed"}})
	return err
}
