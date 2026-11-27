package eventbus

import (
	"encoding/json"
	"testing"
)

// An event read back off the log has its lists as []any, and one just built
// has them as []string. Both must be read, or a job is judged by an empty
// node list and shown to nobody -- or, worse, by a list read as empty
// somewhere it should not be (issue 886).
func TestSubjectOfReadsTheNodesWhicheverWayTheEventWasBuilt(t *testing.T) {
	built := &Event{Tag: "halite/job/J/new", Data: map[string]any{"nodes": []string{"web1", "db1"}}}

	raw, err := json.Marshal(built)
	if err != nil {
		t.Fatal(err)
	}
	var readBack Event
	if err := json.Unmarshal(raw, &readBack); err != nil {
		t.Fatal(err)
	}

	for name, e := range map[string]*Event{"built": built, "read back": &readBack} {
		subject, judged := SubjectOf(e)
		if !judged || !subject.ByNodes || len(subject.Nodes) != 2 {
			t.Errorf("%s: SubjectOf = %+v, %v; want a job reaching two nodes", name, subject, judged)
		}
	}
}

func TestSubjectOfKnowsWhichEventsAreAboutAJobOrARun(t *testing.T) {
	for tag, want := range map[string]bool{
		"halite/job/J/new":              true,
		"halite/job/J/kill":             true,
		"salt/job/J/new":                true, // the event_tag_compat copy
		"halite/run/R/new":              true,
		"halite/run/R/ret":              true,
		"halite/run/R/orch/new":         true,
		"halite/run/R/orch/ret":         true,
		"halite/job/J/ret/web1":         false, // names its node in the tag
		"halite/node/web1/start":        false,
		"halite/reactor/error":          false,
		"halite/hook/deploy/github":     false,
		"halite/job/J":                  false, // too short to be either
		"other/job/J/new":               false,
		"halite/presence/change":        false,
		"halite/error/enrollment/extra": false,
	} {
		if _, got := SubjectOf(&Event{Tag: tag}); got != want {
			t.Errorf("SubjectOf(%q) judged = %v, want %v", tag, got, want)
		}
	}
}

func TestAJobIsShownOnlyWhenEveryNodeItReachedIsInScope(t *testing.T) {
	web := func(node string) bool { return node == "web1" || node == "web2" }
	reader := Reader{Node: web}

	for name, tc := range map[string]struct {
		nodes []string
		want  bool
	}{
		"inside":                   {[]string{"web1", "web2"}, true},
		"one outside":              {[]string{"web1", "db1"}, false},
		"all outside":              {[]string{"db1"}, false},
		"reached none":             {nil, false},
		"a kill that told nothing": {[]string{}, false},
	} {
		s := Subject{ByNodes: true, Nodes: tc.nodes}
		if got := s.VisibleTo(reader); got != tc.want {
			t.Errorf("%s: VisibleTo = %v, want %v", name, got, tc.want)
		}
	}

	everything := Reader{Everything: true, Node: web}
	if !(Subject{ByNodes: true}).VisibleTo(everything) {
		t.Error("a caller whose scope is the estate was not shown a job that reached no node")
	}
}

func TestARunIsShownToItsCallerAndToTheWholeEstateOnly(t *testing.T) {
	made := func(principal, onBehalfOf string) bool {
		return principal == "cert:CN=api" && onBehalfOf == "local:web"
	}
	run := Subject{Principal: "cert:CN=api", OnBehalfOf: "local:web"}
	other := Subject{Principal: "cert:CN=api", OnBehalfOf: "local:other"}

	if !run.VisibleTo(Reader{Made: made}) {
		t.Error("a caller was not shown its own run")
	}
	if other.VisibleTo(Reader{Made: made}) {
		t.Error("a caller was shown another operator's run")
	}
	if (Subject{}).VisibleTo(Reader{Made: made}) {
		t.Error("a run that names nobody was shown to somebody")
	}
	if !other.VisibleTo(Reader{Everything: true}) {
		t.Error("a caller whose scope is the estate was not shown a run")
	}
	if other.VisibleTo(Reader{}) {
		t.Error("a reader that cannot say who the caller is was shown a run")
	}
}
