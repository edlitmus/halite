package builtin

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerShadow installs the `shadow` execution module of SPEC 15.2:
// Salt's Linux shadow module, under Salt's function names and argument
// order, keeping only the functions that were driven against the real
// tools (DIVERGENCE 5.180).
//
// # Reading: the file, not a tool
//
// `info` parses /etc/shadow itself, through the same reader
// `user.present`'s ageing arguments use, so the two cannot disagree about
// what a column says. There is no tool that prints the columns in a
// machine format. `chage -l` renders them as prose, and the prose is
// localised: captured on Debian 13 under `LC_ALL=de_DE.UTF-8` the labels
// were German and the months `Dez`, under `fr_FR.UTF-8` French with
// `sept.` and `déc.`, while on Rocky 9 and Alma 8 -- which carry no
// shadow-utils translations and no de/fr langpack -- the same command
// printed English. A parser of that output would have worked on the
// hosts it was written on and failed on the first node with a different
// LANG. `getent shadow` answers with the file's own line for a local
// account (captured, byte-identical, on all three hosts), and adds
// whatever else nsswitch names -- `sss` on Alma 8, `systemd` on Debian
// 13 -- none of which chage, passwd or chpasswd can change. Reading the
// file keeps the reader to exactly the set the writers can act on.
//
// # Writing: the tools, not the file
//
// Every change goes through `chage`, `passwd` or `chpasswd`, never an
// edit of /etc/shadow. The tools take the lock (lckpwdf) that every other
// writer of the file honours, and they preserve the file's SELinux label:
// on both EL hosts /etc/shadow is `system_u:object_r:shadow_t:s0`, and a
// rewrite by rename would have put a fresh file there with whatever label
// its directory's rules gave it -- on a host other agents were at that
// moment configuring SELinux on. Salt's own `set_password` edits the
// file by default; this does not.
//
// # What the two families do differently
//
// Captured on Rocky Linux 9.8 (shadow-utils 4.9, passwd 0.80), AlmaLinux
// 8.10 (shadow-utils 4.6, passwd 0.80) and Debian 13 (passwd 4.17.4):
//
//   - A fresh `useradd` account's hash is `!!` on EL and `!` on Debian.
//   - `passwd -l` prefixes `!!` on EL and `!` on Debian; `usermod -L`
//     prefixes `!` on both. Both are "locked" by Salt's test, a leading
//     `!`, which is the one used here.
//   - Unlocking an account whose hash is only the lock is refused by
//     every tool, and they disagree about how: EL's `passwd -u` exits
//     254 ("Unsafe operation"), Debian's exits 3, EL's `usermod -U`
//     exits 1 -- and **Debian's `usermod -U` exits 0**, having printed
//     the same refusal and changed nothing. So no function here takes an
//     exit status as the answer; each reads the column back.
//
// # Platforms
//
// Linux only, by declaration, so a FreeBSD node is told by name that
// `shadow.*` runs on linux. FreeBSD's account database is
// /etc/master.passwd (lexicon:allow — the filename FreeBSD uses),
// driven by pw(8), with one `change` and one `expire` column and no minimum, maximum, warning or inactivity
// columns at all -- its password policy is login.conf's, per login
// class. Salt ships that as a different module, `bsd_shadow`, with a
// different function set (`set_change`, not `set_mindays`), and none of
// it has been run here: no FreeBSD host was available to this work that
// could be changed. A FreeBSD tree already sets a password hash through
// `user.present`, which drives `pw usermod -H 0` (user_password.go).
func registerShadow(r *Registries) {
	r.Exec.Add(shadowExecModules()...)
}

// shadowFile is where Linux keeps the columns, a variable so a unit test
// can point it at a captured file.
var shadowFile = "/etc/shadow"

// shadowEntry is one line of /etc/shadow. A nil number is an empty
// column, which shadow(5) means as "not set" and which chage writes when
// given -1 (captured: `chage -E -1` leaves `:` `:` on all three hosts).
type shadowEntry struct {
	Name       string
	Passwd     string
	LastChange *int64
	Min        *int64
	Max        *int64
	Warn       *int64
	Inact      *int64
	Expire     *int64
}

// parseShadowLine reads one line: name:hash:lastchange:min:max:warn:inactive:expire:reserved.
func parseShadowLine(line string) (shadowEntry, bool) {
	fields := strings.Split(line, ":")
	if len(fields) < 2 || fields[0] == "" || strings.HasPrefix(fields[0], "#") {
		return shadowEntry{}, false
	}
	e := shadowEntry{Name: fields[0], Passwd: fields[1]}
	for i, p := range []**int64{&e.LastChange, &e.Min, &e.Max, &e.Warn, &e.Inact, &e.Expire} {
		col := i + 2
		if col >= len(fields) {
			break
		}
		text := strings.TrimSpace(fields[col])
		if text == "" {
			continue
		}
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			continue
		}
		*p = &n
	}
	return e, true
}

