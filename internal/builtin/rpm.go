package builtin

import (
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// rhelOnly is the platform restriction the rpm module carries, for the
// same reason `debianOnly` is only "linux": a signature's axis is GOOS,
// and rpm is a RedHat-family thing rather than a Linux thing. Every
// function then asks whether `rpm` is actually on the node, and the
// refusal names the tool.
var rhelOnly = []string{"linux"}

// registerRpm installs the `rpm` module of SPEC 15.3's RHEL row: Salt's
// `rpm_lowpkg`, the low-level half of the RedHat package story, as far as
// it is real on the machines this was built against.
//
// It is to `pkg` on RHEL what `dpkg` is to `pkg` on Debian. `pkg.list_pkgs`
// answers "what version is installed", one string per name, because that
// is the question a state asks. That shape cannot say that AlmaLinux 8.10
// in the lab has *two* `kernel-core` packages installed side by side, or
// that both lab hosts carry two or three `gpg-pubkey` pseudo-packages --
// one per imported signing key -- and a map from name to one version
// keeps whichever rpm happened to print last. `rpm.list_pkgs` and
// `rpm.info` return a list per name for exactly that reason; it was
// measured, not anticipated.
//
// # What is here, and what is not
//
// `list_pkgs`, `info`, `file_list`, `file_dict`, `owner`, `verify` and
// `version_cmp` are Salt's names and are read-only. Salt's `rpm_lowpkg`
// also has `bin_pkg_info`, `checksum`, `diff` and `modified`: the first
// two read a `.rpm` file, and neither lab host had one on disk without
// asking dnf to download it, on hosts other work was driving dnf on at
// the time -- so they were not built rather than built unrun. `modified`
// is `verify` under another name and `diff` needs the original file out
// of a package, which is the same missing `.rpm`.
//
// # Machine formats, not the human table
//
// Every read goes through `--queryformat` with the tags named, rather
// than parsing `rpm -qi`'s aligned columns. The one field that can span
// lines is DESCRIPTION, so it is last in its record and the record ends
// with a line no description in either lab host's database contains.
// Octal escapes such as `\037` would have made a tidier separator, but
// rpm's queryformat grammar has no documented octal escape and what was
// never run is not something to build a parser on.
func registerRpm(r *Registries) {
	names := req("names", signature.List, "One or more package names.")
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "rpm", Function: "list_pkgs",
				Doc: "Return every installed package rpm knows, name to a list of installed instances, " +
					"so that several kernels, or several gpg-pubkey keys, are all reported.",
				Params: []signature.Param{
					opt("names", signature.List, nil, "Limit to these packages; defaults to all of them."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: rhelOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return rpmListPkgs(c, states.Strings(args, "names"))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "rpm", Function: "info",
				Doc:       "Return the header fields rpm holds for installed packages, name to a list of instances.",
				Params:    []signature.Param{names},
				TestMode:  signature.TestNotApplicable,
				Platforms: rhelOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return rpmInfo(c, states.Strings(args, "names"))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "rpm", Function: "file_list",
				Doc:       "Return the paths the named packages installed, as one sorted list.",
				Params:    []signature.Param{names},
				TestMode:  signature.TestNotApplicable,
				Platforms: rhelOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				byPkg, err := rpmFileDict(c, states.Strings(args, "names"))
				if err != nil {
					return nil, err
				}
				return rpmFlattenFiles(byPkg), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "rpm", Function: "file_dict",
				Doc:       "Return the paths the named packages installed, package name to its list of paths.",
				Params:    []signature.Param{names},
				TestMode:  signature.TestNotApplicable,
				Platforms: rhelOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return rpmFileDict(c, states.Strings(args, "names"))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "rpm", Function: "owner",
				Doc: "Return the packages that own each path, path to a list of package names; " +
					"an empty list is a path no package owns.",
				Params: []signature.Param{
					req("paths", signature.List, "One or more absolute paths."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: rhelOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return rpmOwner(c, states.Strings(args, "paths"))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "rpm", Function: "verify",
				Doc: "Return the installed files of the named packages that no longer match what the " +
					"package shipped, path to what differs.",
				Params:    []signature.Param{names},
				TestMode:  signature.TestNotApplicable,
				Platforms: rhelOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return rpmVerify(c, states.Strings(args, "names"))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "rpm", Function: "version_cmp",
				Doc: "Compare two RPM versions ([epoch:]version[-release]), returning -1, 0, or 1. " +
					"The same comparison as `pkg.version_cmp` with scheme `rpm`.",
				Params: []signature.Param{
					req("ver1", signature.String, "The first version."),
					req("ver2", signature.String, "The second version."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.3",
			},
			// No tool check and no platform restriction: this is
			// CompareRPM, which is a transcription of rpmvercmp rather
			// than a call to it, and a version string can be compared on
			// any node -- the same reason `pkg.version_cmp` has none.
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return int64(CompareRPM(states.Str(args, "ver1", ""), states.Str(args, "ver2", ""))), nil
			},
		},
	)
}

// haveRpm refuses by the tool's name on a Linux node that is not a
// RedHat-family one.
func haveRpm(c *exec.Context) error {
	if c.Which("rpm") == "" {
		return fmt.Errorf("rpm is not installed; the rpm module needs a RedHat-family system")
	}
	return nil
}

// rpmQuery runs one `rpm` invocation that may legitimately exit non-zero.
//
// `rpm -q` exits 1 when any named package is missing and still prints
// every one it found, and `rpm -V` exits non-zero *because* it found
// something -- both measured on rpm 4.14.3 and 4.16.1.3. IgnoreExitCode
// is therefore on every call here, and the output decides: without it
// `exec.OSRunner` turns the exit into an error before a line of the
// answer is read, which no RecordingRunner-based test can show.
func rpmQuery(c *exec.Context, argv ...string) (exec.Result, error) {
	if err := haveRpm(c); err != nil {
		return exec.Result{}, err
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return exec.Result{}, fmt.Errorf("`%s` could not be run on this node: %w",
			exec.Command{Argv: argv}.String(), err)
	}
	return res, nil
}

// rpmNotInstalled recognises rpm's line for a name it has never heard of.
// It arrives on *stdout*, interleaved with the records for the names it
// did find, on both lab hosts -- so every parser below has to step over
// it rather than read it as data.
func rpmNotInstalled(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "package ")
	if !ok {
		return "", false
	}
	name, ok := strings.CutSuffix(rest, " is not installed")
	return name, ok
}

// rpmNone is how rpm spells an absent tag: an unset EPOCH, a gpg-pubkey's
// ARCH, VENDOR and URL all print as the literal "(none)".
const rpmNone = "(none)"

func rpmField(s string) string {
	if s == rpmNone {
		return ""
	}
	return s
}

// rpmEVR spells a version the way `pkg.list_pkgs` on this platform does:
// epoch only when there is one, which is the spelling a `pkg.installed`
// with `version:` is written in and the one CompareRPM reads.
func rpmEVR(epoch, version, release string) string {
	evr := version
	if release != "" {
		evr += "-" + release
	}
	if epoch != "" && epoch != "0" {
		evr = epoch + ":" + evr
	}
	return evr
}

const rpmListFormat = "%{NAME}\\t%{EPOCH}\\t%{VERSION}\\t%{RELEASE}\\t%{ARCH}\\t%{INSTALLTIME}\\n"

func rpmListPkgs(c *exec.Context, names []string) (*value.Map, error) {
	argv := []string{"rpm", "-q", "--queryformat", rpmListFormat}
	if len(names) == 0 {
		argv = append(argv, "-a")
	} else {
		argv = append(argv, "--")
		argv = append(argv, names...)
	}
	res, err := rpmQuery(c, argv...)
	if err != nil {
		return nil, err
	}
	// A name rpm has never heard of is left out rather than failing the
	// call, the way `dpkg.list_pkgs` does: a listing that reports what it
	// found is more use than one that refuses because one name was wrong.
	return parseRpmList(res.Stdout), nil
}

// parseRpmList reads the tab-separated listing. Separate from the call,
// as `dpkg`'s parsers are, because the tool check asks the real
// filesystem and no development machine here has rpm on it.
func parseRpmList(stdout string) *value.Map {
	out := value.NewMap(256)
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 6 {
			continue
		}
		epoch, version, release := rpmField(fields[1]), fields[2], fields[3]
		instance := value.MapOf(
			"version", rpmEVR(epoch, version, release),
			"epoch", epoch,
			"arch", rpmField(fields[4]),
			"install_date_time_t", fields[5],
		)
		existing, _ := out.Get(fields[0])
		list, _ := existing.([]any)
		out.Set(fields[0], append(list, instance))
	}
	return out
}

// rpmInfoFormat names each tag, one per line, with DESCRIPTION last
// because it is the one that spans lines. The signature expression is
// Salt's own, which tries each of the four places a header can carry
// one and says "(none)" when it has none -- which is what a gpg-pubkey
// record really prints on both lab hosts.
const rpmInfoFormat = "name: %{NAME}\\n" +
	"epoch: %{EPOCH}\\n" +
	"version: %{VERSION}\\n" +
	"release: %{RELEASE}\\n" +
	"arch: %{ARCH}\\n" +
	"install_date_time_t: %{INSTALLTIME}\\n" +
	"build_date_time_t: %{BUILDTIME}\\n" +
	"build_host: %{BUILDHOST}\\n" +
	"group: %{GROUP}\\n" +
	"source_rpm: %{SOURCERPM}\\n" +
	"size: %{LONGSIZE}\\n" +
	"license: %{LICENSE}\\n" +
	"signature: %|DSAHEADER?{%{DSAHEADER:pgpsig}}:{%|RSAHEADER?{%{RSAHEADER:pgpsig}}:{%|SIGGPG?{%{SIGGPG:pgpsig}}:{%|SIGPGP?{%{SIGPGP:pgpsig}}:{(none)}|}|}|}|\\n" +
	"packager: %{PACKAGER}\\n" +
	"vendor: %{VENDOR}\\n" +
	"url: %{URL}\\n" +
	"summary: %{SUMMARY}\\n" +
	"description:\\n%{DESCRIPTION}\\n" +
	rpmRecordEnd + "\\n"

// rpmRecordEnd closes a record. A description is free text, so the
// terminator is a line nobody writes rather than something structural.
const rpmRecordEnd = "@@halite-rpm-record-end@@"

func rpmInfo(c *exec.Context, names []string) (*value.Map, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("rpm.info needs at least one package name")
	}
	argv := append([]string{"rpm", "-q", "--queryformat", rpmInfoFormat, "--"}, names...)
	res, err := rpmQuery(c, argv...)
	if err != nil {
		return nil, err
	}
	info, missing := parseRpmInfo(res.Stdout)
	// Unlike a listing, info on a package that is not installed is a
	// question with a false premise, so it is refused -- by rpm's own
	// words about which name.
	if len(missing) > 0 {
		return nil, fmt.Errorf("package %s is not installed", strings.Join(missing, ", "))
	}
	return info, nil
}

