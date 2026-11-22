package rendersandbox

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// probeAsChild runs the probes a live test asked for, in the child and
// therefore under whatever confinement the child is under, and reports
// each on stderr as "probe <name>: ok" or "probe <name>: <error>".
//
// The child's own hello says what account it has and what interfaces it
// sees. These say what that means for what it can do: a root-only file it
// cannot open and a listener on the host it cannot reach are the
// boundaries an operator cares about, and neither is the same claim as a
// uid or an interface list.
func probeAsChild(args []string) {
	for _, arg := range args {
		name, target, ok := strings.Cut(arg, "=")
		if !ok {
			continue
		}
		var err error
		switch name {
		case "probe-read":
			_, err = os.ReadFile(target)
		case "probe-dial":
			var c net.Conn
			c, err = net.DialTimeout("tcp", target, 2*time.Second)
			if err == nil {
				c.Close()
			}
		default:
			continue
		}
		result := "ok"
		if err != nil {
			result = err.Error()
		}
		fmt.Fprintf(os.Stderr, "probe %s: %s\n", name, result)
	}
}

// lockedBuffer is the child's stderr, written by exec's copying goroutine
// and read by the test.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// probeResult waits for the child to report a probe and returns what it
// said.
func (l *lockedBuffer) probeResult(name string) (string, bool) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(l.String(), "\n") {
			if rest, ok := strings.CutPrefix(line, "probe "+name+": "); ok {
				return rest, true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return "", false
}
