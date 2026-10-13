# Runbook: installing a hub

<!-- abstract: Standing up the control plane on Linux or FreeBSD: verify, install, configure, start, make yourself an operator, and prove it works. -->
<!-- kind: runbook -->
<!-- index: install hub; installation; halite-hub serve; hub.yaml; systemd unit; rc.d; useradd; pw useradd; make install; halite account; port 4510; --names; keys operator create; first policy; doctor; firewall -->

**Use this runbook to** bring up a new hub: the machine the fleet's nodes
connect to, serve files from, and take instructions through.

**You will end with** a running hub with an enrollment CA, the tree served
from a directory you control, one working operator, and a green
`halite-hub doctor`.

> **What has and has not been run.** The configuration, the CA creation, the
> operator and policy bootstrap, the enrolment of nodes, and every
> verification command in this runbook were run against a hub on a Linux
> workstation as an unprivileged user. **On FreeBSD** the whole runbook was
> then run as root on two fresh Vultr instances, FreeBSD 14.5-RELEASE-p1 and
> 15.1-RELEASE-p4 (amd64), from the published v0.1.1 `freebsd-amd64` archive:
> the account, the install, the rc.d script, first start, the operator and
> policy, `doctor`, a node on the other host, and a reboot. That run is why
> steps 1, 4 and 5 say what they say below, and it found a bug in the rc.d
> scripts (step 5). **Not run:** a fresh *Linux* host as root (`make install`,
> `useradd`, the systemd unit), and the hub on 14.5 (only the node was; the
> hub ran on 15.1).

## Before you start

You need:

- **A host**: Linux (amd64 or arm64) or FreeBSD (14 or 15). The hub needs no
  root privilege at run time, and little capacity: a hub for hundreds of
  nodes is comfortable on two cores and a couple of gigabytes
  ([chapter 38](38-scaling.md) has the sizing).
- **A stable name** nodes will use: `hub.example.com`, in DNS. Nodes verify
  the hub's certificate against the name they dialled, so it must be the same
  name you put on the certificate in step 5.
- **Network**: inbound **TCP 4510** from every node (and from wherever
  operators and the API run). Nothing else inbound. Outbound as the tree needs
  (git, the secrets manager).
- **Time**: working NTP. Certificates have validity dates.
- **The tree**: a directory of states and pillar (the shape of
  [chapter 10](10-top-and-files.md)). It can be empty for now; the example
  trees ship in `examples/`.
- **The release archive** for the hub's platform, with its `SHA256SUMS`.
- About 20 minutes.

Decide now, because they are awkward to change later:

| Decision | Recommended |
|---|---|
| Where `ca.key` lives | The default (`/etc/halite/pki`, mode `0700`, owned by the `halite` account) and a backup ([chapter 35](35-backup-and-recovery.md)); a KMS if you need more |
| Enrollment mode | `manual` to begin with |
| Where the tree lives | A git repository served by gitfs with signed refs, in time; a plain directory today |

## Procedure

Linux commands first; FreeBSD differences are in each step.

### 1. Verify what you downloaded

```sh
v=0.1.0; p=linux-amd64        # the release and the platform: linux-arm64, freebsd-amd64, …
sha256sum --ignore-missing -c SHA256SUMS
gh attestation verify halite-$v-$p.tar.gz --repo edlitmus/halite
openssl dgst -sha256 -verify halite-release.pub -signature SHA256SUMS.sig SHA256SUMS
```

The first checks the file against the manifest. The second checks the
provenance (that this workflow built it from this repository). The third
checks the maintainers' detached signature, using the public key from the
repository (`contrib/keys/halite-release.pub`, fingerprint in
`docs/releasing.md`). FreeBSD 14.5 and 15.1 have `sha256sum` in the base system
(`/sbin/sha256sum`) and the command above works unchanged there; `sha256 -c`
also works, and `openssl` is in the base too. Do not skip this on the machine that holds your CA.

```sh
tar -xzf halite-$v-$p.tar.gz
cd halite-$v-$p
ls
```

```
EVIDENCE.md  LICENSE  bin/  examples/  handbook/  man/  service/
```

