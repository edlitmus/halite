# Keys and certificates

halite replaces Salt's minion key accept/reject dance with a standard CSR
flow. An agent generates its own private key, asks the CA to sign it, an
operator accepts the request out of band, and the agent collects a client
certificate. The private key never leaves the host that generated it.

Everything is stdlib `crypto/x509`, so every artifact is readable with
`openssl x509`, `openssl req`, and `openssl verify`.

## The PKI directory

```
<pki>/ca.crt              the trust root — distribute freely
<pki>/ca.key              the CA private key — mode 0600, never leaves the master
<pki>/master.crt          the control plane's server certificate
<pki>/master.key          mode 0600
<pki>/pending/<id>.csr    requests awaiting a decision
<pki>/accepted/<id>.crt   issued agent certificates
<pki>/rejected/<id>.csr   refused requests, kept for the audit trail
```

Default location: `/usr/local/etc/halite/pki` on FreeBSD,
`/etc/halite/pki` on Linux, `C:\ProgramData\halite\pki` on Windows.
Override with `-pki DIR` or `HALITE_PKI`.

## On the master

```sh
halite key init -cn "acme fleet ca"        # once per fleet
halite key server master.example.com -san 10.0.0.1
halite key admin ed                        # an operator certificate
halite key admin ed -out ~/.halite         # somewhere other than the PKI directory
```

`key init` refuses to run over an existing `ca.crt`: replacing a CA
invalidates every certificate in the fleet, so it has to be deliberate.

`key admin` issues the certificate that may dispatch work. `-out` writes
the pair somewhere other than the PKI directory, which is what an operator
wants: the key belongs on the workstation that will use it, not beside the
CA. `key gen` takes `-out` for the same reason.

## On an agent

```sh
halite key gen web1                        # writes agent.key (0600) and agent.csr
```

The command prints the public key fingerprint. Compare it against what the
master shows before accepting — that comparison is the whole security of
the enrollment step.

## Accepting

```sh
halite key list
# pending    web1    6a:12:39:24:ab:...
halite key accept web1
halite key accept -all                     # every pending request
halite key show web1                       # the issued certificate, PEM
```

`reject` moves a request aside without signing it; `remove` forgets an
identity entirely so that host can enroll again from scratch.

## What the CA will not do

* **It ignores the identity in the request.** The common name of an issued
  certificate is the id an operator accepted, never a value the requester
  chose. A host cannot ask to be called `master`.
* **It refuses a second key for a known identity**, in every state —
  pending, rejected, and accepted alike. Re-sending the same request is a
  safe no-op, so agents can retry; sending a *different* key for an id that
  already exists is an error until an operator removes the entry. That is
  the case that would otherwise let a new host silently take over an
  existing one's identity, and — because enrollment answers before
  authentication — turn a guessed id into a way to enumerate the fleet.
* **It verifies the request signature**, which proves the requester holds
  the matching private key.
* **It scopes key usage.** Agent certificates are valid for client
  authentication only; the server certificate for server authentication
  only. An agent certificate cannot be used to impersonate the master.
* **It constrains identities** to 1-64 characters of letters, digits, dot,
  dash, and underscore, because identities arrive over the network and are
  used to build file paths.

## Lifetimes

| Certificate | Default | Flag |
|---|---|---|
| CA | 10 years | `key init -days` |
| Server | 825 days | `key server -days` |
| Agent | 1 year | fixed; renewed automatically |

## Renewal

An agent watches its own certificate and asks for a new one when 45 days
of its year are left. Nothing on the control plane has to be enabled, and
no operator is involved:

```
agent                                     control plane
  |--- POST /v1/renew (CSR, mTLS) ------->|  same id, same key
  |<-- [certificate, another year] -------|
  |    replace agent.crt, reconnect       |
```

Renewal is not enrollment, and the differences are the point:

* **The identity comes from the certificate the agent is already using**,
  not from the request body. There is nowhere in a renewal request to name
  an identity, so there is nothing to lie about.
* **The key cannot change.** The control plane refuses a request for any
  key other than the one on file. Changing keys is an enrollment, and an
  operator decides those.
* **The agent checks the answer before it keeps it** — the certificate has
  to parse, chain to the CA the agent trusts, carry the agent's own name,
  and belong to the key on disk. `agent.crt` is then replaced atomically,
  so a crash mid-write cannot leave the host with half a certificate.
* **A failure is not fatal.** Renewal starts with weeks to spare, so an
  unreachable control plane means a line in the log and another attempt an
  hour later, not an agent that stops working.

An agent whose certificate has **already expired** cannot renew: the
control plane will not accept the connection. It says so at startup and
names the fix, which is an operator running `halite key remove <id>` so
the host can enroll again.

Revocation is still missing: `halite key remove` forgets the CA's records,
but a certificate already issued stays valid until it expires. Renewal
makes that window a year at most, which is the whole reason to keep the
lifetime short.

For the other half of the picture — protecting pillar data, which is not
encrypted — see [pillar-security.md](pillar-security.md).
