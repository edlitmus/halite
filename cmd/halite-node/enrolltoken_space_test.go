package main

import (
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/keystore"
)

// **What the hub mints, `enroll --token SECRET` takes** (DIVERGENCE
// 5.674). The two halves are the hub's MintToken and this program's
// argument parser, which refuses, on purpose, a value after a space that
// begins with `-`. A bare base64url token began with one one time in 64.
//
// The minted tokens are the hub's own, so the test fails if MintToken
// goes back to a form the parser cannot take. The constructed one is the
// worst case the prefix leaves -- a hyphen as the first random
// character -- which a sample of 64 would meet only sometimes.
func TestAMintedTokenIsTakenAfterASpace(t *testing.T) {
	store, err := keystore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var secrets []string
	for i := 0; i < 64; i++ {
		_, secret, err := store.MintToken(keystore.TokenOptions{TTL: time.Hour}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, secret)
	}
	secrets = append(secrets, secrets[0][:4]+"-"+secrets[0][5:])
	for _, secret := range secrets {
		args, err := cli.Parse([]string{"--token", secret, "--wait"})
		if err != nil {
			t.Fatal(err)
		}
		if got := args.Flag("token", ""); got != secret {
			t.Fatalf("`--token %s` gave the token %q", secret, got)
		}
	}
}
