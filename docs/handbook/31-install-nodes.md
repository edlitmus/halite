# Runbook: installing and enrolling nodes

<!-- abstract: Putting the agent on a machine and bringing it into the fleet: one node by hand, many with tokens, and what to do about images. -->
<!-- kind: runbook -->
<!-- index: install node; enroll; enrolment; halite-node enroll; halite-node connect; node.yaml; hub_fingerprint; bootstrap token; --token; image bake; cloud-init; golden image; node_id; first highstate; rc.d enrol; startup_states; decommission -->

**Use this runbook to** add a machine to the fleet: install the agent, give
it its identity, get an operator or a token to admit it, and see it apply its
first state.

**You will end with** a connected node that appears in `manage.status`,
answers `test.ping`, and has applied (or rehearsed) its highstate.

## Before you start

You need:

- A **running hub** ([chapter 30](30-install-hub.md)) and its **CA
  fingerprint**, delivered by a route an attacker on the network cannot
  tamper with.
- The machine, with **root**: the agent installs packages and manages
  services, so it runs as root.
- **Outbound TCP to the hub's port 4510** from the machine, and the hub's name
  resolving. No inbound ports.
- The **release archive** for the machine's platform (verify it as in
  [chapter 30](30-install-hub.md) step 1), or a source checkout.
- Working **time** (NTP): certificates have validity dates.
- A **node ID** you can live with: it is permanent, it appears in every log,
  and targeting is by it. `web1.prod.example.com` is better than `ip-10-0-3-7`.
  The default is the fully qualified domain name.

## Procedure

### One node, by hand (manual enrolment)

**1. Install the program.** Only `halite-node` is needed.

```sh
sudo install -m 0755 bin/halite-node /usr/local/bin/
sudo install -d -m 0755 /etc/halite
sudo install -d -m 0700 /etc/halite/pki /var/lib/halite /var/cache/halite
sudo install -d -m 0750 /var/log/halite
sudo install -m 0644 man/man8/halite-node.8 /usr/local/share/man/man8/
halite-node version
```

(FreeBSD: `/usr/local/etc/halite` and `/var/db/halite` instead.) A node runs
as root, so these are root-owned.

**2. Write `node.yaml`.** The fingerprint goes here so the agent and the
service files need nothing else:

```yaml
# /etc/halite/node.yaml
hub: hub.example.com
hub_fingerprint: '3e:3e:f8:89:28:58:16:7d:52:0c:75:2c:b5:d4:74:0b:ce:bf:dd:96:2f:9f:ab:88:ad:b6:57:a4:de:fd:ab:08'
node_id: web1.prod.example.com
```

Other settings (all optional) are in `examples/node-with-hub.yaml`: the
`hub_port` if you moved it from 4510; `schedule:`; `beacons:`; `returner:`;
`require_job_signature`; `metrics_listen`. Setting `node_id` explicitly is
the right habit on anything whose host name might change.

**3. Ask to enrol.**

```sh
sudo halite-node enroll
```

```
node        web1.prod.example.com
fingerprint cd:6e:55:75:a9:0e:80:9b:75:1b:d5:5b:e6:6e:2c:82:37:df:f8:67:b9:6b:ae:91:92:2b:a3:b6:92:c0:4a:7f
hub         https://hub.example.com:4510

the request is waiting for an operator to accept it.
on the hub: halite-hub keys accept web1.prod.example.com
run this again, or pass --wait, once it has been accepted.
```

Exit status `2` means *pending*: neither success nor failure. The node made its
private key (in `pki_dir`, mode `0600`; it never leaves) and sent a request.
The **fingerprint it printed is the node's own**.

**4. On the hub, compare and accept.** The step that matters:

```sh
halite-hub keys list                         # the pending request, with its fingerprint
# compare that fingerprint with the one printed on the node, by another route:
# the node's console, a ticket, a call. Then:
halite-hub keys accept web1.prod.example.com
```

```
accepted web1.prod.example.com (cd:6e:55:75:…), expires 2027-01-09T18:24:45Z
```

**5. On the node, collect the certificate and start the agent.**

```sh
sudo halite-node enroll
```
```
enrolled: /etc/halite/pki/node.crt
```

```sh
# Linux
sudo install -m 0644 service/systemd/halite-node.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now halite-node

# FreeBSD (the script also runs `enroll` for you if there is no certificate)
sudo install -m 0555 service/rc.d/halite_node /usr/local/etc/rc.d/
sudo sysrc halite_node_enable=YES
sudo service halite_node start
```

On FreeBSD the rc script enrols if the node holds no certificate, and
refuses to start until it does, printing the `keys accept` line to *your*
terminal rather than into a log. The systemd unit has no equivalent: enrol
before you start the unit.

### Many nodes: token enrolment

A fleet that scales automatically cannot wait for a person. Use **bootstrap
tokens**, which are single-use, short-lived, scoped and recorded.

**1. Once, on the hub**: set `enrollment_mode: token` in `hub.yaml` and
restart it. (In the default `manual` mode a token is ignored, silently, and the
node simply waits for a human.)

**2. Per node, mint a token**, as narrow as you can:

```sh
halite-hub keys token create --ttl 1h --nodes 'web*.prod.example.com' --cidr 10.0.0.0/8
```
```
token   vh9exVfcVbIBGlTzLZKNPdVqqlMqYkvW613SUZjaQCE
id      1eedeb8e0623
expires 2026-10-11T19:51:52Z
uses    1
nodes   web*.prod.example.com

The hub stores only a digest: this is the only time the token is shown.
```

