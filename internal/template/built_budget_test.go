package template

import (
	"errors"
	"strings"
	"testing"
)

// These hold budget.build: the bytes of the strings a render makes are
// counted across the render, not only one string at a time. Each case
// keeps three 400-byte strings under the small environment's 1,000-byte
// limit, each one allowed on its own, so the old code held all three.

// builtCtx supplies the operands, so that only what the case itself
// makes is charged.
func builtCtx() map[string]any {
	s := strings.Repeat("a", 400)
	return map[string]any{
		"s":  s,
		"ss": []any{s, s, s},
	}
}

func renderBuilt(src string) (string, error) {
	res, err := smallEnv().RenderString(src, "t.sls", builtCtx())
	return res.Output, err
}

func TestBuiltStringsAreChargedToTheRender(t *testing.T) {
	refused := map[string]string{
		// The case the review measured: 500 of these under a 1,000-byte
		// limit held 450,000 bytes.
		"append repetition": `{% set l = [] %}{% for i in range(3) %}{% do l.append('a' * 400) %}{% endfor %}{{ l | length }}`,
		"list concat":       `{% set ns = namespace(l=[]) %}{% for i in range(3) %}{% set ns.l = ns.l + ['a' * 400] %}{% endfor %}{{ ns.l | length }}`,
		"dict update":       `{% set m = {} %}{% for i in range(3) %}{% do m.update({i: 'a' * 400}) %}{% endfor %}{{ m | length }}`,
		"filter":            `{% set l = [] %}{% for i in range(3) %}{% do l.append(s | upper) %}{% endfor %}{{ l | length }}`,
		"method":            `{% set l = [] %}{% for i in range(3) %}{% do l.append(s.upper()) %}{% endfor %}{{ l | length }}`,
		"map":               `{{ ss | map('upper') | list | length }}`,
		"concat":            `{% set l = [] %}{% for i in range(3) %}{% do l.append(s ~ 'x') %}{% endfor %}{{ l | length }}`,
		"format method":     `{% set l = [] %}{% for i in range(3) %}{% do l.append('{}!'.format(s)) %}{% endfor %}{{ l | length }}`,
		"format filter":     `{% set l = [] %}{% for i in range(3) %}{% do l.append('%s!' | format(s)) %}{% endfor %}{{ l | length }}`,
		"rendered list":     `{% set l = [] %}{% for i in range(3) %}{% do l.append(ss[:1] | string) %}{% endfor %}{{ l | length }}`,
	}
	for name, src := range refused {
		t.Run(name, func(t *testing.T) {
			out, err := renderBuilt(src)
			var te *Error
			if !errors.As(err, &te) || te.Pos.Line == 0 || !strings.Contains(te.Msg, "strings built in this render") {
				t.Fatalf("want a positioned error from the render's total, got %q, %v", out, err)
			}
		})
	}

	// None of these makes a string: each returns one it was given or
	// found. Ten of them would be 4,000 bytes if they were charged.
	free := map[string]string{
		"first":       `{% for i in range(10) %}{% set x = ss | first %}{% endfor %}ok`,
		"default":     `{% for i in range(10) %}{% set x = nothing | default(s) %}{% endfor %}ok`,
		"string":      `{% for i in range(10) %}{% set x = s | string %}{% endfor %}ok`,
		"trim":        `{% for i in range(10) %}{% set x = s | trim %}{% endfor %}ok`,
		"strip":       `{% for i in range(10) %}{% set x = s.strip() %}{% endfor %}ok`,
		"concat none": `{% for i in range(10) %}{% set x = s ~ '' %}{% endfor %}ok`,
		"get":         `{% set m = {'k': s} %}{% for i in range(10) %}{% set x = m.get('k') %}{% endfor %}ok`,
		"repeat once": `{% for i in range(10) %}{% set x = s * 1 %}{% endfor %}ok`,
		// What is written is output, which has its own limit, and is
		// not counted again as built.
		"print": `{{ s }}{{ ss[:1] }}`,
	}
	for name, src := range free {
		t.Run(name, func(t *testing.T) {
			out, err := renderBuilt(src)
			if err != nil || !strings.HasSuffix(out, "ok") && name != "print" {
				t.Fatalf("got %q, %v", out, err)
			}
		})
	}

	// Under the limit, building is unchanged: two 400-byte strings.
	out, err := renderBuilt(`{% set l = [] %}{% for i in range(2) %}{% do l.append('a' * 400) %}{% endfor %}{{ l | length }}`)
	if err != nil || out != "2" {
		t.Fatalf("got %q, %v", out, err)
	}
}