func parseRpmInfo(stdout string) (*value.Map, []string) {
	out := value.NewMap(4)
	var missing []string
	var record *value.Map
	var description []string
	inDescription := false
	for _, line := range strings.Split(stdout, "\n") {
		if record == nil {
			if name, ok := rpmNotInstalled(line); ok {
				missing = append(missing, name)
				continue
			}
			if !strings.HasPrefix(line, "name: ") {
				continue
			}
			record = value.NewMap(20)
			inDescription = false
			description = nil
		}
		if line == rpmRecordEnd {
			record.Set("description", strings.TrimRight(strings.Join(description, "\n"), "\n"))
			epoch, _ := record.GetString("epoch")
			version, _ := record.GetString("version")
			release, _ := record.GetString("release")
			record.Set("evr", rpmEVR(value.KeyString(epoch), value.KeyString(version), value.KeyString(release)))
			name, _ := record.GetString("name")
			existing, _ := out.Get(value.KeyString(name))
			list, _ := existing.([]any)
			out.Set(value.KeyString(name), append(list, record))
			record = nil
			continue
		}
		if inDescription {
			description = append(description, line)
			continue
		}
		if line == "description:" {
			inDescription = true
			continue
		}
		key, val, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		record.Set(key, rpmField(val))
	}
	return out, missing
}

