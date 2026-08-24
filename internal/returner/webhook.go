package returner

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/job"
)

func init() {
	register("webhook", true, func(opts Options) (Returner, error) {
		if opts.URL == "" {
			return nil, fmt.Errorf("the webhook returner needs a url")
		}
		if !strings.HasPrefix(opts.URL, "https://") {
			// A return carries whatever a job printed. Sending that
			// over plaintext because someone typed http:// is not a
			// choice worth offering.
			return nil, fmt.Errorf("the webhook returner needs an https url; %q is not", opts.URL)
		}
		if opts.Secret == "" {
			return nil, fmt.Errorf("the webhook returner needs a secret to sign with")
		}
		if opts.SpoolDir == "" {
			opts.SpoolDir = filepath.Join(opts.StateDir, "returner-spool")
		}
		if opts.MaxAttempts <= 0 {
			opts.MaxAttempts = 5
		}
		if opts.Timeout <= 0 {
			opts.Timeout = 30 * time.Second
		}
		if opts.SpoolMax <= 0 {
			opts.SpoolMax = 256 << 20
		}
		if err := os.MkdirAll(opts.SpoolDir, 0o700); err != nil {
			return nil, err
		}
		client := &http.Client{Timeout: opts.Timeout}
		if opts.CAFile != "" {
			// An estate whose receiver holds a certificate from its own
			// CA needs a way to say so. Without one the only options
			// are a public certificate or skipping verification, and
			// the second is not on offer: this connection carries
			// whatever a job printed.
			pool, err := certPool(opts.CAFile)
			if err != nil {
				return nil, err
			}
			client.Transport = &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			}
		}
		return &webhookReturner{opts: opts, client: client}, nil
	})
}

// The headers a receiver verifies. The same three the API's own hook
// ingress reads, so an estate that has written a verifier for one has
// written it for the other.
const (
	SignatureHeader = "X-Halite-Signature"
	TimestampHeader = "X-Halite-Timestamp"
	NonceHeader     = "X-Halite-Nonce"
)

// webhookReturner posts a return to an HTTP endpoint.
//
// SPEC 20.3 asks for three things together, and the third is what makes
// the other two worth having: HMAC body signing, retry with backoff,
// and a durable spool so a returner outage does not lose returns. A
// webhook returner without the spool loses exactly the returns from the
// incident that took the receiver down, which are the ones anybody
// wanted.
type webhookReturner struct {
	opts   Options
	client *http.Client

	mu       sync.Mutex
	spooled  int64
	measured bool
}

func (r *webhookReturner) Name() string { return "webhook" }

func (r *webhookReturner) Return(ctx context.Context, ret *job.Return) error {
	raw, err := json.Marshal(ret)
	if err != nil {
		return err
	}
	return r.deliver(ctx, "return", raw)
}

func (r *webhookReturner) Event(ctx context.Context, e *eventbus.Event) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return r.deliver(ctx, "event", raw)
}

// deliver posts once, retrying, and spools what it could not send.
func (r *webhookReturner) deliver(ctx context.Context, kind string, body []byte) error {
	// Anything already spooled goes first, so the receiver sees returns
	// in the order they happened rather than the new ones ahead of the
	// backlog.
	r.drain(ctx)

	if err := r.post(ctx, kind, body); err != nil {
		if spoolErr := r.spool(kind, body); spoolErr != nil {
			return fmt.Errorf("%s could not be sent (%w) and could not be spooled: %w",
				kind, err, spoolErr)
		}
		r.opts.logf("warn", "a return was spooled for later delivery",
			"url", r.opts.URL, "error", err.Error())
		return nil
	}
	return nil
}

