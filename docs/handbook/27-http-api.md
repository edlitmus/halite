# The HTTP API

<!-- abstract: Driving halite from other software: logging in, running functions and states, jobs, nodes, events as a stream, webhook ingress, and how the API is bounded by the policy. -->
<!-- kind: guide -->
<!-- index: HTTP API; halite-api; REST; /v1/login; /v1/run; /v1/jobs; /v1/nodes; /v1/events; server-sent events; websocket; /v1/hook; webhook ingress; /v1/token; /v1/schema; /v1/healthz; /v1/readyz; /v1/metrics; bearer token; client local; local_async; hooks; X-Halite-Signature; on_behalf_of; halite-api token; api.yaml -->

`halite-api` is the HTTP interface to the fleet: for dashboards, CI
pipelines, chat bots, anything that is not a person at a terminal. It does
what `halite-hub run` and its siblings do, over HTTPS and JSON.

It is a **separate program** and a separate identity. It is a *client of the
hub*, not a component of it: it holds its own operator certificate, talks to
the hub over the same mutual-TLS transport as any other operator, and keeps
no fleet state of its own. A flaw in the API is bounded by what that one
certificate's policy allows, not by the whole control plane.

Everything in this chapter was run against a lab API and the output is real.

## Running it

A minimal `api.yaml`:

```yaml
listen: 127.0.0.1:4511
hub: hub.example:4510
api_operator: api
tls_cert: /etc/halite/pki/api.crt
tls_key:  /etc/halite/pki/api.key
accounts: /etc/halite/accounts.yaml
token_lifetime: 12h
token_idle: 4h
```

