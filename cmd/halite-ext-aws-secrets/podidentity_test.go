package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This extension is started with PATH and HOME only, so EKS Pod Identity
// reaches it through two keys in its block rather than through the pod's
// environment. The block's keys have to arrive at a request that carries
// the token: parsed, handed to the provider, and used, with the process's
// own environment playing no part.
func TestTheBlockReachesPodIdentityWithoutTheEnvironment(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "pod-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{
			"AccessKeyId": "ASIAPOD", "SecretAccessKey": "s", "Token": "t",
			"Expiration": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	defer srv.Close()
	tokenFile := filepath.Join(t.TempDir(), "eks-pod-identity-token")
	if err := os.WriteFile(tokenFile, []byte("pod-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{
		"region":                             "us-west-2",
		"container_credentials_full_uri":     srv.URL + "/v1/credentials",
		"container_authorization_token_file": tokenFile,
	})
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.provider()
	// Nothing from the environment, as under the extension runtime.
	p.Environ = func(string) string { return "" }
	creds, err := p.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("Retrieve: %v (the endpoint saw %q)", err, seen)
	}
	if creds.AccessKeyID != "ASIAPOD" || len(seen) != 1 || seen[0] != "pod-token" {
		t.Errorf("key %q, headers %q", creds.AccessKeyID, seen)
	}
}
