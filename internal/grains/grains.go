// Package grains collects facts about the node it runs on.
//
// Everything here comes from the operating system directly, through files
// and stdlib calls, with no third-party code. On Linux that means /proc,
// /sys, /etc/os-release, and net.Interfaces rather than dmidecode, lscpu,
// or ip: grain collection has to work on a minimal image where none of
// those are installed. SPEC section 14.1.
package grains

import (
	"net"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

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

	locale := value.MapOf(
		"defaultlanguage", envOr("LANG", ""),
		"defaultencoding", "UTF-8",
		"detectedencoding", "UTF-8",
		"timezone", localTimezone(),
	)
	g.Set("locale_info", locale)
	g.Set("init", detectInit())
	g.Set("systemd", detectSystemd())
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
		hwaddrs.Set(iface.Name, iface.HardwareAddr.String())
	}

	g.Set("ipv4", orEmpty(ipv4))
	g.Set("ipv6", orEmpty(ipv6))
	g.Set("ip_interfaces", ipInterfaces)
	g.Set("ip4_interfaces", ip4Interfaces)
	g.Set("ip6_interfaces", ip6Interfaces)
	g.Set("hwaddr_interfaces", hwaddrs)
	g.Set("dns", resolverConfig())
}

func orEmpty(v []any) []any {
	if v == nil {
		return []any{}
	}
	return v
}

// resolverConfig reads /etc/resolv.conf, which every unix has and which
// needs no external tool.
func resolverConfig() *value.Map {
	out := value.MapOf("nameservers", []any{}, "search", []any{}, "domain", "")
	b, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return out
	}
	var nameservers, search []any
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
		case "search":
			for _, s := range fields[1:] {
				search = append(search, s)
			}
		case "domain":
			domain = fields[1]
		}
	}
	out.Set("nameservers", orEmpty(nameservers))
	out.Set("search", orEmpty(search))
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
		if info.Mode()&0o111 != 0 {
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

func runGrainProvider(path string, timeout time.Duration) (*value.Map, *Warning) {
	cmd := exec.Command(path)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}

	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return nil, &Warning{Source: path, Msg: "timed out and was skipped"}
	}
	if err != nil {
		return nil, &Warning{Source: path, Msg: "exited non-zero and was skipped: " + err.Error()}
	}
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

// majorVersion returns the leading numeric component of a release string.
func majorVersion(release string) string {
	for i, r := range release {
		if r < '0' || r > '9' {
			return release[:i]
		}
	}
	return release
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
