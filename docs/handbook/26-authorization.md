# Authentication and authorization

<!-- abstract: Who may do what: principals, the one policy file, roles and rules with target, function and argument limits, the functions no wildcard grants, and how to test a policy before it matters. -->
<!-- kind: concept -->
<!-- index: authorization; authentication; policy; policy.yaml; RBAC; role; binding; principal; deny by default; policy test; policy show; allow_sls; deny_sls; allow_mods; deny_kwargs; arbitrary_code; cmd.run; wildcard; runners; local account; LDAP; OIDC; TOTP; token; least privilege; cert:CN; node: principal; reactor: principal -->

Two questions decide what happens when anything asks halite to do
something:

1. **Authentication**: *who is asking?* The answer is a **principal**, a
   name such as `cert:CN=ed`.
2. **Authorization**: *is that principal allowed to do this, to these
   nodes?* The answer comes from one file, the **policy**.

The design principle is that authorization is **deny by default**,
expressed in **one grammar** in **one file**, and that it is **testable**.
In the systems halite learned from, the same job was split across several
overlapping mechanisms with different syntaxes and surprising precedence,
which is why their access control is so often wrong in practice.

## Principals: who is asking

Everything that can ask has a principal, written `kind:name`:

| Principal | Who | Authenticated by |
|---|---|---|
| `cert:CN=ed` | An operator, or a service, using a certificate from the hub's CA | mutual TLS ([chapter 25](25-trust-and-identity.md)) |
| `node:web1.prod` | A node acting for itself (reading the mine, for instance) | its enrolment certificate |
| `reactor:watch` | A reaction ([chapter 20](20-reactors.md)) | named in the reactor entry |
| `hook:deploy` | A webhook ([chapter 27](27-http-api.md)) | HMAC, token or client certificate |
| `local:alice` | A person logging in to the API with a local account | password, optionally TOTP |
| `ldap:alice` | The same, through a directory | LDAP bind |
| `oidc:<claim>` | The same, through an identity provider | OIDC token |

Operators of the command line (`halite-hub run`, `keys`, `orch`, `policy`)
always use `cert:`. The login-based principals belong to the API. For the
hub, **being logged in on the hub's machine is not an identity**: even root
at the console needs a certificate and a policy that grants it something.
The very first time, `halite-hub keys operator create <name> --admin` writes
both a certificate and a bootstrap policy (only if no policy exists yet).

### Logging in to the API

`halite-api` accepts several ways to prove who you are. Choose by how
people in your organisation already authenticate.

**OIDC** (the recommended path) uses your identity provider with
Authorization Code and PKCE for people and Client Credentials for
automation. Tokens are verified against the provider's published keys;
`none` and symmetric algorithms are refused; issuer, audience, expiry and
nonce are all checked. Configure `oidc_issuer`, `oidc_client_id` and
`oidc_role_map` (a group with no entry in the map grants nothing, and an
issuer with no map is refused at start).

**LDAP / Active Directory** is a bind-only client over LDAPS or StartTLS.
Anonymous bind and plain LDAP are refused. The username is escaped into the
filter and never into a DN. Groups map to roles with `ldap_role_map`.

**Local accounts** are for break-glass and for automation identities, not
for the primary operator path. A verifier is PBKDF2-HMAC-SHA-512; the
password is read from standard input, never from an argument (which would
reach the process table and shell history):

```sh
printf 'the-password' | halite-api account hash
```

```
pbkdf2-sha512$600000$NA8tWPw90G2qxQNp3ngKMA$G6LE1wf61dzDc8Xlz…
```

```yaml
# accounts.yaml (mode 0600; keep it out of git)
accounts:
  alice:
    hash: 'pbkdf2-sha512$600000$NA8t…'
    roles:
      - webops
```

An account's roles are the union of this list and anything the policy binds
to `local:<name>`; using both for one person is how a grant ends up
somewhere nobody looks. TOTP (RFC 6238) can be added per account.