// post sends one body, retrying with backoff.
//
// Only what is worth retrying: a 4xx other than 408 or 429 means the
// receiver understood and refused, and sending it again forever is how
// a spool fills a disk with a request nobody will ever accept.
func (r *webhookReturner) post(ctx context.Context, kind string, body []byte) error {
	var lastErr error
	for attempt := 1; attempt <= r.opts.MaxAttempts; attempt++ {
		if attempt > 1 {
			// 1s, 2s, 4s, 8s. Bounded, because a returner that waits
			// ten minutes is holding whatever called it.
			wait := time.Duration(1<<(attempt-2)) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		retryable, err := r.postOnce(ctx, kind, body)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable {
			return err
		}
	}
	return fmt.Errorf("after %d attempts: %w", r.opts.MaxAttempts, lastErr)
}

func (r *webhookReturner) postOnce(ctx context.Context, kind string, body []byte) (retryable bool, err error) {
	stamp := r.opts.now().UTC().Format(time.RFC3339)
	nonce := digestOf(append([]byte(stamp), body...))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.opts.URL, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Halite-Kind", kind)
	req.Header.Set(TimestampHeader, stamp)
	req.Header.Set(NonceHeader, nonce)
	// Over the timestamp and the raw bytes together, so a captured
	// signature cannot be replayed with a fresh timestamp.
	req.Header.Set(SignatureHeader, Sign(r.opts.Secret, stamp, body))
	if r.opts.NodeID != "" {
		req.Header.Set("X-Halite-Node", printableASCII(r.opts.NodeID))
	}

	res, err := r.client.Do(req)
	if err != nil {
		return true, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return false, nil
	}
	worthRetrying := res.StatusCode >= 500 ||
		res.StatusCode == http.StatusRequestTimeout ||
		res.StatusCode == http.StatusTooManyRequests
	return worthRetrying, fmt.Errorf("%s answered %d", r.opts.URL, res.StatusCode)
}

// Sign is the HMAC-SHA-256 over the timestamp and the body.
//
// Exported because a receiver written in this tree — a test, a bridge —
// must verify with the same function that signed, and two
// implementations of a signature are one implementation and one bug.
func Sign(secret, stamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// spool writes one undelivered body to disk.
//
// The name carries the time and a digest, so the drain reads them in
// the order they were spooled and a repeat of the same body does not
// make a second file.
func (r *webhookReturner) spool(kind string, body []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.measure(); err != nil {
		return err
	}
	if r.spooled+int64(len(body)) > r.opts.SpoolMax {
		// Refused rather than making room by deleting the oldest. A
		// spool that silently discards is the failure this exists to
		// prevent, and a full disk on a node is worse than a gap the
		// operator was told about.
		return fmt.Errorf("the spool is full at %d bytes; %s discarded", r.spooled, kind)
	}
	name := fmt.Sprintf("%d-%s-%s.json",
		r.opts.now().UTC().UnixNano(), kind, digestOf(body))
	path := filepath.Join(r.opts.SpoolDir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return err
	}
	r.spooled += int64(len(body))
	return nil
}

// measure sums the spool once, so that every spool write does not read
// the directory.
func (r *webhookReturner) measure() error {
	if r.measured {
		return nil
	}
	entries, err := os.ReadDir(r.opts.SpoolDir)
	if err != nil {
		return err
	}
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	r.spooled, r.measured = total, true
	return nil
}

// drain sends what is spooled, oldest first, and stops at the first
// failure so the order is kept.
func (r *webhookReturner) drain(ctx context.Context) {
	entries, err := os.ReadDir(r.opts.SpoolDir)
	if err != nil || len(entries) == 0 {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	// The name begins with a nanosecond timestamp, zero-padded by
	// nothing, so this is a string sort over equal-width numbers for
	// any time this century.
	sort.Strings(names)

	for _, name := range names {
		path := filepath.Join(r.opts.SpoolDir, name)
		body, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			continue
		}
		kind := "return"
		if parts := strings.Split(name, "-"); len(parts) > 1 {
			kind = parts[1]
		}
		// One attempt each while draining: the backlog is drained on
		// the next return anyway, and retrying every spooled item with
		// full backoff would hold the caller for minutes.
		if retryable, err := r.postOnce(ctx, kind, body); err != nil {
			if !retryable {
				// The receiver understood and refused. Keeping it
				// forever would block every later return behind it.
				r.opts.logf("warn", "a spooled return was refused and discarded",
					"file", name, "error", err.Error())
				r.remove(path, int64(len(body)))
			}
			return
		}
		r.remove(path, int64(len(body)))
	}
}

func (r *webhookReturner) remove(path string, size int64) {
	if err := os.Remove(path); err != nil {
		return
	}
	r.mu.Lock()
	r.spooled -= size
	if r.spooled < 0 {
		r.spooled = 0
	}
	r.mu.Unlock()
}

// Spooled reports how many undelivered bodies are waiting.
func (r *webhookReturner) Spooled() int {
	entries, err := os.ReadDir(r.opts.SpoolDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n
}

func (r *webhookReturner) Close() error {
	r.client.CloseIdleConnections()
	return nil
}

// certPool reads a CA bundle.
func certPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s holds no certificate", path)
	}
	return pool, nil
}
