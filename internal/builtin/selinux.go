package builtin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// SELinuxFSPath is where selinuxfs is mounted. A variable so a test can
// point it at a directory of its own.
//
// Its `enforce` file is the running mode -- `1` or `0`, one byte and no
// newline, captured on Rocky 9.8 and Alma 8.10 -- and is what
// getenforce(8) itself reads. Reading it directly rather than running
// `getenforce` needs no package and no privilege: the file is 0644 and
// `nobody` read `1` from it on both hosts. A node where the file is
// absent has no SELinux running, which is how Debian 13 looks: no
// /sys/fs/selinux at all.
var SELinuxFSPath = "/sys/fs/selinux"

// SELinuxConfigPath is the file that decides the mode at boot.
var SELinuxConfigPath = "/etc/selinux/config"

// registerSELinux installs SPEC 15.2's `selinux` module, the
// `selinux.*` states of 15.5, and the two SELinux functions SPEC 15.2
// lists under `file`.
//
// # Everything here was driven, and what was not is refused
//
// Built against two real policies, Rocky Linux 9.8 (policycoreutils 3.6)
// and AlmaLinux 8.10 (policycoreutils 2.9), both enforcing, and every
// parser reads output captured there (testdata/selinux). The pieces Salt
// has and this does not are left out rather than built blind: module
// install/remove/enable (`setsemod`, `install_semod`, `remove_semod` and
// the `module*` states), and fcontext equivalence rules.
//
// # The running mode only
//
// Salt's `setenforce` writes the running mode *and* rewrites the
// SELINUX= line of /etc/selinux/config, and `selinux.mode` inherits
// that. This module changes the running mode only, and never writes the
// file. The reason is what could be demonstrated: the lab hosts this was
// built on are reached over SSH and shared, and a config write that is
// wrong -- or a `disabled` that is right -- is found out at the next
// boot, which is exactly the run nobody was there to watch. So the file
// is read and never written, and `selinux.mode` refuses to set a running
// mode the file disagrees with: a node that would change mode at its
// next boot is the one state it must not report as converged.
// DIVERGENCE 5.183.
//
// # Rules are compared in semanage's own spelling
//
// semanage stores an fcontext specification verbatim. Captured on both
// hosts: `/srv/x/`, `/srv//x` and even `relative/path` are all accepted
// with exit 0 and listed back byte for byte, so a rule is found by
// comparing the specification exactly as the caller gave it, which is
// exactly how semanage will list it -- DIVERGENCE 5.31's lesson, and here
// the tool's spelling and the caller's are the same string. Two of
// those spellings are refused before semanage sees them, because
// semanage stores a rule that can never match anything: a relative
// specification, and one ending in `/` (captured: with
// `/srv/halite-ts/` added, `matchpathcon /srv/halite-ts` still answered
// `var_t`).
func registerSELinux(r *Registries) {
	r.Exec.Add(selinuxExecModules()...)
	r.Exec.Add(selinuxFileModules()...)
	r.States.Add(selinuxStateModules()...)
}

// selinuxPackages names the package each tool comes from on the RHEL
// family, so a missing tool is reported as something to install.
var selinuxPackages = map[string]string{
	"semanage":   "policycoreutils-python-utils",
	"setsebool":  "policycoreutils",
	"restorecon": "policycoreutils",
	"semodule":   "policycoreutils",
	"chcon":      "coreutils",
	"stat":       "coreutils",
}

// selinuxEnabled reports whether this kernel is running SELinux, and why
// not when it is not.
func selinuxEnabled() (bool, string) {
	enforce := filepath.Join(SELinuxFSPath, "enforce")
	if _, err := os.Stat(enforce); err != nil {
		if os.IsNotExist(err) {
			return false, "SELinux is not running on this node: " + enforce + " does not exist"
		}
		return false, err.Error()
	}
	return true, ""
}

// selinuxReady is the precondition of every function that talks to the
// policy: SELinux is running, and the tool is installed.
func selinuxReady(c *exec.Context, tool string) error {
	if ok, why := selinuxEnabled(); !ok {
		return errors.New(why)
	}
	if c.Which(tool) == "" {
		return fmt.Errorf("%s is not installed; it is in the %s package", tool, selinuxPackages[tool])
	}
	return nil
}

// selinuxToolMessage is what a policycoreutils tool said, on one line.
//
// semanage fails with a Python exception's last line (`ValueError: Port
// tcp/80 is defined in policy, cannot be deleted`), but a libsemanage
// failure prints several lines of which the useful one comes first
// (`libsemanage.semanage_port_validate_local: port overlap between
// ranges ...` and then `FileNotFoundError: No such file or directory`),
// so all of it is kept.
func selinuxToolMessage(s string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	msg := strings.Join(lines, " ")
	const max = 600
	if len(msg) > max {
		msg = msg[:max] + " …"
	}
	return msg
}

// selinuxRun runs one command and turns a non-zero exit into an error
// carrying what the tool said.
//
// IgnoreExitCode is set so the exit code reaches this function at all:
// without it OSRunner returns an error of its own before the code can be
// read, and the fakes do not reproduce that (DIVERGENCE 5.113).
// TestSELinuxCommandsAskForTheirExitCode holds every call to it.
func selinuxRun(c *exec.Context, argv ...string) (string, error) {
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", fmt.Errorf("%s: %s", strings.Join(argv, " "), selinuxToolMessage(res.Stderr+"\n"+res.Stdout))
	}
	return res.Stdout, nil
}

// ---- mode ----

// selinuxGetenforce is getenforce(8)'s answer, read from selinuxfs.
func selinuxGetenforce() (string, error) {
	b, err := os.ReadFile(filepath.Join(SELinuxFSPath, "enforce"))
	if err != nil {
		if os.IsNotExist(err) {
			return "Disabled", nil
		}
		return "", err
	}
	switch strings.TrimSpace(string(b)) {
	case "1":
		return "Enforcing", nil
	case "0":
		return "Permissive", nil
	}
	return "", fmt.Errorf("%s holds %q, which is neither 1 nor 0", filepath.Join(SELinuxFSPath, "enforce"), b)
}

// selinuxGetconfig is the SELINUX= line of the config file, capitalised
// as getenforce spells a mode, or "" when there is no file or no line.
// The first such line wins, which is what Salt reads.
func selinuxGetconfig() (string, error) {
	b, err := os.ReadFile(SELinuxConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "SELINUX=") {
			return selinuxCapitalise(strings.TrimSpace(strings.TrimPrefix(line, "SELINUX="))), nil
		}
	}
	return "", nil
}

