package builtin

import (
	"fmt"
	"os"
	"strings"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The rest of the package-management states: the two `pkg` functions that
// `packageCases` left, both `pkgrepo` functions, `debconf.set` and the two
// `snap` functions.
//
// # pkg.latest does not need a package with two versions in it
//
// That is what the excuse in `unconformed` assumed, and it is why this one sat
// there longest. Reading `pkgLatest` rather than reasoning about the name says
// otherwise: it asks the provider for the newest available version and
// compares it against the installed one, and for a package that is *not*
// installed the installed version is the empty string. So a state naming an
// absent package is outdated by that comparison and the state installs it.
//
// The case therefore stages exactly what `pkg.installed`'s does -- remove the
// package, apply -- and what it checks that `pkg.installed`'s cannot is the
// thing that would break an estate: that the newest version the provider
// *offers* is spelled the same way as the version dpkg or pkg(8) reports once
// it is installed. If those two strings differ in format at all,
// `latest == currentVersion` is false for ever and every highstate on every
// node reports an upgrade it already did. Nothing in the unit suite can see
// that, because both strings come from fixtures written together.
//
// Writing this case found a defect the harness cannot see: the comment said
// the package "would be upgraded" for one it was about to install. It was
// recorded here rather than fixed, because changing a message changes what
// operators grep for -- and then fixed in its own commit, since this estate's
// only reader of that text said so. `pkgLatestSentence` and
// `pkg_latest_test.go` are where it lives now.
//
// # pkgrepo writes where software comes from, so the repository is a real one
//
// A repository declared with a URL nobody serves would exercise the write and
// the read and stop there. This case names the machine's own archive --
// discovered from /etc/os-release rather than written down, so it is right on
// Ubuntu and on Debian -- with `type: deb-src`, which is the one spelling a
// runner does not already have configured. `deb-src` matters for a reason
// beyond tidiness: a duplicate source makes `apt-get update` warn, and a
// warning in a leg's log is a thing people learn to scroll past.
//
// What this proves and what it does not: `refresh` is left at its default, so
// `refreshAfterRepoChange` runs. A refresh that *fails* is carried as a
// warning rather than a failure by design -- the file is written, and saying
// the state failed would report the wrong thing -- so this case establishes
// that the refresh was attempted and converges around it. That apt can
// actually read what the module writes is established elsewhere, by
// `TestLiveRepoIsWrittenAndAptReadsIt` against the debian container's own
// signed offline repository, and that is the assertion to go and read rather
// than assume this one covers.
//
// # debconf.set answers questions for a package that does not exist
//
// Which sounds like cheating and is the only honest way to do it. debconf's
// database is keyed by question, and setting an answer for a package that is
// installed would change how that package configures itself at its next
// `dpkg-reconfigure` -- on a machine somebody might be using. A fictional
// package owns a namespace nothing reads.
//
// It is not a guess that debconf tolerates this:
// `TestLiveDebconfShowOnAnUnknownPackageIsEmptyRatherThanAnError` measured it
// on real Debian, and `TestLiveDebconfSetsAnAnswerAndDebconfAgrees` has been
// pre-seeding `halite-fleet-probe` for months.
//
// DIVERGENCE 5.157.

// ---- pkg.latest and pkg.purged ----
//
// The same package `packageCases` uses, for its reasons: small, no service,
// and present in every family's repositories.
func pkgRestCases() []liveCase {
	r := New()
	root := liveRoot()
	const pkg = conformancePkgName

	installed := func() (string, error) {
		out, err := r.Exec.Call(root, "pkg.version", value.MapOf("name", pkg))
		if err != nil {
			return "absent", nil
		}
		v := strings.TrimSpace(fmt.Sprintf("%v", out))
		if v == "" || v == "<nil>" {
			return "absent", nil
		}
		return "installed " + v, nil
	}
	remove := func() {
		_, _ = r.States.Call(root, "pkg.purged", value.MapOf("name", pkg))
	}
	install := func() error {
		return applyForSetup(r, root, "pkg.installed", value.MapOf("name", pkg))
	}

	// Linux and FreeBSD, for the reasons packageCases records: macOS is not
	// yet run here (DIVERGENCE 5.188) and choco has no `tree`.
	onPkgPlatforms := func(lc liveCase) liveCase {
		lc.platforms = []string{"linux", "freebsd"}
		return lc
	}

	return []liveCase{
		onPkgPlatforms(liveCase{Conformance: states.Conformance{
			Name:    "pkg.latest",
			Args:    value.MapOf("name", pkg),
			Probe:   installed,
			Setup:   func() error { remove(); return nil },
			Cleanup: remove,
		}}),
		onPkgPlatforms(liveCase{Conformance: states.Conformance{
			// Purge rather than remove, so the setup installs and the
			// state has both the package and its configuration to take
			// away. The probe cannot see the difference -- `pkg.version`
			// reports a purged and a removed package identically -- and
			// that limit is stated rather than papered over: what this
			// case establishes is that `purged` converges and predicts,
			// not that the configuration files went with it.
			Name:    "pkg.purged",
			Args:    value.MapOf("name", pkg),
			Probe:   installed,
			Setup:   install,
			Cleanup: remove,
		}}),
	}
}

// conformancePkgName is the package both pkg case sets use. Named here so
// the two files cannot drift on to different packages, which would make a
// case's cleanup leave the other's subject behind.
const conformancePkgName = "tree"

// ---- pkgrepo ----
func pkgrepoCases() []liveCase {
	r := New()
	root := liveRoot()
	name := liveConformancePrefix + "-src"
	args := value.NewMap(6)

	// The machine's own archive and suite, from the file every Linux
	// distribution ships. A URL written down here would be right on one
	// distribution and wrong on the next, and `pkgrepo`'s apt provider is
	// used by both families.
	fill := func() error {
		if _, done := args.Get("baseurl"); done {
			return nil
		}
		release, err := os.ReadFile("/etc/os-release")
		if err != nil {
			return fmt.Errorf("/etc/os-release could not be read, so this case cannot name "+
				"an archive this machine already trusts: %w", err)
		}
		fields := map[string]string{}
		for _, line := range strings.Split(string(release), "\n") {
			key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}
			fields[key] = strings.Trim(val, `"`)
		}
		codename := fields["VERSION_CODENAME"]
		if codename == "" {
			return fmt.Errorf("this machine's /etc/os-release names no VERSION_CODENAME, " +
				"so there is no suite to declare")
		}
		var baseurl string
		switch fields["ID"] {
		case "ubuntu":
			baseurl = "http://archive.ubuntu.com/ubuntu"
		case "debian":
			baseurl = "http://deb.debian.org/debian"
		default:
			return fmt.Errorf("this case knows the archive URL for ubuntu and debian, "+
				"and this machine's ID is %q", fields["ID"])
		}
		args.Set("name", name)
		args.Set("baseurl", baseurl)
		args.Set("dist", codename)
		args.Set("comps", []any{"main"})
		// Source packages, because a runner has the binary suites
		// configured already and a duplicate source is a warning on every
		// apt-get update.
		args.Set("type", "deb-src")
		return nil
	}

	// The file the provider writes, read as apt would read it rather than
	// through the module -- the module's own `get_repo` parses what it
	// wrote, which is a round trip and not the effect.
	probe := func() (string, error) {
		body, err := os.ReadFile("/etc/apt/sources.list.d/" + name + ".list")
		if os.IsNotExist(err) {
			return "no source file", nil
		}
		if err != nil {
			return "", err
		}
		var kept []string
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				kept = append(kept, line)
			}
		}
		if len(kept) == 0 {
			return "a source file with no source line in it", nil
		}
		return strings.Join(kept, " | "), nil
	}
	drop := func() {
		_, _ = r.Exec.Call(root, "pkgrepo.del_repo", value.MapOf("name", name))
	}

	onApt := func(lc liveCase) liveCase {
		lc.platforms = []string{"linux"}
		lc.needs = []string{"apt-get"}
		return lc
	}

	return []liveCase{
		onApt(liveCase{Conformance: states.Conformance{
			Name:  "pkgrepo.managed",
			Args:  args,
			Probe: probe,
			Setup: func() error {
				if err := fill(); err != nil {
					return err
				}
				drop()
				return nil
			},
			Cleanup: drop,
		}}),
		onApt(liveCase{Conformance: states.Conformance{
			// Added through `pkgrepo.managed`, the module's own way in,
			// rather than by writing the file here. A second
			// implementation of "configure a repository" could pass while
			// the module's was broken.
			Name: "pkgrepo.absent",
			// Only the name: `absent` takes no declaration, and giving it
			// one would suggest it compared against it.
			Args:  value.MapOf("name", name),
			Probe: probe,
			Setup: func() error {
				if err := fill(); err != nil {
					return err
				}
				drop()
				return applyForSetup(r, root, "pkgrepo.managed", args)
			},
			Cleanup: drop,
		}}),
	}
}

