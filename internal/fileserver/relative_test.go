package fileserver

import (
	"reflect"
	"testing"
)

// **A listing cannot place a file outside the destination** (issue 321,
// section 5). relativeTo trimmed the prefix and kept what was left, so a
// manifest entry with `..` in it -- which only a hub that is lying sends,
// and SPEC 25.6 does not trust the hub -- reached file.recurse as
// "../../etc/cron.d/x" and was written there. And the prefix was trimmed
// as a string, not a path: under "web", an entry "webby/x" came back as
// "by/x", a file of another directory copied in.
func TestRelativeToRefusesWhatLeavesTheDestination(t *testing.T) {
	m := func(paths ...string) *Manifest {
		out := &Manifest{}
		for _, p := range paths {
			out.Files = append(out.Files, Entry{Path: p})
		}
		return out
	}
	got, err := relativeTo("web", m("web/a", "/web/b/c", "webby/x", "web"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b/c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("relativeTo = %q, want %q: an entry outside the prefix's directory was kept", got, want)
	}
	for _, bad := range []string{"web/x/../../../etc/cron.d/evil", "web/../y", "web/a/../../b"} {
		if got, err := relativeTo("web", m("web/ok", bad)); err == nil {
			t.Errorf("an entry %q was listed as %q", bad, got)
		}
	}
}
