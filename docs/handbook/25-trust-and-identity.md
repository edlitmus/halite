# Trust and identity

<!-- abstract: How a node proves who it is: the enrollment CA, fingerprint pinning, manual and token enrollment, certificate lifetimes and renewal, revocation, and what each key protects. -->
<!-- kind: concept -->
<!-- index: enrollment; PKI; certificate authority; CA; fingerprint; hub_fingerprint; pinning; keys accept; keys reject; keys revoke; keys delete; keys list; keys show; bootstrap token; keys token create; enrollment_mode; token enrollment; renewal; halite-node renew; certificate_lifetime; CRL; export-crl; node identity; operator certificate; keys operator create; node ID; ca.key; trust on first use -->

Everything in halite that matters rests on one question: *is this really
the node (or the hub, or the operator) it says it is?* This chapter is how
that question is answered, what the answer is worth, and how you operate it.

The short version:

- The hub runs a **certificate authority** (CA). Every node, and every
  operator, holds a **certificate** it issued.
- A node's private key is made **on the node** and never leaves it. The hub
  never holds anything that could impersonate a node.
- A node joins by **enrolling**: it asks, an operator **approves** (or a
  short-lived **token** does), and the hub issues a certificate.
- Certificates **expire** (90 days for a node) and **renew themselves**.
  A stolen key has a bounded useful life, and any certificate can be
  **revoked** at once.
- The one moment trust is established from nothing, the first contact, is
  protected by a **fingerprint** you compare by another route.

## Who holds what

| Party | Holds | Never holds |
|---|---|---|
| **The hub** | The CA key and certificate; its own serving key and certificate; each node's **certificate** and a record of the request | Any node's private key |
| **A node** | Its own private key and certificate; the CA certificate it pinned | The CA's private key; another node's anything |
| **An operator** | A certificate and key issued for them (30 days by default) | The CA's private key |

Because the hub never holds a node's key, **compromising the hub's files
does not let an attacker pose as a node** to another party. And
because a node holds only its own certificate, compromising a node does
not let an attacker pose as the hub or as another node.

