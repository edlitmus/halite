// Package apitoken is the token handling of SPEC section 23.6: what the
// API hands an operator in exchange for credentials.
//
// Named for what it is rather than `token`, because the enrollment
// bootstrap tokens of SPEC 7.3 already live in the key store and the
// two are not the same thing. One admits a machine to the estate once;
// this one carries an operator's authority for a working day.
package apitoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/edlitmus/halite/internal/atomicfile"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Token is one issued token, as it is stored.
//
// The secret is not here and never was: what is stored is its digest,
// so that a stolen token file is not a set of working credentials. The
// same reasoning as a password file, for the same reason.
type Token struct {
	// ID names the token for revocation and for the audit, and is not
	// secret.
	ID string `json:"id"`
	// Digest is SHA-256 of the secret, hex.
	Digest string `json:"digest"`
	// Principal is who the token speaks for.
	Principal string `json:"principal"`
	// Roles are frozen at issue. A role added to the principal
	// afterwards does not widen a token already in someone's hands,
	// and a role taken away is a reason to revoke rather than a change
	// that quietly applies. SPEC 23.6.
	Roles []string `json:"roles,omitempty"`

	Issued time.Time `json:"issued"`
	// Expires is the absolute expiry: the token stops at this moment
	// whatever it has been doing.
	Expires time.Time `json:"expires"`
	// IdleFor is how long the token may go unused before it stops.
	// Zero means only the absolute expiry applies.
	IdleFor time.Duration `json:"idle_for,omitempty"`
	// LastUsed moves as the token is used, for the idle expiry.
	LastUsed time.Time `json:"last_used,omitempty"`

	// SourceCIDR binds the token to a network. Empty means anywhere.
	SourceCIDR string `json:"source_cidr,omitempty"`

	// Revoked records a withdrawal, kept rather than deleted so that an
	// audit can say what was withdrawn and when.
	Revoked   bool      `json:"revoked,omitempty"`
	RevokedAt time.Time `json:"revoked_at,omitempty"`
}

// ErrNotFound is returned for a token this store has never issued.
var ErrNotFound = errors.New("no such token")

// Live reports whether a token may be used at a moment.
func (t *Token) Live(now time.Time) bool { return t.Why(now) == "" }

// Why says why a token may not be used, or empty when it may.
//
// A sentence rather than a boolean, because "your token does not work"
// is the least useful thing an API can say to someone at three in the
// morning.
func (t *Token) Why(now time.Time) string {
	switch {
	case t.Revoked:
		return "this token was revoked"
	case !t.Expires.IsZero() && !now.Before(t.Expires):
		return "this token expired at " + t.Expires.UTC().Format(time.RFC3339)
	case t.IdleFor > 0 && !t.LastUsed.IsZero() && now.Sub(t.LastUsed) > t.IdleFor:
		return fmt.Sprintf("this token went unused for longer than %s", t.IdleFor)
	}
	return ""
}

// AllowsSource reports whether a token may be used from an address.
func (t *Token) AllowsSource(remote string) bool {
	if t.SourceCIDR == "" {
		return true
	}
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	_, network, err := net.ParseCIDR(t.SourceCIDR)
	if err != nil {
		return false
	}
	return network.Contains(ip)
}

// Options are what to issue.
type Options struct {
	Principal string
	Roles     []string
	// Lifetime is the absolute expiry. Zero takes DefaultLifetime.
	Lifetime time.Duration
	// IdleFor is the idle expiry. Zero takes DefaultIdle.
	IdleFor time.Duration
	// SourceCIDR binds the token to a network.
	SourceCIDR string
}

// The defaults of SPEC 23.6: a working day, and an afternoon of not
// using it.
const (
	DefaultLifetime = 12 * time.Hour
	DefaultIdle     = 4 * time.Hour
)

// Store keeps issued tokens on disk.
type Store struct {
	dir string
	Now func() time.Time
}

// Open prepares the store.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("the token store needs a directory")
	}
	// 0700: the digests are not credentials, but the set of live
	// principals is worth keeping to the service account.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the token store: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Dir is where the tokens live.
func (s *Store) Dir() string { return s.dir }

func (s *Store) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) path(id string) (string, error) {
	if s == nil || s.dir == "" {
		return "", fmt.Errorf("this service keeps no tokens")
	}
	if !isHex(id) || len(id) != 32 {
		return "", fmt.Errorf("that is not a token identifier")
	}
	return filepath.Join(s.dir, id+".json"), nil
}

