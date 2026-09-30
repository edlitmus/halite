package builtin

import (
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// zypper, the SUSE provider of SPEC section 15.2, and what SPEC 15.3's
// `zypperpkg` resolves to.
//
// Written on 2026-09-30 against a real openSUSE Leap 16.0 (zypper
// 1.14.101, rpm 4.20.1) in the lab, and every fixture under
// testdata/zypper was captured there. DIVERGENCE 5.176.
//
// # What it reads, and how
//
// zypper has a machine-readable mode, `-x`/`--xmlout`, and every read here that
// zypper answers uses it: `search`, `list-updates`, `locks` and `repos`.
// The installed set comes from `rpm -qa --queryformat`, as the dnf
// provider's does, because rpm's database is what "installed" means on
// both families and zypper's own listing of it is a table.
//
// The XML is a *stream*, not a document about one thing: progress
// messages arrive as `<message type="info">` elements beside the answer
// ("Refreshing service 'openSUSE'." precedes nearly everything), errors
// about one repository arrive as `<message type="error">` beside a
// perfectly good answer from the others, and the odd line of plain text
// ("package 'x' not found.") is written into the middle of it. So the
// parser walks the stream and picks out the elements it knows, rather
// than decoding it into one struct.
//
// # Exit codes are part of the answer
//
// zypper(8) -- the page installed on the lab machine, read there -- gives
// 100 to 107 as informational codes, and three of them change what a call
// means. The behaviour below each is measured, not read:
//
//   - 104, nothing matched the name. For `search` that is an answer
//     ("there is no such package"); for `install` it is a failure --
//     `zypper install --name nosuchpkg` exits 104 and installs nothing,
//     and `zypper install --name tree=9.9` says the same about a version
//     no repository has.
//   - 106, some repositories were skipped because they could not be
//     refreshed. Measured with one unreachable repository among good
//     ones: `search` and `list-updates` exit 106 *with the full answer
//     from the others*, and `install` exits 106 *having installed the
//     package*. Treating 106 as a failure would make every state on a
//     node with one dead mirror fail while doing its job; treating it as
//     success without looking would report a package that the skipped
//     repository was the only source of. So a mutation that exits 106 is
//     checked against rpm afterwards, and the answer is what rpm says.
//   - 107, an rpm scriptlet failed. The transaction went through and the
//     package is on disk, half configured. That is reported as a failure,
//     naming the scriptlet, because it is one; the next run finds the
//     package present and has nothing to do.
//
// 100 to 103 are success, on the page's word rather than a measurement:
// the first two are `patch-check`'s "patches are available", and 102 and
// 103 follow a successful install of a patch that wants a reboot or wants
// zypper run again. None of the four was seen on the lab machine. 105 is
// "killed by a signal" and is a failure. `zypper refresh` does not use 106
// at all: the same unreachable repository makes it exit 4.
//
// This is the trap DIVERGENCE 5.113 found in `mac_defaults`: the OS runner
// turns any non-zero exit into an error unless the command asks for its
// exit code, and the recording runner the unit tests use does not, so a
// branch on `res.Code` can pass every test and never run on a machine.
// Every zypper command here goes through runZypper, which asks, and
// `TestZypperCommandsAskForTheirExitCode` holds that.
//
// # Flags, and the ones left out
//
//   - `--non-interactive` on every call. Without it a solver problem
//     waits on a terminal nobody is at.
//   - `--name` on install and remove, so a name is a package name and not
//     a capability. Without it `zypper install foo` installs whatever
//     *provides* foo, `list_pkgs` never shows a package called foo, and
//     `pkg.installed` installs it again on every run.
//   - `--auto-agree-with-licenses` on install and update, which is what
//     Salt's zypper module passes. A package whose licence needs
//     confirming otherwise aborts a non-interactive install, and a state
//     naming the package is the operator's decision to install it.
//   - `--oldpackage` whenever a version is pinned. Without it a pin
//     below the installed version is not an error: zypper says the
//     selected package "has lower version than the installed one", does
//     nothing and exits **0** -- so `pkg.installed` would report success
//     and never converge. Measured, and it is the reason the flag is
//     unconditional rather than computed.
//   - **Not** `--gpg-auto-import-keys`. It trusts whatever key a
//     repository offers, which is the one decision a package manager
//     exists to make carefully; a repository whose key is not yet trusted
//     should fail loudly until somebody trusts it.
//   - **Not** `--no-refresh` on install or upgrade. zypper's autorefresh
//     is its own policy for when metadata is stale, and forbidding it is
//     how a node ends up fetching an RPM its mirror deleted last week --
//     the shape of the dnf `-C` defect (DIVERGENCE 5.157) in zypper's
//     vocabulary. Reads that download nothing (`list_upgrades` without
//     `refresh`) do take it.
type zypperProvider struct{}

func (zypperProvider) Name() string { return "zypperpkg" }

// Available asks for zypper and rpm on the path.
//
// By binary, like every provider here, rather than by the `os_family`
// grain: the live tests' contexts carry the unit suite's fixed grains, and
// a provider keyed on them would be unreachable on the very host it was
// written for. The order in pkgProviders does the rest -- see the comment
// there.
func (zypperProvider) Available(c *exec.Context) bool {
	return c.Which("zypper") != "" && c.Which("rpm") != ""
}

const (
	zypperExitNotFound        = 104
	zypperExitReposSkipped    = 106
	zypperExitScriptletFailed = 107
)

// zypperSucceeded is zypper(8)'s own list of the codes that mean it did
// what it was asked: zero, and the four that add a note (patches
// pending, security patches pending, reboot suggested, run zypper again
// for the rest of a patch).
func zypperSucceeded(code int) bool {
	return code == 0 || (code >= 100 && code <= 103)
}

// runZypper runs zypper non-interactively and turns its exit code into an
// error, except for the codes named in `answers`, which the caller reads
// for itself.
//
// global options go before the command, which zypper requires.
func runZypper(c *exec.Context, global, args []string, answers ...int) (exec.Result, error) {
	return runZypperArgv(c, zypperArgv(false, global, args), answers...)
}

// zypperXML is runZypper with `-x`, zypper's `--xmlout`.
func zypperXML(c *exec.Context, global, args []string, answers ...int) (exec.Result, error) {
	return runZypperArgv(c, zypperArgv(true, global, args), answers...)
}

// zypperArgv spells a command exactly as the fixtures' capture script did,
// `zypper [-x] --non-interactive [global...] command [args...]`, so that
// each fixture's recorded argv is the key a test serves it under and a
// command that drifts from what was captured finds nothing to answer it.
func zypperArgv(xmlOut bool, global, args []string) []string {
	argv := []string{"zypper"}
	if xmlOut {
		argv = append(argv, "-x")
	}
	argv = append(argv, "--non-interactive")
	argv = append(argv, global...)
	return append(argv, args...)
}

func runZypperArgv(c *exec.Context, argv []string, answers ...int) (exec.Result, error) {
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return res, err
	}
	if zypperSucceeded(res.Code) {
		return res, nil
	}
	for _, a := range answers {
		if res.Code == a {
			return res, nil
		}
	}
	return res, zypperError(argv, res)
}