func selinuxCapitalise(s string) string {
	s = strings.ToLower(s)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// selinuxMode turns what a caller wrote into getenforce's spelling:
// Enforcing, Permissive or Disabled. `1` and `0` are setenforce(8)'s
// own synonyms, and a YAML `true`/`false` arrives as a bool.
func selinuxMode(v any) (string, error) {
	var s string
	switch t := v.(type) {
	case bool:
		s = "0"
		if t {
			s = "1"
		}
	case int64:
		s = strconv.FormatInt(t, 10)
	case string:
		s = t
	default:
		s = value.KeyString(v)
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "enforcing", "1":
		return "Enforcing", nil
	case "permissive", "0":
		return "Permissive", nil
	case "disabled":
		return "Disabled", nil
	}
	return "", fmt.Errorf("%q is not a SELinux mode: use enforcing, permissive or disabled", s)
}

// selinuxSetRunningMode writes selinuxfs's enforce file, which is what
// setenforce(8) does, and reads it back.
func selinuxSetRunningMode(mode string) error {
	b := "0"
	if mode == "Enforcing" {
		b = "1"
	}
	path := filepath.Join(SELinuxFSPath, "enforce")
	if err := os.WriteFile(path, []byte(b), 0o644); err != nil {
		return fmt.Errorf("the running mode could not be set to %s: %w", mode, err)
	}
	got, err := selinuxGetenforce()
	if err != nil {
		return err
	}
	if got != mode {
		return fmt.Errorf("%s was written to %s and the running mode reads back %s", b, path, got)
	}
	return nil
}

func selinuxSetenforceFn(c *exec.Context, args *value.Map) (any, error) {
	v, _ := args.Get("mode")
	want, err := selinuxMode(v)
	if err != nil {
		return nil, err
	}
	if want == "Disabled" {
		return nil, errors.New("SELinux cannot be disabled while the node is running: it takes " +
			"SELINUX=disabled in " + SELinuxConfigPath + " and a reboot, and this module does not " +
			"write that file (Salt's setenforce does)")
	}
	if ok, why := selinuxEnabled(); !ok {
		return nil, errors.New(why)
	}
	current, err := selinuxGetenforce()
	if err != nil {
		return nil, err
	}
	if current == want {
		return iptablesMutateResult(c, false, fmt.Sprintf("SELinux is already %s.", want), nil), nil
	}
	changes := value.MapOf("mode", states.Change(current, want))
	if c.Test {
		return iptablesMutateResult(c, true, fmt.Sprintf("SELinux would be set from %s to %s until "+
			"the next boot.", current, want), changes), nil
	}
	if err := selinuxSetRunningMode(want); err != nil {
		return nil, err
	}
	return iptablesMutateResult(c, true, fmt.Sprintf("SELinux was set from %s to %s. %s was not "+
		"changed, so the next boot uses what it says.", current, want, SELinuxConfigPath), changes), nil
}

// ---- booleans ----

type selinuxBoolean struct {
	State, Default, Description string
}

// selinuxBooleanLine is one row of `semanage boolean -l`:
//
//	httpd_can_network_connect      (on   ,  off)  Allow HTTPD scripts ...
//
// State is the running value and Default the one in the policy store,
// which is what `setsebool -P` writes: captured, a runtime `setsebool on`
// reads (on, off), and -P on then runtime off reads (off, on). A name of
// thirty characters or more pushes the columns right by one space rather
// than truncating (`container_manage_public_content (off  ,  off)`), so
// the row is split on whitespace, not on columns.
var selinuxBooleanLine = regexp.MustCompile(`^(\S+)\s+\((on|off)\s*,\s*(on|off)\)\s*(.*)$`)

func parseSemanageBooleans(out string) map[string]selinuxBoolean {
	bools := map[string]selinuxBoolean{}
	for _, line := range strings.Split(out, "\n") {
		m := selinuxBooleanLine.FindStringSubmatch(strings.TrimRight(line, " \r"))
		if m == nil {
			continue
		}
		bools[m[1]] = selinuxBoolean{State: m[2], Default: m[3], Description: strings.TrimSpace(m[4])}
	}
	return bools
}

func selinuxBooleans(c *exec.Context) (map[string]selinuxBoolean, error) {
	if err := selinuxReady(c, "semanage"); err != nil {
		return nil, err
	}
	out, err := selinuxRun(c, "semanage", "boolean", "-l")
	if err != nil {
		return nil, err
	}
	bools := parseSemanageBooleans(out)
	if len(bools) == 0 {
		return nil, errors.New("semanage boolean -l listed no booleans, which a loaded policy always has")
	}
	return bools, nil
}

func selinuxBooleanMap(b selinuxBoolean) *value.Map {
	return value.MapOf("State", b.State, "Default", b.Default, "Description", b.Description)
}

// selinuxOnOff is Salt's reading of a boolean's value. YAML turns a bare
// `on` into true, so a bool is expected as often as a string.
func selinuxOnOff(v any) (string, error) {
	switch t := v.(type) {
	case bool:
		if t {
			return "on", nil
		}
		return "off", nil
	case int64:
		if t == 1 {
			return "on", nil
		}
		if t == 0 {
			return "off", nil
		}
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "on", "1", "true", "yes":
			return "on", nil
		case "off", "0", "false", "no":
			return "off", nil
		}
	}
	return "", fmt.Errorf("%v is not a boolean value: use on or off", v)
}

// selinuxBooleanChange is what setting one boolean would change, or nil.
func selinuxBooleanChange(b selinuxBoolean, want string, persist bool) *value.Map {
	change := value.NewMap(2)
	if b.State != want {
		change.Set("State", states.Change(b.State, want))
	}
	if persist && b.Default != want {
		change.Set("Default", states.Change(b.Default, want))
	}
	if change.Len() == 0 {
		return nil
	}
	return change
}

// selinuxSetBooleans sets every pair in one setsebool, which is atomic:
// captured on both hosts, `setsebool a=on nosuch=on` exits 255 and sets
// neither. The names have been checked against the listing before this
// is called, so that is a second line of defence rather than the first.
func selinuxSetBooleans(c *exec.Context, pairs map[string]string, persist bool) error {
	if err := selinuxReady(c, "setsebool"); err != nil {
		return err
	}
	argv := []string{"setsebool"}
	if persist {
		argv = append(argv, "-P")
	}
	names := make([]string, 0, len(pairs))
	for name := range pairs {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 1 {
		argv = append(argv, names[0], pairs[names[0]])
	} else {
		for _, name := range names {
			argv = append(argv, name+"="+pairs[name])
		}
	}
	_, err := selinuxRun(c, argv...)
	return err
}

// selinuxSetBooleansFn is setsebool and setsebools together: both check
// every name against the policy, change only what differs, and read the
// listing back.
func selinuxSetBooleansFn(c *exec.Context, pairs map[string]any, persist bool) (any, error) {
	if len(pairs) == 0 {
		return nil, errors.New("no boolean was named")
	}
	bools, err := selinuxBooleans(c)
	if err != nil {
		return nil, err
	}
	changes := value.NewMap(len(pairs))
	toSet := map[string]string{}
	names := make([]string, 0, len(pairs))
	for name := range pairs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		want, err := selinuxOnOff(pairs[name])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		current, ok := bools[name]
		if !ok {
			return nil, fmt.Errorf("%s is not a boolean this node's policy defines", name)
		}
		if change := selinuxBooleanChange(current, want, persist); change != nil {
			changes.Set(name, change)
			toSet[name] = want
		}
	}
	where := "the running policy"
	if persist {
		where = "the running policy and the policy store"
	}
	if len(toSet) == 0 {
		return iptablesMutateResult(c, false, fmt.Sprintf("Every boolean named is already as asked in %s.", where), nil), nil
	}
	if c.Test {
		return iptablesMutateResult(c, true, fmt.Sprintf("%d boolean(s) would be set in %s.", len(toSet), where), changes), nil
	}
	if err := selinuxSetBooleans(c, toSet, persist); err != nil {
		return nil, err
	}
	after, err := selinuxBooleans(c)
	if err != nil {
		return nil, err
	}
	for name, want := range toSet {
		if selinuxBooleanChange(after[name], want, persist) != nil {
			return nil, fmt.Errorf("setsebool exited 0 and %s still reads (%s, %s)", name, after[name].State, after[name].Default)
		}
	}
	return iptablesMutateResult(c, true, fmt.Sprintf("%d boolean(s) were set in %s.", len(toSet), where), changes), nil
}

// ---- policy modules ----

type selinuxModule struct {
	Name     string
	Priority int
	Kind     string
	Enabled  bool
}

// parseSemoduleFull reads `semodule -lfull`:
//
//	100 zabbix            pp
//	100 zabbix            pp  disabled
//	100 permissivedomains cil
//
// A module can be installed at more than one priority (AlmaLinux 8.10
// ships `cockpit` at both 100 and 200), and the highest is the one in
// the policy, so that is the one kept. The disabled row was captured by
// running `semodule -d zabbix` and `-e zabbix` on both lab hosts.
func parseSemoduleFull(out string) map[string]selinuxModule {
	mods := map[string]selinuxModule{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		prio, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		m := selinuxModule{Name: f[1], Priority: prio, Kind: f[2], Enabled: !(len(f) > 3 && f[3] == "disabled")}
		if have, ok := mods[m.Name]; ok && have.Priority > m.Priority {
			continue
		}
		mods[m.Name] = m
	}
	return mods
}

func selinuxModules(c *exec.Context) (map[string]selinuxModule, error) {
	if err := selinuxReady(c, "semodule"); err != nil {
		return nil, err
	}
	out, err := selinuxRun(c, "semodule", "-lfull")
	if err != nil {
		return nil, err
	}
	return parseSemoduleFull(out), nil
}

func selinuxModuleMap(m selinuxModule) *value.Map {
	return value.MapOf("Enabled", m.Enabled, "Priority", int64(m.Priority), "Kind", m.Kind)
}

// ---- file contexts ----

// selinuxFiletypes is semanage's -f letter and the words it lists the
// type under. All eight spellings were read out of `semanage fcontext
// -l` on both hosts; the letters `a` (by omission), `d` and `f` were
// the ones driven.
var selinuxFiletypes = map[string]string{
	"a": "all files",
	"f": "regular file",
	"d": "directory",
	"c": "character device",
	"b": "block device",
	"s": "socket",
	"l": "symbolic link",
	"p": "named pipe",
}

func selinuxFiletypeWords(letter string) (string, error) {
	if letter == "" {
		letter = "a"
	}
	words, ok := selinuxFiletypes[letter]
	if !ok {
		return "", fmt.Errorf("%q is not a file type: use one of a, f, d, c, b, s, l or p", letter)
	}
	return words, nil
}

