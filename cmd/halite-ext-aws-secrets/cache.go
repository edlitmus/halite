package main

import (
	"container/list"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

// secretCache holds fetched values for their TTL.
//
// Keyed by who fetched it -- the block's credentials, endpoint and
// partition, see secretsClient.identity -- and by region, secret and
// stage, but not by node: two nodes needing the same secret through the
// same block is the common case, and keying by node would fetch it
// twice. It bounds how stale a rotated secret can be on this hub, which
// is the trade the TTL is.
//
// The identity was missing (issue 622). The host keeps one pool of
// these processes per extension, not per block, so every
// `aws_secrets_manager` block on a hub shares this cache, and a block
// holding low-privilege credentials, or pointed at another endpoint, was
// served what a different block had fetched -- with nothing reaching
// Secrets Manager to refuse it. A cached value is reused only by a
// block that would have sent the same signed request.
//
// And bounded (issue 622): at most maxCacheEntries values and
// maxCacheBytes of secret text, the least recently used going first. It
// never evicted, so it held every value any node had had fetched for as
// long as the process lived. A node with `node_grain` names its own
// secret IDs, and what the allow list admits can be many; each one it
// named stayed. A value that has outlived its TTL is dropped when it is
// next looked up; one never looked up again waits for the bounds like
// any other.
type secretCache struct {
	now func() time.Time
	// maxEntries and maxBytes are the bounds. newSecretCache sets them
	// to the package's; the tests set them smaller.
	maxEntries int
	maxBytes   int

	mu      sync.Mutex
	entries map[string]*list.Element
	// order is most recently used first. Each element holds a
	// *cacheEntry.
	order *list.List
	bytes int
}

// maxCacheEntries is how many values the cache holds.
//
// Far more than a hub's own configuration names -- a block lists tens --
// so a hub that is not being pushed by node-named secrets never evicts,
// and the TTL is the only thing that refreshes a value.
const maxCacheEntries = 1024

// maxCacheBytes bounds the secret text held. Secrets Manager's own limit
// is 64 KiB a secret, so this is a thousand of those; a compatible
// `endpoint` may return up to maxSecretBody, and a value larger than this
// whole bound is not cached at all.
const maxCacheBytes = 64 << 20

type cacheEntry struct {
	key   string
	value any
	at    time.Time
	size  int
}

func newSecretCache(now func() time.Time) *secretCache {
	return &secretCache{
		now:        now,
		maxEntries: maxCacheEntries,
		maxBytes:   maxCacheBytes,
		entries:    map[string]*list.Element{},
		order:      list.New(),
	}
}

// fetch reads one secret, through the cache.
func (c *secretCache) fetch(ctx context.Context, client *secretsClient,
	s Secret, region string, ttl time.Duration) (any, error) {

	key := client.identity() + "|" + region + "|" + s.SecretID + "|" + s.Stage
	if ttl > 0 {
		if value, ok := c.get(key, ttl); ok {
			return value, nil
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
		c.put(key, parsed, len(text))
	}
	return parsed, nil
}

// get returns a value younger than ttl, and marks it used. One older is
// removed, so an expired value does not hold its place until it is the
// oldest.
func (c *secretCache) get(key string, ttl time.Duration) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	entry := el.Value.(*cacheEntry)
	if c.now().Sub(entry.at) >= ttl {
		c.remove(el)
		return nil, false
	}
	c.order.MoveToFront(el)
	return entry.value, true
}

// put stores a value, then evicts the least recently used until both
// bounds hold.
func (c *secretCache) put(key string, value any, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Two compilations can fetch the same secret at once; the later
	// value replaces the earlier rather than both being counted.
	if el, ok := c.entries[key]; ok {
		c.remove(el)
	}
	if size > c.maxBytes {
		return
	}
	c.entries[key] = c.order.PushFront(&cacheEntry{key: key, value: value, at: c.now(), size: size})
	c.bytes += size
	for c.order.Len() > c.maxEntries || c.bytes > c.maxBytes {
		c.remove(c.order.Back())
	}
}

func (c *secretCache) remove(el *list.Element) {
	entry := c.order.Remove(el).(*cacheEntry)
	delete(c.entries, entry.key)
	c.bytes -= entry.size
}

// len is how many values are held, for the tests.
func (c *secretCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
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
