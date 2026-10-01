package builtin

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerLogrotate installs the `logrotate` execution module of SPEC
// 15.2 and its `logrotate.set` state of SPEC 15.5, under Salt's names and
// arguments: `show_conf`, `get` and `set`, each taking `conf_file`
// (DIVERGENCE 5.186).
//
// # The grammar, as logrotate 3.22.0 reads it
//
// Salt parses the file with heuristics and rewrites a whole stanza from a
// dict. Everything below was measured instead, by handing throwaway
// configurations to `logrotate -d` on Debian 13 (logrotate 3.22.0) on
// 2026-09-30, and the parser follows the tool rather than Salt wherever
// they differ:
//
//   - A line whose first character is a letter is a directive. One that
//     starts with `/`, `~`, `"` or `'` names log files. Anything else --
//     a bare `*.log`, a relative `logs/a.log`, a digit -- is an error
//     ("lines must begin with a keyword or a filename"). Salt guesses from
//     the presence of glob characters instead.
//   - Names may span lines before the `{`, with blank lines and comments
//     between them (Debian's unattended-upgrades drop-in puts three names
//     on three lines, ufw's puts the `{` on a line of its own), and a
//     directive may follow the `{` on the same line.
//   - `#` starts a comment only at the start of a line. `rotate 2 # two`
//     is "bad rotation count '2 # two'", and `x { rotate 2 }` on one line
//     is the same error.
//   - `=` separates like white space: `rotate=3`, `rotate = 3` and
//     `rotate =3` are all three rotations.
//   - Text after a `}` is an error that logrotate reports and then exits
//     0 from. A stanza still open at the end of the file is accepted
//     silently.
//   - Numbers are read the way C's strtoul reads base 0: `rotate 010` is
//     eight rotations and `rotate 0x10` sixteen; `size 010k` is 8192
//     bytes. Salt's `_convert_if_int` reads `010` as ten. Size units are
//     `k`, `K`, `M` and `G`; `1m`, `1g`, `1kb` and `1.5M` are refused.
//     `create 644`, `0644` and `00644` all create mode 0644.
//   - Directives that switch a behaviour on and off -- `compress` and
//     `nocompress` and their thirteen siblings, `ifempty` and
//     `notifempty` -- and the six rotation criteria `hourly`, `daily`,
//     `weekly`, `monthly`, `yearly` and `size` are last-one-wins within a
//     scope: logrotate says "note: 'size' overrides previously specified
//     'weekly'", and `nocompress` then `compress` compresses. Measured for
//     every ordered pair of the criteria and for compress; the other
//     pairs are only measured to be accepted directive names.
//   - Global directives apply to what follows them, not to stanzas above
//     them: a `rotate 7` after a stanza left it at zero rotations.
//   - An error in a global directive abandons the whole file ("Handling 0
//     logs").
//
// # What `get` and `show_conf` report
//
// What logrotate will do, not what the file happens to contain: a key
// overridden later in its scope by its opposite or by another rotation
// criterion is reported absent. A stanza is reported under each of its
// names, as Salt does, and an included file contributes its stanzas
// (listed by file under `include files`) but not its top-level
// directives, which `get` would otherwise report as though they were
// the named file's own.
//
// # How `set` writes
//
// In place, keeping every other byte of the file: the last directive in
// the scope that decides the key (the key itself, or the member of its
// group that would override it) is rewritten on its own line; a new
// stanza directive goes before the stanza's `}`, and a new global one
// before the file's first directive, which is where Salt prepends it.
// Salt's rewrite of the whole stanza (read from its source, not run)
// drops comments, reorders it, and -- because its pattern starts at the
// key -- drops the other names of a stanza that has several, so that
// `/var/log/cloud-init.log` stops rotating
// `/var/log/cloud-init-output.log`. Here a stanza with several names is
// edited only when `key` names all of them, since a change to one name is
// a change to the others.
//
// Every write is checked by logrotate before it is installed:
// `logrotate -d` (which, measured, runs no scripts, moves no log and
// writes no state, here pointed at a state file of its own) reads the
// edited text from a private copy with the original's mode and owner,
// and the write is refused if that reports an error or warning located
// in a configuration file that the original did not. The exit status is
// no use for this: it is 1 when a log named in a stanza without
// `missingok` is absent and 0 for "unexpected text after }". When the
// edited file is one the named `conf_file` includes, the whole
// configuration is checked again after the write, and the old text put
// back if that check found something new -- a duplicate log entry
// across two files cannot be seen from either one alone.
//
// # Platforms
//
// Linux and FreeBSD. FreeBSD's base system rotates with newsyslog(8),
// which SPEC 15.2 does not name and this module does not manage. There,
// logrotate is the sysutils/logrotate port (3.22.0 on FreeBSD 15.1),
// which lists only /usr/local/etc/logrotate.conf.sample among its files
// but, measured, also leaves a copy of it at
// /usr/local/etc/logrotate.conf and an empty /usr/local/etc/logrotate.d
// that it includes; that file is the default there. The port installs no
// cron job or periodic script, so on FreeBSD this module edits a
// configuration that nothing runs unless the operator has arranged it.
func registerLogrotate(r *Registries) {
	r.Exec.Add(logrotateExecModules()...)
	r.States.Add(logrotateStateModules()...)
}

var logrotatePlatforms = []string{"linux", "freebsd"}

func logrotateDefaultConf(goos string) string {
	if goos == "freebsd" {
		return "/usr/local/etc/logrotate.conf"
	}
	return "/etc/logrotate.conf"
}

