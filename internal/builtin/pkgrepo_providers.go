package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The repository providers.
//
// Two of the three write a file rather than running a command, because
// that is what the package manager itself reads: `apt-add-repository` and
// `yum-config-manager` are wrappers over the same files, they are not
// installed everywhere, and neither can be asked what it *would* do. A
// file that is written atomically and read back is a repository whose
// configuration a state can compare against a declaration. Chocolatey is
// the exception: its sources live in an XML file it rewrites itself, so
// its own command is the supported way in.

// ---- apt ----

// aptSourcesDir is where a managed source goes.
//
// One file per repository under sources.list.d, never a line appended to
// sources.list. Appending means a state can add but never reliably
// remove, because it would have to find its own line again among lines
// nobody else's state agreed to leave alone.
const aptSourcesDir = "/etc/apt/sources.list.d"

// aptRepoProvider writes apt's source lists.
//
// root prefixes every path it touches. Empty is the running system,
// which is what the registered provider uses; a caller managing a chroot
// or an image being built sets it, and so does a test, which is how this
// provider is exercised on a machine with no apt.
type aptRepoProvider struct{ root string }

func (aptRepoProvider) Name() string { return "aptpkg" }

func (aptRepoProvider) Available(c *exec.Context) bool {
	return c.Which("apt-get") != "" && c.Which("dpkg-query") != ""
}

func (p aptRepoProvider) dir() string { return filepath.Join(p.root, aptSourcesDir) }

func (p aptRepoProvider) path(name string) string {
	return filepath.Join(p.dir(), name+".list")
}

func (p aptRepoProvider) Get(c *exec.Context, name string) (*value.Map, error) {
	if name == "" {
		return nil, fmt.Errorf("pkgrepo needs a name")
	}
	raw, err := os.ReadFile(p.path(name))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p.path(name), err)
	}
	return p.parse(string(raw)), nil
}

// parse reads back a file this provider wrote.
//
// A commented-out line is a disabled repository, which is how apt has
// always spelled it and what an operator who disabled one by hand will
// have done.
func (p aptRepoProvider) parse(body string) *value.Map {
	out := value.NewMap(8)
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		enabled := true
		if strings.HasPrefix(trimmed, "#") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
			enabled = false
		}
		if !looksLikeSourceLine(trimmed) {
			continue
		}
		kind, url, dist, comps, opts := parseSourceLine(trimmed)
		out.Set("type", kind)
		out.Set("baseurl", url)
		out.Set("dist", dist)
		if len(comps) > 0 {
			out.Set("comps", stringsToAny(comps))
		}
		for k, v := range opts {
			out.Set(k, v)
		}
		out.Set("enabled", enabled)
		// The first source line wins: a file this provider wrote holds
		// one, and a hand-written file holding several is described by
		// its first rather than by an arbitrary one.
		return out
	}
	return out
}

// render builds the file's contents from a declaration.
func (p aptRepoProvider) render(name string, config *value.Map) (string, error) {
	kind := repoStr(config, "type", "deb")
	url := repoStr(config, "baseurl", "")
	if url == "" {
		return "", fmt.Errorf("an apt repository needs a baseurl")
	}
	dist := repoStr(config, "dist", "")
	if dist == "" {
		return "", fmt.Errorf(
			"an apt repository needs a dist, such as `noble` or `stable`; " +
				"it is the suite the packages are published under")
	}

	var opts []string
	if arches := repoList(config, "architectures"); len(arches) > 0 {
		opts = append(opts, "arch="+strings.Join(arches, ","))
	}
	if signed := repoStr(config, "signedby", ""); signed != "" {
		opts = append(opts, "signed-by="+signed)
	}
	if trusted, ok := config.Get("trusted"); ok {
		if b, _ := trusted.(bool); b {
			opts = append(opts, "trusted=yes")
		}
	}

	line := kind
	if len(opts) > 0 {
		line += " [" + strings.Join(opts, " ") + "]"
	}
	line += " " + url + " " + dist
	if comps := repoList(config, "comps"); len(comps) > 0 {
		line += " " + strings.Join(comps, " ")
	}
	if !repoBool(config, "enabled", true) {
		line = "# " + line
	}

	header := "# Managed by halite. Edits are replaced on the next state run.\n"
	if human := repoStr(config, "humanname", ""); human != "" {
		header += "# " + human + "\n"
	}
	return header + line + "\n", nil
}

