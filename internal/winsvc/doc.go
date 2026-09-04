// Package winsvc is the Windows service control manager: what
// `service.running` reaches on this platform, and what the win_service
// module of SPEC section 15.3 is built on.
//
// SPEC 15.2 lists `windows` among the providers the virtual `service`
// module has, and there was none: a node on this platform answered
// every service call with "no init system was recognised on this node",
// naming four providers and none of them for it. So a Windows node
// could not be told to start anything.
//
// It speaks to the manager through its own API rather than by running
// sc.exe and parsing what comes back. That is the same choice SPEC 15.2
// makes for systemd — "the D-Bus client is a direct implementation of
// the wire protocol", rather than shelling out — and for the same
// reason: sc.exe writes for a person, its output is localised, and a
// configuration management system that misreads whether a service is
// running is worse than one that cannot read it.
package winsvc
