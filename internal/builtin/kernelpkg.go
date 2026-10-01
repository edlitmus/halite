package builtin

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerKernelpkg installs SPEC 15.2's and 15.5's `kernelpkg`: Salt's
// kernelpkg_linux_apt and kernelpkg_linux_yum behind one name, and the
// three states over them.
//
// # One question, asked of the package manager that installed the kernel
//
// Every function here is a question about two lists -- the kernels the
// package database holds and the kernels the repositories offer -- and
// one fact the kernel itself reports, the release it is running. The
// family is chosen the way `pkg` chooses its provider, so a node that
// `pkg` manages with apt has its kernels read from dpkg, and one it
// manages with dnf or yum from rpm. Any other package manager is refused
// by name: Salt's module loads on Debian and RedHat families only.
//
// # Ordered by the package manager, not by the release string
//
// Salt sorts releases with LooseVersion, which is a guess about strings
// that the package managers already have an exact answer to. Here every
// ordering is the package manager's own -- dpkg's for an image package's
// Version, rpm's for a kernel's EVR -- through the comparisons
// `pkg.version_cmp` uses (DIVERGENCE 5.177 for rpm's).
//
// # What Debian 13 actually names its kernels
//
// Measured on the lab's Debian 13.7, not read from Salt:
//
//	uname -r                 6.12.107+deb13-amd64
//	the image package        linux-image-6.12.107+deb13-amd64   6.12.107-1
//	its unsigned twin        linux-image-6.12.107+deb13-amd64-unsigned
//	the metapackage          linux-image-amd64                   6.12.111-1
//	                         Depends: linux-image-6.12.111+deb13-amd64 (= 6.12.111-1)
//
// Salt's apt module finds none of this. Its installed-kernel pattern is
// `^linux-image-[\d.-]+-<type>$`, which stops at the `+`; and its
// `latest_available` reads the metapackage's version with
// `^(\d+\.\d+\.\d+)\.(\d+)`, which is Ubuntu's `5.15.0.91.88` spelling
// and matches nothing Debian ships. Both were applied, as written, to
// these names with the host's own python3 (DIVERGENCE 5.187).
//
// So the release a kernel will report is read from the *package name*,
// which on Debian is `linux-image-` and exactly that string, and the
// newest available is the image the metapackage's candidate depends on.
//
// # Removal is simulated first, because it is not what it says
//
// On Debian 13 `apt-get purge linux-image-6.12.107+deb13-amd64` does not
// remove that kernel. The release's headers depend on `linux-image-R |
// linux-image-R-unsigned`, so apt satisfies them by *installing the
// unsigned twin* -- the same kernel, back in /boot, under another name --
// and reports success. Purging the newest kernel also removes
// `linux-image-amd64` and `linux-headers-amd64`, the two packages through
// which the node receives every later kernel. Both were measured with
// `apt-get -s`. So removal here names the image, its twin and every installed package of that release together (debianRemovalTargets),
// asks apt to simulate it first, and refuses when the simulation would
// install anything or remove a package that does not belong to that
// release, naming them.
//
// # Nothing here reboots unasked
//
// `needs_reboot` only reports. `upgrade reboot=True` and the
// `latest_active` state schedule one through `reboot.schedule`, which
// has no immediate form: Salt's `at_time` of None means "now" to
// `system.reboot`, and here it means `reboot.schedule`'s default delay,
// so there is always a window in which `reboot.cancel` works.
func registerKernelpkg(r *Registries) {
	r.Exec.Add(kernelpkgExecModules()...)
	r.States.Add(kernelpkgStateModules()...)
}

var kernelpkgPlatforms = []string{"linux"}

// kernelImage is one kernel, installed or available.
type kernelImage struct {
	// Release is what `uname -r` prints when this kernel is running, in
	// the spelling Salt's functions return: Debian's whole release, and
	// EL's without the architecture (Salt's pkg.normalize_name).
	Release string
	// Packages are the package names this kernel is installed as.
	Packages []string
	// Version is the package manager's version, the ordering key.
	Version string
}

// kernelFamily is one package manager's view of the kernels.
type kernelFamily interface {
	name() string
	// active is the running kernel's release in this family's spelling.
	active(c *exec.Context) (string, error)
	// installed lists the installed kernels of the running kernel's kind,
	// oldest first.
	installed(c *exec.Context) ([]kernelImage, error)
	// available is the newest kernel the repositories offer, and false
	// when they offer none this module can identify.
	available(c *exec.Context) (kernelImage, bool, error)
	compare(a, b string) int
	install(c *exec.Context, k kernelImage) error
	// removal reports what removing k would remove, or why it is
	// refused. It changes nothing.
	removal(c *exec.Context, k kernelImage) ([]string, error)
	remove(c *exec.Context, k kernelImage, packages []string) error
}

func pickKernelFamily(c *exec.Context) (kernelFamily, error) {
	p, err := pickPkgProvider(c)
	if err != nil {
		return nil, err
	}
	switch v := p.(type) {
	case aptProvider:
		return aptKernels{}, nil
	case dnfProvider:
		return rpmKernels{binary: v.binary}, nil
	}
	return nil, fmt.Errorf("kernelpkg reads kernels through apt or dnf/yum, as Salt's kernelpkg_linux_apt "+
		"and kernelpkg_linux_yum do, and this node's package manager is %s", p.Name())
}

