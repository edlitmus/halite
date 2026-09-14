// Package grains collects facts about the node it runs on.
//
// Everything here comes from the operating system directly, through files
// and stdlib calls, with no third-party code. On Linux that means /proc,
// /sys, /etc/os-release, and net.Interfaces rather than dmidecode, lscpu,
// or ip: grain collection has to work on a minimal image where none of
// those are installed. SPEC section 14.1.
package grains

import (
	"context"
	"net"
	"net/netip"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/version"
	"github.com/edlitmus/halite/internal/yaml"
)

// Options control collection.
type Options struct {
	// NodeID is the resolved node identity, exposed as the `id` grain.
	NodeID string
	// Static is the path to a static grains file, merged last so it can
	// override. SPEC section 14.2.
	StaticFile string
	// GrainsDir holds executable and YAML grain fragments.
	GrainsDir string
	// Extra is the `grains:` block from the configuration.
	Extra *value.Map
	// Cloud enables the cloud metadata grains, which are opt-in because
	// they cost a metadata round trip on every refresh.
	Cloud bool
	// CloudOptions configure that walk. The zero value is the real
	// metadata service with the default timeout.
	CloudOptions CloudOptions
	// ProviderTimeout bounds one executable grain provider.
	ProviderTimeout time.Duration
}

// Warning is a collection problem that did not stop the rest.
//
// A single bad grain script taking down grain collection is a familiar
// Salt failure and is not reproduced: a provider that times out, exits
// non-zero, or emits invalid JSON is skipped with a warning. SPEC section
// 14.2.
type Warning struct {
	Source string
	Msg    string
}

func (w Warning) String() string { return w.Source + ": " + w.Msg }

// Collect gathers the grains for this node.
func Collect(opts Options) (*value.Map, []Warning) {
	if opts.ProviderTimeout == 0 {
		opts.ProviderTimeout = 10 * time.Second
	}
	var warnings []Warning

	g := value.NewMap(96)
	collectIdentity(g, opts)
	collectOS(g)
	collectKernel(g)
	collectCPU(g)
	collectMemory(g)
	collectNetwork(g, &warnings)
	collectPlatform(g)
	collectSecurity(g)
	collectVirtualization(g)
	collectHardware(g)
	collectVersion(g)
	// Before the custom grains, so that a static file or a grains.d
	// fragment can still override a collected fact -- and after the
	// core ones, because `region` and `cloud` are facts about this
	// machine like any other.
	if opts.Cloud {
		warnings = append(warnings, collectCloud(g, opts.CloudOptions)...)
	}

	// Custom grains are merged last, in the order SPEC section 14.2
	// gives, so a static file can override a collected fact.
	if opts.Extra != nil {
		mergeInto(g, opts.Extra)
	}
	if opts.GrainsDir != "" {
		warnings = append(warnings, mergeGrainsDir(g, opts.GrainsDir, opts.ProviderTimeout)...)
	}
	if opts.StaticFile != "" {
		if w := mergeStaticFile(g, opts.StaticFile); w != nil {
			warnings = append(warnings, *w)
		}
	}
	return g, warnings
}

func mergeInto(dst, src *value.Map) {
	for _, e := range src.Entries() {
		dst.Set(e.Key, e.Val)
	}
}

func collectIdentity(g *value.Map, opts Options) {
	host, _ := os.Hostname()
	g.Set("id", opts.NodeID)
	g.Set("host", shortHost(host))
	g.Set("nodename", host)
	g.Set("localhost", host)

	fqdn := resolveFQDN(host)
	g.Set("fqdn", fqdn)
	if i := strings.IndexByte(fqdn, '.'); i >= 0 {
		g.Set("domain", fqdn[i+1:])
	} else {
		g.Set("domain", "")
	}
}

func shortHost(h string) string {
	if i := strings.IndexByte(h, '.'); i >= 0 {
		return h[:i]
	}
	return h
}

