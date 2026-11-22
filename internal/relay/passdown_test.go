package relay

import (
	"testing"

	"github.com/edlitmus/halite/internal/transport"
)

// A relay passes quiesce and drain down although neither is part of the
// protocol (DIVERGENCE 5.699.1). The node behind it is what warns, by
// name, that it did nothing; a relay that dropped them would leave that
// node silent and the operator looking at the wrong machine.
//
// This holds the relay's choice, not the delivery: the path down is
// forwardDown, which revoke, reload and kill share, and a subordinate's
// stream cannot be attached from outside package hub.
func TestARelayPassesDownTheMessagesItsNodesWarnAbout(t *testing.T) {
	for _, kind := range []string{transport.MsgQuiesce, transport.MsgDrain} {
		if !passesDown(kind) {
			t.Errorf("a relay drops %q, so the node behind it never says it ignored one", kind)
		}
	}
}

// A ping is the upstream checking on the relay itself, and a type the
// relay does not know is nothing it can vouch for to its subordinates.
func TestARelayKeepsAPingAndAnUnknownTypeToItself(t *testing.T) {
	for _, kind := range []string{transport.MsgPing, "an-unknown-type"} {
		if passesDown(kind) {
			t.Errorf("a relay passes %q down", kind)
		}
	}
}
