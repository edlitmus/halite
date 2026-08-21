"""Generate internal/template/testdata/jinja-corpus/halite.json.

Jinja's own tests cover Jinja. This corpus covers what they cannot: Salt's
added filters of SPEC section 10.2.4, the strict undefined of 10.2.6, the
limits of 10.2.8, and the refusals the subset owes an operator.

Every case here is expected to pass. There is no deviation table, because
a case that does not pass is a case this project got wrong.

Usage: python3 tools/make-halite-template-corpus.py \
           > internal/template/testdata/jinja-corpus/halite.json
"""
import json

C = []
def case(cid, template, expected=None, context=None, error=None, permissive=False):
    c = {"id": cid, "template": template}
    if context: c["context"] = context
    if error is not None: c["error"] = error
    else: c["expected"] = expected
    if permissive: c["permissive"] = True
    C.append(c)

# ---- SPEC 10.2.6, the undefined policy, which is the headline divergence.
case("undefined/strict-is-an-error", "{{ nope }}", error="nope")
case("undefined/strict-names-the-position", "line1\n{{ nope }}", error=":2:")
case("undefined/permissive-renders-empty", "[{{ nope }}]", "[]", permissive=True)
case("undefined/default-filter", "{{ nope | default('fallback') }}", "fallback")
case("undefined/default-filter-short", "{{ nope | d('fallback') }}", "fallback")
case("undefined/is-defined", "{% if nope is defined %}y{% else %}n{% endif %}", "n")
case("undefined/is-undefined", "{% if nope is undefined %}y{% else %}n{% endif %}", "y")
case("undefined/defined-name-wins", "{{ here | default('x') }}", "value", {"here": "value"})
case("undefined/attribute-of-missing", "{{ nope.attr }}", error="nope")

