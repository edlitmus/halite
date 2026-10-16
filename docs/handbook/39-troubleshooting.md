# Troubleshooting guide

<!-- abstract: A method for finding what is wrong, then a catalogue of the symptoms halite actually produces, with the real message, the cause and the fix. -->
<!-- kind: guide -->
<!-- index: troubleshooting; debugging; diagnosis; error messages; doctor; event listen; show_lowstate; jobs missing; no answer; not connected; node refused; policy denied; compile error; undefined; permission denied; certificate error; clock skew; stale grains; pillar not found -->

Most halite problems are one of a few dozen, and most of the messages say what
to do. This chapter is a method, and then the messages themselves, organised by
what you were doing when you saw them. Every message quoted here is verbatim
from the program.

## The method

Work from the outside in. Four commands answer nearly every question, in this
order:

| Step | Command | Tells you |
|---|---|---|
| 1. **Is it healthy?** | `halite-node doctor` / `halite-hub doctor` / `halite-api doctor` | Configuration loads and matches the process; certificates and clocks; the hub is reachable; pillar compiles; disk; the evidence chain. Each failure has a remediation line. |
| 2. **Is it connected?** | `halite-hub runner manage.status`, `halite-hub keys list` | Which nodes the hub can reach, and each node's enrolment state |
| 3. **What happened?** | `halite-hub jobs show <jid>`, `halite-hub jobs missing <jid>`, `halite-hub event listen --from earliest --once --tag 'halite/…'` | The job, who it was sent to, who answered, and the events around it |
| 4. **What does the tree say?** | `halite-node state show_lowstate --local …`, `halite-node lint FILE`, `halite-node pillar items` | Exactly what would run, and what the node would be given |

Then, if you still need it: `--log-level debug --log-fmt console` on the
program in question (templates show their rendered text at debug), and
`halite-node state apply --test` on the machine, which reproduces a hub-driven
run with full diagnostics.

Two habits shorten everything: **change one thing at a time**, and **read the
whole message**. The most useful part of a halite error is often its last
clause: it names the setting, the command or the file.

## Connecting and enrolling

| Message or symptom | Cause | Fix |
|---|---|---|
| `this node needs hub_fingerprint to enrol. Take it from halite-hub keys fingerprint …` | The pin is missing | Put `hub_fingerprint` in `node.yaml` |
| `fetching the hub CA from …: the hub presented 2 certificates and none matches the fingerprint this node pinned (…)` | Wrong fingerprint, or interception | **Stop.** Get the fingerprint again by an independent route |
| `… the hub presented only its own certificate and no CA …` | A hub older than the node does not serve its CA | Upgrade the hub, or give the node `hub_ca_file` |
| `fetching the hub CA from …: connection refused` | Hub down, firewall, wrong host or port | `nc -vz hub 4510` |
| `enrol` exits `2` and waits | The request is pending | `halite-hub keys list` then `keys accept` (after comparing fingerprints) |
| A token node stays pending | Hub is in `manual` mode, which ignores tokens, silently | `enrollment_mode: token`, restart the hub |
| `--BLy… is not a flag of halite-node enroll` | The token began with `-` | `--token=SECRET` |
| Handshake error naming a host | The hub's certificate lacks the name the node dialled | Hub: `--names` |
| `x509: certificate signed by unknown authority` repeated in the node log | The hub has a different CA than the node pinned (restored wrongly, or rebuilt) | [Chapter 35](35-backup-and-recovery.md) |
| `this node's enrollment has been revoked reason=…`; the agent exits | Someone revoked it | Intended, or re-enrol it |
| The node connects, then `not yet valid` / `expired` | Clock skew or an expired certificate | NTP; `renew --force`; `doctor` |
| A node connects and disconnects repeatedly | Two machines share one identity | Revoke both and re-enrol each |

## Running commands

