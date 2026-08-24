package account

import (
	"crypto/sha1"
	"encoding/base32"
	"hash"
	"sort"
	"strings"
)

// sha1New is HMAC-SHA-1 for RFC 6238, which specifies it. It is not a
// choice about hashing: every authenticator application implements this
// one, and a TOTP that used something stronger would not be a TOTP.
func sha1New() hash.Hash { return sha1.New() }

// decodeBase32 reads a TOTP secret in the form an authenticator app
// shows, which is unpadded and often spaced into groups.
func decodeBase32(s string) ([]byte, error) {
	cleaned := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	return base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(cleaned)
}

func sortStrings(s []string) { sort.Strings(s) }
