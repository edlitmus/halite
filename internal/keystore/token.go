package keystore

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/target"
)

// MaxTokenTTL is SPEC 7.3's ceiling. A token cannot be created without
// a TTL and cannot be created with one longer than this.
const MaxTokenTTL = 24 * time.Hour

// tokenBytes is 256 bits, per SPEC 7.3.
const tokenBytes = 32

// Token is a bootstrap token's record on the hub.
//
// The secret is not here and is not anywhere: only its digest is
// stored, so a hub's disk does not enrol anything.
type Token struct {
	ID       string    `json:"id"`
	Digest   string    `json:"digest"`
	NodeGlob string    `json:"node_glob,omitempty"`
	CIDR     string    `json:"cidr,omitempty"`
	MaxUses  int       `json:"max_uses"`
	Uses     int       `json:"uses"`
	Expires  time.Time `json:"expires"`
	Created  time.Time `json:"created"`
	Revoked  bool      `json:"revoked,omitempty"`
	// SpentBy names what the token admitted, so that a leaked token can
	// be answered with a list rather than with a guess.
	SpentBy []string `json:"spent_by,omitempty"`
	Comment string   `json:"comment,omitempty"`
}

// Live reports whether the token can still admit a node.
func (t *Token) Live(now time.Time) bool {
	return !t.Revoked && t.Uses < t.MaxUses && now.Before(t.Expires)
}

// Why says, for an operator, what is wrong with a token that is not
// live. It returns the empty string for a live one.
func (t *Token) Why(now time.Time) string {
	switch {
	case t.Revoked:
		return "revoked"
	case !now.Before(t.Expires):
		return fmt.Sprintf("expired at %s", t.Expires.UTC().Format(time.RFC3339))
	case t.Uses >= t.MaxUses:
		return fmt.Sprintf("spent (%d of %d uses)", t.Uses, t.MaxUses)
	}
	return ""
}

// TokenOptions is what an operator asks for when minting one.
type TokenOptions struct {
	// TTL is mandatory and is capped at MaxTokenTTL.
	TTL      time.Duration
	NodeGlob string
	CIDR     string
	// MaxUses defaults to 1: single-use, per SPEC 7.3.
	MaxUses int
	Comment string
}

// ErrNoToken is returned when a secret matches nothing on the hub.
var ErrNoToken = errors.New("the bootstrap token is not one this hub issued")

func (s *Store) tokenDir() string { return filepath.Join(s.dir, "tokens") }

func (s *Store) tokenPath(id string) (string, error) {
	if len(id) != tokenIDLen || !isHex(id) {
		return "", fmt.Errorf("%q is not a token identifier", id)
	}
	return filepath.Join(s.tokenDir(), id+".json"), nil
}

// tokenIDLen is enough of the digest to name a token without ambiguity
// and not enough to be worth attacking: it is public, and the secret is
// 256 bits regardless.
const tokenIDLen = 12

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

// digestOf is the stored form of a token secret.
func digestOf(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// MintToken creates a token and returns the record and the secret.
//
// The secret is returned once, here, and never again: it is not stored,
// so a hub cannot show it a second time and an operator who loses it
// mints another.
func (s *Store) MintToken(opts TokenOptions, now time.Time) (*Token, string, error) {
	if opts.TTL <= 0 {
		return nil, "", fmt.Errorf("a bootstrap token needs a lifetime; the maximum is %s", MaxTokenTTL)
	}
	if opts.TTL > MaxTokenTTL {
		return nil, "", fmt.Errorf("a bootstrap token may live at most %s, not %s", MaxTokenTTL, opts.TTL)
	}
	if opts.MaxUses < 0 {
		return nil, "", fmt.Errorf("a bootstrap token cannot have %d uses", opts.MaxUses)
	}
	if opts.MaxUses == 0 {
		opts.MaxUses = 1
	}
	if opts.CIDR != "" {
		if _, err := netip.ParsePrefix(opts.CIDR); err != nil {
			return nil, "", fmt.Errorf("the source restriction %q is not a CIDR: %w", opts.CIDR, err)
		}
	}
	if opts.NodeGlob != "" && !target.MatchGlob(opts.NodeGlob, opts.NodeGlob) && opts.NodeGlob != "*" {
		// A pattern that does not match itself is malformed --
		// path.Match reports an error and globMatch turns that into
		// "no", which would make the token admit nothing at all.
		return nil, "", fmt.Errorf("the node scope %q is not a usable pattern", opts.NodeGlob)
	}

	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("generating a bootstrap token: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	digest := digestOf(secret)

	tok := &Token{
		ID:       digest[:tokenIDLen],
		Digest:   digest,
		NodeGlob: opts.NodeGlob,
		CIDR:     opts.CIDR,
		MaxUses:  opts.MaxUses,
		Expires:  now.Add(opts.TTL),
		Created:  now,
		Comment:  opts.Comment,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.tokenDir(), 0o700); err != nil {
		return nil, "", fmt.Errorf("creating the token store: %w", err)
	}
	if err := s.putToken(tok); err != nil {
		return nil, "", err
	}
	return tok, secret, nil
}

func (s *Store) putToken(tok *Token) error {
	path, err := s.tokenPath(tok.ID)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding token %s: %w", tok.ID, err)
	}
	return writeAtomic(path, append(raw, '\n'), 0o600)
}

// GetToken returns one token record by its identifier.
func (s *Store) GetToken(id string) (*Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getToken(id)
}

func (s *Store) getToken(id string) (*Token, error) {
	path, err := s.tokenPath(id)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("token %s: %w", id, ErrNoToken)
	}
	if err != nil {
		return nil, fmt.Errorf("reading token %s: %w", id, err)
	}
	var tok Token
	if err := json.Unmarshal(raw, &tok); err != nil {
		return nil, fmt.Errorf("the token record at %s is not readable: %w", path, err)
	}
	return &tok, nil
}