type selinuxFcontext struct {
	Spec, Filetype string
	// Context is the whole context as listed, or `<<None>>` for a rule
	// that says "do not label this".
	Context                 string
	User, Role, Type, Level string
}

func (f selinuxFcontext) toMap() *value.Map {
	return value.MapOf("filespec", f.Spec, "filetype", f.Filetype,
		"sel_user", f.User, "sel_role", f.Role, "sel_type", f.Type, "sel_level", f.Level)
}

// selinuxContextParts splits user:role:type:level. The level is
// everything after the third colon, because an MLS range contains one
// (`s0-s0:c0.c1023`).
func selinuxContextParts(ctx string) (user, role, typ, level string, ok bool) {
	parts := strings.SplitN(ctx, ":", 4)
	if len(parts) != 4 {
		return "", "", "", "", false
	}
	return parts[0], parts[1], parts[2], parts[3], true
}

func selinuxContextMap(ctx string) *value.Map {
	user, role, typ, level, ok := selinuxContextParts(ctx)
	if !ok {
		return value.MapOf("context", ctx)
	}
	return value.MapOf("sel_user", user, "sel_role", role, "sel_type", typ, "sel_level", level)
}

// parseSemanageFcontexts reads `semanage fcontext -l` or `-l -C`:
//
//	SELinux fcontext                                   type               Context
//
//	/srv/halite-cap(/.*)?                              all files          system_u:object_r:httpd_sys_content_t:s0
//	/boot/\.journal                                    all files          <<None>>
//
//	SELinux Distribution fcontext Equivalence
//
//	/run = /var/run
//
// Local rules are listed in place, sorted among the policy's, and a
// local rule for a specification the policy also has *replaces* the
// policy's row rather than appearing beside it: with `/vicepa` changed
// to public_content_t, the full listing has one `/vicepa` row, the
// local one. So the full listing is the effective set, and `-C` is what
// says which of it is local. A row whose middle is not one of the eight
// file types is skipped rather than guessed at.
func parseSemanageFcontexts(out string) []selinuxFcontext {
	known := map[string]bool{}
	for _, words := range selinuxFiletypes {
		known[words] = true
	}
	var rules []selinuxFcontext
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || strings.HasPrefix(line, "SELinux ") || (len(f) == 3 && f[1] == "=") {
			continue
		}
		ftype := strings.Join(f[1:len(f)-1], " ")
		if !known[ftype] {
			continue
		}
		rule := selinuxFcontext{Spec: f[0], Filetype: ftype, Context: f[len(f)-1]}
		if user, role, typ, level, ok := selinuxContextParts(rule.Context); ok {
			rule.User, rule.Role, rule.Type, rule.Level = user, role, typ, level
		}
		rules = append(rules, rule)
	}
	return rules
}

func selinuxFcontexts(c *exec.Context, localOnly bool) ([]selinuxFcontext, error) {
	if err := selinuxReady(c, "semanage"); err != nil {
		return nil, err
	}
	argv := []string{"semanage", "fcontext", "-l"}
	if localOnly {
		argv = append(argv, "-C")
	}
	out, err := selinuxRun(c, argv...)
	if err != nil {
		return nil, err
	}
	return parseSemanageFcontexts(out), nil
}

func selinuxFindFcontext(rules []selinuxFcontext, spec, filetype string) (selinuxFcontext, bool) {
	for _, r := range rules {
		if r.Spec == spec && r.Filetype == filetype {
			return r, true
		}
	}
	return selinuxFcontext{}, false
}

// selinuxFilespec refuses the two spellings semanage accepts and stores
// a rule for that can never match a file. See registerSELinux.
func selinuxFilespec(spec string) error {
	switch {
	case spec == "":
		return errors.New("a file specification is needed, such as /srv/www(/.*)?")
	case !strings.HasPrefix(spec, "/"):
		return fmt.Errorf("%q is not absolute; semanage stores a relative specification and "+
			"nothing is ever labelled by it", spec)
	case len(spec) > 1 && strings.HasSuffix(spec, "/"):
		return fmt.Errorf("%q ends in /; semanage stores it and it never matches the directory "+
			"(write %q)", spec, strings.TrimRight(spec, "/"))
	}
	return nil
}

// selinuxFcontextWant is what an add was asked for.
type selinuxFcontextWant struct {
	Spec, Letter, Filetype, Type, User, Level string
}

func selinuxFcontextArgs(args *value.Map, needType bool) (selinuxFcontextWant, error) {
	w := selinuxFcontextWant{
		Spec:   strings.TrimSpace(states.Str(args, "name", "")),
		Letter: strings.TrimSpace(states.Str(args, "filetype", "a")),
		Type:   strings.TrimSpace(states.Str(args, "sel_type", "")),
		User:   strings.TrimSpace(states.Str(args, "sel_user", "")),
		Level:  strings.TrimSpace(states.Str(args, "sel_level", "")),
	}
	if err := selinuxFilespec(w.Spec); err != nil {
		return w, err
	}
	words, err := selinuxFiletypeWords(w.Letter)
	if err != nil {
		return w, err
	}
	w.Filetype = words
	if needType && w.Type == "" {
		return w, errors.New("sel_type is needed, such as httpd_sys_content_t")
	}
	return w, nil
}

// matches reports whether a listed rule already says what was asked. The
// user and level are compared only when they were given, because
// semanage fills them in (system_u, s0) when they are not.
func (w selinuxFcontextWant) matches(r selinuxFcontext) bool {
	return r.Type == w.Type && (w.User == "" || r.User == w.User) && (w.Level == "" || r.Level == w.Level)
}

func (w selinuxFcontextWant) newMap() *value.Map {
	m := value.MapOf("filetype", w.Filetype, "sel_type", w.Type)
	if w.User != "" {
		m.Set("sel_user", w.User)
	}
	if w.Level != "" {
		m.Set("sel_level", w.Level)
	}
	return m
}

// selinuxFcontextAdd brings one rule to what was asked, and returns the
// change, or nil when it already said so.
//
// A local rule is modified with -m. Anything else is added with -a,
// which covers a specification the policy already has: captured on both
// hosts, `-a` over `/vicepa` printed "already defined, modifying
// instead", exited 0 and left a local rule that replaces the policy's.
// The listing is read back afterwards, because an exit status is not
// the thing being claimed.
func selinuxFcontextAdd(c *exec.Context, w selinuxFcontextWant) (*value.Map, error) {
	all, err := selinuxFcontexts(c, false)
	if err != nil {
		return nil, err
	}
	have, found := selinuxFindFcontext(all, w.Spec, w.Filetype)
	if found && w.matches(have) {
		return nil, nil
	}
	var old any
	if found {
		old = have.toMap()
	}
	change := value.MapOf(w.Spec, states.Change(old, w.newMap()))
	if c.Test {
		return change, nil
	}
	local, err := selinuxFcontexts(c, true)
	if err != nil {
		return nil, err
	}
	op := "-a"
	if _, isLocal := selinuxFindFcontext(local, w.Spec, w.Filetype); isLocal {
		op = "-m"
	}
	argv := []string{"semanage", "fcontext", op}
	// `-f a` is never passed: "all files" is semanage's default, and the
	// default is what was driven.
	if w.Letter != "a" {
		argv = append(argv, "-f", w.Letter)
	}
	argv = append(argv, "-t", w.Type)
	if w.User != "" {
		argv = append(argv, "-s", w.User)
	}
	if w.Level != "" {
		argv = append(argv, "-r", w.Level)
	}
	argv = append(argv, w.Spec)
	if _, err := selinuxRun(c, argv...); err != nil {
		return nil, err
	}
	after, err := selinuxFcontexts(c, false)
	if err != nil {
		return nil, err
	}
	if got, ok := selinuxFindFcontext(after, w.Spec, w.Filetype); !ok || !w.matches(got) {
		return nil, fmt.Errorf("semanage exited 0 and the listing does not show %s (%s) as %s", w.Spec, w.Filetype, w.Type)
	}
	return change, nil
}

