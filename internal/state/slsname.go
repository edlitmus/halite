package state

import (
	"fmt"
	"strings"
)

// CanonicalSLS is the one definition of what an SLS name may look like,
// and every path that turns a name into a file, or decides whether a
// name may be loaded, goes through it.
//
// The reason it exists is a pair that had to agree and did not. The
// compiler's state_denylist and state_allowlist matched the name as
// written; the loader turned dots into slashes and cleaned the result.
// So `secret.x`, `secret..x`, `secret/x`, `.secret.x` and `secret/./x`
// were five names to the denylist and one file to the loader, and a
// denylist entry for `secret.x` refused only the first spelling. Salt
// accepts every one of them as well -- its `get_state` replaces dots
// with slashes and the file server joins the result -- but Salt has no
// state denylist for the spellings to get round, so the looseness was
// harmless there and is not here.
//
// The fix is to have one spelling, not to teach the matcher the
// loader's arithmetic. A name is canonical when it is non-empty, every
// dot-separated component is non-empty, and it contains no path
// separator. That rules out a leading or trailing dot, `..`, a `.`
// component (which can only be written with a slash), and any slash at
// all. A non-canonical name is refused with the canonical spelling in
// the message rather than silently rewritten: rewriting would make the
// denylist correct, but it would also leave two spellings that work,
// and the next check somebody writes against the raw name would have
// the same hole. Refusing leaves exactly one.
//
// Salt's relative include, `include: - .foo`, is not affected. It is
// legitimate syntax, and it is resolved to the absolute name by
// resolveRelative while the including file is parsed -- before the name
// reaches loadSLS, and so before both this check and the denylist. A
// relative include therefore arrives here already canonical, and a
// denylist entry for `web.secret` refuses `.secret` written inside
// `web.nginx`.
//
// It is exported so the hub's policy (deny_sls/allow_sls in
// internal/policy) can apply the same definition to the arguments of
// state.sls and state.apply, which today it matches as written.
// DIVERGENCE 5.294.
func CanonicalSLS(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("an SLS name may not be empty")
	}
	if strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("SLS name %q contains a path separator; write it with dots, as %q",
			name, suggestSLS(name))
	}
	for _, comp := range strings.Split(name, ".") {
		if comp == "" {
			return "", fmt.Errorf("SLS name %q has an empty component (a leading, trailing or doubled dot); write it as %q",
				name, suggestSLS(name))
		}
	}
	return name, nil
}

// suggestSLS is what a non-canonical name would load, spelled the one
// way that is accepted. It is only ever shown to a person: it is never
// used as the name, which is the point of refusing rather than
// rewriting.
func suggestSLS(name string) string {
	s := strings.NewReplacer("/", ".", "\\", ".").Replace(name)
	var comps []string
	for _, c := range strings.Split(s, ".") {
		if c != "" {
			comps = append(comps, c)
		}
	}
	return strings.Join(comps, ".")
}
