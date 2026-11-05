package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/awsauth"
)

// A node that names many secrets cannot grow the cache past its bound
// (issue 622). The cache never evicted, so every value any node had had
// fetched stayed for the life of the process. Driven through the
// handler with the default bound and more secrets than it holds, named
// the way a node names them: its own grain, admitted by the allow list.
func TestANodeNamingManySecretsDoesNotGrowTheCachePastItsBound(t *testing.T) {
	n := maxCacheEntries + 50
	values := map[string]string{}
	var wants []any
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("vmop/s%04d", i)
		values[id] = "v"
		wants = append(wants, map[string]any{"name": fmt.Sprintf("k%04d", i), "secret_id": id})
	}
	f := newFakeSecrets(t, values)
	grains, _ := json.Marshal(map[string]any{"wants": wants})

	out, err := f.call(t, `{"node_grain":"wants","node_grain_allow":["vmop/*"]}`, string(grains), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(out["aws_secrets"].(map[string]any)); got != n {
		t.Fatalf("the pillar holds %d secrets, want %d: the bound must not cost the node any", got, n)
	}
	if got := cache.len(); got != maxCacheEntries {
		t.Errorf("the cache holds %d values after %d were fetched, want the bound %d", got, n, maxCacheEntries)
	}
}

// Least recently used goes first, not least recently fetched: a secret
// every compilation reads stays cached while a node churns through
// others.
func TestTheCacheEvictsTheLeastRecentlyUsed(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"a": "1", "b": "2", "c": "3", "d": "4"})
	c := newSecretCache(time.Now)
	c.maxEntries = 3
	client := &secretsClient{
		Provider: &awsauth.Provider{Explicit: awsauth.Credentials{AccessKeyID: "A", SecretAccessKey: "B"}},
		Endpoint: f.server.URL,
		Client:   f.server.Client(),
	}
	get := func(id string) {
		t.Helper()
		if _, err := c.fetch(t.Context(), client, Secret{Key: id, SecretID: id}, "us-east-1", time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	get("a")
	get("b")
	get("c")
	get("a") // a is now the most recently used; b the least
	get("d") // evicts b
	if c.len() != 3 {
		t.Errorf("it holds %d", c.len())
	}
	before := len(f.calls)
	get("a")
	get("c")
	get("d")
	if len(f.calls) != before {
		t.Errorf("a, c or d was evicted: calls went from %d to %d (%v)", before, len(f.calls), f.calls)
	}
	get("b")
	if len(f.calls) != before+1 {
		t.Errorf("b was still held: calls %v", f.calls)
	}
}

// The bytes held are bounded too, and a value larger than the whole
// bound is returned but not cached.
func TestTheCacheBoundsTheBytesItHolds(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{
		"small1": strings.Repeat("x", 40), "small2": strings.Repeat("y", 40),
		"small3": strings.Repeat("w", 40), "huge": strings.Repeat("z", 200),
	})
	c := newSecretCache(time.Now)
	c.maxBytes = 100
	client := &secretsClient{
		Provider: &awsauth.Provider{Explicit: awsauth.Credentials{AccessKeyID: "A", SecretAccessKey: "B"}},
		Endpoint: f.server.URL,
		Client:   f.server.Client(),
	}
	for _, id := range []string{"small1", "small2", "huge"} {
		v, err := c.fetch(t.Context(), client, Secret{Key: id, SecretID: id}, "us-east-1", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if id == "huge" && v != strings.Repeat("z", 200) {
			t.Error("the value too large to cache was not returned whole")
		}
	}
	if c.len() != 2 || c.bytes != 80 {
		t.Errorf("it holds %d values, %d bytes; want the two small ones, 80", c.len(), c.bytes)
	}
	// A third small one pushes the oldest out.
	if _, err := c.fetch(t.Context(), client, Secret{Key: "s3", SecretID: "small3"}, "us-east-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	if c.len() != 2 || c.bytes != 80 {
		t.Errorf("it holds %d values, %d bytes after a third", c.len(), c.bytes)
	}
}

// An expired value is dropped when it is looked up, not kept until it
// is the oldest.
func TestAnExpiredValueIsDroppedWhenLookedUp(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"k": "v"})
	now := time.Now()
	c := newSecretCache(func() time.Time { return now })
	client := &secretsClient{
		Provider: &awsauth.Provider{Explicit: awsauth.Credentials{AccessKeyID: "A", SecretAccessKey: "B"}},
		Endpoint: f.server.URL,
		Client:   f.server.Client(),
	}
	if _, err := c.fetch(t.Context(), client, Secret{Key: "k", SecretID: "k"}, "us-east-1", time.Minute); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, ok := c.get(client.identity()+"|us-east-1|k|", time.Minute); ok {
		t.Error("an expired value was returned")
	}
	if c.len() != 0 || c.bytes != 0 {
		t.Errorf("the expired value is still held: %d values, %d bytes", c.len(), c.bytes)
	}
}
