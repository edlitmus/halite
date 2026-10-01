package builtin

import (
	"fmt"
	"os"
	"os/user"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// Each provider speaks to its package manager through the manager's own
// binary, in a machine-readable output mode and with a non-interactive
// environment, never through a C library binding. SPEC section 15.2.

// aptEnv is the environment every apt invocation gets: non-interactive, so
// a configuration file prompt cannot hang a state run forever.
func aptEnv() []string {
	return append(exec.CleanEnv(),
		"DEBIAN_FRONTEND=noninteractive",
		"APT_LISTCHANGES_FRONTEND=none",
		"UCF_FORCE_CONFFOLD=1",
	)
}

type aptProvider struct{}

func (aptProvider) Name() string { return "aptpkg" }

func (aptProvider) Available(c *exec.Context) bool {
	return c.Which("dpkg-query") != "" && c.Which("apt-get") != ""
}

func (aptProvider) ListPkgs(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{
		Argv: []string{"dpkg-query", "-W", "-f=${Package}\\t${Version}\\t${Status}\\n"},
		Env:  aptEnv(),
	})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(256)
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		// Only packages that are actually installed count; dpkg lists
		// removed-but-configured ones too, and treating those as present
		// makes a pkg.installed state a no-op forever.
		if !strings.HasSuffix(fields[2], " installed") {
			continue
		}
		out.Set(fields[0], fields[1])
	}
	return out, nil
}

func (aptProvider) Install(c *exec.Context, names []string, versions map[string]string, refresh bool) error {
	if refresh {
		if _, err := c.Run(exec.Command{Argv: []string{"apt-get", "update", "-q"}, Env: aptEnv()}); err != nil {
			return fmt.Errorf("apt-get update: %w", err)
		}
	}
	argv := []string{"apt-get", "install", "-y", "-q",
		"-o", "Dpkg::Options::=--force-confold",
		"-o", "Dpkg::Options::=--force-confdef"}
	for _, n := range names {
		if v, ok := versions[n]; ok && v != "" && !strings.HasSuffix(v, "*") {
			argv = append(argv, n+"="+v)
			continue
		}
		argv = append(argv, n)
	}
	_, err := c.Run(exec.Command{Argv: argv, Env: aptEnv()})
	return err
}

func (aptProvider) Remove(c *exec.Context, names []string, purge bool) error {
	verb := "remove"
	if purge {
		verb = "purge"
	}
	argv := append([]string{"apt-get", verb, "-y", "-q"}, names...)
	_, err := c.Run(exec.Command{Argv: argv, Env: aptEnv()})
	return err
}

func (aptProvider) LatestVersion(c *exec.Context, name string) (string, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"apt-cache", "policy", name},
		Env:            aptEnv(),
		IgnoreExitCode: true,
	})
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Candidate:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "Candidate:"))
			if v == "(none)" {
				return "", nil
			}
			return v, nil
		}
	}
	return "", nil
}

func (aptProvider) RefreshDB(c *exec.Context) error {
	_, err := c.Run(exec.Command{Argv: []string{"apt-get", "update", "-q"}, Env: aptEnv()})
	return err
}

// dnfProvider covers both dnf and yum, whose command surfaces are the same
// for what halite needs.
type dnfProvider struct{ binary string }

func (p dnfProvider) Name() string { return p.binary + "pkg" }

func (p dnfProvider) Available(c *exec.Context) bool {
	return c.Which("rpm") != "" && c.Which(p.binary) != ""
}

// ListPkgs reads rpm's database, keeping the newest instance of a name.
//
// EL installs kernels side by side -- AlmaLinux 8.10 in the lab had two
// `kernel-core` (DIVERGENCE 5.172) -- and a map keyed by name has room for
// one. This used to keep whichever rpm printed last, which is the order of
// rpm's database and not an answer to anything; the zypper provider had
// already been made to keep the newest by rpm's ordering (5.176), and this
// is the same argv, so it is the same parser (5.177).
func (p dnfProvider) ListPkgs(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{
		Argv: []string{"rpm", "-qa", "--queryformat", "%{NAME}\\t%{EPOCH}:%{VERSION}-%{RELEASE}\\n"},
	})
	if err != nil {
		return nil, err
	}
	return parseRPMInstalledNewest(res.Stdout), nil
}

