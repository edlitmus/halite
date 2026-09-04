package saltdiff

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/builtin"
	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/grains"
	"github.com/edlitmus/halite/internal/runner"
	"github.com/edlitmus/halite/internal/value"
)

// SPEC 31 asks the differential to compare the low state, the pillar,
// and the state *results*. The third was recorded as absent because
// comparing results means applying a tree, and applying one needs a
// container to apply it in.
//
// Test mode is the way round that. It is a prediction rather than an
// application: every state reads the system and reports what it would
// do, and writes nothing. Two implementations predicting the same thing
// can be compared without either of them touching the host, and a
// difference is exactly what SPEC 31 is looking for — one of them is
// wrong about what a real run would do.
//
// What is compared is the verdict and whether there would be changes,
// not the wording: the comment is prose and the two write it
// differently on purpose.

// resultDeviation records one state the two predict differently.
type resultDeviation struct {
	tree string
	key  string
	// salt lists the major versions the difference was observed under,
	// or is empty for every version. Observed majors rather than a
	// range, for the reason the low state's table gives.
	salt   []string
	reason string
}

// appliesTo reports whether a row was recorded for this Salt.
func (d resultDeviation) appliesTo(version string) bool {
	if len(d.salt) == 0 {
		return true
	}
	for _, major := range d.salt {
		if strings.HasPrefix(version, major) {
			return true
		}
	}
	return false
}

const devOnFailInTestMode = "Salt fires onfail when the target did not *succeed*, and in test " +
	"mode a state that would change reports neither success nor failure, so Salt predicts that an " +
	"onfail state will run when a real run would not run it. halite fires onfail when the target " +
	"failed, which is what the requisite means and what the real run will do."

const devTestModeChanges = "in test mode halite reports what would change and Salt reports " +
	"nothing. SPEC 11.6 asks a state that would change to say what, and an empty `changes` on a " +
	"result of None tells an operator only that something was going to happen."

var resultDeviations = []resultDeviation{
	{"requisites", "cmd_|-fourth_|-echo fourth_|-run", nil, devOnFailInTestMode},
	{"basic", "cmd_|-a_script_|-salt://files/probe.sh_|-script", nil, devTestModeChanges},
}

type prediction struct {
	verdict string // "would change", "unchanged", or "failed"
	changes bool
	// why is the implementation's own comment. It is not compared — the
	// two write it differently on purpose — but a difference reported
	// without it says only that one of them disagreed, which leaves the
	// reader to reproduce the run to find out why.
	why string
}

func (p prediction) String() string {
	out := p.verdict
	if p.changes {
		out += ", with changes"
	}
	if p.why != "" {
		out += ": " + clip(strings.TrimSpace(strings.SplitN(p.why, "\n", 2)[0]))
	}
	return out
}

// same compares the verdict and whether there would be changes, and not
// the comment.
func (p prediction) same(q prediction) bool {
	return p.verdict == q.verdict && p.changes == q.changes
}

// saltPredictions runs a tree through Salt in test mode.
func saltPredictions(t *testing.T, saltcall string, tree corpusTree) map[string]prediction {
	t.Helper()
	out := saltRun(t, saltcall, tree, "state.apply", "test=True")

	// Salt returns a mapping of `state_|-id_|-name_|-fun` to the result,
	// or a list of strings when the compilation itself failed.
	var wrapper struct {
		Local map[string]struct {
			Result  *bool          `json:"result"`
			Changes map[string]any `json:"changes"`
			Comment string         `json:"comment"`
		} `json:"local"`
	}
	if err := json.Unmarshal(out, &wrapper); err != nil {
		t.Fatalf("decoding salt's test-mode results for %s: %v\n%s", tree.name, err, clip(string(out)))
	}
	got := map[string]prediction{}
	for key, res := range wrapper.Local {
		p := prediction{changes: len(res.Changes) > 0, why: res.Comment}
		switch {
		case res.Result == nil:
			p.verdict = "would change"
		case *res.Result:
			p.verdict = "unchanged"
		default:
			p.verdict = "failed"
		}
		got[key] = p
	}
	return got
}

// halitePredictions runs the same tree through halite in test mode.
func halitePredictions(t *testing.T, tree corpusTree) map[string]prediction {
	t.Helper()
	compiled := haliteLowstate(t, tree)
	registries := builtin.New()
	g, _ := grains.Collect(grains.Options{NodeID: tree.id})
	pillarValues := halitePillar(t, tree)

	ctx := &hexec.Context{
		Ctx: context.Background(), Grains: g, Pillar: pillarValues,
		Config: value.NewMap(0), NodeID: tree.id, Env: "base", JobID: "saltdiff",
		Test:   true,
		Files:  fileserver.NewFetcher(fileserver.NewRoots(map[string][]string{"base": {tree.states}})),
		Runner: &hexec.OSRunner{},
	}
	ctx.Dispatch = dispatch{registries.Exec}

	r := &runner.Runner{States: registries.States, Exec: registries.Exec, Ctx: ctx}
	out := r.Run(compiled)

	got := map[string]prediction{}
	for _, res := range out.Results {
		p := prediction{changes: res.Result.HasChanges(), why: res.Result.Comment}
		switch {
		case res.Result.Failed():
			p.verdict = "failed"
		case res.Result.Result == nil:
			p.verdict = "would change"
		default:
			p.verdict = "unchanged"
		}
		got[res.Chunk.Key()] = p
	}
	return got
}

type dispatch struct{ r *hexec.Registry }

func (d dispatch) Call(c *hexec.Context, name string, args *value.Map) (any, error) {
	return d.r.Call(c, name, args)
}
func (d dispatch) CallPositional(c *hexec.Context, name string, args []any, kwargs *value.Map) (any, error) {
	return d.r.CallPositional(c, name, args, kwargs)
}
func (d dispatch) Has(name string) bool { return d.r.Has(name) }

func clip(s string) string {
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}

// TestResultsMatchSalt is the third comparison of SPEC 31's differential.
func TestResultsMatchSalt(t *testing.T) {
	if os.Getenv("HALITE_SALTDIFF_RESULTS") == "" {
		t.Skip("test-mode result differential skipped: set HALITE_SALTDIFF_RESULTS=1. " +
			"It evaluates every state against this host, which reads the system and writes nothing, " +
			"but it is slower than the compilation comparisons and touches more of the machine.")
	}
	saltcall := saltCall(t)
	version := saltVersion(t, saltcall)

	recorded := map[string]string{}
	for _, d := range resultDeviations {
		if !d.appliesTo(version) {
			continue
		}
		recorded[d.tree+" "+d.key] = d.reason
	}
	seen := map[string]bool{}

	for _, tree := range trees(t) {
		t.Run(tree.name, func(t *testing.T) {
			theirs := saltPredictions(t, saltcall, tree)
			ours := halitePredictions(t, tree)

			for key, our := range ours {
				their, both := theirs[key]
				if !both {
					t.Errorf("halite predicted %s for %s; Salt has no such state", our, key)
					continue
				}
				if our.same(their) {
					continue
				}
				k := tree.name + " " + key
				seen[k] = true
				if _, ok := recorded[k]; !ok {
					t.Errorf("%s:\n  halite predicts %s\n  salt   predicts %s", key, our, their)
				}
			}
			for key := range theirs {
				if _, ok := ours[key]; !ok {
					t.Errorf("Salt has %s and halite does not", key)
				}
			}
		})
	}
	for key := range recorded {
		if !seen[key] {
			t.Errorf("%q has a deviation row and did not differ", key)
		}
	}
}
