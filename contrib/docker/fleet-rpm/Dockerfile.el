# The image the RHEL row's modules are driven in, against the real tools.
#
# # Why this exists
#
# `rpm`, `chattr`, `authselect`, `dnf_module` and the `pkg` module's dnf
# provider were written against two throwaway lab hosts, Rocky Linux 9.8
# and AlmaLinux 8.10, and driven there by hand (DIVERGENCE 5.172-5.174,
# 5.157). That is evidence, and it is the kind that decays: a run that
# happened once in the lab is not a run that happens again when the
# module changes. GitHub offers no RHEL runner, so the same real tools
# are run here, in a container on an Ubuntu runner, the way the `debian`
# leg runs dpkg (DIVERGENCE 5.35 and 5.178).
#
# One Dockerfile for both EL releases, chosen by BASE, because what
# differs between them is exactly what the tests are for: rpm 4.14 and
# 4.16, dnf 4.7 and 4.14, e2fsprogs 1.45 and 1.46. What this file does
# to either is the same.
#
# # This one reaches the network, and the debian image does not
#
# The debian image runs `--network none` and bakes everything in. That
# cannot hold here and is not pretended: `dnf_module` enables real
# module streams and installs redis from AppStream, and the conformance
# harness installs and removes `tree` through the dnf provider. Those
# read the distribution's own mirrors at run time, because a mirror
# frozen into the image would test dnf against a repository nobody
# serves. So a mirror outage can turn this leg red for reasons that are
# not the change under test; that is the price of driving the real
# package manager, and the leg's log names the repositories it used.
#
# # What it cannot drive
#
# `firewalld` needs a running daemon on a system bus with the kernel's
# netfilter behind it, which a container has none of; SELinux is the
# host kernel's, and the runner's is Ubuntu's AppArmor. Neither is
# attempted here, and fleet.yml says so where the legs are defined.

# The toolchain is taken from the same image, at the same tag, that the
# debian leg's image starts from -- which internal/buildpolicy holds to
# go.mod's `toolchain` directive, for this file as for that one. Go is a
# static binary with no dependency on the distribution it was built on,
# so copying /usr/local/go into an EL image is the whole of installing
# it, and the official image already verified the tarball's checksum.
# A second download from go.dev here would be a second digest to keep
# in step with contrib/tofu's, for nothing.
#
# BASE is declared before the first FROM because that is the only place
# an ARG can reach a FROM line from.
ARG BASE=rockylinux/rockylinux:9
FROM golang:1.26.6-bookworm AS toolchain

FROM ${BASE}

COPY --from=toolchain /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH

# What the tests drive, and what they check it with:
#
#   authselect        the module under test; not in the container base
#   e2fsprogs         chattr and lsattr, for `chattr`
#   which             its doc file is what the rpm.verify test moves away
#   coreutils-common  owns /etc/DIR_COLORS.lightbgcolor, the config file
#                     the same test appends to
#
# `tsflags=` clears the container image's `nodocs`, and the reinstall
# is what makes that take effect for `which`, which the base already
# has: without its NEWS file on disk the verify test skips, and a leg
# that skips is not a leg that ran.
RUN dnf -y install authselect e2fsprogs which coreutils-common \
    && dnf -y --setopt=tsflags= reinstall which \
    && test -f /usr/share/doc/which/NEWS \
    && test -f /etc/DIR_COLORS.lightbgcolor \
    && dnf clean all

# `local`: the image carries the pinned toolchain and nothing should
# fetch another. CGO off, because there is no C compiler here and the
# release build does not use one either.
ENV GOTOOLCHAIN=local
ENV CGO_ENABLED=0
ENV GOCACHE=/gocache

COPY run.sh /usr/local/bin/run.sh
RUN chmod +x /usr/local/bin/run.sh
ENTRYPOINT ["/usr/local/bin/run.sh"]
