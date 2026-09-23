// Package jobsign is SPEC 25.6's detached job signing: an operator or a
// CI signer holds a key the hub does not have, and a node can refuse to
// run anything that key did not authorise.
//
// # What it is for
//
// It is the second half of the compromised-hub row of SPEC 25.1's threat
// model, the first being `internal/nodeevidence`. Evidence makes a hub's
// lying detectable afterwards; this makes it ineffective at the time. A
// hub that has been taken over can dispatch anything it likes, and a node
// with `require_job_signature` will run none of it, because the hub has
// nothing to sign with.
//
// # Why ECDSA here and Ed25519 for extensions
//
// `internal/extension` signs bundles with Ed25519 and gives good reasons:
// one key size, no parameters to get wrong. This is ECDSA anyway, for
// three reasons that are specific to signing jobs. SPEC 25.6 says ECDSA.
// It also says "signing keys may be held in a hardware token", and a PIV
// card or a cloud KMS will sign with P-256 long before it will sign with
// Ed25519. And the estate's own PKI is already ECDSA P-256 and P-384, so
// this is the curve an operator's key material is already on.
//
// The signature is ASN.1 DER, which is what `openssl dgst -verify` reads
// — a test drives that, because a signature format checked only against
// the code that produced it is a format nobody else can produce.
package jobsign

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Payload is what a signature covers: SPEC 25.6's list, plus the target's
// kind.
//
// The kind is in it because a target without one is ambiguous — `web*` is
// a glob and also a perfectly good single-element list, and they select
// different machines. Signing the text and not the kind would let
// whoever relays the job change which of the two it meant.
type Payload struct {
	JID        string
	Target     string
	TargetKind string
	Fun        string
	Arg        []string
	Kwarg      map[string]any
	Env        string
	// Expires bounds the signature as well as the job: a signature with
	// no expiry is a permanent authorisation to do one thing, and SPEC
	// 6.3 already requires a node to refuse an expired job.
	Expires time.Time
}

// domain separates this signature from everything else this project
// signs, so that a signature over an extension bundle, a token or a state
// tree can never be replayed as a signature over a job. The same
// reasoning as internal/extension's, and the same shape.
const domain = "halite-job-v1\x00"

// Canonical is the byte string a signature is computed over.
//
// # The rule
//
// Every field is written as `name` `:` `length` `:` `value` `;`, with the
// length in bytes of the value as written. Lengths are what stop two
// adjacent fields being re-cut into one — without them, a target of `a`
// and a function of `bc` encode the same as a target of `ab` and a
// function of `c`, and one signature would authorise both.
//
// Arguments are counted before they are written, so that dropping the
// last one changes the encoding. Keyword arguments are sorted by name,
// because a map has no order and the two ends of this must agree.
//
// # Numbers
//
// A number is written by value and not by the text it arrived as, so that
// `1`, `1.0` and a `json.Number` holding either all encode identically.
// An integer goes through int64, which is what keeps SPEC 6.4's promise
// that a 64-bit integer survives — encoding it through float64 would lose
// the last digits of a large one and produce a signature that does not
// verify against the job that was sent.
//
// # Why it is written down here
//
// SPEC 25.6 allows the signer to be a bridged extension backed by a KMS,
// which means something other than this package will one day produce
// these bytes. The rule above is the interface, and docs/operations.md
// states it for that reader.
func Canonical(p Payload) []byte {
	var b strings.Builder
	b.WriteString(domain)
	field(&b, "jid", p.JID)
	field(&b, "target", p.Target)
	field(&b, "target_kind", p.TargetKind)
	field(&b, "fun", p.Fun)
	fmt.Fprintf(&b, "argc:%d;", len(p.Arg))
	for _, a := range p.Arg {
		field(&b, "arg", a)
	}
	keys := make([]string, 0, len(p.Kwarg))
	for k := range p.Kwarg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(&b, "kwargc:%d;", len(keys))
	for _, k := range keys {
		field(&b, "kwarg", k)
		field(&b, "value", scalar(p.Kwarg[k]))
	}
	field(&b, "env", p.Env)
	expires := ""
	if !p.Expires.IsZero() {
		// UTC and nanoseconds, which is how the job record and the wire
		// both spell an expiry. A signature over a local-time rendering
		// would verify in one timezone and not in another.
		expires = p.Expires.UTC().Format(time.RFC3339Nano)
	}
	field(&b, "expires", expires)
	return []byte(b.String())
}