// ---- the functions, family-independent ----

type kernelState struct {
	family    kernelFamily
	active    string
	installed []kernelImage
}

func readKernels(c *exec.Context) (*kernelState, error) {
	f, err := pickKernelFamily(c)
	if err != nil {
		return nil, err
	}
	active, err := f.active(c)
	if err != nil {
		return nil, err
	}
	installed, err := f.installed(c)
	if err != nil {
		return nil, err
	}
	return &kernelState{family: f, active: active, installed: installed}, nil
}

func (s *kernelState) releases() []any {
	out := make([]any, 0, len(s.installed))
	for _, k := range s.installed {
		out = append(out, k.Release)
	}
	return out
}

func (s *kernelState) latest() (kernelImage, bool) {
	if len(s.installed) == 0 {
		return kernelImage{}, false
	}
	return s.installed[len(s.installed)-1], true
}

func (s *kernelState) find(release string) (kernelImage, bool) {
	for _, k := range s.installed {
		if k.Release == release {
			return k, true
		}
	}
	return kernelImage{}, false
}

// latestAvailable is the repositories' newest, or the newest installed
// when they offer none, which is what Salt returns in that case.
func (s *kernelState) latestAvailable(c *exec.Context) (kernelImage, bool, error) {
	k, ok, err := s.family.available(c)
	if err != nil {
		return kernelImage{}, false, err
	}
	if ok {
		return k, true, nil
	}
	k, ok = s.latest()
	return k, ok, nil
}

// needsReboot reports whether a newer kernel than the running one is
// installed.
//
// The running kernel has to be one the package database holds, because
// its version is what the comparison needs. A container, which runs its
// host's kernel, and a hand-built kernel are both refused rather than
// answered: `false` would be read as "this node is current".
func (s *kernelState) needsReboot() (bool, error) {
	running, ok := s.find(s.active)
	if !ok {
		return false, fmt.Errorf("the running kernel, %s, is not one this node's %s database holds "+
			"(a container runs its host's kernel, and a kernel built by hand is not a package), so "+
			"whether a newer one is installed cannot be told", s.active, s.family.name())
	}
	latest, _ := s.latest()
	return s.family.compare(latest.Version, running.Version) > 0, nil
}

// upgradeAvailable reports whether the repositories offer a kernel newer
// than every installed one.
func (s *kernelState) upgradeAvailable(c *exec.Context) (bool, kernelImage, error) {
	avail, ok, err := s.family.available(c)
	if err != nil || !ok {
		return false, avail, err
	}
	latest, have := s.latest()
	if !have {
		return true, avail, nil
	}
	return s.family.compare(avail.Version, latest.Version) > 0, avail, nil
}

func kernelpkgActive(c *exec.Context) (any, error) {
	f, err := pickKernelFamily(c)
	if err != nil {
		return nil, err
	}
	return f.active(c)
}

func kernelpkgListInstalled(c *exec.Context) (any, error) {
	s, err := readKernels(c)
	if err != nil {
		return nil, err
	}
	return s.releases(), nil
}

func kernelpkgLatestInstalled(c *exec.Context) (any, error) {
	s, err := readKernels(c)
	if err != nil {
		return nil, err
	}
	if k, ok := s.latest(); ok {
		return k.Release, nil
	}
	return nil, nil
}

func kernelpkgLatestAvailable(c *exec.Context) (any, error) {
	s, err := readKernels(c)
	if err != nil {
		return nil, err
	}
	k, ok, err := s.latestAvailable(c)
	if err != nil || !ok {
		return nil, err
	}
	return k.Release, nil
}

func kernelpkgNeedsReboot(c *exec.Context) (any, error) {
	s, err := readKernels(c)
	if err != nil {
		return nil, err
	}
	return s.needsReboot()
}

func kernelpkgUpgradeAvailable(c *exec.Context) (any, error) {
	s, err := readKernels(c)
	if err != nil {
		return nil, err
	}
	up, _, err := s.upgradeAvailable(c)
	return up, err
}

