package ext

import "slices"

// The extension kinds of SPEC 24.2.
//
// An extension provides exactly one. The host names the kind it wants in
// its hello frame, and Serve refuses a mismatch rather than answering a
// call it was never written for — a `returner` asked to supply pillar
// would otherwise fail somewhere further along, with a message about
// the shape of its answer rather than about the mistake.
//
// Which program runs which kind follows from where the work happens:
// `pillar` runs on the hub, because that is where pillar compiles, and
// `module` and `returner` run on a node.
const (
	KindModule     = "module"
	KindState      = "state"
	KindGrain      = "grain"
	KindBeacon     = "beacon"
	KindReturner   = "returner"
	KindPillar     = "pillar"
	KindRunner     = "runner"
	KindRenderer   = "renderer"
	KindAuth       = "auth"
	KindRoster     = "roster"
	KindFileServer = "fileserver"
	KindSigner     = "signer"
)

// Kinds is every kind, for a host validating a manifest and for an
// error message that can list the alternatives.
var Kinds = []string{
	KindModule, KindState, KindGrain, KindBeacon, KindReturner, KindPillar,
	KindRunner, KindRenderer, KindAuth, KindRoster, KindFileServer, KindSigner,
}

// ValidKind reports whether a string names one.
func ValidKind(kind string) bool { return slices.Contains(Kinds, kind) }

// The declarations of SPEC 24.3.
//
// Nothing is granted that is not declared, and the declaration is signed
// into the manifest — so an extension cannot ask for more at handshake
// than somebody signed off on. Declare the minimum: an extension that
// asks for root because it might one day need it is an extension running
// as root today.
const (
	// DeclareRoot keeps the extension's own identity instead of dropping
	// to the unprivileged account the host names.
	DeclareRoot = "root"
	// DeclareNetwork permits it to dial. Without this the host sets
	// HALITE_EXT_NETWORK=deny, which NetworkDenied reports.
	DeclareNetwork = "network"
)