Logins are throttled in two ways. Password checks in flight are bounded, and
a login past that is refused with `503`. And a failed login holds its name,
whether or not an account by that name exists, to a window refused with
`429` and `Retry-After`, doubling from one second to fifteen minutes and
cleared by a success. It is a backoff and not a lockout, because a lockout
lets anybody who knows the break-glass account's name keep its owner out.
Every failure returns the same message, so the response is no oracle for
which accounts exist (the log says which it was).

A **token** (the result of a login) is 256 random bits, stored only as its
SHA-256 digest, with an absolute expiry, an idle expiry, the roles it was
**issued** with, and optionally a source network. It appears in the
`Authorization: Bearer` header and nowhere else: never in a log, an event, an
error or a URL. It can be revoked singly or by principal
(`halite-api token revoke --principal alice`), which is what disabling an
account means for the tokens already issued.

## The policy file

The file is `policy.yaml` in the configuration root (`policy:` in `hub.yaml`
changes where). It has two parts:

- **`roles`**: named lists of rules; each rule says what is allowed.
- **`bindings`**: which principals hold which roles.

This is the example policy that ships with the handbook, as used for every
decision shown below:

<!-- example: handbook/security/policy.yaml -->
```yaml
# A policy for a small estate: who may do what, to which machines.
#
# One file, one grammar, deny by default. A rule names a TARGET and the
# FUNCTIONS permitted against it together, and a request must match one rule
# entirely. A principal bound to no role may do nothing.
#
# `halite-hub policy test <principal> <target> <function> [args]` evaluates a
# request against this file and exits 1 on a denial; the handbook's chapter on
# authorization shows each decision below, and a test checks them.
roles:
  webops:
    - target: 'web*.prod'
      functions:
        - 'state.apply'
        - 'service.*'
        - 'pkg.installed'
      args:
        'state.apply':
          allow_sls: ['webserver.*']
          deny_kwargs: ['pillar']
    - target: 'web*.prod'
      functions: ['test.ping', 'grains.items']

  readonly:
    - target: '*'
      functions: ['test.ping', 'grains.*', 'sys.*', 'state.show_*']

  everything-but:
    - target: '*'
      functions: ['*']

  deployer:
    - runners: ['state.orchestrate']
      args:
        'state.orchestrate':
          allow_mods: ['deploy.*']

  lb_peer:
    - target: 'web*'
      functions: ['ips']

bindings:
  - principal: 'cert:CN=alice'
    roles: ['webops', 'readonly']
  - principal: 'cert:CN=carol'
    roles: ['everything-but']
  - principal: 'cert:CN=ci-pipeline'
    roles: ['deployer']
  - principal: 'node:lb*.prod'
    roles: ['lb_peer']
```

### Rules

A **rule** is one list item in a role. There are two shapes:

- **A fleet rule**: `target` and `functions`. It permits running those
  execution functions against nodes that match that target
  ([chapter 13](13-targeting.md)).
- **A runner rule**: `runners`. It permits calling those hub-side
  functions ([chapter 22](22-runners-and-mine.md)). Permission to ask the
  hub something and permission to run a command everywhere are different
  permissions, and they are written in different places so that one is
  never mistaken for the other.

A request must match **one rule entirely**: the target *and* the function
*and* any argument limits, together. This is the central safety: a role
that may restart `web*.prod` services cannot restart `db*` ones, and cannot
`cmd.run` anything, whatever the operator types. Rules are never
combined across to make a larger grant.

Function names are **patterns**: `service.*` is every function of the
`service` module, `state.show_*` the inspection functions, `*` everything
(with an important exception, below). A missing field matches nothing;
wildcards must be written.

### Argument limits

A rule can restrict how a function is called:

| Limit | Meaning |
|---|---|
| `allow_sls: [webserver.*]` | `state.apply` may name only these SLS files (patterns). |
| `deny_sls: [...]` | ...or must not name these. |
| `allow_mods: [deploy.*]` | `state.orchestrate` may run only these orchestrations. |
| `deny_kwargs: [pillar]` | The call may not pass a keyword argument of this name. |

