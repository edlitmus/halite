# A tree halite manages itself with

States and pillar that let an estate keep halite's own configuration,
certificates and extensions in its tree, like anything else it manages:

| Machine | States | What they do |
|---|---|---|
| every node | `halite.node` | writes `node.yaml` from pillar, schedule included, and restarts the agent a little later when it changes |
| the hub host | `halite.hub`, `halite.api` | writes `hub.yaml`, `policy.yaml` and `api.yaml` from pillar, and restarts the hub or the API when they change |
| the hub host, daily | `halite.certs` | renews halite-api's serving certificate, and its operator certificate to the hub |
| the signer, hourly | `halite.signer` | signs each extension pillar lists, publishes it into the tree, and pushes |

The node IDs are `hub.example.com`, `signer.example.com` and everything
else; the paths and service names follow `grains['kernel']`, FreeBSD's
or Linux's (`states/halite/map.jinja`). Copy `states/` and `pillar/`
into your own tree and change the IDs and the values.

## What renews itself, and what this tree renews

| Certificate | Renewed by |
|---|---|
| each node's identity | the node, at half its life (SPEC 7.4) |
| the hub's own serving certificate | the hub, at half its life, served without a restart |
| halite-api's serving certificate | `halite.certs`, 30 days before expiry, from an API CA the same state makes once; halite-api reads it again without a restart |
| halite-api's operator certificate to the hub | `halite.certs`, a week before expiry; halite-api reads it again without a restart |
| the enrollment CA | nothing. It lasts ten years and there is no rotation yet; `halite-hub doctor` warns 30 days before. |

Clients of the API -- Prometheus, an operator's `curl` -- trust
`<pki>/api-ca.crt`, not the serving certificate, so a renewal changes
nothing they hold. `docs/metrics.md` describes the quicker route, a
self-signed serving certificate, which is a new certificate to trust at
every renewal.

## Extensions: signed on the signer, never on the hub

The signing key lives on the signer node and nowhere else. A hub holding
it would verify its own signatures, and whoever took the hub could sign
anything every node then runs (`docs/extensions.md`). halite moves files
from the hub to nodes and not back, so the signer publishes into its
checkout of the repository the hub serves through gitfs, and pushes.

1. The first highstate on the signer makes the key. Its output -- the
   `halite signing key` state's -- is the `extension_trust_keys` line;
   put it in `pillar/halite/common.sls` and `pillar/halite/hub.sls`.
2. Build an extension and put its executable in
   `/srv/halite-incoming/<name>/<version>/`, and list it in
   `pillar/halite/signer.sls`.
3. The signer's hourly run signs it, publishes it to
   `_ext/<name>/<version>/`, and pushes. `extensions sign` prints the
   pin; add it to `extension_pins` in the same two pillar files.
4. The next highstate writes the trust key and the pin into every
   `node.yaml` and the hub's `hub.yaml`, and the restarts load it. A
   node syncs the bundle with `saltutil.sync_all`; a hub with
   `halite-hub extensions sync`.

## Bootstrapping

Nothing here can configure a hub that does not exist yet, or enrol a
node. In order, once the binaries are installed (`make install`):

1. **The hub.** Start it once; it makes the enrollment CA. Make an
   operator certificate for yourself (`halite-hub keys operator create
   <you> --admin`) and the API's (`halite-hub keys operator create api`),
   and put the tree where `file_roots` and `pillar_roots` say.
2. **The hub host's own configuration.** Apply the tree locally, as the
   hub host, which writes `hub.yaml`, `policy.yaml`, `api.yaml` and its
   own `node.yaml`:
   `halite-node state apply --local --file-root <tree>/states --pillar-root <tree>/pillar --id hub.example.com`
   That run also asks for the node agent to be running, and the agent
   cannot connect until step 3 has enrolled it: expect that one state to
   fail this first time.
3. **Its node.** Enrol it to the hub on the same machine (`halite-node
   enroll`, with `hub_fingerprint` from `halite-hub keys fingerprint`, then
   `halite-hub keys accept`). It has its own `pki_dir` and `state_dir`
   (`separate_dirs` in its pillar): sharing the hub's would mix its key
   material with the CA's.
4. **Every other node and the signer.** Enrol as usual. Their first
   highstate writes their `node.yaml` with the schedule.

## What has been demonstrated, and what has not

`TestTheExampleTreeCompilesForEveryRole` (cmd/halite-node) compiles
every role's highstate, and `halite.certs` and `halite.signer` on their
own, as FreeBSD and as Linux. It loads every `node.yaml`, `hub.yaml` and
`api.yaml` the tree would write with the program that reads it, and
requires no warning. It loads the policy the same way, and parses every
node's schedule, because a schedule that does not parse stops the agent
from starting.

That is all it does. It compiles; it applies nothing, because nearly
every state here writes under `/etc` or `/usr/local/etc` and drives a
service manager. **Not run by this project:**

- **The deferred node restart.** `daemon(8)` on FreeBSD,
  `systemd-run --on-active` on Linux. A restart run from inside the
  agent's own job would stop the job with it; whether these two escape
  the agent as intended has not been seen.
- **The hub and the API restarting on a change,** the operator
  certificate re-issued as `halite` and picked up by the running API, and
  the API CA and serving certificate on a real hub host.
- **The signer's push**, and the hub serving what it pushed through
  gitfs. The credentials are yours to arrange.
- **A hub host enrolled to itself** with the separate directories.

`halite-hub extensions key create` and `extensions sign` themselves,
including a hub accepting a bundle under the right key and refusing it
under another, are tested in cmd/halite-hub (DIVERGENCE 5.260).
