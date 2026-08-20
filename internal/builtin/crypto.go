package builtin

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerCrypto installs hashutil and random.
//
// Every random value here comes from crypto/rand. SPEC section 25.3
// permits math/rand nowhere outside the deterministic template seed, and
// the build policy test enforces it — a "random" password from a
// predictable generator is worse than no password, because it looks fine.
func registerCrypto(r *Registries) {
	digest := func(name string, sum func([]byte) string, note string) exec.Module {
		return exec.Module{
			Sig: signature.Signature{
				Module: "hashutil", Function: name,
				Doc:      "Return the " + name + " digest of a string." + note,
				Params:   []signature.Param{req("value", signature.String, "What to digest.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return sum([]byte(states.Str(args, "value", ""))), nil
			},
		}
	}

	r.Exec.Add(
		digest("sha256", func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }, ""),
		digest("sha512", func(b []byte) string { h := sha512.Sum512(b); return hex.EncodeToString(h[:]) }, ""),
		// MD5 and SHA-1 are here because upstreams still publish them.
		// Neither is collision-resistant, and neither should be used for
		// anything but comparing against such an upstream.
		digest("md5", func(b []byte) string { h := md5.Sum(b); return hex.EncodeToString(h[:]) },
			" MD5 is not collision-resistant; use it only to verify an upstream that publishes nothing better."),
		digest("sha1", func(b []byte) string { h := sha1.Sum(b); return hex.EncodeToString(h[:]) },
			" SHA-1 is not collision-resistant; use it only to verify an upstream that publishes nothing better."),

		exec.Module{
			Sig: signature.Signature{
				Module: "hashutil", Function: "digest_file",
				Doc: "Return a file's digest.",
				Params: []signature.Param{
					req("path", signature.Path, "The file."),
					choice("checksum", "sha256", "The algorithm.", "sha256", "sha384", "sha512"),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return hashFile(states.Str(args, "path", ""), states.Str(args, "checksum", "sha256"))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "hashutil", Function: "base64_encodestring",
				Doc:      "Return the base64 encoding of a string.",
				Params:   []signature.Param{req("value", signature.String, "What to encode.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return base64.StdEncoding.EncodeToString([]byte(states.Str(args, "value", ""))), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "hashutil", Function: "base64_decodestring",
				Doc:      "Decode a base64 string.",
				Params:   []signature.Param{req("value", signature.String, "What to decode.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(states.Str(args, "value", "")))
				if err != nil {
					return nil, err
				}
				return string(b), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "hashutil", Function: "hmac_signature",
				Doc: "Verify an HMAC-SHA-256 signature, in constant time.",
				Params: []signature.Param{
					req("value", signature.String, "The signed text."),
					req("shared_secret", signature.String, "The key."),
					req("challenge_hmac", signature.String, "The base64 signature to check."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				mac := hmac.New(sha256.New, []byte(states.Str(args, "shared_secret", "")))
				mac.Write([]byte(states.Str(args, "value", "")))
				want, err := base64.StdEncoding.DecodeString(states.Str(args, "challenge_hmac", ""))
				if err != nil {
					return false, nil
				}
				// hmac.Equal rather than a byte comparison: a signature
				// check that leaks timing is a signature check an attacker
				// can walk.
				return hmac.Equal(mac.Sum(nil), want), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "hashutil", Function: "hmac_compute",
				Doc: "Return the base64 HMAC-SHA-256 of a string.",
				Params: []signature.Param{
					req("value", signature.String, "The text to sign."),
					req("shared_secret", signature.String, "The key."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				mac := hmac.New(sha256.New, []byte(states.Str(args, "shared_secret", "")))
				mac.Write([]byte(states.Str(args, "value", "")))
				return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
			},
		},

		exec.Module{
			Sig: signature.Signature{
				Module: "random", Function: "get_str",
				Doc: "Return a random string, from crypto/rand.",
				Params: []signature.Param{
					opt("length", signature.Int, int64(20), "How many characters."),
					opt("chars", signature.String, "", "The alphabet; defaults to letters, digits, and punctuation."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return randomString(int(states.Int(args, "length", 20)), states.Str(args, "chars", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "random", Function: "hash",
				Doc: "Return the digest of a value.",
				Params: []signature.Param{
					req("value", signature.String, "What to digest."),
					choice("algorithm", "sha512", "The algorithm.", "sha256", "sha384", "sha512"),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return hashBytes([]byte(states.Str(args, "value", "")), states.Str(args, "algorithm", "sha512"))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "random", Function: "seed",
				Doc: "Return a number derived from a value, stable for that value.",
				Params: []signature.Param{
					req("value", signature.String, "The value to derive from."),
					opt("range", signature.Int, int64(10), "The exclusive upper bound."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				// Derived rather than random: a tree uses this to splay a
				// schedule across a fleet, and the value has to be the
				// same on every run for that node or the splay moves.
				sum := sha256.Sum256([]byte(states.Str(args, "value", "")))
				bound := states.Int(args, "range", 10)
				if bound <= 0 {
					bound = 10
				}
				n := int64(0)
				for _, b := range sum[:8] {
					n = n<<8 | int64(b)
				}
				if n < 0 {
					n = -n
				}
				return n % bound, nil
			},
		},
	)
}

// defaultAlphabet is what random.get_str draws from when the caller does
// not name one. Shell metacharacters are excluded: a generated password
// that lands in a configuration file should not need quoting to be safe.
const defaultAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.+="

func randomString(length int, alphabet string) (string, error) {
	if length <= 0 {
		return "", fmt.Errorf("length must be positive")
	}
	if length > 4096 {
		return "", fmt.Errorf("length must be at most 4096")
	}
	if alphabet == "" {
		alphabet = defaultAlphabet
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	// The modulo bias across a 64-character alphabet is negligible, and
	// rejection sampling here would buy nothing an operator can measure.
	out := make([]byte, length)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

// registerDataStore installs the data and environ modules.
func registerDataStore(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "environ", Function: "get",
				Doc: "Return an environment variable of the halite process.",
				Params: []signature.Param{
					req("key", signature.String, "The variable."),
					opt("default", signature.String, "", "What to return when it is unset."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if v, ok := os.LookupEnv(states.Str(args, "key", "")); ok {
					return v, nil
				}
				return states.Str(args, "default", ""), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "environ", Function: "has_value",
				Doc:      "Report whether an environment variable is set and non-empty.",
				Params:   []signature.Param{req("key", signature.String, "The variable.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				v, ok := os.LookupEnv(states.Str(args, "key", ""))
				return ok && v != "", nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "environ", Function: "items",
				Doc: "Return the halite process's environment.",
				// The environment of the agent is not the environment of a
				// child it spawns: SPEC section 25.4 gives a child a clean
				// one. This reports what the agent itself sees.
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				out := value.NewMap(64)
				for _, kv := range os.Environ() {
					if k, v, ok := strings.Cut(kv, "="); ok {
						out.Set(k, v)
					}
				}
				return out, nil
			},
		},
	)
}