// kernelpkgUpgrade installs the newest kernel the repositories offer
// beside the others, as Salt's does, and schedules a reboot only when
// asked and only when one is needed.
func kernelpkgUpgrade(c *exec.Context, reboot bool, atTime int64) (any, error) {
	s, err := readKernels(c)
	if err != nil {
		return nil, err
	}
	up, avail, err := s.upgradeAvailable(c)
	if err != nil {
		return nil, err
	}
	upgrades := value.NewMap(1)
	if up {
		for _, p := range avail.Packages {
			upgrades.Set(p, states.Change("", avail.Version))
		}
		if !c.Test {
			if err := s.family.install(c, avail); err != nil {
				return nil, err
			}
			if s, err = readKernels(c); err != nil {
				return nil, err
			}
			if _, ok := s.find(avail.Release); !ok {
				return nil, fmt.Errorf("%s was installed and the package database does not list kernel %s",
					strings.Join(avail.Packages, " "), avail.Release)
			}
		}
	}
	out := value.NewMap(6)
	out.Set("upgrades", upgrades)
	out.Set("active", s.active)
	latest := any(nil)
	if k, ok := s.latest(); ok {
		latest = k.Release
	}
	if c.Test && up {
		latest = avail.Release
	}
	out.Set("latest_installed", latest)
	out.Set("reboot_requested", reboot)
	need := false
	if c.Test && up {
		need = avail.Release != s.active
	} else if need, err = s.needsReboot(); err != nil {
		// The install is done and is not undone by not knowing this: in
		// an EL container on the fleet's rpm legs the running kernel is
		// the runner's, and this was an error there after a successful
		// install. Unknown is reported as unknown, and only a reboot that
		// was asked for turns it into a failure.
		if reboot {
			return nil, fmt.Errorf("no reboot was scheduled: %w", err)
		}
		out.Set("reboot_required", nil)
		out.Set("reboot_required_comment", err.Error())
		return out, nil
	}
	out.Set("reboot_required", need)
	if reboot && need {
		r, err := rebootSchedule(c, atTime, "kernelpkg: booting kernel "+value.KeyString(latest))
		if err != nil {
			return nil, err
		}
		out.Set("reboot", r)
	}
	return out, nil
}

// kernelpkgRemove removes one installed kernel that is not running.
func kernelpkgRemove(c *exec.Context, release string) (any, error) {
	s, err := readKernels(c)
	if err != nil {
		return nil, err
	}
	removed, err := removeKernel(c, s, release)
	if err != nil {
		return nil, err
	}
	return value.MapOf("removed", removed), nil
}

func removeKernel(c *exec.Context, s *kernelState, release string) ([]any, error) {
	release = strings.TrimSpace(release)
	k, ok := s.find(release)
	if !ok {
		return nil, fmt.Errorf("kernel release %q is not installed; installed: %s",
			release, value.KeyString(s.releases()))
	}
	if release == s.active {
		return nil, fmt.Errorf("kernel %s is the running kernel and cannot be removed", release)
	}
	pkgs, err := s.family.removal(c, k)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, p)
	}
	if c.Test {
		return out, nil
	}
	if err := s.family.remove(c, k, pkgs); err != nil {
		return nil, err
	}
	after, err := readKernels(c)
	if err != nil {
		return nil, err
	}
	if _, still := after.find(release); still {
		return nil, fmt.Errorf("%s was removed and the package database still lists kernel %s",
			strings.Join(pkgs, " "), release)
	}
	return out, nil
}

// kernelpkgCleanup removes every installed kernel but the running one
// and, by default, the newest.
//
// Every removal is planned before any is made, so a refusal of the third
// does not leave the first two gone and a report that says only "error".
func kernelpkgCleanup(c *exec.Context, keepLatest bool) (any, error) {
	s, err := readKernels(c)
	if err != nil {
		return nil, err
	}
	if _, ok := s.find(s.active); !ok {
		return nil, fmt.Errorf("the running kernel, %s, is not one this node's %s database holds, so "+
			"which installed kernel is safe to remove cannot be told", s.active, s.family.name())
	}
	latest, _ := s.latest()
	var targets []kernelImage
	for _, k := range s.installed {
		if k.Release == s.active || (keepLatest && k.Release == latest.Release) {
			continue
		}
		if _, err := s.family.removal(c, k); err != nil {
			return nil, fmt.Errorf("nothing was removed: %w", err)
		}
		targets = append(targets, k)
	}
	removed := []any{}
	for _, k := range targets {
		got, err := removeKernel(c, s, k.Release)
		if err != nil {
			return value.MapOf("removed", removed), err
		}
		removed = append(removed, got...)
	}
	return value.MapOf("removed", removed), nil
}

// ---- Debian and Ubuntu: dpkg and apt ----

type aptKernels struct{}

func (aptKernels) name() string { return "dpkg" }

func (aptKernels) active(c *exec.Context) (string, error) { return kernelRelease(c) }

func (aptKernels) compare(a, b string) int { return CompareDebian(a, b) }

// debianReleaseRE splits a Debian or Ubuntu kernel release into its
// version-and-ABI and its flavour: `6.12.107+deb13` and `amd64` on Debian
// 13, `6.1.0-28` and `cloud-amd64` on Debian 12, `5.15.0-91` and
// `generic` on Ubuntu. Only Debian 13's spelling was measured; the other
// two are the shape of their own package names.
var debianReleaseRE = regexp.MustCompile(`^([0-9]+\.[0-9]+(?:\.[0-9]+)?(?:\+[a-z]+[0-9]*(?:\.[0-9]+)*|-[0-9]+(?:\.[0-9]+)*))-(.+)$`)

// debianFlavour is the part of a release a metapackage is named after.
func debianFlavour(release string) (string, bool) {
	m := debianReleaseRE.FindStringSubmatch(release)
	if m == nil {
		return "", false
	}
	return m[2], true
}

