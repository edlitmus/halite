package nodeevidence

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// KindAnchorReceipt is the hub's signed acknowledgement of a head this
// node reported, filed in the node's own chain.
//
// Dotted like every other kind, rather than hyphenated, because the
// kinds are tokens an investigator greps for and a family of them that
// is spelled two ways is a family somebody's grep misses half of.
const KindAnchorReceipt = "anchor.receipt"

// The detail keys of a KindAnchorReceipt record.
//
// `anchored_` rather than plain `seq` and `hash`, because the record
// that holds a receipt has a sequence number and a hash of its own, and
// they are never the ones the receipt is about: a receipt is always
// filed after the head it acknowledges. A reader who took `seq` to be
// the record's own number would check the receipt against the wrong
// record and find a break that is not there.
const (
	ReceiptAnchoredSeq  = "anchored_seq"
	ReceiptAnchoredHash = "anchored_hash"
	ReceiptNode         = "node"
	ReceiptReceived     = "received"
	ReceiptSignature    = "signature"
)

// receiptDomain begins every payload a receipt signs.
//
// The enrollment CA's key is what signs a receipt, and that key's only
// other job is signing certificates and revocation lists, both of which
// are DER and begin 0x30. A payload that begins with an ASCII `h` can
// never be one of those, whatever a node puts in the fields that follow,
// so a receipt the hub was persuaded to sign is a receipt and never a
// certificate. The version is in it so that a later encoding is a
// different domain rather than a reinterpretation of this one.
const receiptDomain = "halite evidence anchor receipt v1\n"

// hashShape is what Log writes into a record's Hash, and so the only
// thing a head can be.
//
// Checked rather than trusted because the hash is the one field of a
// payload the node chooses freely, and the encoding below is unambiguous
// only while no field holds a newline. A node that could send
// "sha256:…\nreceived 1999-…" would otherwise be asking the hub to sign
// a payload that reads as two different receipts.
var hashShape = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// CheckHead reports whether (seq, hash) is something Log could have
// produced as a head: a sequence number from 1, and a SHA-256 in the
// form hashOf writes.
func CheckHead(seq uint64, hash string) error {
	if seq == 0 {
		return errors.New("an evidence head is numbered from 1; 0 is a chain with no records")
	}
	if !hashShape.MatchString(hash) {
		return fmt.Errorf("%q is not an evidence record hash; one is sha256: and 64 lowercase hex digits", hash)
	}
	return nil
}

// ReceiptPayload is the exact byte string a receipt's signature covers.
//
// The encoding is one field per line, each a fixed lowercase name, one
// space, and the value, in this order, every line ending in a newline:
//
//	halite evidence anchor receipt v1
//	node <node id>
//	seq <decimal, no leading zeros>
//	hash <sha256:64 hex digits>
//	received <RFC 3339 timestamp, UTC, nanoseconds as Go's RFC3339Nano prints them>
//
// It is unambiguous because no value can contain a newline: the node id
// is held to pki.ValidateNodeID's alphabet, the sequence number is
// printed by strconv, the hash to hashShape, and the timestamp must
// parse as RFC 3339 and print back identically. That last check matters
// more than it looks. Two spellings of one instant are two payloads, so
// a verifier that normalised the time before checking would accept a
// receipt whose stored text says something its signature does not.
//
// Lines of text rather than JSON on purpose. A JSON encoding is canonical
// only by convention -- key order, escaping, number formatting -- and
// the two sides of this are a hub and a node that may be different
// builds years apart. Four fixed lines are canonical by construction and
// can be checked by hand with printf and openssl.
func ReceiptPayload(nodeID string, seq uint64, hash, received string) ([]byte, error) {
	if err := pki.ValidateNodeID(nodeID); err != nil {
		return nil, fmt.Errorf("an anchor receipt: %w", err)
	}
	if err := CheckHead(seq, hash); err != nil {
		return nil, fmt.Errorf("an anchor receipt: %w", err)
	}
	when, err := time.Parse(time.RFC3339Nano, received)
	if err != nil {
		return nil, fmt.Errorf("an anchor receipt's time %q is not RFC 3339: %w", received, err)
	}
	if when.UTC().Format(time.RFC3339Nano) != received {
		return nil, fmt.Errorf("an anchor receipt's time %q is not in the one spelling a receipt uses (UTC, RFC3339Nano)", received)
	}
	var b strings.Builder
	b.WriteString(receiptDomain)
	b.WriteString("node " + nodeID + "\n")
	b.WriteString("seq " + strconv.FormatUint(seq, 10) + "\n")
	b.WriteString("hash " + hash + "\n")
	b.WriteString("received " + received + "\n")
	return []byte(b.String()), nil
}

// ReceivedNow is the spelling of a receipt's timestamp.
func ReceivedNow(now time.Time) string {
	return now.UTC().Format(time.RFC3339Nano)
}

// SignReceipt signs a receipt with the hub's enrollment CA key and
// answers with base64 of an ASN.1 DER ECDSA signature over the SHA-256
// of ReceiptPayload.
//
// ECDSA only. Every CA this build creates is ECDSA, and a crypto.Signer
// holding an RSA key would sign PKCS #1 v1.5 here, which SPEC 25.3 does
// not list for anything; refusing is better than producing a receipt
// with a primitive the inventory does not admit to.
func SignReceipt(signer crypto.Signer, nodeID string, seq uint64, hash, received string) (string, error) {
	if signer == nil {
		return "", errors.New("signing an anchor receipt needs the enrollment CA's key, and this hub does not hold it")
	}
	if _, ok := signer.Public().(*ecdsa.PublicKey); !ok {
		return "", fmt.Errorf("the enrollment CA's key is %T and an anchor receipt is ECDSA", signer.Public())
	}
	payload, err := ReceiptPayload(nodeID, seq, hash, received)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	der, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return "", fmt.Errorf("signing an anchor receipt: %w", err)
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

// VerifyReceipt checks a receipt's signature against the enrollment CA
// certificate, which every node already pins.
//
// The CA certificate rather than a separate receipt key, because the
// node already holds it and has already decided to trust it: a second key
// would need its own distribution and its own pinning, and a node that
// learned it from the hub would be trusting the party the receipt exists
// to hold to account.
func VerifyReceipt(ca *x509.Certificate, nodeID string, seq uint64, hash, received, signature string) error {
	if ca == nil {
		return errors.New("checking an anchor receipt needs the enrollment CA certificate")
	}
	pub, ok := ca.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("the CA certificate's key is %T and an anchor receipt is ECDSA", ca.PublicKey)
	}
	payload, err := ReceiptPayload(nodeID, seq, hash, received)
	if err != nil {
		return err
	}
	der, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("the receipt's signature is not base64: %w", err)
	}
	digest := sha256.Sum256(payload)
	if !ecdsa.VerifyASN1(pub, digest[:], der) {
		return errors.New("the receipt's signature does not verify against the CA certificate")
	}
	return nil
}
