package builtin

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerZFS installs the zfs module. The zpool module is beside it in
// zpool.go.
//
// Both tools have a scripted output mode — `-H -p` for zfs, `-H -p` for
// zpool — which emits tab-separated fields with no header and no unit
// suffixes. Parsing that is stable; parsing the human output is not, and
// a configuration management system that misreads a pool's health is
// worse than one that cannot read it at all.
func registerZFS(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "zfs", Function: "list",
				Doc: "Return the datasets and their space usage.",
				Params: []signature.Param{
					opt("dataset", signature.String, "", "Restrict to one dataset and its children."),
					opt("recursive", signature.Bool, false, "Include children."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: []string{"freebsd", "linux", "illumos"},
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				argv := []string{"zfs", "list", "-H", "-p", "-o", "name,used,avail,refer,mountpoint"}
				if states.Bool(args, "recursive", false) {
					argv = append(argv, "-r")
				}
				if d := states.Str(args, "dataset", ""); d != "" {
					argv = append(argv, d)
				}
				res, err := zfsRun(c, argv...)
				if err != nil {
					return nil, err
				}
				out := value.NewMap(16)
				for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
					f := strings.Split(line, "\t")
					if len(f) < 5 {
						continue
					}
					out.Set(f[0], value.MapOf(
						"used", parseInt64(f[1]),
						"available", parseInt64(f[2]),
						"referenced", parseInt64(f[3]),
						"mountpoint", f[4],
					))
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "zfs", Function: "get",
				Doc: "Return a dataset's properties.",
				Params: []signature.Param{
					req("dataset", signature.String, "The dataset."),
					opt("properties", signature.List, nil, "Which properties; defaults to all."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: []string{"freebsd", "linux", "illumos"},
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				props := "all"
				if p := states.Strings(args, "properties"); len(p) > 0 {
					props = strings.Join(p, ",")
				}
				res, err := zfsRun(c, "zfs", "get", "-H", "-p", "-o", "property,value", props,
					states.Str(args, "dataset", ""))
				if err != nil {
					return nil, err
				}
				return parseTabPairs(res.Stdout), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "zfs", Function: "exists",
				Doc:       "Report whether a dataset exists.",
				Params:    []signature.Param{req("dataset", signature.String, "The dataset.")},
				TestMode:  signature.TestNotApplicable,
				Platforms: []string{"freebsd", "linux", "illumos"},
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return zfsExists(c, states.Str(args, "dataset", "")), nil
			},
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "zfs", Function: "filesystem_present",
				Doc: "Ensure a ZFS filesystem exists with the given properties.",
				Params: []signature.Param{
					nameParam("The dataset. Defaults to the state ID."),
					opt("properties", signature.Map, nil, "Properties to set."),
					opt("create_parent", signature.Bool, false, "Create the parent datasets."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{signature.PrivZFSDelegation},
				Platforms:  []string{"freebsd", "linux", "illumos"},
				Section:    "15.5",
			},
			Fn: zfsFilesystemPresent,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "zfs", Function: "absent",
				Doc: "Ensure a ZFS dataset does not exist.",
				Params: []signature.Param{
					nameParam("The dataset. Defaults to the state ID."),
					opt("recursive", signature.Bool, false, "Destroy the children too."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{signature.PrivZFSDelegation},
				Platforms:  []string{"freebsd", "linux", "illumos"},
				Section:    "15.5",
			},
			Fn: zfsAbsent,
		},
	)
}

func zfsRun(c *exec.Context, argv ...string) (exec.Result, error) {
	if c.Which(argv[0]) == "" {
		return exec.Result{}, fmt.Errorf("%s was not found on this node", argv[0])
	}
	return c.Run(exec.Command{Argv: argv})
}

func zfsExists(c *exec.Context, dataset string) bool {
	if c.Which("zfs") == "" {
		return false
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"zfs", "list", "-H", "-o", "name", dataset},
		IgnoreExitCode: true,
	})
	return err == nil && res.Code == 0
}

// parseTabPairs reads `key\tvalue` lines into a mapping.
func parseTabPairs(out string) *value.Map {
	m := value.NewMap(32)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		k, v, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		m.Set(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	return m
}

// zfsArg renders a declared property value the way zfs and zpool read
// it on their command lines.
//
// The one case that is not value.KeyString is a boolean, and it is the
// commonest one. YAML 1.1 is this project's default (SPEC, and
// internal/yaml's bool11), so the spelling every ZFS tutorial uses —
// `atime: off`, `compression: on`, `autotrim: on` — arrives here as a Go
// bool, and KeyString renders that as "false"/"true". zfs does not take
// those for an on|off property, and `zfs get` never prints them, so the
// state compared "false" against "off", called it a difference every
// run, and asked `zfs set atime=false` to fix it (DIVERGENCE 5.311).
// Salt avoids the same trap in salt.utils.zfs.to_bool, which turns
// True/False into "on"/"off" before a command is built; this mirrors it.
//
// Salt also has a yes|no flavour (to_bool_alt). No property zfs or zpool
// lets anybody *set* takes yes|no — `mounted` and `defer_destroy` are the
// read-only ones that print it — so a declared boolean is always on|off
// here, and a declared "yes"/"no" string is passed through as written.
func zfsArg(v any) string {
	if b, ok := v.(bool); ok {
		if b {
			return "on"
		}
		return "off"
	}
	return value.KeyString(v)
}

// zfsSizeProperties are the properties whose value is a number of bytes,
// which `zfs get -p` prints exactly and a person writes as `10G`. Without
// this the state compared "10G" against "10737418240", found them
// different every run, and set the quota again every run.
//
// The list is the settable `<size>`-valued properties of zfs(8)/zfsprops(7)
// plus recordsize and volblocksize, which Salt's probe types as strings
// (their VALUES column reads "512 to 1M, power of 2", not "<size>") but
// which take and print a byte count all the same. The per-user, per-group
// and per-project quotas are matched by prefix in zfsIsSizeProperty.
// Anything not named here is compared as the exact string, which is what
// it was before; that can only fail to converge, never set a value the
// declaration did not ask for.
var zfsSizeProperties = map[string]bool{
	"quota":                true,
	"refquota":             true,
	"reservation":          true,
	"refreservation":       true,
	"recordsize":           true,
	"volsize":              true,
	"volblocksize":         true,
	"special_small_blocks": true,
}

// zfsNoneIsZero are the size properties `zfs get -p` prints as 0 when
// they are unset, where the human output and the declaration say "none".
// With -p, libzfs prints the raw integer for these rather than the word
// (the word is the non-literal branch of zfs_prop_get). That is read from
// the OpenZFS source and has not been captured on a machine here — see
// DIVERGENCE 5.311 for what a lab run needs to check.
var zfsNoneIsZero = map[string]bool{
	"quota":          true,
	"refquota":       true,
	"reservation":    true,
	"refreservation": true,
}

func zfsIsSizeProperty(name string) bool {
	if zfsSizeProperties[name] {
		return true
	}
	for _, p := range []string{"userquota@", "groupquota@", "projectquota@"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// zfsNoneMeansZero adds the per-user, per-group and per-project quotas to
// zfsNoneIsZero; their literal branch in libzfs prints the integer too.
func zfsNoneMeansZero(name string) bool {
	return zfsNoneIsZero[name] || strings.Contains(name, "quota@")
}

// zfsSizeUnits is salt.utils.zfs's zfs_size, in order: K is 1024^1.
const zfsSizeUnits = "KMGTPE"

// zfsParseSize reads a size the way salt.utils.zfs.from_size does: a
// whole or decimal number, then one of K M G T P E in either case with an
// optional B, each a power of 1024 (re_zfs_size,
// `^(\d+|\d+(?=\d*)\.\d+)([KkMmGgTtPpEe][Bb]?)$`), or a bare integer of
// bytes, which is what `zfs get -p` prints. Salt keeps a decimal result
// that is not a whole number of bytes as a float, which then never equals
// anything zfs reads back; here such a value is not parsed at all and
// falls back to the exact string comparison, rather than guessing at how
// libzfs rounds it. Spellings libzfs accepts beyond Salt's grammar (a
// bare `B`, `KiB`, `Z`) are left unparsed the same way, for the same
// reason: nobody has watched what they read back as.
func zfsParseSize(s string) (uint64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if n, err := strconv.ParseUint(s, 10, 64); err == nil {
		return n, true
	}
	num := s
	if len(num) >= 2 && (num[len(num)-1] == 'B' || num[len(num)-1] == 'b') {
		num = num[:len(num)-1]
	}
	if num == "" {
		return 0, false
	}
	shift := strings.IndexByte(zfsSizeUnits, upperASCII(num[len(num)-1]))
	if shift < 0 {
		return 0, false
	}
	num = num[:len(num)-1]
	if !zfsSizeNumber(num) {
		return 0, false
	}
	mult := uint64(1) << (10 * uint(shift+1))
	if whole, err := strconv.ParseUint(num, 10, 64); err == nil {
		if whole > ^uint64(0)/mult {
			return 0, false
		}
		return whole * mult, true
	}
	// A decimal, which Salt accepts (`1.5G`): taken only when it comes to
	// a whole number of bytes. Multiplying by a power of two is exact in
	// a float64, so the only rounding is in parsing the decimal itself;
	// a fraction with more digits than a float64 holds (seventeen or so)
	// can round onto a whole number, and is then read as that number.
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, false
	}
	bytes := f * float64(mult)
	if bytes != math.Trunc(bytes) || bytes >= math.Ldexp(1, 64) {
		return 0, false
	}
	return uint64(bytes), true
}

// zfsSizeNumber is the numeric half of Salt's re_zfs_size: digits, or
// digits, a point and digits.
func zfsSizeNumber(s string) bool {
	whole, frac, dot := strings.Cut(s, ".")
	if whole == "" || !allDigits(whole) {
		return false
	}
	return !dot || (frac != "" && allDigits(frac))
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func upperASCII(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - ('a' - 'A')
	}
	return b
}

// zfsSameValue reports whether a declared property value is what
// `zfs get -H -p` / `zpool get -H -p` printed for it. Both sides go
// through the same normalisation, so a value read back as "none" on a
// version that prints it compares equal to a declared 0 as well.
func zfsSameValue(name string, declared, current any) bool {
	want, have := zfsArg(declared), zfsArg(current)
	if want == have {
		return true
	}
	if !zfsIsSizeProperty(name) {
		return false
	}
	w, wok := zfsSizeOrNone(name, want)
	h, hok := zfsSizeOrNone(name, have)
	return wok && hok && w == h
}

func zfsSizeOrNone(name, s string) (uint64, bool) {
	if strings.EqualFold(strings.TrimSpace(s), "none") {
		return 0, zfsNoneMeansZero(name)
	}
	return zfsParseSize(s)
}

func zfsFilesystemPresent(c *exec.Context, args *value.Map) (states.Result, error) {
	dataset := states.Str(args, "name", "")
	if dataset == "" {
		return states.False("This state needs a dataset name."), nil
	}
	if c.Which("zfs") == "" {
		return states.False("Zfs was not found on this node."), nil
	}
	props := states.Mapping(args, "properties")

	exists := zfsExists(c, dataset)
	changes := value.NewMap(4)

	if !exists {
		changes.Set(dataset, states.Change(nil, "present"))
	} else if props != nil {
		current, err := zfsRun(c, "zfs", "get", "-H", "-p", "-o", "property,value", propertyList(props), dataset)
		if err != nil {
			return states.False(fmt.Sprintf("The properties of %s could not be read: %v", dataset, err)), nil
		}
		have := parseTabPairs(current.Stdout)
		for _, e := range props.Entries() {
			name := value.KeyString(e.Key)
			if cur, ok := have.Get(name); !ok || !zfsSameValue(name, e.Val, cur) {
				changes.Set(name, states.Change(cur, zfsArg(e.Val)))
			}
		}
	}

	if changes.Len() == 0 {
		return states.True(fmt.Sprintf("The dataset %s already exists with the requested properties.", dataset)), nil
	}
	verb := "updated"
	if !exists {
		verb = "created"
	}
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The dataset %s would be %s.", dataset, verb), changes), nil
	}

	if !exists {
		argv := []string{"zfs", "create"}
		if states.Bool(args, "create_parent", false) {
			argv = append(argv, "-p")
		}
		for _, name := range sortedPropertyNames(props) {
			v, _ := props.Get(name)
			argv = append(argv, "-o", name+"="+zfsArg(v))
		}
		argv = append(argv, dataset)
		if _, err := c.Run(exec.Command{Argv: argv}); err != nil {
			return states.False(fmt.Sprintf("The dataset %s could not be created: %v", dataset, err)), nil
		}
		return states.Changed(fmt.Sprintf("The dataset %s was created.", dataset), changes), nil
	}

	for _, e := range changes.Entries() {
		name := value.KeyString(e.Key)
		v, ok := props.Get(name)
		if !ok {
			continue
		}
		if _, err := c.Run(exec.Command{Argv: []string{"zfs", "set", name + "=" + zfsArg(v), dataset}}); err != nil {
			return states.False(fmt.Sprintf("The property %s of %s could not be set: %v", name, dataset, err)), nil
		}
	}
	return states.Changed(fmt.Sprintf("The dataset %s was updated.", dataset), changes), nil
}

func propertyList(props *value.Map) string {
	if props == nil || props.Len() == 0 {
		return "all"
	}
	return strings.Join(sortedPropertyNames(props), ",")
}

func sortedPropertyNames(props *value.Map) []string {
	if props == nil {
		return nil
	}
	out := props.StringKeys()
	sort.Strings(out)
	return out
}

func zfsAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
	dataset := states.Str(args, "name", "")
	if c.Which("zfs") == "" {
		return states.False("Zfs was not found on this node."), nil
	}
	if !zfsExists(c, dataset) {
		return states.True(fmt.Sprintf("The dataset %s is already absent.", dataset)), nil
	}
	changes := value.MapOf(dataset, states.Change("present", nil))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The dataset %s would be destroyed.", dataset), changes), nil
	}
	argv := []string{"zfs", "destroy"}
	if states.Bool(args, "recursive", false) {
		argv = append(argv, "-r")
	}
	argv = append(argv, dataset)
	if _, err := c.Run(exec.Command{Argv: argv}); err != nil {
		return states.False(fmt.Sprintf("The dataset %s could not be destroyed: %v", dataset, err)), nil
	}
	return states.Changed(fmt.Sprintf("The dataset %s was destroyed.", dataset), changes), nil
}