// debianImageRelease reads the release out of an image package's name,
// or false when the name is not a kernel image of that flavour: the
// metapackages (`linux-image-amd64`), the debug symbols (`-dbg`), and a
// different flavour's kernels are all `linux-image-*` too.
func debianImageRelease(pkg, flavour string) (string, bool) {
	rest, ok := strings.CutPrefix(pkg, "linux-image-")
	if !ok {
		return "", false
	}
	// Debian's unsigned twin is a suffix and Ubuntu's a prefix.
	rest = strings.TrimSuffix(rest, "-unsigned")
	rest = strings.TrimPrefix(rest, "unsigned-")
	f, ok := debianFlavour(rest)
	if !ok || f != flavour {
		return "", false
	}
	return rest, true
}

func (a aptKernels) flavour(c *exec.Context) (string, error) {
	active, err := a.active(c)
	if err != nil {
		return "", err
	}
	f, ok := debianFlavour(active)
	if !ok {
		return "", fmt.Errorf("the running kernel's release, %q, is not in a shape this module can read "+
			"a flavour from (Debian's 6.12.107+deb13-amd64 or 6.1.0-28-amd64, Ubuntu's 5.15.0-91-generic)", active)
	}
	return f, nil
}

func (a aptKernels) installed(c *exec.Context) ([]kernelImage, error) {
	flavour, err := a.flavour(c)
	if err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"dpkg-query", "-W", "-f=${Package}\\t${Version}\\t${Status}\\n", "linux-image-*"},
		Env:            aptEnv(),
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	// dpkg-query exits 1 when the pattern matches nothing, and says so.
	if res.Code == 1 && strings.Contains(res.Stderr, "no packages found") {
		return nil, nil
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("dpkg-query exited %d: %s", res.Code, strings.TrimSpace(firstLine(res.Stderr)))
	}
	return parseDebianImages(res.Stdout, flavour), nil
}

// parseDebianImages reads dpkg-query's listing into installed kernels of
// one flavour, oldest first by dpkg's ordering.
func parseDebianImages(stdout, flavour string) []kernelImage {
	byRelease := map[string]*kernelImage{}
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 3 || !strings.HasSuffix(fields[2], " installed") {
			continue
		}
		release, ok := debianImageRelease(fields[0], flavour)
		if !ok {
			continue
		}
		k := byRelease[release]
		if k == nil {
			k = &kernelImage{Release: release, Version: fields[1]}
			byRelease[release] = k
		}
		k.Packages = append(k.Packages, fields[0])
	}
	out := make([]kernelImage, 0, len(byRelease))
	for _, k := range byRelease {
		sort.Strings(k.Packages)
		out = append(out, *k)
	}
	sortKernels(out, CompareDebian)
	return out
}

func sortKernels(ks []kernelImage, cmp func(a, b string) int) {
	sort.SliceStable(ks, func(i, j int) bool {
		if c := cmp(ks[i].Version, ks[j].Version); c != 0 {
			return c < 0
		}
		return ks[i].Release < ks[j].Release
	})
}

// available reads the image the flavour's metapackage depends on, in the
// version apt would install.
func (a aptKernels) available(c *exec.Context) (kernelImage, bool, error) {
	flavour, err := a.flavour(c)
	if err != nil {
		return kernelImage{}, false, err
	}
	meta := "linux-image-" + flavour
	res, err := c.Run(exec.Command{
		Argv:           []string{"apt-cache", "show", "--no-all-versions", meta},
		Env:            aptEnv(),
		IgnoreExitCode: true,
	})
	if err != nil {
		return kernelImage{}, false, err
	}
	// apt-cache exits 100 with "E: No packages found" for a name no
	// repository knows.
	if res.Code != 0 {
		if strings.Contains(res.Stderr, "No packages found") {
			return kernelImage{}, false, nil
		}
		return kernelImage{}, false, fmt.Errorf("apt-cache show %s exited %d: %s", meta, res.Code,
			strings.TrimSpace(firstLine(res.Stderr)))
	}
	k, ok := parseDebianMetaDepends(res.Stdout, flavour)
	if !ok {
		return kernelImage{}, false, nil
	}
	if k.Version == "" {
		v, err := aptProvider{}.LatestVersion(c, k.Packages[0])
		if err != nil {
			return kernelImage{}, false, err
		}
		k.Version = v
	}
	return k, true, nil
}

// parseDebianMetaDepends finds the kernel image a metapackage's
// `apt-cache show` stanza depends on.
func parseDebianMetaDepends(stanza, flavour string) (kernelImage, bool) {
	depends := controlField(stanza, "Depends")
	for _, group := range strings.Split(depends, ",") {
		for _, alt := range strings.Split(group, "|") {
			fields := strings.Fields(alt)
			if len(fields) == 0 {
				continue
			}
			release, ok := debianImageRelease(fields[0], flavour)
			if !ok {
				continue
			}
			k := kernelImage{Release: release, Packages: []string{"linux-image-" + release}}
			rest := strings.Join(fields[1:], " ")
			if v, ok := strings.CutPrefix(rest, "(= "); ok {
				k.Version = strings.TrimSuffix(v, ")")
			}
			return k, true
		}
	}
	return kernelImage{}, false
}