// Issue mints a token and returns the record and the secret.
//
// The secret is returned once, here, and never again: what is stored is
// its digest, so a store that is read cannot be replayed.
func (s *Store) Issue(opts Options) (*Token, string, error) {
	if opts.Principal == "" {
		return nil, "", fmt.Errorf("a token needs a principal")
	}
	if opts.SourceCIDR != "" {
		if _, _, err := net.ParseCIDR(opts.SourceCIDR); err != nil {
			return nil, "", fmt.Errorf("%q is not a network: %w", opts.SourceCIDR, err)
		}
	}

	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, "", err
	}
	// 256 bits, as SPEC 23.6 requires.
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(secretBytes)

	lifetime := opts.Lifetime
	if lifetime <= 0 {
		lifetime = DefaultLifetime
	}
	idle := opts.IdleFor
	if idle < 0 {
		idle = 0
	} else if idle == 0 {
		idle = DefaultIdle
	}

	now := s.now()
	t := &Token{
		ID:         hex.EncodeToString(idBytes),
		Digest:     Digest(secret),
		Principal:  opts.Principal,
		Roles:      append([]string(nil), opts.Roles...),
		Issued:     now,
		Expires:    now.Add(lifetime),
		IdleFor:    idle,
		LastUsed:   now,
		SourceCIDR: opts.SourceCIDR,
	}
	if err := s.put(t); err != nil {
		return nil, "", err
	}
	return t, secret, nil
}

// Digest is how a secret is stored and looked up.
func Digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func (s *Store) put(t *Token) error {
	path, err := s.path(t.ID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return writeAtomic(path, raw, 0o600)
}

// Get reads one token by its identifier.
func (s *Store) Get(id string) (*Token, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var t Token
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("the record for %s is unreadable: %w", id, err)
	}
	return &t, nil
}

// Redeem finds the token a secret belongs to, checks it, and moves its
// idle clock.
//
// The lookup is by digest across the store rather than by an index, and
// the secret never becomes a filename: a filename reaches a log, a
// backup, and a directory listing.
func (s *Store) Redeem(secret, remote string) (*Token, error) {
	if secret == "" {
		return nil, fmt.Errorf("no token was presented")
	}
	want := Digest(secret)
	tokens, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, t := range tokens {
		if t.Digest != want {
			continue
		}
		now := s.now()
		if why := t.Why(now); why != "" {
			return nil, errors.New(why)
		}
		if !t.AllowsSource(remote) {
			return nil, fmt.Errorf("this token is bound to %s", t.SourceCIDR)
		}
		t.LastUsed = now
		if err := s.put(t); err != nil {
			return nil, err
		}
		return t, nil
	}
	return nil, fmt.Errorf("that token is not one this service issued")
}

// Revoke withdraws a token, keeping the record for the audit.
func (s *Store) Revoke(id string) (*Token, error) {
	t, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if t.Revoked {
		return t, nil
	}
	t.Revoked = true
	t.RevokedAt = s.now()
	if err := s.put(t); err != nil {
		return nil, err
	}
	return t, nil
}

// RevokePrincipal withdraws every token a principal holds, which is
// what an account being disabled means for the tokens already issued.
func (s *Store) RevokePrincipal(principal string) (int, error) {
	tokens, err := s.List()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range tokens {
		if t.Principal != principal || t.Revoked {
			continue
		}
		if _, err := s.Revoke(t.ID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// List reads every stored token, newest first.
func (s *Store) List() ([]*Token, error) {
	if s == nil || s.dir == "" {
		return nil, fmt.Errorf("this service keeps no tokens")
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []*Token
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		t, err := s.Get(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			// One unreadable record must not hide the rest.
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Issued.After(out[j].Issued) })
	return out, nil
}

// Prune deletes tokens that expired long enough ago to be of no
// interest to an audit, and reports how many went.
func (s *Store) Prune(keepAfterExpiry time.Duration) (int, error) {
	tokens, err := s.List()
	if err != nil {
		return 0, err
	}
	now := s.now()
	n := 0
	for _, t := range tokens {
		if t.Expires.IsZero() || now.Sub(t.Expires) < keepAfterExpiry {
			continue
		}
		path, err := s.path(t.ID)
		if err != nil {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return n, err
		}
		n++
	}
	return n, nil
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

// writeAtomic writes through a temporary file in the same directory.
//
// The idiom is one package rather than six copies of it, because all six
// were wrong on Windows in the same way: see internal/atomicfile.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	return atomicfile.Write(path, data, mode)
}
