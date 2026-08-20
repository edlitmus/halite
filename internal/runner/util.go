package runner

import (
	"crypto/rand"
	"encoding/binary"
	"os"
	"time"
)

// fileExists backs the `creates` option: the state is skipped when the
// named path is already there.
func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// splay returns a random offset in [0, max].
//
// crypto/rand rather than math/rand: SPEC section 25.3 permits math/rand
// nowhere outside the deterministic template seed, and the build policy
// test enforces it. Drawing a few bytes here costs nothing next to the
// retry interval it is added to.
func splay(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return max / 2
	}
	n := binary.BigEndian.Uint64(b[:]) % uint64(max+1)
	return time.Duration(n)
}
