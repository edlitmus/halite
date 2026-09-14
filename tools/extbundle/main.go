// Command extbundle signs an extension bundle.
//
// For the lab and for whoever publishes an extension: it digests a
// directory, writes the manifest, and signs the Merkle root.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/edlitmus/halite/internal/extension"
)

func main() {
	dir := flag.String("dir", "", "the bundle directory")
	name := flag.String("name", "", "the extension name")
	version := flag.String("version", "1.0.0", "the version")
	kind := flag.String("kind", "module", "the extension kind")
	exe := flag.String("exe", "", "the executable, relative to the directory")
	declares := flag.String("declares", "", "what it needs, comma separated")
	keyFile := flag.String("key", "", "the signing key; generated when absent")
	flag.Parse()

	if *dir == "" || *name == "" || *exe == "" {
		fmt.Fprintln(os.Stderr, "usage: extbundle -dir <dir> -name <name> -exe <file>")
		os.Exit(2)
	}

	private, public := loadOrCreateKey(*keyFile)

	manifest, err := extension.Build(*dir, extension.Manifest{
		Name: *name, Version: *version, Kind: *kind,
		Executables: map[string]string{
			extension.Platform(runtime.GOOS, runtime.GOARCH): *exe,
		},
		Declares: splitDeclares(*declares),
	})
	check(err)

	raw, err := manifest.Encode()
	check(err)
	check(os.WriteFile(filepath.Join(*dir, extension.ManifestName), raw, 0o644))

	root, err := extension.MerkleRoot(manifest.Files)
	check(err)
	check(os.WriteFile(filepath.Join(*dir, extension.SignatureName),
		extension.Sign(private, root), 0o644))

	fmt.Println("trust_key:", extension.FormatTrustKey("lab", public))
	fmt.Printf("root: %x\n", root)
}

func loadOrCreateKey(path string) (ed25519.PrivateKey, ed25519.PublicKey) {
	if path != "" {
		raw, err := os.ReadFile(path)
		if err == nil {
			decoded, err := base64.StdEncoding.DecodeString(string(raw))
			check(err)
			private := ed25519.PrivateKey(decoded)
			return private, private.Public().(ed25519.PublicKey)
		}
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	check(err)
	if path != "" {
		check(os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(private)), 0o600))
	}
	return private, public
}

// splitDeclares reads `-declares network,root`.
//
// On a comma, as the flag's own help says. This used to use
// filepath.SplitList, which splits on the operating system's path list
// separator -- so `network,root` was one declaration named
// "network,root" on every platform, and the colon form it did accept on
// unix was silently one declaration on Windows. A declaration that does
// not parse is a permission the sandbox never grants, which shows up as
// an extension that cannot reach the network for no stated reason.
func splitDeclares(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "extbundle:", err)
		os.Exit(1)
	}
}