// Install adds packages, refreshing the metadata first when asked.
//
// # Why `-C` is not the opposite of a refresh
//
// This used to pass `-C` when `refresh` was false, on the reading that
// "do not refresh" means "work from the cache". It does not, and dnf says
// so in its own help:
//
//	-C, --cacheonly       run entirely from system cache, don't update cache
//	--refresh             set metadata as expired before running the command
//
// `--cacheonly` forbids *downloading the packages*, not just refreshing the
// metadata. So `pkg.installed` -- whose `refresh` defaults to false --
// could not install anything that was not already in the local cache, which
// on a machine that has never installed it is everything. Measured on Rocky
// Linux 9, where dnf names the cause itself:
//
//	# dnf install -y -q -C tree
//	Error: Some packages have invalid cache, but cannot be downloaded
//	due to "--cacheonly" option
//	# dnf install -y -q tree
//	Installed: tree-1.8.0-10.el9.x86_64
//
// The shape to copy was beside it all along: `aptProvider.Install` runs
// `apt-get update` when refresh is set and adds nothing when it is not. This
// is the same behaviour in dnf's vocabulary. `--refresh` rather than a
// separate `makecache` because it is one command and cannot half-succeed.
//
// EL7's yum 3 has no `--refresh`, so an explicit `refresh: true` would be
// refused there. EL7 left support in June 2024 and is in no tier of SPEC
// 27.1; EL8 and later ship yum as a link to dnf, where the flag is the
// same one. Saying so beats silently dropping the caller's request.
//
// `ListUpgrades` keeps its `-C`, and correctly: reading what is available
// from the metadata already held downloads nothing, so there "do not
// refresh" and "cache only" really are the same instruction.
func (p dnfProvider) Install(c *exec.Context, names []string, versions map[string]string, refresh bool) error {
	argv := []string{p.binary, "install", "-y", "-q"}
	if refresh {
		argv = append(argv, "--refresh")
	}
	for _, n := range names {
		if v, ok := versions[n]; ok && v != "" && !strings.HasSuffix(v, "*") {
			argv = append(argv, n+"-"+v)
			continue
		}
		argv = append(argv, n)
	}
	_, err := c.Run(exec.Command{Argv: argv})
	return err
}

func (p dnfProvider) Remove(c *exec.Context, names []string, purge bool) error {
	argv := append([]string{p.binary, "remove", "-y", "-q"}, names...)
	_, err := c.Run(exec.Command{Argv: argv})
	return err
}

func (p dnfProvider) LatestVersion(c *exec.Context, name string) (string, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{p.binary, "--quiet", "list", "available", name},
		IgnoreExitCode: true,
	})
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.HasPrefix(fields[0], name+".") {
			return strings.TrimPrefix(fields[1], "0:"), nil
		}
	}
	return "", nil
}

func (p dnfProvider) RefreshDB(c *exec.Context) error {
	_, err := c.Run(exec.Command{Argv: []string{p.binary, "makecache", "-q"}})
	return err
}

// pkgngProvider is FreeBSD's pkg.
type pkgngProvider struct{}

func (pkgngProvider) Name() string { return "pkgng" }

func (pkgngProvider) Available(c *exec.Context) bool { return c.Which("pkg") != "" }

func (pkgngProvider) ListPkgs(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{Argv: []string{"pkg", "query", "%n\\t%v"}})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(256)
	for _, line := range strings.Split(res.Stdout, "\n") {
		name, version, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		out.Set(name, version)
	}
	return out, nil
}

func (pkgngProvider) Install(c *exec.Context, names []string, versions map[string]string, refresh bool) error {
	if refresh {
		if _, err := c.Run(exec.Command{Argv: []string{"pkg", "update", "-q"}}); err != nil {
			return fmt.Errorf("pkg update: %w", err)
		}
	}
	argv := append([]string{"pkg", "install", "-y", "-q"}, names...)
	_, err := c.Run(exec.Command{Argv: argv, Env: append(exec.CleanEnv(), "ASSUME_ALWAYS_YES=YES")})
	return err
}