// rpmFileDict asks for every path in one call, the owning name carried on
// each line by `%{=NAME}` so that several packages' lists come back
// already separated. `[...]` iterates over FILENAMES, which also means a
// package with no files prints nothing at all -- `rpm -ql` prints the
// literal "(contains no files)" instead, measured for gpg-pubkey on both
// lab hosts, and that line is not a path.
func rpmFileDict(c *exec.Context, names []string) (*value.Map, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("rpm file queries need at least one package name")
	}
	argv := append([]string{"rpm", "-q", "--queryformat", "[%{=NAME}\\t%{FILENAMES}\\n]", "--"}, names...)
	res, err := rpmQuery(c, argv...)
	if err != nil {
		return nil, err
	}
	byPkg, missing := parseRpmFileDict(res.Stdout)
	if len(missing) > 0 {
		return nil, fmt.Errorf("package %s is not installed", strings.Join(missing, ", "))
	}
	return byPkg, nil
}

func parseRpmFileDict(stdout string) (*value.Map, []string) {
	out := value.NewMap(4)
	var missing []string
	for _, line := range strings.Split(stdout, "\n") {
		if name, ok := rpmNotInstalled(line); ok {
			missing = append(missing, name)
			continue
		}
		name, path, ok := strings.Cut(line, "\t")
		if !ok || path == "" {
			continue
		}
		existing, _ := out.Get(name)
		list, _ := existing.([]any)
		out.Set(name, append(list, path))
	}
	return out, missing
}

