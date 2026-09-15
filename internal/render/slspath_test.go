package render

import (
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// Salt's path helpers name the *directory* holding the current file.
//
// Its documentation is one line — "slspath: directory containing current
// sls (same as tpldir)" — and this build returned the SLS name with its
// dots turned into slashes, which is the directory only when the file is
// an `init.sls`. An estate's tree writes
// `{% from slspath ~ "/map.jinja" import users %}`, which is the ordinary
// formula idiom, and in `base/users/sudo.sls` that went looking for
// `base/users/sudo/map.jinja` instead of `base/users/map.jinja`.
//
// That is the `init.sls` distinction of the relative include, in a second
// place. Every value below was read back from `salt-call` on the same
// host rather than worked out from the documentation.
func TestSlsPathsAreTheDirectory(t *testing.T) {
	for _, c := range []struct {
		what, sls, file  string
		slsPath, tplFile string
	}{
		// A module: the file sits in a directory named by everything
		// but the last component.
		{"a plain .sls", "base.users.sudo", "/srv/x/base/users/sudo.sls", "base/users", "base/users/sudo.sls"},
		{"a nested .sls", "webserver.nginx", "webserver/nginx.sls", "webserver", "webserver/nginx.sls"},
		// A package: the SLS name *is* the directory.
		{"an init.sls", "base.cleanup", "/srv/x/base/cleanup/init.sls", "base/cleanup", "base/cleanup/init.sls"},
		{"a deep init.sls", "a.b.c", "a/b/c/init.sls", "a/b/c", "a/b/c/init.sls"},
		// A top-level module has no directory at all, which Salt spells
		// as the empty string.
		{"a top-level .sls", "top", "/srv/x/top.sls", "", "top.sls"},
	} {
		slsPath, tplFile := slsPaths(c.sls, c.file)
		if slsPath != c.slsPath {
			t.Errorf("%s: slspath = %q, want %q", c.what, slsPath, c.slsPath)
		}
		if tplFile != c.tplFile {
			t.Errorf("%s: tplfile = %q, want %q", c.what, tplFile, c.tplFile)
		}
	}
}

// The separator forms are the same value with its slashes swapped, and
// `tpldir` is "." where the directory is empty — both as Salt has them.
func TestSlsPathSeparatorForms(t *testing.T) {
	src := "slspath: {{ slspath }}\n" +
		"slsdotpath: {{ slsdotpath }}\n" +
		"slscolonpath: {{ slscolonpath }}\n" +
		"sls_path: {{ sls_path }}\n" +
		"tpldir: {{ tpldir }}\n" +
		"tplfile: {{ tplfile }}\n"
	res := renderSLS(t, src, Options{File: "base/users/sudo.sls", SLS: "base.users.sudo"})
	m := res.Value.(*value.Map)
	for k, want := range map[string]string{
		"slspath":      "base/users",
		"slsdotpath":   "base.users",
		"slscolonpath": "base:users",
		"sls_path":     "base_users",
		"tpldir":       "base/users",
		"tplfile":      "base/users/sudo.sls",
	} {
		if got, _ := m.Get(k); got != want {
			t.Errorf("%s = %#v, want %q", k, got, want)
		}
	}
}