The CA key (`ca.key` in the hub's `pki_dir`) is the one secret to protect
like a root password. Anyone holding it can issue a certificate for any node
ID. It is generated on the hub's first start. Keep it off shared media, back
it up encrypted ([chapter 35](35-backup-and-recovery.md)), and restrict the
directory to the account the hub runs as. It can be held in a hardware
token or a KMS through the signer interface, so that it need not be on the
hub's disk at all.

## First contact: the fingerprint

A node that has never seen the hub faces the oldest problem in secure
communication: it needs to trust the hub's CA, and it has nothing yet to
base that trust on.

halite's answer is a **fingerprint**: the SHA-256 digest of the CA
certificate. You obtain it from the hub:

```sh
halite-hub keys fingerprint
```

```
3e:3e:f8:89:28:58:16:7d:52:0c:75:2c:b5:d4:74:0b:ce:bf:dd:96:2f:9f:ab:88:ad:b6:57:a4:de:fd:ab:08
```

and give it to the node **by a route an attacker cannot tamper with**:
written into the node's configuration by your provisioning tool, read over
the phone, taken from a signed document. The node's `hub_fingerprint` setting
holds it. A node **will not enrol without one**, whichever way it is later
admitted:

```
halite: this node needs `hub_fingerprint` to enrol. Take it from `halite-hub keys fingerprint`
on the hub and set it in the configuration, or pass --hub-fingerprint. It is what proves the CA
is the right one, so there is no enrolling without it
```

This is deliberately stricter than "trust on first use", where a node
accepts whatever it is shown the first time. Here the fingerprint **is** the
trust decision. An attacker in the middle would have to present a
certificate whose SHA-256 matches the one you pinned, which is a preimage
attack. What they cannot do is put the genuine (public) CA in the chain
beside their own certificate: their certificate then does not chain to it,
and the handshake fails. After the first contact the CA certificate is in
the node's `pki_dir` and every later connection verifies against it with no
fingerprint needed.

(The node fetches the CA certificate from the hub with certificate
verification *off*, and then checks it against the pinned fingerprint
itself. That is the only place in the whole build that verifies less than
fully, and it is why there is no mode of `hub_fingerprint` that is optional.)

## Enrolling a node

Two ways in. They differ in who says yes.

### Manual (the default)

```sh
# on the node
halite-node enroll --hub hub.example.com --hub-fingerprint '3e:3e:f8:89:…'
```

```
node        web1.lab
fingerprint cd:6e:55:75:a9:0e:80:9b:75:1b:d5:5b:e6:6e:2c:82:37:df:f8:67:b9:6b:ae:91:92:2b:a3:b6:92:c0:4a:7f
hub         https://127.0.0.1:14510

the request is waiting for an operator to accept it.
on the hub: halite-hub keys accept web1.lab
run this again, or pass --wait, once it has been accepted.
```

The node made a key pair, sent a signing request, and printed **its own
fingerprint** and exited with status `2`: *pending*, neither success nor
failure. On the hub:

```sh
halite-hub keys list
```

```
pending (1)
  web1.lab    cd:6e:55:75:a9:0e:80:9b:75:1b:d5:5b:e6:6e:2c:82:37:df:f8:67:b9:6b:ae:91:92:2b:a3:b6:92:c0:4a:7f
```

The operator **compares the two fingerprints by a route other than the
network the enrolment travelled over** (the node's console, a ticket, a
shell on the machine) and accepts:

```sh
halite-hub keys accept web1.lab
```

```
accepted web1.lab (cd:6e:55:…), expires 2027-01-09T18:24:45Z
```

On the node, `halite-node enroll` again collects the certificate:

```
enrolled: …/pki/node.crt
```

(`--wait` makes the first command wait for the decision instead.) That
comparison is the human step that makes "manual" mean something: it proves
that the request the hub is looking at came from the machine you are
looking at. Skipping it, and accepting whatever is pending, throws away the
reason for having approval at all.

### By token (for machines nobody is watching)

An autoscaling fleet cannot wait for an operator. A **bootstrap token**
admits a node without one:

```sh
# on the hub: first, enrollment_mode: token in hub.yaml (a restart)
halite-hub keys token create --ttl 1h --nodes '*.lab'
```

```
token   vh9exVfcVbIBGlTzLZKNPdVqqlMqYkvW613SUZjaQCE
id      1eedeb8e0623
expires 2026-10-11T19:51:52Z
uses    1
nodes   *.lab

The hub stores only a digest: this is the only time the token is shown.
```

```sh
# on the node
halite-node enroll --hub-fingerprint 'ab:cd:…' --token='vh9exVfc…'
```

```
node        web2.lab
fingerprint 56:2f:f9:5d:9e:dd:b1:c0:e7:3b:d7:f2:66:b5:bd:c8:3b:8d:85:ef:48:30:d3:52:44:f1:12:5f:ac:3c:f0:94
hub         https://127.0.0.1:14510

enrolled: …/pki/node.crt
```

Properties of a token, all deliberate:

| Property | |
|---|---|
| **Single use** by default | `uses 1`; one node, once. |
| **A mandatory lifetime**, at most 24 hours | `--ttl` is required; a token cannot be created without one. |
| **Scoped** | `--nodes` restricts the IDs it may admit (a glob); `--cidr` the source addresses. |
| **Stored as a digest** | The hub keeps SHA-256 of the secret. Shown once. |
| **Accountable** | `keys token list` shows what each admitted. |
| **Does not replace the fingerprint** | A node enrolling for the first time is refused without `hub_fingerprint`, token or not. |

> **Things the labs found.** First, the hub only honours tokens when
> `enrollment_mode: token` is set in `hub.yaml`; in the default `manual`
> mode a node that presents one is simply held pending, with no message
> saying that its token was ignored. Second, write the token as
> `--token=SECRET` with an equals sign. A token is random base64 and can
> begin with a hyphen, and `--token -abc…` is then read as a flag
> (`--BLy… is not a flag of halite-node enroll`); `--token=-abc…` is read
> correctly. Both are in the ledger.

Prefer `keys token revoke <id>` to `keys token delete <id>`: a revoked token
admits nothing and keeps the record of what it already admitted, which
answers "what did this token let in?" after a leak. `delete` is for fleets
that mint a token per instance, where the list would grow without bound.

There is deliberately **no equivalent of "auto-accept every request"**: no
configuration key produces it. The automatic path is the token, which
expires, is scoped, and records what it admitted. A mode called `attested`
(admitting nodes that present a cloud instance identity document) is named
in the design and **is not built**: it is refused by name.

## The node's identity

A node's identity in its certificate is its **node ID**, in the subject
common name and in a URI name of the form `halite://node/<id>`. The URI is
authoritative. The hub reads the identity from the **certificate**, never
from anything the node says in a request, which is why a node cannot ask
for another node's pillar or publish into another node's mine.

The ID is chosen once and then fixed ([chapter 8](08-grains.md)): the
configuration file's `node_id`, `HALITE_NODE_ID`, the pinned file, a cloud
instance ID, the FQDN, the host name, in that order. After enrolment it is
written down (`node_id_caching`, on by default) so a DHCP lease change or a
cloud rename cannot turn one machine into two.

Names matter, because targeting, pillar and policy all select on them.
Choose them so a glob can say what you mean (`web1.prod.example.com`), set
`node_id` explicitly on anything whose hostname might change, and decide on
case (`node_id_lowercase`), since targeting is case-sensitive.

## Certificate lifetimes

| Certificate | Lifetime | Renewed |
|---|---|---|
| A node's | 90 days (`certificate_lifetime`, default `2160h`) | By the node, at half its life, with a new key, automatically |
| The hub's serving certificate | the same | By the hub, at half its life, **without a restart** |
| An operator's | 30 days (`keys operator create --lifetime`) | Re-issue by an operator. Give a scraper or an automation identity a long one on purpose. |
| `halite-api`'s certificates | set by whoever makes them | The example tree renews them; see [chapter 32](32-install-api.md) |
| The enrollment CA | ten years | **Not renewed.** No CA rotation exists yet; `halite-hub doctor` warns 30 days before it expires. |

**Renewal** is hands-off. A running `halite-node connect` checks its
certificate when it starts and every twentieth of its life after (hourly for
the default), and once it is past halfway it asks for a new one with a fresh
key, presenting the old certificate as proof. The hub revokes the old serial
and the node reconnects on the new certificate within a second. A failed
renewal is a warning, tried again at the next check; the old certificate
keeps working until it expires, and `halite-node doctor` warns two weeks
ahead. The previous key is kept beside the new one as
`node.key.renewed.<UTC time>`, in case the new identity turns out to be bad,
and removed after the next successful renewal.

A node that is not running `connect` is **not** renewed. `halite-node renew`
does it by hand; before the halfway point it says so and changes nothing, so
a nightly `renew` in cron is safe for machines that only ever run `--local`.

Because certificates carry validity dates, **clocks matter**. A node whose
clock is far from the hub's cannot complete a handshake; `halite-node
doctor` reports skew against the hub.

## Revocation

```sh
halite-hub keys revoke web2.lab --reason "decommissioned"
```

```
revoked web2.lab
a running hub applies this within a few seconds; the record on disk is the decision
```

There is no waiting for a revocation list to propagate: the hub checks its
denylist of revoked serials at every handshake and on every request over a
connection that is already open. A connected node whose enrolment is revoked
sees:

```
error: this node's enrollment has been revoked reason=documentation test
```

and exits (status `1`, which the service files treat as "do not restart",
since a restart could not fix it). `keys list` then shows it, with the
reason:

```
revoked (1)
  web2.lab   56:2f:f9:5d:…
               documentation test
```

Related commands:

| Command | Does |
|---|---|
| `keys list [--state <state>]` | Every node the hub has heard from, by state: pending, accepted, rejected, revoked. |
| `keys show <node>` | One node's record: state, source (manual or token), fingerprint, serial, expiry, first seen, and the certificate. |
| `keys accept <node>... \| --all` | Issue to pending requests. (`--all` is for when you have verified a batch; use it knowingly.) |
| `keys reject <node>... --reason` | Refuse a request and **keep it**, for the record. |
| `keys revoke <node>... --reason` | Withdraw an issued certificate. |
| `keys delete <node>...` | Forget a node entirely. Its certificates stay refused until they expire, kept under `keys/deleted/` in the key store, so deleting a record can never leave a live certificate. |
| `keys export-crl [--out <path>]` | Write a signed revocation list for anything outside halite that wants one. It lists every certificate the hub refuses (revoked, replaced by a renewal, or belonging to a deleted node), and its number only rises. |

To bring a revoked or lost node back: delete its record, make a new token (or
repeat the manual enrolment), and enrol it again from a new key.

> **Why this matters operationally.** A decommissioned machine that is
> not revoked still holds a valid certificate until it expires. Make
> `keys revoke` a step of your decommissioning runbook
> ([chapter 40](40-incidents.md)).

## Operators

People and automation prove themselves with an **operator certificate**:

```sh
halite-hub keys operator create ed --admin
halite-hub keys operator create ci-pipeline --lifetime 8760h
```

Each writes a certificate and key (`operator-<name>.crt`, `.key`) in the
hub's `pki_dir`, and prints their path and expiry. What an operator may do
is not in the certificate; it is in the policy, bound to the principal
`cert:CN=<name>` ([chapter 26](26-authorization.md)). The same trust root
issues them, so they can be revoked with the same machinery. Treat the key
file like a password: it can act as that operator until it expires.

## Where the weak points are, honestly

- **The CA key.** Whoever holds it can issue any identity. Protect it, back
  it up, consider a KMS.
- **The fingerprint's route.** If the fingerprint you give a node comes over
  the same network an attacker controls, the pin protects nothing. The
  protection is exactly as good as the independence of that route.
- **Approval by habit.** Manual enrolment is only as strong as the operator
  who actually compares fingerprints.
- **No CA rotation yet.** The CA lasts ten years. Know the date. The
  design describes overlapping CA generations; it is not built.
- **Clocks.** Time is part of certificate validity.

None of these is exotic, and each has an answer that costs a procedure and
not a product.

## Key ideas

- The hub is a **CA**; nodes and operators hold certificates it issued;
  **node keys never leave the node**.
- First contact is protected by a **pinned fingerprint** delivered by an
  independent route; **there is no enrolling without it**.
- **Manual** enrolment: request, compare fingerprints out of band,
  `keys accept`. **Token** enrolment (`enrollment_mode: token`): scoped,
  short-lived, single-use, accountable; write `--token=SECRET`.
- Certificates **expire and renew themselves** at half their life; a
  failed renewal is retried and warned about.
- **Revocation** takes effect immediately; the node stops, and the record
  keeps the reason.
- Protect `ca.key`; mind the CA's ten-year life; keep clocks right.

Next: [authentication and authorization](26-authorization.md): what an
identity is allowed to do.
