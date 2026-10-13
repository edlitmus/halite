# Runbook: certificate operations

<!-- abstract: Every certificate in the estate, who renews it, how to see how long it has, how to renew or replace each, and how to plan for the one thing that does not renew: the CA. -->
<!-- kind: runbook -->
<!-- index: certificate; renewal; expiry; halite-node renew; renew --force; node.key.renewed; operator certificate reissue; serving certificate; API certificate; CA expiry; CA rotation; halite_hub_ca_expiry_seconds; keys expiring; openssl x509; certificate_lifetime; clock skew; key rotation; enroll --force -->

**Use this runbook to** know every certificate halite uses, never be surprised
by an expiry, and replace any of them.

**You will end with** an inventory of what expires when, an alert on each, and a
dated plan for the CA.

## Before you start

Know the inventory. Four of these renew themselves; two do not, and a third has
no mechanism at all.

| Certificate | Held by | Lifetime | Renewed by |
|---|---|---|---|
| **A node's** (`node.crt`) | the node | 90 days (`certificate_lifetime`) | **The node**, at half its life, automatically while `connect` runs |
| **The hub's serving** (`hub.crt`) | the hub | the same | **The hub**, at half its life, without a restart |
| **An operator's** (`operator-<name>.crt`) | a person, a CI job, **the API**, a scraper | 30 days by default (`--lifetime`) | **Nobody.** Re-issue it before it expires. |
| **The API's serving** (`tls_cert`) | `halite-api` | whatever you issued | **You**: your HTTPS PKI, ACME or a state. Picked up without a restart. |
| **A node's metrics serving** (`metrics_tls_cert`) | a node, if enabled | whatever you issued | **You** |
| **The enrollment CA** (`ca.crt`) | the hub; every node pins it | ten years | **Nothing, and there is no rotation yet.** |

(Signing keys for extensions and jobs are keys, not certificates, and do not
expire; see [chapter 29](29-extensions.md) and
[chapter 28](28-hardening.md).)

Clocks matter. A certificate carries a validity window, so a node with a
clock far from the hub's cannot connect. `halite-node doctor` reports the skew.

## Procedure

### 1. See what you have

Everything below changes nothing.

**One program's view:**

```sh
halite-hub doctor
```
```
  pass  certificate validity and expiry  operator ed: 29 days left; the enrollment CA: 3649 days left; this hub's certificate: 89 days left
```
```sh
halite-node doctor      # …the hub's CA: 3649 days left; this node's certificate: 89 days left
halite-api doctor
```

`doctor` warns two weeks ahead of a node's expiry and 30 days ahead of the
CA's, and warns of an operator or serving certificate nearing its end. A node
that is *connected* keeps its connection past its certificate's expiry and is
refused only on the next one, so the symptom of an expired certificate
appears at a **restart**, not at the expiry.

**The fleet's view**, from the hub's metrics ([chapter 34](34-monitoring.md)):

```
halite_hub_keys_accepted            3
halite_hub_keys_expiring            0      # certificates ending within 30 days
halite_hub_keys_expired             0
halite_hub_soonest_certificate_expiry_seconds   …
halite_hub_ca_expiry_seconds        3.15e+08     # about ten years
```

and for any one certificate file:

```sh
openssl x509 -noout -serial -subject -enddate -in /etc/halite/pki/node.crt
```
```
serial=177C87DA328B89F822B515FAF89C62CC
notAfter=Jan  9 19:34:04 2027 GMT
```

**Alert on these** and on the CA long before it matters: page on
`halite_hub_keys_expired > 0`, ticket on `keys_expiring > 0` that does not clear,
and put the CA's date in a calendar *now*.

### 2. Node certificates

They renew themselves: a running `halite-node connect` checks its certificate
at start and every twentieth of its life, and past halfway asks the hub for a
new one with a **fresh key**, presenting the old certificate as proof. The hub
revokes the old serial; the node reconnects on the new one within a second. A
failed renewal is logged, tried again at the next check, and the old
certificate keeps working until it expires.

The cases that need you:

- **A node that is not running `connect`** (stopped, or used only with
  `--local`) is never renewed. Run `halite-node renew` from cron on such
  machines. Before the halfway point it prints the date and changes nothing, so a
  daily line is harmless:

  ```
  $ halite-node renew
  the certificate is good until 2027-01-09T18:24:45Z; renewal is due at half its life
  ```

- **Force it** when you want a new key now (a suspected leak, a changed
  algorithm):

  ```
  $ halite-node renew --force
  renewed: web1.lab expires 2027-01-09T19:34:04Z
  the previous key is at …/pki/node.key.renewed.20261011T193404
  ```

  The serial changed, the hub's `keys list` showed a new fingerprint, and the
  node never stopped answering. The old key is kept beside the new one in case
  the new identity turns out to be bad, and removed after the *next*
  successful renewal; so a node holds one at a time. `--key-algorithm` changes
  the algorithm.
