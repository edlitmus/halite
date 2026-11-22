package migrate

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// blackoutFindings are the findings this audit makes, by file, line and
// subject, so a test can say exactly which it expected.
func blackoutFindings(rep *Report) []string {
	var out []string
	for _, f := range rep.Findings {
		if f.Category == CatSemantics && strings.Contains(f.Action, "issue 780") {
			out = append(out, fmt.Sprintf("%s:%d %s", f.File, f.Line, f.Subject))
		}
	}
	return out
}

// Salt reads blackout from pillar and grains only. halite reads it from
// pillar too (DIVERGENCE 5.703) and not from grains, so a grain's
// blackout is lost on migration and a pillar's is kept. The audit names
// each place Salt would have read it, and says which is which (issue
// 780).
func TestABlackoutSetWhereSaltReadsItIsReported(t *testing.T) {
	root := t.TempDir()
	mkdirAll(t, filepath.Join(root, "state"))
	mkdirAll(t, filepath.Join(root, "pillar"))
	writeFile(t, filepath.Join(root, "pillar", "maintenance.sls"), "#!jinja|yaml\n"+
		"# minion_blackout is set during the window\n"+ // lexicon:allow
		"minion_blackout: True\n"+ // lexicon:allow
		"minion_blackout_whitelist:\n"+ // lexicon:allow
		"  - test.ping\n"+
		"minion_blackout_reason: window\n") // lexicon:allow
	writeFile(t, filepath.Join(root, "state", "freeze.sls"), "freeze:\n"+
		"  grains.present:\n"+
		"    - name: minion_blackout\n"+ // lexicon:allow
		"    - value: True\n")
	config := filepath.Join(root, "agent.conf")
	writeFile(t, config, "grains:\n  minion_blackout: true\n  role: web\n") // lexicon:allow

	rep, err := Run(Options{Root: root, ConfigFiles: []string{config}})
	if err != nil {
		t.Fatal(err)
	}

	got := strings.Join(blackoutFindings(rep), "\n")
	want := strings.Join([]string{
		config + ":0 grains:minion_blackout",          // lexicon:allow
		"maintenance.sls:3 minion_blackout",           // lexicon:allow
		"maintenance.sls:4 minion_blackout_whitelist", // lexicon:allow
		"state/freeze.sls:3 minion_blackout",          // lexicon:allow
	}, "\n")
	if got != want {
		t.Errorf("blackout findings:\n%s\nwant:\n%s", got, want)
	}
	for _, f := range rep.Findings {
		if !strings.Contains(f.Action, "issue 780") {
			continue
		}
		pillar := f.File == "maintenance.sls"
		switch {
		case pillar && f.Severity != Note:
			t.Errorf("%s:%d is %s; the node honours pillar, so it is a note", f.File, f.Line, f.Severity)
		case pillar && !strings.Contains(f.Action, "Nothing to do"):
			t.Errorf("%s:%d tells the operator to act on a key the node honours: %q", f.File, f.Line, f.Action)
		case !pillar && f.Severity != Review:
			t.Errorf("%s:%d is %s; a grain's blackout is lost, so it is review", f.File, f.Line, f.Severity)
		case !pillar && !strings.Contains(f.Action, "does not read this from grains"):
			t.Errorf("%s:%d does not say grains are not read: %q", f.File, f.Line, f.Action)
		}
	}
}

// The shim converts the top-level key, which Salt never read from its
// configuration file, so "halite calls it quiesce" read as though the
// node had been blacked out. The finding has to say it was not, and that
// here it will be.
func TestTheConvertedBlackoutKeySaysSaltNeverReadItThere(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "top.sls"), "base: {}\n")
	config := filepath.Join(root, "agent.conf")
	writeFile(t, config, "minion_blackout: true\n") // lexicon:allow

	rep, err := Run(Options{Root: root, ConfigFiles: []string{config}})
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range rep.Findings {
		if f.Subject != "minion_blackout" || f.Category != CatConfig { // lexicon:allow
			continue
		}
		if !strings.Contains(f.Action, "Salt never read this key from its configuration file") {
			t.Errorf("the conversion does not say Salt ignored the key there: %q", f.Action)
		}
		if f.Severity != Review {
			t.Errorf("the conversion is %s; it starts refusing jobs Salt ran, so it is review", f.Severity)
		}
		return
	}
	t.Errorf("no finding for the converted key: %v", rep.Findings)
}
