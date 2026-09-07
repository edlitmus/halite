# The image the Debian row's modules are driven in, against the real tools.
#
# # Why this exists
#
# `dpkg`, `debconf`, `pkgrepo` and `timezone` change a machine as root,
# and every one of them worked by running another program and reading
# what it said back — with the output in their tests written from a
# manual page rather than captured from the program. DIVERGENCE 5.33
# records what that is worth: a fixture written by whoever wrote the
# parser agrees with the parser, and `pf` matched no rule at all on a
# real host while its idempotence test passed for exactly that reason.
#
# A container running the real `dpkg-query` is not a stand-in for it.
# It is the same binary a Debian node runs, reading the same package
# database format, and the mutations here are real: this image's own
# `dpkg --set-selections` database is changed, its own debconf database
# is written, its own `/etc/apt/sources.list.d` gains and loses a file,
# and its own `/etc/localtime` is relinked. What it is *not* is a
# machine anybody depends on, which is what makes that acceptable.
#
# # It does not touch the network at test time
#
# Every package and every repository the tests need is baked in here, and
# `run.sh` asserts it. That is not tidiness: this project builds with
# GOPROXY=off from a vendored tree, and a check that reaches
# archive.ubuntu.com goes red for reasons unrelated to the change under
# test. A gate people learn to ignore is worse than no gate, and one that
# depends on somebody else's mirror becomes that gate within a month.
#
# So the image build is allowed the network and the run is not.
FROM golang:1.25-bookworm

# The tools under test, and the two that build the local repository.
#
#   dpkg-dev      dpkg-scanpackages, to make an apt repository offline
#   debconf-utils debconf-set-selections and debconf-get-selections
#   tzdata        the zone files timezone.set_zone links to
#   apt-utils     apt-ftparchive, and what apt expects of a full install
#
# `debconf` and `dpkg` themselves come with the base image, which is the
# point: they are Debian's own, at Debian's own version, not something
# installed to make a test pass.
RUN apt-get update \
    && apt-get install --no-install-recommends -y \
        dpkg-dev debconf-utils tzdata apt-utils \
    && rm -rf /var/lib/apt/lists/*

# A real .deb on disk, for `dpkg.bin_pkg_info` — which reads a package
# file rather than the database, and is the one function here that cannot
# be exercised against what is already installed.
#
# `hello` is chosen because it is tiny, has no dependencies, and is in
# Debian's own archive rather than anywhere this project controls.
RUN mkdir -p /srv/halite-fleet/pkgs /etc/apt/keyrings \
    && cd /srv/halite-fleet/pkgs \
    && apt-get update \
    && apt-get download hello \
    && rm -rf /var/lib/apt/lists/*

# A local apt repository, so that `pkgrepo.mod_repo` with a refresh is
# exercised rather than skipped.
#
# Writing a file into sources.list.d proves the module can write a file.
# What it does not prove is that apt can *read* what it wrote, and that
# is the half worth having: a repository line whose fields are in the
# wrong order produces a file that looks right and an `apt-get update`
# that fails. The repository is flat and served from the filesystem, so
# the refresh is real and reaches no network.
#
# It is signed, and that is the point rather than a detail. apt refuses
# an unsigned repository, and the way round that is `[trusted=yes]` -- a
# field this module does not offer. The field it does offer is
# `signedby`, which names a keyring, and that is what an operator
# actually configures. So the repository is signed with a key generated
# here and the test names that keyring, which exercises the field
# instead of stepping past it.
#
# `apt-ftparchive` writes the Release; gpg signs it in place as
# InRelease, which is the form apt prefers and the only one a flat
# repository needs.
RUN cd /srv/halite-fleet/pkgs \
    && dpkg-scanpackages . /dev/null > Packages \
    && gzip -9c Packages > Packages.gz \
    && apt-ftparchive release . > Release \
    && export GNUPGHOME=/srv/halite-fleet/gnupg \
    && mkdir -p "$GNUPGHOME" && chmod 700 "$GNUPGHOME" \
    && gpg --batch --quick-generate-key --passphrase "" \
        "halite fleet check <fleet@halite.invalid>" default default never \
    && gpg --batch --yes --clearsign -o InRelease Release \
    && gpg --batch --yes --export --output /etc/apt/keyrings/halite-fleet.gpg \
    && rm -f Release

# The tests run as root, deliberately, and this is the opposite of the
# race image's decision.
#
# There, root was wrong: CAP_DAC_OVERRIDE means a test asserting a
# refusal on an unwritable directory cannot create the condition it
# tests for. Here root is the condition. Every module in this image
# declares that it needs root, and running them as anybody else would
# exercise the privilege check rather than the tool.
ENV GOTOOLCHAIN=auto
ENV GOCACHE=/gocache
ENV GOMODCACHE=/gomodcache

# GOFLAGS unset, so vendor/ resolves the same way it does on a host and
# in the release build. The race image says why at length.

COPY run.sh /usr/local/bin/run.sh
RUN chmod +x /usr/local/bin/run.sh
ENTRYPOINT ["/usr/local/bin/run.sh"]
