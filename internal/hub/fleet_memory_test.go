package hub

import (
	"fmt"
	"runtime"
	"testing"
)

// What one connected node costs the hub before anything is sent to it.
//
// The outbound queue was a channel of queueDepth transport.Message
// values, and a buffered channel allocates its whole buffer when it is
// made: 19 KiB per node, 381 MiB at 20,000, measured as the largest
// single item in a running hub's heap (DIVERGENCE 5.661). Nothing about
// the hub's behaviour shows it -- every test passed either way -- so it
// is held here as a number. 4 KiB per stream leaves room for the stream
// itself, its map entry and its done channel, and is a fifth of what the
// value queue cost.
func TestAnIdleStreamCostsLittle(t *testing.T) {
	const n = 2000
	f := newFleet()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	streams := make([]*stream, 0, n)
	for i := range n {
		st, ok := f.attachLimited(fmt.Sprintf("n%04d.example", i), nowForTest(), n)
		if !ok {
			t.Fatalf("stream %d was refused", i)
		}
		streams = append(streams, st)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(streams)
	per := (int64(after.HeapAlloc) - int64(before.HeapAlloc)) / n
	t.Logf("%d bytes of heap per idle stream (queueDepth %d)", per, queueDepth)
	if per > 4<<10 {
		t.Errorf("an idle stream costs %d bytes of heap, want at most %d", per, 4<<10)
	}
}
