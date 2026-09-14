#!/usr/bin/env python3
"""An external pillar source, written against the wire rather than a library.

This is the whole protocol of SPEC 24.2 in one file, with nothing but
the standard library. It exists to prove a claim that is easy to make
and easy to get wrong: that an extension need not be written in Go, and
that the wire format is specified well enough for somebody to implement
it without reading the Go.

A Salt estate migrating to halite has Python in `_modules/`, `_grains/`
and `_pillar/`, and the point of SPEC 24 is not that Python is bad. It
is that the agent must not *import* it: a separate process, signed,
pinned, sandboxed and killable is a different proposition from
`exec()` inside the agent as root. This file is on the right side of
that line, and it is still Python.

What it does is deliberately dull, because the interesting part is the
protocol. It merges three things into pillar:

    ext_pillar:
      - example_pillar:
          data:                    # everybody
            timezone: UTC
          per_os:                  # by the `os` grain
            Ubuntu:
              package_manager: apt
          per_node:                # by node id
            web1.prod:
              role: web

It declares nothing: no network, no root, no filesystem. Run it as an
extension and it can do nothing but answer.

Packaging is the same as for a Go extension, and docs/extensions.md is
the walkthrough. The one difference is that the bundle carries a script
rather than a compiled binary, so it needs an interpreter on the host
that runs it, and it is not portable to a platform without one.
"""

import json
import struct
import sys

PROTOCOL_VERSION = 1
MAX_FRAME_SIZE = 16 << 20

NAME = "example_pillar"
VERSION = "1.0.0"
KIND = "pillar"


def read_frame(stream):
    """Read one length-prefixed frame, or None at end of stream.

    Four bytes of big-endian length, then that many bytes of JSON.
    Length-prefixed rather than newline-delimited so that a frame
    boundary does not depend on nobody ever emitting a newline inside a
    string -- a JSON encoder that pretty-prints would otherwise break
    the stream, and it would look like a protocol error in the host.
    """
    header = stream.read(4)
    if not header:
        return None
    if len(header) != 4:
        raise IOError("a truncated frame header")
    (size,) = struct.unpack(">I", header)
    if size > MAX_FRAME_SIZE:
        raise IOError(f"the host announced a {size} byte frame")
    body = stream.read(size)
    if len(body) != size:
        raise IOError("a truncated frame body")
    return json.loads(body)


def write_frame(stream, frame):
    """Write one frame, omitting the fields this kind does not use."""
    body = json.dumps({k: v for k, v in frame.items() if v is not None}).encode()
    if len(body) > MAX_FRAME_SIZE:
        raise IOError(f"a {len(body)} byte frame, past the limit")
    stream.write(struct.pack(">I", len(body)))
    stream.write(body)
    stream.flush()


def functions():
    """What this extension provides, in the shape of SPEC 15.6.

    A parameter's type is its *name*. Sending a number here -- which is
    what happens if you serialise an enum -- has every signature refused
    and leaves the extension reporting no functions at all.
    """
    return [
        {
            "module": NAME,
            "function": "ext_pillar",
            "doc": "Merge static pillar data, by operating system and by node.",
            "params": [
                {"name": "node_id", "type": "string", "required": True},
                {"name": "env", "type": "string"},
                {"name": "grains", "type": "map"},
                {"name": "pillar", "type": "map"},
                {"name": "config", "type": "map"},
            ],
        }
    ]


def ext_pillar(call, log):
    """Return what this source contributes, as a mapping.

    Returning None contributes nothing, which is not an error. Raising
    fails this node's whole pillar compilation -- which is the point:
    Salt's external pillar logged a failure and returned what it did
    get, so a state applied with an empty password and nothing said so.
    """
    config = call.get("config") or {}
    grains = call.get("grains") or {}
    node_id = call.get("node_id") or ""

    if not isinstance(config, dict):
        raise ValueError("this source's ext_pillar block is a mapping")

    unknown = set(config) - {"data", "per_os", "per_node"}
    if unknown:
        # A misspelt setting that silently does nothing is the failure
        # this project's configuration handling exists to prevent, and
        # arriving over a pipe does not exempt it.
        raise ValueError(
            f"{', '.join(sorted(unknown))} is not a setting for {NAME}; "
            "use data, per_os, or per_node"
        )

    out = dict(config.get("data") or {})

    per_os = config.get("per_os") or {}
    this_os = grains.get("os")
    if this_os and this_os in per_os:
        out.update(per_os[this_os] or {})

    per_node = config.get("per_node") or {}
    if node_id in per_node:
        out.update(per_node[node_id] or {})

    # A log frame. The host records it against this extension's name.
    # Keys and counts, never values: a pillar source's values are the
    # thing not to log.
    log("debug", f"{len(out)} key(s) for {node_id}")
    return out or None


def serve(stdin, stdout):
    """The handshake and the call loop."""
    hello = read_frame(stdin)
    if hello is None:
        return 0
    if hello.get("kind") != "hello":
        raise IOError(f"the host opened with a {hello.get('kind')!r} frame")
    if hello.get("protocol") != PROTOCOL_VERSION:
        raise IOError(
            f"the host speaks protocol {hello.get('protocol')} "
            f"and this extension speaks {PROTOCOL_VERSION}"
        )
    wanted = hello.get("extension_kind")
    if wanted and wanted != KIND:
        raise IOError(f"the host asked for a {wanted!r} extension and this one is {KIND!r}")

    write_frame(
        stdout,
        {
            "kind": "hello_ok",
            "name": NAME,
            "version": VERSION,
            "functions": functions(),
            # Nothing. No network, no root.
            "declares": None,
        },
    )

    while True:
        frame = read_frame(stdin)
        if frame is None or frame.get("kind") == "shutdown":
            return 0
        if frame.get("kind") != "call":
            raise IOError(f"the host sent a {frame.get('kind')!r} frame")
        answer(stdout, frame)


def answer(stdout, frame):
    """Run one call and write exactly one result frame.

    Exactly one, whatever happens. An exception that escaped would leave
    the host waiting for an answer that is never coming, until it kills
    the process on the timeout -- which is a correct outcome reached
    slowly, and reported as a hang rather than as the error it was.
    """
    call_id = frame.get("id")

    def log(level, message):
        write_frame(stdout, {"kind": "log", "id": call_id, "level": level, "message": message})

    try:
        if frame.get("function") != "ext_pillar":
            raise ValueError(f"this extension provides ext_pillar, not {frame.get('function')!r}")
        # The host sends the request as kwargs.
        kwargs = frame.get("kwargs") or {}
        value = ext_pillar(kwargs, log)
        write_frame(stdout, {"kind": "result", "id": call_id, "ok": True, "value": value})
    except Exception as err:  # noqa: BLE001 -- one result frame, always
        write_frame(stdout, {"kind": "result", "id": call_id, "error": str(err)})


def main():
    # Stdout is the protocol. Anything printed to it is a frame the host
    # cannot read, and the host kills a process that violates the
    # protocol rather than failing the call. Everything for a person
    # goes to stderr.
    try:
        return serve(sys.stdin.buffer, sys.stdout.buffer)
    except Exception as err:  # noqa: BLE001
        print(f"{NAME}: {err}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
