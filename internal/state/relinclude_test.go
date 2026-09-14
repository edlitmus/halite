package state

import (
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// Salt's leading-dot include, and the difference an `init.sls` makes.
//
// Two files share the SLS name `web.nginx`: `web/nginx.sls` and
// `web/nginx/init.sls`. In the first, `.foo` is a sibling -- `web.foo`.
// In the second it is a child -- `web.nginx.foo` -- because an
// `init.sls` *is* its directory rather than a file inside one.
//
// This was one rule, the sibling one, applied to both. Every relative
// include in every `init.sls` therefore resolved one level too high,
// and it went unnoticed because the tests used flat SLS files, where
// the two rules agree. What found it was compiling an estate's real
// tree: `base/cleanup/init.sls` includes `.datadog`, which is
// `base.cleanup.datadog`, and the compiler reported
// `sls "base.datadog" was not found`.
//
// "Not found" is the lucky outcome. Where a file of that name does
// exist at the parent level -- and in a tree with `base/init.sls` and
// `base/cleanup/init.sls` that is ordinary -- the wrong file is
// included and nothing says so. DIVERGENCE 5.84.
func TestRelativeIncludesResolveAgainstTheRightPackage(t *testing.T) {
	for _, c := range []struct {
		name, sls string
		pkg       bool
		want      string
	}{
		// An init.sls is its directory: `.foo` is a child.
		{".datadog", "base.cleanup", true, "base.cleanup.datadog"},
		{".foo", "web.nginx", true, "web.nginx.foo"},
		{".foo", "base", true, "base.foo"},

		// A flat .sls is a file in its directory: `.foo` is a sibling.
		{".foo", "web.nginx", false, "web.foo"},
		{".datadog", "base.cleanup", false, "base.datadog"},
		{".foo", "top", false, "foo"},

		// Each extra dot climbs one more level, from whichever of the
		// two starting points applies.
		{"..foo", "a.b.c", false, "a.foo"},
		{"..foo", "a.b.c", true, "a.b.foo"},
		{"...foo", "a.b.c", true, "a.foo"},

		// An absolute name is untouched either way.
		{"base.security", "base.cleanup", true, "base.security"},
		{"base.security", "base.cleanup", false, "base.security"},
	} {
		var diags Diags
		got := resolveRelative(c.name, c.sls, c.pkg, c.sls, value.Pos{}, &diags)
		if got != c.want {
			t.Errorf("resolveRelative(%q, %q, pkg=%v) = %q, want %q",
				c.name, c.sls, c.pkg, got, c.want)
		}
		if len(diags) != 0 {
			t.Errorf("resolveRelative(%q, %q, pkg=%v) reported %v",
				c.name, c.sls, c.pkg, diags)
		}
	}
}

// Climbing past the root is Salt's error rather than a name. Resolving
// it to a top-level `foo` is how a tree acquires a state nobody meant,
// so it is reported.
func TestARelativeIncludeThatClimbsTooFarIsRefused(t *testing.T) {
	var diags Diags
	resolveRelative("...foo", "web", false, "web", value.Pos{}, &diags)
	if len(diags) == 0 {
		t.Fatal("a relative include climbing above the root was accepted silently")
	}
}

func TestIsPackageIsAboutInitSLS(t *testing.T) {
	for path, want := range map[string]bool{
		"base/cleanup/init.sls": true,
		"init.sls":              true,
		"base/cleanup.sls":      false,
		"base/initial.sls":      false,
		"base/init.sls.jinja":   false,
	} {
		if got := isPackage(path); got != want {
			t.Errorf("isPackage(%q) = %v, want %v", path, got, want)
		}
	}
}
