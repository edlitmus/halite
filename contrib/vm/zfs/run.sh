#!/bin/bash
# Boot an Ubuntu node with ZFS and run the zpool probe on it.
#
# /src is the checkout, /vm is a cache directory that keeps the cloud
# image and the built disk between runs. The first run downloads about
# 600MB and installs zfsutils inside the guest; later runs reuse the disk
# and take under a minute.
set -euo pipefail

IMAGE_URL="${IMAGE_URL:-https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-amd64.img}"
cd /vm

[ -f base.img ] || curl -fsSL -o base.img "$IMAGE_URL"
if [ ! -f work.img ]; then
	qemu-img create -f qcow2 -F qcow2 -b base.img work.img 12G >/dev/null
fi

# The payload is the binary under test and the probe that drives it,
# handed to the guest as a read-only ISO so that nothing has to be
# mounted on the host.
rm -rf payload && mkdir -p payload
( cd /src && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /vm/payload/halite-node ./cmd/halite-node )
cp /src/contrib/vm/zfs/probe.sh payload/probe.sh
genisoimage -quiet -o payload.iso -V HALITE -J -r payload/

# A fresh instance-id each run, so cloud-init treats a reused disk as a
# new node and runs the probe again rather than skipping it.
printf 'instance-id: halite-zfs-%s\nlocal-hostname: zfsprobe\n' "$(date +%s)" > meta-data
cat > user-data <<'UD'
#cloud-config
package_update: true
packages:
  - zfsutils-linux
runcmd:
  - [ sh, -c, "mkdir -p /payload && mount -L HALITE /payload" ]
  - [ sh, -c, "bash /payload/probe.sh > /dev/ttyS0 2>&1" ]
  - [ sh, -c, "poweroff" ]
UD
cloud-localds seed.iso user-data meta-data

ACCEL=(-enable-kvm -cpu host)
if [ ! -e /dev/kvm ]; then
	echo "zfscheck: no /dev/kvm; emulating the processor, which is slow" >&2
	ACCEL=()
fi

rm -f console.log
timeout "${ZFSCHECK_TIMEOUT:-1800}" qemu-system-x86_64 \
	"${ACCEL[@]}" -smp 4 -m 4096 \
	-drive file=work.img,if=virtio,format=qcow2 \
	-drive file=seed.iso,if=virtio,format=raw \
	-drive file=payload.iso,if=virtio,format=raw \
	-netdev user,id=n0 -device virtio-net-pci,netdev=n0 \
	-nographic -serial file:/vm/console.log -monitor none -display none || true

sed -n '/HALITE-PROBE-START/,/HALITE-PROBE-END/p' console.log | tr -d '\r'
if ! grep -q 'HALITE-PROBE-END' console.log; then
	echo "zfscheck: the probe did not finish; the whole console is in /vm/console.log" >&2
	exit 1
fi
grep -q 'PROBE-FAILED' console.log && exit 1
exit 0
