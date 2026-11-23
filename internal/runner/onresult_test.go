package runner

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/states"
)

// OnResult hears every result, in order, as it is made: the one that ran,
// the one seeded by a resume and the one a requisite skipped. An
// orchestration's record is written from it while the run goes
// (DIVERGENCE 5.707), and a record written partway through has to name
// the seeded and skipped steps before the one in flight, or a resume of
// it runs them again.
func TestOnResultHearsEveryResultAsItIsMade(t *testing.T) {
	var heard []string
	out, _ := compileAndRun(t, `
seeded:
  probe.run: []

failing:
  probe.run:
    - fail: true

skipped:
  probe.run:
    - require:
      - probe: failing

last:
  probe.run: []
`, func(r *Runner) {
		r.Seed = map[string]states.Result{"seeded": states.True("seeded")}
		r.OnResult = func(sofar []*StateResult) {
			ids := make([]string, len(sofar))
			for i, sr := range sofar {
				ids[i] = sr.Chunk.ID
			}
			heard = append(heard, strings.Join(ids, ","))
		}
	})
	want := []string{"seeded", "seeded,failing", "seeded,failing,skipped", "seeded,failing,skipped,last"}
	if strings.Join(heard, " | ") != strings.Join(want, " | ") {
		t.Errorf("OnResult heard\n  %s\nwant\n  %s", strings.Join(heard, " | "), strings.Join(want, " | "))
	}
	if len(out.Results) != 4 {
		t.Errorf("the run has %d results", len(out.Results))
	}
}
