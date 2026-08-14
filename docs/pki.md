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
* **It refuses a second key for a known identity.** Re-sending the same
  request is a safe no-op, so agents can retry; sending a *different* key
  for an id that already exists is an error until an operator removes the
  entry. That is the case that would otherwise let a new host silently take
  over an existing one's identity.
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
| Agent | 1 year | fixed; re-enroll to renew |

Revocation is a CRL or short-lived certificates; today, `halite key remove`
plus a re-issued CA is the blunt instrument. Proper revocation lands with
the control plane.

For the other half of the picture — protecting pillar data, which is not
encrypted — see [pillar-security.md](pillar-security.md).
