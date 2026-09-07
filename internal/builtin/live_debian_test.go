package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The Debian row's modules, driven against the real tools.
//
// # What these are for
//
// `dpkg`, `debconf`, `pkgrepo` and `timezone` change a machine as root
// and reach their subsystems by running another program and reading what
// it says back. Their unit tests supply that output, so what they
// establish is that the parser reads what the test author believed the
// program prints — and DIVERGENCE 5.33 is the record of what that
// belief has been worth. `pf` matched no rule at all on a real host
// while its idempotence test passed, because the fixture was written in
// the module's own spelling.
//
// These run the real `dpkg-query`, the real `debconf-set-selections`,
// the real `apt-get update`, and the real zone files, and they *change
// things*: the package selection database, the debconf database,
// `/etc/apt/sources.list.d`, and `/etc/localtime`. Reading is not
// enough — the modules in question are the mutating ones, and a read
// that parses correctly says nothing about whether the write took.
//
// # Why they skip everywhere else
//
// `HALITE_FLEET_LIVE=1` is set by `contrib/docker/fleet/run.sh` and by
// nothing else. A test that holds a package and relinks the system clock
// is not one to run by accident on the machine somebody works on, and
// making it opt-in is what lets it be destructive enough to be worth
// something.
//
// The skip is honest rather than convenient: on a machine with no dpkg
// there is nothing here to establish, and a test that quietly passed by
// finding no tool would be the third instance of the defect this file
// exists to prevent.

// live reports whether this is the image, and skips otherwise.
func live(t *testing.T) *exec.Context {
	t.Helper()
	if os.Getenv("HALITE_FLEET_LIVE") != "1" {
		t.Skip("set HALITE_FLEET_LIVE=1 to drive the real tools; `make fleetcheck` does")
	}
	c := realCtx(t)
	if c.Which("dpkg-query") == "" {
		t.Fatal("HALITE_FLEET_LIVE is set and there is no dpkg-query; this is not the image")
	}
	if os.Geteuid() != 0 {
		t.Fatal("HALITE_FLEET_LIVE is set and this is not root; every module here needs it")
	}
	return c
}

// pkgDir is where the image put a real .deb and the local repository.
func pkgDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("HALITE_FLEET_PKGDIR")
	if dir == "" {
		t.Skip("HALITE_FLEET_PKGDIR is unset")
	}
	return dir
}

// ---- dpkg ----

// What `dpkg-query -W` actually prints, read by the module that parses
// it.
//
// The format string is the module's own, so the risk is not that dpkg
// refuses it — it is that a field arrives in a shape the parser does not
// expect. Multi-arch is the case in point: `${Architecture}` is empty
// for some pseudo-packages and the status word count varies by state.
func TestLiveDpkgListsWhatIsActuallyInstalled(t *testing.T) {
	c := live(t)
	r := New()

	out, err := r.Exec.Call(c, "dpkg.list_pkgs", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	pkgs, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("list_pkgs returned %T", out)
	}
	if pkgs.Len() < 20 {
		t.Fatalf("a Debian base image has more than %d packages; the listing was not parsed", pkgs.Len())
	}

	// dpkg itself is installed, by definition of being able to ask.
	entry, ok := pkgs.Get("dpkg")
	if !ok {
		t.Fatalf("dpkg is not in its own listing; keys begin %v", firstKeys(pkgs, 5))
	}
	fields, ok := entry.(*value.Map)
	if !ok {
		t.Fatalf("dpkg's entry is %T", entry)
	}
	for _, key := range []string{"architecture", "version", "want", "state", "installed"} {
		if _, ok := fields.Get(key); !ok {
			t.Errorf("the parsed entry has no %q: %v", key, fields)
		}
	}
	if v, _ := fields.Get("installed"); v != true {
		t.Errorf("dpkg reports itself as not installed: %v", fields)
	}
	if v, _ := fields.Get("state"); v != "installed" {
		t.Errorf("state = %v, and dpkg's own status word is `installed`", v)
	}

	// Against dpkg-query directly, field by field.
	//
	// "the version is not empty" was the first version of this and it
	// did not bite: swapping the architecture and version columns leaves
	// both non-empty, so the assertion passed with the parser reading
	// the wrong tab. Comparing each field with what the tool says about
	// the same package is the assertion that has somewhere to fail.
	for _, tc := range []struct{ key, format string }{
		{"architecture", "${Architecture}"},
		{"version", "${Version}"},
	} {
		res, err := c.Run(exec.Command{
			Argv: []string{"dpkg-query", "-W", "-f=" + tc.format, "dpkg"},
		})
		if err != nil {
			t.Fatal(err)
		}
		want := strings.TrimSpace(res.Stdout)
		if got, _ := fields.Get(tc.key); got != want {
			t.Errorf("%s = %v and dpkg-query says %q; the columns did not line up",
				tc.key, got, want)
		}
		if want == "" {
			t.Errorf("dpkg-query returned nothing for %s, so this comparison proves nothing", tc.format)
		}
	}
}

