package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/extension"
	"github.com/edlitmus/halite/internal/fileperm"
)

// Signing extensions with the shipped binary.
//
// Until this, the only way to sign one was `go run ./tools/extbundle`,
// which needs a checkout of this repository and a Go toolchain, so an
// estate could not make signing a job it ran. These are that, as
// subcommands, on the same code (extension.SignBundle).
//
// They run anywhere the binary is installed, and they are meant to run
// somewhere other than the hub: a machine that holds the signing key and
// also verifies the extensions it signed is checking its own signature,
// and whoever takes it can sign anything (docs/extensions.md). Neither
// opens the hub's state. DIVERGENCE 5.260.

// extensionKeyPath is where a named key lives: beside the rest of this
// machine's key material, as `keys signer create` puts a job signer's.
func extensionKeyPath(h *hubContext, name string) string {
	return filepath.Join(h.cfg.PathUnderRoot("pki_dir", "pki"), "extension-"+name+".key")
}

// extensionsKeyCreate is `extensions key create <name> [--out <path>]`.
func extensionsKeyCreate(args *cli.Args) int {
	if len(args.Positional) < 3 || args.Positional[1] != "create" {
		fmt.Fprint(os.Stderr, extensionsUsage)
		return cli.ExitUsage
	}
	name := args.Positional[2]
	if strings.ContainsAny(name, " /\\") {
		cli.Fatalf("a key name is one word: it goes in a file name and in extension_trust_keys")
	}
	h := openHubForConfig(args)
	path := args.Flag("out", extensionKeyPath(h, name))
	if err := fileperm.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		cli.Fatalf("%v", err)
	}
	public, err := extension.CreateSigningKey(path)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	fmt.Printf("key       %s\n", path)
	fmt.Println("\nTrust it on the hub and on every node that runs what it signs:")
	fmt.Println("extension_trust_keys:")
	fmt.Printf("  - '%s'\n", extension.FormatTrustKey(name, public))
	return 0
}

// extensionsSign is `extensions sign <dir> --name <n> --exe <file> --key <k>`.
func extensionsSign(args *cli.Args) int {
	if len(args.Positional) < 2 {
		fmt.Fprint(os.Stderr, extensionsUsage)
		return cli.ExitUsage
	}
	dir := args.Positional[1]
	name, exe, keyArg := args.Flag("name", ""), args.Flag("exe", ""), args.Flag("key", "")
	if name == "" || exe == "" || keyArg == "" {
		cli.Usagef("extensions sign needs --name, --exe and --key")
	}
	h := openHubForConfig(args)

	// A name is looked up beside this machine's keys; anything that
	// looks like a path is a path. A missing key is an error rather than
	// a new one: a key made by accident signs a bundle no node trusts.
	keyPath := keyArg
	if !strings.ContainsAny(keyArg, "/\\") {
		keyPath = extensionKeyPath(h, keyArg)
	}
	key, err := extension.LoadSigningKey(keyPath)
	if os.IsNotExist(err) {
		cli.Fatalf("there is no signing key at %s; make one with `halite-hub extensions key create %s`",
			keyPath, keyArg)
	}
	if err != nil {
		cli.Fatalf("%v", err)
	}

	// Not --version, which every command reads as the flag that prints
	// its own version, and which takes no value.
	version := args.Flag("ext-version", "1.0.0")
	root, err := extension.SignBundle(extension.SignOptions{
		Dir: dir, Name: name, Version: version, Kind: args.Flag("kind", "module"), Exe: exe,
		Platform: args.Flag("platform", ""), Declares: extension.SplitDeclares(args.Flag("declares", "")),
		Key: key,
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}

	where := dir
	if tree := args.Flag("publish", ""); tree != "" {
		where, err = publishBundle(dir, tree, name, version)
		if err != nil {
			cli.Fatalf("signed, and not published: %v", err)
		}
	}
	fmt.Printf("signed    %s %s\n", name, version)
	fmt.Printf("bundle    %s\n", where)
	fmt.Printf("root      %x\n", root)
	fmt.Println("\nPin it on the hub and the nodes that run it:")
	fmt.Println("extension_pins:")
	fmt.Printf("  %s:\n    version: %s\n    root: %x\n", name, version, root)
	return 0
}

// publishBundle copies a signed bundle -- the files its manifest names,
// the manifest and the signature -- to <tree>/_ext/<name>/<version>/,
// where `extensions sync` and a node's sync look for it.
//
// The version's directory is claimed with one Mkdir, which fails if it
// already exists: a version is published once, and a bundle copied over
// another leaves a mixture that matches neither signature. Each file is
// then written through atomicfile, the executable first and the
// signature last, so a sync that runs in between finds a bundle that does
// not verify and refuses it, rather than one that runs.
func publishBundle(dir, tree, name, version string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, extension.ManifestName))
	if err != nil {
		return "", err
	}
	manifest, err := extension.ParseManifest(raw)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(tree, extension.ExtPrefix, name, version)
	if err := fileperm.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := os.Mkdir(dest, 0o755); os.IsExist(err) {
		return "", fmt.Errorf("%s already exists; a published version is never replaced, so sign a new one", dest)
	} else if err != nil {
		return "", err
	}
	payload := make([]string, 0, len(manifest.Files))
	for rel := range manifest.Files {
		payload = append(payload, rel)
	}
	sort.Strings(payload)
	for _, f := range append(payload, extension.ManifestName, extension.SignatureName) {
		src := filepath.Join(dir, f)
		info, err := os.Stat(src)
		if err != nil {
			return "", err
		}
		body, err := os.ReadFile(src)
		if err != nil {
			return "", err
		}
		target := filepath.Join(dest, f)
		if err := fileperm.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		if err := atomicfile.Write(target, body, info.Mode().Perm()); err != nil {
			return "", err
		}
	}
	return dest, nil
}
