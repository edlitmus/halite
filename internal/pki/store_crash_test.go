package pki

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// What a crash during a first start leaves, and what the next start makes
// of it. cmd/halite-hub's TestAHubKilledDuringItsFirstStartStartsAgain is
// the same two layouts through a real `serve`; these are the refusals that
// must survive the fix, which that test cannot see. DIVERGENCE 5.739.

func TestCreateCAFinishesACAWhoseCertificateWasNeverWritten(t *testing.T) {
	f := Files{Dir: t.TempDir()}
	if err := f.WriteKey(CAKeyFile, testKey(t)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.Path(CAKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := f.CreateCA(ECDSAP384, "halite enrollment CA", time.Hour)
	if err != nil {
		t.Fatalf("a CA key with no certificate was refused: %v", err)
	}
	after, _ := os.ReadFile(f.Path(CAKeyFile))
	if !bytes.Equal(before, after) {
		t.Fatal("finishing the CA replaced its key")
	}
	loaded, err := f.LoadCA(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Cert.Equal(ca.Cert) || !loaded.Cert.IsCA {
		t.Fatal("the CA on disk is not the one returned")
	}
}

func TestCreateCAStillRefusesAWholeCA(t *testing.T) {
	f := Files{Dir: t.TempDir()}
	if _, err := f.CreateCA(ECDSAP384, "halite enrollment CA", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := f.CreateCA(ECDSAP384, "halite enrollment CA", time.Hour); err == nil ||
		!strings.Contains(err.Error(), "already holds an enrollment CA") {
		t.Fatalf("a second CA over a whole one gave %v", err)
	}
}

// A ca.key this package cannot read is not finished and not replaced: it
// may be a key in a form something else wrote, and it is not ours to lose.
func TestCreateCALeavesACAKeyItCannotRead(t *testing.T) {
	f := Files{Dir: t.TempDir()}
	if err := f.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.Path(CAKeyFile), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.CreateCA(ECDSAP384, "halite enrollment CA", time.Hour); err == nil {
		t.Fatal("a CA was made over a key that could not be read")
	}
	if got, _ := os.ReadFile(f.Path(CAKeyFile)); string(got) != "x" || f.Exists(CACertFile) {
		t.Fatalf("the unreadable key was touched: %q, ca.crt present %v", got, f.Exists(CACertFile))
	}
}

func TestWriteKeyReplacesAnEmptyFileAndNothingElse(t *testing.T) {
	f := Files{Dir: t.TempDir()}
	if err := f.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.Path(HubKeyFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteKey(HubKeyFile, testKey(t)); err != nil {
		t.Fatalf("an empty file under the key's name was refused: %v", err)
	}
	if _, err := f.ReadKey(HubKeyFile); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"a key": nil, "one byte": []byte("x")} {
		t.Run(name, func(t *testing.T) {
			if body != nil {
				if err := os.WriteFile(f.Path(HubKeyFile), body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(f.Path(HubKeyFile))
			err := f.WriteKey(HubKeyFile, testKey(t))
			if err == nil || !strings.Contains(err.Error(), "already holds a private key") {
				t.Fatalf("got %v", err)
			}
			if after, _ := os.ReadFile(f.Path(HubKeyFile)); !bytes.Equal(before, after) {
				t.Fatal("the refused write changed the file")
			}
		})
	}
}