// The control fields of a package that is really installed.
//
// `parseControlFields` joins continuation lines, which is how
// Description carries its long form. A real package is the only way to
// know that the joining matches what dpkg emits rather than what the
// RFC describes.
func TestLiveDpkgReadsRealControlFields(t *testing.T) {
	c := live(t)
	r := New()

	out, err := r.Exec.Call(c, "dpkg.info", value.MapOf("name", "tzdata"))
	if err != nil {
		t.Fatal(err)
	}
	info, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("info returned %T", out)
	}
	if v, _ := info.Get("Package"); v != "tzdata" {
		t.Errorf("Package = %v", v)
	}
	desc, _ := info.Get("Description")
	text, _ := desc.(string)
	if !strings.Contains(text, "\n") {
		t.Errorf("tzdata's Description is a multi-line field and arrived as one line: %q", text)
	}

	// And a package that is not installed is an error naming it, rather
	// than an empty map that a state would read as "no fields".
	if _, err := r.Exec.Call(c, "dpkg.info", value.MapOf("name", "not-a-real-package")); err == nil {
		t.Error("dpkg.info on an absent package returned no error")
	}
}

// Which package owns a file, and which files a package owns, both
// against the real database.
func TestLiveDpkgSearchAndFileListAgreeWithEachOther(t *testing.T) {
	c := live(t)
	r := New()

	out, err := r.Exec.Call(c, "dpkg.file_list", value.MapOf("name", "tzdata"))
	if err != nil {
		t.Fatal(err)
	}
	files, ok := out.([]any)
	if !ok || len(files) == 0 {
		t.Fatalf("file_list returned %T with %d entries", out, len(files))
	}

	// Pick a real file from that list and ask who owns it. The two
	// answers have to agree, and neither of them came from a fixture.
	var probe string
	for _, f := range files {
		s, _ := f.(string)
		if info, err := os.Stat(s); err == nil && !info.IsDir() {
			probe = s
			break
		}
	}
	if probe == "" {
		t.Fatalf("no regular file among %d entries; the listing was mis-parsed", len(files))
	}

	found, err := r.Exec.Call(c, "dpkg.search", value.MapOf("path", probe))
	if err != nil {
		t.Fatal(err)
	}
	owners, ok := found.(*value.Map)
	if !ok {
		t.Fatalf("search returned %T", found)
	}
	// Keyed by owning package, holding the paths it owns -- because
	// several packages can own one path and dpkg lists them comma
	// separated on the left of its colon.
	paths, ok := owners.Get("tzdata")
	if !ok {
		t.Fatalf("dpkg.search(%q) does not name tzdata, which file_list said owns it: %v", probe, owners)
	}
	list, _ := paths.([]any)
	if !listHas(list, probe) {
		t.Errorf("tzdata's entry does not hold %q: %v", probe, list)
	}
}

// A real .deb read from disk rather than from the database.
func TestLiveDpkgReadsARealPackageFile(t *testing.T) {
	c := live(t)
	r := New()

	matches, err := filepath.Glob(filepath.Join(pkgDir(t), "*.deb"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no .deb in the image's package directory: %v", err)
	}

	out, err := r.Exec.Call(c, "dpkg.bin_pkg_info", value.MapOf("path", matches[0]))
	if err != nil {
		t.Fatal(err)
	}
	info, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("bin_pkg_info returned %T", out)
	}
	for _, key := range []string{"Package", "Version", "Architecture"} {
		if v, _ := info.Get(key); v == nil || v == "" {
			t.Errorf("%s is empty in %v", key, info)
		}
	}
}

