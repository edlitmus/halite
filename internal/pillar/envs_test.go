package pillar

import (
	"strings"
	"testing"
)

// envLoader is memLoader serving more than one environment, base first
// and the rest in the order given, as fileserver.Roots lists them.
type envLoader struct {
	memLoader
	envs []string
}

func (e envLoader) Envs() []string { return e.envs }

func compileEnvs(t *testing.T, envs []string, files map[string]string, cfg Config) *Compiled {
	t.Helper()
	if cfg.NodeID == "" {
		cfg.NodeID = "web1.prod"
	}
	c := &Compiler{Loader: envLoader{memLoader(files), envs}, Config: cfg}
	return c.Compile()
}

// A tree whose pillar lives in two repositories, served as `base` and
// `ref`, each with its own top file. DIVERGENCE 5.286.
var twoEnvTree = map[string]string{
	"base|top":    "base:\n  '*':\n    - common\n",
	"base|common": "common_key: from-base\n",
	"ref|top":     "ref:\n  '*':\n    - refapp\n",
	"ref|refapp":  "ref_key: from-ref\n",
}

// With pillarenv unset, Salt reads every environment's top file and
// merges them, so a node gets both repositories' pillar. This compiled
// base alone, and the `ref` repository reached no node in `base`.
func TestAnUnsetPillarenvCompilesEveryEnvironment(t *testing.T) {
	out := compileEnvs(t, []string{"base", "ref"}, twoEnvTree, Config{Env: "base", AllEnvs: true})
	if err := out.Err(); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"common_key": "from-base", "ref_key": "from-ref"} {
		if got, _ := out.Pillar.Get(k); got != want {
			t.Errorf("%s = %v, want %q; pillar %v", k, got, want, out.Pillar)
		}
	}
	if got := strings.Join(out.SLS, ","); got != "common,refapp" {
		t.Errorf("merge order %s, want common,refapp (base first)", got)
	}
}

// With pillarenv set, Salt reads that environment's top file and counts
// only the entries under its own heading.
func TestASetPillarenvCompilesThatEnvironmentAlone(t *testing.T) {
	out := compileEnvs(t, []string{"base", "ref"}, twoEnvTree, Config{Env: "ref"})
	if err := out.Err(); err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Pillar.Get("common_key"); ok {
		t.Errorf("pillarenv ref delivered base's pillar: %v", out.Pillar)
	}
	if got, _ := out.Pillar.Get("ref_key"); got != "from-ref" {
		t.Errorf("ref_key = %v; pillar %v", got, out.Pillar)
	}
}

// A file listed under a heading is read from that heading's environment.
// This read every listed file from the one environment being compiled,
// whatever heading it sat under, so a `ref:` entry in base's top file
// delivered base's copy of the file -- or failed to find one.
func TestAnEntryIsReadFromTheEnvironmentItsHeadingNames(t *testing.T) {
	files := map[string]string{
		"base|top":    "base:\n  '*':\n    - common\nref:\n  '*':\n    - refapp\n",
		"base|common": "common_key: from-base\n",
		"ref|refapp":  "ref_key: from-ref\n",
	}
	out := compileEnvs(t, []string{"base", "ref"}, files, Config{Env: "base", AllEnvs: true})
	if err := out.Err(); err != nil {
		t.Fatal(err)
	}
	if got, _ := out.Pillar.Get("ref_key"); got != "from-ref" {
		t.Errorf("ref_key = %v; the ref: entry was not read from ref. Pillar %v", got, out.Pillar)
	}

	// And with pillarenv set to base, the ref: heading is not this
	// compilation's at all.
	only := compileEnvs(t, []string{"base", "ref"}, files, Config{Env: "base"})
	if err := only.Err(); err != nil {
		t.Fatal(err)
	}
	if _, ok := only.Pillar.Get("ref_key"); ok {
		t.Errorf("pillarenv base delivered the ref: heading: %v", only.Pillar)
	}
}

// A heading naming an environment nothing serves is an error, as it is
// in Salt, rather than a silent read from somewhere else.
func TestAHeadingForAnUnservedEnvironmentIsAnError(t *testing.T) {
	files := map[string]string{
		"base|top":    "base:\n  '*':\n    - common\nqa:\n  '*':\n    - qaonly\n",
		"base|common": "common_key: from-base\n",
		"base|qaonly": "qa_key: wrongly-from-base\n",
	}
	out := compileEnvs(t, []string{"base"}, files, Config{Env: "base", AllEnvs: true})
	err := out.Err()
	if err == nil || !strings.Contains(err.Error(), `environment "qa", which pillar_roots does not serve`) {
		t.Fatalf("want an unserved-environment error, got %v; pillar %v", err, out.Pillar)
	}
	if _, ok := out.Pillar.Get("qa_key"); ok {
		t.Errorf("qaonly was read from base: %v", out.Pillar)
	}
}

// Two top files naming the same heading and target: the later one's
// entry replaces the earlier, as merge_tops does. Later is the
// environment order, base first and then sorted; Salt's own order comes
// from a Python set and is not stable between runs.
func TestALaterTopFileReplacesTheSameHeadingAndTarget(t *testing.T) {
	files := map[string]string{
		"base|top":   "base:\n  '*':\n    - one\n",
		"base|one":   "k: one\n",
		"base|two":   "k: two\n",
		"other|top":  "base:\n  '*':\n    - two\n",
		"other|none": "",
	}
	out := compileEnvs(t, []string{"base", "other"}, files, Config{Env: "base", AllEnvs: true})
	if err := out.Err(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(out.SLS, ","); got != "two" {
		t.Errorf("delivered %s, want two alone: the later top file's entry replaces the earlier", got)
	}
}

// ignore_missing still applies per heading.
func TestIgnoreMissingAppliesInEveryEnvironment(t *testing.T) {
	files := map[string]string{
		"base|top":    "base:\n  '*':\n    - common\n",
		"base|common": "common_key: from-base\n",
		"ref|top":     "ref:\n  '*':\n    - ignore_missing: true\n    - absent\n",
	}
	out := compileEnvs(t, []string{"base", "ref"}, files, Config{Env: "base", AllEnvs: true})
	if err := out.Err(); err != nil {
		t.Fatalf("an ignore_missing file in ref failed the compile: %v", err)
	}
}