// ---- debconf ----
func debconfCases() []liveCase {
	r := New()
	root := liveRoot()
	pkg := liveConformancePrefix + "-probe"
	question := liveConformancePrefix + "/answer"

	probe := func() (string, error) {
		out, err := r.Exec.Call(root, "debconf.show", value.MapOf("package", pkg))
		if err != nil {
			return "", err
		}
		answers, ok := out.(*value.Map)
		if !ok {
			return "", fmt.Errorf("debconf.show returned %T", out)
		}
		raw, there := answers.Get(question)
		if !there {
			return "debconf holds no answer for " + question, nil
		}
		m, _ := raw.(*value.Map)
		if m == nil {
			return "", fmt.Errorf("the answer is %T", raw)
		}
		val, _ := m.Get("value")
		// `seen` as well as the value, because the module treats an
		// answer that matches and has never been seen as not converged:
		// debconf will still ask. A probe reporting only the value would
		// read as unchanged across the very transition the state makes.
		seen, _ := m.Get("seen")
		return fmt.Sprintf("%v seen=%v", val, seen), nil
	}

	// debconf has no "unset". `UNREGISTER` through debconf's own protocol
	// drops the owner's claim on the question, and the question goes with
	// the last owner -- which is the closest thing to putting the machine
	// back, and is what makes the case runnable twice on one host. Whether
	// it worked is not asserted here and does not need to be: a second run
	// of this suite on the same machine would find the answer already set
	// and the harness would say so, in as many words, rather than passing.
	forget := func() {
		_, _ = root.Run(hexec.Command{
			Argv:           []string{"debconf-communicate", pkg},
			Stdin:          "UNREGISTER " + question + "\n",
			IgnoreExitCode: true,
		})
	}

	return []liveCase{{
		Conformance: states.Conformance{
			Name: "debconf.set",
			Args: value.MapOf("name", pkg,
				"data", value.MapOf(question, true),
				"type", "boolean"),
			Probe:   probe,
			Setup:   func() error { forget(); return nil },
			Cleanup: forget,
		},
		platforms: []string{"linux"},
		needs:     []string{"debconf-show", "debconf-set-selections", "debconf-communicate"},
	}}
}

