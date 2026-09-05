#!/bin/bash
# The zpool probe, run inside a node that has ZFS.
#
# It exercises the module against real pools on file vdevs, and asserts
# rather than only printing: a probe whose output has to be read by a
# person is one nobody runs twice.
exec 2>&1
echo "=== HALITE-PROBE-START ==="

fail() { echo "PROBE-FAILED: $*"; }
want() { # want <description> <expected substring> <actual>
	case "$3" in
	*"$2"*) echo "  ok: $1" ;;
	*) fail "$1: wanted %$2% in: $3" ;;
	esac
}
wantnot() {
	case "$3" in
	*"$2"*) fail "$1: did not want %$2% in: $3" ;;
	*) echo "  ok: $1" ;;
	esac
}

modprobe zfs || { apt-get install -y -qq "linux-modules-extra-$(uname -r)" >/dev/null 2>&1; modprobe zfs; }
zfs version | head -2

HN=/root/halite-node
cp /payload/halite-node $HN && chmod +x $HN
mkdir -p /vdevs /root/sls
rm -f /vdevs/*
for d in a b c d e f g; do truncate -s 256M /vdevs/$d; done
printf "base:\n  '*':\n    - z\n" > /root/sls/top.sls
apply() { $HN state apply --local --file-root /root/sls "$@" 2>&1; }

echo "--- the listing is read the way ZFS writes it ---"
# Every shape at once: two mirrors, a log, a cache and a spare. The
# section headers are the part that ignores -H, and the part a reader
# that assumed otherwise got wrong.
zpool create tank mirror /vdevs/a /vdevs/b mirror /vdevs/c /vdevs/d \
	log /vdevs/e cache /vdevs/f spare /vdevs/g
read_back=$($HN call zpool.vdevs zpool=tank --local --out json)
want "the two mirrors are two vdevs" '"type":"mirror","devices":["/vdevs/a","/vdevs/b"]' "$read_back"
want "the log is its own vdev"       '"type":"log","devices":["/vdevs/e"]' "$read_back"
want "the cache is its own vdev"     '"type":"cache","devices":["/vdevs/f"]' "$read_back"
want "the spare is its own vdev"     '"type":"spare","devices":["/vdevs/g"]' "$read_back"
wantnot "no device is filed under the wrong vdev" \
	'"devices":["/vdevs/a","/vdevs/b","/vdevs/e"]' "$read_back"
zpool destroy tank

echo "--- a stripe is several vdevs of one, not one of several ---"
zpool create tank /vdevs/a /vdevs/b /vdevs/c
want "three top-level devices" '[{"type":"","devices":["/vdevs/a"]},{"type":"","devices":["/vdevs/b"]},{"type":"","devices":["/vdevs/c"]}]' \
	"$($HN call zpool.vdevs zpool=tank --local --out json)"
zpool destroy tank

echo "--- present creates, and the run after it changes nothing ---"
cat > /root/sls/z.sls <<'SLS'
tank:
  zpool.present:
    - layout:
        - mirror:
            - /vdevs/a
            - /vdevs/b
        - mirror:
            - /vdevs/c
            - /vdevs/d
    - properties:
        ashift: '12'
    - import: false
SLS
want "test mode says what it would do" "would be created from mirror of 2, mirror of 2" "$(apply --test)"
wantnot "test mode created nothing" "tank" "$(zpool list -H -o name)"
want "the pool is created" "was created from mirror of 2, mirror of 2" "$(apply)"
want "ashift really took" "12" "$(zpool get -H -p -o value ashift tank)"
second=$(apply)
want "the second run is satisfied" "exists with the requested properties" "$second"
wantnot "and warns about nothing" "Warning" "$second"

echo "--- a layout that does not match is reported and not acted on ---"
cat > /root/sls/z.sls <<'SLS'
tank:
  zpool.present:
    - layout:
        mirror:
          - /vdevs/a
          - /vdevs/b
SLS
out=$(apply)
want "the difference is named" "is mirror of 2, mirror of 2 and the declaration asks for mirror of 2" "$out"
want "and refused" "does not reshape a pool that exists" "$out"
want "the pool still has both mirrors" "mirror-1" "$(zpool list -v -H -p tank)"

echo "--- a property is set, and then it is not ---"
cat > /root/sls/z.sls <<'SLS'
tank:
  zpool.present:
    - properties:
        comment: managed-by-halite
SLS
want "the property is set" "properties comment of tank were set" "$(apply)"
want "and stays set" "exists with the requested properties" "$(apply)"
want "the pool really carries it" "managed-by-halite" "$(zpool get -H -p -o value comment tank)"

echo "--- absent exports, and present imports rather than creating over ---"
zfs create tank/data && dd if=/dev/urandom of=/tank/data/file bs=1M count=8 status=none
sum_before=$(md5sum /tank/data/file | cut -d' ' -f1)
cat > /root/sls/z.sls <<'SLS'
tank:
  zpool.absent: []
SLS
want "the pool is exported" "was exported" "$(apply)"
wantnot "and is gone from this node" "tank" "$(zpool list -H -o name)"
cat > /root/sls/z.sls <<'SLS'
tank:
  zpool.present:
    - layout:
        mirror:
          - /vdevs/a
          - /vdevs/b
    - device_dir: /vdevs
SLS
want "the pool comes back by import" "was imported" "$(apply)"
want "with its data intact" "$sum_before" "$(md5sum /tank/data/file | cut -d' ' -f1)"

echo "--- absent destroys only when told to ---"
cat > /root/sls/z.sls <<'SLS'
tank:
  zpool.absent:
    - export: false
SLS
want "the pool is destroyed" "was destroyed" "$(apply)"
want "and the run after that is satisfied" "is not attached to this node" "$(apply)"

echo "=== HALITE-PROBE-END ==="
