package extpillar

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

func parseYAML(t *testing.T, src string, defaultIgnore bool) ([]Spec, error) {
	t.Helper()
	doc, _, err := yaml.Parse([]byte(src), yaml.Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := doc.(*value.Map)
	if !ok {
		t.Fatalf("the document is %s", value.TypeName(doc))
	}
	raw, _ := m.Get("ext_pillar")
	return ParseList(raw, defaultIgnore)
}

// Salt's shape is read, and the source's own block is handed over
// untouched: the hub does not know an extension's schema and must not
// pretend to.
func TestTheSourceBlockIsNotInterpreted(t *testing.T) {
	specs, err := parseYAML(t, `
ext_pillar:
  - aws_secrets_manager:
      region: us-east-1
      secrets:
        - name: database.creds
          secret_id: vmop/prod/database
`, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Name != "aws_secrets_manager" {
		t.Fatalf("it read %+v", specs)
	}
	block, ok := specs[0].Config.(*value.Map)
	if !ok {
		t.Fatalf("the block is %s", value.TypeName(specs[0].Config))
	}
	if got, _ := block.Get("region"); got != "us-east-1" {
		t.Errorf("region is %v", got)
	}
	// A key the hub has never heard of survives, because it belongs to
	// the extension.
	specs, err = parseYAML(t, "ext_pillar:\n  - thing:\n      wholly_unknown: 1\n", false)
	if err != nil {
		t.Fatal(err)
	}
	block = specs[0].Config.(*value.Map)
	if !block.Has("wholly_unknown") {
		t.Error("the hub dropped a key it did not recognise")
	}
}

// `fail` is the one key the hub reads, and it is taken out of the block
// so an extension never sees a setting that was not meant for it.
func TestFailIsReadAndStripped(t *testing.T) {
	specs, err := parseYAML(t, `
ext_pillar:
  - aws_secrets_manager:
      fail: ignore
      region: us-east-1
`, false)
	if err != nil {
		t.Fatal(err)
	}
	if !specs[0].Ignore {
		t.Error("`fail: ignore` did not take")
	}
	block := specs[0].Config.(*value.Map)
	if block.Has("fail") {
		t.Error("`fail` was passed to the extension")
	}
	if !block.Has("region") {
		t.Error("stripping fail took the rest of the block")
	}
}

// The list form Salt writes most blocks in carries `fail` as an entry,
// and that is stripped too.
func TestFailInTheListForm(t *testing.T) {
	specs, err := parseYAML(t, `
ext_pillar:
  - aws_secrets_manager:
      - fail: ignore
      - name: k
        secret_id: vmop/k
`, false)
	if err != nil {
		t.Fatal(err)
	}
	if !specs[0].Ignore {
		t.Error("`fail: ignore` did not take")
	}
	list, ok := specs[0].Config.([]any)
	if !ok {
		t.Fatalf("the block is %s", value.TypeName(specs[0].Config))
	}
	if len(list) != 1 {
		t.Errorf("the `fail` entry was passed through: %v", list)
	}
}

// The global default applies to a source that says nothing, and a
// source may override it in either direction.
func TestTheGlobalFailDefaultAndItsOverride(t *testing.T) {
	for _, ignore := range []bool{true, false} {
		specs, err := parseYAML(t, "ext_pillar:\n  - src: {}\n", ignore)
		if err != nil {
			t.Fatal(err)
		}
		if specs[0].Ignore != ignore {
			t.Errorf("default ignore=%v gave %v", ignore, specs[0].Ignore)
		}
	}
	specs, err := parseYAML(t, "ext_pillar:\n  - src:\n      fail: hard\n", true)
	if err != nil {
		t.Fatal(err)
	}
	if specs[0].Ignore {
		t.Error("`fail: hard` did not override a global ignore")
	}
}

// A misshapen list says which shape it wanted.
func TestAMisshapenListIsRefused(t *testing.T) {
	for _, src := range []string{
		"ext_pillar:\n  - just_a_name\n",
		"ext_pillar:\n  a_mapping: {}\n",
		"ext_pillar:\n  - src:\n      fail: perhaps\n",
	} {
		if _, err := parseYAML(t, src, false); err == nil {
			t.Errorf("accepted:\n%s", src)
		}
	}
}

// An absent setting is no sources, not an error.
func TestAnAbsentListIsNoSources(t *testing.T) {
	specs, err := ParseList(nil, false)
	if err != nil || len(specs) != 0 {
		t.Errorf("specs=%v err=%v", specs, err)
	}
}

// Order is preserved, because SPEC 12.7 says each source sees what the
// ones before it produced and that is meaningless without an order.
func TestOrderIsPreserved(t *testing.T) {
	specs, err := parseYAML(t, "ext_pillar:\n  - first: {}\n  - second: {}\n  - third: {}\n", false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range specs {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "first,second,third" {
		t.Errorf("order is %v", names)
	}
}
