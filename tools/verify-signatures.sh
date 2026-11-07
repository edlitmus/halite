#!/bin/sh
# verify-signatures.sh TAG [REPO]
#
# Downloads a release as GitHub holds it -- a draft, for someone who can
# see drafts, or a published release -- and runs `relsign verify
# -published` over it with the committed public key: every archive and
# SBOM the manifest lists is present, matches its line and carries a
# signature that verifies; the manifest is signed; and nothing is in the
# release that the manifest does not list.
#
# Two callers, and they must ask the same question:
#   - `make release-publish`, before it makes a draft public;
#   - `.github/workflows/verify-release.yml`, after a release is made
#     public by any route, including the Publish button on GitHub's
#     release page, which never runs the Makefile.
# The second exists because nothing GitHub offers stops an account with
# write access from publishing a draft without the first (issue 311,
# DIVERGENCE 5.278). So the check runs once more after the fact, and a
# release published unsigned turns that run red. One script, so that the
# check before and the check after cannot drift apart.
#
# Needs `gh` (signed in, or GH_TOKEN) and Go; reads no AWS credentials.
set -u
tag=${1:?usage: verify-signatures.sh TAG [REPO]}
repo=${2:-edlitmus/halite}
pub=${RELEASE_PUB:-contrib/keys/halite-release.pub}

dir=$(mktemp -d) || exit 1
trap 'rm -rf "$dir"' EXIT INT TERM

gh release download "$tag" --repo "$repo" -D "$dir" ||
	{ echo "verify-signatures: could not download $tag of $repo" >&2; exit 1; }

env CGO_ENABLED=0 go run ./tools/relsign verify -published -dist "$dir" -pub "$pub"