func (pkgngProvider) Remove(c *exec.Context, names []string, purge bool) error {
	argv := append([]string{"pkg", "delete", "-y", "-q"}, names...)
	_, err := c.Run(exec.Command{Argv: argv, Env: append(exec.CleanEnv(), "ASSUME_ALWAYS_YES=YES")})
	return err
}

func (pkgngProvider) LatestVersion(c *exec.Context, name string) (string, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"pkg", "rquery", "%v", name},
		IgnoreExitCode: true,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(firstLine(res.Stdout)), nil
}

func (pkgngProvider) RefreshDB(c *exec.Context) error {
	_, err := c.Run(exec.Command{Argv: []string{"pkg", "update", "-q"}})
	return err
}

// apkProvider is Alpine's apk.
type apkProvider struct{}

func (apkProvider) Name() string { return "apkpkg" }

func (apkProvider) Available(c *exec.Context) bool { return c.Which("apk") != "" }

func (apkProvider) ListPkgs(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{Argv: []string{"apk", "info", "-v"}})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(128)
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// apk prints name-version-release; the name ends at the last two
		// hyphen-separated fields.
		parts := strings.Split(line, "-")
		if len(parts) < 3 {
			continue
		}
		name := strings.Join(parts[:len(parts)-2], "-")
		version := strings.Join(parts[len(parts)-2:], "-")
		out.Set(name, version)
	}
	return out, nil
}

func (apkProvider) Install(c *exec.Context, names []string, versions map[string]string, refresh bool) error {
	argv := []string{"apk", "add", "--no-progress"}
	if refresh {
		argv = append(argv, "--update-cache")
	}
	argv = append(argv, names...)
	_, err := c.Run(exec.Command{Argv: argv})
	return err
}

func (apkProvider) Remove(c *exec.Context, names []string, purge bool) error {
	argv := append([]string{"apk", "del", "--no-progress"}, names...)
	_, err := c.Run(exec.Command{Argv: argv})
	return err
}

func (apkProvider) LatestVersion(c *exec.Context, name string) (string, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"apk", "list", "--available", name},
		IgnoreExitCode: true,
	})
	if err != nil {
		return "", err
	}
	line := firstLine(res.Stdout)
	if line == "" {
		return "", nil
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", nil
	}
	parts := strings.Split(fields[0], "-")
	if len(parts) < 3 {
		return "", nil
	}
	return strings.Join(parts[len(parts)-2:], "-"), nil
}

func (apkProvider) RefreshDB(c *exec.Context) error {
	_, err := c.Run(exec.Command{Argv: []string{"apk", "update", "--no-progress"}})
	return err
}

// brewEnv is the environment every Homebrew invocation gets: no
// auto-update on every command (that belongs to refresh_db alone), no
// post-install cleanup pass, and no interactive hints, so a state run gets
// only the output of the thing it asked for.
//
// Unlike every other provider here, brew refuses outright to run without
// $HOME set ("Error: $HOME must be set to run brew"), which CleanEnv does
// not carry — this was found by running the provider against a real
// Homebrew, not read off documentation.
//
// The caller's HOME is passed only when brew runs as the caller. When it
// runs as the account that owns it, the credential switch supplies that
// account's HOME, and root's must not be there as well: a duplicate key
// resolves to the last one, which happens to be the right one today, and
// a Homebrew cache written under /var/root by a process that cannot read
// it back is not a failure anybody should be one append away from.
func brewEnv(asOwner bool) []string {
	env := append(exec.CleanEnv(),
		"HOMEBREW_NO_AUTO_UPDATE=1",
		"HOMEBREW_NO_INSTALL_CLEANUP=1",
		"HOMEBREW_NO_ENV_HINTS=1",
	)
	if home := os.Getenv("HOME"); home != "" && !asOwner {
		env = append(env, "HOME="+home)
	}
	return env
}

// brewEuid and brewOwner are what brewRun asks about the machine, as
// variables so a test can say "this is root, and alice owns brew"
// without being either.
var (
	brewEuid  = os.Geteuid
	brewOwner = brewBinaryOwner
)