// selinuxFcontextDelete removes a local rule, and returns the change, or
// nil when there is nothing to remove.
//
// Only a local rule can go. semanage refuses the policy's own
// (`ValueError: File context for /vicepb is defined in policy, cannot be
// deleted`, both hosts), so that is refused here before it is asked,
// with the same meaning. Deleting a local rule that overrode the
// policy's brings the policy's back, and the comment says so.
func selinuxFcontextDelete(c *exec.Context, w selinuxFcontextWant) (*value.Map, string, error) {
	all, err := selinuxFcontexts(c, false)
	if err != nil {
		return nil, "", err
	}
	have, found := selinuxFindFcontext(all, w.Spec, w.Filetype)
	if !found {
		return nil, fmt.Sprintf("No rule for %s (%s) exists.", w.Spec, w.Filetype), nil
	}
	if w.Type != "" && have.Type != w.Type {
		return nil, fmt.Sprintf("The rule for %s (%s) gives %s, not %s, so there is none to remove.",
			w.Spec, w.Filetype, have.Type, w.Type), nil
	}
	local, err := selinuxFcontexts(c, true)
	if err != nil {
		return nil, "", err
	}
	if _, isLocal := selinuxFindFcontext(local, w.Spec, w.Filetype); !isLocal {
		return nil, "", fmt.Errorf("the rule for %s (%s) is defined in the policy, not added locally, "+
			"and semanage cannot delete it", w.Spec, w.Filetype)
	}
	change := value.MapOf(w.Spec, states.Change(have.toMap(), nil))
	if c.Test {
		return change, fmt.Sprintf("The local rule for %s (%s) would be deleted.", w.Spec, w.Filetype), nil
	}
	argv := []string{"semanage", "fcontext", "-d"}
	if w.Letter != "a" {
		argv = append(argv, "-f", w.Letter)
	}
	argv = append(argv, w.Spec)
	if _, err := selinuxRun(c, argv...); err != nil {
		return nil, "", err
	}
	after, err := selinuxFcontexts(c, false)
	if err != nil {
		return nil, "", err
	}
	comment := fmt.Sprintf("The local rule for %s (%s) was deleted.", w.Spec, w.Filetype)
	if back, ok := selinuxFindFcontext(after, w.Spec, w.Filetype); ok {
		if back.Context == have.Context {
			return nil, "", fmt.Errorf("semanage exited 0 and %s (%s) is still listed as %s", w.Spec, w.Filetype, have.Context)
		}
		comment += fmt.Sprintf(" The policy's own rule applies again: %s.", back.Context)
		change = value.MapOf(w.Spec, states.Change(have.toMap(), back.toMap()))
	}
	return change, comment, nil
}

// ---- restorecon ----

type selinuxRelabel struct{ Path, From, To string }

// parseRestorecon reads restorecon -v's lines, which say `Would relabel`
// under -n and `Relabeled` without it:
//
//	Would relabel /srv/t/top from unconfined_u:object_r:var_t:s0 to unconfined_u:object_r:public_content_t:s0
//	Relabeled /srv/t/top from user_u:object_r:httpd_sys_content_t:s0 to system_u:object_r:public_content_t:s0
//
// Salt parses `restorecon reset (.*) context (.*)->(.*)`, which is how
// policycoreutils printed this before 2.7; neither host printed it, so
// Salt's `fcontext_apply_policy` reports no changes on either. The path
// is taken up to the last " from ", because a context has no spaces and
// a path may.
func parseRestorecon(out string) []selinuxRelabel {
	var changes []selinuxRelabel
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		var rest string
		switch {
		case strings.HasPrefix(line, "Would relabel "):
			rest = strings.TrimPrefix(line, "Would relabel ")
		case strings.HasPrefix(line, "Relabeled "):
			rest = strings.TrimPrefix(line, "Relabeled ")
		default:
			continue
		}
		from := strings.LastIndex(rest, " from ")
		if from < 0 {
			continue
		}
		ctxs := strings.SplitN(rest[from+len(" from "):], " to ", 2)
		if len(ctxs) != 2 {
			continue
		}
		changes = append(changes, selinuxRelabel{Path: rest[:from], From: ctxs[0], To: strings.TrimSpace(ctxs[1])})
	}
	return changes
}

func selinuxRelabelChanges(relabels []selinuxRelabel) *value.Map {
	out := value.NewMap(len(relabels))
	for _, r := range relabels {
		out.Set(r.Path, states.Change(r.From, r.To))
	}
	return out
}

// selinuxRestorecon asks restorecon what it would change, or has it
// change it.
//
// **Both use -F**, and that is the point. Salt's
// `fcontext_policy_is_applied` asks `restorecon -n -v` and its
// `fcontext_apply_policy` runs `restorecon -v -F`, which are different
// questions: without -F restorecon leaves the user, role and level alone,
// with it it resets the whole context. Captured on both hosts, a file
// whose user was chcon'd to user_u reads `user_u:...` as the target
// without -F and `system_u:...` with it. So Salt's check can say
// "applied" about a file its apply would still change. Here the check
// and the change are the same command but for -n.
func selinuxRestorecon(c *exec.Context, path string, recursive, dryRun bool) ([]selinuxRelabel, error) {
	if err := selinuxReady(c, "restorecon"); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%q is not an absolute path", path)
	}
	if _, err := os.Lstat(path); err != nil {
		return nil, err
	}
	argv := []string{"restorecon"}
	if dryRun {
		argv = append(argv, "-n")
	}
	argv = append(argv, "-v", "-F")
	if recursive {
		argv = append(argv, "-R")
	}
	argv = append(argv, path)
	out, err := selinuxRun(c, argv...)
	if err != nil {
		return nil, err
	}
	return parseRestorecon(out), nil
}

// ---- ports ----

type selinuxPort struct {
	Type, Protocol string
	Ports          []string
}

// parseSemanagePorts reads `semanage port -l` or `-l -C`:
//
//	http_port_t                    tcp      8080, 18990-18995, 18999, 80, 81, 443
//
// A local rule is merged into its type's row, and **a local rule over a
// port the policy already has does not remove it from the policy's
// row**: captured on both hosts with tcp/8080 changed to http_port_t,
// 8080 is listed under http_cache_port_t *and* http_port_t. Which of
// the two is in force is only answerable with `-C`, which is why
// selinuxPortLookup takes both. That is also where Salt goes wrong: its
// `port_get_policy` greps the full listing for type and port, so after
// such a change it still finds tcp/8080 as http_cache_port_t, and
// `port_policy_present` reports a rule in place that is not.
func parseSemanagePorts(out string) []selinuxPort {
	var rows []selinuxPort
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || strings.HasPrefix(line, "SELinux ") {
			continue
		}
		var ports []string
		for _, p := range strings.Split(strings.Join(f[2:], " "), ",") {
			if p = strings.TrimSpace(p); p != "" {
				ports = append(ports, p)
			}
		}
		rows = append(rows, selinuxPort{Type: f[0], Protocol: f[1], Ports: ports})
	}
	return rows
}

func selinuxPorts(c *exec.Context, localOnly bool) ([]selinuxPort, error) {
	if err := selinuxReady(c, "semanage"); err != nil {
		return nil, err
	}
	argv := []string{"semanage", "port", "-l"}
	if localOnly {
		argv = append(argv, "-C")
	}
	out, err := selinuxRun(c, argv...)
	if err != nil {
		return nil, err
	}
	return parseSemanagePorts(out), nil
}

// selinuxPortLookup is the type a protocol and port (or range, exactly
// as semanage lists it) is in, whether that comes from a local rule, and
// whether anything lists it at all. The local rule wins; see
// parseSemanagePorts.
func selinuxPortLookup(all, local []selinuxPort, proto, port string) (typ string, isLocal, found bool) {
	if typ, ok := selinuxPortIn(local, proto, port); ok {
		return typ, true, true
	}
	if typ, ok := selinuxPortIn(all, proto, port); ok {
		return typ, false, true
	}
	return "", false, false
}

func selinuxPortIn(rows []selinuxPort, proto, port string) (string, bool) {
	for _, row := range rows {
		if row.Protocol != proto {
			continue
		}
		for _, p := range row.Ports {
			if p == port {
				return row.Type, true
			}
		}
	}
	return "", false
}

var selinuxPortName = regexp.MustCompile(`^(tcp|udp)/(\d+)(?:-(\d+))?$`)

// selinuxPortSpec reads Salt's `tcp/8080` or `tcp/8080-8090` spelling, or
// protocol and port given separately, and returns the port in
// semanage's own spelling: a range whose ends are equal is listed as
// the one port (captured: `-a ... 18998-18998` is listed as `18998`).
// A reversed range and a port above 65535 are refused here, because
// semanage's own refusals are five lines of libsepol about "No such file
// or directory".
func selinuxPortSpec(name, protocol, port string) (string, string, error) {
	m := selinuxPortName.FindStringSubmatch(strings.TrimSpace(name))
	if m == nil && protocol != "" && port != "" {
		m = selinuxPortName.FindStringSubmatch(strings.TrimSpace(protocol) + "/" + strings.TrimSpace(port))
	}
	if m == nil {
		return "", "", fmt.Errorf("%q is not a port: write tcp/8080 or udp/9000-9010, or give protocol and port", name)
	}
	low, _ := strconv.Atoi(m[2])
	high := low
	if m[3] != "" {
		high, _ = strconv.Atoi(m[3])
	}
	if low < 1 || high > 65535 || low > high {
		return "", "", fmt.Errorf("%s is not a port range semanage will take: ports run from 1 to 65535, low end first", name)
	}
	if low == high {
		return m[1], strconv.Itoa(low), nil
	}
	return m[1], fmt.Sprintf("%d-%d", low, high), nil
}

