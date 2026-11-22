package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/transport"
)

// A quiesce or drain changes nothing on a node, and the node says so at
// warn, by name, with what to do instead. Neither is part of the protocol
// (issue 782, DIVERGENCE 5.699.1), but earlier drafts of SPEC 6.2 named
// both, so a peer of another version may send one. It was an info line
// naming only a type, so an operator who asked for a quiesce had nothing
// to find.
func TestAMessageTheNodeDoesNotActOnIsWarnedAboutByName(t *testing.T) {
	for _, kind := range []string{transport.MsgQuiesce, transport.MsgDrain} {
		t.Run(kind, func(t *testing.T) {
			var logged bytes.Buffer
			n := &node{log: testLogger(t, &logged)}

			if err := n.handle(transport.Message{T: kind, Reason: "maintenance window"}); err != nil {
				t.Fatalf("the node stopped on a %s message: %v", kind, err)
			}

			line := logged.String()
			for _, want := range []string{`"level":"warn"`, `"type":"` + kind + `"`, "does not act on", "maintenance window", "set quiesce in this node's configuration"} {
				if !strings.Contains(line, want) {
					t.Errorf("the log does not say %q:\n%s", want, line)
				}
			}
		})
	}
}