| Message or symptom | Cause | Fix |
|---|---|---|
| `this hub has no policy, so nothing is authorized` | No `policy.yaml`, or it was created after the hub started | `keys operator create <name> --admin`; restart the hub |
| `denied: <principal> is bound to no role in policy.yaml` | The principal has no binding | Add a binding; restart the hub (and API) |
| `denied: no rule in [role] permits fn against "target"` | A role exists but no rule matches both target and function | `halite-hub policy test` with the same arguments shows the rule that is missing |
| `cmd.run runs arbitrary code and is never granted by a wildcard; name it …` | `functions: ['*']` does not cover it | Name `cmd.run` in the role |
| `no node matched "x"` (exit `1`) | The target matches nothing connected | `manage.status`; `nodegroups.expand`; check quoting and the target type |
| `web1: not connected` / `no answer` (exit `3`) | The node is off or disconnected | `--offline queue` if the job can wait; `manage.down` |
| `refused: this node requires a signature for fn (every job)` | The node enforces signed jobs | `--sign-key`, or the fleet's signing flow |
| `"x.y" is not a function this build ships; see the tier table in SPEC section 15` | Typo, or a function not built | `sys.list_functions <module>`; the message lists neighbours |
| `<module>.<fn>: argument "p": is required` / `is not a parameter of this function` | A wrong or missing argument | `sys.doc module.fn` |
| `command exited N:` and exit `1` | The command ran and failed on the node | Run it there; add `stderr` with `cmd.run_all` |
| `freebsdpkg.version: this is the pkgng spelling of pkg … call pkg instead` | A provider-specific name on the wrong platform | Use `pkg.*` |
| `1 of 1 matched nodes are not connected … and the offline policy is "require"` | You asked for that | Drop `--offline require` or bring the node up |
| A batch ends with `stopped by its safe limit … never reached` | The safe limit worked | `jobs show <jid>`: who failed and who was never reached; fix; `jobs resume` or rerun on the rest |

## Compiling and applying state

| Message or symptom | Cause | Fix |
|---|---|---|
| `must be a quoted string, found the integer 420; write it as '0420' … or '0644'` | An unquoted file mode | Quote it: `mode: '0644'` |
| `ownr is undefined (mapping has no key ownr); use \| default(...), is defined, or .get(name, fallback)` | A name that does not exist, strictly | Fix the typo, or say it is optional |
| `"yes" resolves to the boolean true under YAML 1.1; quote it to keep the string` | A YAML 1.1 boolean | Quote it |
| `duplicate mapping key "a"` | The same key twice in one mapping | Remove one |
| `tab character used for indentation; YAML permits only spaces` | A tab | Spaces |
| `"nosuch" is not a state module this build ships` / `the file state module has no function "managd"; it provides …` | Typo or not built | The message lists what exists |
| `requisite cycle: a -> require -> b -> require -> a` | A loop | Break one edge |
| `require_any names \`test: x\`, which is not declared in this run` | A requisite target that does not exist (or is excluded) | Fix the name; `show_states` |
| `This state was skipped because breaker (…) failed.` | A dependency failed | Fix that one first; it is the real error |
| `This state was not attempted: the run was aborted by failhard on X.` | `failhard` fired | Fix X |
| `prereq names cmd.run, whose test mode is unreliable …` (warning) | `prereq` on a command | Use a declarative target or accept the risk |
| `salt[pillar.get] is undefined` **under lint** | `lint` has no function dispatcher | Use `state show_lowstate` for files that call `salt[...]` |
| `pillar sls "x" was not found in environment "base"` | The pillar top names a missing file | Create it, or `- ignore_missing: True` |
| `pillar top expression "G@role:x" targets on the grain(s) role, which a node controls …` | An untrusted grain in a pillar top | Add it to `pillar_trusted_grains` as a decision, or target on the ID |
| `pillar top expression "I@…" targets on pillar …` | Pillar cannot target pillar | Use the ID or a trusted grain |
| `import "x.jinja": no template loader is configured` | `halite-hub lint` cannot resolve imports | Use `halite-node lint` with `--file-root` |
| `the py renderer is not supported: requires Python` | A `#!py` file | Port it ([chapter 29](29-extensions.md)) |
| A run needs two applications to settle | A missing requisite | The idempotency test of [chapter 4](04-idempotency.md) |
| `cmd.run` with `watch` runs every time | `watch` only reacts in states that react | `cmd.wait`, or `onchanges` |
| A state "changes" every run | A volatile value, or an unguarded `cmd.run` | Compare two runs' `Changes` |
| `apt-get install … exited 100: Could not open lock file … Permission denied` | The node is not running as root | Run the agent as root |

## Pillar, grains, files

