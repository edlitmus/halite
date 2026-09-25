package builtin

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The password-ageing fields of an account: `mindays`, `maxdays`,
// `warndays`, `inactdays` and `expire`.
//
// Every one of them is a column of /etc/shadow and an option of
// chage(1), and together they are how a hardened estate expresses its
// password policy per account. This build had none of them, so the
// states that set them did not compile at all -- eleven of the
// twenty-nine errors an estate's real tree produced were these five
// arguments across three declarations.
//
// They are read from the shadow file rather than from a tool, because
// there is no chage that prints them in a machine format: `chage -l`
// renders dates in the caller's locale, which is a parse that breaks
// the first time a node runs under a different LANG. The columns are
// integers and the file's layout is stable.
//
// # Platforms
//
// Linux only, and by refusal rather than by silence. FreeBSD's account
// database has a single `expire` column and no equivalent of the other
// four -- its password policy lives in login.conf, keyed by login class
// rather than by account -- so a tree asking for `maxdays` on FreeBSD
// is asking for something the platform will not do. Accepting it there
// and applying nothing is the shape of defect this project keeps
// finding; the state says so instead.

// shadowAging holds the five ageing columns. A nil field is one the
// declaration did not mention, which is different from a zero: chage
// reads -1 as "never" and 0 as "immediately", and both are meaningful.
type shadowAging struct {
	Min    *int64
	Max    *int64
	Warn   *int64
	Inact  *int64
	Expire *int64
}

// agingFields maps each argument to its shadow column and chage option.
// The column numbers are /etc/shadow's, which shadow(5) fixes:
// name:hash:lastchange:min:max:warn:inactive:expire.
var agingFields = []struct {
	Arg    string
	Column int
	Flag   string
	Get    func(*shadowAging) **int64
}{
	{"mindays", 3, "-m", func(a *shadowAging) **int64 { return &a.Min }},
	{"maxdays", 4, "-M", func(a *shadowAging) **int64 { return &a.Max }},
	{"warndays", 5, "-W", func(a *shadowAging) **int64 { return &a.Warn }},
	{"inactdays", 6, "-I", func(a *shadowAging) **int64 { return &a.Inact }},
	{"expire", 7, "-E", func(a *shadowAging) **int64 { return &a.Expire }},
}

// agingFrom reads the five arguments off a declaration.
func agingFrom(args *value.Map) shadowAging {
	var want shadowAging
	for _, f := range agingFields {
		v, ok := args.Get(f.Arg)
		if !ok || v == nil {
			continue
		}
		n, ok := asInt64(v)
		if !ok {
			continue
		}
		*f.Get(&want) = &n
	}
	return want
}

// agingRequested reports whether a declaration mentioned any of them,
// which is what decides if the shadow file is read at all.
func agingRequested(args *value.Map) bool {
	for _, f := range agingFields {
		if v, ok := args.Get(f.Arg); ok && v != nil {
			return true
		}
	}
	return false
}

// asInt64 accepts the spellings YAML produces for a whole number. A
// string is accepted because a tree that writes `maxdays: '90'`, or
// derives one from pillar, means ninety.
func asInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case float64:
		return int64(t), t == float64(int64(t))
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n, err == nil
	}
	return 0, false
}

// readAging reads an account's current ageing columns.
//
// An empty column means "unset", which shadow(5) writes as an empty
// field and chage reports as -1; it is returned as nil so that a
// declaration setting it is a change and one not mentioning it is not.
func readAging(name string) (shadowAging, bool, error) {
	var cur shadowAging
	loc, ok := hashLocations[runtime.GOOS]
	if !ok {
		return cur, false, fmt.Errorf("this build does not know where %s keeps password ageing", runtime.GOOS)
	}
	f, err := os.Open(loc.path)
	if err != nil {
		if os.IsPermission(err) {
			return cur, false, fmt.Errorf("reading %s needs root, and so does setting password ageing", loc.path)
		}
		return cur, false, err
	}
	defer f.Close()

	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := scan.Text()
		if !strings.HasPrefix(line, name+":") {
			continue
		}
		fields := strings.Split(line, ":")
		for _, fd := range agingFields {
			if fd.Column >= len(fields) {
				continue
			}
			text := strings.TrimSpace(fields[fd.Column])
			if text == "" {
				continue
			}
			n, err := strconv.ParseInt(text, 10, 64)
			if err != nil {
				continue
			}
			v := n
			*fd.Get(&cur) = &v
		}
		return cur, true, nil
	}
	return cur, false, scan.Err()
}

// diffAging records each ageing column the declaration would change.
func diffAging(want, have shadowAging, changes *value.Map) {
	for _, f := range agingFields {
		w := *f.Get(&want)
		if w == nil {
			continue
		}
		h := *f.Get(&have)
		if h != nil && *h == *w {
			continue
		}
		from := any("(unset)")
		if h != nil {
			from = *h
		}
		changes.Set(f.Arg, states.Change(from, *w))
	}
}

// agingArgv builds the chage command for the columns that differ.
// It returns nil when there is nothing to do.
func agingArgv(name string, want, have shadowAging) []string {
	var argv []string
	for _, f := range agingFields {
		w := *f.Get(&want)
		if w == nil {
			continue
		}
		h := *f.Get(&have)
		if h != nil && *h == *w {
			continue
		}
		argv = append(argv, f.Flag, strconv.FormatInt(*w, 10))
	}
	if argv == nil {
		return nil
	}
	return append(append([]string{"chage"}, argv...), name)
}

// applyAging sets the ageing columns that differ.
func applyAging(c *exec.Context, name string, want, have shadowAging) error {
	argv := agingArgv(name, want, have)
	if argv == nil {
		return nil
	}
	if c.Which("chage") == "" {
		return fmt.Errorf("password ageing needs chage(1), which is not on this node's PATH")
	}
	_, err := c.Run(exec.Command{Argv: argv})
	return err
}

// agingUnsupported names the platform's refusal, or "" where the five
// arguments can be honoured.
func agingUnsupported(args *value.Map) string {
	if !agingRequested(args) || runtime.GOOS == "linux" {
		return ""
	}
	var named []string
	for _, f := range agingFields {
		if v, ok := args.Get(f.Arg); ok && v != nil {
			named = append(named, f.Arg)
		}
	}
	return fmt.Sprintf(
		"This state sets %s, which is Linux password ageing over chage(1); %s has no equivalent "+
			"per-account setting. On FreeBSD a password policy lives in login.conf, keyed by login class.",
		strings.Join(named, ", "), runtime.GOOS)
}

// groupMemberNames lists a group's supplementary members: the fourth
// column of its /etc/group line, which is getgrnam's gr_mem and what
// Salt's group.present compares `members` against.
//
// It used to fold in every account whose *primary* group this is, with a
// comment saying that removing such an account "would be both impossible
// and wrong". Both halves were right, and folding them in was what made
// the reconciler try it: on an Ubuntu runner, `members: [u2, u3]` on a
// group that was u4's primary group ran `gpasswd -d u4` and failed with
// "user 'u4' is not a member", on every run (DIVERGENCE 5.151). A primary
// group is set on the account, by `user.present`'s `gid`, and no member
// list can add or remove it.
func groupMemberNames(name string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	f, err := os.Open("/etc/group")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		fields := strings.Split(scan.Text(), ":")
		if len(fields) < 4 || fields[0] != name {
			continue
		}
		for _, m := range strings.Split(fields[3], ",") {
			if m = strings.TrimSpace(m); m != "" && !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}