// controlField reads one field of the first stanza of Debian control
// output, joining continuation lines.
func controlField(stanza, field string) string {
	var out []string
	in := false
	for _, line := range strings.Split(stanza, "\n") {
		if line == "" {
			if in || len(out) > 0 {
				break
			}
			continue
		}
		if in && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			out = append(out, strings.TrimSpace(line))
			continue
		}
		in = false
		if v, ok := strings.CutPrefix(line, field+":"); ok {
			out = append(out, strings.TrimSpace(v))
			in = true
		}
	}
	return strings.Join(out, " ")
}

func (aptKernels) install(c *exec.Context, k kernelImage) error {
	return aptProvider{}.Install(c, k.Packages[:1], nil, false)
}

// removal simulates purging the kernel and refuses what the simulation
// shows would go wrong; see registerKernelpkg for the two cases measured.
func (aptKernels) removal(c *exec.Context, k kernelImage) ([]string, error) {
	targets, err := debianRemovalTargets(c, k)
	if err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{
		Argv:           append([]string{"apt-get", "-s", "purge"}, targets...),
		Env:            aptEnv(),
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("apt-get -s purge %s exited %d: %s", strings.Join(targets, " "), res.Code,
			strings.TrimSpace(firstLine(res.Stderr)))
	}
	return checkDebianRemoval(res.Stdout, k.Release)
}

// debianRemovalTargets are every installed package of the kernel's own
// release -- the image, and on Ubuntu the `linux-modules-R` and
// `linux-modules-extra-R` that hold its modules and that purging the
// image leaves behind -- and the unsigned twins apt knows of.
//
// A twin is named even when it is not installed: apt then says it is not
// installed and does not remove it, and naming it is what stops apt
// installing it in the image's place (Debian 13). A twin apt does not
// know is not named, because apt refuses the whole command over one
// unknown name: Ubuntu 24.04 has no `linux-image-R-unsigned`, and
// naming it made `apt-get -s` exit 100 on the fleet's linux leg.
func debianRemovalTargets(c *exec.Context, k kernelImage) ([]string, error) {
	r := k.Release
	res, err := c.Run(exec.Command{
		Argv:           []string{"dpkg-query", "-W", "-f=${Package}\\t${Status}\\n", "linux-*-" + r},
		Env:            aptEnv(),
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var targets []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		name, status, ok := strings.Cut(line, "\t")
		if ok && strings.HasSuffix(status, " installed") && !seen[name] {
			seen[name] = true
			targets = append(targets, name)
		}
	}
	twins := []string{"linux-image-" + r + "-unsigned", "linux-image-unsigned-" + r}
	pol, err := c.Run(exec.Command{
		Argv:           append([]string{"apt-cache", "policy"}, twins...),
		Env:            aptEnv(),
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	for _, twin := range twins {
		if !seen[twin] && strings.Contains("\n"+pol.Stdout, "\n"+twin+":\n") {
			seen[twin] = true
			targets = append(targets, twin)
		}
	}
	for _, p := range k.Packages {
		if !seen[p] {
			targets = append(targets, p)
		}
	}
	sort.Strings(targets)
	return targets, nil
}

// checkDebianRemoval reads `apt-get -s` and returns the packages it
// would remove, or why the removal is refused.
func checkDebianRemoval(sim, release string) ([]string, error) {
	var removed, foreign, installs []string
	for _, line := range strings.Split(sim, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "Purg", "Remv":
			removed = append(removed, fields[1])
			if !strings.HasSuffix(fields[1], "-"+release) && !strings.HasSuffix(fields[1], "-"+release+"-unsigned") {
				foreign = append(foreign, fields[1])
			}
		case "Inst":
			installs = append(installs, fields[1])
		}
	}
	if len(installs) > 0 {
		return nil, fmt.Errorf("removing kernel %s would make apt install %s in its place, so the kernel "+
			"would not be removed; nothing was changed", release, strings.Join(installs, ", "))
	}
	if len(foreign) > 0 {
		return nil, fmt.Errorf("removing kernel %s would also remove %s, which do not belong to that "+
			"kernel (a metapackage is how this node receives later kernels); nothing was changed",
			release, strings.Join(foreign, ", "))
	}
	if len(removed) == 0 {
		return nil, fmt.Errorf("apt would remove nothing for kernel %s", release)
	}
	sort.Strings(removed)
	return removed, nil
}

// remove purges the same targets the simulation was given, asked for
// again rather than passed along: the simulation's own list includes
// what apt removes as a consequence, which is apt's to decide.
func (aptKernels) remove(c *exec.Context, k kernelImage, _ []string) error {
	targets, err := debianRemovalTargets(c, k)
	if err != nil {
		return err
	}
	_, err = c.Run(exec.Command{Argv: append([]string{"apt-get", "purge", "-y", "-q"}, targets...), Env: aptEnv()})
	return err
}

// ---- RHEL and its rebuilds: rpm and dnf or yum ----
//
// EL8 and later split the kernel: `kernel-core` is the image, `kernel`
// an empty package of the same version that pulls in `kernel-core` and
// `kernel-modules`, and all of them install side by side, one set per
// version. A kernel is counted by `kernel-core`, the package that holds
// /lib/modules/<release>/vmlinuz; Salt counts `kernel`, the empty one.
// A kernel is installed as
// `kernel`, as `dnf upgrade kernel` (Salt's `pkg.upgrade name=kernel`)
// would, so that its modules come with it.
//
// Every instance is read with `rpm -q`, never through `pkg.list_pkgs`,
// which keeps only the newest of a name (DIVERGENCE 5.177).

type rpmKernels struct{ binary string }

func (rpmKernels) name() string { return "rpm" }

func (rpmKernels) compare(a, b string) int { return CompareRPM(a, b) }

// rpmArches are the architecture suffixes an EL `uname -r` carries,
// which Salt's pkg.normalize_name strips.
var rpmArches = map[string]bool{
	"x86_64": true, "aarch64": true, "ppc64le": true, "s390x": true, "i686": true, "noarch": true,
}

func (rpmKernels) active(c *exec.Context) (string, error) {
	r, err := kernelRelease(c)
	if err != nil {
		return "", err
	}
	return rpmStripArch(r), nil
}

func rpmStripArch(release string) string {
	if i := strings.LastIndexByte(release, '.'); i > 0 && rpmArches[release[i+1:]] {
		return release[:i]
	}
	return release
}

// rpmKernelPackage is the package each installed kernel is counted by.
const rpmKernelPackage = "kernel-core"

const rpmKernelQueryFormat = "%{NAME}\\t%{EPOCH}:%{VERSION}-%{RELEASE}\\t%{ARCH}\\n"

func (rpmKernels) installed(c *exec.Context) ([]kernelImage, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"rpm", "-q", rpmKernelPackage, "--queryformat", rpmKernelQueryFormat},
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	// rpm exits 1 and says "package kernel-core is not installed" on
	// standard output.
	if res.Code == 1 && strings.Contains(res.Stdout, "is not installed") {
		return nil, nil
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("rpm -q %s exited %d: %s", rpmKernelPackage, res.Code,
			strings.TrimSpace(firstLine(res.Stderr+res.Stdout)))
	}
	return parseRPMKernels(res.Stdout), nil
}

// parseRPMKernels reads lines of name, EPOCH:VERSION-RELEASE and arch,
// oldest first by rpm's ordering.
func parseRPMKernels(stdout string) []kernelImage {
	var out []kernelImage
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) < 3 {
			continue
		}
		// "(none)" is rpm's missing epoch and dnf prints 0; neither is
		// part of the release `uname -r` reports.
		evr := strings.TrimPrefix(strings.TrimPrefix(fields[1], "(none):"), "0:")
		release := evr
		if _, after, ok := strings.Cut(evr, ":"); ok {
			release = after
		}
		out = append(out, kernelImage{
			Release:  release,
			Version:  evr,
			Packages: []string{"kernel-" + release + "." + fields[2]},
		})
	}
	sortKernels(out, CompareRPM)
	return out
}

