package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// The `fun` label is the hub's own record of the function, not a node's text.
//
// `halite_job_duration_seconds` was labelled with the function name in the
// *return*, which a node writes: an enrolled node, or one that was broken,
// could post returns naming a different function each time, and after
// `metrics.MaxSeries` of them every real function's series would be counted
// under `__overflow__` (the bound is 5.510's; this test posts thirty). The hub already holds the function the job was submitted
// with and a node cannot change it. DIVERGENCE 5.532.
func TestTheDurationLabelIsTheJobsFunctionNotTheNodes(t *testing.T) {
	l := metricsLab(t, metricsPolicy)
	web1 := l.enrolled(t, "web1.example")
	stop := l.connectSilent(t, web1, "web1.example", `{}`)
	defer stop()
	op := l.operator(t, "ed")

	for i := 0; i < 30; i++ {
		res, err := op.Submit(context.Background(), transport.SubmitRequest{
			Target: "web1.example", TargetKind: "L", Fun: "test.ping",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := web1.Return(context.Background(), job.Return{
			JID: job.ID(res.JID), NodeID: "web1.example", Fun: fmt.Sprintf("junk.fn%d", i),
			Success: true, Return: json.RawMessage(`true`), DurationMS: 5, Schema: job.ReturnSchema,
		}); err != nil {
			t.Fatal(err)
		}
	}

	var buf bytes.Buffer
	if err := l.server.Metrics.Write(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, `junk.fn`) {
		t.Errorf("a function name a node wrote became a label:\n%s", grepLines(out, "halite_job_duration_seconds_count"))
	}
	if !strings.Contains(out, `halite_job_duration_seconds_count{fun="test.ping"}`) {
		t.Errorf("the job's own function is not the label:\n%s", grepLines(out, "halite_job_duration_seconds_count"))
	}
}

// A function name that is not shaped like one is one label, whoever it came
// from: an operator allowed `*` may type anything, and a label with a quote or
// a newline in it is an exposition a scraper has to be tolerant of.
func TestAFunctionNameThatIsNotOneIsOneLabel(t *testing.T) {
	for in, want := range map[string]string{
		"test.ping":                    "test.ping",
		"state.apply":                  "state.apply",
		"cmd.run_all":                  "cmd.run_all",
		"x.y.z":                        "x.y.z",
		"nodot":                        "other",
		"has space.x":                  "other",
		"quote\".x":                    "other",
		"new\nline.x":                  "other",
		"":                             "other",
		strings.Repeat("a", 70) + ".b": "other",
	} {
		if got := metricFun(in); got != want {
			t.Errorf("metricFun(%q) = %q, want %q", in, got, want)
		}
	}
}
