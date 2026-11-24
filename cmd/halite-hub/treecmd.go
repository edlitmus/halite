package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/fileperm"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/jobsign"
	"github.com/edlitmus/halite/internal/treesign"
	"github.com/edlitmus/halite/internal/value"
)

const treeUsage = `halite-hub tree — sign a state tree, for nodes that require it

Usage:
  halite-hub tree manifest <dir> --env <name>   list <dir> into <dir>/_halite/manifest.json
  halite-hub tree sign <dir> --sign-key <path>  sign it, writing <dir>/_halite/manifest.sig
  halite-hub tree sign <dir> --sign-extension <path>
                                                sign it through a bridged signer instead
  halite-hub tree verify <dir> --env <name> --keys <file>
                                                check a signed tree as a node would

A node with require_state_signature applies only files the signed manifest
names, with the digests it names, for the environment it names, and never
one older than the newest it has already accepted (SPEC section 25.8).
Pillar is not covered: the hub compiles it.

None of these opens the hub's state, and they are meant for a machine that
is not the hub, usually CI: a hub that holds the signing key can sign
whatever it serves. --config is read only for file_ignore_glob,
file_ignore_regex, fileserver_follow_symlinks and hash_type, so that the
manifest lists exactly what the hub will serve; pass the hub's.

<dir> is the tree as the hub serves it. For a gitfs tree that is the
commit as git archive writes it, not a checkout: export-ignore and
attributes can make the two differ. The manifest describes everything but
_halite/ itself, which is then committed on top of the content.

Flags for manifest:
  --env <name>         the environment it is for; required
  --source <text>      what it was built from, such as "commit <id> <ref>"; signed, not checked
  --expires <dur>      a node refuses it after this long; default none
  --hash-type <name>   sha256 (default, or the hub's hash_type), sha384, sha512, sha3-256

Flags for sign:
  --sign-key <path>    a PEM ECDSA P-256 or P-384 key, as keys signer create writes
  --sign-extension <path>  a bridged signer extension, such as halite-ext-signer-kms

Flags for verify:
  --env <name>         the environment the tree is served as; required
  --keys <file>        trusted keys, one "<name> <base64 key>" line each
`

func runTree(args *cli.Args) int {
	if len(args.Positional) < 2 {
		fmt.Fprint(os.Stderr, treeUsage)
		return cli.ExitUsage
	}
	sub, dir := args.Positional[0], args.Positional[1]
	switch sub {
	case "manifest":
		return treeManifest(args, dir)
	case "sign":
		return treeSign(args, dir)
	case "verify":
		return treeVerify(args, dir)
	}
	fmt.Fprint(os.Stderr, treeUsage)
	return cli.ExitUsage
}

// treeRoots is <dir> as the hub's file server would serve it, under the
// hub's own hide rules.
func treeRoots(args *cli.Args, env, dir string) (*fileserver.Roots, string) {
	h := openHubForConfig(args)
	roots := fileserver.NewRoots(map[string][]string{env: {dir}})
	roots.FollowSymlinks = h.cfg.Bool("fileserver_follow_symlinks", false)
	roots.IgnoreGlobs = h.cfg.StringSlice("file_ignore_glob")
	if err := roots.SetIgnoreRegexes(h.cfg.StringSlice("file_ignore_regex")); err != nil {
		cli.Fatalf("%v", err)
	}
	if roots.HidesManifest() {
		cli.Fatalf("file_ignore_glob or file_ignore_regex hides %s, so the hub would serve no manifest; "+
			"exempt %s/", treesign.ManifestPath, treesign.ReservedDir)
	}
	return roots, args.Flag("hash-type", h.cfg.String("hash_type", "sha256"))
}

func requiredEnv(args *cli.Args) string {
	env := args.Flag("env", "")
	if env == "" {
		cli.Usagef("--env names the environment the tree is served as")
	}
	if err := treesign.CheckEnv(env); err != nil {
		cli.Usagef("%v", err)
	}
	return env
}

func treeManifest(args *cli.Args, dir string) int {
	env := requiredEnv(args)
	roots, hashType := treeRoots(args, env, dir)
	if err := roots.ReservedProblem(env); err != nil {
		cli.Fatalf("the hub would refuse to serve this tree: %v", err)
	}
	files, err := roots.TreeEntries(env, hashType)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	issued := time.Now()
	m := &treesign.Manifest{Env: env, Issued: treesign.FormatTime(issued),
		Source: args.Flag("source", ""), HashType: hashType, Files: files}
	if raw := args.Flag("expires", ""); raw != "" {
		ttl, err := value.ParseDuration(raw)
		if err != nil || ttl <= 0 {
			cli.Usagef("--expires %q is not a positive duration", raw)
		}
		m.Expires = treesign.FormatTime(issued.Add(ttl))
	}
	encoded, err := treesign.Encode(m)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	target := filepath.Join(dir, filepath.FromSlash(treesign.ManifestPath))
	if err := fileperm.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		cli.Fatalf("%v", err)
	}
	if err := atomicfile.Write(target, encoded, 0o644); err != nil {
		cli.Fatalf("%v", err)
	}
	// A signature beside an older manifest would be a pair that does not
	// verify, and the next sign would be asked to sign the new one anyway.
	_ = os.Remove(filepath.Join(dir, filepath.FromSlash(treesign.SignaturePath)))
	fmt.Printf("manifest  %s\n", target)
	fmt.Printf("env       %s\n", env)
	fmt.Printf("files     %d\n", len(files))
	fmt.Printf("issued    %s\n", m.Issued)
	fmt.Printf("revision  %s\n", treesign.ID(encoded))
	return 0
}