// zypperError says what zypper said.
//
// zypper writes a solver problem to stdout and not stderr -- `remove` of a
// locked package exits 4 with stderr empty and "Problem: ... remove lock
// to allow removal" on stdout -- so an error built from stderr alone would
// be "exited 4" and nothing else. The tail of stdout stands in when stderr
// is empty, and in `--xmlout` mode the error messages are pulled out of
// the stream.
func zypperError(argv []string, res exec.Result) error {
	meaning := ""
	switch res.Code {
	case zypperExitNotFound:
		meaning = " (nothing matched the name)"
	case zypperExitReposSkipped:
		meaning = " (a repository could not be refreshed)"
	case zypperExitScriptletFailed:
		meaning = " (the transaction completed, and an rpm scriptlet failed)"
	case 4:
		meaning = " (zypper reported a problem)"
	case 7:
		meaning = " (another process holds the zypp lock)"
	}
	detail := strings.TrimSpace(res.Stderr)
	if detail == "" {
		if errs := zypperStreamMessages(res.Stdout, "error"); len(errs) > 0 {
			detail = strings.Join(errs, "; ")
		}
	}
	if detail == "" {
		detail = lastLines(res.Stdout, 6)
	}
	cmd := strings.Join(argv, " ")
	if detail == "" {
		return fmt.Errorf("`%s` exited %d%s", cmd, res.Code, meaning)
	}
	return fmt.Errorf("`%s` exited %d%s: %s", cmd, res.Code, meaning, detail)
}