// resolveFQDN finds the fully qualified name without requiring a working
// reverse lookup, because a node with no PTR record still has a name.
func resolveFQDN(host string) string {
	if strings.Contains(host, ".") {
		return host
	}
	addrs, err := net.LookupHost(host)
	if err != nil {
		return host
	}
	for _, addr := range addrs {
		names, err := net.LookupAddr(addr)
		if err != nil {
			continue
		}
		for _, n := range names {
			n = strings.TrimSuffix(n, ".")
			if strings.HasPrefix(n, host+".") {
				return n
			}
		}
	}
	return host
}

func collectVersion(g *value.Map) {
	g.Set("haliteversion", version.Version)
	// saltversion reports the compatibility level this build targets, so
	// that `{% if saltversion >= ... %}` guards in an existing tree
	// evaluate sensibly. SPEC section 10.2.7.
	g.Set("saltversion", version.SaltCompat)
	parts := strings.SplitN(version.Version, ".", 3)
	info := make([]any, 0, 3)
	for _, p := range parts {
		if n, err := strconv.ParseInt(strings.SplitN(p, "-", 2)[0], 10, 64); err == nil {
			info = append(info, n)
			continue
		}
		info = append(info, p)
	}
	g.Set("haliteversioninfo", info)
}

func collectPlatform(g *value.Map) {
	g.Set("shell", envOr("SHELL", "/bin/sh"))
	g.Set("path", os.Getenv("PATH"))

	var systempath []any
	for _, p := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if p != "" {
			systempath = append(systempath, p)
		}
	}
	g.Set("systempath", systempath)

	language, encoding := splitLocale(envOr("LANG", ""))
	locale := value.MapOf(
		"defaultlanguage", language,
		"defaultencoding", encoding,
		"detectedencoding", strings.ToLower(encoding),
		"timezone", localTimezone(),
	)
	g.Set("locale_info", locale)
	g.Set("init", detectInit())
	g.Set("systemd", detectSystemd())
}

// splitLocale separates a locale into its language and its codeset.
//
// `LANG` holds both -- `en_US.UTF-8`, `C.UTF-8` -- and the two grains
// are the two halves. What used to be reported was the whole string as
// the language and a hardcoded "UTF-8" as the encoding, so a tree
// comparing `grains['locale_info']['defaultlanguage'] == 'C'` on a host
// whose LANG is `C.UTF-8` got false, and a host in a non-UTF-8 codeset
// was told it was in UTF-8 regardless.
//
// A modifier -- the `@euro` of `de_DE.ISO8859-15@euro` -- belongs to
// the language, which is where setlocale(3) puts it and where Salt's
// own split leaves it. A locale with no codeset keeps the UTF-8 default
// rather than reporting nothing, because that is the encoding a machine
// with a bare `C` or an unset LANG actually uses here.
func splitLocale(lang string) (language, encoding string) {
	language, encoding = lang, "UTF-8"
	dot := strings.IndexByte(lang, '.')
	if dot < 0 {
		return language, encoding
	}
	language, encoding = lang[:dot], lang[dot+1:]
	// The modifier rides with the language.
	if at := strings.IndexByte(encoding, '@'); at >= 0 {
		language += encoding[at:]
		encoding = encoding[:at]
	}
	if encoding == "" {
		encoding = "UTF-8"
	}
	return language, encoding
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func localTimezone() string {
	name, _ := time.Now().Zone()
	return name
}

func collectNetwork(g *value.Map, warnings *[]Warning) {
	ifaces, err := net.Interfaces()
	if err != nil {
		*warnings = append(*warnings, Warning{Source: "network", Msg: err.Error()})
		return
	}

	var ipv4, ipv6 []any
	ipInterfaces := value.NewMap(len(ifaces))
	ip4Interfaces := value.NewMap(len(ifaces))
	ip6Interfaces := value.NewMap(len(ifaces))
	hwaddrs := value.NewMap(len(ifaces))

	// Interfaces are visited in a stable order so that two collections on
	// an unchanged host produce identical grains, which is what keeps a
	// grain-triggered reactor from firing on nothing.
	sort.Slice(ifaces, func(i, j int) bool { return ifaces[i].Name < ifaces[j].Name })

	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		var all, v4, v6 []any
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err != nil {
				continue
			}
			s := ip.String()
			all = append(all, s)
			if ip.To4() != nil {
				v4 = append(v4, s)
				ipv4 = append(ipv4, s)
				continue
			}
			v6 = append(v6, s)
			ipv6 = append(ipv6, s)
		}
		ipInterfaces.Set(iface.Name, orEmpty(all))
		ip4Interfaces.Set(iface.Name, orEmpty(v4))
		ip6Interfaces.Set(iface.Name, orEmpty(v6))
		// The loopback interface has no hardware address, and Go
		// renders that as an empty string where Salt renders the
		// all-zero address every other tool prints for it. A tree
		// reading hwaddr_interfaces['lo'] gets the same answer here as
		// it did there.
		hw := iface.HardwareAddr.String()
		if hw == "" && iface.Flags&net.FlagLoopback != 0 {
			hw = "00:00:00:00:00:00"
		}
		hwaddrs.Set(iface.Name, hw)
	}

	g.Set("ipv4", orEmpty(sortIPs(ipv4)))
	g.Set("ipv6", orEmpty(sortIPs(ipv6)))
	g.Set("ip_interfaces", ipInterfaces)
	g.Set("ip4_interfaces", ip4Interfaces)
	g.Set("ip6_interfaces", ip6Interfaces)
	g.Set("hwaddr_interfaces", hwaddrs)
	g.Set("dns", resolverConfig())
}