// brewRun runs one Homebrew command as the account Homebrew belongs to.
//
// **Homebrew refuses to run as root**, and the node runs as root. Every
// command, reading ones included, ends at "Error: Running Homebrew as
// root is extremely dangerous and no longer supported." -- measured with
// `sudo halite-node call pkg.list_pkgs` on a Mac with Homebrew 7.0.7
// under /opt/homebrew. Before this, the provider ran brew as whoever ran
// the node, so on a real node every `pkg` function on macOS failed, and
// nothing said so: `pkg`'s evidence was earned by apt, pkgng and
// chocolatey, and the live suite leaves macOS out because its leg is
// root and brew refuses root -- which is the same fact, read as a reason
// not to test rather than as the defect.
//
// The account is the owner of the brew program, which is how Salt's
// mac_brew_pkg chooses it and the only thing on the machine that says
// who installed Homebrew. Not the console user: the person logged in is
// not necessarily the person whose Homebrew it is, and a node with
// nobody logged in still has one.
//
// Three cases where it does not switch:
//
//   - The node is not root. Then it already is somebody, a non-root
//     process cannot become anybody else, and brew decides for itself
//     whether that somebody may use it.
//   - The context already names an account (a state's `runas`). That is
//     an operator's explicit choice and is honoured as Context.Run
//     honours it everywhere else; if it names root, brew's own refusal
//     says why.
//   - root owns brew. Homebrew's installer refuses to install as root,
//     so this is a machine somebody built by hand, and running as the
//     owner would be running as root. It is refused here by name rather
//     than passed to brew to refuse less clearly.
//
// The working directory moves to the owner's home. A node's cwd is
// wherever it was started, which under sudo is the caller's directory
// and under launchd is /, and brew run as another account in a directory
// that account cannot read fails in Ruby's startup rather than in
// anything that names the directory.
func brewRun(c *exec.Context, cmd exec.Command) (exec.Result, error) {
	owner, err := brewRunAs(c)
	if err != nil {
		return exec.Result{}, err
	}
	cmd.Env = brewEnv(owner != nil)
	if owner != nil {
		cmd.RunAs = owner.Username
		if cmd.Dir == "" {
			cmd.Dir = owner.HomeDir
		}
	}
	return c.Run(cmd)
}

// brewRunAs is the account brew must run as, or nil for "as whoever this
// is". See brewRun for the three cases that are nil.
func brewRunAs(c *exec.Context) (*user.User, error) {
	if c.RunAs != "" || brewEuid() != 0 {
		return nil, nil
	}
	path := c.Which("brew")
	if path == "" {
		return nil, fmt.Errorf("mac_brew_pkg: brew was not found on the path")
	}
	owner, err := brewOwner(path)
	if err != nil {
		return nil, fmt.Errorf("mac_brew_pkg: finding the account Homebrew belongs to: %w", err)
	}
	if owner.Uid == "0" {
		return nil, fmt.Errorf("mac_brew_pkg: %s is owned by root, and Homebrew will not run as root; "+
			"it has to belong to an ordinary account, which is the only way its own installer makes it", path)
	}
	return owner, nil
}

// brewProvider is macOS's Homebrew, named mac_brew_pkg to match the module
// Salt trees already call by that name. SPEC section 15.2, 15.3.
type brewProvider struct{}

func (brewProvider) Name() string { return "mac_brew_pkg" }

func (brewProvider) Available(c *exec.Context) bool { return c.Which("brew") != "" }