- **A node's identity must be replaced** (the machine was cloned, restored
  from an old image, or its key leaked): `halite-hub keys revoke` it, then
  enrol again from a fresh key (`halite-node enroll --force` moves the old key
  aside as `node.key.<UTC time>`, which is never removed: re-enrolling was an
  operator's decision).

A node on a build before the automatic-renewal change does not renew itself.
If any of yours are that old, upgrade them
([chapter 36](36-upgrades.md)), and until then run `renew` daily from cron.

### 3. The hub's serving certificate

The hub renews it at half its life, replaces it in memory, and keeps serving
without a restart. If its **names** must change (a new DNS alias), stop the hub, remove
`pki_dir/hub.crt` (keep `hub.key`), and start it with `--names` listing every
name nodes dial, `localhost` and `127.0.0.1` included: `--names` replaces the
default names, it does not add to them. It issues a fresh certificate that
chains to the same CA, so no node changes. **Restarting with new `--names` and
leaving `hub.crt` in place changes nothing**: the certificate is only issued
when there is none or it is past half its life (`servingCertificate` in
`cmd/halite-hub/serve.go`, seen on a FreeBSD host: the old names stayed until
the file was removed). Check with `openssl x509 -in pki_dir/hub.crt -noout
-ext subjectAltName`.

### 4. Operator certificates

They are the ones people forget, because they last only 30 days and nothing
renews them. Re-issue with the same command, as the hub's account, in place:

```sh
sudo -u halite halite-hub keys operator create ed --lifetime 720h
sudo -u halite halite-hub keys operator create api              # the API's own
```

```
operator  api
principal cert:CN=api
cert      /etc/halite/pki/operator-api.crt
key       /etc/halite/pki/operator-api.key
expires   2026-11-10T19:18:15Z
```

Two details. **Re-issuing does not revoke the previous certificate**, so
nothing in flight is refused. And **no restart is needed** for the API: it
reads the pair again before each request to the hub. Give a scraper or an
automation identity a long lifetime *on purpose* (`--lifetime 8760h`), since it
cannot notice its own credential expiring, and put a date on when you will
re-issue it. The example tree (`halite.certs`) re-issues the API's operator
certificate a week before it expires, from a state.

To end a person's access immediately, `halite-hub keys revoke` is for *nodes*;
for an operator remove their binding from `policy.yaml` and restart the hub
and API, since the certificate is still valid but is bound to nothing.

### 5. The API's and a node's serving certificates

These come from **your** PKI and are read on each new connection, so renewing
in place needs no restart:

```sh
# ACME client, or a state:
x509.certificate_managed …    # see the module reference
```

`halite-api doctor` warns when its presented certificate is near expiry (the
warning appeared on a three-day lab certificate, as it should). For a scraper,
the certificate it verifies is *this* one, or the CA that signed it; if you use
a self-signed certificate, every renewal is a new certificate to trust, which
is why an internal CA is better.

### 6. The CA: plan it now

The enrollment CA lasts **ten years** and **there is no rotation**. The design
describes overlapping CA generations; it is not built, and a hub warns 30 days
before expiry, which is not enough time for a fleet. The honest plan is:

1. **Write the date down** (`halite-hub doctor` shows days left) and set
   calendar reminders at 2 years, 1 year and 6 months.
2. **Know the replacement procedure**: it is the lost-CA procedure of
   [chapter 35](35-backup-and-recovery.md), done on purpose: stand up a new
   hub CA, give every node the new `hub_fingerprint`, and re-enrol each (a
   token-based provisioning script makes this an afternoon; manual enrolment
   makes it a project).
3. **Rehearse it** on the lab at least once before year eight.
4. Consider holding the CA key in a KMS through the signer interface so its
   protection does not depend on a file.

A CA compromise, as opposed to expiry, is handled by the same replacement and
is in [chapter 40](40-incidents.md).

## Verify

- [ ] `halite-hub doctor` shows the operator, CA and hub certificates with
      comfortable margins.
- [ ] `halite_hub_keys_expiring` and `halite_hub_keys_expired` are 0.
- [ ] Every operator certificate has an owner and a re-issue date, the API's
      first among them.
- [ ] Calendar entries exist for the CA.
- [ ] You have seen a forced renewal succeed (`renew --force`) and the node keep
      answering.
- [ ] Nodes that do not run `connect` have a `renew` line in cron.

## If it goes wrong

| Symptom | Cause | Fix |
|---|---|---|
| A node cannot reconnect after a restart: `certificate has expired` | It was not renewed (stopped, or an old build) and the connection that kept it alive ended | `renew --force` needs the old certificate to still be valid; if it is not, revoke, delete, and re-enrol |
| `halite-hub run` fails: certificate rejected | Your operator certificate expired | Re-issue it |
| The API answers `readyz: not ready` after 30 days | Its operator certificate to the hub expired | Re-issue `api`; no restart |
| Browsers or scrapers reject the API | The serving certificate expired or lacks the name | Re-issue from your PKI; check `subjectAltName` |
| `renew` says the certificate is "good until …; renewal is due at half its life" | It is early | Nothing; or `--force` |
| Handshakes fail with `not yet valid` | Clock skew | NTP; `halite-node doctor` |
| Many nodes expire at once | They were all enrolled at once and none was running `connect`, or the hub was down at renewal | Stagger by enrolling in waves; renew with `--force` in batches |

## Key ideas

- **Nodes and the hub's serving certificate renew themselves;** operator
  certificates, the API's serving certificate, and the CA do not.
- Operator certificates last 30 days: **put a re-issue on a calendar or in a
  state**, the API's especially.
- `renew --force` swaps a node's key without interrupting it; the previous key
  is kept beside it.
- **Expiry bites at the next connection**, so alert early, on `doctor` and the
  `halite_hub_keys_*` metrics.
- The CA has ten years and no rotation: **write the date down and rehearse the
  replacement.**

Next: [scaling](38-scaling.md).
