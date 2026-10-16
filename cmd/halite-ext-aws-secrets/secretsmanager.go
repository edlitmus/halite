// The Secrets Manager client.
//
// One operation is needed -- GetSecretValue -- which is a JSON 1.1 POST
// with a target header. That is not a reason to link an SDK, and an
// extension that linked one would be shipping a dependency tree into
// the sandbox this model exists to keep small.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/awsauth"
)

// maxSecretBody bounds one Secrets Manager response.
//
// A secret is 64 KiB at the service's own limit. A megabyte leaves room
// for the envelope and stops a wrong endpoint from being read without
// bound.
const maxSecretBody = 1 << 20

// secretsClient calls Secrets Manager with SigV4.
//
// One operation is needed — GetSecretValue — which is a JSON 1.1 POST
// with a target header. That is not a reason to link an SDK.
type secretsClient struct {
	Provider  *awsauth.Provider
	Client    *http.Client
	Partition string
	// Endpoint overrides the host, for a compatible service and for the
	// tests.
	Endpoint string
	Timeout  time.Duration
	Now      func() time.Time
}

func (c *secretsClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *secretsClient) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

// endpoint is the URL a GetSecretValue for a region is posted to.
//
// Built from the partition rather than hardcoded to `amazonaws.com`: an
// endpoint built for the commercial partition is wrong in GovCloud and
// in China, and this estate runs in GovCloud.
//
// And built as a URL, from a region that has been checked, rather than
// by concatenation. It was `"https://secretsmanager." + region + "." +
// suffix`, and the region could be a node's own `region` grain, so a
// node reporting `x@evil.example/` had the hub post a SigV4-signed
// request -- access key ID in the Authorization header, session token
// in X-Amz-Security-Token -- to evil.example, and put whatever came
// back into that node's pillar (DIVERGENCE 5.324). Whoever supplied the
// region, this is where it becomes part of a host, so this is where it
// is checked: awsauth.ServiceURL refuses anything that is not a region
// name and then confirms the URL parses back to exactly the host meant.
//
// The region is checked when an endpoint is configured too. It is not
// in the host then, but it is in the signature's credential scope, and
// a value that is not a region has no business there either.
func (c *secretsClient) endpoint(region string) (*url.URL, error) {
	if c.Endpoint != "" {
		if err := awsauth.ValidRegion(region); err != nil {
			return nil, err
		}
		u, err := url.Parse(strings.TrimSuffix(c.Endpoint, "/") + "/")
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("the configured endpoint %s is not an http(s) URL",
				awsauth.Bounded(c.Endpoint))
		}
		return u, nil
	}
	suffix, err := awsauth.PartitionSuffix(c.Partition)
	if err != nil {
		return nil, err
	}
	return awsauth.ServiceURL("secretsmanager", region, suffix)
}

// secretValue is the part of the GetSecretValue response that is used.
type secretValue struct {
	Name         string `json:"Name"`
	SecretString string `json:"SecretString"`
	SecretBinary string `json:"SecretBinary"`
}

// apiError is how Secrets Manager reports a refusal.
type apiError struct {
	Type     string `json:"__type"`
	Message  string `json:"message"`
	Message2 string `json:"Message"`
}

// GetSecretValue reads one secret.
//
// secretID is a name or an ARN; the service accepts both, which is why
// the configuration does not need to say which it was given.
func (c *secretsClient) GetSecretValue(ctx context.Context, secretID, region, stage string) (string, error) {
	// The endpoint first, before credentials are resolved: a region
	// that cannot be one is refused before anything is fetched, signed
	// or sent, including from the metadata service or STS.
	u, err := c.endpoint(region)
	if err != nil {
		return "", err
	}
	endpoint := strings.TrimSuffix(u.String(), "/")

	creds, err := c.Provider.Retrieve(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving AWS credentials: %w", err)
	}

	payload := map[string]string{"SecretId": secretID}
	if stage != "" {
		payload["VersionStage"] = stage
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "secretsmanager.GetSecretValue")
	req.ContentLength = int64(len(body))

	sum := sha256.Sum256(body)
	signer := awsauth.Signer{Region: region, Service: "secretsmanager"}
	if err := signer.Sign(req, creds, hex.EncodeToString(sum[:]), c.now()); err != nil {
		return "", err
	}

	res, err := c.client().Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	resBody, err := io.ReadAll(io.LimitReader(res.Body, maxSecretBody))
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusOK {
		// The service's own message names the secret and the reason —
		// not found, denied, wrong region — and an operator debugging
		// this needs it. It carries no secret material.
		var apiErr apiError
		_ = json.Unmarshal(resBody, &apiErr)
		msg := apiErr.Message
		if msg == "" {
			msg = apiErr.Message2
		}
		if apiErr.Type != "" || msg != "" {
			return "", fmt.Errorf("%s answered %d: %s %s",
				endpoint, res.StatusCode, apiErr.Type, msg)
		}
		return "", fmt.Errorf("%s answered %d", endpoint, res.StatusCode)
	}

	var parsed secretValue
	if err := json.Unmarshal(resBody, &parsed); err != nil {
		return "", fmt.Errorf("the response from %s is not readable: %w", endpoint, err)
	}
	if parsed.SecretString == "" && parsed.SecretBinary != "" {
		// Deliberately refused rather than base64-decoded into a
		// pillar value: a binary secret in a YAML pillar is a
		// mojibake bug waiting for the first non-UTF-8 byte.
		return "", fmt.Errorf("the secret %q holds binary, which this source does not deliver", secretID)
	}
	return parsed.SecretString, nil
}

// regionFromARN reads the region out of a secret ARN.
//
// `arn:aws-us-gov:secretsmanager:us-gov-east-1:123:secret:name-AbCdEf`
// — field three. A name that is not an ARN gives nothing, and the
// caller falls back.
func regionFromARN(secretID string) string {
	if !strings.HasPrefix(secretID, "arn:") {
		return ""
	}
	parts := strings.Split(secretID, ":")
	if len(parts) < 6 {
		return ""
	}
	return parts[3]
}

// partitionFromARN reads the partition out of a secret ARN, so that a
// GovCloud ARN is signed against a GovCloud endpoint even when the
// configuration did not say so.
func partitionFromARN(secretID string) string {
	if !strings.HasPrefix(secretID, "arn:") {
		return ""
	}
	parts := strings.Split(secretID, ":")
	if len(parts) < 6 {
		return ""
	}
	return parts[1]
}