`deny_kwargs: ['pillar']` matters more than it looks. Pillar can be passed
on the command line, which is otherwise a trivial way around any
authorization that is based on pillar.

### The functions no wildcard grants

A function whose signature says `arbitrary_code` can run anything. They are
never granted by a pattern; a role must **name** them:

```
never granted by a wildcard: cmd.retcode, cmd.run, cmd.run_all, cmd.run_stderr,
cmd.run_stdout, cmd.script, cmd.shell, cmd.wait, file.replace, file.write,
module.run, module.wait, schedule.add, schedule.modify, schedule.run_job
```

(That is `halite-hub policy show` for this build.) The list is taken from
the signatures the build ships, so a function marked in a later build is
covered without anyone remembering to add it. `functions: ['*']` therefore
covers `pkg.installed` and not `cmd.run`:

```
$ halite-hub policy test cert:CN=carol web1.prod pkg.install nginx
allowed by role "everything-but" rule 0
$ halite-hub policy test cert:CN=carol web1.prod cmd.run id
denied: cmd.run runs arbitrary code and is never granted by a wildcard; name it in the role's functions
```

In the systems this replaces, everybody's access control granted `.*` and
so granted a root shell on every machine. Reading a node's **pillar** is a
similarly distinct permission (it is reading its secrets) and must also be
named, not wildcarded.

### Bindings

`bindings` attach roles to principals. A principal may be a pattern:

```yaml
bindings:
  - principal: 'cert:CN=alice'
    roles: ['webops', 'readonly']
  - principal: 'node:lb*.prod'          # every load balancer node
    roles: ['lb_peer']
```

A principal bound to nothing can do nothing:

```
$ halite-hub policy test cert:CN=mallory web1.prod test.ping
denied: cert:CN=mallory is bound to no role in policy.yaml
```

## Testing a policy

`halite-hub policy test <principal> <target> <function> [arguments]`
evaluates a hypothetical request and tells you the rule that allows it or
the reason it was denied. It exits `0` on allow and `1` on deny, so a policy
can be tested in CI. Each of these was run against the policy above:

| Command | Result |
|---|---|
| `policy test cert:CN=alice web1.prod state.apply webserver.nginx` | allowed by role "webops" rule 0 |
| `… web1.prod state.apply db.postgres` | denied: `db.postgres` is not in the SLS names this role may apply (`webserver.*`) |
| `… web1.prod state.apply webserver.nginx --kwarg pillar=x` | denied: `state.apply` may not be called with "pillar" by this role |
| `… db1.prod test.ping` | allowed by role "readonly" rule 0 |
| `… web1.prod service.restart nginx` | allowed by role "webops" rule 0 |
| `cert:CN=ci-pipeline '' state.orchestrate deploy.web --runner` | allowed by role "deployer" rule 0 |
| `cert:CN=ci-pipeline '' state.orchestrate other.thing --runner` | denied: `other.thing` is not in the modules this role may orchestrate (`deploy.*`) |
| `node:lb1.prod web1.prod ips` | allowed by role "lb_peer" rule 0 |
| `node:lb1.prod web1.prod pillar.items` | denied: no rule in [lb_peer] permits pillar.items against "web1.prod" |

`--runner` evaluates the function as a hub runner and `--kwarg k=v` supplies
an argument. A test suite for your policy is a shell script of these lines
with the expected exit status: write it when you write the policy and run
it on every change.

`halite-hub policy show` prints the loaded policy in a normalised form
and, last, the never-wildcarded list for this build.

Every decision is **logged**, allowed or denied, with the rule that matched:

```
info: job authorized fun=cmd.run principal=cert:CN=ed role=administrator rule=0 target=web1.lab
info: runner authorized fun=event.send principal=reactor:watch role=watch rule=1
```

## Where the policy is read