| Message or symptom | Cause | Fix |
|---|---|---|
| A command needing pillar fails: the hub could not be reached | Pillar does not fall back | Restore the hub, or `pillar_cache_disk: true` |
| Targeting on a grain finds the wrong nodes | The hub holds stale grains | `saltutil.refresh_grains`, then target |
| A custom grain script is ignored | It timed out, exited non-zero or printed non-JSON | The node warns: `…: timed out and was skipped` |
| A grain has the wrong value on one node | A custom grain with the same name as a core one wins (config block < `grains.d` < `grains` file) | Rename the custom grain |
| `file_roots … contains state_dir` (hub will not start) | The tree overlaps the hub's data | Move the tree |
| `source_hash` mismatch | The served file differs from the hash | Fix the hash (it is checked before rendering) |
| Files do not update | The cache or gitfs did not refresh | `runner fileserver.update`; check `gitfs_update_interval` |
| A gitfs ref is not served | It failed signature verification | The event and log name it; sign the commit |

## Events, beacons, reactors, orchestration

| Message or symptom | Cause | Fix |
|---|---|---|
| A reaction never fires | The pattern does not match (`*` is one segment; use `**`); or hub not restarted | `reactor.list`; `reactor.test` with a made-up event |
| `halite/reactor/error … data[path] is undefined` | The reaction reads a key the event lacks | Look at a real event; `data` *is* the body |
| A reaction is denied | Its principal has no role | `reactor.test` prints `authorized:`; fix the policy |
| A reaction fires late, or not for a quick second edit | `dedupe_window` drops later events | Use `debounce` |
| A reaction loops (events pile up on one tag) | The reaction causes its own trigger | Remove the entry and **restart the hub** (the causality limit does not stop a loop through `event.send`); then `dedupe_window`, different tags, `onchangeonly`, `disable_during_state_run` |
| A beacon event repeats every interval | `onchangeonly` is not set | Set it |
| The node will not start: a beacon "is declared and not built" | An unbuilt beacon is configured | Remove it; `beacons.list available=True` says why |
| `memusage` fires `status.meminfo did not report a total and an available figure` | Known defect | Ledger |
| `schedule.add` refused: no schedule running | Runtime changes need a running set | Configure one job first |
| `orch lint` passes, `salt.parallel` fails at run | `lint` does not catch it | Not built; use sequential steps |
| `No event matching "…" arrived within the timeout.` | The approval never came | Send the event; or raise `timeout` |
| `orch resume` refused: a real resume of a `--test` run | None of its steps happened | Run it for real |

## The API

| Message or symptom | Cause | Fix |
|---|---|---|
| `401 those credentials were not accepted` | One message for every failure | The API log says which it was |
| `429` with `Retry-After` | A failed-login backoff | Wait; it is per address and name, or per address across names |
| `403 no rule in [role] permits … against ""` | The role lacks a runner the endpoint uses | Add it to the named role; **restart the API** |
| Policy changes do nothing in the API | It reads the policy at start | Restart it |
| `{"error":"not ready"}` | The API cannot reach the hub | `halite-api doctor`; the operator certificate |
| `curl` fails silently over TLS | The serving certificate lacks the name | `subjectAltName` |
| `hub_port is not recognised` | The API has `hub: host:port` | Use one setting |
| `this delivery has already been accepted` | A replayed nonce | Use a fresh nonce |

## When nothing here matches

1. Run the three `doctor`s. 2. `halite-hub event listen --from earliest --once`
and read the minutes around the failure. 3. Reproduce locally with
`halite-node state apply --local --test --log-level debug --log-fmt console`.
4. Search [the ledger](../DIVERGENCE.md) for the message: a great many
behaviours that surprised someone are written down with the reason. 5. If it is
a defect, the project's first rule applies: a claim nobody demonstrated is a
bug, so write down the exact command, output and version
(`halite-node version`) and report it.

## Key ideas

- Work **outside in**: `doctor`, connection, job record and events, then the
  tree.
- **Read the whole message**; the last clause usually names the fix.
- Exit status tells you the class: `1` failed, `2` converged (node), `3` no
  answer (hub), `64` usage.
- `reactor.test`, `policy test`, `lint`, `show_lowstate` and `--test` find
  problems before production does.

Next: [incident playbooks](40-incidents.md).
