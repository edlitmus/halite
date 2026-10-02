package config

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Every directory a binary writes by default is writable under its unit.
//
// Two things that have to agree: the defaults in this package, which say
// where each program writes on Linux, and the sandbox in each systemd
// unit, which says where it may. The hub's unit ran with
// ProtectSystem=strict and made writable its StateDirectory=, its
// LogsDirectory= and /etc/halite/pki -- and not /var/cache/halite, the
// default cache_dir. `serve` creates and probe-writes
// <cache dir>/nodes before it will start, so the stock unit on default
// paths described a hub that exits at startup. Nothing compared the two;
// this does.
//
// The paths are derived rather than listed. The candidate settings are
// every key in the table whose default is one of SPEC 27.3's layout
// paths, resolved for Linux whatever host the test runs on; a setting is
// checked for a binary when that binary's own command package names it,
// which is the same evidence the unread-key audit uses. A new directory
// setting, or a binary starting to read one, is picked up without anyone
// remembering this test.
//
// What it does not check, and cannot from here: that the unit starts
// under a real systemd, that a ReadWritePaths= entry exists at start
// (systemd refuses the unit with 226/NAMESPACE when it does not, and
// `make install` is what creates /etc/halite/pki), and that the
// directory is owned by the unit's account. *Directory= entries are
// created and chowned by systemd; ReadWritePaths= entries are not.
func TestUnitsMakeWritableEveryDirectoryTheirBinaryWrites(t *testing.T) {
	// The host's defaults, mapped onto Linux, which is the only platform
	// these units run on. Keyed by the host value because that is what
	// the Keys table holds.
	linux := map[string]string{
		DefaultPKIDir:    posixJoin(RootFor("linux"), "pki"),
		DefaultStateDir:  VarPathFor("linux", "lib"),
		DefaultCacheDir:  VarPathFor("linux", "cache"),
		DefaultSocketDir: RunPathFor("linux"),
	}

	// Settings a binary names but only reads. Each entry is a claim
	// about the code, so it carries its reason; a stale one -- the
	// binary no longer names the setting -- fails below rather than
	// excusing nothing quietly.
	readOnly := map[string]map[string]string{
		"halite-api": {
			"pki_dir": "the API presents an operator certificate the hub issued, and reads the CA; it creates nothing there",
		},
	}

	// Settings a unit expects its configuration file to override,
	// because the unit's sandbox deliberately does not cover the
	// built-in default. The value is the worked example that has to set
	// it; the path checked is what that example sets.
	//
	// The API's is the one entry, and it is a real gap rather than a
	// design: halite-api serve on built-in defaults wants
	// /var/lib/halite/tokens, which ProtectSystem=strict leaves
	// read-only, and nothing but api.yaml moves it.
	fromConfig := map[string]map[string]string{
		"halite-api.service": {"state_dir": "api.yaml"},
	}

	units := map[string]string{}
	for p, body := range readServiceFiles(t) {
		if strings.HasSuffix(p, ".service") && strings.Contains(p, "systemd") {
			units[p] = body
		}
	}

	checked := 0
	for unitPath, body := range units {
		unitName := filepath.Base(unitPath)
		svc := parseServiceSection(body)
		exec := svc["ExecStart"]
		if len(exec) == 0 {
			continue
		}
		fields := strings.Fields(exec[len(exec)-1])
		binary := path.Base(strings.TrimLeft(fields[0], "-+!@:"))
		role, ok := map[string]Role{"halite-hub": Hub, "halite-node": Node, "halite-api": API}[binary]
		if !ok {
			t.Errorf("%s runs %s, which this check does not know the role of", unitPath, binary)
			continue
		}

		named := namedDirectoryKeys(t, binary)
		for key := range readOnly[binary] {
			if !named[key] {
				t.Errorf("%s is excused from writing %s, and cmd/%s no longer names it; drop the excuse",
					binary, key, binary)
			}
		}

		writable, readOnlyUnder := unitWritablePaths(svc)
		for _, k := range Keys {
			def, isLayout := linux[k.Default]
			if k.Default == "" || !isLayout {
				if strings.HasSuffix(k.Name, "_dir") && k.Default != "" && k.appliesTo(role) {
					t.Errorf("%s defaults to %q, which this check cannot map onto Linux; add it to the table",
						k.Name, k.Default)
				}
				continue
			}
			if !k.appliesTo(role) || !named[k.Name] {
				continue
			}
			if _, inert := InertKeys[k.Name]; inert {
				continue
			}
			if _, ro := readOnly[binary][k.Name]; ro {
				continue
			}
			want := def
			if example, ok := fromConfig[unitName][k.Name]; ok {
				cfg, err := Load(role, LoadOptions{Path: filepath.Join("..", "..", "contrib", "examples", example)})
				if err != nil {
					t.Fatalf("loading the example %s that %s relies on: %v", example, unitName, err)
				}
				want = cfg.String(k.Name, "")
				if want == "" || want == def {
					t.Errorf("%s relies on contrib/examples/%s to move %s off its default, and it does not",
						unitName, example, k.Name)
					continue
				}
			}
			checked++
			t.Logf("%s: %s %s=%s, ProtectSystem=%q", unitName, binary, k.Name, want, last(svc["ProtectSystem"]))
			if !coveredBy(want, readOnlyUnder) {
				continue
			}
			if !coveredBy(want, writable) {
				t.Errorf("%s runs %s with ProtectSystem=%s, and %s, %s, is not writable: "+
					"it is under none of %v", unitName, binary, last(svc["ProtectSystem"]),
					k.Name, want, sortedKeys(writable))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no unit's directory was checked; this check has stopped checking")
	}
}

// parseServiceSection reads a unit's [Service] section as directive to
// every value assigned, in order. Comments and other sections are
// skipped; continuation lines are not used by these units and would be
// read as their own lines.
func parseServiceSection(body string) map[string][]string {
	out := map[string][]string{}
	in := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			in = line == "[Service]"
			continue
		}
		if !in {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = append(out[strings.TrimSpace(k)], strings.TrimSpace(v))
	}
	return out
}

// unitWritablePaths is what systemd.exec(5) leaves writable, and the
// trees ProtectSystem= makes read-only: everything under strict; /usr,
// /boot and /efi under yes, and /etc as well under full.
func unitWritablePaths(svc map[string][]string) (writable, readOnlyUnder map[string]bool) {
	readOnlyUnder = map[string]bool{}
	switch last(svc["ProtectSystem"]) {
	case "strict":
		readOnlyUnder["/"] = true
	case "full":
		readOnlyUnder["/etc"] = true
		fallthrough
	case "yes", "true":
		readOnlyUnder["/usr"] = true
		readOnlyUnder["/boot"] = true
		readOnlyUnder["/efi"] = true
	}
	writable = map[string]bool{}
	bases := map[string]string{
		"ReadWritePaths":         "",
		"StateDirectory":         "/var/lib",
		"CacheDirectory":         "/var/cache",
		"LogsDirectory":          "/var/log",
		"RuntimeDirectory":       "/run",
		"ConfigurationDirectory": "/etc",
	}
	for directive, base := range bases {
		var entries []string
		for _, v := range svc[directive] {
			if v == "" {
				// An empty assignment resets that directive's list.
				entries = nil
				continue
			}
			entries = append(entries, strings.Fields(v)...)
		}
		for _, p := range entries {
			// A *Directory= entry may name a symlink after a colon, and a
			// ReadWritePaths= entry may carry a - or + prefix.
			p, _, _ = strings.Cut(p, ":")
			p = strings.TrimLeft(p, "-+")
			if base != "" {
				p = posixJoin(base, p)
			}
			writable[path.Clean(p)] = true
		}
	}
	return writable, readOnlyUnder
}

func last(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

func coveredBy(p string, writable map[string]bool) bool {
	p = path.Clean(p)
	for w := range writable {
		if w == "/" || p == w || strings.HasPrefix(p, w+"/") {
			return true
		}
	}
	return false
}

// namedDirectoryKeys is which of the layout settings cmd/<binary> reads,
// by the quoted name, outside its tests.
func namedDirectoryKeys(t *testing.T, binary string) map[string]bool {
	t.Helper()
	dir := filepath.Join("..", "..", "cmd", binary)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var src strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		src.Write(body)
	}
	out := map[string]bool{}
	for _, k := range Keys {
		if strings.Contains(src.String(), `"`+k.Name+`"`) {
			out[k.Name] = true
		}
	}
	if len(out) == 0 {
		t.Fatalf("cmd/%s names no setting at all; this check is reading the wrong place", binary)
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
