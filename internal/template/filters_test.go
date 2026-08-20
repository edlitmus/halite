package template

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

func TestCoreFilters(t *testing.T) {
	cases := []struct{ src, want string }{
		{`{{ -5 | abs }}`, "5"},
		{`{{ 'hello world' | capitalize }}`, "Hello world"},
		{`{{ 'hello world' | title }}`, "Hello World"},
		{`{{ 'HELLO' | lower }}`, "hello"},
		{`{{ '  pad  ' | trim }}`, "pad"},
		{`{{ 'xxhixx' | trim('x') }}`, "hi"},
		{`{{ '3' | int + 1 }}`, "4"},
		{`{{ 'nope' | int(7) }}`, "7"},
		{`{{ 'ff' | int(0, 16) }}`, "255"},
		{`{{ '2.5' | float }}`, "2.5"},
		{`{{ 3.14159 | round(2) }}`, "3.14"},
		{`{{ 3.1 | round(0, 'ceil') }}`, "4.0"},
		{`{{ [1,2,3] | length }}`, "3"},
		{`{{ 'abc' | length }}`, "3"},
		{`{{ undefined_thing | default('fb') }}`, "fb"},
		{`{{ '' | default('fb', true) }}`, "fb"},
		{`{{ '<b>' | escape }}`, "&lt;b&gt;"},
		{`{{ '%s-%d' | format('a', 2) }}`, "a-2"},
		{`{{ 'a\nb' | indent(2) }}`, "a\n  b"},
		{`{{ 1000000 | filesizeformat }}`, "1.0 MB"},
		{`{{ 1048576 | filesizeformat(true) }}`, "1.0 MiB"},
		{`{{ 'a b c d' | wordcount }}`, "4"},
		{`{{ 'hello' | replace('l', 'L') }}`, "heLLo"},
		{`{{ 'hello' | replace('l', 'L', 1) }}`, "heLlo"},
		{`{{ '<p>hi</p>' | striptags }}`, "hi"},
		{`{{ 42 | string ~ '!' }}`, "42!"},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestSequenceFilters(t *testing.T) {
	ctx := map[string]any{
		"nums":  []any{int64(3), int64(1), int64(2)},
		"words": []any{"b", "A", "c"},
		"rows": []any{
			value.MapOf("name", "web1", "role", "web", "cpus", int64(4)),
			value.MapOf("name", "db1", "role", "db", "cpus", int64(8)),
			value.MapOf("name", "web2", "role", "web", "cpus", int64(2)),
		},
	}
	cases := []struct{ src, want string }{
		{`{{ nums | first }}`, "3"},
		{`{{ nums | last }}`, "2"},
		{`{{ nums | sort }}`, "[1, 2, 3]"},
		{`{{ nums | sort(true) }}`, "[3, 2, 1]"},
		{`{{ words | sort }}`, "['A', 'b', 'c']"},
		{`{{ nums | sum }}`, "6"},
		{`{{ nums | min }}`, "1"},
		{`{{ nums | max }}`, "3"},
		{`{{ nums | join('-') }}`, "3-1-2"},
		{`{{ nums | reverse }}`, "[2, 1, 3]"},
		{`{{ [1,1,2] | unique }}`, "[1, 2]"},
		{`{{ [1,2,3,4] | batch(2) }}`, "[[1, 2], [3, 4]]"},
		{`{{ [1,2,3] | batch(2, 0) }}`, "[[1, 2], [3, 0]]"},
		{`{{ [1,2,3,4] | slice(2) }}`, "[[1, 2], [3, 4]]"},
		{`{{ rows | map(attribute='name') | join(',') }}`, "web1,db1,web2"},
		{`{{ rows | selectattr('role', 'eq', 'web') | map(attribute='name') | join(',') }}`, "web1,web2"},
		{`{{ rows | rejectattr('role', 'eq', 'web') | map(attribute='name') | join(',') }}`, "db1"},
		{`{{ [0,1,2] | select | list }}`, "[1, 2]"},
		{`{{ [0,1,2] | reject | list }}`, "[0]"},
		{`{{ rows | sum(attribute='cpus') }}`, "14"},
		{`{{ rows | max(attribute='cpus') | attr('name') }}`, "db1"},
		{`{{ ['a','b'] | map('upper') | join('') }}`, "AB"},
	}
	for _, c := range cases {
		if got := render(t, c.src, ctx); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestGroupbyAndDictsort(t *testing.T) {
	rows := []any{
		value.MapOf("name", "web1", "role", "web"),
		value.MapOf("name", "db1", "role", "db"),
		value.MapOf("name", "web2", "role", "web"),
	}
	src := `{% for role, members in rows | groupby('role') %}{{ role }}={{ members | map(attribute='name') | join(',') }} {% endfor %}`
	if got := render(t, src, map[string]any{"rows": rows}); got != "db=db1 web=web1,web2 " {
		t.Errorf("groupby: %q", got)
	}

	m := value.MapOf("z", int64(1), "a", int64(2))
	src = `{% for k, v in m | dictsort %}{{ k }}{{ v }}{% endfor %}`
	if got := render(t, src, map[string]any{"m": m}); got != "a2z1" {
		t.Errorf("dictsort: %q", got)
	}
}

func TestSaltSerializationFilters(t *testing.T) {
	ctx := map[string]any{
		"m":    value.MapOf("b", int64(2), "a", int64(1)),
		"list": []any{"x", "y"},
	}
	cases := []struct{ src, want string }{
		{`{{ m | tojson }}`, `{"b":2,"a":1}`},
		{`{{ 'yes' | yaml_encode }}`, `"yes"`},
		{`{{ 'plain' | yaml_encode }}`, `plain`},
		{`{{ list | yaml_encode }}`, `[x, y]`},
		{`{{ 'a"b' | yaml_dquote }}`, `"a\"b"`},
		{`{{ "it's" | yaml_squote }}`, `'it''s'`},
		{`{{ '{"a": 1}' | json_decode_dict | attr('a') }}`, "1"},
		{`{{ '[1, 2]' | json_decode_list | last }}`, "2"},
		{`{{ 'yes' | to_bool }}`, "True"},
		{`{{ '0' | to_bool }}`, "False"},
		{`{{ '42' | to_num + 1 }}`, "43"},
		{`{{ "a'b" | quote }}`, `'a'\''b'`},
		{`{{ 'a: 1' | load_yaml | attr('a') }}`, "1"},
	}
	for _, c := range cases {
		if got := render(t, c.src, ctx); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestJSONKeepsDeclarationOrder(t *testing.T) {
	m := value.MapOf("zebra", int64(1), "apple", int64(2), "mango", int64(3))
	got := render(t, `{{ m | tojson }}`, map[string]any{"m": m})
	if got != `{"zebra":1,"apple":2,"mango":3}` {
		t.Errorf("tojson lost declaration order: %s", got)
	}
}

func TestHashFilters(t *testing.T) {
	cases := []struct{ src, want string }{
		{`{{ 'abc' | md5 }}`, "900150983cd24fb0d6963f7d28e17f72"},
		{`{{ 'abc' | sha1 }}`, "a9993e364706816aba3e25717850c26c9cd0d89d"},
		{`{{ 'abc' | sha256 }}`, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{`{{ 'abc' | base64_encode }}`, "YWJj"},
		{`{{ 'YWJj' | base64_decode }}`, "abc"},
		{`{{ 'abc' | hex_encode }}`, "616263"},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestUUIDFromNameIsStable(t *testing.T) {
	a := render(t, `{{ 'web1.prod' | uuid }}`, nil)
	b := render(t, `{{ 'web1.prod' | uuid }}`, nil)
	if a != b {
		t.Errorf("a named uuid must be stable: %q then %q", a, b)
	}
	if len(a) != 36 {
		t.Errorf("uuid = %q", a)
	}
}

func TestRegexFilters(t *testing.T) {
	cases := []struct{ src, want string }{
		{`{{ 'a.b' | regex_escape }}`, `a\.b`},
		{`{{ 'web1.prod' | regex_search('web([0-9]+)') | first }}`, "1"},
		{`{{ 'web1' | regex_match('web[0-9]+') }}`, "[]"},
		{`{{ 'nope' | regex_match('web[0-9]+') }}`, "None"},
		{`{{ 'a-b-c' | regex_replace('-', '_') }}`, "a_b_c"},
		{`{{ 'John Smith' | regex_replace('(\\w+) (\\w+)', '\\2 \\1') }}`, "Smith John"},
		{`{{ 'a1b2' | regex_split('[0-9]') | join(',') }}`, "a,b,"},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestRegexRefusesUnsupportedConstructsByName(t *testing.T) {
	// A silent non-match here is a state that reports success and changes
	// nothing, so this must be a hard error naming the construct.
	err := renderErr(t, `{{ 'abc' | regex_search('a(?=b)') }}`, nil)
	for _, want := range []string{"(?=", "lookahead", "SPEC section 10.4"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
	err = renderErr(t, `{{ 'aa' | regex_search('(a)\\1') }}`, nil)
	mustContain(t, err.Error(), "backreference")
}

func TestCollectionAndSetFilters(t *testing.T) {
	cases := []struct{ src, want string }{
		{`{{ [1,2] | union([2,3]) }}`, "[1, 2, 3]"},
		{`{{ [1,2] | intersect([2,3]) }}`, "[2]"},
		{`{{ [1,2] | difference([2,3]) }}`, "[1]"},
		{`{{ [1,2] | symmetric_difference([2,3]) }}`, "[1, 3]"},
		{`{{ [[1,[2]],[3]] | flatten }}`, "[1, 2, 3]"},
		{`{{ [[1,[2]],[3]] | flatten(1) }}`, "[1, [2], 3]"},
		{`{{ [1,2] | zip([3,4]) }}`, "[[1, 3], [2, 4]]"},
		{`{{ [1,2,3] | avg }}`, "2.0"},
		{`{{ [1,2,3] | combinations(2) | length }}`, "3"},
		{`{{ [1,2,3] | permutations(2) | length }}`, "6"},
		{`{{ [1,2] | is_list }}`, "True"},
		{`{{ 'x' | is_list }}`, "False"},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestTraverseAndDictKeyFilters(t *testing.T) {
	m := value.MapOf("a", value.MapOf("b", "deep"))
	ctx := map[string]any{"m": m}
	cases := []struct{ src, want string }{
		{`{{ m | traverse('a:b') }}`, "deep"},
		{`{{ m | traverse('a:zz', 'fb') }}`, "fb"},
		{`{{ m | set_dict_key_value('a:c', 'new') | traverse('a:c') }}`, "new"},
		{`{{ m | append_dict_key_value('a:list', 1) | traverse('a:list') }}`, "[1]"},
		{`{{ m | extend_dict_key_value('a:list', [1,2]) | traverse('a:list') }}`, "[1, 2]"},
	}
	for _, c := range cases {
		if got := render(t, c.src, ctx); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
	// The filters must not mutate the pillar they were handed.
	inner, _ := m.Get("a")
	if _, ok := inner.(*value.Map).Get("c"); ok {
		t.Error("set_dict_key_value mutated its input")
	}
}

func TestNetworkFilters(t *testing.T) {
	cases := []struct{ src, want string }{
		{`{{ '10.0.0.1' | is_ipv4 }}`, "True"},
		{`{{ '::1' | is_ipv6 }}`, "True"},
		{`{{ '10.0.0.1' | is_ipv6 }}`, "False"},
		{`{{ 'nope' | is_ip }}`, "False"},
		{`{{ ['10.0.0.1', '::1', 'x'] | ipv4 }}`, "['10.0.0.1']"},
		{`{{ '10.0.0.0/24' | network_size }}`, "256"},
		{`{{ '10.0.0.0/30' | network_hosts | length }}`, "2"},
		{`{{ '10.0.0.0/24' | cidr_subnets(26) | length }}`, "4"},
		{`{{ '10.0.0.0/24' | cidr_subnets(26) | first }}`, "10.0.0.0/26"},
		{`{{ ['10.0.0.0/24', '10.0.0.0/25'] | cidr_merge }}`, "['10.0.0.0/24']"},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestMiscSaltFilters(t *testing.T) {
	cases := []struct{ src, want string }{
		{`{{ '1G' | human_to_bytes }}`, "1073741824"},
		{`{{ 1073741824 | sizeof_fmt }}`, "1.0 GiB"},
		{`{{ '/srv' | path_join('salt', 'top.sls') }}`, "/srv/salt/top.sls"},
		{`{{ 1755600000 | strftime('%Y-%m-%d') }}`, "2025-08-19"},
		{`{{ 'a,b' | method_call('split', ',') | last }}`, "b"},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestStringMethods(t *testing.T) {
	cases := []struct{ src, want string }{
		{`{{ 'a.b.c'.split('.') | length }}`, "3"},
		{`{{ 'a.b.c'.split('.', 1) | last }}`, "b.c"},
		{`{{ ' x '.strip() }}`, "x"},
		{`{{ 'abc'.startswith('ab') }}`, "True"},
		{`{{ 'abc'.endswith(['x','bc']) }}`, "True"},
		{`{{ '-'.join(['a','b']) }}`, "a-b"},
		{`{{ 'a{0}c'.format('b') }}`, "abc"},
		{`{{ '{x}!'.format(x='hi') }}`, "hi!"},
		{`{{ 'abc'.upper() }}`, "ABC"},
		{`{{ 'a1'.isdigit() }}`, "False"},
		{`{{ '7'.zfill(3) }}`, "007"},
		{`{{ 'a\nb'.splitlines() | length }}`, "2"},
	}
	for _, c := range cases {
		if got := render(t, c.src, nil); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestMapMethods(t *testing.T) {
	m := value.MapOf("a", int64(1), "b", int64(2))
	ctx := map[string]any{"m": m}
	cases := []struct{ src, want string }{
		{`{{ m.keys() | join(',') }}`, "a,b"},
		{`{{ m.values() | join(',') }}`, "1,2"},
		{`{{ m.items() | length }}`, "2"},
		{`{{ m.get('a') }}`, "1"},
		{`{{ m.get('zz', 'fb') }}`, "fb"},
	}
	for _, c := range cases {
		if got := render(t, c.src, ctx); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

func TestTests(t *testing.T) {
	ctx := map[string]any{
		"m":   value.MapOf("a", int64(1)),
		"lst": []any{int64(1)},
	}
	cases := []struct{ src, want string }{
		{`{{ 1 is defined }}`, "True"},
		{"{{ nope is undefined }}", "True"},
		{`{{ none is none }}`, "True"},
		{`{{ 'x' is string }}`, "True"},
		{`{{ 1 is number }}`, "True"},
		{`{{ 1.5 is float }}`, "True"},
		{`{{ 1 is integer }}`, "True"},
		{`{{ m is mapping }}`, "True"},
		{`{{ lst is sequence }}`, "True"},
		{`{{ lst is iterable }}`, "True"},
		{`{{ 4 is even }}`, "True"},
		{`{{ 3 is odd }}`, "True"},
		{`{{ 9 is divisibleby 3 }}`, "True"},
		{`{{ 1 is eq 1 }}`, "True"},
		{`{{ 1 is ne 2 }}`, "True"},
		{`{{ 1 is lt 2 }}`, "True"},
		{`{{ 2 is ge 2 }}`, "True"},
		{`{{ 'abc' is lower }}`, "True"},
		{`{{ 'ABC' is upper }}`, "True"},
		{`{{ 1 is in [1,2] }}`, "True"},
		{`{{ 1 is not eq 2 }}`, "True"},
		{`{{ 'web1' is match('web[0-9]') }}`, "True"},
		{`{{ true is boolean }}`, "True"},
	}
	for _, c := range cases {
		if got := render(t, c.src, ctx); got != c.want {
			t.Errorf("%s -> %q, want %q", c.src, got, c.want)
		}
	}
}

// TestFilterAndTestInventoryMatchesSpec holds the engine to the lists SPEC
// sections 10.2.4 and 10.2.5 name, so that a filter cannot quietly go
// missing and turn into an unknown-filter error in a production tree.
func TestFilterAndTestInventoryMatchesSpec(t *testing.T) {
	env := NewEnvironment(nil, DefaultOptions())

	requiredFilters := []string{
		// Standard Jinja, SPEC section 10.2.4 first paragraph.
		"abs", "attr", "batch", "capitalize", "center", "default", "d",
		"dictsort", "escape", "filesizeformat", "first", "float",
		"forceescape", "format", "groupby", "indent", "int", "join", "last",
		"length", "list", "lower", "map", "max", "min", "pprint", "random",
		"reject", "rejectattr", "replace", "reverse", "round", "safe",
		"select", "selectattr", "slice", "sort", "string", "striptags",
		"sum", "title", "tojson", "trim", "truncate", "unique", "upper",
		"urlencode", "wordcount", "wordwrap", "xmlattr",
		// Salt-added, second paragraph.
		"yaml_encode", "yaml_dquote", "yaml_squote", "json_encode_dict",
		"json_decode_dict", "json_decode_list", "to_bool", "quote",
		"regex_escape", "regex_search", "regex_match", "regex_replace",
		"uuid", "union", "intersect", "difference", "symmetric_difference",
		"is_list", "is_iter", "md5", "sha1", "sha256", "sha512", "hmac",
		"base64_encode", "base64_decode", "hex_encode", "random_hash",
		"rand_str", "strftime", "date_format", "to_num", "avg", "stdev",
		"zip", "zip_longest", "flatten", "combinations", "permutations",
		"human_to_bytes", "sizeof_fmt", "gen_mac", "mac_str_to_bytes",
		"ipv4", "ipv6", "ipaddr", "ip_host", "network_hosts", "network_size",
		"cidr_merge", "cidr_subnets", "is_ip", "is_ipv4", "is_ipv6",
		"dns_check", "path_join", "which", "dict_to_sls_yaml_params",
		"method_call", "set_dict_key_value", "update_dict_key_value",
		"append_dict_key_value", "extend_dict_key_value", "traverse",
	}
	have := map[string]bool{}
	for _, n := range env.FilterNames() {
		have[n] = true
	}
	var missing []string
	for _, n := range requiredFilters {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		t.Errorf("filters named by SPEC section 10.2.4 are missing: %s", strings.Join(missing, ", "))
	}

	requiredTests := []string{
		"callable", "defined", "divisibleby", "eq", "escaped", "even", "ge",
		"gt", "in", "iterable", "le", "lower", "lt", "mapping", "ne", "none",
		"number", "odd", "sameas", "sequence", "string", "undefined", "upper",
		"list", "dict", "match", "equalto",
	}
	haveTests := map[string]bool{}
	for _, n := range env.TestNames() {
		haveTests[n] = true
	}
	missing = nil
	for _, n := range requiredTests {
		if !haveTests[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		t.Errorf("tests named by SPEC section 10.2.5 are missing: %s", strings.Join(missing, ", "))
	}
}