func field(b *strings.Builder, name, value string) {
	fmt.Fprintf(b, "%s:%d:%s;", name, len(value), value)
}

// scalar renders one keyword argument's value.
//
// Structured values go through `encoding/json`, which sorts the keys of a
// map, so a nested mapping is as stable as a flat one. Numbers inside it
// are normalised first, for the reason Canonical gives.
func scalar(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case json.Number, int, int64, uint64, float64, float32:
		return number(t)
	}
	normalised := normalise(v)
	encoded, err := json.Marshal(normalised)
	if err != nil {
		// A value that will not encode cannot be signed over usefully,
		// and rendering it with %v would make two different values
		// encode alike. The Go type is in the text so that a refusal
		// downstream says something.
		return fmt.Sprintf("unencodable:%T", v)
	}
	return string(encoded)
}

// number renders a numeric value by value rather than by spelling.
func number(v any) string {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return strconv.FormatInt(i, 10)
		}
		if f, err := t.Float64(); err == nil {
			return formatFloat(f)
		}
		return t.String()
	case int:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	case uint64:
		return strconv.FormatUint(t, 10)
	case float32:
		return formatFloat(float64(t))
	case float64:
		return formatFloat(t)
	}
	return fmt.Sprint(v)
}

// formatFloat writes a float that happens to be integral as an integer,
// so that float64(1) and json.Number("1") agree.
func formatFloat(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// normalise replaces every number inside a structure with its canonical
// form, recursively, so that json.Marshal writes the same bytes whichever
// decoder produced the values.
func normalise(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, item := range t {
			out[k] = normalise(item)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = normalise(item)
		}
		return out
	case json.Number:
		return json.RawMessage(number(t))
	case float64, float32, int, int64, uint64:
		return json.RawMessage(number(t))
	}
	return v
}

// SignerKey is a public key a node will accept a signed job from.
type SignerKey struct {
	// Name is how an operator refers to it, and what the node records
	// when a signature verifies.
	Name string
	Key  *ecdsa.PublicKey
}

// ParseSignerKey reads `<name> <base64 public key>`, the form
// `job_signer_keys` takes.
//
// The same shape as `extension_trust_keys`, deliberately: an operator
// configuring both should not have to remember two spellings. The key
// itself is a DER SubjectPublicKeyInfo, which is what every other tool
// will hand them — `openssl ec -pubout` writes it, and so does a KMS.
func ParseSignerKey(line string) (SignerKey, error) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) != 2 {
		return SignerKey{}, fmt.Errorf("a job signer key is `<name> <base64 key>`, not %q", line)
	}
	raw, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return SignerKey{}, fmt.Errorf("the key for %s is not base64: %w", fields[0], err)
	}
	parsed, err := x509.ParsePKIXPublicKey(raw)
	if err != nil {
		return SignerKey{}, fmt.Errorf("the key for %s is not a public key in DER SubjectPublicKeyInfo form: %w",
			fields[0], err)
	}
	pub, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		return SignerKey{}, fmt.Errorf("the key for %s is %T and job signing is ECDSA", fields[0], parsed)
	}
	if err := checkCurve(pub.Curve); err != nil {
		return SignerKey{}, fmt.Errorf("the key for %s: %w", fields[0], err)
	}
	return SignerKey{Name: fields[0], Key: pub}, nil
}

// checkCurve refuses a curve this project does not issue on.
//
// P-224 is in the standard library and is below the strength everything
// else here is held to; P-521 is fine cryptographically and is not one of
// the two the PKI offers, so accepting it would mean a key an operator
// cannot generate with `halite-hub keys signer create`. Both are refused
// by name rather than silently, because "the signature does not verify"
// is the wrong message for "this key is not one we accept".
func checkCurve(curve elliptic.Curve) error {
	switch curve {
	case elliptic.P256(), elliptic.P384():
		return nil
	}
	name := "an unnamed curve"
	if curve != nil && curve.Params() != nil {
		name = curve.Params().Name
	}
	return fmt.Errorf("the curve is %s and job signing accepts P-256 and P-384", name)
}

