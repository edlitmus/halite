package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

// secretCache holds fetched values for their TTL.
//
// Keyed by region, secret and stage rather than by node: two nodes
// needing the same secret is the common case, and keying by node would
// fetch it twice. It bounds how stale a rotated secret can be on this
// hub, which is the trade the TTL is.
type secretCache struct {
	now func() time.Time

	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	value any
	at    time.Time
}

func newSecretCache(now func() time.Time) *secretCache {
	return &secretCache{now: now, entries: map[string]cacheEntry{}}
}

// fetch reads one secret, through the cache.
func (c *secretCache) fetch(ctx context.Context, client *secretsClient,
	s Secret, region string, ttl time.Duration) (any, error) {

	key := region + "|" + s.SecretID + "|" + s.Stage
	if ttl > 0 {
		c.mu.Lock()
		entry, ok := c.entries[key]
		c.mu.Unlock()
		if ok && c.now().Sub(entry.at) < ttl {
			return entry.value, nil
		}
	}

	// The partition an ARN names wins over the configured one, so a
	// GovCloud ARN is signed against a GovCloud endpoint in a hub that
	// also reads commercial secrets.
	use := client
	if p := partitionFromARN(s.SecretID); p != "" && p != client.Partition && client.Endpoint == "" {
		clone := *client
		clone.Partition = p
		use = &clone
	}

	text, err := use.GetSecretValue(ctx, s.SecretID, region, s.Stage)
	if err != nil {
		return nil, err
	}
	parsed := parseSecret(text)

	if ttl > 0 {
		c.mu.Lock()
		c.entries[key] = cacheEntry{value: parsed, at: c.now()}
		c.mu.Unlock()
	}
	return parsed, nil
}

// parseSecret turns a secret string into a pillar value.
//
// A JSON object becomes a mapping, so
// `pillar.get('aws_secrets:db:password')` reaches into it; anything
// else stays the string it is. This is the Salt module's behaviour, and
// it is what every tree that stores a credential pair as JSON depends
// on.
func parseSecret(text string) any {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return text
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return text
	}
	return decoded
}

// readSecretFile reads a key out of a file, returning nothing when
// there is no file to read.
//
// A missing or unreadable file is not reported here: the credential
// chain tries the environment and the instance role next, and a
// configuration naming a file that is not there fails with the
// service's own "no credentials" rather than with a read error that
// says less.
func readSecretFile(path string) string {
	if path == "" {
		return ""
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}