func rpmFlattenFiles(byPkg *value.Map) []any {
	seen := map[string]bool{}
	var paths []string
	for _, name := range byPkg.StringKeys() {
		raw, _ := byPkg.Get(name)
		list, _ := raw.([]any)
		for _, p := range list {
			path := value.KeyString(p)
			if !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}
	sort.Strings(paths)
	out := make([]any, len(paths))
	for i, p := range paths {
		out[i] = p
	}
	return out
}

// rpmOwner asks once per path, on purpose. A directory can belong to
// several packages -- /usr/share/man/man1 is `filesystem` and `binutils`
// on Rocky 9, one line each -- and rpm's output does not say which path a
// line answers, so batching the paths would make the answer ambiguous
// exactly where it is interesting.
func rpmOwner(c *exec.Context, paths []string) (*value.Map, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("rpm.owner needs at least one path")
	}
	out := value.NewMap(len(paths))
	for _, path := range paths {
		res, err := rpmQuery(c, "rpm", "-qf", "--queryformat", "%{NAME}\\n", "--", path)
		if err != nil {
			return nil, err
		}
		owners, err := parseRpmOwner(path, res)
		if err != nil {
			return nil, err
		}
		out.Set(path, owners)
	}
	return out, nil
}

// parseRpmOwner reads one `rpm -qf` answer. An unowned path is an answer
// -- "file X is not owned by any package", on stdout, exit 1 -- and comes
// back as an empty list. A path that does not exist is not: rpm checks
// the disk first and says so on stderr, and "nobody owns it" would be a
// claim rpm never made.
func parseRpmOwner(path string, res exec.Result) ([]any, error) {
	owners := []any{}
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasSuffix(line, " is not owned by any package") {
			continue
		}
		owners = append(owners, line)
	}
	if len(owners) == 0 && res.Code != 0 && !strings.Contains(res.Stdout, "is not owned by any package") {
		msg := strings.TrimSpace(firstLine(res.Stderr + res.Stdout))
		if msg == "" {
			msg = fmt.Sprintf("rpm -qf exited %d", res.Code)
		}
		return nil, fmt.Errorf("%s: %s", path, msg)
	}
	return owners, nil
}

