package returner

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Webhook POSTs each record as JSON to an HTTP endpoint.
type Webhook struct {
	url    string
	client *http.Client
}

// NewWebhook validates the endpoint and builds a client for it.
func NewWebhook(endpoint string) (*Webhook, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("returner webhook: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("returner webhook: %q is not an http(s) URL", endpoint)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("returner webhook: %q has no host", endpoint)
	}
	return &Webhook{
		url: endpoint,
		// Bounded, and short enough that one unresponsive endpoint cannot
		// hold up the queue behind it for long.
		client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (w *Webhook) Name() string { return "webhook:" + redactURL(w.url) }

func (w *Webhook) Return(rec Record) error {
	body, err := encode(rec)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("post to %s: %w", redactURL(w.url), err)
	}
	defer resp.Body.Close()
	// Drain so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("post to %s: %s", redactURL(w.url), resp.Status)
	}
	return nil
}

func (w *Webhook) Close() error { return nil }

// redactURL strips userinfo and any query string before the URL reaches a
// log line: webhook endpoints routinely carry a token in one or the other.
func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		if _, after, found := strings.Cut(raw, "://"); found {
			return "(redacted)://" + strings.SplitN(after, "?", 2)[0]
		}
		return "(redacted)"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
