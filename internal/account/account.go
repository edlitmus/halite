// Package account is the local account file of SPEC section 23.2:
// break-glass operators and automation identities that authenticate
// without an external identity provider.
//
// Deliberately not the primary operator path. OIDC is that, and a local
// account exists for the morning the identity provider is the thing
// that is down.
package account

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/fips"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// MinIterations is the lowest PBKDF2 cost this build accepts.
//
// Raised on each release, per SPEC 23.2. A stored record carries its
// own parameters, so raising this does not invalidate an existing hash
// -- it means the next password change costs more, and that a record
// below the floor is refused rather than quietly accepted.
const MinIterations = 210000

// DefaultIterations is what a new hash costs.
const DefaultIterations = 600000

// saltLength and keyLength are in bytes.
const (
	saltLength = 16
	keyLength  = 64
)

// Account is one local identity.
type Account struct {
	Name string
	// Hash is the encoded PBKDF2 verifier, which carries its own
	// parameters so the cost can be raised without invalidating what is
	// already stored.
	Hash string
	// Roles are the roles this account is bound to, which the RBAC
	// policy of SPEC 23.5 resolves.
	Roles []string
	// TOTP is the shared secret for the second factor, base32 as every
	// authenticator app expects. Empty means no second factor.
	TOTP string
	// Disabled keeps a record for the audit while refusing it.
	Disabled bool
}

// Principal is how the policy names this account.
//
// Prefixed, so that an account called `ed` and a certificate whose
// common name is `ed` are different principals. They are different
// people as often as not, and a policy that could not tell them apart
// would be one grant for two identities.
func (a *Account) Principal() string { return "local:" + a.Name }

// File is a loaded account file.
type File struct {
	Path     string
	Accounts map[string]*Account
}

// Lookup finds an account by name.
func (f *File) Lookup(name string) (*Account, bool) {
	if f == nil {
		return nil, false
	}
	a, ok := f.Accounts[name]
	return a, ok
}

// Names lists the accounts, sorted.
func (f *File) Names() []string {
	if f == nil {
		return nil
	}
	out := make([]string, 0, len(f.Accounts))
	for n := range f.Accounts {
		out = append(out, n)
	}
	sortStrings(out)
	return out
}

// Load reads an account file.
//
// An absent file is an empty set rather than an error: an estate on
// OIDC alone has none, and refusing to start without one would make the
// break-glass path mandatory.
func Load(src []byte, path string) (*File, error) {
	f := &File{Path: path, Accounts: map[string]*Account{}}
	doc, warnings, err := yaml.Parse(src, yaml.Options{File: path})
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		// A YAML 1.1 boolean in an account file -- `disabled: no` --
		// means something other than it looks like, and this is not a
		// file to find that out in later.
		return nil, fmt.Errorf("%s: %s", path, w.String())
	}
	if doc == nil {
		return f, nil
	}
	root, ok := doc.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("%s: an account file is a mapping with `accounts`", path)
	}
	raw, ok := root.Get("accounts")
	if !ok || raw == nil {
		return f, nil
	}
	accounts, ok := raw.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("%s: `accounts` is a mapping of name to record", path)
	}

	for _, e := range accounts.Entries() {
		name := value.KeyString(e.Key)
		body, ok := e.Val.(*value.Map)
		if !ok {
			return nil, fmt.Errorf("%s: account %s is a mapping", path, name)
		}
		a := &Account{Name: name}
		for _, field := range body.Entries() {
			key := value.KeyString(field.Key)
			switch key {
			case "hash", "password_hash":
				a.Hash = value.KeyString(field.Val)
			case "roles":
				list, ok := field.Val.([]any)
				if !ok {
					return nil, fmt.Errorf("%s: account %s: `roles` is a list", path, name)
				}
				for _, r := range list {
					a.Roles = append(a.Roles, value.KeyString(r))
				}
			case "totp", "totp_secret":
				a.TOTP = value.KeyString(field.Val)
			case "disabled":
				a.Disabled = value.Truthy(field.Val)
			default:
				return nil, fmt.Errorf("%s: account %s: %q is not an account field", path, name, key)
			}
		}
		if a.Hash == "" {
			return nil, fmt.Errorf("%s: account %s has no password hash", path, name)
		}
		if _, err := parseHash(a.Hash); err != nil {
			return nil, fmt.Errorf("%s: account %s: %w", path, name, err)
		}
		f.Accounts[name] = a
	}
	return f, nil
}