// sortIPs deduplicates addresses and orders them numerically.
//
// The flat `ipv4` and `ipv6` grains are a set, not a per-interface
// listing, and Salt hands them over sorted as addresses. Two things
// follow that string ordering does not give: an address held by two
// interfaces appears once, and 9.0.0.1 sorts before 10.0.0.1 rather
// than after it. A tree that reads `grains['ipv4'][0]` is reading a
// position, so the order is part of the answer.
func sortIPs(addrs []any) []any {
	seen := make(map[string]bool, len(addrs))
	parsed := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		s, ok := a.(string)
		if !ok || seen[s] {
			continue
		}
		ip, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		seen[s] = true
		parsed = append(parsed, ip)
	}
	sort.Slice(parsed, func(i, j int) bool { return parsed[i].Less(parsed[j]) })
	out := make([]any, len(parsed))
	for i, ip := range parsed {
		out[i] = ip.String()
	}
	return out
}

func orEmpty(v []any) []any {
	if v == nil {
		return []any{}
	}
	return v
}

// resolvConfPath is the resolver configuration to read.
//
// /etc/resolv.conf is the file every unix has, and on a host running
// systemd-resolved it is a symlink to a stub that names one nameserver:
// 127.0.0.53, the local resolver itself. That is a true description of
// where this machine sends queries and a useless answer to the question
// the grain is asked, which is which nameservers the estate uses --
// every systemd-resolved host in a fleet reports the same loopback
// address and none reports a server anybody configured.
//
// systemd-resolved writes the real upstreams to a second file in the
// same format, and Salt prefers it for this reason. So does this.
func resolvConfPath() string {
	const resolved = "/run/systemd/resolve/resolv.conf"
	if _, err := os.Stat(resolved); err == nil {
		return resolved
	}
	return "/etc/resolv.conf"
}

