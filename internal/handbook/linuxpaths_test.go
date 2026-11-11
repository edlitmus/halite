package handbook

import (
	"testing"

	"github.com/edlitmus/halite/internal/config"
)

// The CLI chapter is made by running each program's `help`, which prints
// the defaults of the platform it was built for. These are help lines as
// each platform prints them, built from the same config functions the
// binaries use, and every one must come out as the Linux line the
// committed chapter carries (issue 678).
//
// The platforms are named rather than taken from runtime.GOOS so that the
// Windows and FreeBSD cases are checked on every host, not only on the
// one machine of that kind that happens to run `handbook gen`.
func TestLinuxPathsMakesEveryPlatformsHelpTheLinuxOne(t *testing.T) {
	t.Setenv("PROGRAMDATA", `C:\ProgramData`)

	sep := map[string]string{"windows": `\`}
	line := func(goos string) string {
		s := sep[goos]
		if s == "" {
			s = "/"
		}
		root := config.RootFor(goos)
		// The <root>/node.yaml line is literal text in every binary's help,
		// not a path, and must come through untouched.
		return "  --pki-dir <dir>      key material, default " + root + s + "pki\n" +
			"  --config <path>      configuration file, default <root>/node.yaml\n" +
			"  --root <dir>         configuration root, default " + root + "\n" +
			"  state in " + config.VarPathFor(goos, "lib") + ", sockets in " + config.RunPathFor(goos) + "\n"
	}
	want := line("linux")
	for _, goos := range []string{"linux", "darwin", "freebsd", "openbsd", "windows"} {
		if got := LinuxPaths(line(goos), goos); got != want {
			t.Errorf("%s help, made Linux:\n%s\nwant:\n%s", goos, got, want)
		}
	}

	// The Windows and FreeBSD lines must actually differ from Linux's, or
	// the loop above checks nothing for them.
	for _, goos := range []string{"freebsd", "windows"} {
		if line(goos) == want {
			t.Errorf("the %s help line is already the Linux one, so the test proves nothing for it", goos)
		}
	}
}
