# The image that boots a Linux node with ZFS on it, for `make zfscheck`.
#
# ZFS is a kernel module and there is no userspace stand-in for it, so the
# zpool module cannot be verified in a container: the container shares the
# host's kernel, and neither Docker Desktop's WSL2 kernel nor a macOS VM
# kernel has zfs.ko. A virtual machine with its own kernel is the smallest
# thing that can run the code being tested against the thing it drives.
#
# It needs /dev/kvm. Without it qemu falls back to emulating the processor,
# which works and takes about twenty times as long.
# The Go toolchain comes from the official image rather than from
# Ubuntu's archive, so that the binary the probe runs is built by the
# same version the rest of the project is. go.mod pins a newer toolchain
# than any base image ships, so GOTOOLCHAIN is left on auto to let it
# fetch one, as contrib/docker/saltdiff does.
FROM golang:1.25-bookworm AS toolchain

FROM ubuntu:24.04

COPY --from=toolchain /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH
ENV GOTOOLCHAIN=auto
ENV GOCACHE=/vm/gocache
ENV GOMODCACHE=/vm/gomodcache

RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      qemu-system-x86 qemu-utils cloud-image-utils genisoimage ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*

COPY run.sh /usr/local/bin/zfscheck
RUN chmod +x /usr/local/bin/zfscheck
ENTRYPOINT ["/usr/local/bin/zfscheck"]
