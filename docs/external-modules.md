# External modules

Custom modules in halite are Go, compiled in — that is ADR-1, and it is
why there is one binary and no dependency tree. External modules are the
escape hatch for the rest: a program you write in whatever language you
like, shipped with your states, called like any other state function.

```
states/
  top.sls
  site.sls
  _modules/
    motd          <- executable
```

```yaml
# site.sls
banner:
  motd.set:
    - path: /etc/motd
    - text: welcome to the fleet
```

The file name is the module name, so `_modules/motd` provides `motd.*`.
The directory ships to agents with the rest of the state tree, executable
bit intact, so a module works masterless, under a control plane, and over
`halite ssh` without being installed anywhere.

## The protocol

halite runs the executable with the function as its only argument, writes
a JSON request on stdin, and reads a JSON result from stdout.

Request:

```json
{
  "function": "set",
  "id": "banner",
  "test": false,
  "args": {"path": "/etc/motd", "text": "welcome to the fleet"},
  "grains": {"os": "FreeBSD", "id": "web1"},
  "pillar": {"...": "..."},
  "mine": {"...": "..."}
}
```

Result:

```json
{
  "result": true,
  "changed": true,
  "comment": "motd set",
  "changes": {"text": "welcome to the fleet"}
}
```

`result` and `changed` mean what they mean everywhere else in halite:
`result` is whether the state is now as asked, `changed` is whether this
run altered anything. A module that reports `changed` on every run will
retrigger every `watch` that depends on it, every time.

**Honour `test`.** When it is true, report what you *would* do with
`changed: true` and change nothing. A module that ignores it makes
`halite apply -test` unsafe, which is the one thing dry run must never be.

A worked example in `sh` is in
[examples/states/\_modules/motd](../examples/states/_modules/motd).

## Failure

| The module | halite reports |
|---|---|
| exits non-zero | failed, with its stderr as the comment |
| exits 0 writing nothing | failed — a silent success would skip the state |
| writes output that is not a JSON result | failed, quoting what it printed |
| takes longer than five minutes | failed, timed out |
| returns `result: false` | failed, with its comment and any stderr |

Killing a module on timeout kills the module, not whatever it spawned.

## What this is not

* **Not a plugin API.** There is no linking and no ABI. A module that
  crashes fails one state.
* **Not a way to shadow built-ins.** The compiled registry is consulted
  first, so an executable named `file` cannot replace `file.managed`.
* **Not a new trust boundary.** An external module is code in your state
  tree, and the state tree already runs `cmd.run`. Anyone who can write
  to it can already run commands on every host it reaches. Protect it the
  way you protect the rest of your configuration.