// ListPkgs reads `brew info --json=v2 --installed`, not `brew list
// --versions`, because the list fails as a whole on one cask Homebrew
// cannot load.
//
// Measured on a Mac with Homebrew 7.0.7 and 18 Caskroom entries, four of
// which brew itself calls "not installed" -- three holding only
// `.metadata`, one whose cask definition no longer loads. `brew list
// --versions` printed the 389 formulae, then `Error: Cask 'kiro-cli' is
// not installed.` and exit 1; `brew list --cask --versions` printed
// nothing at all, so the 13 healthy casks were lost with the broken one.
// The provider failed on the exit, so `pkg.list_pkgs`, and every
// `pkg.installed` that reads it first, failed on that machine for a cask
// nobody had asked about. DIVERGENCE 5.189.
//
// `info --installed` enumerates through Homebrew's Caskroom.casks, which
// skips a cask it cannot load ("Don't blow up because of a single
// unavailable cask", cask/caskroom.rb) and keeps the ones it considers
// installed. That is brew's own answer to "which casks are installed",
// so a broken entry is left out here for the reason brew leaves it out,
// not because this provider chose to look away. It answered 389 formulae
// and 13 casks, exit 0, on the same machine.
func (brewProvider) ListPkgs(c *exec.Context) (*value.Map, error) {
	res, err := brewRun(c, exec.Command{Argv: []string{"brew", "info", "--json=v2", "--installed"}})
	if err != nil {
		return nil, err
	}
	return parseBrewInstalled(res.Stdout)
}

// parseBrewInstalled turns `brew info --json=v2 --installed` into name ->
// version.
//
// A formula's version is the one that is **linked**, which is the one on
// the PATH, and the newest installed when none is (a keg-only formula, or
// one unlinked by hand). Not the newest outright: this Mac has fish
// 4.0b1 linked beside 4.6.0, and `fish` there is 4.0b1. `installed` is
// sorted by Homebrew's own version order (`sort_by(&:scheme_and_version)`
// in formula.rb), so its last entry is the newest by brew's rules rather
// than this file's. The `brew list --versions` this replaces printed kegs
// in directory order -- `fish 4.6.0 4.0b1` -- and its parser took the
// last field, which happened to be right for fish and was documented as
// "oldest first", which it is not. On the capture this was checked
// against, the two agree for all 389 formulae.
//
// Names are the short `name` and cask `token`, which is what `brew list`
// printed and so what every existing state already declares. A cask is
// set after the formulae, so a name that is both is reported at the
// cask's version, as the list's last line used to win.
func parseBrewInstalled(stdout string) (*value.Map, error) {
	v, err := value.DecodeJSON([]byte(stdout))
	if err != nil {
		return nil, fmt.Errorf("brew info --json=v2 --installed: %w", err)
	}
	doc, ok := v.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("brew info --json=v2 --installed: top level is %s, not an object", value.TypeName(v))
	}
	out := value.NewMap(256)
	formulae, _ := doc.Get("formulae")
	for _, item := range asList(formulae) {
		f, ok := item.(*value.Map)
		if !ok {
			continue
		}
		name := brewString(f, "name")
		version := brewString(f, "linked_keg")
		if version == "" {
			installed, _ := f.Get("installed")
			if kegs := asList(installed); len(kegs) > 0 {
				if last, ok := kegs[len(kegs)-1].(*value.Map); ok {
					version = brewString(last, "version")
				}
			}
		}
		if name != "" && version != "" {
			out.Set(name, version)
		}
	}
	casks, _ := doc.Get("casks")
	for _, item := range asList(casks) {
		k, ok := item.(*value.Map)
		if !ok {
			continue
		}
		if token, version := brewString(k, "token"), brewString(k, "installed"); token != "" && version != "" {
			out.Set(token, version)
		}
	}
	return out, nil
}

// brewString is a string field of a brew JSON object, or "" when it is
// absent or null -- `linked_keg` is null for a keg-only formula.
func brewString(m *value.Map, key string) string {
	v, ok := m.Get(key)
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func (brewProvider) Install(c *exec.Context, names []string, versions map[string]string, refresh bool) error {
	if refresh {
		if _, err := brewRun(c, exec.Command{Argv: []string{"brew", "update", "--quiet"}}); err != nil {
			return fmt.Errorf("brew update: %w", err)
		}
	}
	// Homebrew has no apt/dnf-style "name=version" pin at install time; a
	// version request is satisfied by installing by name and is enforced
	// afterward with brew.hold, the way pkgng's lock does.
	argv := append([]string{"brew", "install", "--quiet"}, names...)
	_, err := brewRun(c, exec.Command{Argv: argv})
	return err
}

func (brewProvider) Remove(c *exec.Context, names []string, purge bool) error {
	argv := []string{"brew", "uninstall", "--quiet"}
	if purge {
		// Homebrew's uninstall already removes a formula's Cellar
		// entirely; --force additionally takes every installed version
		// rather than just the linked one, which is the closest analogue
		// this package manager has to purge.
		argv = append(argv, "--force")
	}
	argv = append(argv, names...)
	_, err := brewRun(c, exec.Command{Argv: argv})
	return err
}

func (brewProvider) LatestVersion(c *exec.Context, name string) (string, error) {
	res, err := brewRun(c, exec.Command{
		Argv:           []string{"brew", "info", "--json=v2", name},
		IgnoreExitCode: true,
	})
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", nil
	}
	f, err := firstBrewFormula(res.Stdout)
	if err != nil || f == nil {
		return "", err
	}
	versions, ok := mustMap(f, "versions")
	if !ok {
		return "", nil
	}
	stable, _ := versions.Get("stable")
	return value.KeyString(stable), nil
}