func selinuxPortMap(typ, proto, port string, isLocal bool) *value.Map {
	return value.MapOf("sel_type", typ, "protocol", proto, "port", port, "local", isLocal)
}

func selinuxPortArgs(args *value.Map) (string, string, error) {
	return selinuxPortSpec(states.Str(args, "name", ""), states.Str(args, "protocol", ""), states.Str(args, "port", ""))
}

// selinuxPortAdd brings one port to a type, and returns the change, or
// nil when it is already there. -m for a local rule, -a otherwise, which
// over a policy port changes it (captured: "Port tcp/8080 already
// defined, modifying instead", exit 0, both hosts).
func selinuxPortAdd(c *exec.Context, proto, port, typ, rangeLevel string) (*value.Map, error) {
	all, err := selinuxPorts(c, false)
	if err != nil {
		return nil, err
	}
	local, err := selinuxPorts(c, true)
	if err != nil {
		return nil, err
	}
	have, isLocal, found := selinuxPortLookup(all, local, proto, port)
	if found && have == typ {
		return nil, nil
	}
	var old any
	if found {
		old = selinuxPortMap(have, proto, port, isLocal)
	}
	change := value.MapOf(proto+"/"+port, states.Change(old, selinuxPortMap(typ, proto, port, true)))
	if c.Test {
		return change, nil
	}
	op := "-a"
	if isLocal {
		op = "-m"
	}
	argv := []string{"semanage", "port", op, "-t", typ, "-p", proto}
	if rangeLevel != "" {
		argv = append(argv, "-r", rangeLevel)
	}
	argv = append(argv, port)
	if _, err := selinuxRun(c, argv...); err != nil {
		return nil, err
	}
	all, err = selinuxPorts(c, false)
	if err != nil {
		return nil, err
	}
	if local, err = selinuxPorts(c, true); err != nil {
		return nil, err
	}
	if got, _, _ := selinuxPortLookup(all, local, proto, port); got != typ {
		return nil, fmt.Errorf("semanage exited 0 and %s/%s is listed as %q, not %s", proto, port, got, typ)
	}
	return change, nil
}

// selinuxPortDelete removes a local port rule. As with file contexts,
// the policy's own cannot be removed (`ValueError: Port tcp/80 is
// defined in policy, cannot be deleted`, both hosts), so it is refused
// first.
func selinuxPortDelete(c *exec.Context, proto, port, typ string) (*value.Map, string, error) {
	all, err := selinuxPorts(c, false)
	if err != nil {
		return nil, "", err
	}
	local, err := selinuxPorts(c, true)
	if err != nil {
		return nil, "", err
	}
	have, isLocal, found := selinuxPortLookup(all, local, proto, port)
	if !found {
		return nil, fmt.Sprintf("No rule for %s/%s exists.", proto, port), nil
	}
	if typ != "" && have != typ {
		return nil, fmt.Sprintf("%s/%s is %s, not %s, so there is no such rule to remove.", proto, port, have, typ), nil
	}
	if !isLocal {
		return nil, "", fmt.Errorf("%s/%s is %s by the policy, not by a local rule, and semanage cannot delete it", proto, port, have)
	}
	change := value.MapOf(proto+"/"+port, states.Change(selinuxPortMap(have, proto, port, true), nil))
	if c.Test {
		return change, fmt.Sprintf("The local rule making %s/%s %s would be deleted.", proto, port, have), nil
	}
	if _, err := selinuxRun(c, "semanage", "port", "-d", "-p", proto, port); err != nil {
		return nil, "", err
	}
	all, err = selinuxPorts(c, false)
	if err != nil {
		return nil, "", err
	}
	if local, err = selinuxPorts(c, true); err != nil {
		return nil, "", err
	}
	comment := fmt.Sprintf("The local rule making %s/%s %s was deleted.", proto, port, have)
	if _, stillLocal, _ := selinuxPortLookup(all, local, proto, port); stillLocal {
		return nil, "", fmt.Errorf("semanage exited 0 and %s/%s still has a local rule", proto, port)
	}
	if back, _, ok := selinuxPortLookup(all, local, proto, port); ok {
		comment += fmt.Sprintf(" The policy's own type applies again: %s.", back)
		change = value.MapOf(proto+"/"+port, states.Change(selinuxPortMap(have, proto, port, true), selinuxPortMap(back, proto, port, false)))
	}
	return change, comment, nil
}

// ---- the execution module ----

