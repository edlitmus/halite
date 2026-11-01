package hub

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

// Two publications from one node are both kept.
//
// `Update` reads the node's file, adds its functions and writes it back, and
// `Delete` of one function does the same without them. Run together for one
// node they interleaved, each writing back a file that lacked the other's
// change, so a function a node had just published could vanish until it
// published again. DIVERGENCE 5.520.
func TestConcurrentMinePublicationsAreAllKept(t *testing.T) {
	store, err := OpenMineStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := store.Update("web1.example", map[string]*MineEntry{
				fmt.Sprintf("fn%02d", i): {Data: json.RawMessage(`1`)},
			})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	data, err := store.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Functions) != n {
		t.Errorf("%d of %d concurrently published functions survived", len(data.Functions), n)
	}
}

// A function removed while another is published does not bring the removed
// one back or lose the new one.
func TestAMineDeleteDoesNotLoseAConcurrentPublication(t *testing.T) {
	store, err := OpenMineStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seed := map[string]*MineEntry{}
	for i := 0; i < 20; i++ {
		seed[fmt.Sprintf("old%02d", i)] = &MineEntry{Data: json.RawMessage(`1`)}
	}
	if err := store.Update("web1.example", seed); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if err := store.Delete("web1.example", fmt.Sprintf("old%02d", i)); err != nil {
				t.Error(err)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			if err := store.Update("web1.example", map[string]*MineEntry{
				fmt.Sprintf("new%02d", i): {Data: json.RawMessage(`2`)},
			}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	data, err := store.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, ok := data.Functions[fmt.Sprintf("new%02d", i)]; !ok {
			t.Errorf("new%02d, published during the deletes, was lost", i)
		}
		if _, ok := data.Functions[fmt.Sprintf("old%02d", i)]; ok {
			t.Errorf("old%02d was deleted and came back", i)
		}
	}
}