// **A real hold, placed and then read back.**
//
// This is the mutating half, and the one a fixture cannot reach.
// `dpkg --set-selections` reads from stdin in a format this module
// writes, and whether dpkg accepts that format is exactly the question.
// It is put back afterwards, because a held package changes what every
// later test in this image sees.
func TestLiveDpkgHoldsAPackageAndDpkgAgrees(t *testing.T) {
	c := live(t)
	r := New()
	const pkg = "tzdata"

	t.Cleanup(func() {
		_, _ = r.Exec.Call(c, "dpkg.set_selections",
			value.MapOf("selections", value.MapOf(pkg, "install")))
	})

	out, err := r.Exec.Call(c, "dpkg.set_selections",
		value.MapOf("selections", value.MapOf(pkg, "hold")))
	if err != nil {
		t.Fatalf("set_selections: %v", err)
	}
	changed, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("set_selections returned %T", out)
	}
	if _, ok := changed.Get(pkg); !ok {
		t.Errorf("setting a hold reported no change for %s: %v", pkg, changed)
	}

	// Read back through the module, which is the parser under test.
	back, err := r.Exec.Call(c, "dpkg.get_selections", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	sels, ok := back.(*value.Map)
	if !ok {
		t.Fatalf("get_selections returned %T", back)
	}
	// Flat: package to its selection word, which is dpkg's own shape
	// rather than Salt's grouping by state.
	if state, _ := sels.Get(pkg); state != "hold" {
		t.Errorf("%s was held and get_selections reports it as %v", pkg, state)
	}

	// And read back through dpkg directly, so the assertion does not
	// rest on this build parsing its own write.
	res, err := c.Run(exec.Command{Argv: []string{"dpkg", "--get-selections", pkg}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "hold") {
		t.Errorf("dpkg itself does not report the hold: %q", res.Stdout)
	}
}

// ---- debconf ----

// **A real answer written into the real debconf database, and read
// back.**
//
// `debconf-set-selections` takes four whitespace-separated fields on
// stdin and is silent when it accepts them, so a malformed line is
// accepted-looking and does nothing. That is the failure this test
// exists for: the module composes the line, and only debconf can say
// whether it means what the module intended.
func TestLiveDebconfSetsAnAnswerAndDebconfAgrees(t *testing.T) {
	c := live(t)
	r := New()
	const pkg = "halite-fleet-probe"

	_, err := r.Exec.Call(c, "debconf.set", value.MapOf(
		"package", pkg,
		"question", "halite/probe",
		"type", "boolean",
		"value", "true",
	))
	if err != nil {
		t.Fatalf("debconf.set: %v", err)
	}

	out, err := r.Exec.Call(c, "debconf.get_selections", value.MapOf("package", pkg))
	if err != nil {
		t.Fatal(err)
	}
	sels, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("get_selections returned %T", out)
	}
	// Keyed "<package> <question>", which is how the selections format
	// addresses an answer.
	entry, ok := sels.Get(pkg + " halite/probe")
	if !ok {
		t.Fatalf("the question this test just answered is not in the database: %v", firstKeys(sels, 8))
	}
	fields, ok := entry.(*value.Map)
	if !ok {
		t.Fatalf("the entry is %T", entry)
	}
	if v, _ := fields.Get("value"); v != "true" {
		t.Errorf("value = %v, want true; the answer was written as something else", v)
	}
	if v, _ := fields.Get("type"); v != "boolean" {
		t.Errorf("type = %v, want boolean", v)
	}

	// And debconf-show, the other reader, agrees about the same answer.
	shown, err := r.Exec.Call(c, "debconf.show", value.MapOf("package", pkg))
	if err != nil {
		t.Fatal(err)
	}
	answers, _ := shown.(*value.Map)
	got, ok := answers.Get("halite/probe")
	if !ok {
		t.Fatalf("debconf.show does not report the question: %v", answers)
	}
	if m, _ := got.(*value.Map); m != nil {
		if v, _ := m.Get("value"); v != "true" {
			t.Errorf("debconf-show reports %v and debconf-get-selections reported true", v)
		}
	}

	// Through debconf's own tool as well, for the same reason the dpkg
	// test does it: parsing this build's own write proves the round
	// trip and not the format.
	res, err := c.Run(exec.Command{
		Argv:           []string{"debconf-get-selections"},
		IgnoreExitCode: true,
	})
	if err == nil && res.Code == 0 && !strings.Contains(res.Stdout, "halite/probe") {
		t.Errorf("debconf-get-selections does not show the answer this test set")
	}
}