func selinuxExecModules() []exec.Module {
	read := func(function, doc string, params []signature.Param, root bool, fn exec.Func) exec.Module {
		sig := signature.Signature{
			Module: "selinux", Function: function, Doc: doc, Params: params,
			TestMode: signature.TestNotApplicable, Platforms: linuxOnly, Section: "15.2",
		}
		if root {
			sig.Privileges = []string{"root"}
		}
		return exec.Module{Sig: sig, Fn: fn}
	}
	mutate := func(function, doc string, params []signature.Param, fn exec.Func) exec.Module {
		return exec.Module{
			Sig: signature.Signature{
				Module: "selinux", Function: function, Doc: doc, Params: params,
				Mutates: true, TestMode: signature.TestReliable, Privileges: []string{"root"},
				Platforms: linuxOnly, Section: "15.2",
			},
			Fn: fn,
		}
	}
	boolean := req("boolean", signature.String, "The boolean, such as httpd_can_network_connect.")
	persist := opt("persist", signature.Bool, false,
		"Also write the value to the policy store (setsebool -P), so it survives a reboot. Off by default, as in Salt.")
	filespec := req("name", signature.String, "The file specification, a regular expression as semanage takes it, such as /srv/www(/.*)?")
	filetype := opt("filetype", signature.String, "a",
		"The file type: a (all files), f, d, c, b, s, l or p.")
	selUser := opt("sel_user", signature.String, "", "The SELinux user. semanage uses system_u when none is given.")
	selLevel := opt("sel_level", signature.String, "", "The MLS level. semanage uses s0 when none is given.")
	portName := req("name", signature.String, "The port as protocol/port, such as tcp/8080 or udp/9000-9010.")
	protocol := opt("protocol", signature.String, "", "tcp or udp, when name does not say.")
	port := opt("port", signature.String, "", "The port or range, when name does not say.")
	recursive := opt("recursive", signature.Bool, false, "Descend into a directory (restorecon -R).")

	return []exec.Module{
		read("getenforce", "Return the running mode: Enforcing, Permissive, or Disabled when SELinux is not running.",
			nil, false, func(c *exec.Context, _ *value.Map) (any, error) { return selinuxGetenforce() }),
		read("getconfig", "Return the mode "+SELinuxConfigPath+" sets at boot, or nothing if the file does not say.",
			nil, false, func(c *exec.Context, _ *value.Map) (any, error) {
				mode, err := selinuxGetconfig()
				if err != nil || mode == "" {
					return nil, err
				}
				return mode, nil
			}),
		mutate("setenforce", "Set the running mode to Enforcing or Permissive until the next boot. Never writes "+SELinuxConfigPath+", and so cannot disable SELinux.",
			[]signature.Param{req("mode", signature.Any, "Enforcing, Permissive, 1 or 0.")}, selinuxSetenforceFn),

		read("list_sebool", "Return every boolean with its running value (State), its value in the policy store (Default), and its description.",
			nil, true, func(c *exec.Context, _ *value.Map) (any, error) {
				bools, err := selinuxBooleans(c)
				if err != nil {
					return nil, err
				}
				out := value.NewMap(len(bools))
				names := make([]string, 0, len(bools))
				for name := range bools {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					out.Set(name, selinuxBooleanMap(bools[name]))
				}
				return out, nil
			}),
		read("getsebool", "Return one boolean's State, Default and Description, or an empty mapping if the policy has no such boolean.",
			[]signature.Param{boolean}, true, func(c *exec.Context, args *value.Map) (any, error) {
				bools, err := selinuxBooleans(c)
				if err != nil {
					return nil, err
				}
				if b, ok := bools[strings.TrimSpace(states.Str(args, "boolean", ""))]; ok {
					return selinuxBooleanMap(b), nil
				}
				return value.NewMap(0), nil
			}),
		mutate("setsebool", "Set one boolean on or off, in the running policy and, with persist, in the policy store.",
			[]signature.Param{boolean, req("value", signature.Any, "on or off."), persist},
			func(c *exec.Context, args *value.Map) (any, error) {
				v, _ := args.Get("value")
				return selinuxSetBooleansFn(c, map[string]any{strings.TrimSpace(states.Str(args, "boolean", "")): v},
					states.Bool(args, "persist", false))
			}),
		mutate("setsebools", "Set several booleans in one setsebool, which sets all of them or none.",
			[]signature.Param{req("pairs", signature.Map, "A mapping of boolean to on or off."), persist},
			func(c *exec.Context, args *value.Map) (any, error) {
				m := states.Mapping(args, "pairs")
				pairs := map[string]any{}
				if m != nil {
					for _, e := range m.Entries() {
						pairs[strings.TrimSpace(value.KeyString(e.Key))] = e.Val
					}
				}
				return selinuxSetBooleansFn(c, pairs, states.Bool(args, "persist", false))
			}),

		read("list_semod", "Return every policy module at the priority in force, whether it is enabled, and its kind (pp or cil).",
			nil, true, func(c *exec.Context, _ *value.Map) (any, error) {
				mods, err := selinuxModules(c)
				if err != nil {
					return nil, err
				}
				names := make([]string, 0, len(mods))
				for name := range mods {
					names = append(names, name)
				}
				sort.Strings(names)
				out := value.NewMap(len(mods))
				for _, name := range names {
					out.Set(name, selinuxModuleMap(mods[name]))
				}
				return out, nil
			}),
		read("getsemod", "Return one policy module's entry from list_semod, or an empty mapping if it is not installed.",
			[]signature.Param{req("module", signature.String, "The module, such as zabbix.")}, true,
			func(c *exec.Context, args *value.Map) (any, error) {
				mods, err := selinuxModules(c)
				if err != nil {
					return nil, err
				}
				if m, ok := mods[strings.TrimSpace(states.Str(args, "module", ""))]; ok {
					return selinuxModuleMap(m), nil
				}
				return value.NewMap(0), nil
			}),

		read("fcontext_get_policy", "Return the rule in force for a file specification, or nothing. filetype, sel_type, sel_user and sel_level narrow the match when given.",
			[]signature.Param{filespec, opt("filetype", signature.String, "", "The file type letter, or empty for any."),
				opt("sel_type", signature.String, "", "Match only this type."), selUser, selLevel}, true,
			selinuxFcontextGetFn),
		mutate("fcontext_add_policy", "Add a file-context rule, or change the one for the same specification and file type. Does not relabel anything; fcontext_apply_policy does.",
			[]signature.Param{filespec, filetype, req("sel_type", signature.String, "The type, such as httpd_sys_content_t."), selUser, selLevel},
			func(c *exec.Context, args *value.Map) (any, error) {
				w, err := selinuxFcontextArgs(args, true)
				if err != nil {
					return nil, err
				}
				change, err := selinuxFcontextAdd(c, w)
				if err != nil {
					return nil, err
				}
				if change == nil {
					return iptablesMutateResult(c, false, fmt.Sprintf("The rule for %s (%s) is already %s.", w.Spec, w.Filetype, w.Type), nil), nil
				}
				return iptablesMutateResult(c, true, fmt.Sprintf("The rule for %s (%s) was set to %s.", w.Spec, w.Filetype, w.Type), change), nil
			}),
		mutate("fcontext_delete_policy", "Delete a locally added file-context rule. A rule the policy itself defines cannot be deleted and is refused.",
			[]signature.Param{filespec, filetype, opt("sel_type", signature.String, "", "Delete only if the rule gives this type.")},
			func(c *exec.Context, args *value.Map) (any, error) {
				w, err := selinuxFcontextArgs(args, false)
				if err != nil {
					return nil, err
				}
				change, comment, err := selinuxFcontextDelete(c, w)
				if err != nil {
					return nil, err
				}
				return iptablesMutateResult(c, change != nil, comment, change), nil
			}),
		read("fcontext_policy_is_applied", "Return the relabelling restorecon -F would do to a path, as {path: {old, new}}; empty when every label is as the rules say.",
			[]signature.Param{req("name", signature.Path, "The path."), recursive}, true,
			func(c *exec.Context, args *value.Map) (any, error) {
				relabels, err := selinuxRestorecon(c, states.Str(args, "name", ""), states.Bool(args, "recursive", false), true)
				if err != nil {
					return nil, err
				}
				return selinuxRelabelChanges(relabels), nil
			}),
		mutate("fcontext_apply_policy", "Relabel a path to what the rules say (restorecon -F), and return what changed.",
			[]signature.Param{req("name", signature.Path, "The path."), recursive},
			func(c *exec.Context, args *value.Map) (any, error) {
				path := states.Str(args, "name", "")
				rec := states.Bool(args, "recursive", false)
				relabels, err := selinuxRestorecon(c, path, rec, true)
				if err != nil {
					return nil, err
				}
				if len(relabels) == 0 {
					return iptablesMutateResult(c, false, fmt.Sprintf("%s is labelled as the rules say.", path), nil), nil
				}
				if c.Test {
					return iptablesMutateResult(c, true, fmt.Sprintf("%d path(s) would be relabelled.", len(relabels)),
						selinuxRelabelChanges(relabels)), nil
				}
				done, err := selinuxRestorecon(c, path, rec, false)
				if err != nil {
					return nil, err
				}
				return iptablesMutateResult(c, len(done) > 0, fmt.Sprintf("%d path(s) were relabelled.", len(done)),
					selinuxRelabelChanges(done)), nil
			}),

		read("port_get_policy", "Return the type a port is in, and whether a local rule put it there; nothing when no rule lists it, or when sel_type is given and it is another type.",
			[]signature.Param{portName, opt("sel_type", signature.String, "", "Match only this type."), protocol, port}, true,
			func(c *exec.Context, args *value.Map) (any, error) {
				proto, p, err := selinuxPortArgs(args)
				if err != nil {
					return nil, err
				}
				all, err := selinuxPorts(c, false)
				if err != nil {
					return nil, err
				}
				local, err := selinuxPorts(c, true)
				if err != nil {
					return nil, err
				}
				typ, isLocal, found := selinuxPortLookup(all, local, proto, p)
				want := strings.TrimSpace(states.Str(args, "sel_type", ""))
				if !found || (want != "" && typ != want) {
					return nil, nil
				}
				return selinuxPortMap(typ, proto, p, isLocal), nil
			}),
		mutate("port_add_policy", "Put a port or range into a type, adding a local rule or changing the one there.",
			[]signature.Param{portName, req("sel_type", signature.String, "The port type, such as http_port_t."), protocol, port,
				opt("sel_range", signature.String, "", "The MLS range.")},
			func(c *exec.Context, args *value.Map) (any, error) {
				proto, p, err := selinuxPortArgs(args)
				if err != nil {
					return nil, err
				}
				typ := strings.TrimSpace(states.Str(args, "sel_type", ""))
				change, err := selinuxPortAdd(c, proto, p, typ, strings.TrimSpace(states.Str(args, "sel_range", "")))
				if err != nil {
					return nil, err
				}
				if change == nil {
					return iptablesMutateResult(c, false, fmt.Sprintf("%s/%s is already %s.", proto, p, typ), nil), nil
				}
				return iptablesMutateResult(c, true, fmt.Sprintf("%s/%s was put into %s.", proto, p, typ), change), nil
			}),
		mutate("port_delete_policy", "Delete a local port rule. A port the policy itself assigns cannot be deleted and is refused.",
			[]signature.Param{portName, protocol, port},
			func(c *exec.Context, args *value.Map) (any, error) {
				proto, p, err := selinuxPortArgs(args)
				if err != nil {
					return nil, err
				}
				change, comment, err := selinuxPortDelete(c, proto, p, "")
				if err != nil {
					return nil, err
				}
				return iptablesMutateResult(c, change != nil, comment, change), nil
			}),
	}
}

func selinuxFcontextGetFn(c *exec.Context, args *value.Map) (any, error) {
	spec := strings.TrimSpace(states.Str(args, "name", ""))
	var words string
	if letter := strings.TrimSpace(states.Str(args, "filetype", "")); letter != "" {
		var err error
		if words, err = selinuxFiletypeWords(letter); err != nil {
			return nil, err
		}
	}
	rules, err := selinuxFcontexts(c, false)
	if err != nil {
		return nil, err
	}
	typ := strings.TrimSpace(states.Str(args, "sel_type", ""))
	user := strings.TrimSpace(states.Str(args, "sel_user", ""))
	level := strings.TrimSpace(states.Str(args, "sel_level", ""))
	for _, r := range rules {
		if r.Spec != spec || (words != "" && r.Filetype != words) ||
			(typ != "" && r.Type != typ) || (user != "" && r.User != user) || (level != "" && r.Level != level) {
			continue
		}
		return r.toMap(), nil
	}
	return nil, nil
}

