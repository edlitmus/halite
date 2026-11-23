// Package treesign is issue 764's signed state content: a manifest naming
// every file of one environment's tree with its digest, signed by a key
// the hub does not hold, so that a node can refuse a tree a compromised
// hub altered.
//
// # What it binds, and what it does not
//
// The manifest names each file's path, digest, size and mode, the
// environment, and when it was issued. A node that requires it answers
// every read and every listing of that environment from the manifest, so
// the hub can neither change a file, add one, hide one, nor serve one
// environment's signed tree as another's. Pillar is outside it: pillar is
// compiled by the hub per node, and a node never sees its sources, so a
// signed state that interpolates a pillar value is still steerable by the
// hub through that value. SPEC 25.1 and the hardening chapter say so.
//
// # The format
//
// `_halite/manifest.json` is JSON of schema `halite.tree/1`, strictly
// parsed: an unknown field or schema is refused, because a field this
// build does not understand could be one that narrows what the manifest
// authorises. `_halite/manifest.sig` holds one or more lines, each the
// base64 of an ASN.1 DER ECDSA signature over
//
//	SHA-256( "halite-tree-v1\x00" || <the exact bytes of manifest.json> )
//
// The signature is over the bytes, not a canonical re-encoding: the node
// verifies the bytes it received and only then parses them, so there is
// no canonicaliser to get wrong at either end (the job signature's v1
// history, DIVERGENCE 5.330, is the cost of needing one).
//
// The domain is not the job signature's, so neither signature can be
// replayed as the other, and the keys are separate too
// (`state_signer_keys`, not `job_signer_keys`): a CI signer for trees and
// an operator key for jobs are different authorities.
//
// # One signer, and room for more
//
// This version accepts a manifest when any line of the signature file
// verifies against any trusted key. A signer writes one line. More than
// one line is accepted now, deliberately, so that a later version can
// require M of N distinct keys by counting the lines that verify, without
// changing either file's format and without an older node refusing a
// manifest that carries two signatures.
package treesign

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/jobsign"
)

// Schema is the manifest's format, and the only one this build reads.
const Schema = "halite.tree/1"

// domain separates a tree signature from a job signature
// (`halite-job-v2\x00`) and from anything else this project signs.
const domain = "halite-tree-v1\x00"

// The reserved directory and the two files that may live in it.
//
// `_halite/` mirrors `_ext/`, where signed extension bundles are
// published. The hub refuses to serve an environment whose `_halite/`
// holds anything else (see fileserver.Roots.ReservedProblem), so the
// directory cannot become a place a tree hides content the manifest does
// not describe.
const (
	ReservedDir   = "_halite"
	ManifestPath  = ReservedDir + "/manifest.json"
	SignaturePath = ReservedDir + "/manifest.sig"
)

// Bounds on what a node will read before it has verified anything.
//
// The manifest's is arithmetic, not a measurement: 50,000 entries
// (MaxEntries) of a few hundred bytes each. The signature file's allows
// MaxSignatures lines of a P-384 signature, about 140 bytes of base64.
const (
	MaxManifestBytes  = 16 << 20
	MaxSignatureBytes = 16 << 10
	MaxSignatures     = 16
	// MaxEntries is fileserver.MaxManifestEntries, the bound on the
	// hub's own listing. A tree larger than that cannot be signed in this
	// version, and there is no `truncated` in this schema.
	MaxEntries = 50000
)

// Manifest is one signed revision of one environment.
type Manifest struct {
	Schema string `json:"schema"`
	Env    string `json:"env"`
	// Issued orders revisions of one environment: RFC 3339 in UTC with
	// nanoseconds. A timestamp rather than a counter, so a signer keeps
	// no state between runs; the CI clock is the ordering root, which
	// the maintainer accepted (issue 764, proposal 10.2).
	Issued string `json:"issued"`
	// Expires is optional. Past it, a node refuses the manifest.
	Expires string `json:"expires,omitempty"`
	// Source is what the signer says it built from. Signed and shown to
	// an operator; nothing decides on it.
	Source   string  `json:"source,omitempty"`
	HashType string  `json:"hash_type"`
	Files    []Entry `json:"files"`

	issued  time.Time
	expires time.Time
}