# ---- SPEC 10.2.4, Salt-added filters that trees actually use.
case("filter/yaml_encode-plain", "{{ 'a b' | yaml_encode }}", "a b")
# A value that would resolve to a boolean has to come back quoted, or a
# mode of 0644 or a version of "yes" changes type on the way into a file.
# The spelling of the quote is halite's; PyYAML picks the single quote and
# both mean the same string.
case("filter/yaml_encode-quotes-a-boolean-word", "{{ 'yes' | yaml_encode }}", '"yes"')
case("filter/yaml_encode-quotes-a-number-word", "{{ '0644' | yaml_encode }}", '"0644"')
case("filter/yaml_dquote", '{{ \'say "hi"\' | yaml_dquote }}', '"say \\"hi\\""')
case("filter/yaml_squote", "{{ 'plain' | yaml_squote }}", "'plain'")
case("filter/to_bool-yes", "{{ 'yes' | to_bool }}", "True")
case("filter/to_bool-no", "{{ 'no' | to_bool }}", "False")
case("filter/quote", "{{ 'x' | quote }}", "'x'")
case("filter/regex_escape", "{{ 'a.b' | regex_escape }}", "a\\.b")
case("filter/regex_replace", "{{ 'abc' | regex_replace('b', 'X') }}", "aXc")
case("filter/regex_match", "{{ 'web01' | regex_match('web(\\\\d+)') }}", "['01']")
case("filter/regex_search", "{{ 'x web01' | regex_search('web(\\\\d+)') }}", "['01']")
case("filter/md5", "{{ 'abc' | md5 }}", "900150983cd24fb0d6963f7d28e17f72")
case("filter/sha256", "{{ 'abc' | sha256 }}",
     "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
case("filter/base64_encode", "{{ 'abc' | base64_encode }}", "YWJj")
case("filter/base64_decode", "{{ 'YWJj' | base64_decode }}", "abc")
case("filter/hex_encode", "{{ 'abc' | hex_encode }}", "616263")
case("filter/unique", "{{ [1, 2, 2, 3] | unique | list }}", "[1, 2, 3]")
case("filter/union", "{{ [1, 2] | union([2, 3]) | list }}", "[1, 2, 3]")
case("filter/intersect", "{{ [1, 2, 3] | intersect([2, 3, 4]) | list }}", "[2, 3]")
case("filter/difference", "{{ [1, 2, 3] | difference([2]) | list }}", "[1, 3]")
case("filter/is_list-true", "{{ [1] | is_list }}", "True")
case("filter/is_list-false", "{{ 'x' | is_list }}", "False")
case("filter/flatten", "{{ [[1, 2], [3]] | flatten | list }}", "[1, 2, 3]")
case("filter/avg", "{{ [1, 2, 3] | avg }}", "2.0")
case("filter/path_join", "{{ ['a', 'b'] | path_join }}", "a/b")
case("filter/human_to_bytes", "{{ '1KB' | human_to_bytes }}", "1024")
case("filter/to_num", "{{ '42' | to_num }}", "42")

# ---- Standard Jinja filters, the ones an SLS tree leans on.
case("filter/default-on-empty-string", "{{ '' | default('d', true) }}", "d")
case("filter/join", "{{ ['a', 'b'] | join('-') }}", "a-b")
case("filter/length", "{{ [1, 2, 3] | length }}", "3")
case("filter/upper-lower", "{{ 'aB' | upper }}{{ 'aB' | lower }}", "ABab")
case("filter/replace", "{{ 'aaa' | replace('a', 'b', 2) }}", "bba")
case("filter/int-float", "{{ '3' | int }}{{ '3' | float }}", "33.0")
case("filter/sort", "{{ [3, 1, 2] | sort | list }}", "[1, 2, 3]")
case("filter/map-attribute", "{{ items | map(attribute='n') | list }}", "[1, 2]",
     {"items": [{"n": 1}, {"n": 2}]})
case("filter/selectattr", "{{ items | selectattr('ok') | list | length }}", "1",
     {"items": [{"ok": True}, {"ok": False}]})
case("filter/indent", "{{ 'a\\nb' | indent(2) }}", "a\n  b")
case("filter/trim", "{{ '  x  ' | trim }}", "x")
case("filter/tojson", '{{ {"a": 1} | tojson }}', '{"a": 1}')
case("filter/tojson-list", "{{ [1, 2] | tojson }}", "[1, 2]")
case("filter/first-last", "{{ [1, 2, 3] | first }}{{ [1, 2, 3] | last }}", "13")
case("filter/batch", "{{ [1, 2, 3] | batch(2) | list }}", "[[1, 2], [3]]")
case("filter/round", "{{ 2.5 | round }}", "3.0")
case("filter/abs", "{{ -5 | abs }}", "5")

# ---- SPEC 10.2.5, tests.
case("test/defined", "{{ 1 is defined }}", "True")
case("test/even-odd", "{{ 2 is even }}{{ 3 is odd }}", "TrueTrue")
case("test/divisibleby", "{{ 6 is divisibleby(3) }}", "True")
case("test/string-number", "{{ 'a' is string }}{{ 1 is number }}", "TrueTrue")
case("test/mapping-sequence", "{{ {} is mapping }}{{ [] is sequence }}", "TrueTrue")
case("test/in", "{{ 1 is in([1, 2]) }}", "True")
case("test/salt-list", "{{ [1] is list }}", "True")
case("test/salt-dict", "{{ {'a': 1} is dict }}", "True")
case("test/equalto", "{{ 1 is equalto(1) }}", "True")

# ---- SPEC 10.2.3, expression semantics stated as surprising.
case("expr/true-division", "{{ 3 / 2 }}", "1.5")
case("expr/floor-division", "{{ 3 // 2 }}", "1")
case("expr/power", "{{ 2 ** 10 }}", "1024")
case("expr/string-concat-tilde", "{{ 'a' ~ 1 }}", "a1")
case("expr/string-repeat", "{{ 'ab' * 2 }}", "abab")
case("expr/list-concat", "{{ [1] + [2] }}", "[1, 2]")
case("expr/conditional", "{{ 'y' if true else 'n' }}", "y")
case("expr/slice", "{{ [1, 2, 3, 4][1:3] }}", "[2, 3]")

# A tuple is a sequence in every way but its spelling. It prints with
# parentheses, and a one-element tuple carries the trailing comma that
# tells it from a parenthesised expression; everything else about it
# behaves as a list, because the nine-type model of SPEC 6.4 has no tuple
# and one must never reach pillar or a state argument.
case("tuple/renders-with-parentheses", "{{ () }}|{{ (1,) }}|{{ (1, 2) }}", "()|(1,)|(1, 2)")
case("tuple/iterates", "{% for a, b in [(1, 2), (3, 4)] %}{{ a }}{{ b }}{% endfor %}", "1234")
case("tuple/indexes-and-slices", "{{ (1, 2, 3)[0] }}|{{ (1, 2, 3)[1:] }}", "1|[2, 3]")
case("tuple/length-and-membership", "{{ (1, 2) | length }}|{{ 1 in (1, 2) }}", "2|True")
case("tuple/unpacks-in-set", "{% set a, b = (1, 2) %}{{ a }}{{ b }}", "12")
case("tuple/filters-as-a-list", "{{ (3, 1) | sort }}|{{ (1, 2) | join('-') }}", "[1, 3]|1-2")
case("tuple/concatenates", "{{ (1, 2) + (3,) }}", "[1, 2, 3]")
case("tuple/answers-the-sequence-tests",
     "{{ (1,2) is sequence }}|{{ (1,2) is iterable }}|{{ (1,2) is list }}", "True|True|True")
case("tuple/equals-a-list", "{{ (1, 2) == [1, 2] }}", "True")
case("tuple/serialises-as-a-list", "{{ (1, 2) | tojson }}", "[1, 2]")
case("expr/slice-step", "{{ [1, 2, 3, 4][::2] }}", "[1, 3]")
case("expr/negative-index", "{{ [1, 2, 3][-1] }}", "3")
case("expr/divide-by-zero-is-an-error", "{{ 1 / 0 }}", error="zero")
case("expr/mixed-add-is-an-error", "{{ 'a' + 1 }}", error="cannot add")
case("expr/mixed-add-suggests-tilde", "{{ 'a' + 1 }}", error="use ~ to concatenate")
case("expr/truthiness", "{% if '' %}a{% endif %}{% if [] %}b{% endif %}{% if 0 %}c{% endif %}d", "d")

# ---- SPEC 10.2.2, statements.
case("stmt/for-loop-vars",
     "{% for i in [1, 2] %}{{ loop.index }}{{ loop.first }}{{ loop.last }}{% endfor %}",
     "1TrueFalse2FalseTrue")
case("stmt/for-else", "{% for i in [] %}x{% else %}empty{% endfor %}", "empty")
case("stmt/for-tuple-unpack", "{% for a, b in pairs %}{{ a }}{{ b }}{% endfor %}", "1234",
     {"pairs": [[1, 2], [3, 4]]})
case("stmt/loop-cycle", "{% for i in [1, 2, 3] %}{{ loop.cycle('a', 'b') }}{% endfor %}", "aba")
case("stmt/loop-previtem-nextitem",
     "{% for i in [1, 2] %}{{ loop.previtem | default('-') }}{{ loop.nextitem | default('-') }}{% endfor %}",
     "-21-")
# `{% set %}` assigns in the current scope and nowhere else. A loop body
# is a fresh scope per iteration, so an assignment there neither survives
# to the next iteration nor escapes the loop. This is Jinja's rule and the
# whole reason namespace() exists; a tree relying on the assignment
# leaking would render differently under Salt.
case("scope/set-in-a-loop-does-not-persist",
     "{% for item in seq %}{{ x }}{% set x = item %}{{ x }}{% endfor %}", "010203",
     {"seq": [1, 2, 3], "x": 0})
case("scope/set-in-a-loop-does-not-escape",
     "{% set foo = 0 %}{% for i in [1, 2] %}{% set foo = 1 %}{% endfor %}{{ foo }}", "0")
case("scope/set-outside-a-loop-persists",
     "{% set x = 1 %}{{ x }}{% set x = 2 %}{{ x }}", "12")
case("scope/namespace-carries-out-of-a-loop",
     "{% set ns = namespace(n=0) %}{% for i in [1, 2, 3] %}{% set ns.n = ns.n + i %}{% endfor %}{{ ns.n }}",
     "6")
# `if` introduces no scope, so a set inside one is visible after it and
# overwrites an outer name. `for`, `with`, and a macro body each do
# introduce one. Getting the loop right must not take these with it.
case("scope/set-inside-if-is-visible-after",
     "{% if true %}{% set x = 1 %}{% endif %}{{ x }}", "1")
case("scope/set-inside-if-overwrites-an-outer-name",
     "{% set z = 0 %}{% if true %}{% set z = 1 %}{% endif %}{{ z }}", "1")
case("scope/set-inside-with-does-not-escape",
     "{% with %}{% set w = 1 %}{% endwith %}{{ w | default('gone') }}", "gone")
case("scope/set-inside-a-macro-does-not-escape",
     "{% macro m() %}{% set q = 1 %}{% endmacro %}{{ m() }}{{ q | default('gone') }}", "gone")

case("stmt/set-and-namespace",
     "{% set ns = namespace(n=0) %}{% for i in [1, 2] %}{% set ns.n = ns.n + i %}{% endfor %}{{ ns.n }}",
     "3")
case("stmt/block-set", "{% set x %}body{% endset %}{{ x }}", "body")
case("stmt/macro", "{% macro m(a, b=2) %}{{ a }}{{ b }}{% endmacro %}{{ m(1) }}", "12")
case("stmt/macro-varargs",
     "{% macro m() %}{{ varargs | join(',') }}{% endmacro %}{{ m(1, 2) }}", "1,2")
case("stmt/call-and-caller",
     "{% macro m() %}[{{ caller() }}]{% endmacro %}{% call m() %}body{% endcall %}", "[body]")
case("stmt/filter-block", "{% filter upper %}ab{% endfilter %}", "AB")
case("stmt/raw", "{% raw %}{{ not a variable }}{% endraw %}", "{{ not a variable }}")
case("stmt/with", "{% with x = 1 %}{{ x }}{% endwith %}", "1")
case("stmt/do", "{% set ns = namespace(n=0) %}{% do ns.__setattr__ %}{{ ns.n }}", "0")
case("stmt/comment", "a{# hidden #}b", "ab")
case("stmt/autoescape-is-a-no-op",
     "{% autoescape true %}{{ '<x>' }}{% endautoescape %}", "<x>")

# ---- SPEC 10.2.1, whitespace control.
case("ws/minus-left", "a\n  {{- 'b' }}", "ab")
case("ws/minus-right", "{{ 'a' -}}\n  b", "ab")
case("ws/statement-both", "a\n{%- if true -%}\nb\n{%- endif -%}\nc", "abc")
# `+` is the explicit opposite of `-`: it keeps whitespace that
# trim_blocks or lstrip_blocks would otherwise have eaten. A tree
# templating a file whose leading indentation matters needs it.
case("ws/plus-keeps-the-newline", "{% if true +%}\nx{% endif %}", "\nx")
case("ws/plus-in-a-filter-block", "{% filter upper|replace('A', 'b') %}aa{% endfilter %}", "bb")
case("stmt/filter-block-chain", "{% filter trim|upper %}  ab  {% endfilter %}", "AB")

# ---- SPEC 10.2.8, limits, each with a named error.
case("limits/output-size", "{% for i in range(100000000) %}xxxxxxxxxx{% endfor %}", error="limit")
case("limits/recursion", "{% macro m() %}{{ m() }}{% endmacro %}{{ m() }}", error="recursed deeper")

# ---- Constructs the subset refuses, which must say so rather than
# rendering something plausible.
case("refused/unknown-filter", "{{ 'x' | no_such_filter }}", error="no_such_filter")
case("refused/unknown-test", "{{ 'x' is no_such_test }}", error="no_such_test")
case("refused/unknown-tag", "{% no_such_tag %}", error="no_such_tag")
case("refused/trans-not-supported", "{% trans %}x{% endtrans %}", error="trans")

print(json.dumps(C, indent=1, ensure_ascii=False, sort_keys=True))
