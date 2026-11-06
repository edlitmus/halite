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
a login past that is refused with `503`. A failed login holds its name *from
that address*, whether or not an account by that name exists, to a window
refused with `429` and `Retry-After`, doubling from one second to fifteen
minutes and cleared by a success. Somebody guessing from elsewhere keeps only
themselves out, never the account's owner. And one address's failures across
all names share a budget of thirty, draining one every thirty seconds. The
address is the connection's peer, so behind a proxy everybody shares one.
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
- **A runner rule**: `runners`, and a `target`. It permits calling those
  hub-side functions ([chapter 22](22-runners-and-mine.md)) for the nodes
  the target covers. Permission to ask the hub something and permission
  to run a command everywhere are different permissions, and they are
  written in different places so that one is never mistaken for the
  other.

A runner runs on the hub, with the hub's reach over every node's pillar,
cache, key and job returns, so a runner rule's `target` is what keeps
that reach to the nodes the role is for. This is stricter than Salt,
where a runner grant has no target and reads any managed machine's data. Each
runner says what it touches:

| A runner that... | For example | Is held to the target by |
|---|---|---|
| touches no node | `fileserver.*`, `event.send`, `reactor.list` | nothing; it needs no target |
| reaches nodes by sending jobs | `state.orchestrate`, `saltutil.refresh_pillar`, `mine.update` | each job, which is authorized against the role's fleet rules as it is sent |
| names one node, or takes a target | `pillar.show_pillar`, `key.delete`, `cache.grains web1`, `mine.get` | refusing a node, or an expression reaching a node, outside it |
| lists what it holds about nodes | `manage.status`, `key.list`, `jobs.list_jobs`, `event.replay` | listing only the nodes inside it; a job only if it went to one, and only that node's part |
| acts on every node at once | `cache.clear_all`, `jobs.prune` | requiring `target: '*'`, written |

A runner rule with no target grants the runner and reaches **no node**:
`manage.status` lists nothing, and `pillar.show_pillar` is refused for
every node. A missing field is not a wildcard, here as everywhere. An
orchestration run, or a runner's own job, is seen only by the principal
who started it, and by a role whose runner rule says `target: '*'`.
`halite-hub policy test --runner` says which nodes a runner reaches.

A request must match **one rule entirely**: the target *and* the function
*and* any argument limits, together. This is the central safety: a role
that may restart `web*.prod` services cannot restart `db*` ones, and cannot
`cmd.run` anything, whatever the operator types. Rules are never
combined across to make a larger grant.

A rule's `target` is a glob over node IDs, and a request is checked by the
**kind** of target it uses:

- **A glob** is covered if it is one node ID inside the rule's target, or
  exactly the rule's target.
- **A list** (`-L`) is covered if every node in it is inside the rule's
  target.
- **Any other kind** (compound, nodegroup, pcre, grain, pillar, subnet) is
  covered only by `target: '*'`. Which nodes such an expression reaches
  depends on the fleet when the job is sent, not on the text. An operator
  with a scoped role names the nodes as a list instead.

Function names are **patterns**: `service.*` is every function of the
`service` module, `state.show_*` the inspection functions, `*` everything
(with an important exception, below). A missing field matches nothing;
wildcards must be written.

### Argument limits

A rule can restrict how a function is called:

| Limit | Meaning |
|---|---|
| `allow_sls: [webserver.*]` | `state.apply` may name only these SLS files (patterns). A request naming none runs the highstate and is refused, as is `state.highstate`. |
| `deny_sls: [...]` | ...or must not name these. |
| `allow_mods: [deploy.*]` | `state.orchestrate` may run only these orchestrations. Each name is checked, positional or the `sls` keyword, and each name of a comma list on its own. |
| `deny_kwargs: [pillar]` | The call may not pass a keyword argument of this name (patterns). |
| `allow_env: [base]` | The call may run only in these environments (patterns). The environment is the `saltenv` of a job or the `env` of an orchestration. |
| `deny_env: [release-*]` | ...or must not run in these. `deny_env` is checked first. |
| `require_kwargs: {test: true}` | The call must carry each keyword with exactly this value, or it is refused. A bool, a string or a number; the name is exact, not a pattern; the type must match, so the string `"true"` is not `true`. |