// available is the newest kernel-core the enabled repositories offer.
func (p rpmKernels) available(c *exec.Context) (kernelImage, bool, error) {
	res, err := c.Run(exec.Command{
		Argv: []string{p.binary, "repoquery", "--quiet", "--latest-limit=1",
			"--queryformat", rpmKernelQueryFormat, rpmKernelPackage},
		IgnoreExitCode: true,
	})
	if err != nil {
		return kernelImage{}, false, err
	}
	if res.Code != 0 {
		return kernelImage{}, false, fmt.Errorf("%s repoquery %s exited %d: %s", p.binary, rpmKernelPackage,
			res.Code, strings.TrimSpace(firstLine(res.Stderr)))
	}
	ks := parseRPMKernels(res.Stdout)
	if len(ks) == 0 {
		return kernelImage{}, false, nil
	}
	return ks[len(ks)-1], true, nil
}

func (p rpmKernels) install(c *exec.Context, k kernelImage) error {
	_, err := c.Run(exec.Command{Argv: append([]string{p.binary, "install", "-y", "-q"}, k.Packages...)})
	return err
}

// rpmRemovalTarget is the image package of one kernel; removing it
// takes `kernel` and `kernel-modules` of that version with it, as
// packages that require it.
func rpmRemovalTarget(k kernelImage) string {
	return strings.Replace(k.Packages[0], "kernel-", rpmKernelPackage+"-", 1)
}

