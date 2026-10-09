# Metrics

Prometheus text exposition, written directly rather than through a
client library. SPEC section 26.2.

This covers where the metrics are, how to point an external Prometheus
at them, what each family means, and what is worth alerting on.

## Where they are, and what can reach them

| Service | Port | Path | Authentication |
|---|---|---|---|
| `halite-api` | 4511 | `/v1/metrics` | a bearer token |
| `halite-hub` | 4510 | `/v1/metrics` | an operator certificate **and** the `metrics.show` grant |
| `halite-node` | `metrics_listen`, typically 4512 | `/v1/metrics` | a serving certificate, and optionally a client one |

The hub and the API follow the `listen` setting; there is no separate
metrics port for them and no way to move the path. A node listens on
`metrics_listen` and nowhere else, and opens no port until that is set.
`metrics: false` turns the recording off entirely, on any of the three.

**Point the scraper at `halite-api`.** It answers with both expositions
— its own and the hub's — merged into one document, and it is the only
part of the control plane an ordinary client can reach.

The hub cannot be scraped directly, by Prometheus or by anything else
that does not speak halite's own ALPN identifier. It looks like this,
with or without a client certificate:

```
$ curl https://hub.example:4510/v1/metrics
curl: (35) TLS connect error: error:0A000438:SSL routines::tlsv1 alert internal error
```

That is the ALPN gate of [DIVERGENCE 1.7](DIVERGENCE.md), not a
certificate problem, and TLS 1.3 has no way to say so in the alert.

