#!/bin/sh
# Refresh internal/yaml/testdata/yaml-test-suite from upstream.
#
# The suite is test data, not a dependency: nothing in it is imported, so
# the zero-dependency property of SPEC section 4.2 is untouched and
# `go list -m all` still returns only this module.
#
# Usage: tools/vendor-yaml-test-suite.sh [commit-ish]
set -eu

REPO="https://github.com/yaml/yaml-test-suite"
BRANCH="${1:-data}"
DEST="internal/yaml/testdata/yaml-test-suite"

[ -d .git ] || { echo "run this from the repository root" >&2; exit 1; }
command -v git >/dev/null || { echo "git is required" >&2; exit 1; }
command -v python3 >/dev/null || { echo "python3 is required" >&2; exit 1; }

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT HUP TERM

echo "cloning $REPO ($BRANCH)"
git clone -q --depth 1 --branch "$BRANCH" "$REPO" "$TMP/yts"
SHA=$(git -C "$TMP/yts" rev-parse HEAD)

mkdir -p "$DEST"
python3 - "$TMP/yts" "$DEST/cases.json" <<'PY'
import json, os, sys

root, out = sys.argv[1], sys.argv[2]
cases = []
for top in sorted(os.listdir(root)):
    p = os.path.join(root, top)
    # `name` and `tags` are index directories of symlinks, not tests.
    if not os.path.isdir(p) or top.startswith('.') or top in ('name', 'tags'):
        continue
    subs = sorted(x for x in os.listdir(p) if os.path.isdir(os.path.join(p, x)))
    leaves = [(top + '/' + s, os.path.join(p, s)) for s in subs] or [(top, p)]
    for cid, d in leaves:
        inyaml = os.path.join(d, 'in.yaml')
        if not os.path.exists(inyaml):
            continue
        c = {"id": cid}
        with open(os.path.join(d, '===')) as f:
            c["name"] = f.read().strip()
        with open(inyaml, 'rb') as f:
            c["yaml"] = f.read().decode('utf-8')
        if os.path.exists(os.path.join(d, 'error')):
            c["error"] = True
        ij = os.path.join(d, 'in.json')
        if os.path.exists(ij):
            with open(ij) as f:
                c["json"] = f.read()
        cases.append(c)

with open(out, 'w') as f:
    json.dump(cases, f, indent=1, ensure_ascii=False, sort_keys=True)
    f.write("\n")
print("wrote %d cases to %s" % (len(cases), out))
PY

{
	echo "The YAML Test Suite, vendored."
	echo
	echo "Source:  $REPO"
	echo "Branch:  $BRANCH"
	echo "Commit:  $SHA"
	echo "Fetched: $(date -u +%Y-%m-%d)"
	echo
	echo "cases.json is a mechanical repacking of that commit's per-test"
	echo "directories into one file: the test id, its name, the exact bytes of"
	echo "in.yaml, whether the case is expected to fail, and in.json where the"
	echo "suite provides one. The bytes are unchanged; JSON string escaping is"
	echo "used so that trailing whitespace, tabs, and a missing final newline"
	echo "survive being stored, since in YAML all three change the meaning."
	echo
	echo "Regenerate with tools/vendor-yaml-test-suite.sh."
	echo
	echo "----------------------------------------------------------------"
	echo
	cat "$TMP/yts/License" 2>/dev/null ||
		git -C "$TMP/yts" show main:License 2>/dev/null ||
		echo "MIT License. See $REPO."
} > "$DEST/LICENSE"

echo "vendored $SHA"
echo "now run: go test ./internal/yaml/ -run TestSuite"