// logrotateScriptKeys open a shell script that runs to `endscript`.
var logrotateScriptKeys = map[string]bool{
	"firstaction": true, "lastaction": true, "prerotate": true, "postrotate": true, "preremove": true,
}

// logrotateCriteria override one another, measured for every ordered pair.
var logrotateCriteria = []string{"hourly", "daily", "weekly", "monthly", "yearly", "size"}

// logrotateOpposites are the on/off pairs logrotate 3.22.0 accepted as
// directive names. `noifempty`, `nodateyesterday` and `nodatehourago`
// were refused as unknown options, so `ifempty` pairs with `notifempty`
// and the date switches have no opposite.
var logrotateOpposites = [][2]string{
	{"compress", "nocompress"}, {"copy", "nocopy"}, {"copytruncate", "nocopytruncate"},
	{"create", "nocreate"}, {"createolddir", "nocreateolddir"}, {"delaycompress", "nodelaycompress"},
	{"dateext", "nodateext"}, {"ifempty", "notifempty"}, {"mail", "nomail"},
	{"missingok", "nomissingok"}, {"olddir", "noolddir"}, {"sharedscripts", "nosharedscripts"},
	{"shred", "noshred"}, {"renamecopy", "norenamecopy"}, {"allowhardlink", "noallowhardlink"},
}

// logrotateGroup is every directive whose last appearance in a scope
// decides `key`, `key` included.
func logrotateGroup(key string) []string {
	for _, c := range logrotateCriteria {
		if c == key {
			return logrotateCriteria
		}
	}
	for _, p := range logrotateOpposites {
		if p[0] == key || p[1] == key {
			return p[:]
		}
	}
	return []string{key}
}

func inLogrotateGroup(group []string, key string) bool {
	for _, g := range group {
		if g == key {
			return true
		}
	}
	return false
}

// logrotateTaboo is the list of names an `include` of a directory skips,
// as logrotate 3.22.0 on Debian 13 printed it ("Ignoring f.bak, because of
// *.bak pattern match"), one file per candidate ending. `.conf`, `.save`,
// `.tmp`, `.dpkg-remove`, `.dpkg-backup` and `.ucf-bak` are not on it.
// Empty files and anything but a regular file are skipped too; a name
// starting with a dot is not.
var logrotateTaboo = []string{
	"*,v", "*.bak", "*.cfsaved", "*.disabled", "*.dpkg-bak", "*.dpkg-del", "*.dpkg-dist",
	"*.dpkg-new", "*.dpkg-old", "*.dpkg-tmp", "*.new", "*.old", "*.orig", "*.rhn-cfg-tmp-*",
	"*.rpmnew", "*.rpmorig", "*.rpmsave", "*.swp", "*.ucf-dist", "*.ucf-new", "*.ucf-old", "*~",
}

type lrDirective struct {
	Key      string
	Args     []string
	Line     int // index into lrFile.Lines
	OnHeader bool
	Script   []string
	IsScript bool
}

type lrStanza struct {
	Names      []string
	Open       int // the line holding `{`
	Close      int // the line holding `}`, or -1 when the file ended first
	Directives []lrDirective
}

// lrItem keeps the top level in file order, which decides what a global
// directive applies to and where an include's stanzas fall.
type lrItem struct {
	stanza    int
	directive int
}

type lrFile struct {
	Path    string
	Lines   []string
	Globals []lrDirective
	Stanzas []*lrStanza
	items   []lrItem
}

type lrConfig struct {
	main     *lrFile
	included []*lrFile
}

func isLetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

func lrFields(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == '=' })
}

// lrNames reads log file names up to an optional `{`, honouring quotes.
// It returns the names unquoted, what followed the `{`, and whether there
// was one.
func lrNames(s string) ([]string, string, bool, error) {
	var names []string
	i := 0
	for i < len(s) {
		switch c := s[i]; {
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '{':
			return names, strings.TrimSpace(s[i+1:]), true, nil
		case c == '"' || c == '\'':
			end := strings.IndexByte(s[i+1:], c)
			if end < 0 {
				return nil, "", false, fmt.Errorf("unterminated quote")
			}
			names = append(names, s[i+1:i+1+end])
			i += end + 2
		default:
			j := i
			for j < len(s) && s[j] != ' ' && s[j] != '\t' && s[j] != '{' && s[j] != '\r' {
				j++
			}
			names = append(names, s[i:j])
			i = j
		}
	}
	return names, "", false, nil
}

func lrDirectiveAt(text string, line int, onHeader bool) lrDirective {
	f := lrFields(text)
	return lrDirective{Key: f[0], Args: f[1:], Line: line, OnHeader: onHeader, IsScript: logrotateScriptKeys[f[0]]}
}