- **The hub** reads `policy.yaml` when it starts. There is **no reload**:
  edit the file, then restart the hub (nodes reconnect on their own).
- **`halite-api` also reads the same file, at its own start**, to decide
  what an operator logged in through it may do. After a change, restart
  *both*. A stale API will deny what the hub would allow, with a message that
  names the role and not the staleness.
- A hub with **no policy file** starts, logs that it authorizes nothing,
  and refuses every job: *security that depends on a file existing is not
  security*. An empty file is a policy that grants nothing.
- A policy that would mislead is refused at load. For example, a rule that
  names its principal inside the role (`principals:` beside `runners:`) would
  load and grant nothing, so it is rejected; principals belong in `bindings`.
  Warnings from the YAML parser are returned too: `functions: [on]` is the
  boolean `true`.

### The API authorizes twice

A request through the API is checked **at the API** against the policy for
the operator who logged in, and **again at the hub** for the API's own
certificate, `cert:CN=api` by default. An estate that grants the API less
than the sum of its operators gets exactly that, which is a control and not
an accident. In practice the API's role has to cover what its operators do,
and the operators' roles narrow it:

```yaml
roles:
  api-service:
    - target: '*'
      functions: ['test.ping', 'state.apply']
    - runners: ['jobs.*', 'manage.*', 'pillar.show_pillar']
bindings:
  - principal: 'cert:CN=api'
    roles: ['api-service']
```

Used with a narrow operator role, the effective rights of a person are the
intersection. The jobs the API submits record both: `submitter` is the API's
certificate and `on_behalf_of` is the person, which is recorded and never
trusted for authorization. A role that is granted after a token was issued
does not widen that token (the roles were frozen at issue), so a role taken
away is a reason to revoke the token, not a change that applies mid-session.

## Designing roles

Start from what people actually do, and write the narrowest rule that
allows it.

| Role | Rules | For |
|---|---|---|
| `readonly` | `test.ping`, `grains.*`, `sys.*`, `state.show_*` on `*` | Anyone who needs to look. Everybody should hold this one. |
| `webops` | `state.apply` limited by `allow_sls`, `service.*`, on `web*.prod` | A team that owns a tier. |
| `deployer` | the `state.orchestrate` runner with `allow_mods` | A pipeline. It can run named orchestrations and nothing else. |
| `api-service` | what the API's operators collectively need | The API's own certificate. |
| `reactor:<name>` roles | exactly the functions one reaction dispatches | Every reactor entry ([chapter 20](20-reactors.md)). |
| `lb_peer` | read specific mine entries | Nodes that read the mine ([chapter 22](22-runners-and-mine.md)). |
| `break-glass` | broad, including named `cmd.run`, bound to a certificate kept offline | Emergencies, and logged loudly. |

Habits that keep a policy honest:

- **Name functions, not `*`,** wherever the set is small and known.
- **Never put `cmd.run` in a role that does not need it,** and when one
  does, restrict its `target`.
- **Give every automated actor its own principal** (each reactor, each CI
  job, each hook), so a decision in the log says who.
- **Review the policy as code.** It is a file in version control. A change
  that widens a grant should look alarming in a diff.
- **Keep a test script** next to it.

## Key ideas

- **Authentication** gives a **principal** (`cert:`, `node:`, `reactor:`,
  `hook:`, `local:`, `ldap:`, `oidc:`); **authorization** is the single
  `policy.yaml`: roles of rules, bindings of principals to roles.
- **Deny by default.** No policy, no binding, no matching rule: no.
- A rule couples **target + function + argument limits**; a request must
  match one rule entirely.
- `runners:` are a separate permission from `functions:`.
- **Functions that run arbitrary code are never granted by a wildcard**;
  name them. Reading pillar is likewise named.
- Test with `halite-hub policy test` (exit status 0/1) and keep a test
  script; every decision is logged with its rule.
- The hub and the API each read the policy **at start**; restart both. The
  API authorizes twice.

Next: [the HTTP API](27-http-api.md).