func lastLines(s string, n int) string {
	var kept []string
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			kept = append(kept, ln)
		}
	}
	if len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	return strings.Join(kept, " / ")
}

// ---- the XML stream ----

type zypperSolvable struct {
	Status  string `xml:"status,attr"`
	Name    string `xml:"name,attr"`
	Kind    string `xml:"kind,attr"`
	Edition string `xml:"edition,attr"`
	Arch    string `xml:"arch,attr"`
	Repo    string `xml:"repository,attr"`
}

type zypperUpdate struct {
	Kind       string `xml:"kind,attr"`
	Name       string `xml:"name,attr"`
	Edition    string `xml:"edition,attr"`
	EditionOld string `xml:"edition-old,attr"`
}

type zypperRepo struct {
	Alias       string `xml:"alias,attr"`
	Name        string `xml:"name,attr"`
	Type        string `xml:"type,attr"`
	Priority    string `xml:"priority,attr"`
	Enabled     string `xml:"enabled,attr"`
	Autorefresh string `xml:"autorefresh,attr"`
	GPGCheck    string `xml:"gpgcheck,attr"`
	URL         string `xml:"url"`
}

type zypperLock struct {
	Name string `xml:"name"`
	Type string `xml:"type"`
}

type zypperMessage struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

// zypperStream walks an `--xmlout` stream and decodes each element named
// in `into` with its function, ignoring everything else: messages,
// progress, the wrapping elements, and stray text.
func zypperStream(stdout string, into map[string]func(*xml.Decoder, xml.StartElement) error) error {
	d := xml.NewDecoder(strings.NewReader(stdout))
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("zypper's XML output could not be read: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if decode, wanted := into[start.Name.Local]; wanted {
			if err := decode(d, start); err != nil {
				return fmt.Errorf("zypper's <%s> could not be read: %w", start.Name.Local, err)
			}
		}
	}
}

// zypperStreamMessages returns the text of every <message> of one type,
// for an error that has nothing on stderr to say.
func zypperStreamMessages(stdout, kind string) []string {
	if !strings.Contains(stdout, "<stream>") {
		return nil
	}
	var out []string
	_ = zypperStream(stdout, map[string]func(*xml.Decoder, xml.StartElement) error{
		"message": func(d *xml.Decoder, s xml.StartElement) error {
			var m zypperMessage
			if err := d.DecodeElement(&m, &s); err != nil {
				return err
			}
			if m.Type == kind {
				out = append(out, strings.Join(strings.Fields(m.Text), " "))
			}
			return nil
		},
	})
	return out
}

func zypperSolvables(stdout string) ([]zypperSolvable, error) {
	var out []zypperSolvable
	err := zypperStream(stdout, map[string]func(*xml.Decoder, xml.StartElement) error{
		"solvable": func(d *xml.Decoder, s xml.StartElement) error {
			var v zypperSolvable
			if err := d.DecodeElement(&v, &s); err != nil {
				return err
			}
			out = append(out, v)
			return nil
		},
	})
	return out, err
}

