package yamlite

import (
	"strings"
	"testing"
)

const slsSample = `
# comment
install_nginx:
  pkg.installed:
    - name: nginx

/usr/local/etc/nginx/nginx.conf:
  file.managed:
    - source: files/nginx.conf
    - mode: "0644"
    - require:
      - pkg: install_nginx

nginx_service:
  service.running:
    - name: nginx
    - enable: true
    - watch:
      - file: /usr/local/etc/nginx/nginx.conf
`

func TestParseSLS(t *testing.T) {
	v, err := Parse(slsSample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root, ok := v.(*Map)
	if !ok {
		t.Fatalf("root is %T, want *Map", v)
	}
	if len(root.Keys) != 3 {
		t.Fatalf("got %d top-level keys, want 3: %v", len(root.Keys), root.Keys)
	}
	if root.Keys[0] != "install_nginx" {
		t.Errorf("order not preserved: %v", root.Keys)
	}

	body := root.Vals["/usr/local/etc/nginx/nginx.conf"].(*Map)
	argsAny, _ := body.Get("file.managed")
	args := argsAny.([]any)
	if len(args) != 3 {
		t.Fatalf("file.managed args: got %d, want 3", len(args))
	}
	mode := args[1].(*Map)
	if mv, _ := mode.Get("mode"); mv != "0644" {
		t.Errorf("mode = %v, want 0644 (quotes stripped)", mv)
	}
	req := args[2].(*Map)
	reqList, _ := req.Get("require")
	pair := reqList.([]any)[0].(*Map)
	if id, _ := pair.Get("pkg"); id != "install_nginx" {
		t.Errorf("require ref = %v", id)
	}
}

func TestColonInValue(t *testing.T) {
	v, err := Parse("url: https://example.com:8443/path\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m := v.(*Map)
	if got, _ := m.Get("url"); got != "https://example.com:8443/path" {
		t.Errorf("url = %v", got)
	}
}

func TestTabsRejected(t *testing.T) {
	if _, err := Parse("a:\n\tb: c\n"); err == nil {
		t.Fatal("expected error for tab indentation")
	}
}

func TestInlineComment(t *testing.T) {
	v, err := Parse("key: value # trailing\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, _ := v.(*Map).Get("key"); got != "value" {
		t.Errorf("key = %q", got)
	}
}

func TestDoubleQuoteEscapes(t *testing.T) {
	v, err := Parse(`key: "line1\nline2\ttab"` + "\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, _ := v.(*Map).Get("key"); got != "line1\nline2\ttab" {
		t.Errorf("key = %q", got)
	}
}

func TestSingleQuoteLiteral(t *testing.T) {
	v, _ := Parse(`key: 'no\nescapes'` + "\n")
	if got, _ := v.(*Map).Get("key"); got != `no\nescapes` {
		t.Errorf("key = %q", got)
	}
}

func TestMultiKeyListItems(t *testing.T) {
	// Ordinary YAML: a list entry's content starts after the dash, and
	// further keys at that column belong to the same mapping.
	tree, err := Parse(`disk:
  - mount: /var
    threshold: "80"
    interval: 30s
  - mount: /
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root := tree.(*Map)
	list, ok := root.Vals["disk"].([]any)
	if !ok {
		t.Fatalf("disk is %T, want a list", root.Vals["disk"])
	}
	if len(list) != 2 {
		t.Fatalf("got %d entries, want 2", len(list))
	}

	first := list[0].(*Map)
	if len(first.Keys) != 3 {
		t.Fatalf("first entry has keys %v, want three", first.Keys)
	}
	// Order is preserved within the item, as everywhere else.
	if first.Keys[0] != "mount" || first.Keys[2] != "interval" {
		t.Errorf("key order = %v", first.Keys)
	}
	if first.Vals["threshold"] != "80" || first.Vals["interval"] != "30s" {
		t.Errorf("values = %v", first.Vals)
	}
	if second := list[1].(*Map); len(second.Keys) != 1 || second.Vals["mount"] != "/" {
		t.Errorf("second entry = %v", second.Vals)
	}
}

func TestMultiKeyListItemWithNestedValue(t *testing.T) {
	tree, err := Parse(`rules:
  - name: first
    match:
      tag: halite/job
      source: master
    action: run
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	entry := tree.(*Map).Vals["rules"].([]any)[0].(*Map)
	if entry.Vals["name"] != "first" || entry.Vals["action"] != "run" {
		t.Errorf("scalars around the nested map were lost: %v", entry.Vals)
	}
	match, ok := entry.Vals["match"].(*Map)
	if !ok {
		t.Fatalf("match is %T, want a map", entry.Vals["match"])
	}
	if match.Vals["tag"] != "halite/job" || match.Vals["source"] != "master" {
		t.Errorf("nested map = %v", match.Vals)
	}
}

func TestSingleQuoteDoubledEscape(t *testing.T) {
	v, err := Parse("key: 'it''s here'\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, _ := v.(*Map).Get("key"); got != "it's here" {
		t.Errorf("key = %q, want %q", got, "it's here")
	}
}

func TestSingleQuoteDoubledEscapeInKey(t *testing.T) {
	v, err := Parse("'who''s here': yes\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, _ := v.(*Map).Get("who's here"); got != "yes" {
		t.Errorf("value = %q, want %q", got, "yes")
	}
}

func TestSingleQuoteDoubledEscapeWithComment(t *testing.T) {
	// The '' escape must not be read as close+open by the comment
	// stripper, or the # inside the quotes would eat the value.
	v, err := Parse("key: 'it''s # not a comment' # real comment\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, _ := v.(*Map).Get("key"); got != "it's # not a comment" {
		t.Errorf("key = %q", got)
	}
}

func TestApostropheInPlainScalar(t *testing.T) {
	// A quote mid-plain-scalar is literal text, not the start of a quoted
	// region, so the trailing comment must still be stripped.
	v, err := Parse("motd: can't stop # a comment\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, _ := v.(*Map).Get("motd"); got != "can't stop" {
		t.Errorf("motd = %q, want %q", got, "can't stop")
	}
}

func TestApostropheInPlainKey(t *testing.T) {
	v, err := Parse("who's here: yes\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, _ := v.(*Map).Get("who's here"); got != "yes" {
		t.Errorf("value = %q, want %q", got, "yes")
	}
}

func TestQuotedScalarsStillQuoted(t *testing.T) {
	// Quotes at the start of a scalar (after "key: ", after "- ", or at
	// the start of content) still open quoted regions.
	v, err := Parse("key: 'a # not a comment'\nlist:\n  - 'x # kept'\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m := v.(*Map)
	if got, _ := m.Get("key"); got != "a # not a comment" {
		t.Errorf("key = %q", got)
	}
	if got := m.Vals["list"].([]any)[0]; got != "x # kept" {
		t.Errorf("list[0] = %q", got)
	}
}

func TestFlowCollectionsRejected(t *testing.T) {
	for _, src := range []string{
		"pkgs: [nginx, curl]\n",
		"conf: {a: 1}\n",
		"pkgs:\n  - [a, b]\n",
	} {
		if _, err := Parse(src); err == nil {
			t.Errorf("Parse(%q): expected flow-collection error, got nil", src)
		} else if !strings.Contains(err.Error(), "flow collection") {
			t.Errorf("Parse(%q): error %q does not mention flow collections", src, err)
		}
	}
}

func TestEmptyFlowCollectionsStillWork(t *testing.T) {
	v, err := Parse("a: []\nb: {}\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m := v.(*Map)
	if l, ok := m.Vals["a"].([]any); !ok || len(l) != 0 {
		t.Errorf("a = %#v, want empty list", m.Vals["a"])
	}
	if mm, ok := m.Vals["b"].(*Map); !ok || len(mm.Keys) != 0 {
		t.Errorf("b = %#v, want empty map", m.Vals["b"])
	}
}

func TestQuotedFlowSyntaxStaysLiteral(t *testing.T) {
	v, err := Parse(`pkgs: "[nginx, curl]"` + "\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, _ := v.(*Map).Get("pkgs"); got != "[nginx, curl]" {
		t.Errorf("pkgs = %q", got)
	}
}

func TestDuplicateKeysRejected(t *testing.T) {
	for _, src := range []string{
		"a: 1\na: 2\n",
		"top:\n  a: 1\n  b: 2\n  a: 3\n",
		"disk:\n  - mount: /var\n    mount: /\n",
	} {
		if _, err := Parse(src); err == nil {
			t.Errorf("Parse(%q): expected duplicate-key error, got nil", src)
		} else if !strings.Contains(err.Error(), "duplicate key") {
			t.Errorf("Parse(%q): error %q does not mention duplicate key", src, err)
		}
	}
}

func TestSameKeyDifferentLevelsOK(t *testing.T) {
	v, err := Parse("a:\n  a: 1\nb:\n  a: 2\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := v.(*Map).Vals["b"].(*Map).Vals["a"]; got != "2" {
		t.Errorf("b.a = %q", got)
	}
}

func TestSinglePairListItemsStillParse(t *testing.T) {
	// The SLS argument convention must be unaffected by multi-key support.
	tree, err := Parse(`nginx_conf:
  file.managed:
    - name: /etc/nginx.conf
    - mode: "0644"
    - require:
      - pkg: nginx
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := tree.(*Map).Vals["nginx_conf"].(*Map).Vals["file.managed"].([]any)
	if len(args) != 3 {
		t.Fatalf("got %d args, want 3 separate single-pair entries", len(args))
	}
	for i, want := range []string{"name", "mode", "require"} {
		entry := args[i].(*Map)
		if len(entry.Keys) != 1 || entry.Keys[0] != want {
			t.Errorf("arg %d = %v, want the single key %q", i, entry.Keys, want)
		}
	}
}