// readShadowEntry finds one account's line.
func readShadowEntry(name string) (shadowEntry, bool, error) {
	f, err := os.Open(shadowFile)
	if err != nil {
		if os.IsPermission(err) {
			return shadowEntry{}, false, fmt.Errorf("reading %s needs root", shadowFile)
		}
		return shadowEntry{}, false, err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		if e, ok := parseShadowLine(scan.Text()); ok && e.Name == name {
			return e, true, nil
		}
	}
	return shadowEntry{}, false, scan.Err()
}

// readShadowNames lists every account the file has a line for.
func readShadowNames() ([]string, error) {
	f, err := os.Open(shadowFile)
	if err != nil {
		if os.IsPermission(err) {
			return nil, fmt.Errorf("reading %s needs root", shadowFile)
		}
		return nil, err
	}
	defer f.Close()
	names := []string{}
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		if e, ok := parseShadowLine(scan.Text()); ok {
			names = append(names, e.Name)
		}
	}
	sort.Strings(names)
	return names, scan.Err()
}

// shadowColumn is -1 for an empty column, which is what Salt's `info`
// returns for one (its `_getspnam` and Python's spwd both do).
func shadowColumn(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}

// shadowInfo is Salt's `shadow.info`: the eight keys, with every value
// empty when the account has no line.
func shadowInfo(name string) (*value.Map, error) {
	e, found, err := readShadowEntry(name)
	if err != nil {
		return nil, err
	}
	if !found {
		return value.MapOf("name", "", "passwd", "", "lstchg", "", "min", "",
			"max", "", "warn", "", "inact", "", "expire", ""), nil
	}
	return value.MapOf(
		"name", e.Name,
		"passwd", e.Passwd,
		"lstchg", shadowColumn(e.LastChange),
		"min", shadowColumn(e.Min),
		"max", shadowColumn(e.Max),
		"warn", shadowColumn(e.Warn),
		"inact", shadowColumn(e.Inact),
		"expire", shadowColumn(e.Expire),
	), nil
}

// shadowAccountName refuses a name that a tool would read as something
// else: a leading `-` is an option to chage and passwd, and a colon or a
// line break is a field or a record to chpasswd.
func shadowAccountName(args *value.Map) (string, error) {
	name := states.Str(args, "name", "")
	if name == "" {
		return "", fmt.Errorf("an account name is required")
	}
	if strings.HasPrefix(name, "-") || strings.ContainsAny(name, ": \t\r\n") {
		return "", fmt.Errorf("%q is not an account name this will pass to chage or passwd", name)
	}
	return name, nil
}

// shadowDays turns Salt's spellings of a day into the day number chage
// stores: an integer (days since 1970-01-01, or -1 for "unset"), or a
// `YYYY-MM-DD` date.
//
// The date is converted here and chage is handed the number, rather than
// passing the date through, because chage converts a date in the node's
// local time zone and this would then have to guess its answer to
// compare against. Captured: `chage -E 2027-01-31` stored 20849, which is
// this function's answer for that date, on all three hosts in UTC; and
// `chage -E 20000` stored 20000.
func shadowDays(v any) (int64, error) {
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		if t, err := time.Parse("2006-01-02", s); err == nil {
			if t.Unix() < 0 {
				return 0, fmt.Errorf("%s is before 1970-01-01, which chage cannot store", s)
			}
			return t.Unix() / 86400, nil
		}
	}
	n, ok := asInt64(v)
	if !ok {
		return 0, fmt.Errorf("%v is neither a number of days nor a YYYY-MM-DD date", v)
	}
	if n < -1 {
		return 0, fmt.Errorf("%d is not a day count chage accepts; -1 means unset", n)
	}
	return n, nil
}

// shadowAttribute is one chage-managed column: its info key, chage's
// flag, and where it lives in an entry.
type shadowAttribute struct {
	Key  string
	Flag string
	Get  func(*shadowEntry) *int64
}