func zypperUpdates(stdout string) ([]zypperUpdate, error) {
	var out []zypperUpdate
	err := zypperStream(stdout, map[string]func(*xml.Decoder, xml.StartElement) error{
		"update": func(d *xml.Decoder, s xml.StartElement) error {
			var v zypperUpdate
			if err := d.DecodeElement(&v, &s); err != nil {
				return err
			}
			out = append(out, v)
			return nil
		},
	})
	return out, err
}

func zypperRepos(stdout string) ([]zypperRepo, error) {
	var out []zypperRepo
	err := zypperStream(stdout, map[string]func(*xml.Decoder, xml.StartElement) error{
		"repo": func(d *xml.Decoder, s xml.StartElement) error {
			var v zypperRepo
			if err := d.DecodeElement(&v, &s); err != nil {
				return err
			}
			out = append(out, v)
			return nil
		},
	})
	return out, err
}

func zypperLocks(stdout string) ([]zypperLock, error) {
	var out []zypperLock
	err := zypperStream(stdout, map[string]func(*xml.Decoder, xml.StartElement) error{
		"lock": func(d *xml.Decoder, s xml.StartElement) error {
			var v zypperLock
			if err := d.DecodeElement(&v, &s); err != nil {
				return err
			}
			out = append(out, v)
			return nil
		},
	})
	return out, err
}

// ---- pkgProvider ----

// ListPkgs reads rpm's database.
//
// # Two of one package is normal here
//
// SUSE installs kernels side by side, so the lab machine had
// `kernel-default` at 6.12.0-160000.35.1 *and* 6.12.0-160000.37.1, and
// `-extra` and `-optional` likewise. A map keyed by name has room for
// one, and taking whichever rpm printed last is taking whatever order its
// database happens to be in. The newest by rpm's own ordering is kept:
// it is the one an upgrade moves, and the one `pkg.latest` compares the
// repository's newest against, so a node whose older kernel is still
// installed does not report an upgrade it already did.
func (zypperProvider) ListPkgs(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{
		Argv: []string{"rpm", "-qa", "--queryformat", "%{NAME}\\t%{EPOCH}:%{VERSION}-%{RELEASE}\\n"},
	})
	if err != nil {
		return nil, err
	}
	return parseRPMInstalledNewest(res.Stdout), nil
}

func parseRPMInstalledNewest(stdout string) *value.Map {
	out := value.NewMap(512)
	for _, line := range strings.Split(stdout, "\n") {
		name, version, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		// "(none)" is rpm's missing epoch and zero is conventionally
		// omitted; either kept literally makes every comparison fail.
		version = strings.TrimPrefix(version, "(none):")
		version = strings.TrimPrefix(version, "0:")
		if prev, seen := out.Get(name); seen && CompareRPM(value.KeyString(prev), version) >= 0 {
			continue
		}
		out.Set(name, version)
	}
	return out
}

// Install installs or upgrades the named packages.
func (p zypperProvider) Install(c *exec.Context, names []string, versions map[string]string, refresh bool) error {
	if refresh {
		if err := p.RefreshDB(c); err != nil {
			return err
		}
	}
	args := []string{"install", "--auto-agree-with-licenses"}
	pinned := false
	var specs []string
	for _, n := range names {
		if v, ok := versions[n]; ok && v != "" && !strings.HasSuffix(v, "*") {
			specs = append(specs, n+"="+v)
			pinned = true
			continue
		}
		specs = append(specs, n)
	}
	if pinned {
		args = append(args, "--oldpackage")
	}
	args = append(args, "--name")
	args = append(args, specs...)

	res, err := runZypper(c, nil, args, zypperExitReposSkipped)
	if err != nil {
		return err
	}
	if res.Code != zypperExitReposSkipped {
		return nil
	}
	return p.confirm(c, args, names, true, res)
}