// resolverConfig reads the resolver configuration, which every unix has
// and which needs no external tool.
func resolverConfig() *value.Map {
	out := value.MapOf(
		"nameservers", []any{},
		"ip4_nameservers", []any{},
		"ip6_nameservers", []any{},
		"sortlist", []any{},
		"search", []any{},
		"options", []any{},
		"domain", "",
	)
	b, err := os.ReadFile(resolvConfPath())
	if err != nil {
		return out
	}
	var nameservers, ip4, ip6, search, sortlist, options []any
	domain := ""
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "nameserver":
			nameservers = append(nameservers, fields[1])
			// The two families are split as well as listed together,
			// because a state that writes a resolver configuration
			// writes them separately.
			if ip, err := netip.ParseAddr(fields[1]); err == nil && ip.Is4() {
				ip4 = append(ip4, fields[1])
			} else if err == nil {
				ip6 = append(ip6, fields[1])
			}
		case "search":
			for _, s := range fields[1:] {
				search = append(search, s)
			}
		case "sortlist":
			for _, s := range fields[1:] {
				sortlist = append(sortlist, s)
			}
		case "options":
			for _, s := range fields[1:] {
				options = append(options, s)
			}
		case "domain":
			domain = fields[1]
		}
	}
	out.Set("nameservers", orEmpty(nameservers))
	out.Set("ip4_nameservers", orEmpty(ip4))
	out.Set("ip6_nameservers", orEmpty(ip6))
	out.Set("sortlist", orEmpty(sortlist))
	out.Set("search", orEmpty(search))
	out.Set("options", orEmpty(options))
	out.Set("domain", domain)
	return out
}

// ---- custom grains, SPEC section 14.2 ----

func mergeStaticFile(g *value.Map, path string) *Warning {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return &Warning{Source: path, Msg: err.Error()}
	}
	v, _, err := yaml.Parse(b, yaml.DefaultOptions(path))
	if err != nil {
		return &Warning{Source: path, Msg: err.Error()}
	}
	m, ok := v.(*value.Map)
	if !ok {
		if v == nil {
			return nil
		}
		return &Warning{Source: path, Msg: "a static grains file must hold a mapping"}
	}
	mergeInto(g, m)
	return nil
}

// mergeGrainsDir reads /etc/halite/grains.d/: an executable file is run
// and its JSON output merged, and a non-executable file is parsed as YAML.
// This is the low-ceremony path that most Salt `_grains/` modules actually
// needed. SPEC section 14.2.
func mergeGrainsDir(g *value.Map, dir string, timeout time.Duration) []Warning {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []Warning{{Source: dir, Msg: err.Error()}}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var warnings []Warning
	for _, name := range names {
		path := dir + string(os.PathSeparator) + name
		info, err := os.Stat(path)
		if err != nil {
			warnings = append(warnings, Warning{Source: path, Msg: err.Error()})
			continue
		}
		// Asked of the platform. The execute bit is the answer on unix
		// and there is no such bit on Windows, where this was false for
		// every file and so parsed every provider script as YAML.
		if isRunnable(path, info) {
			m, w := runGrainProvider(path, timeout)
			if w != nil {
				warnings = append(warnings, *w)
				continue
			}
			mergeInto(g, m)
			continue
		}
		if w := mergeStaticFile(g, path); w != nil {
			warnings = append(warnings, *w)
		}
	}
	return warnings
}

// maxProviderOutput bounds what one grain provider may emit, so that a
// runaway script cannot exhaust memory during collection.
const maxProviderOutput = 1 << 20

