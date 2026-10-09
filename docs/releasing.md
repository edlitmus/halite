# Releasing

How a tag becomes a release, what each step proves, how an operator
checks a download, and how the detached signatures SPEC 4.3 asks for are
made. The first three are what `.github/workflows/release.yml` does
today. The fourth is a tool that exists and a step that has not yet been
taken, and this page says which is which.

## What a tag does

Pushing a `v*` tag runs `release.yml`, five jobs in a line, each one a
precondition of the next.

1. **The release gate.** `make release-gate` refuses to go on while any
   module that changes a machine as root is still at the *assumed*
   evidence level, where nobody has run it against the tool it drives.
   `make release-gate` locally says what the gate will say.
2. **Two builders.** `make dist` runs on two different runner images,
   `ubuntu-24.04` and `ubuntu-22.04`, with the pinned toolchain, vendored
   and offline. Each cross-compiles the three binaries for every platform
   in SPEC 27.1, assembles one archive per platform with `tools/disttar`,
   and writes `dist/SHA256SUMS` over the lot: 64 lines, 48 binaries and 16
   archives. Each builder's `dist/` is kept as a workflow artifact for
   three days.
3. **Compare.** The two manifests must be byte-identical, and each must
   list at least 64 artifacts so that two empty manifests cannot agree.
   This is the reproducibility property SPEC 4.3 asks for, and the one
   thing a single machine cannot prove about itself.
4. **Attest.** Keyless SLSA build provenance, signed through Sigstore
   with the workflow's own GitHub identity, over exactly the digests the
   two builders agreed on. There is no long-lived key anywhere in this
   step. The gate must have passed for this job to run at all.
5. **Publish.** The archives and `SHA256SUMS` from the first builder are
   checked against the compared manifest once more, then
   `gh release create` publishes them. A `v0.*` tag, or any tag with a
   suffix such as `-rc1`, is marked a pre-release.

A manual run of the workflow, from the Actions tab, does everything but
publish. It is how the build and the compare are exercised without
minting a tag, which is not free: a tag is fetched by other people and
changes what `git describe` reports for everything after it.

Before tagging, `make check` should be green and `main` should be what
you are tagging. The version is read from the tag by `git describe`; the
tag `v0.1.0` stamps the binaries `0.1.0` and names the archives
`halite-0.1.0-<os>-<arch>`.

## Checking a download

Three things can be checked, each independent of the others.

The digest, against the published manifest:

```sh
sha256sum --ignore-missing -c SHA256SUMS
```

The provenance, which needs the network and Sigstore's trust root:

```sh
gh attestation verify halite-0.1.0-linux-amd64.tar.gz --repo edlitmus/halite
```

And, once a release carries them, the detached signatures, which need
nothing but the public key and an `openssl`:

```sh
openssl dgst -sha256 -verify halite-release.pub -signature SHA256SUMS.sig SHA256SUMS
openssl dgst -sha256 -verify halite-release.pub -signature halite-0.1.0-linux-amd64.tar.gz.sig halite-0.1.0-linux-amd64.tar.gz
```

The manifest's signature covers every line in it, so checking it and
then running `sha256sum -c` checks every file; the per-file signatures
are for a site that fetched one archive and not the manifest.

## Signing a release

**No release is signed yet, and no key exists.** What follows is the
tool and the procedure. The procedure has not been run, because its first
step is a key that has not been created, and nothing here claims
otherwise. When it has been run, this section will say which release,
with which key, and what was not covered.

### Why the key is in KMS, and why a person signs

SPEC 4.3 deferred the detached signature on one question: where the key
lives, given that a long-lived key does not belong on a hosted runner. An
asymmetric key in AWS KMS answers it by not living anywhere a process can
read. The private half is generated inside the service's HSMs and never
leaves them; what a caller holds is permission to ask for a signature
over a digest, and every such request is a CloudTrail record naming who
asked and when.

