// Command extbundle signs an extension bundle.
//
// For the lab and for whoever publishes an extension: it digests a
// directory, writes the manifest, and signs the Merkle root. It is a
// thin front on extension.SignBundle, which `halite-hub extensions sign`
// uses too; unlike that command it generates the key the first time.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/edlitmus/halite/internal/extension"
	"github.com/edlitmus/halite/internal/fileperm"
)

func main() {
	dir := flag.String("dir", "", "the bundle directory")
	name := flag.String("name", "", "the extension name")
	version := flag.String("version", "1.0.0", "the version")
	kind := flag.String("kind", "module", "the extension kind")
	exe := flag.String("exe", "", "the executable, relative to the directory")
	declares := flag.String("declares", "", "what it needs, comma separated")
	keyFile := flag.String("key", "", "the signing key; generated when absent")
	platform := flag.String("platform", "",
		"goos/goarch the executable is for; read from the executable when absent, and refused when it disagrees")
	flag.Parse()

	if *dir == "" || *name == "" || *exe == "" {
		fmt.Fprintln(os.Stderr, "usage: extbundle -dir <dir> -name <name> -exe <file>")
		os.Exit(2)
	}

	// Before the key: a bundle that is going to be refused should not
	// leave a freshly generated signing key behind it.
	_, err := extension.ExecutablePlatform(filepath.Join(*dir, *exe), *platform)
	check(err)

	private, public := loadOrCreateKey(*keyFile)

	root, err := extension.SignBundle(extension.SignOptions{
		Dir: *dir, Name: *name, Version: *version, Kind: *kind, Exe: *exe,
		Platform: *platform, Declares: extension.SplitDeclares(*declares), Key: private,
	})
	check(err)

	fmt.Println("trust_key:", extension.FormatTrustKey("lab", public))
	fmt.Printf("root: %x\n", root)
}

func loadOrCreateKey(path string) (ed25519.PrivateKey, ed25519.PublicKey) {
	if path != "" {
		if private, err := extension.LoadSigningKey(path); err == nil {
			return private, private.Public().(ed25519.PublicKey)
		} else if !os.IsNotExist(err) {
			check(err)
		}
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	check(err)
	if path != "" {
		check(fileperm.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(private)), 0o600))
	}
	return private, public
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "extbundle:", err)
		os.Exit(1)
	}
}