// ---- file.get_selinux_context and file.set_selinux_context ----

// selinuxFileContext reads a path's own context, not its target's.
//
// `stat -c %C`, without -L, which is what Salt runs: captured, for a
// symlink it prints the link's context (`unconfined_u:...`) where `-L`
// prints the target's (`system_u:...`). On a node without SELinux it
// prints `?` and exits 1 with "failed to get security context ... No
// data available" (Debian 13), so a non-zero exit is an error here --
// Salt returns a sentence in place of a context, which a caller
// comparing contexts reads as one.
func selinuxFileContext(c *exec.Context, path string) (string, error) {
	if c.Which("stat") == "" {
		return "", errors.New("stat is not installed; it is in coreutils")
	}
	res, err := c.Run(exec.Command{Argv: []string{"stat", "-c", "%C", path}, IgnoreExitCode: true})
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		msg := selinuxToolMessage(res.Stderr)
		if ok, why := selinuxEnabled(); !ok {
			msg += " (" + why + ")"
		}
		return "", fmt.Errorf("%s has no SELinux context to read: %s", path, msg)
	}
	return strings.TrimSpace(res.Stdout), nil
}

func selinuxFileModules() []exec.Module {
	return []exec.Module{
		{
			Sig: signature.Signature{
				Module: "file", Function: "get_selinux_context",
				Doc: "Return a path's SELinux context, such as system_u:object_r:etc_t:s0. A symlink's own, not its target's.",
				Params: []signature.Param{
					req("path", signature.Path, "The path."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				path := states.Str(args, "path", "")
				if _, err := os.Lstat(path); err != nil {
					return nil, err
				}
				return selinuxFileContext(c, path)
			},
		},
		{
			Sig: signature.Signature{
				Module: "file", Function: "set_selinux_context",
				Doc: "Change a path's SELinux context with chcon, and with persist also add the file-context rule that keeps it across a relabel.",
				Params: []signature.Param{
					req("path", signature.Path, "The path. Not a symlink: chcon would change its target, and get_selinux_context reads the link."),
					opt("user", signature.String, "", "The SELinux user."),
					opt("role", signature.String, "", "The role."),
					opt("type", signature.String, "", "The type."),
					opt("range", signature.String, "", "The MLS range, such as s0."),
					opt("persist", signature.Bool, false, "Also add a semanage fcontext rule for the path, so a relabel keeps the type. Needs type; a role cannot be persisted."),
				},
				Mutates: true, TestMode: signature.TestReliable, Privileges: []string{"root"},
				Platforms: linuxOnly,
				Section:   "15.2",
			},
			Fn: selinuxSetFileContextFn,
		},
	}
}

func selinuxSetFileContextFn(c *exec.Context, args *value.Map) (any, error) {
	path := states.Str(args, "path", "")
	want := [4]string{
		strings.TrimSpace(states.Str(args, "user", "")),
		strings.TrimSpace(states.Str(args, "role", "")),
		strings.TrimSpace(states.Str(args, "type", "")),
		strings.TrimSpace(states.Str(args, "range", "")),
	}
	if want == [4]string{} {
		return nil, errors.New("give at least one of user, role, type and range")
	}
	persist := states.Bool(args, "persist", false)
	if persist && want[2] == "" {
		return nil, errors.New("persist needs a type: a file-context rule is a type, with a user and range optional")
	}
	if persist && want[1] != "" {
		return nil, errors.New("persist cannot keep a role: a file-context rule has no role of its own (it is always object_r)")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symlink; chcon would change its target and get_selinux_context reads the link, "+
			"so name the target", path)
	}
	if err := selinuxReady(c, "chcon"); err != nil {
		return nil, err
	}
	current, err := selinuxFileContext(c, path)
	if err != nil {
		return nil, err
	}
	user, role, typ, level, ok := selinuxContextParts(current)
	if !ok {
		return nil, fmt.Errorf("%s's context %q is not user:role:type:level", path, current)
	}
	have := [4]string{user, role, typ, level}
	target := have
	for i, v := range want {
		if v != "" {
			target[i] = v
		}
	}
	newCtx := strings.Join(target[:], ":")
	changes := value.NewMap(2)
	if newCtx != current {
		changes.Set(path, states.Change(current, newCtx))
	}

	var rule selinuxFcontextWant
	if persist {
		rule = selinuxFcontextWant{Spec: path, Letter: "a", Filetype: "all files", Type: want[2], User: want[0], Level: want[3]}
		if err := selinuxFilespec(path); err != nil {
			return nil, err
		}
		cc := *c
		cc.Test = true
		ruleChange, err := selinuxFcontextAdd(&cc, rule)
		if err != nil {
			return nil, err
		}
		if ruleChange != nil {
			changes.Set("fcontext", ruleChange)
		}
	}
	if changes.Len() == 0 {
		return iptablesMutateResult(c, false, fmt.Sprintf("%s is already %s.", path, current), nil), nil
	}
	if c.Test {
		return iptablesMutateResult(c, true, fmt.Sprintf("%s would be changed to %s.", path, newCtx), changes), nil
	}
	if persist {
		if _, err := selinuxFcontextAdd(c, rule); err != nil {
			return nil, err
		}
	}
	if newCtx != current {
		argv := []string{"chcon"}
		for i, flag := range []string{"-u", "-r", "-t", "-l"} {
			if want[i] != "" {
				argv = append(argv, flag, want[i])
			}
		}
		argv = append(argv, path)
		if _, err := selinuxRun(c, argv...); err != nil {
			return nil, err
		}
		after, err := selinuxFileContext(c, path)
		if err != nil {
			return nil, err
		}
		if after != newCtx {
			return nil, fmt.Errorf("chcon exited 0 and %s reads %s, not %s", path, after, newCtx)
		}
	}
	return iptablesMutateResult(c, true, fmt.Sprintf("%s was changed to %s.", path, newCtx), changes), nil
}

// ---- states ----

func selinuxStateModules() []states.Module {
	state := func(function, doc string, params []signature.Param, fn states.Func) states.Module {
		return states.Module{
			Sig: signature.Signature{
				Module: "selinux", Function: function, Doc: doc, Params: params,
				Mutates: true, TestMode: signature.TestReliable, Privileges: []string{"root"},
				Platforms: linuxOnly, Section: "15.5",
			},
			Fn: fn,
		}
	}
	filetype := opt("filetype", signature.String, "a", "The file type: a (all files), f, d, c, b, s, l or p.")
	selUser := opt("sel_user", signature.String, "", "The SELinux user.")
	selLevel := opt("sel_level", signature.String, "", "The MLS level.")
	portName := nameParam("The port as protocol/port, such as tcp/8080. Defaults to the state ID.")
	protocol := opt("protocol", signature.String, "", "tcp or udp, when name does not say.")
	port := opt("port", signature.String, "", "The port or range, when name does not say.")
	return []states.Module{
		state("mode", "Ensure SELinux is running in a mode. The running mode only: it refuses a mode "+SELinuxConfigPath+
			" disagrees with, and never writes that file.",
			[]signature.Param{nameParam("Enforcing or Permissive. Defaults to the state ID.")}, selinuxModeState),
		state("boolean", "Ensure a boolean is on or off, and with persist, that it stays so across a reboot.",
			[]signature.Param{nameParam("The boolean. Defaults to the state ID."), req("value", signature.Any, "on or off."),
				opt("persist", signature.Bool, false, "Also set it in the policy store (setsebool -P).")}, selinuxBooleanState),
		state("fcontext_policy_present", "Ensure a file-context rule gives a specification a type. Does not relabel; see fcontext_policy_applied.",
			[]signature.Param{nameParam("The file specification, such as /srv/www(/.*)?. Defaults to the state ID."),
				req("sel_type", signature.String, "The type."), filetype, selUser, selLevel},
			func(c *exec.Context, args *value.Map) (states.Result, error) {
				w, err := selinuxFcontextArgs(args, true)
				if err != nil {
					return states.False(fmt.Sprintf("The rule cannot be managed: %v.", err)), nil
				}
				change, err := selinuxFcontextAdd(c, w)
				if err != nil {
					return states.False(fmt.Sprintf("%s (%s) could not be set to %s: %v", w.Spec, w.Filetype, w.Type, err)), nil
				}
				if change == nil {
					return states.True(fmt.Sprintf("%s (%s) is already %s.", w.Spec, w.Filetype, w.Type)), nil
				}
				if c.Test {
					return states.WouldChange(fmt.Sprintf("%s (%s) would be set to %s.", w.Spec, w.Filetype, w.Type), change), nil
				}
				return states.Changed(fmt.Sprintf("%s (%s) was set to %s.", w.Spec, w.Filetype, w.Type), change), nil
			}),
		state("fcontext_policy_absent", "Ensure no local file-context rule exists for a specification. A rule the policy defines is refused, not ignored.",
			[]signature.Param{nameParam("The file specification. Defaults to the state ID."), filetype,
				opt("sel_type", signature.String, "", "Only a rule giving this type counts.")},
			func(c *exec.Context, args *value.Map) (states.Result, error) {
				w, err := selinuxFcontextArgs(args, false)
				if err != nil {
					return states.False(fmt.Sprintf("The rule cannot be managed: %v.", err)), nil
				}
				change, comment, err := selinuxFcontextDelete(c, w)
				if err != nil {
					return states.False(fmt.Sprintf("%s (%s) could not be removed: %v", w.Spec, w.Filetype, err)), nil
				}
				if change == nil {
					return states.True(comment), nil
				}
				if c.Test {
					return states.WouldChange(comment, change), nil
				}
				return states.Changed(comment, change), nil
			}),
		state("fcontext_policy_applied", "Ensure a path is labelled as the file-context rules say (restorecon -F).",
			[]signature.Param{pathParam("The path. Defaults to the state ID."), opt("recursive", signature.Bool, false, "Descend into a directory.")},
			func(c *exec.Context, args *value.Map) (states.Result, error) {
				path := states.Str(args, "name", "")
				rec := states.Bool(args, "recursive", false)
				relabels, err := selinuxRestorecon(c, path, rec, true)
				if err != nil {
					return states.False(fmt.Sprintf("%s could not be checked: %v", path, err)), nil
				}
				if len(relabels) == 0 {
					return states.True(fmt.Sprintf("%s is labelled as the rules say.", path)), nil
				}
				if c.Test {
					return states.WouldChange(fmt.Sprintf("%s: %d path(s) would be relabelled.", path, len(relabels)),
						selinuxRelabelChanges(relabels)), nil
				}
				done, err := selinuxRestorecon(c, path, rec, false)
				if err != nil {
					return states.False(fmt.Sprintf("%s could not be relabelled: %v", path, err)), nil
				}
				return states.Changed(fmt.Sprintf("%s: %d path(s) were relabelled.", path, len(done)), selinuxRelabelChanges(done)), nil
			}),
		state("port_policy_present", "Ensure a port or range is in a type.",
			[]signature.Param{portName, req("sel_type", signature.String, "The port type, such as http_port_t."), protocol, port,
				opt("sel_range", signature.String, "", "The MLS range.")},
			func(c *exec.Context, args *value.Map) (states.Result, error) {
				proto, p, err := selinuxPortArgs(args)
				if err != nil {
					return states.False(fmt.Sprintf("The port cannot be managed: %v.", err)), nil
				}
				typ := strings.TrimSpace(states.Str(args, "sel_type", ""))
				if typ == "" {
					return states.False(fmt.Sprintf("%s/%s needs a sel_type.", proto, p)), nil
				}
				change, err := selinuxPortAdd(c, proto, p, typ, strings.TrimSpace(states.Str(args, "sel_range", "")))
				if err != nil {
					return states.False(fmt.Sprintf("%s/%s could not be put into %s: %v", proto, p, typ, err)), nil
				}
				if change == nil {
					return states.True(fmt.Sprintf("%s/%s is already %s.", proto, p, typ)), nil
				}
				if c.Test {
					return states.WouldChange(fmt.Sprintf("%s/%s would be put into %s.", proto, p, typ), change), nil
				}
				return states.Changed(fmt.Sprintf("%s/%s was put into %s.", proto, p, typ), change), nil
			}),
		state("port_policy_absent", "Ensure no local rule assigns a port or range. A port the policy assigns is refused, not ignored.",
			[]signature.Param{portName, opt("sel_type", signature.String, "", "Only a rule giving this type counts."), protocol, port},
			func(c *exec.Context, args *value.Map) (states.Result, error) {
				proto, p, err := selinuxPortArgs(args)
				if err != nil {
					return states.False(fmt.Sprintf("The port cannot be managed: %v.", err)), nil
				}
				change, comment, err := selinuxPortDelete(c, proto, p, strings.TrimSpace(states.Str(args, "sel_type", "")))
				if err != nil {
					return states.False(fmt.Sprintf("%s/%s could not be removed: %v", proto, p, err)), nil
				}
				if change == nil {
					return states.True(comment), nil
				}
				if c.Test {
					return states.WouldChange(comment, change), nil
				}
				return states.Changed(comment, change), nil
			}),
	}
}

// selinuxModeState is Salt's selinux.mode for the running mode.
//
// It changes the running mode only when /etc/selinux/config already
// says the same mode, so that "converged" means converged at the next
// boot too; otherwise it fails and says which line to change. That is
// the direction of wrong this module will not take: a state that set
// Permissive at runtime on a node whose config says enforcing would
// report success on a node that silently changes back.
func selinuxModeState(c *exec.Context, args *value.Map) (states.Result, error) {
	v, _ := args.Get("name")
	want, err := selinuxMode(v)
	if err != nil {
		return states.False(fmt.Sprintf("The mode cannot be managed: %v.", err)), nil
	}
	if want == "Disabled" {
		return states.False("SELinux cannot be disabled by this state: it takes SELINUX=disabled in " +
			SELinuxConfigPath + " and a reboot, and this state does not write that file."), nil
	}
	if ok, why := selinuxEnabled(); !ok {
		return states.False(fmt.Sprintf("SELinux cannot be put into %s mode: %s. Enabling it takes "+
			"a change to %s and a reboot with a relabel.", want, why, SELinuxConfigPath)), nil
	}
	current, err := selinuxGetenforce()
	if err != nil {
		return states.False(fmt.Sprintf("The running mode could not be read: %v", err)), nil
	}
	config, err := selinuxGetconfig()
	if err != nil {
		return states.False(fmt.Sprintf("%s could not be read: %v", SELinuxConfigPath, err)), nil
	}
	if config != want {
		says := "has no SELINUX= line"
		if config != "" {
			says = "says " + config
		}
		return states.False(fmt.Sprintf("%s %s, so the node boots in that mode whatever this state sets now, and "+
			"this state does not write it. Set SELINUX=%s there if %s is meant to last.",
			SELinuxConfigPath, says, strings.ToLower(want), want)), nil
	}
	if current == want {
		return states.True(fmt.Sprintf("SELinux is already %s, and %s agrees.", want, SELinuxConfigPath)), nil
	}
	changes := value.MapOf("mode", states.Change(current, want))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("SELinux would be set from %s to %s.", current, want), changes), nil
	}
	if err := selinuxSetRunningMode(want); err != nil {
		return states.False(err.Error()), nil
	}
	return states.Changed(fmt.Sprintf("SELinux was set from %s to %s.", current, want), changes), nil
}

