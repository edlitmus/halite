#!/bin/sh
# check-tree.sh STATES PILLAR
#
# Compile a tree for every platform you run and every kind of node you have,
# without changing anything. Run it in CI on every change to the tree.
#
# The platform is chosen by overriding grains in a throwaway configuration
# file, which is how one Linux CI machine can compile the FreeBSD half of the
# tree. The node ID is chosen with --id, so the top file assigns the states
# that node would really get.
set -eu

states=$1
pillar=$2
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failed=0

for platform in Debian:Linux:Debian RedHat:Linux:RedHat FreeBSD:FreeBSD:FreeBSD; do
    os_family=${platform%%:*}
    rest=${platform#*:}
    kernel=${rest%%:*}
    os=${rest#*:}
    cfg="$tmp/$os_family.yaml"
    printf 'grains:\n  kernel: %s\n  os_family: %s\n  os: %s\n' \
        "$kernel" "$os_family" "$os" > "$cfg"

    for id in web1.example.com db1.example.com; do
        if halite-node state show_lowstate --local \
            --file-root "$states" --pillar-root "$pillar" \
            --root "$tmp" --config "$cfg" --id "$id" --out quiet 2> "$tmp/err"; then
            echo "ok    $os_family $id"
        else
            echo "FAIL  $os_family $id"
            sed 's/^/      /' "$tmp/err"
            failed=1
        fi
    done
done

exit "$failed"