| Setting | Meaning |
|---|---|
| `listen` | Address and port (default `:4511`). Bind it to loopback and put a proxy in front, or to the interface you serve. |
| `hub` | The hub, as `host:port` (note: a single setting, unlike `halite-node`'s `hub` plus `hub_port`). |
| `api_operator` | Which operator certificate (from `keys operator create`) it presents to the hub. |
| `tls_cert`, `tls_key` | The API's **serving** certificate. It does **not** come from the enrollment CA; use the CA your HTTPS clients already trust. |
| `accounts` | The local accounts file. |
| `token_lifetime`, `token_idle` | Absolute and idle expiry of tokens (defaults `12h` and `4h`). |
| `hooks` | Webhook ingress, below. |
| `ldap_*`, `oidc_*` | Directory and identity-provider login ([chapter 26](26-authorization.md)). |

Create the operator certificate the API presents (`halite-hub keys operator
create api`), give `cert:CN=api` a role in the policy, make a serving
certificate (a self-signed one is enough for a lab), and start it:

```sh
halite-api serve
```

```
info: api listening accounts=1 address=127.0.0.1:4511 hooks=1 hub=https://127.0.0.1:4510 …
```

`halite-api doctor` checks the three certificates it holds (the one it
presents to callers, the operator certificate it presents to the hub, and
the CA it verifies the hub with), that the hub answers, and FIPS
consistency, and changes nothing. TLS 1.3 only; HTTP/2 and HTTP/1.1; security
headers on every response; no directory listing, no stack traces, request
sizes limited before parsing.

The serving certificate and the operator certificate to the hub can each be
**replaced on disk without a restart** (the API reads them on each new
connection or request); the policy file and the accounts are read at
start.

## Health

```sh
curl --cacert api.crt https://localhost:4511/v1/healthz     # ok
curl --cacert api.crt https://localhost:4511/v1/readyz      # ready
```

These need no authentication and disclose no state. `readyz` answers
`{"error":"not ready"}` until the API can reach the hub, so it is the right
probe for a load balancer. The names in the serving certificate's
`subjectAltName` are what clients verify (the `CN` is ignored by every
current client), so put every name and address the API is reached by in it,
`localhost` and `127.0.0.1` included for testing: a certificate that
omits the one you used fails with no useful message under `curl -s`.

## Logging in

```sh
curl -s --cacert api.crt -X POST https://localhost:4511/v1/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"…","eauth":"local"}'
```

```json
{"token":"RHirG86-a3wwx7nZ4COMNJ32UwUA5yk9io88ui65SXA",
 "token_id":"1fea8e0b0ef1e56d9bfd71c58f770eb6",
 "principal":"local:alice","roles":["webops"],
 "expires":"2026-10-12T07:18:58Z"}
```

`eauth` chooses the method: `local`, `ldap`, or (for OIDC, which is a
redirect flow) `POST /v1/login/oidc` and `/v1/login/oidc/callback`. A failed
login returns one message however it failed:

```
HTTP/2 401
{"error":"those credentials were not accepted"}
```

Everything after sends the token in a header, and **only** there:

```sh
TOKEN=RHirG86-…
H='Authorization: Bearer '$TOKEN
```

Introspect it with `GET /v1/token`:

```json
{"token_id":"1fea8e0b…","principal":"local:alice","roles":["webops"],
 "issued":"2026-10-11T19:18:58Z","expires":"2026-10-12T07:18:58Z",
 "idle_expires":"2026-10-11T23:19:04Z","version":"0.1.0-7-g…"}
```

and revoke it with `POST /v1/logout`. Operators can list, show, revoke and
prune tokens from the host: `halite-api token list|show|revoke|prune`.

## Running functions

`POST /v1/run` runs a function and waits for the answer:

```sh
curl -s --cacert api.crt -H "$H" -H 'Content-Type: application/json' \
  -X POST https://localhost:4511/v1/run \
  -d '{"client":"local","tgt":"web*","fun":"status.uptime"}'
```

```json
{"jid":"20261011T191904977521","nodes":["web1.lab","web2.lab"],
 "return":{"web1.lab":{"seconds":60908,"since":"2026-10-11T02:23:56Z","human":"16h55m0s"},
           "web2.lab":{"seconds":60908,"since":"2026-10-11T02:23:56Z","human":"16h55m0s"}}}
```

The body fields are the same ideas as the command line: `client`, `tgt`,
`tgt_type` (for the non-glob targets of [chapter 13](13-targeting.md)),
`fun`, `arg`, `kwarg`. The client types are the predecessor's, kept:

| `client` | Does |
|---|---|
| `local` | Run on nodes and wait. |
| `local_async` | Run on nodes; return the JID at once. |
| `local_batch` | Run on nodes in batches. |
| `runner`, `runner_async` | Run a hub runner. |
| `wheel`, `wheel_async` | Run a hub management function (one namespace). |
| `ssh` | **Not exposed by the API**; `halite-hub ssh` is built, the API has no route to it. |

Every request is checked against the policy, twice
([chapter 26](26-authorization.md)). A denial is `403` with the reason:

```
POST /v1/run  {"client":"local","tgt":"web*","fun":"cmd.run","arg":["id"]}
HTTP/2 403
{"error":"no rule in [webops] permits cmd.run against \"web*\""}

POST /v1/run  {"client":"local","tgt":"db*","fun":"test.ping"}
HTTP/2 403
{"error":"no rule in [webops] permits test.ping against \"db*\""}
```

## Jobs

`POST /v1/jobs` is the asynchronous form (the same as `client:
local_async`):

```sh
curl -s … -X POST https://localhost:4511/v1/jobs \
  -d '{"client":"local_async","tgt":"web*","fun":"test.ping"}'
```
```json
{"jid":"20261011T191933156076","nodes":["web1.lab","web2.lab"]}
```

```sh
curl -s … https://localhost:4511/v1/jobs/20261011T192016669032
```
```json
{"jid":"20261011T192016669032","fun":"test.ping","target":"web*",
 "submitter":"cert:CN=api","on_behalf_of":"local:alice",
 "created":"2026-10-11T19:20:16.669032Z","state":"complete",
 "nodes":["web1.lab","web2.lab"],"returns":{"web1.lab":true,"web2.lab":true},
 "missing":[]}
```

Note the two identities: `submitter` is what the hub authorized (the API's
own certificate), and `on_behalf_of` is the person, **recorded but never
trusted** for authorization. The hub reads identity from the connection and
nothing from the request body.

| Endpoint | Does |
|---|---|
| `GET /v1/jobs` | List jobs, with filters (`?limit=`). |
| `GET /v1/jobs/{jid}` | Detail and returns. |
| `DELETE /v1/jobs/{jid}` | Kill a job that has not finished. |

## Nodes and keys

| Endpoint | Does |
|---|---|
| `GET /v1/nodes` | Every node, with its last grains and connection state. |
| `GET /v1/nodes/{id}` | One node. |
| `POST /v1/nodes/{id}/state` | Apply state to one node (`{"test":true}` for a rehearsal). |
| `GET /v1/keys`, `POST`, `DELETE` | Enrolment management, subject to policy. |
| `GET /v1/pillar/{id}` | A node's compiled pillar. Needs the role to **name** `pillar.show_pillar` literally. |
| `POST /v1/orch`, `GET /v1/orch/{jid}` | Start an orchestration; read its timeline. |
| `GET /v1/schema` | Every function's signature, machine-readable. Use it to validate a request before sending. |
| `GET /v1/metrics` | Prometheus text: this service's and the hub's ([chapter 34](34-monitoring.md)). |

`GET /v1/nodes` returns a mapping keyed by node ID, each entry carrying the
full grains the hub last received. `POST /v1/nodes/web1.lab/state` with
`{"test":true}` returned the same per-state structure as
`halite-hub run … state.apply --test`.

> To list nodes the policy has to let the caller (and the API's own
> certificate) call the runners the endpoint uses internally (here
> `cache.grains`, `manage.up` and `manage.down`). When an endpoint answers
> `403` with `no rule … permits manage.up against ""`, the missing grant is in
> the message.

## Events as a stream

`GET /v1/events` is a Server-Sent Events stream of the bus, and
`GET /v1/ws/events` the same over a WebSocket. Both take repeated `tag=`
globs and `from=latest|earliest|<offset>`:

```sh
curl -N --cacert api.crt -H "$H" \
  'https://localhost:4511/v1/events?tag=halite/job/**&from=latest'
```

```
id: 00000001:708536
event: halite/job/20261011T192019753473/new
data: {"_stamp":"2026-10-11T19:20:19.759216Z","_tag":"halite/job/…/new","data":{"arg":["sse-demo"],"fun":"test.echo","jid":"…","nodes":["web1.lab"],"submitter":"cert:CN=ed","tgt":"web1.lab", …},"_offset":"…"}

id: 00000001:708778
event: halite/job/20261011T192019753473/ret/web1.lab
data: {…,"data":{"duration_ms":1,"fun":"test.echo","retcode":0,"success":true},…}
```

The `id` of each record is its bus offset, so a client that reconnects with
`from=<offset>` (or the SSE `Last-Event-ID`) resumes where it stopped.
Watching the bus needs `event.listen` in a role's `runners:` list, and a
WebSocket is pinged every thirty seconds so an intermediary does not close a
quiet stream.

Events are filtered by policy: **a tag that names a node reaches only a caller
whose policy targets that node**, an event about no node in particular reaches
any caller granted something, and a principal bound to nothing sees nothing.

> **Known gap:** `halite/job/<jid>/new` has no node in its tag, so it is in
> the second class, and it carries the job's target, function and
> **arguments**. In the lab, `alice` (permitted only `web*`) received the
> `cmd.run` that the self-healing reactor sent to `db1.lab`, argument and all.
> Until this is closed (the ledger records it), do not put secrets in job
> arguments, and grant `event.listen` only to roles that are meant to see
> the whole fleet's activity.

## Webhooks: events from outside

External systems (a CI pipeline, a ticket system, a monitoring tool) start
automation by posting to `/v1/hook/{path}`. **A hook is always
configured, never improvised, and always authenticated**: there is no setting
that makes an unauthenticated one, and a hook with no credential is refused
when the configuration loads.

```yaml
# api.yaml
hooks:
  deploy:
    auth: hmac
    secret_file: /etc/halite/hooks/deploy.secret
    principal: 'hook:deploy'
    content_types: ['application/json']
    replay_window: 5m
```

The sender signs the **raw body**: an HMAC-SHA-256 over the timestamp, a `.`
and the body, keyed with the shared secret, sent as `X-Halite-Signature`,
with `X-Halite-Timestamp` (RFC 3339) and a random `X-Halite-Nonce`. This is
the same construction as the webhook returner
([chapter 23](23-scheduler-and-returners.md)), so one verifier serves both.
Signing and sending, with `openssl` and `curl`:

```sh
BODY='{"version":"1.4","by":"ci"}'
TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)
SIG=$(printf '%s.%s' "$TS" "$BODY" | openssl dgst -sha256 -hmac "$SECRET" -hex | awk '{print $NF}')
curl -s --cacert api.crt -X POST https://localhost:4511/v1/hook/deploy \
  -H 'Content-Type: application/json' \
  -H "X-Halite-Timestamp: $TS" -H "X-Halite-Signature: $SIG" \
  -H "X-Halite-Nonce: $(openssl rand -hex 8)" -d "$BODY"
```

```
HTTP/2 202
{"tag":"halite/hook/deploy"}
```

and the delivery is an event on the bus:

```json
{"_tag":"halite/hook/deploy","_schema":"halite.event/1",
 "data":{"_principal":"cert:CN=api","_remote":"127.0.0.1",
         "body":{"by":"ci","version":"1.4"}}}
```

The refusals, all of which were run:

| Case | Response |
|---|---|
| Same nonce again | `401 {"error":"this delivery has already been accepted"}` |
| Wrong signature | `401 {"error":"the signature did not verify"}` |
| A timestamp outside `replay_window` | `401 {"error":"the delivery is outside this hook's replay window of 5m0s"}` |
| A content type the hook does not accept | `415 {"error":"this hook accepts application/json"}` |
| A hook path that is not configured | `404 {"error":"no such hook"}` |
| A body larger than the limit | `413` |

`auth: token` and `auth: mtls` are the other two modes. The nonce is
recorded after the delivery lands on the bus, not when the signature verifies,
so a delivery that failed downstream and is retried with the same signature
is not refused as a replay.

A reaction can now act on `halite/hook/deploy`
([chapter 20](20-reactors.md)): start an orchestration, apply a state, send
a message. The payload is under `data['body']`, parsed if it was JSON.

> **Known gap:** the design is that the event carries the hook's configured
> principal (`hook:deploy` above), so a reaction authorizes on *who sent it*
> and never on the payload. In this build the hub's `event.send` records the
> principal of the **connection**, which is the API's certificate, so the
> event says `cert:CN=api` and the hook's name is lost (the delivery's `tag`
> still names the hook). Until this is closed (the ledger records it), key
> your reactor on the hook's **tag** (`halite/hook/deploy`) and on the fact
> that only the API can put events there.

