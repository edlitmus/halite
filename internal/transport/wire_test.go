package transport

import (
	"net/http"
	"strings"
	"testing"
)

// A refusal and a fault are different answers, and a caller that cannot
// tell them apart retries the refusal.
//
// A node posting the return of a job the hub has no record of did
// exactly that: five attempts with backoff, for every scheduled run.
func TestARefusalIsDistinguishableFromAFault(t *testing.T) {
	cases := []struct {
		status    int
		permanent bool
	}{
		{http.StatusBadRequest, true},
		{http.StatusForbidden, true},
		{http.StatusNotFound, true},
		{http.StatusRequestTimeout, false},
		{http.StatusTooManyRequests, false},
		{http.StatusInternalServerError, false},
		{http.StatusServiceUnavailable, false},
	}
	for _, c := range cases {
		err := decodeError("/v1/return", []byte(`{"error":"no such job","code":"malformed"}`), c.status)
		if got := Permanent(err); got != c.permanent {
			t.Errorf("%d: Permanent = %v, want %v", c.status, got, c.permanent)
		}
		if !strings.Contains(err.Error(), "no such job") {
			t.Errorf("%d: the message is %q", c.status, err)
		}
	}

	// A body that is not the error shape still carries the status.
	err := decodeError("/v1/return", []byte("not json"), http.StatusForbidden)
	if !Permanent(err) {
		t.Error("a 403 with an unreadable body was treated as transient")
	}
}
