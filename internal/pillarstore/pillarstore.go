// Package pillarstore keeps a node's last known good pillar on its disk,
// encrypted, so that the node can keep converging while its hub cannot be
// reached. SPEC 12.8.
//
// The key is derived from the node's private key with HKDF-SHA-256 and the
// sealing is AES-256-GCM. Both are in the standard library and both are
// approved in FIPS mode, so this adds nothing to SPEC section 4's list.
//
// What this protects against is a copy of the file: a backup, a disk image,
// a read through a directory somebody opened up. It does not protect against
// an attacker who can read node.key, who can in any case ask the hub for this
// pillar and be given it.
package pillarstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/fileperm"
)

const (
	magic = "HLPC1"
	info  = "halite pillar cache v1"
)

// ErrNone is a store with nothing for this environment.
var ErrNone = errors.New("this node holds no last known good pillar")

// Record is one pillar exactly as the hub sent it.
//
// Secrets is kept as well as the pillar because the redactor is seeded from
// it. A cached run that lost the list would either print decrypted values or
// mask everything, depending on which way the absent case fell.
type Record struct {
	Pillar  json.RawMessage `json:"pillar"`
	Secrets *[]string       `json:"secrets,omitempty"`
	SavedAt time.Time       `json:"saved_at"`
}

// Store is the node's copy.
type Store struct {
	// Dir holds one file per pillar environment.
	Dir string
	// NodeID is bound into each file, so that a file moved from another
	// node's disk does not open here even under the same key.
	NodeID string
	// KeyDER returns the node's private key. A function because a renewal
	// can replace the key under a running process. A file sealed under the
	// old key then simply fails to open, and is ErrNone until the next
	// successful fetch.
	KeyDER func() ([]byte, error)
}

func (s *Store) path(env string, all bool) string {
	sum := sha256.Sum256([]byte(env + "\x00" + strconv.FormatBool(all)))
	return filepath.Join(s.Dir, hex.EncodeToString(sum[:])+".pillar")
}

func (s *Store) aead() (cipher.AEAD, error) {
	der, err := s.KeyDER()
	if err != nil {
		return nil, fmt.Errorf("reading this node's key: %w", err)
	}
	key, err := hkdf.Key(sha256.New, der, nil, info, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// additional binds a file to the node and the environment it was saved for.
// The file name alone would not: renaming a file for one environment over
// another's would otherwise hand a run the wrong environment's secrets.
func (s *Store) additional(env string, all bool) []byte {
	return []byte(magic + "\x00" + s.NodeID + "\x00" + env + "\x00" + strconv.FormatBool(all))
}

// Save replaces the record for an environment.
func (s *Store) Save(env string, all bool, rec Record) error {
	plain, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	aead, err := s.aead()
	if err != nil {
		return err
	}
	// A random nonce under a key that lives for a certificate's life: a GCM
	// nonce of 96 bits is safe for far more saves than a node makes.
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	out := append([]byte(magic), nonce...)
	out = aead.Seal(out, nonce, plain, s.additional(env, all))
	if err := fileperm.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return atomicfile.Write(s.path(env, all), out, 0o600)
}

// Load opens the record for an environment. A file that cannot be opened is
// an error rather than an empty pillar: a state rendered against nothing
// would report a convergence that never happened.
func (s *Store) Load(env string, all bool) (*Record, error) {
	raw, err := os.ReadFile(s.path(env, all))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNone
	}
	if err != nil {
		return nil, err
	}
	aead, err := s.aead()
	if err != nil {
		return nil, err
	}
	head := len(magic) + aead.NonceSize()
	if len(raw) < head || string(raw[:len(magic)]) != magic {
		return nil, fmt.Errorf("%s is not a pillar this build wrote", s.path(env, all))
	}
	plain, err := aead.Open(nil, raw[len(magic):head], raw[head:], s.additional(env, all))
	if err != nil {
		return nil, fmt.Errorf("%s does not open under this node's key: %w", s.path(env, all), err)
	}
	var rec Record
	if err := json.Unmarshal(plain, &rec); err != nil {
		return nil, fmt.Errorf("%s: %w", s.path(env, all), err)
	}
	return &rec, nil
}

// Clear removes every record: the hub has told this node it is not entitled
// to its pillar any more.
func (s *Store) Clear() error {
	err := os.RemoveAll(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
