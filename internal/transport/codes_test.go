package transport

import (
	"errors"
	"net/http"
	"testing"
)

// A hub's wording may improve; a node's behaviour must not change when
// it does. That is what the codes are for, and a 5xx used to drop its
// code on the floor — so a node could not tell "this hub compiles no
// pillar" from "this hub's pillar did not compile" except by matching
// prose. The two call for opposite behaviour: fall back to local roots
// for the first, and never for the second.
func TestAServerFailureKeepsItsCode(t *testing.T) {
	err := decodeError("/v1/pillar",
		[]byte(`{"error":"this hub compiles no pillar; set pillar_roots","code":"no_pillar"}`),
		http.StatusServiceUnavailable)
	if got := CodeOf(err); got != CodeNoPillar {
		t.Errorf("CodeOf = %q, want %q", got, CodeNoPillar)
	}
	// Still not a refusal: a 5xx is worth retrying and must not be
	// mistaken for one the hub understood and declined.
	if Permanent(err) {
		t.Error("a 5xx was reported as permanent")
	}

	// A compile failure carries a different code, so the two are
	// distinguishable without reading the message.
	other := decodeError("/v1/pillar",
		[]byte(`{"error":"the hub could not compile this node's pillar","code":"internal"}`),
		http.StatusInternalServerError)
	if CodeOf(other) == CodeNoPillar {
		t.Error("a compile failure was reported as a hub that compiles no pillar")
	}

	// And a 4xx keeps working the way it did.
	refused := decodeError("/v1/jobs", []byte(`{"error":"no","code":"refused"}`), http.StatusForbidden)
	if !Permanent(refused) {
		t.Error("a 4xx stopped being permanent")
	}
	if CodeOf(refused) != CodeRefused {
		t.Errorf("CodeOf on a refusal = %q", CodeOf(refused))
	}

	var status *StatusError
	if !errors.As(err, &status) {
		t.Error("a 5xx is not a StatusError")
	}
}