// ListTokens returns every token record, newest first.
func (s *Store) ListTokens() ([]*Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listTokens()
}

func (s *Store) listTokens() ([]*Token, error) {
	entries, err := os.ReadDir(s.tokenDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the token store at %s: %w", s.tokenDir(), err)
	}
	var out []*Token
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		tok, err := s.getToken(e.Name()[:len(e.Name())-len(".json")])
		if err != nil {
			return nil, err
		}
		out = append(out, tok)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// RevokeToken withdraws a token immediately.
func (s *Store) RevokeToken(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, err := s.getToken(id)
	if err != nil {
		return err
	}
	tok.Revoked = true
	return s.putToken(tok)
}

// DeleteToken removes an expired or spent record.
func (s *Store) DeleteToken(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.tokenPath(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("token %s: %w", id, ErrNoToken)
		}
		return fmt.Errorf("deleting token %s: %w", id, err)
	}
	return nil
}

// SpendToken checks a presented secret against the scope it was minted
// with and records the use.
//
// Every failure returns the same shape of error to the caller that will
// put it on the wire; the detail is for the hub's own log, because a
// node that guesses should not learn whether it guessed a real token
// with the wrong scope or no token at all.
func (s *Store) SpendToken(secret, nodeID, remoteAddr string, now time.Time) (*Token, error) {
	if err := pki.ValidateNodeID(nodeID); err != nil {
		return nil, err
	}
	digest := digestOf(secret)

	s.mu.Lock()
	defer s.mu.Unlock()
	tokens, err := s.listTokens()
	if err != nil {
		return nil, err
	}
	var found *Token
	for _, tok := range tokens {
		if subtle.ConstantTimeCompare([]byte(tok.Digest), []byte(digest)) == 1 {
			found = tok
			break
		}
	}
	if found == nil {
		return nil, ErrNoToken
	}
	if why := found.Why(now); why != "" {
		return nil, fmt.Errorf("bootstrap token %s is %s", found.ID, why)
	}
	if found.NodeGlob != "" && !target.MatchGlob(found.NodeGlob, nodeID) {
		return nil, fmt.Errorf("bootstrap token %s admits %s, and this node is %s", found.ID, found.NodeGlob, nodeID)
	}
	if found.CIDR != "" {
		ok, err := addrInCIDR(remoteAddr, found.CIDR)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("bootstrap token %s admits %s, and this request came from %s", found.ID, found.CIDR, remoteAddr)
		}
	}

	found.Uses++
	found.SpentBy = append(found.SpentBy, nodeID)
	if err := s.putToken(found); err != nil {
		return nil, err
	}
	return found, nil
}

// addrInCIDR reports whether a remote address -- which arrives with a
// port on it -- is inside a prefix.
func addrInCIDR(remoteAddr, cidr string) (bool, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return false, fmt.Errorf("the token's source restriction %q is not a CIDR: %w", cidr, err)
	}
	host := remoteAddr
	if h, _, splitErr := net.SplitHostPort(remoteAddr); splitErr == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false, fmt.Errorf("the request's source address %q is not an address: %w", remoteAddr, err)
	}
	// A v4-mapped v6 address and its v4 form are the same host, and a
	// listener on :: reports the mapped form.
	return prefix.Contains(addr.Unmap()), nil
}
