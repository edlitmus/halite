//go:build linux || darwin || freebsd

package metrics

import (
	"runtime"
	"syscall"
	"testing"
)

// **process_open_fds is the number of descriptors that exist**, counted
// a second way: fcntl(F_GETFD) on every number from 0 to 4095, which
// succeeds exactly for an open descriptor and opens nothing itself.
//
// TestOpenDescriptorsCountWhatIsOpened checks that the count moves by
// what is opened, which an off-by-one cannot fail. This checks the
// figure. On Linux and macOS it is the "less one" in countDir, for the
// descriptor that reads the directory; on FreeBSD it is kern.proc.nfds.
// A test binary's descriptors are all far below 4096, and the test
// refuses to judge if one is not.
func TestOpenDescriptorsAgreeWithAProbe(t *testing.T) {
	if !fdsOnThisPlatform() {
		t.Skipf("no descriptor reader on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	r := NewRegistry()
	r.Process("t")
	const limit = 4096
	probe := func() int {
		n := 0
		for fd := 0; fd < limit; fd++ {
			if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0); errno == 0 {
				n++
			}
		}
		return n
	}
	// Twice, and judged only if the probe agrees with itself on either
	// side of the scrape: the runtime may open a descriptor (a netpoller,
	// a timer) between two reads, and that is not the reader's error.
	for attempt := 0; attempt < 5; attempt++ {
		before := probe()
		got := scrape(t, r)[`process_open_fds{component="t"}`]
		after := probe()
		if before != after {
			continue
		}
		if int(got) != before {
			t.Fatalf("process_open_fds = %.0f, but %d descriptors below %d answer fcntl(F_GETFD)", got, before, limit)
		}
		return
	}
	t.Skip("the descriptor count kept changing under the probe; nothing to judge")
}