That is the layout of every release after v0.1.0. **v0.1.0 itself holds only
`bin/`, `examples/` and `man/man8/`** (checked by listing the published
archive): it was built before the handbook and the service files were added to
the archives. With it, take the service files from `contrib/systemd/` and
`contrib/rc.d/` in a checkout of the `v0.1.0` tag (or `make install-service`
there), and read the handbook from the checkout's `docs/handbook/`.

Releases also publish a separate `halite-$v-linux-amd64-fips.tar.gz` (and
`linux-arm64`) holding the `-fips` binaries; see
[chapter 28](28-hardening.md#fips-builds). You do not need it unless your
estate requires FIPS 140-3.

### 2. Create the account

The hub never needs root.

```sh
# Linux
sudo useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin halite
# FreeBSD
sudo pw useradd halite -d /nonexistent -s /usr/sbin/nologin
```

### 3. Install the programs and directories

If you have a source checkout, `make build` as yourself and `sudo make
install` does all of this and names anything it could not do. From an
archive, the equivalent is:

```sh
# paths: Linux /etc/halite, /var/lib/halite; FreeBSD /usr/local/etc/halite, /var/db/halite
sudo install -m 0755 bin/halite-hub bin/halite-node bin/halite-api /usr/local/bin/
sudo install -d -o halite -m 0755 /etc/halite
sudo install -d -o halite -m 0700 /etc/halite/pki /var/lib/halite /var/cache/halite
sudo install -d -o halite -m 0750 /var/log/halite
sudo install -d -m 0755 /usr/local/share/man/man8
sudo install -m 0644 man/man8/*.8 /usr/local/share/man/man8/
```

Binaries go to `/usr/local/bin` on every platform, because that is the path
written into the service files. The directories are created **owned by the
`halite` account**, which is the point: a directory made by running a command
as root is one the service account cannot use afterwards, and the symptom
names neither the directory nor the account. Nothing is written into the
configuration directory yet, and nothing is started.

### 4. Write the configuration

Start from the example and change only what you must:

```sh
sudo install -o halite -m 0640 examples/hub.yaml /etc/halite/hub.yaml
```

```yaml
# /etc/halite/hub.yaml  (the minimum that matters)
listen: :4510
file_roots:
  base:
    - /srv/halite/states
pillar_roots:
  base:
    - /srv/halite/pillar
enrollment_mode: manual
job_cache_retention: 720h
event_retention: 720h
metrics: true
```

(`pki_dir`, `state_dir`, `cache_dir` and `policy` have correct platform
defaults, the last being `policy.yaml` beside `hub.yaml`: do not set them
unless you are relocating something. An earlier version of this page set
`policy: /etc/halite/policy.yaml`; pasted into a FreeBSD file it made `keys
operator create --admin` print `mkdir /etc/halite: permission denied` after
it had made the certificate, and write no policy.) Then make the tree
directories the hub reads:

```sh
sudo install -d -o halite -m 0755 /srv/halite/states /srv/halite/pillar
```

`serve` **refuses to start** if a file root contains the hub's own
`state_dir`, `cache_dir` or `pki_dir`: that would serve the job cache, and so
every return in the estate, to every node. Keep the tree somewhere else.

Every setting is in [the configuration reference](../configuration.md); the
file's comments explain the rest. A key halite does not recognise is reported
at startup, so a typo is not silent.

### 5. Name the hub

The hub issues itself a serving certificate at first start, valid for the
names a node may dial. By default these are `localhost`, `127.0.0.1`, `::1`,
the machine's host name and FQDN, and the host of `listen` if it is a
specific address. **Add the name nodes will use** with `--names`, which is a
flag of `serve`:

```sh
# systemd: a drop-in
sudo systemctl edit halite-hub
#   [Service]
#   ExecStart=
#   ExecStart=/usr/local/bin/halite-hub serve --config /etc/halite/hub.yaml --names hub.example.com,hub

# FreeBSD
sudo sysrc halite_hub_flags='--names hub.example.com,hub'
```

A name missing here is a handshake failure on the node, with a message about
the certificate, not about this step.

Three things about `--names`, each seen on the FreeBSD hosts:

- **It replaces the defaults, it does not add to them.** With
  `--names hub.example.com,hub` the certificate was valid for those two names
  only, and the next command, `halite-hub runner manage.status` (which dials
  `localhost`), failed with `certificate is valid for hub.example.com, hub,
  not localhost`. List every name, including `localhost,127.0.0.1` and the
  host name if anything dials it: `--names hub.example.com,hub,localhost,127.0.0.1`.
- **It is read when the certificate is issued**, which is the first start, or
  later when the certificate is past half its life. Restarting with different
  names changed nothing: the certificate on disk was kept. To change the names,
  stop the hub, remove `pki_dir/hub.crt` (the key is kept), and start it; the
  new certificate chains to the same CA, so no node changes.
- **On FreeBSD, in v0.1.1, `halite_hub_flags` did not work.** `service
  halite_hub start` failed with `daemon: unrecognized option --names` and the
  usage text of `daemon(8)`, because `rc.subr` hands `halite_hub_flags` to the
  command it wraps, which is `daemon`. The same is true of `halite_api_flags`
  and `halite_node_flags`. It is fixed on `main` (DIVERGENCE 5.283) and in
  releases after v0.1.1; with v0.1.1, install `contrib/rc.d/halite_hub` from
  `main` over the archive's copy before setting the variable. The fixed script
  was run on the lab: the process was `halite-hub serve --config … --names
  hub.example.com,hub,localhost,127.0.0.1` with the flags once, and `start`,
  `restart` and `stop` all behaved, including after a reboot.

### 6. Install the service and start it

```sh
# Linux
sudo install -m 0644 service/systemd/halite-hub.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now halite-hub

# FreeBSD
sudo install -m 0555 service/rc.d/halite_hub /usr/local/etc/rc.d/
sudo sysrc halite_hub_enable=YES
sudo service halite_hub start
```

The unit runs the hub as `halite` with the standard sandboxing. It writes
only `/etc/halite/pki` and the three `halite` directories. On first start
the hub **creates the enrollment CA** and logs its fingerprint:

```
warn: created a new enrollment CA algorithm=ecdsa-p256 dir=/etc/halite/pki fingerprint=3e:3e:f8:89:…
info: issued the hub's serving certificate names=localhost,127.0.0.1,…
warn: there is no policy file, so no operator may submit a job fix=halite-hub keys operator create <name> --admin
info: hub listening address=:4510 ca_fingerprint=3e:3e:f8:89:… enrollment_mode=manual …
```

The warning about no policy is correct and expected: the next step fixes it.

### 7. Make yourself an operator

Run as the account that owns `pki_dir`, so the files belong to it:

```sh
sudo -u halite halite-hub keys operator create ed --admin
```

```
policy    /etc/halite/policy.yaml (administrator)
operator  ed
principal cert:CN=ed
cert      /etc/halite/pki/operator-ed.crt
key       /etc/halite/pki/operator-ed.key
expires   2026-11-10T18:24:35Z
```

`--admin` writes a bootstrap policy giving that one operator everything, and
only because there was no policy. **Restart the hub** so it reads the policy
(there is no reload):

```sh
sudo systemctl restart halite-hub      # FreeBSD: sudo service halite_hub restart
```

Copy `operator-ed.crt` and `.key` to wherever you will type commands (mode
`0600`), or run them on the hub as a user who can read them. The certificate
lasts 30 days by default; plan its renewal
([chapter 37](37-certificates.md)).

### 8. Record the fingerprint, safely

```sh
halite-hub keys fingerprint
```

Write it where you will find it when you enrol nodes, and where nobody can
swap it: your password manager, your provisioning tool's secret store, a
signed internal document. This value is the trust anchor for the whole fleet
([chapter 25](25-trust-and-identity.md)).

## Verify

Everything below changes nothing.

```sh
halite-hub doctor
```

```
halite-hub doctor — /etc/halite/hub.yaml

  pass  configuration validity           /etc/halite/hub.yaml loads, and matches what this process is running
  pass  certificate validity and expiry  operator ed: 29 days left; the enrollment CA: 3649 days left; this hub's certificate: 89 days left
  pass  file server reachability         file_roots for base: 1 root(s)
  pass  pillar compilation               compiles, 2 top-level key(s)
  pass  disk space                       …: 135.4 GiB free
  skip  queue depths                     nothing on this hub keeps a bounded queue
  pass  module verification              every module that changes this machine as root has been run against its tool; 24 unprivileged ones have not
  pass  FIPS mode consistency            neither the kernel nor this build is in FIPS mode

  7 pass, 1 skip
```

On FreeBSD the output differs in two places: the FIPS line is `skip  FIPS mode
consistency  freebsd has no kernel FIPS mode, and this is not a FIPS build`, and
without `--config` the heading names the configuration *directory*
(`/usr/local/etc/halite`) and not `hub.yaml`. The totals there were `6 pass, 2
skip`.

Then check the three claims you care about:

```sh
halite-hub runner manage.status        # works: the operator and policy are right
halite-hub keys list                   # "no nodes have asked to enrol" on a new hub
ss -ltn | grep 4510                    # (FreeBSD: sockstat -l | grep 4510) it is listening
```

From a **different** machine, check the port is reachable and the certificate
names match (this is the call a node's first contact makes):

```sh
openssl s_client -connect hub.example.com:4510 -servername hub.example.com -alpn halite/1,h2 </dev/null 2>/dev/null | openssl x509 -noout -subject -ext subjectAltName
```

`-alpn halite/1,h2` is required. The hub ends the handshake of any client that
does not offer `halite/1` (its log says `peer … does not speak halite/1`), and
it negotiates `h2`, so a client offering only `halite/1` is refused too
(`no application protocol`). Without the option `openssl` prints no certificate
and `x509` reports `Could not read certificate from <stdin>`. A line from the
lab, through a tunnel: `subject=CN = halite hub`, then `DNS:hub.example.com,
DNS:hub, DNS:localhost, IP Address:127.0.0.1`.

and confirm `hub.example.com` is in the names. Finally enrol a first node
([chapter 31](31-install-nodes.md)) before you consider the hub done: a
hub no node has joined is untested.

## If it goes wrong

| Symptom | Likely cause | Fix |
|---|---|---|
| `serve` exits at once: `creating the node cache: mkdir /var/cache/halite/nodes: permission denied` | The cache directory is missing or not writable by the hub's account | Create it owned by `halite`, mode `0700` (step 3). With systemd the unit's `CacheDirectory=` does this. |
| `serve` exits naming a file root | A `file_roots` entry contains `state_dir`, `cache_dir` or `pki_dir` | Move the tree elsewhere |
| `configuration key "…" is not recognised and was ignored` | A typo, or a setting for another program | Check [the reference](../configuration.md); it says which program reads which |
| Every command: `this hub has no policy, so nothing is authorized` | No `policy.yaml`, or the hub started before you made it | Step 7, then restart the hub |
| `halite-hub run` says the certificate is not accepted | The operator certificate expired, or was issued by a different CA | `keys operator create` again as the hub's account |
| A node cannot connect: certificate error naming a host | The node dialled a name not on the hub's certificate | Add the name with `--names`, remove `pki_dir/hub.crt`, restart (step 5); a restart alone keeps the old certificate |
| FreeBSD: `service halite_hub start` prints `daemon: unrecognized option` | `halite_hub_flags` is set and the script is from v0.1.1 or earlier | Use the corrected `contrib/rc.d/halite_hub` (step 5) |
| Nothing listens on 4510 | Firewall, or another service has the port | `listen:` in `hub.yaml`, or free the port |
| `doctor` warns about the pillar | A pillar file does not compile | Run `halite-node pillar items --local --pillar-root …` against it |

To **start over** on a hub with no nodes: stop it, remove everything under
`pki_dir` and `state_dir`, and start again. This makes a new CA and a new
fingerprint. **Never do this on a hub that has enrolled nodes**; they hold
certificates from the old CA and every one would have to enrol again.

## Key ideas

- A hub is **one unprivileged service on one port**; `make install` (or the
  archive commands) makes it, a first start makes the **CA**, `keys
  operator create --admin` makes you an **operator**, and a restart reads the
  policy.
- Put the name nodes will dial in the hub's certificate with **`--names`**.
- Keep the tree **outside** `state_dir`, `cache_dir` and `pki_dir`.
- Record the CA **fingerprint** safely; it is the fleet's trust anchor.
- `doctor` is the acceptance test, and enrolling a node is the real one.

Next: [installing and enrolling nodes](31-install-nodes.md).