// Entry is one file.
type Entry struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
	Size int64  `json:"size"`
	// Mode is the file's permission bits as four octal digits, as the
	// signer's listing saw them. Carried in the first schema so that a
	// later state taking a mode from its source needs no new schema
	// version (the maintainer's answer to proposal 10.10). Nothing on a
	// node reads it in this version: the hub sends bytes and no mode, so
	// there is nothing to compare it with.
	Mode string `json:"mode"`
}

// IssuedAt and ExpiresAt are the parsed times. Zero for an absent expiry.
func (m *Manifest) IssuedAt() time.Time  { return m.issued }
func (m *Manifest) ExpiresAt() time.Time { return m.expires }

// Lookup finds a path. The files are sorted, which Parse enforces.
func (m *Manifest) Lookup(path string) (Entry, bool) {
	i := sort.Search(len(m.Files), func(i int) bool { return m.Files[i].Path >= path })
	if i < len(m.Files) && m.Files[i].Path == path {
		return m.Files[i], true
	}
	return Entry{}, false
}

// hashLengths is the digest set `hash_type` allows, with each one's hex
// length. MD5 and SHA-1 are refused by name.
var hashLengths = map[string]int{"sha256": 64, "sha384": 96, "sha512": 128, "sha3-256": 64}

// ID is a manifest's identity: the SHA-256 of its exact bytes, which no
// signer can choose and which does not depend on git's object ids.
func ID(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Digest is what a signer signs: 32 bytes, so the bridged signer of
// internal/extsigner and halite-ext-signer-kms sign a manifest unchanged.
func Digest(raw []byte) [32]byte {
	h := sha256.New()
	h.Write([]byte(domain))
	h.Write(raw)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Encode validates a manifest and writes the bytes a signer signs.
func Encode(m *Manifest) ([]byte, error) {
	if m.Schema == "" {
		m.Schema = Schema
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	if m.Files == nil {
		m.Files = []Entry{}
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	// Read back with the node's own parser, so a signer cannot write a
	// manifest every node will refuse.
	if _, err := Parse(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// SignatureLine signs a manifest's bytes with a local key.
func SignatureLine(key *ecdsa.PrivateKey, raw []byte) (string, error) {
	if key == nil {
		return "", errors.New("signing a manifest needs a key")
	}
	digest := Digest(raw)
	der, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		return "", fmt.Errorf("signing the manifest: %w", err)
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

// Verify checks the signature file against every trusted key and answers
// with the name of the key that verified.
//
// Every key is tried against every line, so the signature cannot name its
// own checker -- jobsign.Verify's rule, for its reason.
func Verify(keys []jobsign.SignerKey, raw, sig []byte) (string, error) {
	if len(keys) == 0 {
		return "", errors.New("this node trusts no state signer keys; set state_signer_keys")
	}
	if len(sig) > MaxSignatureBytes {
		return "", fmt.Errorf("%s is %d bytes, over the %d this node reads", SignaturePath, len(sig), MaxSignatureBytes)
	}
	var lines [][]byte
	for _, line := range strings.Split(string(sig), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		der, err := base64.StdEncoding.DecodeString(line)
		if err != nil {
			return "", fmt.Errorf("%s holds a line that is not base64: %w", SignaturePath, err)
		}
		lines = append(lines, der)
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("%s holds no signature", SignaturePath)
	}
	if len(lines) > MaxSignatures {
		return "", fmt.Errorf("%s holds %d signatures, over the %d this node reads", SignaturePath, len(lines), MaxSignatures)
	}
	digest := Digest(raw)
	for _, der := range lines {
		for _, key := range keys {
			if ecdsa.VerifyASN1(key.Key, digest[:], der) {
				return key.Name, nil
			}
		}
	}
	return "", fmt.Errorf("the manifest's signature does not verify against any of this node's %d trusted state signer key(s)", len(keys))
}

// Parse reads a manifest strictly.
//
// Nothing is normalised: a manifest that breaks a rule is refused, not
// repaired, because a repair is a decision about what the signer meant
// and the signer is not here to ask.
func Parse(raw []byte) (*Manifest, error) {
	if len(raw) > MaxManifestBytes {
		return nil, fmt.Errorf("the manifest is %d bytes, over the %d this node reads", len(raw), MaxManifestBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("the manifest is not a %s document: %w", Schema, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("the manifest has content after its document")
	}
	if m.Schema != Schema {
		return nil, fmt.Errorf("the manifest's schema is %q and this node reads %q", m.Schema, Schema)
	}
	if err := CheckEnv(m.Env); err != nil {
		return nil, err
	}
	issued, err := parseTime(m.Issued)
	if err != nil {
		return nil, fmt.Errorf("the manifest's issued time: %w", err)
	}
	m.issued = issued
	if m.Expires != "" {
		expires, err := parseTime(m.Expires)
		if err != nil {
			return nil, fmt.Errorf("the manifest's expiry: %w", err)
		}
		if !expires.After(issued) {
			return nil, errors.New("the manifest expires before it was issued")
		}
		m.expires = expires
	}
	length, ok := hashLengths[m.HashType]
	if !ok {
		return nil, fmt.Errorf("the manifest's hash_type is %q; it is one of sha256, sha384, sha512 or sha3-256", m.HashType)
	}
	if m.Files == nil {
		return nil, errors.New("the manifest has no files list")
	}
	if len(m.Files) > MaxEntries {
		return nil, fmt.Errorf("the manifest names %d files, over the %d this version can sign", len(m.Files), MaxEntries)
	}
	for i, e := range m.Files {
		if err := CheckPath(e.Path); err != nil {
			return nil, err
		}
		if i > 0 && m.Files[i-1].Path >= e.Path {
			return nil, fmt.Errorf("the manifest's files are not sorted and unique at %q", e.Path)
		}
		if len(e.Hash) != length || strings.ToLower(e.Hash) != e.Hash {
			return nil, fmt.Errorf("%s: %q is not a lower-case %s digest", e.Path, e.Hash, m.HashType)
		}
		if _, err := hex.DecodeString(e.Hash); err != nil {
			return nil, fmt.Errorf("%s: %q is not a %s digest", e.Path, e.Hash, m.HashType)
		}
		if e.Size < 0 {
			return nil, fmt.Errorf("%s: a size of %d", e.Path, e.Size)
		}
		if err := checkMode(e.Mode); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Path, err)
		}
	}
	return &m, nil
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, err
	}
	if !strings.HasSuffix(s, "Z") {
		return time.Time{}, fmt.Errorf("%q is not in UTC", s)
	}
	return t, nil
}

// FormatTime spells a time the way a manifest does.
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func checkMode(mode string) error {
	if len(mode) != 4 {
		return fmt.Errorf("the mode %q is not four octal digits", mode)
	}
	if _, err := strconv.ParseUint(mode, 8, 16); err != nil {
		return fmt.Errorf("the mode %q is not four octal digits", mode)
	}
	return nil
}

// FormatMode spells permission bits the way a manifest does.
func FormatMode(perm uint32) string { return fmt.Sprintf("%04o", perm&0o7777) }

// CheckEnv refuses an environment name that cannot be a file name, which
// is also what the node's file cache refuses.
func CheckEnv(env string) error {
	if env == "" || strings.ContainsAny(env, "/\\\x00") || env == "." || strings.Contains(env, "..") {
		return fmt.Errorf("%q is not an environment a manifest can name", env)
	}
	return nil
}

// CheckPath is the path rule: relative, slash-separated, no empty, `.` or
// `..` segment, no backslash, no NUL, and not under `_halite/`.
func CheckPath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return fmt.Errorf("the manifest names %q, which is not a relative path", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("the manifest names %q, which is not a clean relative path", p)
		}
	}
	if p == ReservedDir || strings.HasPrefix(p, ReservedDir+"/") {
		return fmt.Errorf("the manifest names %q, under the reserved %s/", p, ReservedDir)
	}
	return nil
}

// IsReserved reports whether a tree path is under `_halite/`.
func IsReserved(p string) bool {
	p = strings.TrimPrefix(p, "/")
	return p == ReservedDir || strings.HasPrefix(p, ReservedDir+"/")
}
