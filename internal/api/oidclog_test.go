package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/log"
)

// What an unauthenticated caller puts in the callback's `error` and
// `error_description` reaches the log bounded, and the state that
// names a login in progress does not reach it at all.
func TestTheOIDCLogHoldsNeitherTheStateNorAnUnboundedError(t *testing.T) {
	l, _ := oidcLab(t, nil)
	var logged bytes.Buffer
	logger, err := log.New(log.Options{Level: log.Debug, Stderr: &logged})
	if err != nil {
		t.Fatal(err)
	}
	l.server.Log = logger

	res, body := l.post(t, PathLoginOIDC, `{}`, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("starting a login answered %d: %s", res.StatusCode, body)
	}
	var start AuthStartResponse
	if err := json.Unmarshal([]byte(body), &start); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logged.String(), start.State) {
		t.Errorf("the log holds the state that completes the login:\n%s", logged.String())
	}
	if !strings.Contains(logged.String(), stateRef(start.State)) {
		t.Errorf("the log does not name the login at all:\n%s", logged.String())
	}

	huge := strings.Repeat("z", 200_000)
	res, _ = l.post(t, PathLoginOIDCBack,
		`{"error":"`+huge+`","error_description":"`+huge+`"}`, "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a provider refusal answered %d", res.StatusCode)
	}
	if n := strings.Count(logged.String(), "z"); n > 2*maxProviderErrorLog {
		t.Errorf("the log took %d bytes of the caller's error text", n)
	}
	if !strings.Contains(logged.String(), "(200000 bytes)") {
		t.Errorf("the log does not say the text was cut:\n%.400s", logged.String())
	}
}

func TestClipForLogCutsOnARuneBoundary(t *testing.T) {
	got := clipForLog(strings.Repeat("é", 200), 255)
	if !strings.HasPrefix(got, strings.Repeat("é", 127)+"…") {
		t.Errorf("the cut split a rune: %q", got[:20])
	}
	if clipForLog("short", 255) != "short" {
		t.Error("a short string was changed")
	}
}
