package main

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/extpillar"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// parseExtPillarYAML reads an `ext_pillar` block the way the loader
// hands it over, so the tests describe what an operator writes rather
// than the value tree it becomes.
func parseExtPillarYAML(t *testing.T, src string, defaultIgnore bool) ([]extPillarSpec, error) {
	t.Helper()
	doc, _, err := yaml.Parse([]byte(src), yaml.Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := doc.(*value.Map)
	if !ok {
		t.Fatalf("the document is %s", value.TypeName(doc))
	}
	raw, ok := m.Get("ext_pillar")
	if !ok {
		t.Fatal("no ext_pillar in the document")
	}
	return parseExtPillar(raw, defaultIgnore)
}

// Salt's own shape is read: a list of single-key mappings, the key
// naming the source, and the source's own configuration under it.
func TestExtPillarReadsSaltsShape(t *testing.T) {
	specs, err := parseExtPillarYAML(t, `
ext_pillar:
  - aws_secrets_manager:
      - name: base.ubuntu_pro
        secret_id: arn:aws:secretsmanager:us-east-1:1:secret:pro-AbCdEf
      - pillar_key: database.creds
        secret_name: vmop/prod/database
        region: us-east-1
`, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 {
		t.Fatalf("it read %d sources", len(specs))
	}
	if specs[0].Name != extpillar.AWSSecretsName {
		t.Errorf("the source is %q", specs[0].Name)
	}
	want := []extpillar.Secret{
		{Key: "base.ubuntu_pro", SecretID: "arn:aws:secretsmanager:us-east-1:1:secret:pro-AbCdEf"},
		{Key: "database.creds", SecretID: "vmop/prod/database", Region: "us-east-1"},
	}
	if len(specs[0].Secrets) != len(want) {
		t.Fatalf("it read %d secrets", len(specs[0].Secrets))
	}
	for i, w := range want {
		if specs[0].Secrets[i] != w {
			t.Errorf("secret %d is %+v, want %+v", i+1, specs[0].Secrets[i], w)
		}
	}
}

// A source this build does not have is refused at startup. Salt would
// have imported whatever Python file was on the file server.
func TestAnUnknownSourceIsRefused(t *testing.T) {
	_, err := parseExtPillarYAML(t, `
ext_pillar:
  - vault:
      url: https://vault.example
`, false)
	if err == nil {
		t.Fatal("an unknown source was accepted")
	}
	if !strings.Contains(err.Error(), "not an external pillar source this build has") {
		t.Errorf("the error is %q", err)
	}
	// And it names where to find out what is and is not built.
	if !strings.Contains(err.Error(), "DIVERGENCE.md") {
		t.Errorf("the error does not say where to look: %q", err)
	}
}

// `fail:` inside a source's block overrides `ext_pillar_fail` for that
// source, which is the per-source exception SPEC 12.7 calls for.
func TestAPerSourceFailOverridesTheDefault(t *testing.T) {
	specs, err := parseExtPillarYAML(t, `
ext_pillar:
  - aws_secrets_manager:
      - fail: ignore
      - name: k
        secret_id: vmop/k
        region: us-east-1
`, false)
	if err != nil {
		t.Fatal(err)
	}
	if !specs[0].FailIgnore {
		t.Error("`fail: ignore` did not take")
	}
	// `fail` is a directive, not a secret.
	if len(specs[0].Secrets) != 1 {
		t.Errorf("it read %d secrets", len(specs[0].Secrets))
	}

	// And the other direction: a hub that sets ignore globally can still
	// require one source to be there.
	specs, err = parseExtPillarYAML(t, `
ext_pillar:
  - aws_secrets_manager:
      - fail: hard
      - name: k
        secret_id: vmop/k
        region: us-east-1
`, true)
	if err != nil {
		t.Fatal(err)
	}
	if specs[0].FailIgnore {
		t.Error("`fail: hard` did not override the global ignore")
	}
}

// The global default applies to a source that says nothing.
func TestTheGlobalFailDefaultApplies(t *testing.T) {
	for _, ignore := range []bool{true, false} {
		specs, err := parseExtPillarYAML(t, `
ext_pillar:
  - aws_secrets_manager:
      - name: k
        secret_id: vmop/k
        region: us-east-1
`, ignore)
		if err != nil {
			t.Fatal(err)
		}
		if specs[0].FailIgnore != ignore {
			t.Errorf("with default ignore=%v the source is %v", ignore, specs[0].FailIgnore)
		}
	}
}

// A secret missing what it needs is refused here, not at the first
// node's compilation.
func TestAnIncompleteSecretIsRefused(t *testing.T) {
	for _, src := range []string{
		"ext_pillar:\n  - aws_secrets_manager:\n      - secret_id: vmop/k\n",
		"ext_pillar:\n  - aws_secrets_manager:\n      - name: k\n",
		"ext_pillar:\n  - aws_secrets_manager:\n      - name: k\n        secret_arn: vmop/k\n",
	} {
		if _, err := parseExtPillarYAML(t, src, false); err == nil {
			t.Errorf("accepted:\n%s", src)
		}
	}
}

// A list where a mapping belongs, and a mapping where a list belongs,
// both say which they wanted.
func TestAMisshapenBlockSaysWhatItWanted(t *testing.T) {
	if _, err := parseExtPillarYAML(t, "ext_pillar:\n  - aws_secrets_manager\n", false); err == nil {
		t.Error("a bare source name was accepted")
	}
	if _, err := parseExtPillarYAML(t, "ext_pillar:\n  aws_secrets_manager: []\n", false); err == nil {
		t.Error("a mapping was accepted where a list belongs")
	}
}
