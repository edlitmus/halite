package modules

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func init() {
	register("pkgrepo.managed", pkgrepoManaged)
	register("pkgrepo.absent", pkgrepoAbsent)
}

// repoFile is one platform's idea of a repository definition: where it
// lives, what it contains, and how to make the package manager notice.
type repoFile struct {
	path    string
	body    string
	refresh []string
}

// pkgrepoManaged writes a repository definition for the host's package
// manager and refreshes its metadata when the file changes.
//
//	nginx-upstream:
//	  pkgrepo.managed:
//	    - url: https://nginx.org/packages/debian
//	    - dist: bookworm
//	    - comps: nginx
//	    - signed_by: /etc/apt/keyrings/nginx.gpg
//
// The signing key itself is a file.managed away: halite does not fetch
// keys, because a repository state that quietly trusts a downloaded key
// would be the wrong default.
func pkgrepoManaged(c *Ctx, id string, args map[string]any) Result {
	name := Str(args, "name", id)
	repo, err := renderRepo(name, args)
	if err != nil {
		return resFail("%v", err)
	}

	current, readErr := os.ReadFile(repo.path)
	if readErr == nil && string(current) == repo.body {
		return resOK(fmt.Sprintf("repository %s is configured", name))
	}
	if c.Test {
		if readErr != nil {
			return resWould(fmt.Sprintf("repository %s would be added at %s", name, repo.path))
		}
		return resWould(fmt.Sprintf("repository %s at %s would be updated", name, repo.path))
	}
	if err := os.MkdirAll(filepath.Dir(repo.path), 0o755); err != nil {
		return resFail("mkdir %s: %v", filepath.Dir(repo.path), err)
	}
	if err := atomicWrite(repo.path, []byte(repo.body), 0o644); err != nil {
		return resFail("write %s: %v", repo.path, err)
	}
	changes := map[string]string{repo.path: "written"}
	if diff := lineDiff(current, []byte(repo.body)); readErr == nil && diff != "" {
		changes["diff"] = diff
	}
	if out, err := refreshRepos(args, repo); err != nil {
		return resFail("repository %s written, but refresh failed: %v: %s", name, err, strings.TrimSpace(out))
	} else if out != "" {
		changes["refresh"] = strings.Join(repo.refresh, " ")
	}
	return resChanged(fmt.Sprintf("repository %s written to %s", name, repo.path), changes)
}

// pkgrepoAbsent removes a repository definition.
func pkgrepoAbsent(c *Ctx, id string, args map[string]any) Result {
	name := Str(args, "name", id)
	repo, err := repoLocation(name)
	if err != nil {
		return resFail("%v", err)
	}
	if _, err := os.Stat(repo.path); os.IsNotExist(err) {
		return resOK(fmt.Sprintf("repository %s is already absent", name))
	}
	if c.Test {
		return resWould(fmt.Sprintf("repository %s would be removed (%s)", name, repo.path))
	}
	if err := os.Remove(repo.path); err != nil {
		return resFail("remove %s: %v", repo.path, err)
	}
	if out, err := refreshRepos(args, repo); err != nil {
		return resFail("repository %s removed, but refresh failed: %v: %s", name, err, strings.TrimSpace(out))
	}
	return resChanged(fmt.Sprintf("repository %s removed", name),
		map[string]string{repo.path: "removed"})
}

// refreshRepos updates the package manager's metadata after a change,
// unless the state turns it off with `refresh: false`.
func refreshRepos(args map[string]any, repo repoFile) (string, error) {
	if !Bool(args, "refresh", true) || len(repo.refresh) == 0 {
		return "", nil
	}
	return pkgRun(repo.refresh...)
}

// renderRepo builds the repository file for this platform. Every backend
// halite installs packages with has a definition here except pacman, brew,
// choco, and winget, whose repository models have no file to write.
func renderRepo(name string, args map[string]any) (repoFile, error) {
	switch {
	case runtime.GOOS == "freebsd":
		return freebsdRepo(name, args), nil
	case has("apt-get"):
		return aptRepo(name, args)
	case has("dnf"), has("yum"):
		return yumRepo(name, args, "/etc/yum.repos.d", refreshArgv()), nil
	case has("zypper"):
		return yumRepo(name, args, "/etc/zypp/repos.d", refreshArgv()), nil
	case has("apk"):
		return apkRepo(name, args)
	}
	return repoLocation(name)
}

// repoLocation is where this platform keeps the definition, without the
// content: enough for pkgrepo.absent, and the one error path both states
// share on a platform with no repository files.
func repoLocation(name string) (repoFile, error) {
	dir := ""
	switch {
	case runtime.GOOS == "freebsd":
		dir, name = "/usr/local/etc/pkg/repos", name+".conf"
	case runtime.GOOS == "windows", runtime.GOOS == "darwin":
		return repoFile{}, fmt.Errorf("pkgrepo is not implemented for %s", runtime.GOOS)
	case has("apt-get"):
		dir, name = "/etc/apt/sources.list.d", name+".list"
	case has("dnf"), has("yum"):
		dir, name = "/etc/yum.repos.d", name+".repo"
	case has("zypper"):
		dir, name = "/etc/zypp/repos.d", name+".repo"
	case has("apk"):
		dir = "/etc/apk/repositories.d"
	default:
		return repoFile{}, fmt.Errorf("pkgrepo is not implemented for this platform's package manager")
	}
	return repoFile{path: filepath.Join(dir, name), refresh: refreshArgv()}, nil
}