func (brewProvider) RefreshDB(c *exec.Context) error {
	_, err := brewRun(c, exec.Command{Argv: []string{"brew", "update", "--quiet"}})
	return err
}

// firstBrewFormula decodes a `brew info --json=v2` response and returns
// its first formula, or nil when the name matched none.
func firstBrewFormula(stdout string) (*value.Map, error) {
	v, err := value.DecodeJSON([]byte(stdout))
	if err != nil {
		return nil, err
	}
	m, ok := v.(*value.Map)
	if !ok {
		return nil, nil
	}
	formulae, ok := m.Get("formulae")
	if !ok {
		return nil, nil
	}
	list, ok := formulae.([]any)
	if !ok || len(list) == 0 {
		return nil, nil
	}
	f, _ := list[0].(*value.Map)
	return f, nil
}

// ---- mac_brew_pkg: hold, upgrade, and the optional interfaces it can
// actually answer. FileList, OwnerOf, and ListRepos have no clean
// Homebrew analogue and are left unimplemented, the same as for every
// provider but pkgng. ----

func (brewProvider) Hold(c *exec.Context, name string) error {
	_, err := brewRun(c, exec.Command{Argv: []string{"brew", "pin", name}})
	return err
}

func (brewProvider) Unhold(c *exec.Context, name string) error {
	_, err := brewRun(c, exec.Command{Argv: []string{"brew", "unpin", name}})
	return err
}

func (brewProvider) ListHolds(c *exec.Context) ([]string, error) {
	res, err := brewRun(c, exec.Command{
		Argv: []string{"brew", "list", "--pinned"}, IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	return sortedLines(res.Stdout), nil
}

func (p brewProvider) Upgrade(c *exec.Context, refresh bool) (*value.Map, error) {
	before, err := p.ListPkgs(c)
	if err != nil {
		return nil, err
	}
	if refresh {
		if _, err := brewRun(c, exec.Command{Argv: []string{"brew", "update", "--quiet"}}); err != nil {
			return nil, fmt.Errorf("brew update: %w", err)
		}
	}
	if _, err := brewRun(c, exec.Command{Argv: []string{"brew", "upgrade", "--quiet"}}); err != nil {
		return nil, err
	}
	return pkgDelta(c, p, before)
}

func (brewProvider) ListUpgrades(c *exec.Context, refresh bool) (*value.Map, error) {
	if refresh {
		if _, err := brewRun(c, exec.Command{Argv: []string{"brew", "update", "--quiet"}}); err != nil {
			return nil, fmt.Errorf("brew update: %w", err)
		}
	}
	res, err := brewRun(c, exec.Command{
		Argv: []string{"brew", "outdated", "--json=v2"}, IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	v, err := value.DecodeJSON([]byte(res.Stdout))
	if err != nil {
		return nil, err
	}
	m, ok := v.(*value.Map)
	if !ok {
		return value.NewMap(0), nil
	}
	formulae, _ := m.Get("formulae")
	list, _ := formulae.([]any)
	out := value.NewMap(len(list))
	for _, item := range list {
		f, ok := item.(*value.Map)
		if !ok {
			continue
		}
		name, _ := f.Get("name")
		current, _ := f.Get("current_version")
		out.Set(value.KeyString(name), value.KeyString(current))
	}
	return out, nil
}