var (
	shadowMin    = shadowAttribute{"min", "-m", func(e *shadowEntry) *int64 { return e.Min }}
	shadowMax    = shadowAttribute{"max", "-M", func(e *shadowEntry) *int64 { return e.Max }}
	shadowWarn   = shadowAttribute{"warn", "-W", func(e *shadowEntry) *int64 { return e.Warn }}
	shadowInact  = shadowAttribute{"inact", "-I", func(e *shadowEntry) *int64 { return e.Inact }}
	shadowExpire = shadowAttribute{"expire", "-E", func(e *shadowEntry) *int64 { return e.Expire }}
	shadowDate   = shadowAttribute{"lstchg", "-d", func(e *shadowEntry) *int64 { return e.LastChange }}
)

// shadowSetAttribute is Salt's `_set_attrib`: false for an account with
// no line, true without acting when the column already holds the value,
// and otherwise chage followed by reading the column back -- the read is
// the answer, not chage's exit status.
func shadowSetAttribute(c *exec.Context, name string, attr shadowAttribute, want int64) (bool, error) {
	before, found, err := readShadowEntry(name)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	if shadowColumn(attr.Get(&before)) == want {
		return true, nil
	}
	if c.Test {
		return true, nil
	}
	if c.Which("chage") == "" {
		return false, fmt.Errorf("chage is not on this node's PATH")
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"chage", attr.Flag, strconv.FormatInt(want, 10), name},
		IgnoreExitCode: true,
	})
	if err != nil {
		return false, err
	}
	after, _, err := readShadowEntry(name)
	if err != nil {
		return false, err
	}
	if got := shadowColumn(attr.Get(&after)); got != want {
		return false, fmt.Errorf("chage %s %d exited %d and the %s column reads %d: %s",
			attr.Flag, want, res.Code, attr.Key, got, firstLine(res.Stderr+res.Stdout))
	}
	return true, nil
}

// shadowHash refuses a hash that would not reach chpasswd as one field
// of one record. chpasswd reads `name:hash` lines, so a line break in
// the hash would be a second record -- for any account the text names.
func shadowHash(hash string) error {
	if strings.ContainsAny(hash, ":\r\n") {
		return fmt.Errorf("a password hash cannot contain a colon or a line break")
	}
	for _, r := range hash {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("a password hash cannot contain a control character")
		}
	}
	return nil
}

// shadowSetPassword is Salt's `set_password`: the hash is written through
// the same chpasswd path `user.present` uses, on standard input, and the
// answer is whether the column now holds it.
func shadowSetPassword(c *exec.Context, name, hash string) (bool, error) {
	if err := shadowHash(hash); err != nil {
		return false, err
	}
	before, found, err := readShadowEntry(name)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	if before.Passwd == hash {
		return true, nil
	}
	if c.Test {
		return true, nil
	}
	if err := setPassword(c, linuxTool, name, hash); err != nil {
		return false, err
	}
	after, _, err := readShadowEntry(name)
	if err != nil {
		return false, err
	}
	return after.Passwd == hash, nil
}

// shadowPasswdTool runs `passwd <flag> <name>` and then reads the column,
// which is the answer; `passwd`'s own status and words are carried into
// an error only, because they differ by family and one of the tools that
// refuses exits 0.
func shadowPasswdTool(c *exec.Context, name, flag string, done func(string) bool) (bool, error) {
	before, found, err := readShadowEntry(name)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	if done(before.Passwd) {
		return true, nil
	}
	if c.Test {
		return true, nil
	}
	if c.Which("passwd") == "" {
		return false, fmt.Errorf("passwd is not on this node's PATH")
	}
	res, err := c.Run(exec.Command{Argv: []string{"passwd", flag, name}, IgnoreExitCode: true})
	if err != nil {
		return false, err
	}
	after, _, err := readShadowEntry(name)
	if err != nil {
		return false, err
	}
	if !done(after.Passwd) {
		return false, fmt.Errorf("passwd %s %s exited %d and did not change the password field: %s",
			flag, name, res.Code, firstLine(strings.TrimSpace(res.Stderr+"\n"+res.Stdout)))
	}
	return true, nil
}

func shadowLocked(hash string) bool { return strings.HasPrefix(hash, "!") }

