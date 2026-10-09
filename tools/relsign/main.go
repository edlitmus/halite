// Command relsign writes, and checks, SPEC 4.3's detached signature per
// release artifact, with a key held in AWS KMS.
//
// It runs after `make dist` has written dist/SHA256SUMS and, in the
// release this is for, after the two builders in release.yml have held
// that manifest equal and the attestation has named it. It is driven by
// an operator from a workstation rather than by the workflow, because
// the one thing a detached signature can say that the keyless
// attestation does not is that a person with the key looked first. Three
// subcommands:
//
//	relsign pubkey -key alias/halite-release -region us-east-1 > halite-release.pub
//	relsign sign   -dist dist -key alias/halite-release -region us-east-1 -pub halite-release.pub
//	relsign verify -dist dist -pub halite-release.pub
//
// # What is signed
//
// Each artifact's SHA-256 digest, as the manifest lists it, and the
// digest of the manifest itself. A signature goes beside its file as
// `<name>.sig`, ASN.1 DER, which is the form a tool an operator already
// has reads:
//
//	openssl dgst -sha256 -verify halite-release.pub -signature SHA256SUMS.sig SHA256SUMS
//
// KMS signs the digest and never sees the artifact, and openssl hashes
// the artifact and checks the digest, so the two agree by construction
// -- which is why `verify` here is for the operator and the tests, and an
// air-gapped site needs nothing from this repository but the public key.
//
// # It signs what it verified and nothing else
//
// `sign` refuses before the first KMS call if any file the manifest
// names is missing or does not match its line, if the manifest lists
// nothing, if a `.sig` already exists, or if the key KMS holds is not
// the one in `-pub`. The last is the important one. The public key file
// is what every operator verifies against, and a release signed with a
// key whose public half is not the committed one is a release nobody
// can check. `-pub` is required for that reason: there is no mode in
// which the tool picks a key and reports what it was afterwards.
//
// The signatures are not listed in the manifest -- they are made after
// it, over it -- and `make dist` removes any that are lying about before
// it sums the directory, so a stale `.sig` cannot become a line in a
// fresh manifest.
package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/awskms"
)

// manifestName is what `make dist` writes and release.yml publishes.
const manifestName = "SHA256SUMS"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var err error
	switch os.Args[1] {
	case "sign":
		err = signCmd(ctx, os.Args[2:], os.Stdout)
	case "verify":
		err = verifyCmd(os.Args[2:], os.Stdout)
	case "pubkey":
		err = pubkeyCmd(ctx, os.Args[2:], os.Stdout, os.Stderr)
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "relsign:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, "usage:\n"+
		"  relsign pubkey -key <id|alias|arn> [-region r]            print the key's public half as PEM\n"+
		"  relsign sign   -dist dist -key <id|alias|arn> -pub file   sign every artifact in dist/SHA256SUMS, and the manifest\n"+
		"  relsign verify -dist dist -pub file                       check every artifact and signature in dist/\n"+
		"\n"+
		"The region is -region, else AWS_REGION, else the region in an ARN key.\n"+
		"Credentials are AWS_ACCESS_KEY_ID and friends in the environment, an\n"+
		"instance role, or a web identity; on a workstation, run\n"+
		"`aws configure export-credentials --format env` after `aws sso login`.\n")
}

// kmsFlags are the flags the two subcommands that reach KMS share.
type kmsFlags struct {
	key, region, partition string
}

func (k *kmsFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&k.key, "key", "", "the KMS key: an id, alias/<name>, or either one's ARN")
	fs.StringVar(&k.region, "region", "", "the key's region; defaults to AWS_REGION, then to the region in an ARN")
	fs.StringVar(&k.partition, "partition", "", "aws, aws-us-gov or aws-cn; defaults to AWS_PARTITION, then to the partition in an ARN")
}

func (k *kmsFlags) client() (*awskms.Client, error) {
	if k.key == "" {
		return nil, errors.New("-key is required")
	}
	region, partition := k.region, k.partition
	if region == "" {
		region = os.Getenv("AWS_REGION")
	}
	if partition == "" {
		partition = os.Getenv("AWS_PARTITION")
	}
	if strings.HasPrefix(k.key, "arn:") {
		// arn:<partition>:kms:<region>:<account>:key/<id>
		parts := strings.Split(k.key, ":")
		if len(parts) >= 6 {
			if region == "" {
				region = parts[3]
			}
			if partition == "" {
				partition = parts[1]
			}
		}
	}
	if region == "" {
		return nil, errors.New("no region: give -region, set AWS_REGION, or name the key by ARN")
	}
	return &awskms.Client{KeyID: k.key, Region: region, Partition: partition}, nil
}