// Matches compares the file this declaration would write against the
// one that is there.
//
// Rendering and comparing bytes rather than comparing fields: the render
// is deterministic from the declaration, so a state that says the same
// thing twice produces the same file twice, and any difference in the
// bytes is a difference apt would see.
func (p aptRepoProvider) Matches(c *exec.Context, name string, config *value.Map) (bool, error) {
	body, err := p.render(name, config)
	if err != nil {
		return false, err
	}
	existing, err := os.ReadFile(p.path(name))
	if err != nil {
		return false, nil
	}
	return string(existing) == body, nil
}

func (p aptRepoProvider) Set(c *exec.Context, name string, config *value.Map) (bool, error) {
	body, err := p.render(name, config)
	if err != nil {
		return false, err
	}
	path := p.path(name)
	if existing, err := os.ReadFile(path); err == nil && string(existing) == body {
		return false, nil
	}
	if err := os.MkdirAll(p.dir(), 0o755); err != nil {
		return false, fmt.Errorf("creating %s: %w", p.dir(), err)
	}
	// World-readable: a source list is not a secret, and apt runs as
	// whatever account the operator's tooling uses. `signed-by` names a
	// keyring rather than carrying a key, so nothing here is sensitive.
	if err := writeAtomic(path, []byte(body), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func (p aptRepoProvider) Delete(c *exec.Context, name string) error {
	err := os.Remove(p.path(name))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", p.path(name), err)
	}
	return nil
}

func (p aptRepoProvider) List(c *exec.Context) (*value.Map, error) {
	entries, err := os.ReadDir(p.dir())
	if os.IsNotExist(err) {
		return value.NewMap(0), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p.dir(), err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".list" {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".list"))
	}
	sort.Strings(names)
	out := value.NewMap(len(names))
	for _, name := range names {
		got, err := p.Get(c, name)
		if err != nil || got == nil {
			continue
		}
		out.Set(name, got)
	}
	return out, nil
}

// ---- dnf and yum ----

const yumReposDir = "/etc/yum.repos.d"

// yumRepoProvider writes RHEL's .repo files. root is the apt
// provider's, and for the same reasons.
type yumRepoProvider struct{ root string }

func (yumRepoProvider) Name() string { return "yumpkg" }

func (yumRepoProvider) Available(c *exec.Context) bool {
	if c.Which("dnf") == "" && c.Which("yum") == "" {
		return false
	}
	return true
}

func (p yumRepoProvider) dir() string { return filepath.Join(p.root, yumReposDir) }

func (p yumRepoProvider) path(name string) string {
	return filepath.Join(p.dir(), name+".repo")
}

func (p yumRepoProvider) Get(c *exec.Context, name string) (*value.Map, error) {
	if name == "" {
		return nil, fmt.Errorf("pkgrepo needs a name")
	}
	raw, err := os.ReadFile(p.path(name))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p.path(name), err)
	}
	return p.parse(string(raw)), nil
}

// parse reads the INI a `.repo` file holds.
//
// The first section only. A file may carry several repositories, and one
// managed by a state is one; describing the file by its first section is
// the same choice the apt provider makes about its first source line.
func (p yumRepoProvider) parse(body string) *value.Map {
	out := value.NewMap(8)
	inSection := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			if inSection {
				break
			}
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		key, val, found := strings.Cut(trimmed, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "name":
			out.Set("humanname", val)
		case "baseurl":
			out.Set("baseurl", val)
		case "gpgkey":
			out.Set("gpgkey", val)
		case "enabled":
			out.Set("enabled", val == "1")
		case "gpgcheck":
			out.Set("gpgcheck", val == "1")
		case "priority":
			if n, err := strconv.ParseInt(val, 10, 64); err == nil {
				out.Set("priority", n)
			}
		}
	}
	return out
}

