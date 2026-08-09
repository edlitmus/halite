package yamlite

import "testing"

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