func pubkeyCmd(ctx context.Context, args []string, out, log io.Writer) error {
	fs := flag.NewFlagSet("pubkey", flag.ContinueOnError)
	var k kmsFlags
	k.bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := k.client()
	if err != nil {
		return err
	}
	pub, err := c.PublicKey(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(log, "%s\n%s %s\nfingerprint %s\n", pub.ARN, pub.Spec, pub.Usage, pub.Fingerprint())
	_, err = out.Write(pub.PEM())
	return err
}

// signer is the one thing `sign` needs from KMS, as an interface so the
// tests can sign with a local key and hold `sign` and `verify` to each
// other without a service in the way.
type signer interface {
	PublicKey(context.Context) (*awskms.PublicKey, error)
	Sign(context.Context, [sha256.Size]byte) ([]byte, error)
}

func signCmd(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	var k kmsFlags
	k.bind(fs)
	dist := fs.String("dist", "dist", "the directory holding SHA256SUMS and the artifacts it lists")
	pubFile := fs.String("pub", "", "the committed public key, PEM; the KMS key must be this one")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pubFile == "" {
		return errors.New("-pub is required: a release is verified against the committed public key, so the key that signs it must be that one")
	}
	c, err := k.client()
	if err != nil {
		return err
	}
	return sign(ctx, *dist, *pubFile, c, out)
}

func sign(ctx context.Context, dist, pubFile string, s signer, out io.Writer) error {
	expected, err := readPublicKeyFile(pubFile)
	if err != nil {
		return err
	}
	entries, manifest, err := readManifest(dist)
	if err != nil {
		return err
	}
	// Everything that can be checked without the key is checked before
	// the first call that costs money or leaves a CloudTrail record.
	if err := checkFiles(dist, entries); err != nil {
		return err
	}
	for _, e := range entries {
		if _, err := os.Lstat(filepath.Join(dist, e.name+".sig")); err == nil {
			return fmt.Errorf("%s.sig already exists; a set signed twice is a question, not a default -- remove the .sig files to sign again", e.name)
		}
	}
	if _, err := os.Lstat(filepath.Join(dist, manifestName+".sig")); err == nil {
		return fmt.Errorf("%s.sig already exists; remove the .sig files to sign again", manifestName)
	}

	pub, err := s.PublicKey(ctx)
	if err != nil {
		return err
	}
	if string(pub.DER) != string(expected.DER) {
		return fmt.Errorf("the KMS key %s is not the key in %s (fingerprints %s and %s); refusing to sign with a key the release cannot be verified against",
			pub.ARN, pubFile, pub.Fingerprint(), expected.Fingerprint())
	}
	fmt.Fprintf(out, "signing %d artifacts and %s with %s (%s)\n", len(entries), manifestName, pub.ARN, pub.Fingerprint())

	for _, e := range entries {
		if err := signOne(ctx, dist, e.name, e.digest, s, pub.Key); err != nil {
			return err
		}
		fmt.Fprintf(out, "  %s.sig\n", e.name)
	}
	if err := signOne(ctx, dist, manifestName, sha256.Sum256(manifest), s, pub.Key); err != nil {
		return err
	}
	fmt.Fprintf(out, "  %s.sig\n", manifestName)
	return nil
}

// signOne signs one digest and writes the signature, after checking it
// against the public key a second time. awskms.Client already does, but
// `sign` takes any signer and the file on disk is the claim; a test
// signer that lies must not produce one.
func signOne(ctx context.Context, dist, name string, digest [sha256.Size]byte, s signer, pub *ecdsa.PublicKey) error {
	der, err := s.Sign(ctx, digest)
	if err != nil {
		return fmt.Errorf("signing %s: %w", name, err)
	}
	if !ecdsa.VerifyASN1(pub, digest[:], der) {
		return fmt.Errorf("signing %s: the signature does not verify against the public key; not written", name)
	}
	path := filepath.Join(dist, name+".sig")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(der); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func verifyCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	dist := fs.String("dist", "dist", "the directory holding SHA256SUMS, the artifacts and the .sig files")
	pubFile := fs.String("pub", "", "the public key, PEM")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pubFile == "" {
		return errors.New("-pub is required")
	}
	return verify(*dist, *pubFile, out)
}

// verify checks the manifest's signature, then every file against its
// line and every signature against its file. Every problem is listed
// rather than the first one stopping it, because an operator looking at
// a bad set wants to know whether it is one file or all of them.
func verify(dist, pubFile string, out io.Writer) error {
	pub, err := readPublicKeyFile(pubFile)
	if err != nil {
		return err
	}
	entries, manifest, err := readManifest(dist)
	if err != nil {
		return err
	}
	var problems []string
	if err := verifyOne(dist, manifestName, sha256.Sum256(manifest), pub.Key); err != nil {
		problems = append(problems, err.Error())
	}
	for _, e := range entries {
		sum, err := fileDigest(filepath.Join(dist, e.name))
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if sum != e.digest {
			problems = append(problems, fmt.Sprintf("%s: digest %x, manifest says %x", e.name, sum, e.digest))
			continue
		}
		if err := verifyOne(dist, e.name, e.digest, pub.Key); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%d problem(s) in %s:\n  %s", len(problems), dist, strings.Join(problems, "\n  "))
	}
	fmt.Fprintf(out, "%s and %d artifacts in %s verify against %s (%s)\n",
		manifestName, len(entries), dist, pubFile, pub.Fingerprint())
	return nil
}

func verifyOne(dist, name string, digest [sha256.Size]byte, pub *ecdsa.PublicKey) error {
	der, err := os.ReadFile(filepath.Join(dist, name+".sig"))
	if err != nil {
		return fmt.Errorf("%s: no signature: %v", name, err)
	}
	if !ecdsa.VerifyASN1(pub, digest[:], der) {
		return fmt.Errorf("%s: the signature does not verify", name)
	}
	return nil
}

func readPublicKeyFile(path string) (*awskms.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pub, err := awskms.ParsePEM(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return pub, nil
}

// entry is one line of the manifest.
type entry struct {
	name   string
	digest [sha256.Size]byte
}

// readManifest parses dist/SHA256SUMS -- `sha256sum -c` format, two
// spaces -- and answers with its entries in the file's order and the
// bytes of the file, which are what the manifest's own signature is
// over.
//
// Strict about the shape, because a line this does not understand is a
// file that will not be signed, and a manifest an operator's
// `sha256sum -c` reads differently from this tool is two paths that must
// agree. A name with a path separator is refused: the manifest names
// files beside it, and a `../` here would be a signature written
// somewhere else.
func readManifest(dist string) ([]entry, []byte, error) {
	path := filepath.Join(dist, manifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("%w; run make dist first", err)
	}
	var entries []entry
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if line == "" {
			return nil, nil, fmt.Errorf("%s:%d: empty line", path, n)
		}
		sum, name, ok := strings.Cut(line, "  ")
		if !ok || len(sum) != sha256.Size*2 || name == "" {
			return nil, nil, fmt.Errorf("%s:%d: not `<sha256>  <name>`: %q", path, n, line)
		}
		raw, err := hex.DecodeString(sum)
		if err != nil {
			return nil, nil, fmt.Errorf("%s:%d: the digest is not hex: %q", path, n, sum)
		}
		if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
			return nil, nil, fmt.Errorf("%s:%d: %q is not a bare file name", path, n, name)
		}
		if name == manifestName || strings.HasSuffix(name, ".sig") {
			return nil, nil, fmt.Errorf("%s:%d: the manifest lists %s, which is written after it; run make dist again", path, n, name)
		}
		if seen[name] {
			return nil, nil, fmt.Errorf("%s:%d: %s is listed twice", path, n, name)
		}
		seen[name] = true
		var e entry
		copy(e.digest[:], raw)
		e.name = name
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil, nil, fmt.Errorf("%s lists nothing; an empty manifest would sign nothing and say it had", path)
	}
	return entries, data, nil
}

// checkFiles holds every file to its line before anything is signed,
// and reports all of the mismatches rather than the first.
func checkFiles(dist string, entries []entry) error {
	var problems []string
	for _, e := range entries {
		sum, err := fileDigest(filepath.Join(dist, e.name))
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if sum != e.digest {
			problems = append(problems, fmt.Sprintf("%s: digest %x, manifest says %x", e.name, sum, e.digest))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("refusing to sign: %d file(s) do not match %s:\n  %s",
			len(problems), manifestName, strings.Join(problems, "\n  "))
	}
	return nil
}

func fileDigest(path string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, fmt.Errorf("%s: %w", path, err)
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