A limit holds every function that takes the same names, not only the one
it is written under. Limits written for any of `state.apply`, `state.sls`,
`state.highstate` or the `state.show_*` functions hold all of them, because
the node compiles the same SLS names (or the highstate) for each. Limits
written for any of the `state.orchestrate`, `state.orch` or
`state.orchestrate_show_sls` runners hold all three, and hold
`state.orch_resume` too. A resume is judged on the names of the run that it
resumes. If a rule has limits under two of them, a call must pass both. So a rule that grants `state.*`
with `allow_sls` under `state.apply` does not leave `state.sls db.secret`
open. A function that should not be limited goes in a rule of its own.

`allow_env` and `deny_env` judge the environment that the caller chooses. With
gitfs every branch is an environment, so without them a role with
`allow_sls: [web.*]` can apply `web.x` from any branch. For a job, the policy
judges its `saltenv`. A job with no `saltenv` runs in the environment of the
node and is not judged: limit that case with `env_allowlist` on the node. For
an orchestration, the policy judges the `env` argument, as a keyword or as the
second positional argument, and an orchestration with no `env` is judged as
`base`. A resume is judged on the environment of the run that it resumes. An
older hub or API ignores these two keys silently, and so does this build for a
key that it does not know. Upgrade first and check the spelling.

`require_kwargs: {test: true}` is how a role is held to a rehearsal. The dry-run
flag (`run --test`, `"test": true` in the API) is a keyword on the wire, and
both limits judge the keywords the node will receive, so `test` counts whether
it was given as the flag or as `test=true`. A call that omits the keyword is
refused, not assumed to be fine. `halite-hub policy test` takes `--test` for
this: `policy test cert:CN=reporter web1 state.apply --test`.

`deny_kwargs: ['pillar']` matters more than it looks. Pillar can be passed
on the command line, which is otherwise a trivial way around any
authorization that is based on pillar.

### The functions no wildcard grants

A function whose signature says `arbitrary_code` can run anything. They are
never granted by a pattern; a role must **name** them:

```
never granted by a wildcard: at.at, cargo.install, cmd.exec_code, cmd.retcode,
cmd.run, cmd.run_all, cmd.run_bg, cmd.run_stderr, cmd.run_stdout, cmd.script,
cmd.script_retcode, cmd.shell, cmd.wait, composer.require, cpan.install,
cron.present, file.replace, file.write, gem.install, go.install, maven.run,
module.run, module.wait, npm.install, pip.install, schedule.add,
schedule.modify, schedule.run_job, virtualenv.create, win_task.create

nor any function this build does not ship, such as an extension's
```

(That is `halite-hub policy show` for this build.) The list is taken from
the signatures the build ships, so a function marked in a later build is
covered without anyone remembering to add it. `functions: ['*']` therefore
covers `pkg.installed` and not `cmd.run`. The list includes the language package
installers and the schedulers, such as `pip.install` and `at.at`. It does not include `file.*`
(other than `file.write` and `file.replace`), `ssh_auth.present` or `sudo.present`.

The last line is the other half. A pattern grants only a function the
hub's build ships and knows is not arbitrary code. An extension's
function is arbitrary code on the node, and the hub cannot know which
extensions a node has loaded, so a wildcard never grants one: a role
that calls `myext.exec` names it, and `myext.*` does not do it either.
The same goes for a function from a newer build than the hub's.

```
$ halite-hub policy test cert:CN=carol web1.prod pkg.install nginx
allowed by role "everything-but" rule 0
$ halite-hub policy test cert:CN=carol web1.prod cmd.run id
denied: cmd.run runs arbitrary code and is never granted by a wildcard; name it in the role's functions
```

In the systems this replaces, everybody's access control granted `.*` and
so granted a root shell on every machine. Reading a node's **pillar** is a
similarly distinct permission (it is reading its secrets) and must also be
named, not wildcarded. `policy show` prints that set on a line of its own:

```
reads a node's pillar, which a wildcard never grants: functions config.get, pillar.get, pillar.items, pillar.raw; runners cache.pillar, pillar.show_pillar
```

It is the function that decides, not the route it arrives by:
`pillar.show_pillar` is refused to `runners: ['*']` through
`GET /v1/pillar/{id}`, `POST /v1/run`, `halite-hub runner`, a reactor and
an orchestration alike, and `pillar.items` sent to a node is refused to
`functions: ['*']`. `config.get` is on the list because it searches
pillar first. `pillar.keys` and `pillar.show_top` are not: they name the
keys and the files, not the values.

