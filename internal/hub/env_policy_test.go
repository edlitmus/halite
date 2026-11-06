package hub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
)

// Issue 620: a role's SLS rules said nothing about the environment, and
// with gitfs every branch is one, so `allow_sls: [web.*]` let a role apply
// web.x from any branch. allow_env and deny_env judge the environment a
// job is dispatched with, which has to reach the policy at every site
// that authorizes one, as the target kind had to (DIVERGENCE 5.296). Each
// subtest below is one of those sites, with a request that differs from
// an allowed one only in its environment.

const envPolicySrc = `
roles:
  deployer:
    - target: '*'
      functions: ['state.*']
      args:
        'state.apply':
          allow_env: ['base']
    - runners: ['state.orchestrate', 'state.orch_resume']
      args:
        'state.orchestrate':
          allow_env: ['base']
bindings:
  - principal: 'cert:CN=alice'
    roles: ['deployer']
`

func envLab(t *testing.T) *lab {
	t.Helper()
	l := orchLab(t, map[string]string{
		"deploy.sls":   "apply_web:\n  salt.state:\n    - tgt: 'web1.example'\n    - sls: [web]\n",
		"sneaky.sls":   "apply_web:\n  salt.state:\n    - tgt: 'web1.example'\n    - sls: [web]\n    - saltenv: attacker\n",
		"web/init.sls": "noop:\n  test.nop: []\n",
	})
	p, _, err := policy.Load([]byte(envPolicySrc), "env-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	catalogued(p)
	l.server.Policy = p
	t.Cleanup(l.answering(t, "web1.example", func(string) bool { return true }))
	return l
}

func refusedForEnv(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s was not refused", what)
	}
	if !strings.Contains(err.Error(), `environment "attacker"`) {
		t.Errorf("%s was refused, but not for its environment: %v", what, err)
	}
}

func TestAllowEnvHoldsEveryHubSiteThatDispatches(t *testing.T) {
	ctx := context.Background()

	// POST /v1/jobs (Server.submit).
	t.Run("submit", func(t *testing.T) {
		l := envLab(t)
		alice := l.operator(t, "alice")
		for _, env := range []string{"base", ""} {
			if _, err := alice.Submit(ctx, transport.SubmitRequest{
				Target: "web1.example", Fun: "state.sls", Arg: []string{"web"}, Env: env, Test: true,
			}); err != nil {
				t.Errorf("a state.sls in %q was refused: %v", env, err)
			}
		}
		_, err := alice.Submit(ctx, transport.SubmitRequest{
			Target: "web1.example", Fun: "state.sls", Arg: []string{"web"}, Env: "attacker", Test: true,
		})
		refusedForEnv(t, "a state.sls from the branch `attacker`", err)
	})

	// A recorded job is read, killed and resumed as the request it was
	// (requestForJob, Server.resume).
	t.Run("a recorded job", func(t *testing.T) {
		l := envLab(t)
		alice := l.operator(t, "alice")
		jid := l.server.clock().Next()
		if err := l.server.Jobs.Put(&job.Job{
			JID: jid, Fun: "state.sls", Arg: []string{"web"}, Env: "attacker",
			Target: "web1.example", TargetKind: "L", Submitter: "cert:CN=alice",
			// Complete and delivered everywhere, so that a resume or a
			// kill the policy wrongly allows fails at once on the job's
			// state and says so, instead of starting a batch that holds
			// the lab open until the test times out.
			Nonce: "n", State: job.Complete, Created: l.server.now(), Expires: l.server.now().Add(time.Hour),
			Nodes: []string{"web1.example"}, Delivered: []string{"web1.example"},
		}); err != nil {
			t.Fatal(err)
		}
		_, err := alice.ResumeJob(ctx, string(jid))
		refusedForEnv(t, "resuming a job from the branch `attacker`", err)
		_, err = alice.KillJob(ctx, string(jid))
		refusedForEnv(t, "killing a job from the branch `attacker`", err)
	})

	// CallRunner, for the orchestration runner's own `env`.
	t.Run("the orchestration runner", func(t *testing.T) {
		l := envLab(t)
		for _, c := range []struct {
			arg   []string
			kwarg map[string]any
		}{
			{nil, map[string]any{"sls": "deploy", "env": "attacker", "test": true}},
			{[]string{"deploy", "attacker"}, map[string]any{"test": true}},
		} {
			_, err := l.server.CallRunner(ctx, RunnerCall{
				Principal: "cert:CN=alice", Fun: "state.orchestrate", Arg: c.arg, Kwarg: c.kwarg,
			})
			var refusal *policyRefusal
			if !errors.As(err, &refusal) {
				t.Errorf("an orchestration from `attacker` (%v %v) was not refused by the policy: %v", c.arg, c.kwarg, err)
				continue
			}
			refusedForEnv(t, "an orchestration from `attacker`", err)
		}
		out, err := l.server.CallRunner(ctx, RunnerCall{
			Principal: "cert:CN=alice", Fun: "state.orchestrate", Kwarg: map[string]any{"sls": "deploy"},
		})
		if err != nil || !out.Success {
			t.Fatalf("an orchestration in base was refused: %v %+v", err, out)
		}
	})

	// dispatchFor: a step's own `saltenv` is judged when it is sent.
	t.Run("an orchestration step", func(t *testing.T) {
		l := envLab(t)
		out, err := l.server.CallRunner(ctx, RunnerCall{
			Principal: "cert:CN=alice", Fun: "state.orchestrate", Kwarg: map[string]any{"sls": "sneaky"},
		})
		if err != nil {
			t.Fatalf("the orchestration itself was refused: %v", err)
		}
		if out.Success {
			t.Fatal("a step with `saltenv: attacker` ran")
		}
		ret, _ := json.Marshal(out.Return)
		if !strings.Contains(strings.ReplaceAll(string(ret), `\"`, `"`)+out.Err, `environment "attacker"`) {
			t.Errorf("the step failed, but not for its environment: %s %s", out.Err, ret)
		}
	})

	// state.orch_resume, judged on the run's own environment.
	t.Run("a resumed orchestration", func(t *testing.T) {
		l := envLab(t)
		jid := l.server.clock().Next()
		if err := l.server.orchStore().Put(&OrchRun{
			JID: jid, SLS: []string{"deploy"}, Env: "attacker", Principal: "cert:CN=alice", State: OrchComplete,
		}); err != nil {
			t.Fatal(err)
		}
		out, err := l.server.CallRunner(ctx, RunnerCall{
			Principal: "cert:CN=alice", Fun: "state.orch_resume",
			Arg: []string{string(jid), "apply_web"}, Kwarg: map[string]any{"test": true},
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.Success {
			t.Fatal("a run from `attacker` was resumed")
		}
		refusedForEnv(t, "resuming a run from `attacker`", errors.New(out.Err))
	})
}
