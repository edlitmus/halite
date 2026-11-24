package fileserver

import (
	"fmt"
	"slices"
	"sync"
	"testing"
)

// Each job's copy of the agent's Remote names its own environment, and
// neither another job nor the agent may see it (issue 849).
//
// The copies are shallow, so they share the agent's Environments array.
// ForEnv appended to it in place; with spare capacity every copy wrote
// the same slot, and a job could compile against another job's
// environment. The agent's Remote is built today with a one-element
// literal, whose capacity is its length, so append always reallocated and
// production never reached the shared slot -- which is why this test makes
// the capacity itself rather than going through the agent.
func TestForEnvOnCopiesWithSpareCapacityKeepsEachCopysEnvironment(t *testing.T) {
	shared := make([]string, 1, 16)
	shared[0] = "base"
	agent := NewRemote(nil, t.TempDir(), shared)

	const jobs = 8
	copies := make([]*Remote, jobs)
	var start, done sync.WaitGroup
	start.Add(1)
	for i := range jobs {
		done.Add(1)
		go func() {
			defer done.Done()
			copied := agent.ForRun()
			start.Wait()
			copies[i] = copied.ForEnv(fmt.Sprintf("env%d", i))
		}()
	}
	start.Done()
	done.Wait()

	// Read after every job has switched, not as each one does: a job
	// reads its environment for as long as it runs, and reading at once
	// would let a goroutine see its own write before the next one landed
	// on it, which is a pass by scheduling. This way the shared slot holds
	// the last writer's environment for all of them, -race or not.
	for i, copied := range copies {
		got := copied.Envs()
		want := []string{"base", fmt.Sprintf("env%d", i)}
		if !slices.Equal(got, want) {
			t.Errorf("job %d sees %v; want %v", i, got, want)
		}
	}
	if got := agent.Envs(); !slices.Equal(got, []string{"base"}) {
		t.Errorf("the agent's own Remote sees %v after the jobs; want [base]", got)
	}
}
