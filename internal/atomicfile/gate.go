package atomicfile

// MaxConcurrentWrites is how many writes through this package, and through
// Bounded, may be in the kernel at once in one process. The rest wait their
// turn as parked goroutines.
//
// Why there must be a number at all. Go runs a goroutine that is waiting on
// the network without an operating-system thread: the poller holds it, and a
// hub with 20,000 idle streams has a few hundred threads. A goroutine in
// fsync, rename or unlink is in the kernel, and keeps its thread until the
// call returns; the scheduler starts another to run everything else. So the
// number of threads follows the number of writes in progress, and Go kills a
// process that reaches 10,000 (debug.SetMaxThreads) with `fatal error:
// thread exhaustion`, which nothing can recover from. Every write here
// fsyncs twice -- the file and its directory -- and the hub makes one per
// return filed and one per stream opened, from request handlers, with
// nothing counting them. On 2026-11-11 that killed a hub twice at 20,000
// simulated nodes: once 1.2 s after a `test.ping` reached them all, once 18
// s into a clean restart. DIVERGENCE 5.279, and 5.653 for this.
//
// Why 64. The writes are bound by the device, not the CPU, so GOMAXPROCS
// is the wrong thing to scale by: a 2-core hub on a fast disk can usefully
// have as many in flight as a 16-core one. Sixty-four keeps an NVMe or EBS
// queue busy and lets a journalling filesystem fold concurrent fsyncs into
// one commit, which is most of the throughput there is; it is under 1% of
// Go's limit, so the threads it can hold are not what a hub runs out of.
// One -- every write in turn -- would be safe and slow, because it gives up
// that folding: the 20,000 reconnects of a restart would wait for 20,000
// commits one after another, each node's stream with them.
//
// Why here and not in the hub's handlers. Three reasons, in order of how
// much each costs to get wrong.
//
//   - A bound in a handler is held across whatever the handler waits on,
//     and the hub's handlers wait on each other: a return from a node whose
//     first copy is still being written waits for it (job.Cache.File), and
//     job records are edited under a per-job lock. With every slot held by
//     a waiter, the writer they wait for never gets one. Inside this
//     package the slot is held only across system calls on a file this
//     call created, which wait on nothing but the disk, so it cannot be
//     part of a cycle.
//   - It covers the writes nobody listed. The crash's stacks named two
//     callers, the node cache and the return file; the job record, the mine,
//     orchestration state and the key store write the same way, and the next
//     one to be added will too.
//   - The node is bounded as well, which costs it nothing: it writes a
//     handful of files at a time.
//
// Why not take the write off the request path instead, for the node cache
// at least, whose record is mostly a last-seen time. Because the grains in
// it are what targeting reads, and subscribe records them before the stream
// opens so that a job dispatched in the same second as a node connects does
// not miss it; an asynchronous write is a window in which it does. And it
// would fix one caller of two, leaving returns -- which must be on disk
// before they are acknowledged -- exactly where they were.
//
// What it does not bound: reads, which are served from the page cache and
// return in microseconds, so they do not accumulate; and fsyncs outside this
// package that are already one at a time under a lock (the event bus, the
// signed-submission replay file). Those that are not go through Bounded,
// as the hub's evidence anchors do. Nor WriteIn and WriteInFrom, which are
// archive extraction on a node, one entry at a time: WriteInFrom copies
// from a reader that may be a download, and a slot held across a slow
// network read would stall every other write in the process behind it.
// The return index's ReadDir and a job record's read are reads too.
const MaxConcurrentWrites = 64

var writeSlots = make(chan struct{}, MaxConcurrentWrites)

// Bounded runs fn holding one of the MaxConcurrentWrites slots, for a
// caller outside this package that makes a write with an fsync and has
// nothing else bounding how many of them run at once.
//
// fn must not call Write, WritePrepared or Bounded: it would wait for a
// second slot while holding one, and sixty-four callers doing that at once
// wait for ever. It must not wait on anything but the disk, for the same
// reason.
func Bounded(fn func() error) error {
	writeSlots <- struct{}{}
	defer func() { <-writeSlots }()
	return fn()
}