// removal asks dnf what removing the kernel would remove, with
// --assumeno, which prints the transaction and stops.
func (p rpmKernels) removal(c *exec.Context, k kernelImage) ([]string, error) {
	target := rpmRemovalTarget(k)
	res, err := c.Run(exec.Command{
		Argv:           []string{p.binary, "remove", "--assumeno", target},
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	return checkRPMRemoval(res.Stdout, k.Release)
}

// checkRPMRemoval reads dnf's transaction table and refuses a removal
// that would take a package of another version with it.
func checkRPMRemoval(table, release string) ([]string, error) {
	var removed, foreign []string
	in := false
	for _, line := range strings.Split(table, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Removing"):
			in = true
			continue
		case trimmed == "" || strings.HasPrefix(trimmed, "Transaction Summary") || strings.HasPrefix(trimmed, "==="):
			if strings.HasPrefix(trimmed, "Transaction Summary") {
				in = false
			}
			continue
		}
		if !in || !strings.HasPrefix(line, " ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		name, version := fields[0], strings.TrimPrefix(fields[2], "0:")
		removed = append(removed, name+"-"+version+"."+fields[1])
		if version != release {
			foreign = append(foreign, name+"-"+version)
		}
	}
	if len(foreign) > 0 {
		return nil, fmt.Errorf("removing kernel %s would also remove %s, which do not belong to that "+
			"kernel; nothing was changed", release, strings.Join(foreign, ", "))
	}
	if len(removed) == 0 {
		return nil, fmt.Errorf("dnf would remove nothing for kernel %s:\n%s", release, strings.TrimSpace(table))
	}
	sort.Strings(removed)
	return removed, nil
}

func (p rpmKernels) remove(c *exec.Context, k kernelImage, _ []string) error {
	_, err := c.Run(exec.Command{Argv: []string{p.binary, "remove", "-y", "-q", rpmRemovalTarget(k)}})
	return err
}

// ---- registration ----

func kernelpkgExecModules() []exec.Module {
	read := func(fn func(*exec.Context) (any, error)) exec.Func {
		return func(c *exec.Context, args *value.Map) (any, error) { return fn(c) }
	}
	return []exec.Module{
		exec.Module{
			Sig: signature.Signature{
				Module: "kernelpkg", Function: "active",
				Doc: "Return the running kernel's release, without the architecture on EL " +
					"(`5.14.0-570.12.1.el9_6`), whole on Debian (`6.12.107+deb13-amd64`).",
				TestMode: signature.TestNotApplicable, Platforms: kernelpkgPlatforms, Section: "15.2",
			},
			Fn: read(kernelpkgActive),
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "kernelpkg", Function: "list_installed",
				Doc: "Return the releases of the installed kernels of the running kernel's kind, oldest " +
					"first by the package manager's own ordering. Every side-by-side kernel is listed.",
				TestMode: signature.TestNotApplicable, Platforms: kernelpkgPlatforms, Section: "15.2",
			},
			Fn: read(kernelpkgListInstalled),
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "kernelpkg", Function: "latest_installed",
				Doc: "Return the release of the newest installed kernel, or null when none is. It is not " +
					"the running kernel until the node has been rebooted into it.",
				TestMode: signature.TestNotApplicable, Platforms: kernelpkgPlatforms, Section: "15.2",
			},
			Fn: read(kernelpkgLatestInstalled),
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "kernelpkg", Function: "latest_available",
				Doc: "Return the release of the newest kernel the repositories offer: on Debian the image the " +
					"flavour's metapackage (`linux-image-amd64`) depends on in its candidate version. The newest " +
					"installed when the repositories offer none, as Salt's does.",
				TestMode: signature.TestNotApplicable, Platforms: kernelpkgPlatforms, Section: "15.2",
			},
			Fn: read(kernelpkgLatestAvailable),
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "kernelpkg", Function: "needs_reboot",
				Doc: "Report whether a kernel newer than the running one is installed, compared by the package " +
					"manager's versions. An error, not false, when the running kernel is not a package this node " +
					"holds, as in a container. It reboots nothing.",
				TestMode: signature.TestNotApplicable, Platforms: kernelpkgPlatforms, Section: "15.2",
			},
			Fn: read(kernelpkgNeedsReboot),
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "kernelpkg", Function: "upgrade_available",
				Doc:      "Report whether the repositories offer a kernel newer than every installed one.",
				TestMode: signature.TestNotApplicable, Platforms: kernelpkgPlatforms, Section: "15.2",
			},
			Fn: read(kernelpkgUpgradeAvailable),
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "kernelpkg", Function: "upgrade",
				Doc: "Install the newest kernel the repositories offer beside the installed ones. With `reboot`, " +
					"schedule a reboot through `reboot.schedule` when one is then needed; there is no immediate " +
					"reboot, so `at_time` defaults to that function's delay rather than to now.",
				Params: []signature.Param{
					opt("reboot", signature.Bool, false, "Schedule a reboot when the new kernel is not the running one."),
					opt("at_time", signature.Int, int64(rebootDefaultDelayMinutes),
						"Minutes until the reboot, with `reboot`. The minimum is 1."),
				},
				Mutates: true, TestMode: signature.TestReliable, Privileges: []string{"root"},
				Platforms: kernelpkgPlatforms, Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return kernelpkgUpgrade(c, states.Bool(args, "reboot", false),
					states.Int(args, "at_time", rebootDefaultDelayMinutes))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "kernelpkg", Function: "remove",
				Doc: "Remove one installed kernel, named by its release as `list_installed` prints it. The " +
					"running kernel is refused, and so is a removal the package manager would widen: on Debian, " +
					"one that would install the unsigned twin in its place or remove a package not of that " +
					"release, such as the metapackage.",
				Params: []signature.Param{
					req("release", signature.String, "The release, as `kernelpkg.list_installed` prints it."),
				},
				Mutates: true, TestMode: signature.TestReliable, Privileges: []string{"root"},
				Platforms: kernelpkgPlatforms, Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return kernelpkgRemove(c, states.Str(args, "release", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "kernelpkg", Function: "cleanup",
				Doc: "Remove every installed kernel except the running one and, unless `keep_latest` is false, " +
					"the newest. Every removal is checked before any is made.",
				Params: []signature.Param{
					opt("keep_latest", signature.Bool, true, "Keep the newest installed kernel as well as the running one."),
				},
				Mutates: true, TestMode: signature.TestReliable, Privileges: []string{"root"},
				Platforms: kernelpkgPlatforms, Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return kernelpkgCleanup(c, states.Bool(args, "keep_latest", true))
			},
		},
	}
}

