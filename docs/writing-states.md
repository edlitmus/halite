# Writing states

## File format

A state file (`.sls`) is a mapping of state IDs to module function calls,
in the same shape as Salt:

```yaml
<state_id>:
  <module>.<function>:
    - <arg>: <value>
    - <arg>: <value>
```

The ID doubles as the default `name` argument, so this is idiomatic:

```yaml
/usr/local/etc/motd:
  file.managed:
    - contents: welcome
```

States run in declaration order unless requisites reorder them.

## The YAML subset

halite parses the subset of YAML that SLS files use. Supported: nested
block mappings, block lists, scalars, single/double quotes, `#` comments
(full-line and trailing), `[]` and `{}` empties. Not supported (parse
error, never a silent misparse): tabs in indentation, anchors/aliases,
flow collections, multi-line (`|` / `>`) scalars.

All scalars are strings. `enable: true` and `enable: "true"` are
identical; modules interpret booleans (`true/yes/1/on`) and numbers.
Quote file modes (`mode: "0644"`) as you would in Salt.

Multi-line file content: put it in a real file and use `- source:` —
better practice than inline blobs anyway.

## Templating

Files are rendered with Go `text/template` before parsing. Grains are
available under `.Grains`:

```yaml
{{ if eq .Grains.os_family "FreeBSD" }}
nginx_conf:
  file.managed:
    - name: /usr/local/etc/nginx/nginx.conf
    - source: files/nginx-freebsd.conf
{{ else }}
nginx_conf:
  file.managed:
    - name: /etc/nginx/nginx.conf
    - source: files/nginx-linux.conf
{{ end }}
```

Jinja → template cheat sheet:

| Jinja (Salt) | text/template (halite) |
|---|---|
| `{{ grains['os'] }}` | `{{ .Grains.os }}` |
| `{% if grains['os_family'] == 'FreeBSD' %}` | `{{ if eq .Grains.os_family "FreeBSD" }}` |
| `{% else %}` / `{% endif %}` | `{{ else }}` / `{{ end }}` |
| `{% for x in list %}` | `{{ range $x := ... }}` ... `{{ end }}` |
| `{{ a | default('b') }}` | (P1 helper; today: `{{ if .X }}{{ .X }}{{ else }}b{{ end }}`) |

Missing grains render as empty rather than erroring.

## Requisites

`require` gates execution order and success; `watch` is `require` plus
change propagation:

```yaml
install_nginx:
  pkg.installed:
    - name: nginx

nginx_conf:
  file.managed:
    - name: /usr/local/etc/nginx/nginx.conf
    - source: files/nginx.conf
    - require:
      - pkg: install_nginx

nginx:
  service.running:
    - enable: true
    - watch:
      - file: nginx_conf
```

References are `- <module>: <state_id>` or a bare `- <state_id>` (matches
any module). If a required state fails, dependents are skipped and
reported as failed with the blocking requisite named. If a watched state
reports changes, `service.running` restarts the service and `cmd.wait`
fires its command.

Cycles and dangling references are compile-time errors before anything
runs.

## Dry runs

`halite apply -test file.sls` reports exactly what would change and
changes nothing. Every module honors this. Use it in CI and before any
production apply, same discipline as `test=True`.

## Ad hoc calls

Any state function can run directly:

```sh
halite call pkg.installed name=nginx
halite call service.running name=nginx enable=true
halite call file.absent name=/tmp/stale
halite call cmd.run name='zfs list' 
```
