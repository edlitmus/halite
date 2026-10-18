package eventbus

import "strings"

// NodeOf is the node an event is about, or "" for one about no node: the
// envelope's, or failing that the one its tag names.
//
// One function, because two readers decide by it what a caller may see:
// the API's event stream, and the hub's event runners (issue 321, M-56).
func NodeOf(e *Event) string {
	if e.Node != "" {
		return e.Node
	}
	// The tag carries it for the events that name a node in the tag
	// rather than in the envelope, which is most of SPEC 17.1's
	// namespace.
	return nodeFromTag(e.Tag)
}

// nodeFromTag reads the node out of the tags that carry one.
//
// `halite/node/<id>/...`, `halite/beacon/<id>/...`, and
// `halite/job/<jid>/ret/<id>` all name a node in the tag, and an event
// read off the log carries the envelope's `_node` only when the hub set
// it. Reading it from the tag as well is what stops a stream leaking
// the existence of a node through a tag nobody may see.
func nodeFromTag(tag string) string {
	parts := strings.Split(tag, "/")
	if len(parts) < 3 || parts[0] != "halite" {
		return ""
	}
	switch parts[1] {
	case "node", "beacon", "key", "mine":
		return parts[2]
	case "job":
		// halite/job/<jid>/ret/<node> and .../prog/<node>/<n>
		if len(parts) >= 5 && (parts[3] == "ret" || parts[3] == "prog") {
			return parts[4]
		}
	case "state":
		// halite/state/<jid>/<node>/<result>
		if len(parts) >= 4 {
			return parts[3]
		}
	}
	return ""
}