```
$ halite-hub policy test cert:CN=carol web1.prod pillar.items
denied: pillar.items reads a node's pillar, which is its secrets, and is never granted by a wildcard; name it in the role's functions (SPEC 22.1)
```

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
| `… web1.prod state.apply` | denied: `state.apply` with no SLS names runs the highstate, which is not in the SLS names this role may apply (`webserver.*`) |
| `… web1.prod state.apply db.postgres` | denied: `db.postgres` is not in the SLS names this role may apply (`webserver.*`) |
| `… web1.prod state.apply webserver.nginx --kwarg pillar=x` | denied: `state.apply` may not be called with "pillar" by this role |
| `… db1.prod test.ping` | allowed by role "readonly" rule 0 |
| `… web1.prod service.restart nginx` | allowed by role "webops" rule 0 |
| `cert:CN=ci-pipeline '' state.orchestrate deploy.web --runner` | allowed by role "deployer" rule 0 |
| `cert:CN=ci-pipeline '' state.orchestrate other.thing --runner` | denied: `other.thing` is not in the modules this role may orchestrate (`deploy.*`) |
| `node:lb1.prod web1.prod ips` | allowed by role "lb_peer" rule 0 |
| `node:lb1.prod web1.prod pillar.items` | denied: no rule in [lb_peer] permits pillar.items against "web1.prod" |

`--runner` evaluates the function as a hub runner and `--kwarg k=v` supplies
an argument. `--target-kind` reads the target as the kind a job would be sent
with (`L`, `C`, `E`, `G`, … or `list`, `compound`, …). Without it, the target
is a glob. `--env <name>` gives the environment (`saltenv`) that the job would
be sent with, so that `allow_env` and `deny_env` can judge it. A test suite for your policy is a shell script of these lines
with the expected exit status: write it when you write the policy and run
it on every change.

`halite-hub policy show` prints the loaded policy in a normalised form
and, last, the never-wildcarded list for this build. Under each argument
limit it prints the functions the limit holds. For a rule with limits, it
also prints what the rule grants that no limit holds, so a grant wider than
its limits is visible before a request finds it:

```
     state.apply: holds state.apply, state.highstate, state.show_highstate, state.show_lowstate, state.show_sls, state.show_states, state.show_top, state.sls
     no argument rule holds: pkg.installed; service.* (all 18 it grants)
```

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
    - target: '*'
      runners: ['jobs.*', 'manage.*', 'pillar.show_pillar']
    - act_for: ['local:*', 'ldap:*', 'oidc:*']
bindings:
  - principal: 'cert:CN=api'
    roles: ['api-service']
```

For a runner call and a kill the API also tells the hub whom it acts for:
the operator and the roles their token was issued with. The hub then asks
every question of the operator as well as of the API, and either refusing
refuses. That covers the jobs a runner sends (`saltutil.refresh_pillar`,
`mine.update`, every step of an orchestration), the runs and runner calls a
caller may see or resume, and which jobs a caller may kill. The hub accepts
the claim only from a certificate whose role has `act_for` naming that
operator, and refuses the call from anyone else. `act_for` takes principal
patterns, as a binding does, and grants nothing else; a rule that holds only
`act_for` may not name a target. Without it, every runner call the API makes
for a logged-in operator is refused (DIVERGENCE 5.582). The example lists the
three kinds of principal that the API makes. Keep only the sources that your
API uses.

The hub judges the operator by the roles in the token, against the
`policy.yaml` of the hub. So that file must define every role that an API
token can carry. A role that the hub does not define grants nothing, and the
hub refuses the call. If the API and the hub run on different hosts, keep the
two copies of `policy.yaml` the same. A copy that drifts narrows or refuses
operators (DIVERGENCE 5.599).

Used with a narrow operator role, the effective rights of a person are the
intersection. The jobs the API submits record both: `submitter` is the API's
certificate and `on_behalf_of` is the person. A job submitted with `POST
/v1/jobs` records `on_behalf_of` and is never authorized on it; the API
authorizes the operator itself before it submits. An `event.send` through the
API records the person as `_on_behalf_of` in the event, and a key action
through the API records the person as `on_behalf_of` in the key event (DIVERGENCE
5.599). A role that is granted after a token was issued
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
  name them. Nor is an extension's function, or any the hub's build does
  not ship. Reading pillar is likewise named.
- Test with `halite-hub policy test` (exit status 0/1) and keep a test
  script; every decision is logged with its rule.
- The hub and the API each read the policy **at start**; restart both. The
  API authorizes twice.

Next: [the HTTP API](27-http-api.md).