// FormatSignerKey renders a public key as the configuration line for it.
func FormatSignerKey(name string, pub *ecdsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("encoding the public key: %w", err)
	}
	return name + " " + base64.StdEncoding.EncodeToString(der), nil
}

// GenerateKey makes a signing key. `curve` is "ecdsa-p256" or
// "ecdsa-p384", the same spellings `--key-algorithm` takes.
func GenerateKey(algorithm string) (*ecdsa.PrivateKey, error) {
	switch algorithm {
	case "", "ecdsa-p256":
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "ecdsa-p384":
		return ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	}
	return nil, fmt.Errorf("%q is not a signing algorithm; use ecdsa-p256 or ecdsa-p384", algorithm)
}

// EncodePrivateKey writes a PKCS#8 PEM block, which is what the rest of
// this project's key material is in.
func EncodePrivateKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encoding the signing key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// DecodePrivateKey reads one back.
func DecodePrivateKey(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("the signing key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// An operator with an `openssl ecparam` key has SEC 1 rather
		// than PKCS#8, and "not PKCS#8" is not a useful thing to be told
		// when the other form is one line away from working.
		if sec1, sec1Err := x509.ParseECPrivateKey(block.Bytes); sec1Err == nil {
			parsed = sec1
		} else {
			return nil, fmt.Errorf("the signing key is not a PKCS#8 or SEC 1 private key: %w", err)
		}
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("the signing key is %T and job signing is ECDSA", parsed)
	}
	if err := checkCurve(key.Curve); err != nil {
		return nil, err
	}
	return key, nil
}

// Sign produces the detached signature for a job, base64 of ASN.1 DER.
func Sign(key *ecdsa.PrivateKey, p Payload) (string, error) {
	if key == nil {
		return "", fmt.Errorf("signing a job needs a key")
	}
	if err := checkCurve(key.Curve); err != nil {
		return "", err
	}
	digest := sha256.Sum256(Canonical(p))
	der, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		return "", fmt.Errorf("signing the job: %w", err)
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

// Verify checks a signature against every trusted key and answers with
// the name of the one that verified.
//
// Every key is tried rather than the job naming which to use, which is
// `internal/extension`'s reasoning and holds here too: a signature that
// says which key checks it is a signature that can ask to be checked
// against a key the attacker chose. The cost is a few microseconds per
// key, once per job.
func Verify(keys []SignerKey, p Payload, signature string) (string, error) {
	if len(keys) == 0 {
		return "", fmt.Errorf("this node trusts no job signer keys; set job_signer_keys")
	}
	if strings.TrimSpace(signature) == "" {
		return "", fmt.Errorf("the job carries no signature")
	}
	der, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return "", fmt.Errorf("the job's signature is not base64: %w", err)
	}
	digest := sha256.Sum256(Canonical(p))
	for _, key := range keys {
		if ecdsa.VerifyASN1(key.Key, digest[:], der) {
			return key.Name, nil
		}
	}
	return "", fmt.Errorf("the signature does not verify against any of this node's %d trusted signer key(s)",
		len(keys))
}

// WireValues puts keyword arguments through the JSON round trip they will
// take on the way to a node.
//
// The signer holds whatever its own command line parser produced -- an
// ordered map from this project's value model, an int64, a
// `json.Number` -- and the node holds whatever `encoding/json` decoded
// from the wire. Those are different Go values for the same JSON, and a
// canonical encoding computed over one does not match the other: an
// ordered map writes its keys in the order they were typed and a
// `map[string]any` writes them sorted, so a signature over a structured
// argument would never verify.
//
// So the signer signs what it is about to send, decoded the way the node
// will decode it. UseNumber, because SPEC 6.4 promises a 64-bit integer
// survives and the default decoder would round it here -- in the one
// place where rounding it changes a signature rather than a value.
func WireValues(kwargs map[string]any) (map[string]any, error) {
	if len(kwargs) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(kwargs)
	if err != nil {
		return nil, fmt.Errorf("encoding the job's arguments for signing: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.UseNumber()
	out := map[string]any{}
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("reading the job's arguments back for signing: %w", err)
	}
	return out, nil
}