The workflow does not sign. The attestation already says "this workflow
built these bytes", and a signature the workflow could make would say the
same thing again with a different key. What a signature can add is that
a person with the key looked first: that the gate passed, the two
builders agreed, and the thing being signed is the thing that was
attested. So the signing step is an operator at a workstation, with
credentials that needed a person to obtain them, and the tool refuses to
sign anything it has not checked.

### Setting up the key, once

An `ECC_NIST_P256` key with usage `SIGN_VERIFY`. KMS pairs that curve
with `ECDSA_SHA_256`, which is what a SHA-256 manifest needs; a P-384
key signs only SHA-384 digests and the tool refuses one by name.

```sh
aws kms create-key --key-spec ECC_NIST_P256 --key-usage SIGN_VERIFY \
    --description 'halite release signing' --tags TagKey=project,TagValue=halite
aws kms create-alias --alias-name alias/halite-release --target-key-id <the KeyId it printed>
```

The key policy should grant `kms:Sign` to the one principal that
releases and `kms:GetPublicKey` to anyone who needs to export the public
half. Asymmetric KMS keys do not rotate in place: a rotation is a new
key, a new public key file, a note in `docs/DIVERGENCE.md`, and the old
public key kept so old releases still verify.

Then the public half, committed so that every operator verifies against
the same bytes:

```sh
make release-pubkey > contrib/keys/halite-release.pub
```

It prints the key's ARN and fingerprint on standard error. `release-sign`
holds the KMS key to this file byte for byte and refuses any other key,
because a release signed with a key whose public half is not the
committed one is a release nobody can check.

### Signing one release

Credentials come from the environment. On a workstation that is
`aws sso login`, then:

```sh
eval "$(aws configure export-credentials --format env)"
export AWS_REGION=us-east-1       # the key's region
```

The set to sign is the full `dist/` the attestation names, not the
release's assets: `SHA256SUMS` lists the 48 bare binaries as well as the
16 archives, and the tool signs what the manifest lists or nothing.
Within three days of the run, the first builder's `dist/` is a workflow
artifact:

```sh
gh run download <run id> --repo edlitmus/halite -n dist-ubuntu-24.04 -D dist
gh attestation verify dist/halite-0.1.0-linux-amd64.tar.gz --repo edlitmus/halite
```

The second line is the operator's own check that what was downloaded is
what was attested; do it for more than one file if you like, since
every digest in the manifest is a subject of the same attestation. The
alternative, after three days or as a stronger check, is to be a third
builder: `make dist` on the tagged commit, then `cmp dist/SHA256SUMS`
against the manifest in the release. Whether a Mac reproduces the two
Linux builders' digests byte for byte has not been demonstrated, and on
a Mac `make` must be `bmake` or `gmake`.

Then:

```sh
make release-sign
make release-verify
gh release upload v0.1.0 dist/*.sig --repo edlitmus/halite
```

`release-sign` checks, before its first call to KMS, that every file
matches its line in the manifest, that no `.sig` exists yet, and that the
KMS key is the committed public key. It then makes one `Sign` call per
line and one for the manifest, verifies each answer against the public
key before writing it, and writes `<name>.sig` beside each file, DER.
`release-verify` reads the set back with the public key and no
credentials, and lists every problem rather than the first.

Today the release is published live by the workflow and the signatures
are added afterwards, so there is a window in which a release exists
unsigned. Having the workflow publish a draft that `release-sign`
finishes is the next change, and not this one.

### What is and is not covered

`internal/awskms` has been run against a fake written from the KMS API
reference, which holds it to the documentation and not to the service.
`TestLiveKMSSignsADigestThisBuildVerifies` holds it to the service, and
to `openssl`, with a real key:

```sh
HALITE_KMS_LIVE=1 HALITE_KMS_KEY=alias/halite-release AWS_REGION=us-east-1 \
    go test -count=1 -v -run TestLiveKMS ./internal/awskms
```

It makes one billed `Sign` call per run. It is not in the fleet
workflow, which carries no AWS credentials, and it has not yet been run.
Until it has, this page describes the tool as written rather than as
demonstrated.
