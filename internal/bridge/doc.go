// Package bridge is the host side of the extension protocol of SPEC section
// 24: it starts an extension as a separate process, speaks length-prefixed
// JSON to it over standard input and output, and keeps it inside the sandbox
// its manifest declared.
//
// An extension never runs in the agent's address space. The agent (or the hub)
// owns a [Pool] of processes for each extension, so concurrency is by process
// and an extension never has to be thread-safe; a caller that finds no process
// free starts one, up to a deliberately small bound, rather than queueing
// behind one slow call. A call that runs out of time is killed (SIGTERM, then
// SIGKILL) and a fresh process replaces it, so a hung extension cannot hang
// the agent, and a protocol violation kills the process rather than failing
// the call: an extension that sent something unreadable has lost its place in
// the stream.
//
// # What the sandbox is
//
// [Sandbox] describes what is actually applied on the machine in front of
// you, because the specification's table of controls differs by platform and
// a control that is not available must say so rather than be silently skipped.
// The default is an unprivileged account, no network, and resource limits;
// only an extension that declared `root` or `network` in its signed manifest
// gets either. The environment is explicit and is not the host's, so an
// extension does not inherit credentials that happen to be in the agent's.
//
// The wire format and the author's half of the protocol are in package ext,
// which is public; the verification that an extension speaks it correctly is
// package extconform; the signing, pinning and distribution of bundles is
// package extension.
package bridge