func shadowExecModules() []exec.Module {
	name := req("name", signature.String, "The account.")
	read := func(function, doc string, params []signature.Param, fn exec.Func) exec.Module {
		return exec.Module{
			Sig: signature.Signature{
				Module: "shadow", Function: function, Doc: doc, Params: params,
				TestMode: signature.TestNotApplicable, Platforms: linuxOnly,
				Privileges: []string{"root"}, Section: "15.2",
			},
			Fn: fn,
		}
	}
	mutate := func(function, doc string, params []signature.Param, fn exec.Func) exec.Module {
		return exec.Module{
			Sig: signature.Signature{
				Module: "shadow", Function: function, Doc: doc, Params: params,
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"}, Platforms: linuxOnly, Section: "15.2",
			},
			Fn: fn,
		}
	}
	days := func(function, doc, param, paramDoc string, attr shadowAttribute) exec.Module {
		return mutate(function, doc,
			[]signature.Param{name, req(param, signature.Any, paramDoc)},
			func(c *exec.Context, args *value.Map) (any, error) {
				n, err := shadowAccountName(args)
				if err != nil {
					return nil, err
				}
				v, _ := args.Get(param)
				want, err := shadowDays(v)
				if err != nil {
					return nil, err
				}
				return shadowSetAttribute(c, n, attr, want)
			})
	}
	count := "A number of days; -1 empties the column, which chage and shadow(5) read as unset."
	date := "Days since 1970-01-01, or a `YYYY-MM-DD` date; -1 empties the column."

	return []exec.Module{
		read("info",
			"Return an account's /etc/shadow columns as Salt names them: `name`, `passwd`, `lstchg`, "+
				"`min`, `max`, `warn`, `inact` and `expire`. An empty column is -1. An account with no "+
				"line gets every key with an empty value, as in Salt. Read from the file, not through "+
				"nsswitch, so a directory-service account is not listed.",
			[]signature.Param{name},
			func(c *exec.Context, args *value.Map) (any, error) {
				n, err := shadowAccountName(args)
				if err != nil {
					return nil, err
				}
				return shadowInfo(n)
			}),
		read("list_users", "Return the name of every account /etc/shadow has a line for, sorted.", nil,
			func(c *exec.Context, args *value.Map) (any, error) {
				names, err := readShadowNames()
				if err != nil {
					return nil, err
				}
				out := make([]any, len(names))
				for i, n := range names {
					out[i] = n
				}
				return out, nil
			}),
		mutate("set_password",
			"Set an account's password to an already-hashed value, through `chpasswd -e` on standard "+
				"input so the hash is never in the process table. Returns whether the column holds it "+
				"afterwards. There is no `gen_password`: this build does not hash a plaintext.",
			[]signature.Param{name, req("password", signature.String, "The hash, as it is to appear in /etc/shadow.")},
			func(c *exec.Context, args *value.Map) (any, error) {
				n, err := shadowAccountName(args)
				if err != nil {
					return nil, err
				}
				return shadowSetPassword(c, n, states.Str(args, "password", ""))
			}),
		mutate("del_password",
			"Empty an account's password with `passwd -d`, which leaves it able to log in with none "+
				"wherever PAM allows that. Returns whether the column is empty afterwards.",
			[]signature.Param{name},
			func(c *exec.Context, args *value.Map) (any, error) {
				n, err := shadowAccountName(args)
				if err != nil {
					return nil, err
				}
				return shadowPasswdTool(c, n, "-d", func(h string) bool { return h == "" })
			}),
		mutate("lock_password",
			"Lock an account's password with `passwd -l`. EL prefixes `!!` and Debian `!`; either "+
				"counts as locked. True without acting when the field already starts with `!`.",
			[]signature.Param{name},
			func(c *exec.Context, args *value.Map) (any, error) {
				n, err := shadowAccountName(args)
				if err != nil {
					return nil, err
				}
				return shadowPasswdTool(c, n, "-l", shadowLocked)
			}),
		mutate("unlock_password",
			"Unlock an account's password with `passwd -u`. Every tool measured refuses to unlock a "+
				"field that is only the lock, because the result would be no password at all; that "+
				"refusal is an error here, whatever the tool's exit status.",
			[]signature.Param{name},
			func(c *exec.Context, args *value.Map) (any, error) {
				n, err := shadowAccountName(args)
				if err != nil {
					return nil, err
				}
				return shadowPasswdTool(c, n, "-u", func(h string) bool { return !shadowLocked(h) })
			}),
		days("set_mindays", "Set the minimum days between password changes, with `chage -m`.", "mindays", count, shadowMin),
		days("set_maxdays", "Set the maximum days a password is valid, with `chage -M`.", "maxdays", count, shadowMax),
		days("set_warndays", "Set the days of warning before a password expires, with `chage -W`.", "warndays", count, shadowWarn),
		days("set_inactdays", "Set the days after a password expires before the account is disabled, with `chage -I`.", "inactdays", count, shadowInact),
		days("set_expire", "Set the day the account expires, with `chage -E`. 0 is 1970-01-01, which has passed.", "expire", date, shadowExpire),
		days("set_date", "Set the day of the last password change, with `chage -d`. 0 makes the password expire now, forcing a change at the next login.", "date", date, shadowDate),
	}
}
