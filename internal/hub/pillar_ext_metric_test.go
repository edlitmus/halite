package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/metrics"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// failingSource is an external pillar source that always fails.
type failingSource struct {
	name string
	soft bool
}

func (f failingSource) Name() string   { return f.name }
func (f failingSource) FailSoft() bool { return f.soft }
func (f failingSource) Pillar(context.Context, pillar.ExtRequest) (*value.Map, error) {
	return nil, errors.New("the secret store did not answer")
}

// The counter for a failed external pillar source was bound with two
// label values -- `.With("source", name)` -- on a family declared with
// one label, `source`. The metrics package panics on a wrong count by
// design, so with metrics on, every external-pillar failure panicked
// the pillar request instead of counting it: the node got no pillar
// even from a source configured `ext_pillar_fail: ignore`, whose whole
// purpose is that it does. Nothing had exercised a failing source on a
// hub with metrics (DIVERGENCE 5.196).
func TestAFailedExternalPillarSourceIsCountedAndNotAPanic(t *testing.T) {
	for _, tc := range []struct {
		name     string
		soft     bool
		wantPill bool
	}{
		{"ignored failure still serves the rest", true, true},
		{"hard failure is a refusal, not a dropped connection", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t).withPillar(t, map[string]string{
				"top.sls":    "base:\n  '*':\n    - common\n",
				"common.sls": "greeting: hello\n",
			})
			reg := metrics.NewRegistry()
			l.server.Metrics = reg
			l.server.Pillar.Ext = []pillar.ExtSource{failingSource{name: "vault", soft: tc.soft}}
			client := l.enrolled(t, "web1.example")

			res, err := client.Pillar(context.Background(), transport.PillarRequest{
				NodeID: "web1.example", Env: "base", Grains: json.RawMessage(`{}`),
			})
			if tc.wantPill {
				if err != nil {
					t.Fatalf("an ignored source's failure lost the node its whole pillar: %v", err)
				}
				decoded, derr := value.DecodeJSON(res.Pillar)
				if derr != nil {
					t.Fatal(derr)
				}
				if m, ok := decoded.(*value.Map); !ok {
					t.Fatalf("pillar is %T", decoded)
				} else if g, _ := m.Get("greeting"); g != "hello" {
					t.Errorf("greeting = %#v; the top file's pillar should still arrive", g)
				}
			} else {
				if err == nil {
					t.Fatal("a hard external pillar failure served a pillar")
				}
				// The hub tells the node only that its pillar did not
				// compile, and keeps the why for its own log, so a node
				// learns nothing about another source's failure. What
				// matters is that it is that answer and not a request
				// the hub dropped mid-stream.
				if msg := err.Error(); strings.Contains(msg, "INTERNAL_ERROR") || !strings.Contains(msg, "could not compile") {
					t.Errorf("want the hub's refusal, not a dropped request: %v", err)
				}
			}

			var buf bytes.Buffer
			if err := reg.Write(&buf); err != nil {
				t.Fatal(err)
			}
			if want := `halite_pillar_ext_failures_total{source="vault"} 1`; !strings.Contains(buf.String(), want) {
				t.Errorf("exposition does not have %s:\n%s", want, grepLines(buf.String(), "halite_pillar_ext"))
			}
		})
	}
}

func grepLines(s, sub string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, sub) {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