// Remove removes the named packages. zypper has no purge: rpm removes a
// package's unmodified configuration with it and keeps a modified one as
// `.rpmsave` either way, so `purge` asks for the same thing `remove`
// does, as Salt's zypper module also does.
//
// 104 is read rather than refused: `zypper remove --name tree nosuch`
// removes tree, says "Package 'nosuch' not found" and exits 104, so the
// code alone cannot say whether what was asked for is now true. rpm can.
func (p zypperProvider) Remove(c *exec.Context, names []string, purge bool) error {
	args := append([]string{"remove", "--name"}, names...)
	res, err := runZypper(c, nil, args, zypperExitReposSkipped, zypperExitNotFound)
	if err != nil {
		return err
	}
	if res.Code == 0 {
		return nil
	}
	return p.confirm(c, args, names, false, res)
}

// confirm settles an exit code that does not say whether the packages
// ended up where they were asked to be, by asking rpm.
func (p zypperProvider) confirm(c *exec.Context, args, names []string, wantInstalled bool, res exec.Result) error {
	installed, err := p.ListPkgs(c)
	if err != nil {
		return err
	}
	var wrong []string
	for _, n := range names {
		if installed.Has(n) != wantInstalled {
			wrong = append(wrong, n)
		}
	}
	if len(wrong) == 0 {
		return nil
	}
	state := "not installed"
	if !wantInstalled {
		state = "still installed"
	}
	return fmt.Errorf("%w; and rpm says %s %s", zypperError(zypperArgv(false, nil, args), res),
		strings.Join(wrong, ", "), state)
}

// LatestVersion is the newest edition any enabled repository offers, or
// the empty string when that edition is already installed.
//
// `search --match-exact -s` lists every edition of the name, installed
// ones marked `installed`, the rest `not-installed` or `other-version`.
// The newest is chosen by rpm's comparison and not by position: the
// kernel's list is newest first, but nothing promises that. Two installed
// kernels are two `installed` rows, and "already at the newest" means the
// newest row is one of them.
//
// Not covered: repository priority and vendor stickiness, either of
// which can make zypper install something other than the highest
// edition. Every repository on the lab machine had the same priority,
// which zypper itself remarks on.
func (zypperProvider) LatestVersion(c *exec.Context, name string) (string, error) {
	res, err := zypperXML(c, nil,
		[]string{"search", "-s", "--match-exact", "-t", "package", name},
		zypperExitNotFound, zypperExitReposSkipped)
	if err != nil {
		return "", err
	}
	if res.Code == zypperExitNotFound {
		return "", nil
	}
	solvables, err := zypperSolvables(res.Stdout)
	if err != nil {
		return "", err
	}
	return newestZypperEdition(solvables, name), nil
}

func newestZypperEdition(solvables []zypperSolvable, name string) string {
	newest, newestInstalled := "", false
	for _, s := range solvables {
		if s.Name != name || s.Kind != "package" || s.Arch == "src" || s.Arch == "nosrc" {
			continue
		}
		switch cmp := CompareRPM(s.Edition, newest); {
		case newest == "" || cmp > 0:
			newest, newestInstalled = s.Edition, s.Status == "installed"
		case cmp == 0 && s.Status == "installed":
			newestInstalled = true
		}
	}
	if newestInstalled {
		return ""
	}
	return strings.TrimPrefix(newest, "0:")
}

// RefreshDB is `zypper refresh`, which checks every enabled repository
// and fetches what changed. One unreachable repository makes it exit 4
// having refreshed the rest, and that is reported: a caller that asked for
// fresh metadata did not get all of it.
func (zypperProvider) RefreshDB(c *exec.Context) error {
	_, err := runZypper(c, nil, []string{"refresh"})
	return err
}

// ---- the optional interfaces ----

// Hold is a zypper lock. A locked package is not upgraded, installed or
// removed -- `zypper remove` of one exits 4 with "remove lock to allow
// removal" -- and a lock may name a package that is not installed, which
// then cannot be.
func (zypperProvider) Hold(c *exec.Context, name string) error {
	_, err := runZypper(c, nil, []string{"addlock", name})
	return err
}