func treeSign(args *cli.Args, dir string) int {
	keyPath, extPath := args.Flag("sign-key", ""), args.Flag("sign-extension", "")
	if (keyPath == "") == (extPath == "") {
		cli.Usagef("tree sign takes one of --sign-key or --sign-extension")
	}
	manifestPath := filepath.Join(dir, filepath.FromSlash(treesign.ManifestPath))
	raw, err := atomicfile.Read(manifestPath)
	if err != nil {
		cli.Fatalf("%v; run `halite-hub tree manifest` first", err)
	}
	// Parsed before it is signed: a signature over a manifest every node
	// refuses is worse than none, because it looks finished.
	m, err := treesign.Parse(raw)
	if err != nil {
		cli.Fatalf("%s: %v", manifestPath, err)
	}
	var line string
	if keyPath != "" {
		pem, err := os.ReadFile(keyPath)
		if err != nil {
			cli.Fatalf("--sign-key %s: %v", keyPath, err)
		}
		key, err := jobsign.DecodePrivateKey(pem)
		if err != nil {
			cli.Fatalf("--sign-key %s: %v", keyPath, err)
		}
		if line, err = treesign.SignatureLine(key, raw); err != nil {
			cli.Fatalf("%v", err)
		}
	} else {
		der, err := signDigestWithExtension(context.Background(), extPath, treesign.Digest(raw))
		if err != nil {
			cli.Fatalf("--sign-extension %s: %v", extPath, err)
		}
		line = encodeSignature(der)
	}
	sigPath := filepath.Join(dir, filepath.FromSlash(treesign.SignaturePath))
	if err := atomicfile.Write(sigPath, []byte(line+"\n"), 0o644); err != nil {
		cli.Fatalf("%v", err)
	}
	fmt.Printf("signed    %s\n", sigPath)
	fmt.Printf("env       %s\n", m.Env)
	fmt.Printf("issued    %s\n", m.Issued)
	fmt.Printf("revision  %s\n", treesign.ID(raw))
	return 0
}

func readTrustedKeys(path string) ([]jobsign.SignerKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var keys []jobsign.SignerKey
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, err := jobsign.ParseSignerKey(line)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// treeVerify checks a signed tree the way a node will: the signature, the
// format, the environment, the age, and then every file the tree holds
// against the manifest in both directions. It is the early warning a CI
// job or the hub's operator runs before the nodes find out.
func treeVerify(args *cli.Args, dir string) int {
	env := requiredEnv(args)
	keysPath := args.Flag("keys", "")
	if keysPath == "" {
		cli.Usagef("--keys names the file of trusted keys")
	}
	keys, err := readTrustedKeys(keysPath)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	roots, _ := treeRoots(args, env, dir)
	if err := roots.ReservedProblem(env); err != nil {
		cli.Fatalf("the hub would refuse to serve this tree: %v", err)
	}
	raw, _, err := roots.Read(env, treesign.ManifestPath)
	if err != nil {
		cli.Fatalf("no manifest: %v", err)
	}
	sig, _, err := roots.Read(env, treesign.SignaturePath)
	if err != nil {
		cli.Fatalf("no signature: %v", err)
	}
	signer, err := treesign.Verify(keys, raw, sig)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	m, err := treesign.Parse(raw)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	if err := treesign.CheckFresh(m, treesign.ID(raw), env, time.Now(), 0, nil); err != nil {
		cli.Fatalf("%v", err)
	}
	served, err := roots.TreeEntries(env, m.HashType)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	var problems []string
	seen := map[string]bool{}
	for _, e := range served {
		seen[e.Path] = true
		want, ok := m.Lookup(e.Path)
		switch {
		case !ok:
			problems = append(problems, e.Path+": in the tree and not in the manifest; a node will not find it")
		case want.Hash != e.Hash:
			problems = append(problems, e.Path+": its content differs from the manifest's; a node will refuse it")
		}
	}
	for _, e := range m.Files {
		if !seen[e.Path] {
			problems = append(problems, e.Path+": in the manifest and not served; a node will fail to fetch it")
		}
	}
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, p)
	}
	if len(problems) > 0 {
		cli.Fatalf("%d file(s) disagree with the signed manifest for %s", len(problems), env)
	}
	fmt.Printf("verified  %s\n", dir)
	fmt.Printf("env       %s\n", env)
	fmt.Printf("signer    %s\n", signer)
	fmt.Printf("files     %d\n", len(m.Files))
	fmt.Printf("issued    %s\n", m.Issued)
	fmt.Printf("revision  %s\n", treesign.ID(raw))
	return 0
}
