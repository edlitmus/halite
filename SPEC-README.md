# halite-specs

Specification for **Halite**, a replacement for SaltStack written in Go with a near-zero
third-party dependency surface.

| Document | Contents |
|---|---|
| [SPEC.md](SPEC.md) | The full specification, sections 1 to 33 |

## The short version

| Goal | Answer |
|---|---|
| Keep state and pillar | An in-house YAML 1.1 parser and a Jinja2-compatible template engine, so existing SLS trees compile unchanged. Sections 10 to 12. |
| Eliminate the supply chain | Go standard library only, plus an allowlist of one module (`golang.org/x/sys`). Vendored, offline, reproducible builds. Section 4. |
| Three binaries | `halite-node`, `halite-hub`, `halite-api`, all static, no interpreter, no plugin loading. Section 2.2. |
| Drop master/minion | hub and node, plus a lexicon policy enforced in CI. Section 2. |
| Priority features | State, pillar, reactors, orchestration, beacons, remote execution. Sections 11, 12, 18, 19, 16, 9. |

## Where third-party code was avoided, and how

| Salt needs | Halite uses |
|---|---|
| PyYAML | An in-house parser restricted to nine types, section 10.1 |
| Jinja2 | An in-house engine, section 10.2 |
| ZeroMQ, tornado, a bespoke crypto envelope | `net/http` HTTP/2 over mutual TLS 1.3, section 6 |
| boto3 | In-house SigV4, section 13.4 |
| pygit2, libgit2 | The system `git` binary, section 13.3 |
| paramiko | The system `ssh` binary, section 21 |
| python-ldap, authlib, cryptography | Stdlib `encoding/asn1` and `crypto/*`, section 23 |
| A dynamic Python module loader | Signed, pinned, sandboxed out-of-process extensions, section 24 |

## Status

Draft v1, 2026-08-19. Open questions requiring a decision are in section 33.