## Calling it well

- **Prefer OIDC for people, a certificate or token for machines.** A CI job
  logging in with a password in a secret store is a break-glass account;
  give it the narrowest role.
- **Tokens are credentials.** Keep them in memory, send them only in the
  header, and let them expire; revoke on suspicion.
- **Validate before you send.** `GET /v1/schema` gives parameter names and
  types for every function.
- **Expect `403`s to be informative.** The message names the role and what
  is missing; that is how you tune the API's own role.
- **Use `local_async` plus `GET /v1/jobs/{jid}`,** or the event stream, for
  anything long.

## Key ideas

- `halite-api` is a **client** of the hub with its own certificate; it
  never exceeds what that certificate's policy allows, and authorizes twice.
- Log in at `/v1/login` (local, LDAP, OIDC) and send the token as
  `Authorization: Bearer`.
- `POST /v1/run` (wait) and `POST /v1/jobs` (async); `GET /v1/jobs/{jid}`
  shows `submitter` and `on_behalf_of`.
- `GET /v1/events` streams the bus (SSE, WebSocket), resumable by offset.
- `/v1/hook/{path}` is **signed, replay-protected, always configured**; one
  verifier serves both webhook directions.
- Two gaps are recorded: job arguments reach every event watcher, and a
  hook's principal is not carried onto the bus.

Next: [hardening: signing, the sandbox and evidence](28-hardening.md).
