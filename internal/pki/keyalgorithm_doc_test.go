package pki

import (
	"regexp"
	"testing"

	"github.com/edlitmus/halite/internal/config"
)

// The configuration reference and the parser must name the same algorithms.
//
// They did not (DIVERGENCE 5.722): the reference, generated from the key
// table, said `key_algorithm` took "ecdsa-p256, ecdsa-p384, rsa-3072,
// rsa-4096, or ed25519 in non-FIPS builds", while ParseKeyAlgorithm has
// only ever accepted the two ECDSA curves. An operator who chose rsa-4096
// from the documentation got a hub that would not start. So every value
// the documentation offers is parsed here, and so is the documented
// default, and each must come back as a distinct algorithm.
func TestKeyAlgorithmDocumentationNamesOnlyWhatParses(t *testing.T) {
	var key *config.Key
	for i := range config.Keys {
		if config.Keys[i].Name == "key_algorithm" {
			key = &config.Keys[i]
			break
		}
	}
	if key == nil {
		t.Fatal("key_algorithm is not in config.Keys")
	}

	// An algorithm name is a lowercase word with a digit in it
	// (ecdsa-p384, rsa-3072, ed25519); the prose around them ("or",
	// "non-FIPS builds") has none. A first version required a hyphen and
	// so never saw ed25519, which is the name this test exists for.
	offered := regexp.MustCompile(`\b[a-z][a-z0-9-]*[0-9][a-z0-9-]*\b`).FindAllString(key.Doc, -1)
	if len(offered) == 0 {
		t.Fatalf("found no algorithm names in the key_algorithm documentation %q", key.Doc)
	}

	seen := map[KeyAlgorithm]string{}
	for _, name := range offered {
		alg, err := ParseKeyAlgorithm(name)
		if err != nil {
			t.Errorf("the documentation offers %q and the parser refuses it: %v", name, err)
			continue
		}
		if prev, dup := seen[alg]; dup {
			t.Errorf("%q and %q both parse to %s; the documentation offers one algorithm twice", prev, name, alg)
		}
		seen[alg] = name
	}
	if _, err := ParseKeyAlgorithm(key.Default); err != nil {
		t.Errorf("the documented default %q does not parse: %v", key.Default, err)
	}
}