// `debconf-show` on a package debconf has never heard of.
//
// The module's own tests assume this is an empty answer. On a real
// debconf it is an empty answer *and a non-zero exit*, which is the
// shape of thing a fixture written from the manual page gets wrong.
func TestLiveDebconfShowOnAnUnknownPackageIsEmptyRatherThanAnError(t *testing.T) {
	c := live(t)
	r := New()

	out, err := r.Exec.Call(c, "debconf.show", value.MapOf("package", "no-such-package-here"))
	if err != nil {
		t.Fatalf("debconf.show on an unknown package failed rather than returning nothing: %v", err)
	}
	shown, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("show returned %T", out)
	}
	if shown.Len() != 0 {
		t.Errorf("an unknown package has %d answers: %v", shown.Len(), shown)
	}
}

// ---- pkgrepo ----

// **A repository written, read by apt, and removed.**
//
// Writing the file proves the module can write a file. What this adds is
// that `apt-get update` accepts what was written: a line with the
// components in the wrong order, or without `[trusted=yes]` on an
// unsigned repository, produces a file that looks correct and a refresh
// that fails.
//
// The repository is the image's own, on the filesystem, so the refresh
// is real and reaches no network.
func TestLiveRepoIsWrittenAndAptReadsIt(t *testing.T) {
	c := live(t)
	r := New()
	const name = "halite-fleet-local"
	dir := pkgDir(t)

	t.Cleanup(func() {
		_, _ = r.Exec.Call(c, "pkgrepo.del_repo", value.MapOf("name", name))
	})

	keyring := os.Getenv("HALITE_FLEET_KEYRING")
	if keyring == "" {
		t.Skip("HALITE_FLEET_KEYRING is unset")
	}

	out, err := r.Exec.Call(c, "pkgrepo.mod_repo", value.MapOf(
		"name", name,
		"baseurl", "file://"+dir,
		"dist", "./",
		// The keyring the image signed the repository with. apt refuses
		// an unsigned repository, so this is the field that decides
		// whether the refresh below can work at all -- and it is the
		// field an operator actually sets.
		"signedby", keyring,
		// The refresh is the point, so it is left on.
		"refresh", true,
	))
	if err != nil {
		t.Fatalf("mod_repo: %v", err)
	}
	if out != true {
		t.Errorf("writing a repository that was not there reported changed=%v", out)
	}

	// apt read it, which is what the refresh above established. Asserted
	// directly too, so a provider that swallowed the refresh error
	// cannot pass.
	res, err := c.Run(exec.Command{Argv: []string{"apt-get", "update"}, IgnoreExitCode: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != 0 {
		t.Fatalf("apt-get update failed on the repository this module wrote:\n%s", res.Stderr+res.Stdout)
	}

	// And apt can actually see a package from it.
	//
	// The exit code above is necessary and not sufficient: measured in
	// this image, `apt-get update` reports an unreachable source as a
	// warning and still exits 0. It exits non-zero for a repository it
	// *rejects* -- an unsigned one, say -- so the check above does bite
	// for that, and would not notice a repository apt had quietly
	// ignored. This is the assertion that apt's index really holds what
	// this module published.
	policy, err := c.Run(exec.Command{
		Argv:           []string{"apt-cache", "policy", "hello"},
		IgnoreExitCode: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(policy.Stdout, "file:"+dir) {
		t.Errorf("apt's index does not name the repository this module wrote.\n"+
			"`apt-cache policy hello` said:\n%s", policy.Stdout)
	}

	// Read back through the module.
	got, err := r.Exec.Call(c, "pkgrepo.get_repo", value.MapOf("name", name))
	if err != nil {
		t.Fatal(err)
	}
	repo, ok := got.(*value.Map)
	if !ok || repo.Len() == 0 {
		t.Fatalf("get_repo returned %T: %v", got, got)
	}
	if v, _ := repo.Get("baseurl"); v != "file://"+dir {
		t.Errorf("baseurl = %v, want file://%s; get_repo returned %v", v, dir, repo)
	}

	listed, err := r.Exec.Call(c, "pkgrepo.list_repos", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	all, ok := listed.(*value.Map)
	if !ok {
		t.Fatalf("list_repos returned %T", listed)
	}
	if _, ok := all.Get(name); !ok {
		t.Errorf("the repository this test wrote is not in list_repos: %v", firstKeys(all, 8))
	}

	// And removing it removes it, from the module's view and from disk.
	if _, err := r.Exec.Call(c, "pkgrepo.del_repo", value.MapOf("name", name)); err != nil {
		t.Fatal(err)
	}
	after, err := r.Exec.Call(c, "pkgrepo.list_repos", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	remaining, _ := after.(*value.Map)
	if _, ok := remaining.Get(name); ok {
		t.Errorf("del_repo left the repository in the listing")
	}
}

// ---- timezone ----

// **The system clock's zone, changed and put back.**
//
// `/etc/localtime` is a symlink into /usr/share/zoneinfo on Debian, and
// `zoneFromPath` reads the zone name back out of wherever the link
// points. That round trip is the whole module, and it has never been
// run against a real zoneinfo tree by this build.
func TestLiveTimezoneSetsTheZoneAndReadsItBack(t *testing.T) {
	c := live(t)
	r := New()

	before, err := r.Exec.Call(c, "timezone.get_zone", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the image's zone is %v", before)

	want := "Europe/Lisbon"
	if before == want {
		want = "Pacific/Auckland"
	}
	t.Cleanup(func() {
		if s, ok := before.(string); ok && s != "" {
			_, _ = r.Exec.Call(c, "timezone.set_zone", value.MapOf("timezone", s))
		}
	})

	if _, err := r.Exec.Call(c, "timezone.set_zone", value.MapOf("timezone", want)); err != nil {
		t.Fatalf("set_zone(%s): %v", want, err)
	}

	got, err := r.Exec.Call(c, "timezone.get_zone", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("set the zone to %s and read back %v", want, got)
	}

	// Against the filesystem rather than against this build's reader,
	// which is the same reason the dpkg and debconf tests ask the tool.
	target, err := os.Readlink("/etc/localtime")
	if err != nil {
		t.Fatalf("/etc/localtime is not a link after set_zone: %v", err)
	}
	if !strings.HasSuffix(target, want) {
		t.Errorf("/etc/localtime points at %q, which does not end in %q", target, want)
	}

	// zone_compare answers about the zone that is now set.
	same, err := r.Exec.Call(c, "timezone.zone_compare", value.MapOf("timezone", want))
	if err != nil {
		t.Fatal(err)
	}
	if same != true {
		t.Errorf("zone_compare(%s) = %v just after setting it", want, same)
	}
}

// A zone the machine does not have is refused rather than set.
//
// The module checks against `list_zones`, which reads the real zoneinfo
// tree here. A build that got that listing wrong would accept a zone
// that does not exist and leave /etc/localtime pointing at nothing —
// which a machine notices at its next boot rather than now.
func TestLiveTimezoneRefusesAZoneThisMachineDoesNotHave(t *testing.T) {
	c := live(t)
	r := New()

	zones, err := r.Exec.Call(c, "timezone.list_zones", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	list, ok := zones.([]string)
	if !ok || len(list) < 100 {
		t.Fatalf("list_zones returned %T with %d entries; a real tzdata has hundreds", zones, len(list))
	}
	var utc bool
	for _, z := range list {
		if z == "UTC" || z == "Etc/UTC" {
			utc = true
		}
	}
	if !utc {
		t.Errorf("neither UTC nor Etc/UTC is in %d zones; the listing was not read from the tree", len(list))
	}

	if _, err := r.Exec.Call(c, "timezone.set_zone",
		value.MapOf("timezone", "Mars/Olympus_Mons")); err == nil {
		t.Error("a zone that does not exist was accepted")
	}
	// And the machine was left alone.
	if _, err := os.Readlink("/etc/localtime"); err != nil {
		t.Errorf("/etc/localtime is no longer a link after a refused set_zone: %v", err)
	}
}

// ---- helpers ----

func listHas(list []any, want string) bool {
	for _, v := range list {
		if s, _ := v.(string); s == want {
			return true
		}
	}
	return false
}

func firstKeys(m *value.Map, n int) []any {
	keys := m.Keys()
	if len(keys) > n {
		keys = keys[:n]
	}
	return keys
}