// parseLogrotate reads one configuration file. It refuses what logrotate
// reports as an error in the file's structure, because an edit placed by
// a misreading of the structure would land in the wrong scope.
func parseLogrotate(filePath, content string) (*lrFile, error) {
	f := &lrFile{Path: filePath, Lines: strings.Split(content, "\n")}
	var (
		stanza      *lrStanza
		pending     []string
		script      *lrDirective
		scriptOwner *[]lrDirective
	)
	fail := func(i int, format string, a ...any) error {
		return fmt.Errorf("%s:%d: %s", filePath, i+1, fmt.Sprintf(format, a...))
	}
	open := func(i int, rest string) error {
		stanza = &lrStanza{Names: pending, Open: i, Close: -1}
		pending = nil
		f.Stanzas = append(f.Stanzas, stanza)
		f.items = append(f.items, lrItem{stanza: len(f.Stanzas) - 1, directive: -1})
		if rest == "" {
			return nil
		}
		if strings.Contains(rest, "}") || !isLetter(rest[0]) {
			return fail(i, "logrotate reads %q after the { as a directive and refuses it", rest)
		}
		d := lrDirectiveAt(rest, i, true)
		if d.IsScript {
			return fail(i, "a %s script opened on the line of the {", d.Key)
		}
		stanza.Directives = append(stanza.Directives, d)
		return nil
	}
	for i, raw := range f.Lines {
		t := strings.TrimSpace(raw)
		if script != nil {
			if fs := strings.Fields(t); len(fs) > 0 && fs[0] == "endscript" {
				*scriptOwner = append(*scriptOwner, *script)
				script = nil
			} else {
				script.Script = append(script.Script, t)
			}
			continue
		}
		if t == "" || t[0] == '#' {
			continue
		}
		if stanza != nil {
			switch {
			case t[0] == '}':
				if rest := strings.TrimSpace(t[1:]); rest != "" {
					return nil, fail(i, "unexpected text after }: %q", rest)
				}
				stanza.Close = i
				stanza = nil
			case t[0] == '{':
				return nil, fail(i, "a { inside the stanza for %s", strings.Join(stanza.Names, " "))
			case !isLetter(t[0]):
				return nil, fail(i, "lines must begin with a keyword or a filename (possibly in double quotes)")
			default:
				d := lrDirectiveAt(t, i, false)
				if d.IsScript {
					script, scriptOwner = &d, &stanza.Directives
				} else {
					stanza.Directives = append(stanza.Directives, d)
				}
			}
			continue
		}
		switch c := t[0]; {
		case c == '}':
			return nil, fail(i, "unexpected }")
		case c == '{':
			if len(pending) == 0 {
				return nil, fail(i, "a { with no log file named before it")
			}
			if err := open(i, strings.TrimSpace(t[1:])); err != nil {
				return nil, err
			}
		case isLetter(c):
			if len(pending) > 0 {
				return nil, fail(i, "the directive %q comes between the log file names %s and their {",
					lrFields(t)[0], strings.Join(pending, " "))
			}
			d := lrDirectiveAt(t, i, false)
			if d.IsScript {
				script, scriptOwner = &d, &f.Globals
				// Its place among the items is taken when it ends, which
				// is fine: a script is never the target of an include.
			} else {
				f.Globals = append(f.Globals, d)
				f.items = append(f.items, lrItem{stanza: -1, directive: len(f.Globals) - 1})
			}
		case c == '/' || c == '~' || c == '"' || c == '\'':
			names, rest, hasOpen, err := lrNames(t)
			if err != nil {
				return nil, fail(i, "%v", err)
			}
			pending = append(pending, names...)
			if hasOpen {
				if err := open(i, rest); err != nil {
					return nil, err
				}
			}
		default:
			return nil, fail(i, "lines must begin with a keyword or a filename (possibly in double quotes)")
		}
	}
	if script != nil {
		return nil, fmt.Errorf("%s: a %s script with no endscript", filePath, script.Key)
	}
	if len(pending) > 0 {
		return nil, fmt.Errorf("%s: the log file names %s have no { after them", filePath, strings.Join(pending, " "))
	}
	return f, nil
}

