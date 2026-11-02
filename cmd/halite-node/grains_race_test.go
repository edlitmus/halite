package main

import (
	"sync"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// The grains are replaced while jobs start. `reloadRuntimeConfig` and the
// refresh loop assigned the field and every job copied the node, which
// reads it: run under -race the two reported a data race. DIVERGENCE 5.553.
func TestReplacingTheGrainsWhileJobsStartIsNotARace(t *testing.T) {
	n := nodeWithBrokenPillar(t)
	n.grainsMu = &sync.RWMutex{}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 3; i++ {
			if err := n.reloadRuntimeConfig("grains"); err != nil {
				t.Error(err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			n.setGrains(value.MapOf("n", int64(i)))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 5000; i++ {
			job := n.forJob()
			if job.grainsNow() == nil {
				t.Error("a job started with no grains")
				return
			}
			_ = n.grainsNow().Len()
		}
	}()
	wg.Wait()
}
