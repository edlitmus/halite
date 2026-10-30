#!/bin/sh
# verify-archives.sh TAG [REPO]
#
# Downloads every archive of a published release and, for each one:
#   - checks its SHA-256 against the release's SHA256SUMS,
#   - verifies its build attestation, pinned to release.yml at that tag
#     (without the pins, `gh attestation verify` accepts any workflow or
#     any ref of the repository),
#   - checks the top level holds exactly the layout of handbook chapter 30,
#   - prints what `file` says the first binary is, for a person to read
#     against the platform in the archive's name.
#
# Exits non-zero if any archive fails any of the first three. The fourth
# is shown and not judged: `file` words ELF, Mach-O and PE differently on
# every host that has it, and a wrong-platform binary is seen in one
# glance at the table where a pattern for it would be a guess.
#
# Run it against a release that exists, with `gh` signed in. It is not
# part of `make check`: it downloads about 360 MiB. Issue 307.
set -u
tag=${1:?usage: verify-archives.sh TAG [REPO]}
repo=${2:-edlitmus/halite}
v=${tag#v}
layout="EVIDENCE.md LICENSE bin examples handbook man service"

work=$(mktemp -d) || exit 1
trap 'rm -rf "$work"' EXIT INT TERM
mkdir "$work/dl" "$work/x"

gh release download "$tag" --repo "$repo" --dir "$work/dl" \
	--pattern "halite-$v-*.tar.gz" --pattern "halite-$v-*.zip" --pattern SHA256SUMS ||
	{ echo "could not download $tag of $repo" >&2; exit 1; }

want=$(for w in $layout; do echo "$w"; done | sort | tr '\n' ' ')
fail=0 count=0
for a in "$work"/dl/halite-"$v"-*.tar.gz "$work"/dl/halite-"$v"-*.zip; do
	[ -e "$a" ] || continue
	count=$((count + 1))
	n=$(basename "$a"); b=${n%.tar.gz}; b=${b%.zip}

	wantsum=$(grep " $n\$" "$work/dl/SHA256SUMS" | cut -d' ' -f1)
	gotsum=$(sha256sum "$a" 2>/dev/null | cut -d' ' -f1)
	[ -n "$wantsum" ] && [ "$wantsum" = "$gotsum" ] && sum=ok || { sum=FAIL; fail=1; }

	if gh attestation verify "$a" --repo "$repo" \
		--signer-workflow "$repo/.github/workflows/release.yml" \
		--source-ref "refs/tags/$tag" >/dev/null 2>&1; then att=ok; else att=FAIL; fail=1; fi

	mkdir "$work/x/$b"
	case $n in
	*.zip) unzip -q "$a" -d "$work/x/$b" ;;
	*) tar -xzf "$a" -C "$work/x/$b" ;;
	esac
	got=$(ls "$work/x/$b/$b" 2>/dev/null | sort | tr '\n' ' ')
	if [ "$got" = "$want" ]; then lay=ok; else lay="FAIL[$got]"; fail=1; fi

	first=$(ls "$work/x/$b/$b/bin" 2>/dev/null | head -1)
	what=$(file -b "$work/x/$b/$b/bin/$first" 2>/dev/null | cut -c1-70)
	printf '%-40s sum=%s att=%s layout=%s  %s: %s\n' "$n" "$sum" "$att" "$lay" "$first" "$what"
done

[ "$count" -gt 0 ] || { echo "no archives found in $tag" >&2; exit 1; }
echo "$count archive(s) checked"
exit $fail