func rpmVerify(c *exec.Context, names []string) (*value.Map, error) {
	// `rpm -Va` is left out deliberately: it reads every file every
	// package installed, which is minutes, and it is the one form of this
	// call that was never run in the lab.
	if len(names) == 0 {
		return nil, fmt.Errorf("rpm.verify needs at least one package name")
	}
	argv := append([]string{"rpm", "-V", "--"}, names...)
	res, err := rpmQuery(c, argv...)
	if err != nil {
		return nil, err
	}
	out, missing := parseRpmVerify(res.Stdout)
	if len(missing) > 0 {
		return nil, fmt.Errorf("package %s is not installed", strings.Join(missing, ", "))
	}
	return out, nil
}

// rpmVerifyTests names the columns of rpm -V's nine-character result, in
// order. A letter in a column means that test failed, `.` that it
// passed, and `?` that it could not be run (usually permissions).
var rpmVerifyTests = []string{"size", "mode", "digest", "device", "link", "user", "group", "mtime", "capabilities"}

// rpmFileTypes are the one-letter attributes rpm prints between the
// result and the path, from the %files markers in the spec.
var rpmFileTypes = map[byte]string{
	'c': "config", 'd': "doc", 'g': "ghost", 'l': "license", 'r': "readme", 'a': "artifact",
}

// parseRpmVerify reads `rpm -V`. The lines captured on both lab hosts
// (rpm 4.14.3 and 4.16.1.3, identical in form) were:
//
//	.M.......  d /usr/share/doc/which/AUTHORS
//	missing   d /usr/share/doc/which/NEWS
//	S.5....T.  c /etc/DIR_COLORS.lightbgcolor
//
// The path starts at the first " /" rather than at a field index, because
// a path can contain spaces and the type letter can be a blank. Lines
// that are neither -- rpm -V also checks dependencies and prints their
// complaints, and a scriptlet can print anything -- are skipped.
func parseRpmVerify(stdout string) (*value.Map, []string) {
	out := value.NewMap(8)
	var missing []string
	for _, line := range strings.Split(stdout, "\n") {
		if name, ok := rpmNotInstalled(line); ok {
			missing = append(missing, name)
			continue
		}
		at := strings.Index(line, " /")
		if at < 0 {
			continue
		}
		head, path := line[:at], line[at+1:]
		fields := strings.Fields(head)
		if len(fields) == 0 {
			continue
		}
		result := fields[0]
		fileType := ""
		if len(fields) > 1 && len(fields[1]) == 1 {
			fileType = rpmFileTypes[fields[1][0]]
		}
		entry := value.MapOf("result", result, "type", fileType)
		switch {
		case result == "missing":
			entry.Set("missing", true)
			entry.Set("failed", []any{})
		case rpmVerifyResult(result):
			failed := []any{}
			for i, test := range rpmVerifyTests {
				if result[i] != '.' {
					failed = append(failed, test)
				}
			}
			entry.Set("missing", false)
			entry.Set("failed", failed)
		default:
			continue
		}
		out.Set(path, entry)
	}
	return out, missing
}

func rpmVerifyResult(s string) bool {
	if len(s) != len(rpmVerifyTests) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(".?SM5DLUGTP", rune(s[i])) {
			return false
		}
	}
	return true
}