**3. Give the node the token and let it enrol**, with the fingerprint still
required:

```sh
sudo halite-node enroll --token='vh9exVfcVbIBGlTzLZKNPdVqqlMqYkvW613SUZjaQCE'
```
```
node        web2.lab
fingerprint 56:2f:f9:5d:…
hub         https://hub.example.com:4510

enrolled: /etc/halite/pki/node.crt
```

and start the agent. **Write the token as `--token=SECRET`**: a token is random
base64 and can begin with `-`, and `--token -abc…` is then taken for a flag
(this was seen: `--BLy… is not a flag of halite-node enroll`). The FreeBSD rc
script passes the token with a space, so if you use `halite_node_token`
regenerate any token that begins with a hyphen. Both are recorded in the
ledger.

**4. Afterwards**, `halite-hub keys token list` shows what each token
admitted. Revoke rather than delete a token you suspect:

```sh
halite-hub keys token revoke 1eedeb8e0623
```

A cloud-init or provisioning-tool user-data script is the natural place to run
steps 2 (on the hub, called by your orchestration) and 3. The shape:

```sh
#!/bin/sh
# on the new instance; $TOKEN delivered by your provisioning system
install -m 0755 /opt/halite/halite-node /usr/local/bin/
mkdir -p /etc/halite/pki && chmod 0700 /etc/halite/pki
cat > /etc/halite/node.yaml <<EOF
hub: hub.example.com
hub_fingerprint: '$HUB_FINGERPRINT'
EOF
halite-node enroll --token="$TOKEN" && systemctl enable --now halite-node
```

> **Pattern, not a tested script.** The pieces (token enrolment, the
> fingerprint requirement, the unit) were each run. This composition was not
> run on a booting cloud instance.

### Images

If you build machine images, **do not bake in**:

- the node's **private key or certificate** (every instance would share an
  identity, and the second to connect would displace the first);
- a **long-lived token** (a token must expire within 24 hours; a baked-in one
  is a standing credential in every copy of the image);
- a **pinned node ID** file (`/etc/halite/node_id`; `node_id_caching` writes
  it at first enrolment, and a copy would make every clone claim the same
  name).

Bake in the **program** and the **configuration with the fingerprint**; enrol
at first boot. For an image meant to re-identify on every boot, turn
`node_id_caching` off.

## Verify

On the hub:

```sh
halite-hub keys list
halite-hub runner manage.status
halite-hub run web1.prod.example.com test.ping
halite-hub run web1.prod.example.com grains.item os osrelease
```

```
accepted (1)
  web1.prod.example.com   cd:6e:55:75:…
up:
    - web1.prod.example.com
down: []
web1.prod.example.com:
True
```

On the node:

```sh
sudo halite-node doctor
```

and read it top to bottom; a node's doctor runs eleven checks including
connectivity, certificate expiry, clock skew against the hub, and that its
pillar compiles. Then **rehearse its highstate** from the hub and read the
diff before you let anything change:

```sh
halite-hub run web1.prod.example.com state.apply --test
```

and, when you are satisfied, apply it. To have a node converge itself when
the agent starts, set `startup_states` in `node.yaml`.

If your tree is new and the node matches no rule in the top file, the
highstate is empty and the exit status is `2`: that is correct, not an error.
Check with `halite-node state show_top`.

## If it goes wrong

| Symptom | Likely cause | Fix |
|---|---|---|
| `this node needs hub_fingerprint to enrol` | The setting is missing | Add it to `node.yaml`, or pass `--hub-fingerprint` |
| `fetching the hub CA from …: connection refused` / timeout | The hub is down, a firewall blocks 4510, or the name is wrong | `nc -vz hub.example.com 4510` from the node |
| A certificate error naming a host | The hub's certificate lacks the name the node dialled | Add it to the hub with `--names` ([chapter 30](30-install-hub.md)) |
| `the hub presented 2 certificates and none matches the fingerprint this node pinned (…)` | The wrong fingerprint in `node.yaml`, or something is intercepting the connection | **Stop.** Re-obtain the fingerprint by an independent route before doing anything else |
| Enrol stays pending forever | No operator has accepted; or in `token` mode the token was not accepted | `halite-hub keys list`; check `enrollment_mode` |
| `--BLy… is not a flag of halite-node enroll` | The token began with `-` | `--token=SECRET` |
| The node enrols and the agent exits at once | The enrolment was revoked, or `hub_tries` is reached | `journalctl -u halite-node`; the message names which |
| Connected, but `state.apply` says `no top file match` | The node ID matches no rule | `halite-node state show_top`; fix the target or the ID |
| `clock skew` in `doctor` | The clock is off | Fix NTP |
| Two nodes keep displacing each other | They share an identity (cloned key or ID) | Revoke both; re-enrol each from a clean state |

To **remove a node**: `halite-hub keys revoke <id> --reason …` first (it stops
the agent and ends its trust at once), then `keys delete <id>` when you want
the record gone. [Chapter 40](40-incidents.md) covers a node you cannot
trust.

## Key ideas

- A node needs the program, a three-line `node.yaml` (hub, **fingerprint**,
  ID), and a certificate. It **runs as root** and **listens on nothing**.
- **Manual**: `enroll`, compare the two fingerprints, `keys accept`, `enroll`
  again. **Token**: `enrollment_mode: token`, scoped short-lived tokens,
  `--token=SECRET`.
- Never bake keys, long-lived tokens or a pinned node ID into an image.
- Verify with `keys list`, `manage.status`, `test.ping`, `doctor`, and a
  `--test` highstate before the first real one.

Next: [the API and access](32-install-api.md).