A node is different in kind. SPEC 6.1 has it dial the hub and be
dialled by nothing, and this endpoint is the one deliberate exception —
[DIVERGENCE 1.13](DIVERGENCE.md). It is off until `metrics_listen` says
otherwise, it serves one path, and it takes a certificate. See
[A node's metrics](#a-nodes-metrics) below.

Most of what a node does is also visible on the hub, as jobs, returns,
and events. What is not is the part that never reaches the hub, which
is exactly the part worth having.

## Setting up an external scraper

Three grants are involved, and missing any one of them fails quietly in
a different way.

### 1. The API's own certificate needs `metrics.show` at the hub

The API is a client of the hub like any other, so reading the hub's
exposition is a request the policy has to permit. In `policy.yaml`:

```yaml
roles:
  scraper:
    - runners: ['metrics.show']
bindings:
  - principal: 'cert:CN=api'
    roles: ['scraper']
```

`cert:CN=api` is whatever `api_operator` names in `api.yaml`, which
defaults to `api`. A role with `runners: ['metrics.show']` and nothing
else is enough — this was checked by giving the API a role without it
and watching the hub's half of the exposition disappear.

Check the binding before restarting anything. `policy test` reads the
file rather than asking the running hub, so it answers for an edit that
is not live yet:

```sh
halite-hub policy test 'cert:CN=api' '*' metrics.show --runner
```

```
allowed by role "scraper" rule 0
```

`--runner` is not optional here. `metrics.show` is a runner, and without
the flag the same command is evaluated as a job against a target and
comes back denied — a denial that says nothing about the binding you
meant to test.

A principal with no binding at all says so plainly, which is the shape
this mistake usually takes:

```
denied: cert:CN=api is bound to no role in /usr/local/etc/halite/policy.yaml
```

The hub reads the policy once at startup and has no SIGHUP handler, so
an edit needs `service halite_hub restart` before it takes effect.

**A missing grant here does not fail the scrape.** Prometheus keeps
reporting `up == 1`, and the exposition simply arrives without any
`halite_hub_*` family in it — the API serves its own and drops the half
it could not read. `halite_api_hub_scrape_failures_total` is the signal,
and the [alerting rules](#alerting) below watch it for exactly this.

### 2. The scraper needs an account and the same grant

A scraper is a machine identity, which is what local accounts are for —
OIDC and LDAP are the operator path and there is nobody to log in here.
Generate a password, keep it where the token will be generated from, and
hash it. The paths on this page are FreeBSD's; a Linux host keeps
Prometheus's files under `/etc/prometheus/` and halite's under
`/etc/halite/` rather than `/usr/local/etc/...`.

```sh
head -c 32 /dev/urandom | base64 > /usr/local/etc/prometheus/halite.password
chmod 600 /usr/local/etc/prometheus/halite.password
halite-api account hash < /usr/local/etc/prometheus/halite.password
```

`account hash` reads the password from standard input rather than from
an argument, which would reach the process table and the shell history.
It prints the verifier and never the password.

Put that in the account file — `<config root>/accounts.yaml` unless
`accounts` in `api.yaml` says otherwise:

```yaml
accounts:
  prometheus:
    hash: 'pbkdf2-sha512$600000$NjtK87YzEN8VZnUqJr0…'
    roles:
      - scraper
```

The `roles` here and a binding in `policy.yaml` are two routes to the
same thing, and an account gets the union of both. One or the other is
enough:

```yaml
# policy.yaml, if you would rather keep the grants in one file
bindings:
  - principal: 'local:prometheus'
    roles: ['scraper']
```

The file holds password verifiers, so give it mode 0600 and the account
`halite-api` runs as.
[`contrib/examples/accounts.yaml`](../contrib/examples/accounts.yaml)
is a worked one with every field, and
[configuration.md](configuration.md) has the `accounts` setting.

Check it before going further:

```sh
halite-api account list
```

### 3. Issue it a token

A scraper cannot log in again when its token runs out, so the defaults
are wrong for one. `token_lifetime` is 12h: a token minted in the
afternoon is dead by the next morning, and the scrape starts failing
overnight with nothing to say why. Set it in `api.yaml` before minting:

```yaml
# api.yaml — a scraper's token has to outlive a working day
token_lifetime: 8760h
```

`token_idle` is 4h. Zero means the default rather than "no idle
expiry"; a negative value such as `-1s` is what disables it. The default
is harmless for a scraper, which uses the token every scrape
interval and never goes idle, but it does mean a token parked for an
afternoon stops working.

Give Prometheus its own copy of the certificate `halite-api` presents,
and an empty token file it owns, before logging in. The login below
reads the first and writes the second, and step 4 says why neither can
live in halite's own directories:

```sh
install -o root -g wheel -m 0644 \
    /usr/local/etc/halite/pki/api.crt \
    /usr/local/etc/prometheus/halite-api-ca.crt
install -o prometheus -g prometheus -m 0600 \
    /dev/null /usr/local/etc/prometheus/halite.token
```

Made in that order, the token written next keeps the owner and mode
`install` gave the file. Made the other way round, `install` from
`/dev/null` empties a token that has just been written.

Log in once and keep the token:

```sh
token=$(curl -sS --fail-with-body \
    --cacert /usr/local/etc/prometheus/halite-api-ca.crt \
    -X POST https://api.example:4511/v1/login \
    -H 'Content-Type: application/json' \
    -d '{"username":"prometheus","password":"…"}' | jq -r '.token // empty')

test -n "$token" || { echo "login failed" >&2; exit 1; }
( umask 077; printf '%s\n' "$token" > /usr/local/etc/prometheus/halite.token )
```

Written in three steps on purpose. The obvious one-liner —
`curl -s … | jq -r .token > token` — writes an empty file when the
connection fails and the word `null` when the login is refused, and says
nothing either way: `-s` silences curl's error and the redirection has
already truncated the file by the time `jq` sees there is nothing to
extract. The form above leaves no file at all unless there is a token in
it.

The host in the URL has to be one the certificate covers. Connecting to
`https://localhost:4511` with a certificate whose names are
`api.example` and an address fails verification, and with `-s` that
failure is invisible.

### 4. Point Prometheus at it

```yaml
scrape_configs:
  - job_name: halite
    scheme: https
    scrape_interval: 30s
    metrics_path: /v1/metrics
    authorization:
      type: Bearer
      credentials_file: /usr/local/etc/prometheus/halite.token
    tls_config:
      # The certificate halite-api presents, which is its own and not
      # the enrollment CA — see "The API's serving certificate" in
      # operations.md for where it comes from. For a self-signed one,
      # this is that same file.
      ca_file: /usr/local/etc/prometheus/halite-api-ca.crt
    static_configs:
      - targets: ['api.example:4511']
```

Both files have to be readable **by the account Prometheus runs as**,
which is not the one halite runs as. Do not point either setting inside
halite's own directories:

```
/usr/local/etc/halite/pki   drwxr--r--   halite:wheel
```

That is how it was found on a host set up before `make install` created
the directory; `make install` now makes it `drwx------`, which is the
same answer for anyone else. Either way the directory has no execute
bit for anyone but `halite`, so nothing
else can open a file inside it however permissive the file itself looks.
A `ca_file` under `pki/` fails for the scraper even though `root` and
the operator can both read it perfectly well. That is why step 3 gave
Prometheus its own copies. Check them as that account rather than as
yourself:

```sh
su -m prometheus -c 'cat /usr/local/etc/prometheus/halite.token'
```

The serving certificate is `api.crt`, not `ca.crt`. The enrollment CA
signs node identities and does not sign this, so pointing `ca_file` at
it fails verification even once the permissions are right.

**A permission failure here produces no target at all**, which is worse
than a failing one. Prometheus cannot build the scrape pool, so there is
nothing to be down: `up{job="halite"}` is empty rather than 0, the
target is missing from `/api/v1/targets`, and every alert in this
document is silent because they all match on a series that was never
created. The only evidence is a log line, every scrape interval:

```
level=error component="scrape manager" msg="error creating new scrape pool"
err="error creating HTTP client: unable to read CA cert: ...: permission denied"
scrape_pool=halite
```

So `absent(up{job="halite"})` belongs in the rules below alongside
anything watching `up` itself — see [Alerting](#alerting).

Validate the file before restarting; `promtool` checks that both files
exist and are readable, which catches this:

```sh
promtool check config /usr/local/etc/prometheus.yml
```

Then confirm the endpoint by hand:

```sh
curl -sS --fail-with-body --cacert /usr/local/etc/prometheus/halite-api-ca.crt \
  -H "Authorization: Bearer $(cat /usr/local/etc/prometheus/halite.token)" \
  https://api.example:4511/v1/metrics | head
```

The stanza above is accepted by `promtool check config`. The endpoint,
the token, and both grants have been exercised with `curl` standing in
for the scraper; the paths shown are FreeBSD's, and a Linux host puts
the same files under `/etc/prometheus/`.

## A node's metrics

`halite-node connect` serves `/v1/metrics` on `metrics_listen`, and a
Prometheus scrapes it like any other target. Unset, which is the
default, a node opens no port at all: it dials the hub and is dialled
by nothing, and a listener on every managed machine is a decision an
operator makes rather than one that arrives with an upgrade.
[DIVERGENCE 1.13](DIVERGENCE.md) records the exception.

```yaml
# node.yaml
metrics_listen: ':4512'
metrics_tls_cert: /usr/local/etc/halite/pki/metrics.crt
metrics_tls_key: /usr/local/etc/halite/pki/metrics.key
```

**Those are FreeBSD's paths.** `pki_dir` is `/etc/halite/pki` on Linux,
and the certificate state below writes to `pki_dir`, so a Linux node
names `/etc/halite/pki/metrics.crt` and `/etc/halite/pki/metrics.key`
here. A path for the other platform is easy to miss because it is not
fatal: the agent logs `the metrics endpoint is not serving` with
`the metrics certificate: open /usr/local/etc/halite/pki/metrics.crt: no
such file or directory` once at startup, runs on without a listener, and
Prometheus reports the target as `connection refused`. That is how an
Ubuntu node in this estate's own fleet was found on 2026-10-08, with a
`node.yaml` copied from a FreeBSD one.

**Only the agent serves it.** A one-shot `halite-node call` or
`state apply` is a fresh process whose counters start at zero and whose
lifetime is a second; there is nothing for a scraper to reach and
nothing worth reaching.

### There is no plaintext mode

`metrics_listen` without `metrics_tls_cert` and `metrics_tls_key`
serves nothing, and says so in the log. A node's exposition names which
functions ran, which extensions, and when a deployment went out. That
is the argument that put the hub's endpoint behind a certificate and
the API's behind a token, and it does not weaken because the subject is
one machine.

**The node's own `node.crt` will not do.** It is issued with
`ExtKeyUsage: ClientAuth` and carries no DNS or IP name, so Go refuses
it as a serving certificate and a scraper would have nothing to verify
it against. This is a certificate you supply, exactly as `halite-api`
takes `tls_cert` — and `x509.certificate_managed` can keep it renewed
from the tree like anything else the estate manages.

To refuse a scraper that presents no certificate of its own, name a CA
it must be signed by. The enrollment CA is the obvious one, so the
scraper carries what `halite-hub keys operator create prometheus`
produced:

```yaml
metrics_client_ca: /usr/local/etc/halite/pki/ca.crt
```

Left empty, the port is served to anyone who can reach it. That is a
decision to make deliberately rather than by omission.

### The two certificates, and how to make them

There are two, and they face opposite ways. Confusing them is the
commonest way to get a target that will not come up.

| | What it proves | Named by | Needed when |
|---|---|---|---|
| The node's **serving** certificate | that the scraper reached the node it meant to | `metrics_tls_cert` on the node, `ca_file` at Prometheus | always |
| The scraper's **client** certificate | that the thing scraping is allowed to | `cert_file` at Prometheus, `metrics_client_ca` on the node | only with `metrics_client_ca` |

#### The scraper's client certificate

An operator certificate, which the hub already knows how to issue:

```sh
halite-hub keys operator create prometheus --lifetime 8760h
```

```
operator  prometheus
principal cert:CN=prometheus
cert      /usr/local/etc/halite/pki/operator-prometheus.crt
key       /usr/local/etc/halite/pki/operator-prometheus.key
expires   2027-09-05T01:07:28Z
```

It carries `TLS Web Client Authentication` and is signed by the
enrollment CA, which is why `metrics_client_ca: <pki_dir>/ca.crt` on
the node accepts it.

**Pass `--lifetime`.** The default is 720h — thirty days — and a
scraper cannot notice its own certificate expiring. The scrape simply
starts failing a month after you set it up, at whatever hour you
happened to run the command. This is the same trap as `token_lifetime`
for the API's scraper, one certificate further along.

The certificate grants nothing at the hub by itself. It is a client
certificate the node checks against a CA, not a principal the policy
consults; a node does not read `policy.yaml`. If you would rather the
same identity never be usable as an operator, bind it to no role and it
can do nothing but present itself.

#### The node's serving certificate, issued on the hub

The simplest, and one trust root for everything. The enrollment CA's key
is already on the hub, so issue there and copy the result out:

```sh
halite-node call x509.create_private_key \
    path=/tmp/node1-metrics.key algo=ec keysize=256

halite-node call x509.create_certificate \
    private_key=/tmp/node1-metrics.key \
    path=/tmp/node1-metrics.crt \
    signing_cert=/usr/local/etc/halite/pki/ca.crt \
    signing_private_key=/usr/local/etc/halite/pki/ca.key \
    CN=node1.example \
    days_valid=90 \
    ext_key_usage='["serverAuth"]' \
    subject_alt_names='["DNS:node1.example","IP:10.0.0.11"]'
```

```sh
$ openssl x509 -in /tmp/node1-metrics.crt -noout -subject -issuer -ext extendedKeyUsage,subjectAltName
subject=CN=node1.example
issuer=CN=halite enrollment CA
X509v3 Extended Key Usage:
    TLS Web Server Authentication
X509v3 Subject Alternative Name:
    DNS:node1.example, IP Address:10.0.0.11
```

Then the pair goes to the node, to the paths `metrics_tls_cert` and
`metrics_tls_key` name (`<pki_dir>/metrics.crt` and `metrics.key` in the
`node.yaml` above), the key mode 0600 and readable by the account the
agent runs as. Remove the copies in `/tmp` once they are there.
Prometheus verifies this certificate against the enrollment CA,
`ca.crt`. That is not the file the `halite` job trusts, which is
`api.crt`; see [Pointing Prometheus at the nodes](#pointing-prometheus-at-the-nodes).

**`ext_key_usage` and `subject_alt_names` are both required in
practice.** Without `serverAuth` Go refuses the certificate for serving
and the handshake fails with an incompatible key usage; without a SAN
covering the address in `targets` it fails verification. Neither error
mentions the setting that produced it. This is also why the node's own
`node.crt` cannot be reused — it has `clientAuth` and no SAN at all.

**Do not run this on the node.** It needs `ca.key`, and the enrollment
CA signs every identity in the estate: a copy on a managed machine is a
copy an attacker who takes that machine can mint node certificates
with. Issue on the hub, ship the result.

#### The node's serving certificate, managed by the tree

For an estate that would rather not copy files around, the same two
functions have state forms, and `days_remaining` renews before expiry.
A running agent serves a renewed certificate from the next connection on,
without a restart: it reads the two files on each handshake and loads them
again when either differs from the pair it is serving. A pair that will not load (the key
written and its certificate not yet) leaves the previous one in service
and is said once in the log. Until DIVERGENCE 5.248 the listener served
the pair it loaded at startup for as long as it ran, so a renewal reached
no scraper.
The signing key has to be where the state runs — on the node — so this
takes **a separate CA for metrics and nothing else**, never the
enrollment CA. A metrics CA that leaks signs metrics certificates; the
enrollment CA that leaks is the estate.

Make it once, on the hub or wherever you keep such things:

```sh
halite-node call x509.create_private_key \
    path=/usr/local/etc/halite/pki/metrics-ca.key algo=ec keysize=256
halite-node call x509.create_certificate \
    private_key=/usr/local/etc/halite/pki/metrics-ca.key \
    path=/usr/local/etc/halite/pki/metrics-ca.crt \
    ca=true CN='halite metrics CA' days_valid=3650
```

Put both in pillar as PEM text, the key GPG-encrypted like any other
secret:

```yaml
# pillar/metrics_ca.sls
metrics_ca:
  cert: |
    -----BEGIN CERTIFICATE-----
    ...
    -----END CERTIFICATE-----
  key: |
    -----BEGIN PRIVATE KEY-----
    ...
    -----END PRIVATE KEY-----
```

`signing_cert` and `signing_private_key` each take a path or the PEM
text itself, so the pillar values go to the state directly. Nothing
writes the CA's key to a file on the node. They must go through
`| json`. A PEM block is several lines, and substituted bare it puts its
second line at the start of a YAML line, where the state no longer
parses. `| json` writes it as one quoted string with its line breaks
escaped.

The state asks the node where its key material lives rather than naming
a directory. That is `/usr/local/etc/halite/pki` on FreeBSD and
`/etc/halite/pki` on Linux, and whatever `pki_dir` says where it is
set. The state does not create the directory unless it is given
`makedirs: true`, and it does not need to: an enrolled node already has
it, because its own certificate is there. A path written for the other
platform fails with `Parent directory not present: <directory>`. Before
DIVERGENCE 5.246 the same mistake failed with `no such file or
directory`, naming the temporary file the key is written through first. Until DIVERGENCE 5.242,
`config.get pki_dir` answered None on a node whose configuration did not
set it, so this had to be a literal path. The state becomes:

```yaml
# states/metrics_cert.sls
{% set pki = salt['config.get']('pki_dir') %}
{{ pki }}/metrics.key:
  x509.private_key_managed:
    - algo: ec
    - keysize: 256
    - mode: '0600'

{{ pki }}/metrics.crt:
  x509.certificate_managed:
    - private_key: {{ pki }}/metrics.key
    - signing_cert: {{ pillar['metrics_ca']['cert'] | json }}
    - signing_private_key: {{ pillar['metrics_ca']['key'] | json }}
    - CN: {{ grains['id'] }}
    - subject_alt_names:
        - 'DNS:{{ grains['id'] }}'
    - ext_key_usage:
        - serverAuth
    - days_valid: 90
    - days_remaining: 30
    - mode: '0644'
    - require:
        - x509: {{ pki }}/metrics.key
```

**It sets the mode and not the owner.** A key file that already exists
keeps the owner it has, so one made earlier by hand under a login
account stays that account's, at mode 0600: in this estate two of four
nodes had their metrics key owned by a login user when the state first
ran. Both x509 states take `user` and `group`; add them for your
platform (`group: wheel` on FreeBSD, `group: root` on Linux) if that
matters to you. They are left out above because the page's own test
applies this block as an unprivileged user, and a change of owner to root
would fail there.

It converges. A second run reports the certificate already in place and
changes nothing; a run inside the renewal window reissues and says so, and
so does a run after the state's subject, names or usages have changed,
naming what differs. Until DIVERGENCE 5.249 a changed name waited for the
renewal window: a certificate that matched its key and its CA was "already
in place" whatever it said.

```
Comment: A certificate was written to /usr/local/etc/halite/pki/metrics.crt,
         because it expires in under 30 days, on 2026-09-25T01:04:56Z.
```

Prometheus then verifies every node against `metrics-ca.crt` rather
than the enrollment CA, and `metrics_client_ca` stays pointed at
`ca.crt` — the two are different trust roots doing different jobs, and
that is the arrangement, not a mistake.

The certificate names the node ID and nothing else, so a node scraped
by this route has to appear in `targets` under its node ID.

**And the Prometheus host has to resolve that name to an address the
node listens on.** Neither is guaranteed. A node ID can have no DNS
record at all, and a node can listen only on a private address
(`metrics_listen: '10.0.0.11:4512'`) while its name resolves to a public
one; Prometheus reports `no such host` for the first and a timeout for
the second. Two ways out. This estate is taking the first for one node,
and neither has yet been seen to bring a target up:

- An `/etc/hosts` entry for the node ID on the Prometheus host, managed
  from the tree with `host.present`. Only when nothing else on that host
  uses the name: the entry moves every connection to it, not only the
  scrape. A mail server's name, used by the host's own mail relay and by
  an external probe, is the case where it must not be done.
- A listener on an address the name already resolves to, with the
  firewall admitting only the Prometheus host.

Adding the reachable address to `subject_alt_names` and targeting that
address also works for TLS. This estate's mail server is scraped that
way: its address added to the state's `subject_alt_names`, the target
written as the address, and an `instance` label of the node ID so graphs
and alerts keep the name:

```yaml
      - targets: ['10.0.0.11:4512']
        labels:
          instance: 'mail.example:4512'
```

Both certificate paths were run end to end against a node and a real
Prometheus before being written down, but not this state's pillar form.
As first published it did not compile, because the PEM was substituted
without `| json` (DIVERGENCE 5.241). The block above is now taken from
this page and applied twice, against a pillar holding a real metrics CA,
by `TestTheDocumentedMetricsCertificateStateConverges`.

### Pointing Prometheus at the nodes

A second scrape job, because these are different targets with different
certificates from the one `halite-api` presents. It needs up to three
files of its own, copied out of halite's directories for the reason
given under [Point Prometheus at it](#4-point-prometheus-at-it). Run
this as root where they were made, which is the hub for all three, and
carry the results to the Prometheus host if that is a different
machine:

**`halite-nodes-ca.crt` is whichever CA signed the nodes' serving
certificates, and the two routes above use different ones.** Get this
wrong and every node target fails with `x509: certificate signed by
unknown authority`, which names no file. Run exactly one of these:

```sh
# Certificates from the state above (the tree manages them): the
# metrics CA.
install -o root -g wheel -m 0644 \
    /usr/local/etc/halite/pki/metrics-ca.crt \
    /usr/local/etc/prometheus/halite-nodes-ca.crt

# Certificates issued on the hub and copied out: the enrollment CA.
install -o root -g wheel -m 0644 \
    /usr/local/etc/halite/pki/ca.crt \
    /usr/local/etc/prometheus/halite-nodes-ca.crt
```

This page used to give the enrollment CA as the line to run and the
metrics CA as a comment after it, and an estate following the state
above copied the enrollment CA: all four node targets failed until it
was replaced (2026-10-08). `openssl verify -CAfile halite-nodes-ca.crt`
against a node's certificate says which one you have.

The client certificate, when the nodes need one:

```sh
# Only when the nodes set metrics_client_ca: the scraper's client
# certificate, from `keys operator create prometheus` above.
install -o root -g wheel -m 0644 \
    /usr/local/etc/halite/pki/operator-prometheus.crt \
    /usr/local/etc/prometheus/scraper.crt
install -o prometheus -g prometheus -m 0600 \
    /usr/local/etc/halite/pki/operator-prometheus.key \
    /usr/local/etc/prometheus/scraper.key
```

Then the job:

```yaml
  - job_name: halite-nodes
    scheme: https
    scrape_interval: 30s
    metrics_path: /v1/metrics
    tls_config:
      ca_file: /usr/local/etc/prometheus/halite-nodes-ca.crt
      # Only when metrics_client_ca is set on the nodes.
      cert_file: /usr/local/etc/prometheus/scraper.crt
      key_file: /usr/local/etc/prometheus/scraper.key
    static_configs:
      - targets:
          - 'web1.example:4512'
          - 'web2.example:4512'
```

The address in `targets` has to be one the node's certificate covers,
the same trap as the API's — a certificate issued for the hostname and
scraped by address fails verification, and Prometheus reports that as a
target that is down rather than as a certificate problem. `server_name`
in `tls_config` is the way out when the two cannot be made to agree.

Every file named there has to be readable **by the account Prometheus
runs as**, which is not the one halite runs as: see the same warning
under [Point Prometheus at it](#4-point-prometheus-at-it) above, which
applies here unchanged.

### What is not fatal

Neither a missing certificate nor an address already in use stops the
agent. Both are warned about, naming the setting and the address, and
the node goes on running jobs. A node that refused to start over its
metrics certificate would be one no highstate could reach to fix the
certificate.

The consequence is that a misconfigured endpoint looks like a target
that is down, or one that was never created — which is why
`absent(up{job="halite-nodes"})` belongs in the rules alongside
anything watching `up`, for the reason given under
[Alerting](#alerting).

### Two things a node knows and the hub cannot

- **Drops.** A node's job queue, its queue of returns waiting for the
  hub, and each beacon's queue are all bounded, and what they discard
  never reaches the hub by definition. `halite_node_returns_dropped_total`
  climbing is a node whose answers are being thrown away, and the only
  other trace is a log line.
- **Where the time goes.** The hub sees one duration per job.
  `halite_state_compile_duration_seconds` and
  `halite_state_run_duration_seconds` split that into rendering the
  tree and applying it, which are the two halves that get slower for
  entirely different reasons. The hub records a family of the second
  name too, and it is the whole job rather than the apply — see
  [What is exposed](#what-is-exposed).

A node with no hub serves no metrics. Only `halite-node connect`
serves them, and `connect` will not start without a certificate from a
hub, so a masterless node — `state apply --local` from cron or
the highstate rc.d script — has no process for a scraper to reach.

## What is exposed

All families are prefixed `halite_`. Which process records one decides
where you read it: the hub's and the API's arrive in the scrape of
`halite-api`, and the node's arrive in a scrape of the nodes
themselves, under whatever you call that job.

### The hub

| Family | Type | Labels | What it says |
|---|---|---|---|
| `halite_build_info` | gauge | `component` `version` `commit` `go_version` `fips` | Always 1. What is deployed, per component. Every process exposes it. |
| `halite_hub_nodes_connected` | gauge | — | Nodes holding an open subscribe stream, relayed ones included. |
| `halite_hub_node_connect_total` | counter | — | Connections accepted. |
| `halite_hub_node_disconnect_total` | counter | `reason` | The other half, and why. |
| `halite_hub_keys_accepted` | gauge | — | Nodes holding an issued certificate. |
| `halite_hub_keys_pending` | gauge | — | Enrollment requests waiting for an operator. |
| `halite_hub_keys_expired` | gauge | — | Accepted nodes whose certificate has already run out. |
| `halite_hub_keys_expiring` | gauge | — | Accepted nodes whose certificate runs out within thirty days. |
| `halite_hub_soonest_certificate_expiry_seconds` | gauge | — | Seconds until the first node certificate expires; negative once one has. |
| `halite_hub_ca_expiry_seconds` | gauge | — | Seconds until the enrollment CA expires. Every node's identity is signed by it. |
| `halite_hub_enrollments_total` | counter | `result` | Enrollment requests: `issued`, `pending`, `refused`, `failed`. |
| `halite_hub_evidence_anchors_total` | counter | `result` | Evidence heads nodes reported: `accepted`, `conflict`, `rate_limited`, `failed`. A conflict is a node whose chain was rewritten, reset or rolled back since it last reported, and is worth an alert; `rate_limited` is a report refused because the node was over `evidence_anchor_rate`, which a node running many short jobs reaches in passing; `failed` is this hub not managing to record one. |
| `halite_hub_requests_total` | counter | `route` `code` | Requests the hub answered. |
| `halite_hub_request_duration_seconds` | histogram | `route` | How long it took to answer one. |
| `halite_jobs_dispatched_total` | counter | `fun` | Jobs sent, by function. |
| `halite_job_returns_total` | counter | `result` | Returns filed, by outcome. |
| `halite_job_duration_seconds` | histogram | `fun` | How long jobs take, dispatch to return. |
| `halite_jobs_missing_returns` | gauge | — | Nodes a dispatched job has not heard from, summed over the jobs still open. It rises on dispatch and falls when a node it was waiting on answers, and when the job ends without every answer: the hub's once-a-minute settling closes a job whose time to live has passed, and `jobs kill` and a batch stopped at its safe limit close theirs at once. A node that answers after its job was closed is recorded and moves nothing. A matched node that is not connected counts until the job expires. Held in memory: after a restart it counts only jobs this process dispatched or resumed, and returns for older jobs move nothing. |
| `halite_jobs_expired_total` | counter | — | Jobs whose time to live passed with a node that had not answered, never delivered or delivered and silent. Counted once per job when the hub settles it, which is up to a minute after the expiry. A killed job and a batch stopped at its safe limit are not counted. |
| `halite_state_states_total` | counter | `result` | Individual states applied, by outcome. |
| `halite_state_changes_total` | counter | — | States that changed something rather than converging. |
| `halite_state_run_duration_seconds` | histogram | — | Time a node spent on a state run end to end, out of its return. Compiling the tree is inside it. |
| `halite_state_compile_duration_seconds` | histogram | — | On the hub, time to compile an orchestration. |
| `halite_orch_runs_total` | counter | `result` | Orchestrations, by `complete`, `failed`, or `compile_failed`. |
| `halite_pillar_compile_duration_seconds` | histogram | — | Time to compile one node's pillar. |
| `halite_pillar_failures_total` | counter | — | Compilations that failed, so a node got no pillar. |
| `halite_pillar_ext_failures_total` | counter | `source` | External pillar sources that failed, by the name the configuration gave them, counted whether the failure stopped the compilation or was ignored: an ignored one is still a node whose pillar is missing what that source held. Before DIVERGENCE 5.196 a failure with metrics on panicked the pillar request instead of counting, and the node got no pillar at all. |
| `halite_fileserver_requests_total` | counter | `backend` `code` | Tree fetches. |
| `halite_fileserver_bytes_total` | counter | — | Bytes served. |
| `halite_events_published_total` | counter | `tag_prefix` | Events reaching the bus. |
| `halite_events_dropped_total` | counter | `reason` | Events that did not. |
| `halite_returns_foreign_schema_total` | counter | — | Returns whose schema this hub does not know, which means a node is newer than it. Non-zero while an upgrade is in flight and zero afterwards; non-zero afterwards is a node somebody forgot. |
| `halite_event_subscriber_lag_seconds` | histogram | — | How old an event was when a subscriber was handed it. |
| `halite_reactor_duration_seconds` | histogram | `tag_prefix` | Render and dispatch time for one reaction. |
| `halite_reactor_dropped_total` | counter | — | Reactions the queue could not hold. |
| `halite_reactor_failures_total` | counter | `reason` | Reactions that failed. |
| `halite_beacon_events_total` | counter | `beacon` | Beacon events received from nodes. |
| `halite_beacon_dropped_total` | counter | `beacon` | Beacon events a node's bounded queue discarded, from the overflow event it sends. |
| `halite_authz_decisions_total` | counter | `result` | Every policy decision, allowed and denied. |

### The API

| Family | Type | Labels | What it says |
|---|---|---|---|
| `halite_auth_attempts_total` | counter | `method` `result` | Logins, by backend: `accepted`, `refused`, `throttled` (the name was inside its backoff window, so nothing was checked), `busy` (every password-check slot was taken), and for LDAP `unmapped`. A sustained `throttled` is somebody guessing at a name. |
| `halite_api_requests_total` | counter | `route` `code` | API requests. |
| `halite_api_request_duration_seconds` | histogram | `route` | How long they take. |
| `halite_api_requests_in_flight` | gauge | `route` | Requests being answered right now. |
| `halite_api_response_bytes_total` | counter | `route` | Bytes written to clients. |
| `halite_api_event_streams` | gauge | `transport` | Event streams open, SSE and WebSocket. |
| `halite_api_stream_events_total` | counter | `transport` | Events delivered on those streams. |
| `halite_api_hook_deliveries_total` | counter | `path` `result` | Webhook deliveries received. |
| `halite_api_hub_requests_total` | counter | `route` `code` | Requests this service made to the hub. A zero code is a request that got none. |
| `halite_api_hub_request_duration_seconds` | histogram | `route` | How long the hub took to answer one. |
| `halite_api_hub_scrape_failures_total` | counter | — | Times the API could not read the hub's exposition. |
| `halite_api_tokens_issued_total` | counter | `method` | Tokens minted, by how the operator authenticated. |
| `halite_api_tokens_revoked_total` | counter | — | Tokens revoked by a logout. |
| `halite_api_tokens_live` | gauge | — | Tokens that exist and have not expired or been revoked. |

### A node

In the scrape of the nodes, not in the scrape of `halite-api`.

| Family | Type | Labels | What it says |
|---|---|---|---|
| `halite_node_connected` | gauge | — | 1 while the subscribe stream to the hub is open. |
| `halite_node_hub_reconnects_total` | counter | — | Times the stream was opened. The first connection counts. |
| `halite_node_hub_requests_total` | counter | `route` `code` | Requests this node made to the hub. |
| `halite_node_hub_request_duration_seconds` | histogram | `route` | How long the hub took to answer one. |
| `halite_node_jobs_total` | counter | `fun` `result` | Jobs this node ran. |
| `halite_node_job_duration_seconds` | histogram | `fun` | How long it spent on one. |
| `halite_node_jobs_refused_total` | counter | `reason` | Jobs it would not run: `replayed`, `expired`, `malformed`, `other`. `other` is a full job queue, or a job without the signature `require_job_signature` asks for. |
| `halite_node_job_queue_depth` | gauge | — | Jobs waiting for the executor. |
| `halite_node_return_queue_depth` | gauge | — | Returns waiting to be posted. |
| `halite_node_returns_dropped_total` | counter | — | Returns discarded because that queue was full. |
| `halite_node_schedule_runs_total` | counter | `name` | Scheduled jobs started, by schedule entry. |
| `halite_node_evidence_records_total` | counter | `kind` | Records appended to the node's SPEC 25.7 chain: `job.accepted`, `job.refused`, `job.result`, `config`, `extension`, `node.start`, `node.stop` and `anchor.receipt`. Each kind is exported at 0 from the start when `evidence` is on, and none when it is off. |
| `halite_node_evidence_failures_total` | counter | — | Evidence records that could not be written, which are jobs with no entry in the record. |
| `halite_node_evidence_log_open` | gauge | — | 1 when the node has its evidence chain open for writing; 0 when it is configured to keep one and the log failed to open, which is logged once and counted nowhere else. Absent when `evidence` is off, so 0 is never a choice. |
| `halite_node_jobs_accepted_total` | counter | — | Jobs this node accepted to run: past the replay guard and the signature check, counted where the evidence record for the acceptance is written. A job refused afterwards, because the queue is full, was accepted first. Unlabelled, so it is exported at 0 and `increase()` sees the first job; `halite_node_jobs_total` is the finished jobs, by function and outcome. |
| `halite_state_compile_duration_seconds` | histogram | — | Time to turn the tree into a low state. |
| `halite_state_run_duration_seconds` | histogram | — | Time to apply it, not counting the line above. |
| `halite_ext_invocations_total` | counter | `name` `result` | Extension calls: `succeeded`, `failed`, `timed_out`. |
| `halite_ext_duration_seconds` | histogram | `name` | How long one took. |
| `halite_ext_timeouts_total` | counter | `name` | The ones that ran out of time, counted again on their own. |
| `halite_beacon_events_total` | counter | `beacon` | Beacon events produced. |
| `halite_beacon_dropped_total` | counter | `beacon` | Beacon events the bounded queue discarded. |
| `halite_beacon_rate_limited_total` | counter | `beacon` | Beacon events the rate limit refused, which is not a loss. |
| `halite_beacon_failures_total` | counter | `beacon` | Beacon polls that failed. |
| `halite_beacon_queue_depth` | gauge | — | Beacon events waiting to be sent. |

`halite_beacon_events_total`, `halite_beacon_dropped_total`,
`halite_state_run_duration_seconds` and
`halite_state_compile_duration_seconds` are recorded on both sides. That
is deliberate, and they are not the same number. For the beacon
families and `halite_state_run_duration_seconds`, the hub's are the
estate totalled from what nodes reported; the node's are that one
machine, and a node nobody scrapes still contributes to the hub's.
`halite_state_compile_duration_seconds` shares its name and nothing
else: on the hub it is compiling an orchestration on the hub, and no
node's compile time reaches it.

`halite_state_run_duration_seconds` differs in a second way, and it is
worth knowing before the two are put on one graph. A return carries one
duration for the job, so the hub's is **end to end** and compiling the
tree is inside it. The node's is the **apply alone**, with
`halite_state_compile_duration_seconds` beside it as the other half.
The hub's number is therefore the larger, by roughly the compile time —
which is the quantity the split exists to expose.

Every family is declared before anything is observed, so its `# HELP`
and `# TYPE` are in the exposition from the start. What follows them
depends on whether the family carries labels:

- **Unlabelled**, and never fired: one series at zero —
  `halite_reactor_dropped_total 0`. An alert on it works from the first
  scrape.
- **Labelled**, and never fired: the declaration and no series at all,
  because there is no label value to name one. Prometheus has no data
  for it until something is observed, so `increase(...) > 0` cannot
  fire — which is the answer you want, but not because the counter said
  zero.

The distinction matters when a rule is not firing and you are deciding
whether that is good news. `halite-hub metrics --filter` keeps the
`# HELP` lines, which is how to tell a family that is quiet from one
this build never registers.

### Families that appear only when the feature runs

Registered by the subsystem that owns them, because a gauge reading a
queue that does not exist would read zero for ever:

| Family | Appears when |
|---|---|
| `halite_reactor_queue_depth` | `reactor:` has entries and the reactor is running |
| `halite_beacon_queue_depth`, `halite_node_job_queue_depth`, `halite_node_return_queue_depth` | the node is the running agent, and for the first, has beacons |
| `halite_node_evidence_log_open` | the node is the running agent and `evidence` is on, which it is by default |
| `halite_relay_subordinates`, `halite_relay_upstream_connected`, `halite_relay_spool_entries`, `halite_relay_spool_dropped_total`, `halite_relay_returns_forwarded_total`, `halite_relay_events_forwarded_total` | `relay: true` |

An alert against one of these on a hub that is not a relay never fires,
and not because nothing is wrong.

The gitfs three — `halite_gitfs_fetch_duration_seconds`,
`halite_gitfs_signature_failures_total` and
`halite_gitfs_refusals_total` — are not among them. They are declared
with the rest of the hub's families, so they are in the exposition of
any hub with metrics on; they simply never move on one whose
`fileserver_backend` names no `git`.

### What SPEC 26.2 names and this build does not have

One of the specification's thirty-two families is not registered, so an
alert written from its table rather than from this one sits silent:
`halite_pillar_cache_hits_total`. [DIVERGENCE 5.23](DIVERGENCE.md)
records why: there is no pillar cache to count hits in. It waits on a
feature rather than on a counter.

Ten that were on this list are registered now. An alert written against
one of those before it existed did not error — it stayed silent, which
is what it would have done if the estate were healthy, and that is the
reason this section exists at all.

## The series cap

A family holds at most 512 series. Past that, further label values are
counted together under `__overflow__` rather than dropped, so the total
stays right and the overflow is visible:

```
halite_jobs_dispatched_total{fun="__overflow__"} 1483
```

Seeing it means a label is taking unbounded values — most likely `fun`
on an estate running hundreds of distinct functions. The number is still
correct; the breakdown is not. There is no setting: an exposition that
can grow without limit is a way to run the scraper out of memory from
the estate being scraped.

## Monitoring

What is worth a dashboard, in rough order of how often it answers a
question:

- **`halite_hub_nodes_connected`** against `halite_hub_keys_accepted`.
  The gap is machines that should be talking and are not, and it is the
  single most useful number on the estate.
- **`rate(halite_jobs_dispatched_total[5m])` by `fun`.** What the estate
  is actually doing.
- **`halite_state_changes_total`.** On a converged estate this is flat
  between deployments. A step means something changed; a slope means
  something is not converging and is being re-applied every run.
- **`histogram_quantile(0.95, rate(halite_job_duration_seconds_bucket[5m]))`
  by `fun`.** Which functions are slow, and whether that is new.
- **`halite_hub_keys_pending`.** Enrollment requests nobody has decided
  on. On a manual-enrollment estate this should return to zero.
- **`halite_hub_ca_expiry_seconds`.** The one number whose reaching zero
  takes the whole estate with it, and the one certificate nothing
  renews on a timer. `halite_hub_keys_expiring` is the same question
  about the nodes.
- **`halite_api_request_duration_seconds` against
  `halite_api_hub_request_duration_seconds`.** Two lines on one graph.
  Where they move together the hub is the slow half; where only the
  first moves, the API is.

## A dashboard to start from

[`contrib/examples/grafana-dashboard.json`](../contrib/examples/grafana-dashboard.json)
is an importable Grafana dashboard over these families. In Grafana:
**Dashboards → New → Import → Upload JSON file**, then pick the
Prometheus that scrapes halite-api.

**To pick up a newer version of the dashboard, import it again and
choose Overwrite** when Grafana says the `uid` exists. Pasting the new JSON
into an existing dashboard's JSON Model and saving can fail with
`precondition failed: StorageError ... UID in precondition ... UID in object
meta`, because the stored object has been replaced since the page loaded
(seen once, on a Grafana whose version was not recorded; re-importing with
Overwrite fixed it). Re-importing replaces the stored dashboard, so edits
made in Grafana itself are lost: keep your own changes in a copy.

It asks for one thing on import, the data source. Four variables at the
top pick what you are looking at:

| Variable | What it is |
|---|---|
| `job` | the scrape job name in `prometheus.yml`, `halite` unless you renamed it |
| `instance` | which `halite-api` to read, when more than one is scraped |
| `node_job` | the scrape job for the nodes themselves, `halite-nodes` in the example above |
| `node_instance` | which nodes the node row covers |

The last two exist because a node's metrics do not arrive in the same
scrape: the nodes are their own targets with their own certificates,
and a node panel filtered by the API's job would draw an empty graph on
every estate — which is checked by a test.

The rows: fleet health, certificates and enrollment, jobs, states and
orchestration, pillar, events and reactions, the file server,
authentication and policy, evidence anchors, the hub's own service metrics, the API's,
two collapsed rows for the node agents and for relays, and the build
row. Every panel
carries a description saying what a reading means — hover the title.

Four panels are worth knowing about before you need them:

- **Scrape** reads `up`, and is empty rather than 0 when the scrape pool
  failed to build. An empty stat there means the whole dashboard is
  showing nothing for a reason that has nothing to do with halite.
- **Hub scrape failures** is the one that says the dashboard is lying to
  you: while it climbs, every `halite_hub_*` panel is empty because the
  API could not read the hub's half, not because the fleet is idle.
- **Where the time goes**, in the API row, is two lines: how long the
  API took to answer and how long the hub took to answer the API. Where
  they move together the hub is the slow half.
- **CA expires in**, in the certificate row, is the number whose
  reaching zero takes the estate with it.

The node row is collapsed because it is empty unless some node sets
`metrics_listen` and something scrapes it. The relay row is
collapsed because those families exist only on a hub running with
`relay: true`. An empty panel in either place on an estate that does
neither is correct.

Four tests hold the file to being importable and to being about this
build: every query names a family that is registered, every panel has
its own identifier, no panel overlaps another or runs past the
twenty-four columns the grid has, and every panel carries a
description. A panel written against a metric that does not exist draws
an empty graph rather than an error, which looks exactly like a fleet
with nothing happening in it; two panels sharing an identifier import
without complaint and misbehave afterwards. The thresholds and the
panel choices have not been tuned against a large estate; they are a
starting point.

## Alerting

Every metric in the rules below exists in this build, which is checked
by a test, and the block is accepted by `promtool check rules`. They
have not been evaluated against live data, so the thresholds are a
starting point rather than tuned figures.

```yaml
groups:
  - name: halite
    rules:
      # First, that the scrape exists at all. A scrape pool that fails
      # to build — an unreadable ca_file or token — registers no target,
      # so `up` is absent rather than 0 and every other rule here goes
      # quiet without firing. This is the only rule that catches that.
      - alert: HaliteScrapeMissing
        expr: absent(up{job="halite"})
        for: 5m
        labels: {severity: critical}
        annotations:
          summary: "No halite target exists; check prometheus.yml and the log"

      - alert: HaliteScrapeDown
        expr: up{job="halite"} == 0
        for: 5m
        labels: {severity: critical}
        annotations:
          summary: "halite-api is not answering the scrape"

      # Nothing should ever be dropped. Both of these are bounded
      # queues whose overflow SPEC 26.2 requires be counted.
      - alert: HaliteEventsDropped
        expr: increase(halite_events_dropped_total[10m]) > 0
        labels: {severity: warning}
        annotations:
          summary: "The event bus dropped {{ $value }} events"

      - alert: HaliteReactorDropped
        expr: increase(halite_reactor_dropped_total[10m]) > 0
        labels: {severity: warning}
        annotations:
          summary: "Reactions were discarded; the queue overflowed"

      # A node getting no pillar. SPEC 12.7 prefers that to a partial
      # one, which makes this the signal that something is wrong rather
      # than quietly half-configured.
      - alert: HalitePillarFailing
        expr: increase(halite_pillar_failures_total[15m]) > 0
        for: 5m
        labels: {severity: critical}
        annotations:
          summary: "The hub cannot compile pillar for some node"

      # Machines that hold a certificate and are not connected.
      - alert: HaliteNodesAbsent
        expr: halite_hub_keys_accepted - halite_hub_nodes_connected > 0
        for: 15m
        labels: {severity: warning}
        annotations:
          summary: "{{ $value }} enrolled nodes are not connected"

      # A rate rather than a total: a rise is either a role somebody
      # got wrong or somebody trying.
      - alert: HaliteAuthzDenials
        expr: rate(halite_authz_decisions_total{result="denied"}[5m]) > 0.2
        for: 10m
        labels: {severity: warning}

      - alert: HaliteLoginRefusals
        expr: rate(halite_auth_attempts_total{result="refused"}[5m]) > 0.2
        for: 10m
        labels: {severity: warning}

      # The API cannot read the hub. The scrape still succeeds and
      # returns the API's own numbers, so nothing else tells you.
      - alert: HaliteHubUnreadable
        expr: increase(halite_api_hub_scrape_failures_total[10m]) > 0
        labels: {severity: critical}
        annotations:
          summary: "The API cannot reach the hub's exposition"

      # Jobs dispatched that a node has not answered for fifteen
      # minutes. A job leaves the gauge when it ends -- answered,
      # expired, killed, or stopped at its safe limit -- so this clears
      # by itself once the job is over. A job with a long --ttl, or a
      # queued one waiting an hour for an absent node, holds it up for
      # as long as it waits; read `jobs active` and `jobs missing`
      # before acting on it.
      - alert: HaliteJobsUnanswered
        expr: halite_jobs_missing_returns > 0
        for: 15m
        labels: {severity: warning}

      # A label taking unbounded values. The totals stay right; the
      # breakdown stops being useful. Past the cap every label of the
      # family reads __overflow__, but PromQL has to name a label to
      # match it, so this watches `fun` — the likeliest — and an
      # overflow in `route`, `tag_prefix`, `beacon`, `name` or `source`
      # needs its own line.
      - alert: HaliteSeriesOverflow
        expr: count({__name__=~"halite_.*", fun="__overflow__"}) > 0
        labels: {severity: info}

      # Certificates. An estate meets this all at once: a batch
      # enrolled on one afternoon a year ago expires on one afternoon
      # this year, and `halite_hub_keys_accepted` does not fall for
      # them, because the record is still there.
      - alert: HaliteCAExpiringSoon
        expr: halite_hub_ca_expiry_seconds < 60 * 60 * 24 * 60
        for: 1h
        labels: {severity: critical}
        annotations:
          summary: "The enrollment CA expires in under sixty days; every node's identity is signed by it"

      - alert: HaliteCertificatesExpiring
        expr: halite_hub_keys_expiring > 0
        for: 1h
        labels: {severity: warning}
        annotations:
          summary: "{{ $value }} node certificates expire within thirty days"

      - alert: HaliteCertificatesExpired
        expr: halite_hub_keys_expired > 0
        for: 15m
        labels: {severity: warning}
        annotations:
          summary: "{{ $value }} accepted nodes hold a certificate that has already expired"

      # A node's beacon queue is bounded, and what it discards never
      # reaches the hub except as the overflow event this counts.
      # A beacon's first drop is not seen: its series is born at 1, and
      # beacon names are not known in advance to start them at 0.
      - alert: HaliteBeaconEventsDropped
        expr: increase(halite_beacon_dropped_total[10m]) > 0
        labels: {severity: warning}
        annotations:
          summary: "{{ $labels.beacon }} lost {{ $value }} events to a full queue on some node"

      # A ref that fails verification is not served, which SPEC 13.3
      # makes a control rather than a warning. Silence here is a tree
      # the estate has quietly stopped applying.
      - alert: HaliteGitSignatureFailures
        expr: increase(halite_gitfs_signature_failures_total[15m]) > 0
        labels: {severity: critical}
        annotations:
          summary: "A git ref is not served because its signature did not verify"

      # A subscriber that is steadily behind the bus. A reconnection
      # from an old offset is a spike and is not this.
      - alert: HaliteSubscribersBehind
        expr: histogram_quantile(0.95, rate(halite_event_subscriber_lag_seconds_bucket[5m])) > 120
        for: 15m
        labels: {severity: warning}
        annotations:
          summary: "Event subscribers are two minutes behind the bus"

      # An orchestration that never ran because its tree would not
      # compile. Nothing was attempted anywhere, and the operator who
      # started it may not be watching.
      - alert: HaliteOrchestrationWillNotCompile
        expr: increase(halite_orch_runs_total{result="compile_failed"}[15m]) > 0
        labels: {severity: warning}

      # A node reported an evidence head that contradicts what this hub
      # recorded: its chain was rewritten, reset or rolled back since it
      # last reported (SPEC 25.7). The series carries no node label --
      # one per node would be unbounded -- so the node is in the hub's
      # log and in the halite/node/<node>/evidence/conflict event, and
      # the finding is the hub's anchor file. A snapshot restore and
      # removing a node's evidence directory both do this innocently;
      # operations.md says how to tell and how to start the record again.
      - alert: HaliteEvidenceConflict
        expr: increase(halite_hub_evidence_anchors_total{result="conflict"}[15m]) > 0
        labels: {severity: critical}
        annotations:
          summary: "A node's evidence chain contradicts the hub's record of it; see the hub log"

      # The hub could not write an anchor line, so the node got no
      # receipt and its head is not recorded. Usually a full or
      # read-only state directory.
      - alert: HaliteEvidenceNotRecorded
        expr: increase(halite_hub_evidence_anchors_total{result="failed"}[10m]) > 0
        labels: {severity: warning}
        annotations:
          summary: "The hub failed to record {{ $value }} evidence heads"
```

One of these reads a family that only moves on a hub with the feature
running — `halite_gitfs_signature_failures_total` needs
`fileserver_backend` to name `git`. The family is declared either way,
so the rule is quiet rather than absent, and quiet is correct on a hub
with no git remote.

On a relay, add its spool — an outage that is not draining is the thing
you want to know about before the cap is reached:

```yaml
      - alert: HaliteRelaySpoolGrowing
        expr: halite_relay_spool_entries > 0 and halite_relay_upstream_connected == 1
        for: 15m
        annotations:
          summary: "The relay is connected upstream and its spool is not draining"
```

On an estate scraping its nodes, eight more. The first two are the
node scrape itself, for the reason given under
[What is not fatal](#what-is-not-fatal): a node whose metrics
certificate is missing or unreadable keeps running and serves nothing,
so its target is down or never exists. The other six read the nodes'
own job rather than the hub's, and they are the only place their
subject appears at all — a return a node discarded never reached the
hub to be counted there, and neither did a record it failed to write:

```yaml
      - alert: HaliteNodeScrapeMissing
        expr: absent(up{job="halite-nodes"})
        for: 5m
        labels: {severity: critical}
        annotations:
          summary: "No halite-nodes target exists; check prometheus.yml and the log"

      - alert: HaliteNodeScrapeDown
        expr: up{job="halite-nodes"} == 0
        for: 5m
        labels: {severity: warning}
        annotations:
          summary: "{{ $labels.instance }} is not answering the node scrape"

      - alert: HaliteNodeReturnsDropped
        expr: increase(halite_node_returns_dropped_total[10m]) > 0
        labels: {severity: critical}
        annotations:
          summary: "{{ $labels.instance }} threw away {{ $value }} job returns; its queue to the hub is full"

      - alert: HaliteNodeJobsRefused
        expr: rate(halite_node_jobs_refused_total{reason!="replayed"}[10m]) > 0
        for: 15m
        labels: {severity: warning}
        annotations:
          summary: "{{ $labels.instance }} is refusing jobs: {{ $labels.reason }}"

      - alert: HaliteExtensionTimeouts
        expr: increase(halite_ext_timeouts_total[15m]) > 0
        labels: {severity: warning}
        annotations:
          summary: "The extension {{ $labels.name }} ran out of time on {{ $labels.instance }}"

      - alert: HaliteNodeEvidenceNotWritten
        expr: increase(halite_node_evidence_failures_total[10m]) > 0
        labels: {severity: critical}
        annotations:
          summary: "{{ $labels.instance }} could not write its evidence record; those jobs have no entry"

      - alert: HaliteNodeEvidenceStopped
        expr: |
          sum by (instance) (increase(halite_node_jobs_accepted_total[1h])) > 0
          and on (instance)
          sum by (instance) (increase(halite_node_evidence_records_total[1h])) == 0
        for: 30m
        labels: {severity: critical}
        annotations:
          summary: "{{ $labels.instance }} is running jobs and recording none of them"

      # The node is configured to keep an evidence record and its log is
      # not open: it failed to open at startup, said so once in the log,
      # and counted nothing. Every other evidence alert is quiet for this
      # node. The gauge does not exist on a node with `evidence: false`, so
      # a node that chose to keep no record is never matched.
      - alert: HaliteNodeEvidenceNotKept
        expr: halite_node_evidence_log_open == 0
        for: 5m
        labels: {severity: critical}
        annotations:
          summary: "{{ $labels.instance }} is configured to keep an evidence record and is keeping none"
```

The second evidence alert is the shape that matters, and it is the one
worth copying elsewhere. A node whose record has quietly stopped being
written emits no signal of its own: nothing fails, nothing is refused,
and the failure counter above stays at zero because nothing is even being
attempted. What contradicts it is another series that is still moving —
jobs are being run, and no records are appearing — which is alerting on
an absence rather than on a badness.

The two sides are summed to `instance` and joined with `on (instance)`
because `and` otherwise matches on every label, and the jobs series
carries `fun` and `result` where the records series carries `kind`: the
sides never matched, and the rule as first written could not fire even for
a node whose record had stopped (DIVERGENCE 5.236).

**This rule is silent on a node that has no `halite_node_evidence_records_total`
series at all, and that is a decision, not an oversight.** A node that keeps
a record exports every record kind at 0 from the start; a node with
`evidence: false` exports none, so it has no series, and the rule does not
fire on it: such a node keeps no record by the operator's choice, and a
critical alert on a choice would be ignored within a day.

A node that is configured to keep a record and whose log failed to open
has the kinds at 0 and no records, so if it accepts jobs this rule fires
for it too. The gauge `halite_node_evidence_log_open` and
`HaliteNodeEvidenceNotKept` above report that same node sooner and say why,
and neither this rule nor `HaliteNodeEvidenceNotWritten` could have seen it
before they existed.

Both sides of the comparison are counters that start at 0. The jobs side
reads `halite_node_jobs_accepted_total`, which is unlabelled. It used to
read `halite_node_jobs_total`, labelled by function: on a node that has just
started, a labelled counter's first series is born at 1, `increase()` of it
does not count that first job, and a node that ran exactly one job and then
stopped recording was invisible. The records side needed the same: a
node's first job ever writes `job.accepted` and `job.result` as new series
born at 1, every kind's `increase()` reads 0, and the rule fired on a node
that had recorded the job correctly. The record kinds are a closed set,
unlike function names, so each is exported at 0 (DIVERGENCE 5.239).

`reason!="replayed"` on `HaliteNodeJobsRefused` is deliberate: a replayed job is
the guard of SPEC 6.3 doing its work, and a hub retrying a delivery is
the ordinary cause. The other three reasons are not.

### What not to alert on

`halite_state_states_total{result="failed"}` looks tempting and is
usually noise: a state that fails once because a package mirror was
briefly down is not an incident. Alert on the rate over a window long
enough to survive one bad run, or on the estate not converging —
`halite_state_changes_total` still climbing hours after a deployment.

## When the hub cannot be reached

The scrape still succeeds. The API returns its own numbers, and the
reason the hub's are missing appears as a comment, which a scraper
ignores:

```
# the hub's metrics are absent: /v1/metrics: no rule in [apionly] permits metrics.show against ""
halite_api_hub_scrape_failures_total 1
```

The counter increments in the scrape that failed, not the one after —
checked by scraping twice against a hub the API was not permitted to
read and watching it go 1, 2. That is why `HaliteHubUnreadable` above
uses `increase(...) > 0` rather than waiting for a gap in the data:
there is no gap, and every hub metric simply stops being reported.

## Reading them without a scraper

```sh
halite-hub metrics --as ed
halite-hub metrics --as ed --filter reactor
```

`--filter` keeps the `# HELP` and `# TYPE` lines, which `grep` loses and
which say whether a counter exists at all — the difference between a
metric that is quiet and one that was never registered.
