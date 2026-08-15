package returner

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// webhookRetries is how many times a failed POST is tried again before the
// record is given up on. Endpoints blip; two more chances with a pause in
// between rides out a restart without holding this returner's queue for
// long, and no one else's at all.
const webhookRetries = 2

// Webhook POSTs each record as JSON to an HTTP endpoint.
type Webhook struct {
	url    string
	client *http.Client
	// retryWait is the pause before the first retry; it doubles for each
	// one after. A field rather than a constant so tests need not sleep.
	retryWait time.Duration
}

// isLoopback reports whether a URL's host is this machine, where an
// unencrypted POST never reaches a network.
func isLoopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// refuseOffHostRedirect stops a redirect from re-sending the record to
// somewhere else. Following one would hand the run's changes to whatever
// host the endpoint named, which is the disclosure https was meant to
// prevent.
func refuseOffHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > 0 && req.URL.Host != via[0].URL.Host {
		return fmt.Errorf("refusing redirect to %s", req.URL.Host)
	}
	if len(via) >= webhookMaxRedirects {
		return fmt.Errorf("too many redirects")
	}
	return nil
}

// webhookMaxRedirects bounds same-host redirects, which are a path
// change an endpoint is entitled to make.
const webhookMaxRedirects = 5

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
	// A result carries the run's changes, which can hold anything a state
	// templated out of pillar. Off the loopback that has to be encrypted;
	// on it there is no wire to encrypt.
	if parsed.Scheme == "http" && !isLoopback(parsed.Hostname()) {
		return nil, fmt.Errorf("returner webhook: %q must be https (results can carry pillar-derived changes); "+
			"http is allowed for a loopback endpoint only", endpoint)
	}
	return &Webhook{
		url: endpoint,
		// Bounded, and short enough that one unresponsive endpoint cannot
		// hold up the queue behind it for long.
		client: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: refuseOffHostRedirect,
		},
		retryWait: time.Second,
	}, nil
}

func (w *Webhook) Name() string { return "webhook:" + redactURL(w.url) }

// Return posts the record, retrying a bounded number of times before
// reporting the failure — the manager logs it as this returner's drop.
func (w *Webhook) Return(rec Record) error {
	body, err := encode(rec)
	if err != nil {
		return err
	}
	wait := w.retryWait
	var lastErr error
	for attempt := 0; attempt <= webhookRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(wait)
			wait *= 2
		}
		if lastErr = w.post(body); lastErr == nil {
			return nil
		}
	}
	return fmt.Errorf("dropping record after %d attempts: %w", webhookRetries+1, lastErr)
}

func (w *Webhook) post(body []byte) error {
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
