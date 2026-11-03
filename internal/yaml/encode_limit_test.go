package yaml

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// TestEncodeStopsAtMaxBytes: block style indents each level, so a
// mapping nested 200 deep is about 40 kB of output from a few hundred
// bytes of keys. With MaxBytes set the encoder stops soon after it is
// passed, rather than building all of it for the caller to refuse.
func TestEncodeStopsAtMaxBytes(t *testing.T) {
	var v any = int64(1)
	for range 200 {
		v = value.MapOf("a", v)
	}
	full := Encode(v, EncodeOptions{})
	if len(full) < 30_000 {
		t.Fatalf("the full encoding is %d bytes; the test needs it large", len(full))
	}
	out := Encode(v, EncodeOptions{MaxBytes: 1000})
	if len(out) <= 1000 {
		t.Fatalf("stopped at %d bytes, which a caller cannot tell from a complete document", len(out))
	}
	// It overshoots by at most the line it was writing: 2 spaces a
	// level at 200 levels, and the key.
	if len(out) > 1000+2*200+10 {
		t.Fatalf("wrote %d bytes past a limit of 1000", len(out))
	}
	if !strings.HasPrefix(full, out[:1000]) {
		t.Fatalf("the stopped output is not a prefix of the full one")
	}
	// Flow style is not indented and is not stopped.
	if got := Encode(v, EncodeOptions{Flow: true, MaxBytes: 10}); got != Encode(v, EncodeOptions{Flow: true}) {
		t.Fatalf("flow output changed under MaxBytes")
	}
}