// loadLogrotate reads a configuration file and every file its top-level
// `include` directives name, as logrotate would choose them.
func loadLogrotate(confPath string) (*lrConfig, error) {
	main, err := readLogrotateFile(confPath)
	if err != nil {
		return nil, err
	}
	cfg := &lrConfig{main: main}
	seen := map[string]bool{confPath: true}
	var walk func(f *lrFile, depth int) error
	walk = func(f *lrFile, depth int) error {
		for _, d := range f.Globals {
			if d.Key == "tabooext" || d.Key == "taboopat" {
				return fmt.Errorf("%s:%d changes logrotate's list of included files to skip with %s, "+
					"which this module does not model, so it cannot say which files are read", f.Path, d.Line+1, d.Key)
			}
			if d.Key != "include" || len(d.Args) == 0 {
				continue
			}
			if depth > 8 {
				return fmt.Errorf("%s: includes nest more than eight deep", f.Path)
			}
			files, err := logrotateIncludeFiles(d.Args[0])
			if err != nil {
				return err
			}
			for _, p := range files {
				if seen[p] {
					continue
				}
				seen[p] = true
				inc, err := readLogrotateFile(p)
				if err != nil {
					return err
				}
				cfg.included = append(cfg.included, inc)
				if err := walk(inc, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(main, 0); err != nil {
		return nil, err
	}
	return cfg, nil
}

func readLogrotateFile(p string) (*lrFile, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, logrotateMissing(p)
		}
		return nil, err
	}
	return parseLogrotate(p, string(b))
}

func logrotateMissing(p string) error {
	if runtime.GOOS == "freebsd" {
		return fmt.Errorf("%s does not exist. FreeBSD's base system rotates logs with newsyslog(8), which "+
			"this module does not manage; logrotate is the sysutils/logrotate port, which puts its configuration "+
			"at /usr/local/etc/logrotate.conf", p)
	}
	return fmt.Errorf("%s does not exist; is logrotate installed?", p)
}

// logrotateIncludeFiles is what `include` reads: the file itself, or a
// directory's regular, non-empty files that no taboo pattern matches, in
// name order.
func logrotateIncludeFiles(p string) ([]string, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, fmt.Errorf("include %s: %w", p, err)
	}
	if !fi.IsDir() {
		return []string{p}, nil
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if logrotateTabooName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			continue
		}
		out = append(out, filepath.Join(p, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}

func logrotateTabooName(name string) bool {
	for _, pat := range logrotateTaboo {
		if ok, _ := path.Match(pat, name); ok {
			return true
		}
	}
	return false
}

// lrValue is a directive's value in Salt's shape: true for a bare
// keyword, an integer for one plain decimal argument, the script's lines
// for a script, and otherwise the arguments joined by one space. `010`
// stays a string, because logrotate reads it as eight.
func lrValue(d lrDirective) any {
	if d.IsScript {
		out := make([]any, len(d.Script))
		for i, l := range d.Script {
			out[i] = l
		}
		return out
	}
	switch len(d.Args) {
	case 0:
		return true
	case 1:
		a := d.Args[0]
		if a == "0" || (a != "" && a[0] != '0' && a[0] != '+') {
			if n, err := strconv.ParseInt(a, 10, 64); err == nil {
				return n
			}
		}
	}
	return strings.Join(d.Args, " ")
}

// lrEffective is the directive that decides `key` in a scope: the last
// one in its group, when that is `key` itself.
func lrEffective(ds []lrDirective, key string) (lrDirective, bool) {
	group := logrotateGroup(key)
	for i := len(ds) - 1; i >= 0; i-- {
		if inLogrotateGroup(group, ds[i].Key) {
			return ds[i], ds[i].Key == key
		}
	}
	return lrDirective{}, false
}

func lrScopeView(ds []lrDirective) *value.Map {
	m := value.NewMap(len(ds))
	for _, d := range ds {
		if d.Key == "include" {
			continue
		}
		for _, g := range logrotateGroup(d.Key) {
			m.Delete(g)
		}
		m.Set(d.Key, lrValue(d))
	}
	return m
}

// showConf is Salt's `show_conf`.
func (cfg *lrConfig) showConf() *value.Map {
	out := value.NewMap(16)
	includeFiles := value.NewMap(len(cfg.included))
	addStanzas := func(f *lrFile, into *[]any) {
		for _, st := range f.Stanzas {
			view := lrScopeView(st.Directives)
			for _, n := range st.Names {
				out.Set(n, view)
				if into != nil {
					*into = append(*into, n)
				}
			}
		}
	}
	m := cfg.main
	for _, it := range m.items {
		if it.stanza >= 0 {
			st := m.Stanzas[it.stanza]
			view := lrScopeView(st.Directives)
			for _, n := range st.Names {
				out.Set(n, view)
			}
			continue
		}
		d := m.Globals[it.directive]
		if d.Key == "include" {
			if len(d.Args) > 0 {
				out.Set("include", d.Args[0])
			}
			continue
		}
		for _, g := range logrotateGroup(d.Key) {
			out.Delete(g)
		}
		out.Set(d.Key, lrValue(d))
	}
	for _, d := range m.Globals {
		if d.IsScript {
			out.Set(d.Key, lrValue(d))
		}
	}
	for _, inc := range cfg.included {
		var names []any
		addStanzas(inc, &names)
		if names == nil {
			names = []any{}
		}
		includeFiles.Set(filepath.Base(inc.Path), names)
	}
	if len(cfg.included) > 0 {
		out.Set("include files", includeFiles)
	}
	return out
}

// findStanza looks a key up as a log file name, in the named file first
// and then in what it includes. `all` reports whether the key named every
// name the stanza has.
func (cfg *lrConfig) findStanza(key string) (*lrFile, *lrStanza, bool) {
	want, _, _, err := lrNames(key)
	if err != nil || len(want) == 0 {
		return nil, nil, false
	}
	for _, f := range append([]*lrFile{cfg.main}, cfg.included...) {
		for _, st := range f.Stanzas {
			if sameNameSet(st.Names, want) {
				return f, st, true
			}
			if len(want) == 1 {
				for _, n := range st.Names {
					if n == want[0] {
						return f, st, len(st.Names) == 1
					}
				}
			}
		}
	}
	return nil, nil, false
}

func sameNameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// get is Salt's `get`: a stanza's settings, one setting of a stanza, or a
// global setting of the named file; false for any of them that is not
// in effect.
func (cfg *lrConfig) get(key, setting string) any {
	if _, st, _ := cfg.findStanza(key); st != nil {
		if setting == "" {
			return lrScopeView(st.Directives)
		}
		if d, ok := lrEffective(st.Directives, setting); ok {
			return lrValue(d)
		}
		return false
	}
	if setting != "" {
		return false
	}
	if d, ok := lrEffective(cfg.main.Globals, key); ok {
		return lrValue(d)
	}
	return false
}

// cInteger reads a number as strtoul with base 0 does: an optional sign,
// then 0x for hexadecimal, a leading 0 for octal, else decimal. It
// returns what is left after the digits.
func cInteger(s string) (int64, string, bool) {
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	base := 10
	switch {
	case len(s) > 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X'):
		base, s = 16, s[2:]
	case len(s) > 1 && s[0] == '0':
		base, s = 8, s[1:]
	}
	end := 0
	for end < len(s) {
		c := s[end]
		ok := (c >= '0' && c <= '9' && int(c-'0') < base) ||
			(base == 16 && ((c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')))
		if !ok {
			break
		}
		end++
	}
	if end == 0 {
		return 0, s, false
	}
	n, err := strconv.ParseInt(s[:end], base, 64)
	if err != nil {
		return 0, s, false
	}
	if neg {
		n = -n
	}
	return n, s[end:], true
}

// lrCanon is an argument list as logrotate means it, for comparison: the
// measured numeric directives by value, sizes in bytes, a create mode in
// octal, and anything else as its words.
func lrCanon(key string, args []string) string {
	joined := strings.Join(args, " ")
	if len(args) == 0 {
		return ""
	}
	switch key {
	case "rotate":
		if n, rest, ok := cInteger(args[0]); ok && rest == "" && len(args) == 1 {
			return strconv.FormatInt(n, 10)
		}
	case "size", "minsize", "maxsize":
		if n, rest, ok := cInteger(args[0]); ok && len(args) == 1 {
			mult := map[string]int64{"": 1, "k": 1 << 10, "K": 1 << 10, "M": 1 << 20, "G": 1 << 30}
			if m, ok := mult[rest]; ok {
				return strconv.FormatInt(n*m, 10)
			}
		}
	case "create", "createolddir":
		if mode, err := strconv.ParseUint(args[0], 8, 32); err == nil {
			return strings.Join(append([]string{"0" + strconv.FormatUint(mode, 8)}, args[1:]...), " ")
		}
	}
	return joined
}

// lrWant is what the caller asked for, turned into the words of a
// directive line. A false, null or empty value removes the directive, as
// Salt's does -- but 0 is written, where Salt's `if value:` deleted
// `rotate 0` rather than setting it.
type lrWant struct {
	remove bool
	args   []string
	show   any
}

func lrWanted(v any) (lrWant, error) {
	switch t := v.(type) {
	case nil:
		return lrWant{remove: true, show: false}, nil
	case bool:
		if !t {
			return lrWant{remove: true, show: false}, nil
		}
		return lrWant{show: true}, nil
	case int64:
		return lrWant{args: []string{strconv.FormatInt(t, 10)}, show: t}, nil
	case int:
		return lrWant{args: []string{strconv.Itoa(t)}, show: int64(t)}, nil
	case float64:
		if t != float64(int64(t)) {
			return lrWant{}, fmt.Errorf("logrotate takes no fractional numbers, and %v is one", t)
		}
		return lrWant{args: []string{strconv.FormatInt(int64(t), 10)}, show: int64(t)}, nil
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return lrWant{remove: true, show: false}, nil
		}
		if strings.ContainsAny(s, "\r\n{}") || strings.HasPrefix(s, "#") {
			return lrWant{}, fmt.Errorf("%q cannot be a directive's value: it would not stay one line of one scope", s)
		}
		args := lrFields(s)
		return lrWant{args: args, show: lrValue(lrDirective{Args: args})}, nil
	}
	return lrWant{}, fmt.Errorf("a %T cannot be a logrotate value", v)
}

func validLogrotateKeyword(k string) bool {
	if k == "" || !isLetter(k[0]) {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if !isLetter(c) && !(c >= '0' && c <= '9') && c != '_' {
			return false
		}
	}
	return true
}

// lrPlan is one `set`, worked out without touching anything.
type lrPlan struct {
	file    *lrFile
	text    string
	changed bool
	old     any
	new     any
	where   string
}

func (cfg *lrConfig) plan(key string, val any, setting any, settingGiven bool) (lrPlan, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return lrPlan{}, fmt.Errorf("a key is required")
	}
	if !settingGiven {
		return cfg.planGlobal(key, val)
	}
	directive, ok := val.(string)
	if !ok || !validLogrotateKeyword(strings.TrimSpace(directive)) {
		return lrPlan{}, fmt.Errorf("with a setting, value names the directive inside the %s stanza, and %v is not a directive name", key, val)
	}
	directive = strings.TrimSpace(directive)
	if logrotateScriptKeys[directive] || directive == "include" {
		return lrPlan{}, fmt.Errorf("%s is not set through logrotate.set", directive)
	}
	want, err := lrWanted(setting)
	if err != nil {
		return lrPlan{}, err
	}
	f, st, all := cfg.findStanza(key)
	if st == nil {
		return cfg.planNewStanza(key, directive, want)
	}
	if !all {
		return lrPlan{}, fmt.Errorf("%s shares its stanza in %s with %s, so a change to it is a change to them; "+
			"give key as all of them, %q, to make it", key, f.Path, strings.Join(otherNames(st.Names, key), " "),
			strings.Join(st.Names, " "))
	}
	if st.Close < 0 && !want.remove {
		if _, found := lrEffective(st.Directives, directive); !found {
			return lrPlan{}, fmt.Errorf("the stanza for %s in %s has no closing }, so there is nowhere certain to add %s",
				key, f.Path, directive)
		}
	}
	p, err := planScope(f, st.Directives, st, directive, want)
	p.where = fmt.Sprintf("%s in the %s stanza of %s", directive, strings.Join(st.Names, " "), f.Path)
	return p, err
}

func otherNames(names []string, key string) []string {
	var out []string
	for _, n := range names {
		if n != key {
			out = append(out, n)
		}
	}
	return out
}

func (cfg *lrConfig) planGlobal(key string, val any) (lrPlan, error) {
	if _, st, _ := cfg.findStanza(key); st != nil {
		return lrPlan{}, fmt.Errorf("%s is a stanza, and a setting inside it was not given", key)
	}
	if !validLogrotateKeyword(key) {
		return lrPlan{}, fmt.Errorf("%q is not a logrotate directive name", key)
	}
	if logrotateScriptKeys[key] || key == "include" || key == "tabooext" || key == "taboopat" {
		return lrPlan{}, fmt.Errorf("%s is not set through logrotate.set", key)
	}
	want, err := lrWanted(val)
	if err != nil {
		return lrPlan{}, err
	}
	p, err := planScope(cfg.main, cfg.main.Globals, nil, key, want)
	p.where = fmt.Sprintf("%s in the global settings of %s", key, cfg.main.Path)
	return p, err
}

func (cfg *lrConfig) planNewStanza(key, directive string, want lrWant) (lrPlan, error) {
	p := lrPlan{file: cfg.main, old: false, new: want.show}
	p.where = fmt.Sprintf("%s in a new %s stanza at the end of %s", directive, key, cfg.main.Path)
	if want.remove {
		return p, nil
	}
	names, _, hasOpen, err := lrNames(key)
	if err != nil || hasOpen || len(names) == 0 {
		return p, fmt.Errorf("%q is not a list of log file names", key)
	}
	var header []string
	for _, n := range names {
		if n == "" || !strings.ContainsAny(n[:1], "/~") || strings.ContainsAny(n, "\"\r\n") {
			return p, fmt.Errorf("%q is neither a stanza in %s nor a log file path logrotate would read as one", n, cfg.main.Path)
		}
		if strings.ContainsAny(n, " \t") {
			n = `"` + n + `"`
		}
		header = append(header, n)
	}
	lines := append([]string(nil), cfg.main.Lines...)
	at := len(lines)
	if at > 0 && lines[at-1] == "" {
		at--
	}
	block := []string{strings.Join(header, " ") + " {", "    " + lrLine(directive, want.args), "}"}
	if at > 0 && strings.TrimSpace(lines[at-1]) != "" {
		block = append([]string{""}, block...)
	}
	lines = insertLines(lines, at, block...)
	if lines[len(lines)-1] != "" {
		lines = append(lines, "")
	}
	p.text = strings.Join(lines, "\n")
	p.changed = true
	return p, nil
}

func lrLine(key string, args []string) string {
	if len(args) == 0 {
		return key
	}
	return key + " " + strings.Join(args, " ")
}

func insertLines(lines []string, at int, add ...string) []string {
	out := make([]string, 0, len(lines)+len(add))
	out = append(out, lines[:at]...)
	out = append(out, add...)
	return append(out, lines[at:]...)
}

func leadingSpace(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

// planScope edits one scope: a stanza, or the file's top level when st is
// nil.
func planScope(f *lrFile, ds []lrDirective, st *lrStanza, key string, want lrWant) (lrPlan, error) {
	p := lrPlan{file: f, old: false, new: want.show}
	if d, ok := lrEffective(ds, key); ok {
		p.old = lrValue(d)
		if !want.remove && lrCanon(key, d.Args) == lrCanon(key, want.args) {
			return p, nil
		}
	} else if want.remove {
		return p, nil
	}
	lines := append([]string(nil), f.Lines...)
	if want.remove {
		var drop []int
		for _, d := range ds {
			if d.Key == key {
				if d.OnHeader {
					return p, fmt.Errorf("%s:%d: %s shares its line with the stanza's {, which this will not split", f.Path, d.Line+1, key)
				}
				drop = append(drop, d.Line)
			}
		}
		for i := len(drop) - 1; i >= 0; i-- {
			lines = append(lines[:drop[i]], lines[drop[i]+1:]...)
		}
		p.text, p.changed = strings.Join(lines, "\n"), true
		return p, nil
	}
	group := logrotateGroup(key)
	target := -1
	for i := len(ds) - 1; i >= 0; i-- {
		if inLogrotateGroup(group, ds[i].Key) {
			target = i
			break
		}
	}
	newLine := lrLine(key, want.args)
	switch {
	case target >= 0:
		d := ds[target]
		if d.OnHeader {
			return p, fmt.Errorf("%s:%d: %s shares its line with the stanza's {, which this will not split", f.Path, d.Line+1, d.Key)
		}
		lines[d.Line] = leadingSpace(lines[d.Line]) + newLine
	case st != nil:
		indent := "    "
		for i := len(st.Directives) - 1; i >= 0; i-- {
			if !st.Directives[i].OnHeader {
				indent = leadingSpace(lines[st.Directives[i].Line])
				break
			}
		}
		lines = insertLines(lines, st.Close, indent+newLine)
	default:
		at := len(lines)
		for i, l := range lines {
			if t := strings.TrimSpace(l); t != "" && t[0] != '#' {
				at = i
				break
			}
		}
		if at == len(lines) && at > 0 && lines[at-1] == "" {
			at--
		}
		lines = insertLines(lines, at, newLine)
	}
	p.text, p.changed = strings.Join(lines, "\n"), true
	return p, nil
}

// logrotateDiagRe is a diagnostic logrotate locates in a configuration
// file: "error: /etc/x.conf:12 duplicate log entry for /var/log/a",
// "warning: /etc/x.conf:5 unknown option 'yes' -- ignoring line",
// "error: /etc/x.conf:12, unexpected text after }".
var logrotateDiagRe = regexp.MustCompile(`^(error|warning): (.+?):(\d+),? (.*)$`)

// logrotateDiagnostics reduces `logrotate -d` output to what it says
// about the configuration, with the file and line dropped so that the
// same complaint before and after an edit compares equal. What it says
// about the logs themselves -- "error: stat of /var/log/x failed" -- is
// not about the configuration and is left out.
func logrotateDiagnostics(out, tmpPath, realPath string) []string {
	var diags []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if tmpPath != "" {
			line = strings.ReplaceAll(line, tmpPath, realPath)
		}
		if m := logrotateDiagRe.FindStringSubmatch(line); m != nil {
			diags = append(diags, m[1]+": "+m[4])
			continue
		}
		if strings.HasPrefix(line, "error: Ignoring ") || strings.HasPrefix(line, "error: found error in ") ||
			strings.HasPrefix(line, "warning: Potentially dangerous mode on ") {
			diags = append(diags, line)
		}
	}
	return diags
}

// newDiagnostics is what `after` says that `before` did not, counted.
func newDiagnostics(before, after []string) []string {
	count := map[string]int{}
	for _, d := range before {
		count[d]++
	}
	var out []string
	for _, d := range after {
		if count[d] > 0 {
			count[d]--
			continue
		}
		out = append(out, d)
	}
	return out
}

func runLogrotateDebug(c *exec.Context, file, stateDir string) (string, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"logrotate", "-d", "-s", filepath.Join(stateDir, "status"), file},
		IgnoreExitCode: true,
	})
	if err != nil {
		return "", err
	}
	return res.Stdout + "\n" + res.Stderr, nil
}

// fileOwner reads a file's mode and numeric owner.
func fileOwner(p string) (os.FileMode, int, int, error) {
	info, err := os.Stat(p)
	if err != nil {
		return 0, -1, -1, err
	}
	m := value.NewMap(4)
	addOwnership(m, p, info)
	uid, gid := -1, -1
	if v, ok := m.GetString("uid"); ok {
		if n, ok := v.(int64); ok {
			uid = int(n)
		}
	}
	if v, ok := m.GetString("gid"); ok {
		if n, ok := v.(int64); ok {
			gid = int(n)
		}
	}
	return info.Mode().Perm(), uid, gid, nil
}

func writeKeepingOwner(p string, data []byte, mode os.FileMode, uid, gid int) error {
	return atomicfile.WritePrepared(p, data, mode, func(tmp string) error {
		if uid < 0 && gid < 0 {
			return nil
		}
		return os.Chown(tmp, uid, gid)
	})
}

// applyPlan checks a planned edit with logrotate and installs it.
func applyPlan(c *exec.Context, confPath string, p lrPlan) error {
	if c.Which("logrotate") == "" {
		return fmt.Errorf("logrotate is not on this node's PATH, and every write is checked with `logrotate -d` before it is made")
	}
	target := p.file.Path
	mode, uid, gid, err := fileOwner(target)
	if err != nil {
		return err
	}
	old, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "halite-logrotate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	candidate := filepath.Join(work, filepath.Base(target))
	if err := os.WriteFile(candidate, []byte(p.text), mode); err != nil {
		return err
	}
	if err := os.Chmod(candidate, mode); err != nil {
		return err
	}
	if uid >= 0 || gid >= 0 {
		_ = os.Chown(candidate, uid, gid)
	}
	before, err := runLogrotateDebug(c, target, work)
	if err != nil {
		return err
	}
	after, err := runLogrotateDebug(c, candidate, work)
	if err != nil {
		return err
	}
	if added := newDiagnostics(logrotateDiagnostics(before, "", ""), logrotateDiagnostics(after, candidate, target)); len(added) > 0 {
		return fmt.Errorf("logrotate -d refused the edited %s, which was not written: %s", target, strings.Join(added, "; "))
	}
	var wholeBefore []string
	checkWhole := filepath.Clean(target) != filepath.Clean(confPath)
	if checkWhole {
		out, err := runLogrotateDebug(c, confPath, work)
		if err != nil {
			return err
		}
		wholeBefore = logrotateDiagnostics(out, "", "")
	}
	if err := writeKeepingOwner(target, []byte(p.text), mode, uid, gid); err != nil {
		return err
	}
	if checkWhole {
		out, err := runLogrotateDebug(c, confPath, work)
		if err != nil {
			return err
		}
		if added := newDiagnostics(wholeBefore, logrotateDiagnostics(out, "", "")); len(added) > 0 {
			if rerr := writeKeepingOwner(target, old, mode, uid, gid); rerr != nil {
				return fmt.Errorf("logrotate -d %s found %s after %s was written, and putting the old text back failed: %v",
					confPath, strings.Join(added, "; "), target, rerr)
			}
			return fmt.Errorf("logrotate -d %s found %s after %s was written, so its old text was put back",
				confPath, strings.Join(added, "; "), target)
		}
	}
	return nil
}

func logrotateConfArg(args *value.Map) string {
	if p := strings.TrimSpace(states.Str(args, "conf_file", "")); p != "" {
		return p
	}
	return logrotateDefaultConf(runtime.GOOS)
}

// logrotateSet is Salt's `set`: the plan, then -- outside test mode --
// the checked write and a read back of what logrotate's own grammar now
// says.
func logrotateSet(c *exec.Context, confPath, key string, val, setting any, settingGiven bool) (lrPlan, error) {
	cfg, err := loadLogrotate(confPath)
	if err != nil {
		return lrPlan{}, err
	}
	p, err := cfg.plan(key, val, setting, settingGiven)
	if err != nil || !p.changed || c.Test {
		return p, err
	}
	if err := applyPlan(c, confPath, p); err != nil {
		return p, err
	}
	again, err := loadLogrotate(confPath)
	if err != nil {
		return p, fmt.Errorf("%s was written and no longer reads: %w", p.file.Path, err)
	}
	if q, err := again.plan(key, val, setting, settingGiven); err != nil || q.changed {
		return p, fmt.Errorf("%s was written and still does not read as %s set", p.file.Path, p.where)
	}
	return p, nil
}

func logrotateExecModules() []exec.Module {
	conf := opt("conf_file", signature.Path, "", "The logrotate configuration file. Defaults to /etc/logrotate.conf, "+
		"and to /usr/local/etc/logrotate.conf on FreeBSD, where logrotate is a port.")
	return []exec.Module{
		exec.Module{
			Sig: signature.Signature{
				Module: "logrotate", Function: "show_conf",
				Doc: "Return the parsed configuration: global settings, each stanza under each of its names, " +
					"and the stanzas of included files, listed by file under `include files`. A setting overridden " +
					"later in its scope -- `compress` by `nocompress`, `weekly` by `size` -- is left out, as logrotate ignores it.",
				Params:   []signature.Param{conf},
				TestMode: signature.TestNotApplicable, Platforms: logrotatePlatforms, Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				cfg, err := loadLogrotate(logrotateConfArg(args))
				if err != nil {
					return nil, err
				}
				return cfg.showConf(), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "logrotate", Function: "get",
				Doc: "Return a global setting, a stanza's settings, or one setting of a stanza (`key` the log file, " +
					"`value` the directive); false when it is not in effect.",
				Params: []signature.Param{
					req("key", signature.String, "A global directive, or a log file name a stanza is headed with."),
					opt("value", signature.String, "", "The directive inside the stanza `key` names."),
					conf,
				},
				TestMode: signature.TestNotApplicable, Platforms: logrotatePlatforms, Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				cfg, err := loadLogrotate(logrotateConfArg(args))
				if err != nil {
					return nil, err
				}
				return cfg.get(strings.TrimSpace(states.Str(args, "key", "")), strings.TrimSpace(states.Str(args, "value", ""))), nil
			},
		},
		// Written out as a literal with string Module and Function fields,
		// because that is what the --test audit reads (DIVERGENCE 5.181).
		exec.Module{
			Sig: signature.Signature{
				Module: "logrotate", Function: "set",
				Doc: "Set a global directive (`key` the directive, `value` its value), or a directive inside a stanza " +
					"(`key` the log file, `value` the directive, `setting` its value), editing the file that holds it in " +
					"place. True writes a bare keyword; false or empty removes the directive. The edited text is checked " +
					"with `logrotate -d` before it is written. A stanza with several log file names is changed only when " +
					"`key` names all of them.",
				Params: []signature.Param{
					req("key", signature.String, "A global directive, or a stanza's log file name."),
					req("value", signature.Any, "The global directive's value, or the directive inside the stanza."),
					opt("setting", signature.Any, nil, "The value of the directive inside the stanza."),
					conf,
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"}, Platforms: logrotatePlatforms, Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				setting, given := args.Get("setting")
				given = given && setting != nil
				val, _ := args.Get("value")
				if _, err := logrotateSet(c, logrotateConfArg(args), states.Str(args, "key", ""), val, setting, given); err != nil {
					return nil, err
				}
				return true, nil
			},
		},
	}
}

func logrotateStateModules() []states.Module {
	return []states.Module{
		{
			Sig: signature.Signature{
				Module: "logrotate", Function: "set",
				Doc: "Ensure a global logrotate directive, or a directive inside a stanza, has a value, compared as " +
					"logrotate reads it (`rotate 010` is eight, `size 1k` is 1024, `create 644` is mode 0644).",
				Params: []signature.Param{
					nameParam("The state ID; not used."),
					req("key", signature.String, "A global directive, or a stanza's log file name."),
					req("value", signature.Any, "The global directive's value, or the directive inside the stanza."),
					opt("setting", signature.Any, nil, "The value of the directive inside the stanza."),
					opt("conf_file", signature.Path, "", "The logrotate configuration file. Defaults to /etc/logrotate.conf, "+
						"and to /usr/local/etc/logrotate.conf on FreeBSD."),
				},
				Mutates: true, TestMode: signature.TestReliable, Privileges: []string{"root"},
				Platforms: logrotatePlatforms, Section: "15.5",
			},
			Fn: logrotateSetState,
		},
	}
}

func logrotateSetState(c *exec.Context, args *value.Map) (states.Result, error) {
	key := strings.TrimSpace(states.Str(args, "key", ""))
	val, _ := args.Get("value")
	setting, given := args.Get("setting")
	given = given && setting != nil
	confPath := logrotateConfArg(args)

	cfg, err := loadLogrotate(confPath)
	if err != nil {
		return states.False(err.Error()), nil
	}
	p, err := cfg.plan(key, val, setting, given)
	if err != nil {
		return states.False(err.Error()), nil
	}
	if !p.changed {
		return states.True(fmt.Sprintf("The directive %s is already %s.", p.where, lrDescribe(p.new))), nil
	}
	changes := states.Change(p.old, p.new)
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The directive %s would be changed from %s to %s.",
			p.where, lrDescribe(p.old), lrDescribe(p.new)), changes), nil
	}
	if _, err := logrotateSet(c, confPath, key, val, setting, given); err != nil {
		return states.False(fmt.Sprintf("The directive %s could not be set: %v.", p.where, strings.TrimSuffix(err.Error(), "."))), nil
	}
	return states.Changed(fmt.Sprintf("The directive %s was changed from %s to %s.",
		p.where, lrDescribe(p.old), lrDescribe(p.new)), changes), nil
}

// lrDescribe phrases a value for a comment: false is a directive that is
// not in effect.
func lrDescribe(v any) string {
	if b, ok := v.(bool); ok {
		if b {
			return "set"
		}
		return "absent"
	}
	return value.KeyString(v)
}
