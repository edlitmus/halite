# Agentless runs over ssh

`halite ssh` manages hosts that have no agent and no control plane. It
copies one static binary, ships the state tree, runs it, and collects the
results as JSON. Nothing is installed and nothing is left behind.

```sh
halite ssh web1,web2 state.highstate -test
halite ssh '*' state.apply web.nginx -roster hosts.txt
halite ssh db1 call pkg.installed name=postgresql16-server
halite ssh '*' call disk.usage -roster hosts.txt
halite ssh web1 grains
```

Everything goes through your `ssh(1)` and `scp(1)`, so ssh_config, agent
forwarding, and jump hosts all apply. Pass extra options with a repeatable
`-o`:

```sh
halite ssh web1 grains -o ProxyJump=bastion -o Port=2222
```

## Hosts and rosters

Without `-roster`, the first argument is a comma-separated list of ssh
destinations. With one, it is a glob over the roster:

```
# hosts.txt — one ssh destination per line
web1.example.com
root@web2.example.com
db1.example.com
```

```sh
halite ssh 'web*' state.highstate -roster hosts.txt
```

A glob matches the destination without its `user@` prefix. A glob that
matches nothing is an error rather than a silent no-op.

Hosts are worked on in parallel, `-jobs` at a time (default 8), each under
`-timeout` (default 10 minutes). The exit status is non-zero if any host
failed.

## Which binary gets pushed

`halite ssh` runs `uname -s -m` on each host and picks a binary to match:

1. `-binary PATH`, if given.
2. `<dist>/halite-<os>-<arch>` — run `make cross` to populate `dist/`.
3. This very executable, when the remote platform matches the local one.

An unbuilt platform is an error, not a wrong binary.

## What happens on the host

```
operator                                    remote host
   |--- ssh: uname -s -m ------------------>|
   |--- ssh: mktemp -d --------------------->|  /tmp/halite.XXXXXX
   |--- scp: the halite binary ------------->|
   |--- ssh: halite grains -json ----------->|
   |<-- grains ------------------------------|
   |    render this host's pillar locally    |
   |--- ssh: cat > pillar.json -------------->|
   |--- ssh: tar -xzf - (the state tree) ---->|
   |--- ssh: halite apply -root ... -json --->|
   |<-- results as JSON ----------------------|
   |--- ssh: rm -rf /tmp/halite.XXXXXX ------>|
```

**Pillar is rendered on your machine**, from the grains that host reports,
and only the result is shipped. A managed host never receives another
host's pillar data. Pillar targeting therefore uses the host's own grains,
not its roster name — a roster entry is an ssh destination, nothing more.

The working directory is removed even when the run fails, so a failed host
is left exactly as it was found.

## Compared with the other two modes

| | masterless | ssh | fleet |
|---|---|---|---|
| Installed on the host | halite | nothing | halite + a certificate |
| Who starts a run | the host (cron) | you | you |
| Needs inbound access to the host | no | ssh | no |
| Pillar rendered on | the host | your machine | the control plane |
| Scales to | one host | tens | thousands |

`halite ssh` is the bootstrap path: use it to install and enrol agents on
hosts that do not have them yet, then drive them with `halite run`.

Run it from a unix host — it needs `ssh(1)`, `scp(1)`, and `tar` on the
remote side.