func (p yumRepoProvider) render(name string, config *value.Map) (string, error) {
	url := repoStr(config, "baseurl", "")
	if url == "" {
		return "", fmt.Errorf("a yum repository needs a baseurl")
	}
	human := repoStr(config, "humanname", "")
	if human == "" {
		// RHEL's own tooling refuses a repository with no name, so the
		// short name stands in rather than the file being written
		// invalid.
		human = name
	}

	var b strings.Builder
	b.WriteString("# Managed by halite. Edits are replaced on the next state run.\n")
	b.WriteString("[" + name + "]\n")
	b.WriteString("name=" + human + "\n")
	b.WriteString("baseurl=" + url + "\n")
	b.WriteString("enabled=" + iniBool(repoBool(config, "enabled", true)) + "\n")
	b.WriteString("gpgcheck=" + iniBool(repoBool(config, "gpgcheck", true)) + "\n")
	if key := repoStr(config, "gpgkey", ""); key != "" {
		b.WriteString("gpgkey=" + key + "\n")
	}
	if v, ok := config.Get("priority"); ok {
		if n, isInt := v.(int64); isInt && n != 0 {
			b.WriteString("priority=" + strconv.FormatInt(n, 10) + "\n")
		}
	}
	return b.String(), nil
}

func iniBool(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// Matches compares the file this declaration would write against the
// one that is there. See the apt provider.
func (p yumRepoProvider) Matches(c *exec.Context, name string, config *value.Map) (bool, error) {
	body, err := p.render(name, config)
	if err != nil {
		return false, err
	}
	existing, err := os.ReadFile(p.path(name))
	if err != nil {
		return false, nil
	}
	return string(existing) == body, nil
}

func (p yumRepoProvider) Set(c *exec.Context, name string, config *value.Map) (bool, error) {
	body, err := p.render(name, config)
	if err != nil {
		return false, err
	}
	path := p.path(name)
	if existing, err := os.ReadFile(path); err == nil && string(existing) == body {
		return false, nil
	}
	if err := os.MkdirAll(p.dir(), 0o755); err != nil {
		return false, fmt.Errorf("creating %s: %w", p.dir(), err)
	}
	if err := writeAtomic(path, []byte(body), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func (p yumRepoProvider) Delete(c *exec.Context, name string) error {
	err := os.Remove(p.path(name))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", p.path(name), err)
	}
	return nil
}

func (p yumRepoProvider) List(c *exec.Context) (*value.Map, error) {
	entries, err := os.ReadDir(p.dir())
	if os.IsNotExist(err) {
		return value.NewMap(0), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p.dir(), err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".repo" {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".repo"))
	}
	sort.Strings(names)
	out := value.NewMap(len(names))
	for _, name := range names {
		got, err := p.Get(c, name)
		if err != nil || got == nil {
			continue
		}
		out.Set(name, got)
	}
	return out, nil
}

// ---- Chocolatey ----

// chocoRepoProvider manages Chocolatey's sources.
//
// Through `choco source` rather than by writing its XML. The file is
// rewritten by Chocolatey itself on every operation, and a state that
// edited it directly would have its work discarded the next time
// anything installed a package.
type chocoRepoProvider struct{}

func (chocoRepoProvider) Name() string { return "chocolatey" }

func (chocoRepoProvider) Available(c *exec.Context) bool { return c.Which("choco") != "" }

// sourceList reads Chocolatey's sources.
//
// `--limit-output` is the machine-readable mode: one record per line,
// pipe-separated, with no banner and no table drawing. Without it the
// output is a paragraph per source.
func (p chocoRepoProvider) sourceList(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"choco", "source", "list", "--limit-output"},
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, fmt.Errorf("listing the Chocolatey sources: %w", err)
	}
	out := value.NewMap(8)
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// name|url|disabled|username|certificate|priority|bypassproxy|selfservice|adminonly
		f := strings.Split(line, "|")
		if len(f) < 3 {
			continue
		}
		m := value.NewMap(4)
		m.Set("baseurl", f[1])
		m.Set("enabled", !strings.EqualFold(strings.TrimSpace(f[2]), "true"))
		if len(f) >= 6 {
			if n, err := strconv.ParseInt(strings.TrimSpace(f[5]), 10, 64); err == nil && n != 0 {
				m.Set("priority", n)
			}
		}
		out.Set(f[0], m)
	}
	return out, nil
}

