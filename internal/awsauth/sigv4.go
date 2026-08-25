// Package awsauth signs AWS requests with Signature Version 4.
//
// SPEC 13.4 implements this directly rather than importing the AWS SDK,
// which would add hundreds of packages to satisfy one signing
// algorithm. What is actually needed is a canonical request, a string
// to sign, a signing key derived over four HMAC-SHA-256 rounds, and an
// Authorization header.
//
// The algorithm is specified precisely and its failure mode is a
// signature that does not verify, so the tests here are the AWS
// documentation's own published vectors: an implementation that agrees
// with itself proves nothing.
package awsauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Algorithm is the only one this signs with.
const Algorithm = "AWS4-HMAC-SHA256"

// UnsignedPayload is what `x-amz-content-sha256` carries when a body is
// streamed rather than hashed up front.
//
// Not used for the requests this project makes — every one of them has
// a body small enough to hash, and a signed payload hash is what stops
// a proxy from altering a request in flight.
const UnsignedPayload = "UNSIGNED-PAYLOAD"

// EmptyPayloadHash is SHA-256 of nothing, which every GET carries.
const EmptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// Credentials are what a request is signed with.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	// SessionToken is set for temporary credentials — an instance
	// role, an assumed role, a web identity. It is signed as a header
	// rather than added afterwards, because a token that is not signed
	// can be stripped.
	SessionToken string
	// Expires is when they stop working, for a caller that refreshes.
	// Zero means they do not expire.
	Expires time.Time
}

// Valid reports whether these credentials are usable now.
func (c Credentials) Valid(now time.Time) bool {
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return false
	}
	if c.Expires.IsZero() {
		return true
	}
	// A minute of margin: credentials that expire during the request
	// they were fetched for are the failure a refresh exists to avoid.
	return now.Add(time.Minute).Before(c.Expires)
}

// Signer signs requests for one service in one region.
type Signer struct {
	Region  string
	Service string
}

// Sign adds the Authorization header, and the headers it covers.
//
// The request is modified in place, which is what every caller wants
// and what makes it impossible to sign a request and then send a
// different one.
func (s Signer) Sign(req *http.Request, creds Credentials, payloadHash string, now time.Time) error {
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return fmt.Errorf("signing needs an access key and a secret")
	}
	if payloadHash == "" {
		payloadHash = EmptyPayloadHash
	}
	stamp := now.UTC()
	amzDate := stamp.Format("20060102T150405Z")
	dateStamp := stamp.Format("20060102")

	if req.Host == "" && req.URL != nil {
		req.Host = req.URL.Host
	}
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if creds.SessionToken != "" {
		// Set before the canonical headers are built, so it is signed.
		// A session token added after signing can be removed in flight
		// and the signature still verifies.
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	}

	canonical, signedHeaders := canonicalRequest(req, payloadHash)
	scope := strings.Join([]string{dateStamp, s.Region, s.Service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		Algorithm,
		amzDate,
		scope,
		hexSHA256([]byte(canonical)),
	}, "\n")

	signature := hex.EncodeToString(hmacSHA256(signingKey(creds.SecretAccessKey, dateStamp, s.Region, s.Service),
		[]byte(stringToSign)))

	req.Header.Set("Authorization", fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		Algorithm, creds.AccessKeyID, scope, signedHeaders, signature))
	return nil
}

// signingKey is the four HMAC rounds SPEC 13.4 names.
//
// Each round narrows the key: to the date, then the region, then the
// service, then the request type. A key derived for one day and one
// region cannot sign for another, which is what makes a leaked
// signature bounded rather than a credential.
func signingKey(secret, dateStamp, region, service string) []byte {
	key := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	key = hmacSHA256(key, []byte(region))
	key = hmacSHA256(key, []byte(service))
	return hmacSHA256(key, []byte("aws4_request"))
}

// canonicalRequest builds the string the signature covers, and the list
// of headers it names.
func canonicalRequest(req *http.Request, payloadHash string) (string, string) {
	names, canonicalHeaders := canonicalHeaders(req)
	return strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		canonicalQuery(req.URL),
		canonicalHeaders,
		names,
		payloadHash,
	}, "\n"), names
}

// canonicalURI is the path, each segment encoded, with `/` for an empty
// path.
//
// `EscapedPath` is used rather than re-encoding `Path`, because a key
// containing a literal `%2F` and a key containing `/` are different
// objects in S3 and re-encoding would make them the same request.
func canonicalURI(u *url.URL) string {
	path := u.EscapedPath()
	if path == "" {
		return "/"
	}
	return path
}

// canonicalQuery is the query sorted by name, then by value, with both
// encoded.
func canonicalQuery(u *url.URL) string {
	values := u.Query()
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		list := append([]string(nil), values[k]...)
		sort.Strings(list)
		for _, v := range list {
			parts = append(parts, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

// canonicalHeaders is the signed header list and the header block.
//
// Every header on the request is signed, not a chosen subset: a header
// left out is one a proxy can add without breaking the signature.
func canonicalHeaders(req *http.Request) (string, string) {
	type header struct{ name, value string }
	var headers []header

	// `Host` is not in the header map — Go carries it on the request —
	// and SigV4 requires it.
	headers = append(headers, header{"host", req.Host})
	for name, values := range req.Header {
		lower := strings.ToLower(name)
		if lower == "authorization" || lower == "host" {
			continue
		}
		joined := make([]string, 0, len(values))
		for _, v := range values {
			joined = append(joined, trimAll(v))
		}
		headers = append(headers, header{lower, strings.Join(joined, ",")})
	}
	sort.Slice(headers, func(i, j int) bool { return headers[i].name < headers[j].name })

	var names []string
	var block strings.Builder
	for _, h := range headers {
		names = append(names, h.name)
		block.WriteString(h.name + ":" + h.value + "\n")
	}
	return strings.Join(names, ";"), block.String()
}

// trimAll collapses runs of spaces, which SigV4's header
// canonicalisation requires.
func trimAll(v string) string {
	return strings.Join(strings.Fields(v), " ")
}

// uriEncode is RFC 3986 percent-encoding, with `/` left alone in a path.
//
// Go's `url.QueryEscape` encodes a space as `+`, which SigV4 does not
// accept, so this is written out.
func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte('/')
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// HexSHA256 is the payload hash a caller puts in
// `x-amz-content-sha256`.
func HexSHA256(body []byte) string { return hexSHA256(body) }

func hexSHA256(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