// Unhold removes the lock. Removing a lock that is not there says "No
// lock has been removed." and exits 0, which is the outcome asked for.
func (zypperProvider) Unhold(c *exec.Context, name string) error {
	_, err := runZypper(c, nil, []string{"removelock", name})
	return err
}

// ListHolds is every package lock, by the name it was made with.
func (zypperProvider) ListHolds(c *exec.Context) ([]string, error) {
	res, err := zypperXML(c, nil, []string{"locks"})
	if err != nil {
		return nil, err
	}
	locks, err := zypperLocks(res.Stdout)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range locks {
		if l.Type != "" && l.Type != "package" {
			continue
		}
		out = append(out, strings.TrimSpace(l.Name))
	}
	sort.Strings(out)
	return out, nil
}

// Upgrade is `zypper update`: the installed packages to their newest
// versions, and not `dist-upgrade`, which may also change vendors and
// remove packages. 106 is refused here rather than confirmed, because
// there is nothing to confirm it against: an upgrade that skipped a
// repository is an upgrade against part of the metadata.
func (p zypperProvider) Upgrade(c *exec.Context, refresh bool) (*value.Map, error) {
	before, err := p.ListPkgs(c)
	if err != nil {
		return nil, err
	}
	if refresh {
		if err := p.RefreshDB(c); err != nil {
			return nil, err
		}
	}
	if _, err := runZypper(c, nil, []string{"update", "--auto-agree-with-licenses"}); err != nil {
		return nil, err
	}
	return pkgDelta(c, p, before)
}

// ListUpgrades is `list-updates`, name to the edition it would move to.
// Without `refresh` it runs from the metadata already held, which for a
// read downloads nothing and so is exactly what "do not refresh" means.
func (zypperProvider) ListUpgrades(c *exec.Context, refresh bool) (*value.Map, error) {
	var global []string
	if !refresh {
		global = []string{"--no-refresh"}
	}
	res, err := zypperXML(c, global, []string{"list-updates"}, zypperExitReposSkipped)
	if err != nil {
		return nil, err
	}
	updates, err := zypperUpdates(res.Stdout)
	if err != nil {
		return nil, err
	}
	out := value.NewMap(len(updates))
	for _, u := range updates {
		if u.Kind != "" && u.Kind != "package" {
			continue
		}
		out.Set(u.Name, strings.TrimPrefix(u.Edition, "0:"))
	}
	return out, nil
}

// FileList and OwnerOf ask rpm, as the dnf provider does.
func (zypperProvider) FileList(c *exec.Context, name string) ([]string, error) {
	return dnfProvider{}.FileList(c, name)
}

func (zypperProvider) OwnerOf(c *exec.Context, path string) (string, error) {
	return dnfProvider{}.OwnerOf(c, path)
}

// ListRepos is every configured repository, enabled or not, by alias.
// Exit 6 is zypper's "no repositories are defined", which is an answer.
func (zypperProvider) ListRepos(c *exec.Context) (*value.Map, error) {
	res, err := zypperXML(c, nil, []string{"repos"}, 6)
	if err != nil {
		return nil, err
	}
	repos, err := zypperRepos(res.Stdout)
	if err != nil {
		return nil, err
	}
	out := value.NewMap(len(repos))
	for _, r := range repos {
		m := value.NewMap(8)
		m.Set("name", r.Name)
		m.Set("enabled", r.Enabled == "1")
		m.Set("autorefresh", r.Autorefresh == "1")
		m.Set("gpgcheck", r.GPGCheck == "1")
		if n, err := strconv.ParseInt(r.Priority, 10, 64); err == nil {
			m.Set("priority", n)
		}
		if u := strings.TrimSpace(r.URL); u != "" {
			m.Set("baseurl", u)
		}
		// Absent on a repository that has never been refreshed: zypper
		// learns the type from the metadata it has not yet fetched.
		if r.Type != "" {
			m.Set("type", r.Type)
		}
		out.Set(r.Alias, m)
	}
	return out, nil
}