func kernelpkgStateModules() []states.Module {
	atTime := opt("at_time", signature.Int, int64(rebootDefaultDelayMinutes),
		"Minutes until the reboot. The minimum is 1: Salt's default of now is not offered, so "+
			"`reboot.cancel` always has a window.")
	return []states.Module{
		states.Module{
			Sig: signature.Signature{
				Module: "kernelpkg", Function: "latest_installed",
				Doc: "Ensure the newest kernel the repositories offer is installed. It is not made the running " +
					"kernel; see `kernelpkg.latest_active`.",
				Params:  []signature.Param{nameParam("The state ID; not used.")},
				Mutates: true, TestMode: signature.TestReliable, Privileges: []string{"root"},
				Platforms: kernelpkgPlatforms, Section: "15.5",
			},
			Fn: kernelpkgLatestInstalledState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "kernelpkg", Function: "latest_active",
				Doc: "Schedule a reboot, through `reboot.schedule`, when the newest installed kernel is not the " +
					"running one. Converged when it is, or when a reboot is already pending.",
				Params:  []signature.Param{nameParam("The state ID; not used."), atTime},
				Mutates: true, TestMode: signature.TestReliable, Privileges: []string{"root"},
				Platforms: kernelpkgPlatforms, Section: "15.5",
			},
			Fn: kernelpkgLatestActiveState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "kernelpkg", Function: "latest_wait",
				Doc: "Do nothing unless a watch or listen requisite fires, and then act as " +
					"`kernelpkg.latest_active`.",
				Params:  []signature.Param{nameParam("The state ID; not used."), atTime},
				Mutates: true, TestMode: signature.TestReliable, Privileges: []string{"root"},
				Platforms: kernelpkgPlatforms, Section: "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return states.True("kernelpkg.latest_wait acts only when a watch or listen requisite fires."), nil
			},
			ModWatch: kernelpkgLatestActiveState,
		},
	}
}

func kernelpkgLatestInstalledState(c *exec.Context, args *value.Map) (states.Result, error) {
	s, err := readKernels(c)
	if err != nil {
		return states.False(err.Error()), nil
	}
	up, avail, err := s.upgradeAvailable(c)
	if err != nil {
		return states.False(fmt.Sprintf("The newest available kernel could not be read: %v", err)), nil
	}
	latest, have := s.latest()
	if !up {
		if !have {
			return states.False("No kernel is installed and the repositories offer none this module can identify."), nil
		}
		return states.True(fmt.Sprintf("The latest kernel package is already installed: %s.", latest.Release)), nil
	}
	old := ""
	if have {
		old = latest.Release
	}
	changes := value.MapOf("kernel", states.Change(old, avail.Release))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The latest kernel package would be installed: %s.", avail.Release), changes), nil
	}
	if _, err := kernelpkgUpgrade(c, false, rebootDefaultDelayMinutes); err != nil {
		return states.False(fmt.Sprintf("Kernel %s could not be installed: %v", avail.Release, err)), nil
	}
	return states.Changed(fmt.Sprintf("The latest kernel package has been installed, but not activated: %s.",
		avail.Release), changes), nil
}

func kernelpkgLatestActiveState(c *exec.Context, args *value.Map) (states.Result, error) {
	delay := states.Int(args, "at_time", rebootDefaultDelayMinutes)
	s, err := readKernels(c)
	if err != nil {
		return states.False(err.Error()), nil
	}
	need, err := s.needsReboot()
	if err != nil {
		return states.False(err.Error()), nil
	}
	latest, _ := s.latest()
	if !need {
		return states.True(fmt.Sprintf("The latest installed kernel package is active: %s.", s.active)), nil
	}
	pending, err := rebootScheduled(c)
	if err != nil {
		return states.False(fmt.Sprintf("Whether a reboot is already pending could not be read: %v", err)), nil
	}
	if already, _ := pending.(*value.Map).GetString("scheduled"); already == true {
		return states.True(fmt.Sprintf("Kernel %s is installed and not running, and a reboot is already "+
			"pending, so this changed nothing.", latest.Release)), nil
	}
	changes := value.MapOf("kernel", states.Change(s.active, latest.Release))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("A reboot would be scheduled in %d minute(s) to activate kernel %s.",
			delay, latest.Release), changes), nil
	}
	if _, err := rebootSchedule(c, delay, "kernelpkg: booting kernel "+latest.Release); err != nil {
		return states.False(fmt.Sprintf("A reboot could not be scheduled: %v", err)), nil
	}
	return states.Changed(fmt.Sprintf("A reboot is scheduled in %d minute(s) to activate kernel %s; "+
		"`reboot.cancel` stops it.", delay, latest.Release), changes), nil
}
