// Package extpillar holds the compiled-in external pillar sources of
// SPEC section 12.7.
//
// Salt loads an external pillar from a Python file on the file server,
// which is the dynamic module loader this project exists to remove. A
// source here is compiled in and satisfies pillar.ExtSource; the
// `ext_pillar` list selects and configures one.
package extpillar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// host is the endpoint for a region.
//
// Built from the partition rather than hardcoded to `amazonaws.com`: an
// endpoint built for the commercial partition is wrong in GovCloud and
// in China, and this estate runs in GovCloud.
func (c *secretsClient) host(region string) string {
	if c.Endpoint != "" {
		return strings.TrimSuffix(c.Endpoint, "/")
	}
	suffix := "amazonaws.com"
	if c.Partition == "aws-cn" {
		suffix = "amazonaws.com.cn"
	}
	return "https://secretsmanager." + region + "." + suffix
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

	endpoint := c.host(region)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/", bytes.NewReader(body))
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
