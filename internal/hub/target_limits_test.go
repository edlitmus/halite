package hub

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/fnmatch"
	"github.com/edlitmus/halite/internal/target"
)

// The hub refuses an over-long target and an over-long glob at Dispatch,
// with an ordinary error and before any node is considered, whatever the
// caller in front of it did. The API checks the length too; the hub's
// own bound is what holds for the CLI, a runner and anything else that
// submits. DIVERGENCE 5.558.
func TestDispatchRefusesAnOverLongTarget(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	for _, c := range []struct{ name, kind, tgt, want string }{
		{"expression", "L", strings.Repeat("web1.example,", target.MaxExprLen/13+1), "limit"},
		{"glob", "glob", "web*" + strings.Repeat("a", fnmatch.MaxPatternLen), "too long"},
		{"grain glob", "G", "os:*" + strings.Repeat("a", fnmatch.MaxPatternLen), "too long"},
	} {
		t.Run(c.name, func(t *testing.T) {
			j, err := l.server.Dispatch(Submission{Fun: "test.ping", Target: c.tgt, TargetKind: c.kind})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error saying %q, got job %v, err %v", c.want, j, err)
			}
			if len(err.Error()) > 300 {
				t.Fatalf("the error is %d bytes; it should not quote the target", len(err.Error()))
			}
		})
	}
	if _, err := l.server.Dispatch(Submission{Fun: "test.ping", Target: "web*", TargetKind: "glob"}); err != nil {
		t.Fatalf("an ordinary glob: %v", err)
	}
}
