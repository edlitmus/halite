package eventbus

import "strings"

// Subject is what an event with no node is about, for the events whose
// payload would otherwise go to anyone the policy grants anything: a job's
// arguments and the nodes it reached, or a runner call's arguments and who
// made it (issue 886).
//
// It is read here, once, because two readers decide by it what a caller may
// see -- the API's event stream and the hub's event runners -- and the first
// version of each decided alone, by "this event names no node, so show it".
// They differ in what they know about the caller, so the verdict takes that
// as a Reader and the rule is written once.
type Subject struct {
	// Nodes are the nodes a job reached or was told to stop. Used when
	// ByNodes is set.
	Nodes   []string
	ByNodes bool

	// Principal and OnBehalfOf are who made a runner call or an
	// orchestration run, as the hub recorded them. Used when ByNodes is
	// not set.
	Principal  string
	OnBehalfOf string
}

// Reader is what a reader knows about the caller who would be shown an
// event.
type Reader struct {
	// Everything is a caller whose scope is the whole estate, written as a
	// target of `*`.
	Everything bool
	// Node says whether one node is inside the caller's scope.
	Node func(node string) bool
	// Made says whether a run by this principal, for this operator, is the
	// caller's own.
	Made func(principal, onBehalfOf string) bool
}

// SubjectOf reports what a node-less event is about, and false when the
// event is not one of those the hub emits about a job or a run, so that the
// reader falls back to its own rule for the rest.
//
// An event of these tags whose payload cannot be read comes back as a
// Subject with nothing in it, which no caller short of Everything is shown:
// a record this reader cannot place must not be given to everybody.
func SubjectOf(e *Event) (Subject, bool) {
	parts := strings.Split(e.Tag, "/")
	// `event_tag_compat` writes each event a second time under `salt/...`
	// (SaltTag), with the same payload, so the same event is read under
	// either root.
	if len(parts) < 4 || (parts[0] != "halite" && parts[0] != "salt") {
		return Subject{}, false
	}
	switch parts[1] {
	case "job":
		// A job's creation names the nodes it reached; a kill names the
		// ones it told and the ones it took out of the queue. Its return
		// and progress events name their node in the tag.
		switch parts[3] {
		case "new":
			return Subject{ByNodes: true, Nodes: stringsIn(e.Data["nodes"])}, true
		case "kill":
			nodes := append(stringsIn(e.Data["told"]), stringsIn(e.Data["unqueued"])...)
			return Subject{ByNodes: true, Nodes: nodes}, true
		}
	case "run":
		// halite/run/<jid>/new and /ret for a runner call, and
		// halite/run/<jid>/orch/... for an orchestration.
		return Subject{
			Principal:  stringIn(e.Data["principal"]),
			OnBehalfOf: stringIn(e.Data["on_behalf_of"]),
		}, true
	}
	return Subject{}, false
}

// VisibleTo reports whether the reader's caller may be shown an event about
// this subject.
//
// A job is shown when every node it reached is in the caller's scope. It is
// not enough that one is: the event carries the whole target and the job's
// arguments, and a caller scoped to web* that saw a job sent to web1 and db1
// would be told what was run on db1. A job that reached no node is shown to
// nobody short of Everything, as jobs.list_jobs treats it (jobVisible).
//
// A run is shown to the one who made it, and to Everything, because its
// arguments name nodes (`pillar.show_pillar db1`) and it went to the hub,
// which is no node in anybody's scope.
func (s Subject) VisibleTo(r Reader) bool {
	if r.Everything {
		return true
	}
	if !s.ByNodes {
		return r.Made != nil && r.Made(s.Principal, s.OnBehalfOf)
	}
	if len(s.Nodes) == 0 || r.Node == nil {
		return false
	}
	for _, node := range s.Nodes {
		if !r.Node(node) {
			return false
		}
	}
	return true
}

// stringsIn reads a list of strings out of an event's data, which is a
// []string when the event was just built and a []any when it was read back
// off the log.
func stringsIn(v any) []string {
	switch list := v.(type) {
	case []string:
		return append([]string(nil), list...)
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func stringIn(v any) string {
	s, _ := v.(string)
	return s
}