// selinuxBooleanState is Salt's selinux.boolean. With persist it is
// converged only when both the running value and the policy store's
// agree, as Salt's is.
func selinuxBooleanState(c *exec.Context, args *value.Map) (states.Result, error) {
	name := strings.TrimSpace(states.Str(args, "name", ""))
	v, _ := args.Get("value")
	want, err := selinuxOnOff(v)
	if err != nil {
		return states.False(fmt.Sprintf("%s cannot be set: %v.", name, err)), nil
	}
	persist := states.Bool(args, "persist", false)
	bools, err := selinuxBooleans(c)
	if err != nil {
		return states.False(fmt.Sprintf("%s could not be read: %v", name, err)), nil
	}
	current, ok := bools[name]
	if !ok {
		return states.False(fmt.Sprintf("%s is not a boolean this node's policy defines.", name)), nil
	}
	change := selinuxBooleanChange(current, want, persist)
	if change == nil {
		if persist {
			return states.True(fmt.Sprintf("%s is already %s, running and in the policy store.", name, want)), nil
		}
		return states.True(fmt.Sprintf("%s is already %s.", name, want)), nil
	}
	changes := value.MapOf(name, change)
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be set %s.", name, want), changes), nil
	}
	if err := selinuxSetBooleans(c, map[string]string{name: want}, persist); err != nil {
		return states.False(fmt.Sprintf("%s could not be set %s: %v", name, want, err)), nil
	}
	after, err := selinuxBooleans(c)
	if err != nil {
		return states.False(fmt.Sprintf("%s was set and could not be read back: %v", name, err)), nil
	}
	if selinuxBooleanChange(after[name], want, persist) != nil {
		return states.False(fmt.Sprintf("%s reads (%s, %s) after setsebool exited 0.", name, after[name].State, after[name].Default)), nil
	}
	return states.Changed(fmt.Sprintf("%s was set %s.", name, want), changes), nil
}
