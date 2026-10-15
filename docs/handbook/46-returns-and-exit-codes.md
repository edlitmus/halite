# Return values and exit codes

<!-- abstract: The shapes halite returns, and every exit status a command uses, in one place for scripts and monitors. -->
<!-- kind: reference -->
<!-- index: exit codes; exit status; return schema; halite.ret/1; halite.job/1; halite.event/1; halite.evidence/1; state return; result; changes; comment; retcode; sysexits; EX_USAGE; 64; EX_CONFIG; 78; RestartPreventExitStatus -->

A script that drives halite should never need to parse prose to learn what
happened. This chapter is the contract: the exit status of each command, and
the structure of what comes back.

## Exit statuses

| Command | `0` | `1` | `2` | `3` | `64` |
|---|---|---|---|---|---|
| `halite-node state apply` (and `highstate`, `sls`) | Succeeded, and something changed | A state failed, or the tree did not compile | Succeeded, and **nothing needed to change** | | Usage error |
| `halite-node enroll` | Enrolled | Failed | **Pending**: waiting for an operator | | Usage error |
| `halite-node call`, `pillar`, `grains`, `state show_*` | Success | Failure | | | Usage error |
| `halite-node lint` | Renders and parses | A problem was found | | | Usage error |
| `halite-node doctor`, `halite-hub doctor`, `halite-api doctor` | No failures (warnings allowed) | At least one **failure** | | | Usage error |
| `halite-node verify-evidence` | The chain holds | A break was found | | | Usage error |
| `halite-hub run` | Every targeted node answered and succeeded | A node answered and failed; no node matched; the request was refused; a safe limit stopped a batch | | A node was sent the job and **did not answer** | Usage error |
| `halite-hub orch run` | Every step succeeded | A step failed (the timeline is printed either way) | | | Usage error |
| `halite-hub runner` | The runner succeeded | It ran and failed, or was refused | | | Usage error |
| `halite-hub policy test` | **Allowed** | **Denied** | | | Usage error |
| `halite-hub migrate` | No finding at or above `--fail-on` | At least one | | | Usage error |
| `halite-hub extensions verify` | Every rule passed (skips do not fail) | A rule failed | | | Usage error |
| `halite-hub keys …`, `jobs …` | Success | Failure | | | Usage error |
| `halite-hub serve` | Stopped by a signal | The listen address is not on the host yet, the port is still held, or the hub failed while serving. A restart may fix it. **`78`** instead for a configuration fault (see the notes) | | | Usage error |
| `halite-api serve` | Stopped by a signal | The listen address is not on the host yet, the port is still held, or the API failed while serving. A restart may fix it. **`78`** instead for a configuration fault (see the notes) | | | Usage error |

Notes:

- **`64` is sysexits' `EX_USAGE`**, used by all three programs for a command line
  they did not understand: no subcommand, an unknown one, a flag it does not
  take, a missing operand, or a malformed number. It was `2` once, which
  collided with "converged"; a typo in a cron line then reported success every
  night. It is deliberately neither `1` (a failed state) nor `2`.
- **Treat `2` as success** in anything that runs `halite-node state apply`.
  The shipped systemd unit sets `SuccessExitStatus=0 2`, and the rc.d wrapper
  turns `2` into `0`.
- **`halite-hub run state.apply` exits `0` for a converged fleet** as well as for
  one that changed; the `0`/`2` distinction belongs to the node. The hub's
  question is only whether every node succeeded.
- **`1` versus `3`** on `halite-hub run`: "it said no" and "it said nothing" call
  for different actions.
- **`78` is sysexits' `EX_CONFIG`**, and only `halite-hub serve` and
  `halite-api serve` use it. It means that the service stopped at start-up on
  its configuration or key material: a setting that does not parse, an
  `ext_pillar` source with no extension installed, a certificate or key that
  does not load, or a directory it may not write. A restart does not fix any of
  these, so both units carry `RestartPreventExitStatus=78` and restart on `1`.
  Before this, the hub's unit restarted on every exit, and one hub was
  restarted 427 times on a missing extension. The API's unit did the opposite:
  it stopped on `1`, so an API whose listen address came up late at boot stayed
  down.
- An agent (`halite-node connect`) that exits `1` by itself has hit one of two
  things a restart cannot fix: its enrolment was revoked, or `hub_tries` was
  reached. The node's unit carries `RestartPreventExitStatus=1` for it.

## Structures

### A state return

The unit of `halite-node state apply --out json` and of a hub-dispatched
`state.apply`: a mapping keyed `module_|-id_|-name_|-function`.

```json
{
  "file_|-playground_motd_|-/tmp/halite-playground/motd_|-managed": {
    "__id__": "playground_motd",
    "__sls__": "playground",
    "__run_num__": 1,
    "name": "/tmp/halite-playground/motd",
    "result": true,
    "changes": {},
    "comment": "/tmp/halite-playground/motd is already in the requested state.",
    "duration": 0.036,
    "start_time": "11:34:25.213640",
    "warnings": []
  }
}
```

| Field | Meaning |
|---|---|
| `result` | `true` satisfied; `false` failed; `null` (**test mode only**) a change would be made |
| `changes` | The structured difference. **Empty means nothing was done.** |
| `comment` | A sentence for a person |
| `__run_num__` | Position in the run |
| `duration` | Milliseconds |

The key format is ugly and stable; dashboards depend on it.

### A job return (`halite.ret/1`)

```json
{"jid":"20261011T185455764041","id":"web1.lab","fun":"test.echo","fun_args":["hi"],
 "success":true,"retcode":0,"return":"hi","start_time":"2026-10-11T18:54:55.785Z",
 "duration_ms":1,"node_version":"0.1.0-7-g8ad761ce275b","schema":"halite.ret/1"}
```

### A job record (`halite.job/1`)

`jid`, `fun`, `arg`, `nonce`, `expires`, `created`, `submitter`, `target`,
`target_kind`, `nodes`, `offline`, `state` (`dispatched`, `complete`, `aborted`),
`batch`, `delivered`, `schema`. A hub that meets a record with a schema it does
not know reads it and refuses to write it, instead of silently dropping fields.

### An event (`halite.event/1`)

See [chapter 18](18-events.md) and the [event reference](45-event-reference.md).

### An evidence record (`halite.evidence/1`)

`seq`, `ts`, `kind` (`node.start`, a job accepted or refused, an extension
change, an anchor receipt, …), `node_id`, `detail`, `prev` (the previous
record's hash), `hash`. `halite-node verify-evidence` checks the chain.

### The output formats

Selectable with `--out`:

| `--out` | Gives |
|---|---|
| `nested` (the default for node commands), `summary` (the hub's) | A readable tree |
| `json` | JSON; **the schema is frozen and versioned** |
| `yaml` | YAML |
| `txt` | One line per top-level key; a nested value is compact JSON |
| `quiet` | Nothing; only the exit status |

## Key ideas

- Exit status first: **`0`/`2`/`1`** for a node's state run, **`0`/`1`/`3`** for a
  hub run, **`64`** for a command line that was not understood.
- `result` is `true`, `false`, or (test mode) `null`; **`changes` empty means
  nothing was done.**
- Records carry a `schema` string; an unknown one is read, not rewritten.
- Use `--out json` for anything a program will read.
