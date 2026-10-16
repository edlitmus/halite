# Runbook: installing the API and giving people access

<!-- abstract: Running halite-api in front of the hub: its two certificates, its policy role, the service, accounts and tokens, and handing out access safely. -->
<!-- kind: runbook -->
<!-- index: install api; halite-api serve; api.yaml; api_operator; tls_cert; serving certificate; accounts.yaml; account hash; token revoke; halite-api doctor; reverse proxy; LDAP; OIDC; access for people; break-glass -->

**Use this runbook to** put the HTTP API in front of a hub, so that CI
pipelines, dashboards, chat bots and people without a shell on the hub can
drive the fleet, within limits you set.

**You will end with** `halite-api` running as a service, a first account that
can log in and run one narrow command, and a policy in which the API's own
authority is bounded.

> **What has and has not been run.** Every step below was run as root on
> FreeBSD 15.1-RELEASE-p4, from the published v0.1.1 archive: the `api`
> operator, the policy role and binding, a self-signed serving certificate, an
> account hash, `api.yaml`, the rc.d script, `halite-api doctor` (`3 pass, 1
> skip`), `/v1/readyz` (`ready`), a login, `POST /v1/run` of `status.uptime`
> (answered by a node), `GET /v1/nodes`, and a reboot, after which the hub and
> the API both came back. **On Linux** (Ubuntu 26.04, the shipped
> `halite-api.service`) the same steps ran, and the check this chapter asks for
> was made: an account bound to a one-function role got `200` for
> `status.uptime`, and `403` with `no rule in [readonly] permits cmd.run against
> "web*"` for `cmd.run` and for `test.ping`, and a wrong password got `401 those
> credentials were not accepted`; after a reboot `readyz` answered `ready`.
> **Not run:** LDAP, OIDC and TOTP, and a role map for a directory group.

## Before you start

- A **working hub** and an operator ([chapter 30](30-install-hub.md)).
- The API runs **beside** the hub or anywhere it can reach TCP 4510. It needs
  no inbound connection from nodes.
- **Two certificates**, which come from different places and are easy to
  confuse:

  | Certificate | Presented to | Comes from |
  |---|---|---|
  | **Serving certificate** (`tls_cert`, `tls_key`) | the API's *clients*: browsers, `curl`, scrapers | **Your** HTTPS PKI: an internal CA, ACME, the estate's tooling. *Not* the enrollment CA, and no halite command issues it. |
  | **Operator certificate** (`api_operator`) | the *hub* | `halite-hub keys operator create api` |

  The first is deliberately not from the enrollment CA: that would mean
  teaching every HTTPS client to trust the authority that issues node
  identities, a much larger thing to trust than a web server needs.
- A **decision about who logs in how**: OIDC for people if you have an
  identity provider, LDAP if you have a directory, local accounts for
  break-glass and automation only.
- A reverse proxy is optional. The API speaks HTTP/2 and HTTP/1.1 over TLS 1.3
  directly.
- 15 minutes.

## Procedure

**1. Make the API's operator certificate**, on the hub, as its account:

```sh
sudo -u halite halite-hub keys operator create api
```

```
operator  api
principal cert:CN=api
cert      /etc/halite/pki/operator-api.crt
key       /etc/halite/pki/operator-api.key
expires   2026-11-10T19:18:15Z
```

From now on the `pki_dir` holds two operator certificates, and `halite-hub`
commands that used to find "the" operator refuse to guess: `halite: there are 2
operator certificates in … (api, ed); say which with --as`. Add `--as ed`
(chapter 14). The API's own certificate is not one you type commands with.

It lasts 30 days unless you pass `--lifetime`, **and nothing renews it**:
re-issue it with the same command before it expires
([chapter 37](37-certificates.md)). The API reads the pair again before each
request to the hub, so a re-issue needs no restart.

**2. Give the API a role in the policy.** The API is a *client* of the hub, so
its authority is whatever this binding grants. The API authorizes each caller
against the policy and the hub authorizes the API again, so the API's role has
to be at least as wide as the operators' and no wider than you want:

```yaml
# policy.yaml
roles:
  api-service:
    - target: '*'
      functions: ['test.ping', 'status.uptime', 'grains.item', 'state.apply']
    - runners: ['manage.status', 'manage.up', 'manage.down', 'jobs.list_jobs',
                'jobs.list_job', 'cache.grains', 'event.listen', 'event.send']
bindings:
  - principal: 'cert:CN=api'
    roles: ['api-service']
```

Start narrow and widen when a `403` names what is missing: its message says
`no rule in [api-service] permits manage.up against ""`. Note which runners
`GET /v1/nodes` needs: `cache.grains`, `manage.up`, `manage.down`. Restart the
hub to read the policy.

**3. Make the serving certificate** and put it, with its key, where the API
reads them. For a first run or a lab, self-signed is enough, and the names in
`subjectAltName` are what clients verify (the `CN` is ignored by every current
client):

```sh
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
    -keyout /etc/halite/pki/api.key -out /etc/halite/pki/api.crt \
    -days 365 -subj "/CN=$(hostname)" \
    -addext "subjectAltName=DNS:$(hostname),DNS:api.example.com,DNS:localhost,IP:127.0.0.1"
sudo chown halite /etc/halite/pki/api.key && sudo chmod 0600 /etc/halite/pki/api.key
```

In production use a certificate from the CA your clients already trust. The
API reads the files on each new connection, so renewing in place (ACME, a
state) needs no restart; a pair that will not load leaves the previous one
serving and is logged once.