func (p chocoRepoProvider) Get(c *exec.Context, name string) (*value.Map, error) {
	if name == "" {
		return nil, fmt.Errorf("pkgrepo needs a name")
	}
	all, err := p.sourceList(c)
	if err != nil {
		return nil, err
	}
	for _, e := range all.Entries() {
		if strings.EqualFold(fmt.Sprint(e.Key), name) {
			m, _ := e.Val.(*value.Map)
			return m, nil
		}
	}
	return nil, nil
}

func (p chocoRepoProvider) List(c *exec.Context) (*value.Map, error) { return p.sourceList(c) }

func (p chocoRepoProvider) Set(c *exec.Context, name string, config *value.Map) (bool, error) {
	url := repoStr(config, "baseurl", "")
	if url == "" {
		return false, fmt.Errorf("a Chocolatey source needs a baseurl")
	}
	enabled := repoBool(config, "enabled", true)

	current, err := p.Get(c, name)
	if err != nil {
		return false, err
	}
	if current != nil {
		sameURL := repoStr(current, "baseurl", "") == url
		sameState := repoBool(current, "enabled", true) == enabled
		if sameURL && sameState {
			return false, nil
		}
	}

	argv := []string{"choco", "source", "add", "--name", name, "--source", url, "--limit-output"}
	if v, ok := config.Get("priority"); ok {
		if n, isInt := v.(int64); isInt && n != 0 {
			argv = append(argv, "--priority", strconv.FormatInt(n, 10))
		}
	}
	if _, err := c.Run(exec.Command{Argv: argv}); err != nil {
		return false, fmt.Errorf("adding the Chocolatey source %s: %w", name, err)
	}

	// Enabling and disabling are separate verbs; `source add` does not
	// take a state, so a source added and then wanted disabled needs the
	// second call.
	verb := "enable"
	if !enabled {
		verb = "disable"
	}
	if _, err := c.Run(exec.Command{
		Argv: []string{"choco", "source", verb, "--name", name, "--limit-output"},
	}); err != nil {
		return false, fmt.Errorf("%s the Chocolatey source %s: %w", verb+"ing", name, err)
	}
	return true, nil
}

func (p chocoRepoProvider) Delete(c *exec.Context, name string) error {
	current, err := p.Get(c, name)
	if err != nil {
		return err
	}
	if current == nil {
		return nil
	}
	_, err = c.Run(exec.Command{
		Argv: []string{"choco", "source", "remove", "--name", name, "--limit-output"},
	})
	if err != nil {
		return fmt.Errorf("removing the Chocolatey source %s: %w", name, err)
	}
	return nil
}

// ---- reading a configuration mapping ----

func repoStr(m *value.Map, key, def string) string {
	if m == nil {
		return def
	}
	v, ok := m.Get(key)
	if !ok || v == nil {
		return def
	}
	s, isStr := v.(string)
	if !isStr {
		return def
	}
	return s
}

func repoBool(m *value.Map, key string, def bool) bool {
	if m == nil {
		return def
	}
	v, ok := m.Get(key)
	if !ok || v == nil {
		return def
	}
	b, isBool := v.(bool)
	if !isBool {
		return def
	}
	return b
}

func repoList(m *value.Map, key string) []string {
	if m == nil {
		return nil
	}
	v, ok := m.Get(key)
	if !ok || v == nil {
		return nil
	}
	list, isList := v.([]any)
	if !isList {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, fmt.Sprint(item))
	}
	return out
}

// Matches compares a declaration against the source Chocolatey holds.
//
// Only the URL and whether it is enabled: those are the two things
// `choco source add` writes, so they are the two a declaration can be
// held to. A priority the caller did not state is not a difference.
func (p chocoRepoProvider) Matches(c *exec.Context, name string, config *value.Map) (bool, error) {
	current, err := p.Get(c, name)
	if err != nil {
		return false, err
	}
	if current == nil {
		return false, nil
	}
	if repoStr(current, "baseurl", "") != repoStr(config, "baseurl", "") {
		return false, nil
	}
	if repoBool(current, "enabled", true) != repoBool(config, "enabled", true) {
		return false, nil
	}
	if v, stated := config.Get("priority"); stated {
		if n, isInt := v.(int64); isInt && n != 0 {
			got, _ := current.Get("priority")
			gotN, _ := got.(int64)
			if gotN != n {
				return false, nil
			}
		}
	}
	return true, nil
}