// Hash produces a verifier for a password.
func Hash(password string, iterations int) (string, error) {
	if iterations <= 0 {
		iterations = DefaultIterations
	}
	if iterations < MinIterations {
		return "", fmt.Errorf("%d iterations is below this build's floor of %d", iterations, MinIterations)
	}
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha512.New, password, salt, iterations, keyLength)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha512$%d$%s$%s",
		iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// verifier is a decoded hash.
type verifier struct {
	iterations int
	salt       []byte
	key        []byte
}

func parseHash(encoded string) (*verifier, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha512" {
		return nil, fmt.Errorf("the password hash is not in this build's format")
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return nil, fmt.Errorf("the password hash has no iteration count")
	}
	if iterations < MinIterations {
		// Refused rather than accepted and re-hashed on the next
		// login: a record below the floor is one an operator has to
		// know about, and silently accepting it would keep it there.
		return nil, fmt.Errorf(
			"the password hash costs %d iterations and this build's floor is %d; "+
				"set the password again", iterations, MinIterations)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("the password hash's salt is not readable")
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return nil, fmt.Errorf("the password hash is not readable")
	}
	return &verifier{iterations: iterations, salt: salt, key: key}, nil
}

// Verify reports whether a password matches an account.
//
// Constant time, and the same amount of work whether the account exists
// or not: a login endpoint that answers faster for an unknown name is
// one that enumerates accounts.
func (a *Account) Verify(password string) bool {
	if a == nil {
		// A caller with no account still pays for a hash, so that the
		// answer takes the same time either way.
		_, _ = pbkdf2.Key(sha512.New, password, make([]byte, saltLength), MinIterations, keyLength)
		return false
	}
	v, err := parseHash(a.Hash)
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha512.New, password, v.salt, v.iterations, keyLength)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, v.key) == 1
}

// LockedOut names the accounts that require a second factor this build
// cannot check.
//
// A FIPS build has no TOTP, and an account configured with one cannot
// log in. That is the right refusal and the wrong thing to discover at
// a login prompt, so the service says it at startup instead.
func (f *File) LockedOut() []string {
	if f == nil || TOTPAvailable() {
		return nil
	}
	var out []string
	for name, a := range f.Accounts {
		if a.NeedsSecondFactor() {
			out = append(out, name)
		}
	}
	sortStrings(out)
	return out
}

// NeedsSecondFactor reports whether this account requires a TOTP code.
//
// Still true when TOTP is unavailable. The account requires a second
// factor that this build cannot check, which locks it out — the safe
// direction, and the one an operator has to be told about before the
// first login rather than at it. See File.LockedOut.
func (a *Account) NeedsSecondFactor() bool { return a != nil && a.TOTP != "" }

// TOTPAvailable reports whether this build can check a TOTP code.
//
// SPEC 27.4: SHA-1 is unavailable in FIPS mode, and RFC 6238 is defined
// on HMAC-SHA-1, so the path is disabled rather than reimplemented on
// another hash — a TOTP that agreed with no authenticator app would be
// worse than one that is absent.
func TOTPAvailable() bool { return !fips.Restricted() }

// VerifyTOTP checks a time-based one-time code, per RFC 6238.
//
// One step either side is accepted, which is the standard allowance for
// a clock that is slightly out. More than that turns a thirty-second
// code into a minute-and-a-half one.
func (a *Account) VerifyTOTP(code string, now time.Time) bool {
	if a == nil || a.TOTP == "" {
		return false
	}
	if !TOTPAvailable() {
		// Refused, not downgraded. RFC 6238 is HMAC-SHA-1 and SPEC 27.4
		// disables the path in FIPS mode; under GODEBUG=fips140=only
		// the module panics rather than returning an error, so without
		// this the second factor crashes the login handler instead of
		// failing it.
		//
		// It must fail closed. Reporting that the account no longer
		// needs a second factor would let a password alone through on
		// exactly the accounts that asked for two.
		return false
	}
	secret, err := decodeBase32(a.TOTP)
	if err != nil {
		return false
	}
	counter := now.Unix() / 30
	match := 0
	for _, skew := range []int64{-1, 0, 1} {
		want := totpCode(secret, counter+skew)
		// Every candidate is compared, and in constant time, so the
		// answer does not say which step matched.
		match |= subtle.ConstantTimeCompare([]byte(want), []byte(code))
	}
	return match == 1
}

// totpCode is HMAC-SHA-1 over the counter, truncated as RFC 4226 says.
func totpCode(secret []byte, counter int64) string {
	var buf [8]byte
	for i := 7; i >= 0; i-- {
		buf[i] = byte(counter)
		counter >>= 8
	}
	mac := hmac.New(sha1New, secret)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	truncated := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	return fmt.Sprintf("%06d", truncated%1000000)
}