**4. Write `api.yaml`:**

```yaml
# /etc/halite/api.yaml
listen: :4511
hub: hub.example.com:4510
api_operator: api
tls_cert: /etc/halite/pki/api.crt
tls_key:  /etc/halite/pki/api.key
accounts: /etc/halite/accounts.yaml
token_lifetime: 12h
token_idle: 4h
metrics: true
```

`hub` here is `host:port` in one setting. The API's `pki_dir` defaults to the
hub's directory on the same machine, which is where `operator-api.crt` is; on a
separate machine copy the operator pair and the **CA certificate** (`ca.crt`,
public) into the API's `pki_dir`, and nothing else from the hub's.

**5. Make a first account**, with a password hash that never touches a command
line:

```sh
sudo -u halite halite-api account hash
```

(it prompts with echo off; or `printf '…' | halite-api account hash` for
provisioning, which writes the verifier only)

```
pbkdf2-sha512$600000$NA8tWPw90G2qxQNp3ngKMA$G6LE1wf61dzDc8Xlz…
```

```yaml
# /etc/halite/accounts.yaml   mode 0600, owner halite, never in git
accounts:
  alice:
    hash: 'pbkdf2-sha512$600000$NA8t…'
    roles:
      - webops
```

and give `webops` a narrow role in `policy.yaml`
([chapter 26](26-authorization.md)). A role named here that does not exist
in the policy grants nothing, silently, so check with `halite-hub policy
show`. TOTP can be added per account.

**6. Install the service and start it.** The unit runs as the `halite`
account, with its token store under `StateDirectory`:

```sh
# Linux
sudo install -m 0644 service/systemd/halite-api.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now halite-api

# FreeBSD
sudo install -m 0555 service/rc.d/halite_api /usr/local/etc/rc.d/
sudo sysrc halite_api_enable=YES && sudo service halite_api start
```

**7. For directory or identity-provider login**, add the `ldap_*` or `oidc_*`
settings to `api.yaml` (a role map is mandatory: a group with no entry grants
nothing, and an issuer with no map is refused at start). The settings are in
the reference, and the complete examples in `examples/api.yaml`.

## Verify

```sh
halite-api doctor
```

```
halite-api doctor — /etc/halite/api.yaml

  pass  configuration validity           …/api.yaml loads, and matches what this process is running
  warn  certificate validity and expiry  the certificate this service presents expires in 2 days
                                         Renew before it lapses: …
  pass  connectivity                     127.0.0.1:14510 answered in 5ms: halite-hub 0.1.0-… ok
  pass  FIPS mode consistency            neither the kernel nor this build is in FIPS mode

  3 pass, 1 warn
```

(That warning was real: the lab's self-signed serving certificate had three
days of life, and `doctor` said so. This is what it is for.) Then use it as a
client would:

```sh
curl -s --cacert /etc/halite/pki/api.crt https://localhost:4511/v1/readyz
curl -s --cacert /etc/halite/pki/api.crt -X POST https://localhost:4511/v1/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"…","eauth":"local"}'
```

You should see `ready` and a JSON token. Run the one command alice is allowed
(`POST /v1/run` with `status.uptime` on `web*`) and the one she is not
(`cmd.run`), and confirm a `403` with a reason
([chapter 27](27-http-api.md) has the requests). A role you grant should be
**checked by trying to exceed it**.

## If it goes wrong

| Symptom | Cause | Fix |
|---|---|---|
| `this service needs a serving certificate; set tls_cert and tls_key` | The pair is not configured | Step 3 and 4 |
| `/v1/readyz` says `not ready` | The API cannot reach the hub (wrong `hub`, firewall, expired operator certificate) | `halite-api doctor` |
| `403 no rule in [api-service] permits … against ""` | The API's own role lacks a runner an endpoint uses internally | Add the named runner to `api-service`; restart hub **and API** |
| `403 no rule in [webops] permits …` | The person's role lacks it | Widen deliberately, or it is working |
| A policy change has no effect | The hub *and* the API each read the policy at start | Restart both |
| `curl` fails with no message under `-s` | The serving certificate lacks the name you used in `subjectAltName` | Re-issue with the name; `-v` shows the verification error |
| `401` for the right password | Typo in the hash, or the account file is not readable by the service account | `doctor`, the log (it says which of "no such account" and "wrong password" it was) |
| `429` on login | The name is in a backoff window after failures from this address, or this address has spent its budget of failures across names | Wait for `Retry-After`; it doubles to 15 minutes. Another address is not held |
| Tokens vanish after a restart | `state_dir` is not persistent | Check the token store is on durable storage |

To **disable a person immediately**: remove the account or role, then
`halite-api token revoke --principal alice` (a removed account does not
invalidate tokens already issued). To see who holds tokens: `halite-api token
list`.

## Key ideas

- Two certificates: **serving** (from your HTTPS PKI) and **operator** (from
  `keys operator create api`; not auto-renewed; re-issue before it expires).
- The API's authority is the **policy role of `cert:CN=api`**; it
  authorizes callers too, so both ends need the grant.
- **Restart the hub and the API** after any policy change.
- Use OIDC or LDAP for people; keep local accounts for break-glass and
  automation, with narrow roles.
- `halite-api doctor`, then a request **inside** and a request **outside** the
  role, is the acceptance test.

Next: [configuring halite](33-configuration.md).