func refreshArgv() []string {
	switch {
	case runtime.GOOS == "freebsd":
		return []string{"pkg", "update", "-f"}
	case has("apt-get"):
		return []string{"apt-get", "update"}
	case has("dnf"):
		return []string{"dnf", "makecache"}
	case has("yum"):
		return []string{"yum", "makecache"}
	case has("zypper"):
		return []string{"zypper", "-n", "refresh"}
	case has("apk"):
		return []string{"apk", "update"}
	}
	return nil
}

// freebsdRepo writes a pkg(8) repository under /usr/local/etc/pkg/repos.
func freebsdRepo(name string, args map[string]any) repoFile {
	var b strings.Builder
	fmt.Fprintf(&b, "# managed by halite\n%s: {\n", name)
	fmt.Fprintf(&b, "  url: %q,\n", Str(args, "url", ""))
	if v := Str(args, "mirror_type", ""); v != "" {
		fmt.Fprintf(&b, "  mirror_type: %q,\n", v)
	}
	if v := Str(args, "signature_type", ""); v != "" {
		fmt.Fprintf(&b, "  signature_type: %q,\n", v)
	}
	if v := Str(args, "fingerprints", ""); v != "" {
		fmt.Fprintf(&b, "  fingerprints: %q,\n", v)
	}
	if v := Str(args, "priority", ""); v != "" {
		fmt.Fprintf(&b, "  priority: %s,\n", v)
	}
	fmt.Fprintf(&b, "  enabled: %s\n}\n", boolLiteral(Bool(args, "enabled", true)))
	return repoFile{
		path:    filepath.Join("/usr/local/etc/pkg/repos", name+".conf"),
		body:    b.String(),
		refresh: []string{"pkg", "update", "-f"},
	}
}

// aptRepo writes a one-line sources.list.d entry. `line` is taken verbatim
// when given; otherwise it is built from url, dist, and comps.
func aptRepo(name string, args map[string]any) (repoFile, error) {
	line := Str(args, "line", "")
	if line == "" {
		url := Str(args, "url", "")
		dist := Str(args, "dist", "")
		if url == "" || dist == "" {
			return repoFile{}, fmt.Errorf("apt repositories need url and dist (or a verbatim line)")
		}
		var options []string
		if arch := Str(args, "arch", ""); arch != "" {
			options = append(options, "arch="+arch)
		}
		if key := Str(args, "signed_by", ""); key != "" {
			options = append(options, "signed-by="+key)
		}
		kind := "deb"
		if Bool(args, "source", false) {
			kind = "deb-src"
		}
		line = kind
		if len(options) > 0 {
			line += " [" + strings.Join(options, " ") + "]"
		}
		line += " " + url + " " + dist
		if comps := strings.Join(List(args, "comps"), " "); comps != "" {
			line += " " + comps
		}
	}
	if !Bool(args, "enabled", true) {
		line = "# " + line
	}
	return repoFile{
		path:    filepath.Join("/etc/apt/sources.list.d", name+".list"),
		body:    "# managed by halite\n" + line + "\n",
		refresh: []string{"apt-get", "update"},
	}, nil
}

// yumRepo writes an INI-shaped repository for dnf, yum, or zypper. Keys
// other than the ones halite names are passed through, since the format is
// open-ended and the alternative is a state that cannot express them.
func yumRepo(name string, args map[string]any, dir string, refresh []string) repoFile {
	fields := map[string]string{
		"name":     Str(args, "humanname", name),
		"enabled":  boolDigit(Bool(args, "enabled", true)),
		"gpgcheck": boolDigit(Bool(args, "gpgcheck", true)),
	}
	for _, key := range []string{"baseurl", "metalink", "mirrorlist", "gpgkey", "priority", "module_hotfixes"} {
		if v := Str(args, key, ""); v != "" {
			fields[key] = v
		}
	}
	if _, ok := fields["baseurl"]; !ok {
		if v := Str(args, "url", ""); v != "" {
			fields["baseurl"] = v
		}
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	fmt.Fprintf(&b, "# managed by halite\n[%s]\n", name)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, fields[k])
	}
	return repoFile{path: filepath.Join(dir, name+".repo"), body: b.String(), refresh: refresh}
}

// apkRepo writes an Alpine repository line under /etc/apk/repositories.d,
// which apk reads alongside its main file.
func apkRepo(name string, args map[string]any) (repoFile, error) {
	url := Str(args, "url", "")
	if url == "" {
		return repoFile{}, fmt.Errorf("apk repositories need a url")
	}
	line := url
	if !Bool(args, "enabled", true) {
		line = "# " + line
	}
	return repoFile{
		path:    filepath.Join("/etc/apk/repositories.d", name),
		body:    "# managed by halite\n" + line + "\n",
		refresh: []string{"apk", "update"},
	}, nil
}

func boolLiteral(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func boolDigit(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