// ---- snap ----
//
// `hello-world` is the snap, and it is the same one the linux leg installs for
// `TestLiveSnap` to read. Sharing it is deliberate rather than lazy: it is the
// one snap this project knows to be installable on that runner, and inventing
// a second would be inventing a second thing that can stop existing in the
// store.
//
// Both cases therefore leave it *installed*, whichever way round their own
// staging ran, so the machine ends as the leg set it up. The coupling is worth
// naming: if a case here fails half way and leaves it removed, the leg's
// "the snap tests ran rather than skipping" step fails with a message pointing
// at the install step, which would be the wrong place to look. The conformance
// failure is directly above it in the same log.
func snapCases() []liveCase {
	r := New()
	root := liveRoot()
	const snap = "hello-world"

	// Presence only, and not the revision. snapd refreshes snaps on its
	// own -- the state's own refusal to take a version says so at length --
	// so a probe carrying a revision could differ between two readings
	// seconds apart for reasons that are not the module's fault, and the
	// harness would read that as test mode changing the system.
	probe := func() (string, error) {
		out, err := r.Exec.Call(root, "snap.installed", value.MapOf("name", snap))
		if err != nil {
			return "", err
		}
		if out == nil {
			return "absent", nil
		}
		return "installed", nil
	}
	remove := func() error {
		_, err := r.Exec.Call(root, "snap.remove", value.MapOf("name", snap))
		return err
	}
	install := func() error {
		_, err := r.Exec.Call(root, "snap.install", value.MapOf("name", snap))
		return err
	}

	onSnapd := func(lc liveCase) liveCase {
		lc.platforms = []string{"linux"}
		lc.needs = []string{"snap"}
		return lc
	}

	return []liveCase{
		onSnapd(liveCase{Conformance: states.Conformance{
			Name:    "snap.installed",
			Args:    value.MapOf("name", snap),
			Probe:   probe,
			Setup:   remove,
			Cleanup: func() { _ = install() },
		}}),
		onSnapd(liveCase{Conformance: states.Conformance{
			Name:    "snap.removed",
			Args:    value.MapOf("name", snap),
			Probe:   probe,
			Setup:   install,
			Cleanup: func() { _ = install() },
		}}),
	}
}

// conformancePkgSysCases is the four families above, in one list.
func conformancePkgSysCases() []liveCase {
	var cases []liveCase
	cases = append(cases, pkgRestCases()...)
	cases = append(cases, pkgrepoCases()...)
	cases = append(cases, debconfCases()...)
	cases = append(cases, snapCases()...)
	return cases
}
