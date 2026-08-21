#!/bin/sh
# Refresh internal/template/testdata/jinja-corpus from upstream.
#
# The corpus is test data, not a dependency: nothing in it is imported, so
# the zero-dependency property of SPEC section 4.2 is untouched.
#
# Usage: tools/vendor-jinja-corpus.sh [commit-ish]
set -eu

REPO="https://github.com/pallets/jinja"
REF="${1:-main}"
DEST="internal/template/testdata/jinja-corpus"

[ -d .git ] || { echo "run this from the repository root" >&2; exit 1; }
command -v git >/dev/null || { echo "git is required" >&2; exit 1; }
command -v python3 >/dev/null || { echo "python3 is required" >&2; exit 1; }

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT HUP TERM

echo "cloning $REPO ($REF)"
git clone -q --depth 1 --branch "$REF" "$REPO" "$TMP/jinja"
SHA=$(git -C "$TMP/jinja" rev-parse HEAD)

mkdir -p "$DEST"
python3 tools/extract-jinja-cases.py "$TMP/jinja/tests" > "$DEST/cases.json"

{
	echo "Jinja's own test cases, extracted."
	echo
	echo "Source:  $REPO"
	echo "Commit:  $SHA"
	echo "Fetched: $(date -u +%Y-%m-%d)"
	echo
	echo "cases.json holds the triples that could be lifted out of Jinja's"
	echo "pytest suite without interpreting Python: a template built from a"
	echo "string literal by env.from_string, rendered with literal keyword"
	echo "arguments, and compared with == against a string literal. Anything"
	echo "using a fixture, a custom filter, an expected exception, or async is"
	echo "left behind, which is why a couple of hundred cases come out of"
	echo "8000 lines of tests."
	echo
	echo "SPEC section 31 asks for \"the Jinja project's own test cases where"
	echo "they apply to the supported subset\". Where they do not apply, the"
	echo "case still runs and its disagreement is recorded with the reason, in"
	echo "internal/template/corpus_test.go."
	echo
	echo "Regenerate with tools/vendor-jinja-corpus.sh."
	echo
	echo "----------------------------------------------------------------"
	echo
	cat "$TMP/jinja/LICENSE.txt"
} > "$DEST/LICENSE"

echo "vendored $SHA"
echo "now run: go test ./internal/template/ -run TestJinjaCorpus"