// runGrainProvider runs one provider and reads its JSON.
//
// Through the command runner rather than os/exec directly. This used to
// start the process itself, and then, on a timeout, kill it and wait for
// cmd.Output() — which does not return until the output pipe closes, and
// a killed script's own children keep that pipe open. A provider that
// started anything at all was therefore waited out rather than stopped:
// measured here at 61 seconds against a 300ms timeout. The runner has
// killed the whole process tree since the timeout was fixed there, and
// bounds the wait after the kill as well.
func runGrainProvider(path string, timeout time.Duration) (*value.Map, *Warning) {
	argv := providerArgv(path)
	runner := &exec.OSRunner{}
	res, err := runner.Run(context.Background(), exec.Command{
		Argv: argv,
		// The same clean environment every other child gets: an
		// explicit search path, a fixed locale, and nothing of
		// halite's own. A grain provider must not see the hub's
		// credentials any more than a cmd.run may.
		Env:     exec.CleanEnv(),
		Timeout: timeout,
	})
	if err != nil {
		if strings.Contains(err.Error(), "timed out") {
			return nil, &Warning{Source: path, Msg: "timed out and was skipped"}
		}
		return nil, &Warning{Source: path, Msg: "exited non-zero and was skipped: " + err.Error()}
	}
	out := []byte(res.Stdout)
	if len(out) > maxProviderOutput {
		return nil, &Warning{Source: path, Msg: "emitted more than 1 MiB and was skipped"}
	}

	v, err := value.DecodeJSON(out)
	if err != nil {
		return nil, &Warning{Source: path, Msg: "emitted invalid JSON and was skipped: " + err.Error()}
	}
	m, ok := v.(*value.Map)
	if !ok {
		return nil, &Warning{Source: path, Msg: "did not emit a JSON object and was skipped"}
	}
	return m, nil
}

// ---- helpers shared by the platform files ----

// readKeyValueFile parses a `key=value` file such as /etc/os-release,
// stripping the quotes the format allows.
func readKeyValueFile(path string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out
}

// firstLineOf reads a file and returns its first line, trimmed. Missing
// files return an empty string, which is what a grain absent from this
// platform should look like.
func firstLineOf(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// majorRelease is `osmajorrelease`: the leading component of a release
// as a *number*.
//
// A string is what this returned, on every platform, and it is the
// wrong type for the only thing the grain is for. `{% if
// grains['osmajorrelease'] >= 22 %}` is how a tree asks "is this at
// least jammy", and against a string that comparison is either an error
// or an alphabetical ordering in which "9" is greater than "22". Salt
// has reported an integer here since 3001.
//
// A release with no leading number -- a rolling distribution whose
// VERSION_ID is absent or a word -- yields an empty string, which is
// what Salt does by leaving the grain unset: there is no number, and
// zero would be a number that compares.
func majorRelease(release string) any {
	major := majorVersion(release)
	if major == "" {
		return ""
	}
	n, err := strconv.ParseInt(major, 10, 64)
	if err != nil {
		return ""
	}
	return n
}

// majorVersion returns the leading numeric component of a release string.
func majorVersion(release string) string {
	for i, r := range release {
		if r < '0' || r > '9' {
			return release[:i]
		}
	}
	return release
}

// osFinger is the `os`-and-version pair a tree uses to name a platform
// in one string, and the two halves are chosen differently.
//
// Salt spells the version as the *full* release for Ubuntu, Pop and
// NixOS, whose releases only mean something with both components --
// `Ubuntu-22.04`, not `Ubuntu-22`, because 22.04 and 22.10 are
// different systems -- and as the major alone everywhere else, where
// the minor is a point release. The name half is the short `os` for the
// families that have one and `osfullname` otherwise.
//
// This build used the major everywhere, so every Ubuntu host answered
// `Ubuntu-22` to a grain an existing tree matches as `Ubuntu-22.04`.
func osFinger(osName, osFullName, release string) string {
	name := osFullName
	switch osName {
	case "Debian", "FreeBSD", "OpenBSD", "NetBSD", "Mac", "Raspbian", "AlmaLinux":
		name = osName
	}
	version := majorVersion(release)
	switch osName {
	case "Ubuntu", "Pop", "NixOS":
		version = release
	}
	return name + "-" + version
}

// releaseInfo splits a release string into its numeric components.
func releaseInfo(release string) []any {
	var out []any
	for _, part := range strings.FieldsFunc(release, func(r rune) bool { return r == '.' || r == '-' }) {
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

// goarchToCPU maps Go's architecture names to the ones Salt trees expect.
func goarchToCPU() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "386":
		return "i686"
	case "arm64":
		return "aarch64"
	case "arm":
		return "armv7l"
	default:
		return runtime.GOARCH
	}
}
