package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks JSON to the control plane over mTLS. Agents and operators
// differ only in which certificate they present.
type Client struct {
	Host string // host:port
	HTTP *http.Client
}

// NewClient builds a client for host. If host has no port, DefaultPort is
// assumed.
func NewJSONClient(host string, tlsCfg *tls.Config, timeout time.Duration) *Client {
	if !strings.Contains(host, ":") {
		host = fmt.Sprintf("%s:%d", host, DefaultPort)
	}
	return &Client{Host: host, HTTP: NewClient(tlsCfg, timeout)}
}

// URL builds an absolute URL for a control plane path.
func (c *Client) URL(path string) string {
	return (&url.URL{Scheme: "https", Host: c.Host, Path: path}).String()
}

// Get performs a GET and decodes the JSON response into out, which may be
// nil to discard the body.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL(path), nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

// Post sends body as JSON and decodes the response into out, which may be
// nil to discard the body.
func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL(path), bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

// Stream performs a GET and hands back the raw body for non-JSON payloads.
// The caller closes it.
func (c *Client) Stream(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL(path), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp.Body, nil
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", req.URL.Path, err)
	}
	return nil
}

// checkStatus turns a non-2xx reply into the message the control plane
// sent, rather than a bare status code.
func checkStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	var errResp ErrorResponse
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		return fmt.Errorf("%s: %s", resp.Status, errResp.Error)
	}
	return fmt.Errorf("%s", resp.Status)
}
