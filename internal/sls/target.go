package sls

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// The target language, shared by top files and the control plane:
//
//	*                          every host
//	web*                       glob on the host id
//	os_family:FreeBSD          glob on a grain's value
//	G@os_family:FreeBSD        the same, spelled Salt's way
//	L@web1,web2                one of these ids
//	E@^web[0-9]+$              regular expression on the id
//	P@osrelease:^14\.          regular expression on a grain's value
//	web* and not L@web9        boolean combinations, with parentheses
//
// Boolean expressions are what makes the two matchers enough: "the web
// hosts, except the one being rebuilt" has no single-pattern spelling.
// Salt's remaining matchers — I@ (pillar), S@ (subnet), N@ (nodegroup),
// R@ (range) — are not implemented, and a pattern using one is an error
// rather than a silent non-match.

// TargetMatch reports whether a target pattern selects a host with the
// given grains. A pattern that does not parse matches nothing; callers
// that can report the problem should use MatchTarget instead.
func TargetMatch(pat string, grains map[string]any) bool {
	ok, err := MatchTarget(pat, grains)
	return err == nil && ok
}

// MatchTarget is TargetMatch with the parse error surfaced.
func MatchTarget(pat string, grains map[string]any) (bool, error) {
	tokens := tokenizeTarget(pat)
	if len(tokens) == 0 {
		return false, fmt.Errorf("empty target")
	}
	p := &targetParser{tokens: tokens, grains: grains}
	result, err := p.expression()
	if err != nil {
		return false, err
	}
	if p.pos != len(p.tokens) {
		return false, fmt.Errorf("target %q: unexpected %q", pat, p.tokens[p.pos])
	}
	return result, nil
}

// tokenizeTarget splits a target into words, with parentheses as tokens of
// their own so `not(web*)` and `not (web*)` read the same.
func tokenizeTarget(pat string) []string {
	var tokens []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}
	for i := 0; i < len(pat); i++ {
		switch c := pat[i]; c {
		case ' ', '\t':
			flush()
		case '(', ')':
			flush()
			tokens = append(tokens, string(c))
		default:
			current.WriteByte(c)
		}
	}
	flush()
	return tokens
}

// targetParser is a recursive-descent parser over the token list:
// expression := term ("or" term)*, term := factor ("and" factor)*,
// factor := "not" factor | "(" expression ")" | pattern.
type targetParser struct {
	tokens []string
	pos    int
	grains map[string]any
}

func (p *targetParser) peek() string {
	if p.pos < len(p.tokens) {
		return p.tokens[p.pos]
	}
	return ""
}

func (p *targetParser) expression() (bool, error) {
	result, err := p.term()
	if err != nil {
		return false, err
	}
	for strings.EqualFold(p.peek(), "or") {
		p.pos++
		right, err := p.term()
		if err != nil {
			return false, err
		}
		result = result || right
	}
	return result, nil
}

func (p *targetParser) term() (bool, error) {
	result, err := p.factor()
	if err != nil {
		return false, err
	}
	// Two patterns side by side are an error rather than an implied `and`:
	// a stray word must not silently widen or narrow a target.
	for strings.EqualFold(p.peek(), "and") {
		p.pos++
		right, err := p.factor()
		if err != nil {
			return false, err
		}
		result = result && right
	}
	return result, nil
}

func (p *targetParser) factor() (bool, error) {
	switch tok := p.peek(); {
	case tok == "":
		return false, fmt.Errorf("target ends after %q", p.previous())
	case strings.EqualFold(tok, "not"):
		p.pos++
		inner, err := p.factor()
		if err != nil {
			return false, err
		}
		return !inner, nil
	case tok == "(":
		p.pos++
		inner, err := p.expression()
		if err != nil {
			return false, err
		}
		if p.peek() != ")" {
			return false, fmt.Errorf("unbalanced parentheses in target")
		}
		p.pos++
		return inner, nil
	case tok == ")":
		return false, fmt.Errorf("unbalanced parentheses in target")
	}
	tok := p.tokens[p.pos]
	p.pos++
	return matchPattern(tok, p.grains)
}

func (p *targetParser) previous() string {
	if p.pos > 0 {
		return p.tokens[p.pos-1]
	}
	return ""
}

// matchPattern evaluates one leaf pattern.
func matchPattern(pat string, grains map[string]any) (bool, error) {
	if pat == "*" {
		return true, nil
	}
	if len(pat) > 2 && pat[1] == '@' {
		return matchPrefixed(pat[0], pat[2:], grains)
	}
	if key, valPat, ok := splitGrainPattern(pat); ok {
		return matchGrain(key, valPat, grains, globMatch)
	}
	return globMatch(pat, hostID(grains))
}

// matchPrefixed handles Salt's letter@pattern matchers.
func matchPrefixed(kind byte, rest string, grains map[string]any) (bool, error) {
	switch kind {
	case 'G': // grain glob
		key, valPat, ok := splitGrainPattern(rest)
		if !ok {
			return false, fmt.Errorf("G@ target %q needs a grain:pattern", rest)
		}
		return matchGrain(key, valPat, grains, globMatch)
	case 'P': // grain regular expression
		key, valPat, ok := splitGrainPattern(rest)
		if !ok {
			return false, fmt.Errorf("P@ target %q needs a grain:pattern", rest)
		}
		return matchGrain(key, valPat, grains, regexMatch)
	case 'E': // regular expression on the host id
		return regexMatch(rest, hostID(grains))
	case 'L': // explicit list of ids
		id := hostID(grains)
		for _, name := range strings.Split(rest, ",") {
			if strings.TrimSpace(name) == id {
				return true, nil
			}
		}
		return false, nil
	}
	return false, fmt.Errorf("target matcher %c@ is not implemented", kind)
}

// splitGrainPattern splits "key:pattern" at the first colon.
func splitGrainPattern(pat string) (key, valPat string, ok bool) {
	i := strings.IndexByte(pat, ':')
	if i <= 0 || i == len(pat)-1 {
		return "", "", false
	}
	return pat[:i], pat[i+1:], true
}

// matchGrain applies a matcher to a grain's value. A host without the
// grain matches nothing, not even `key:*`; otherwise every glob would
// select the whole fleet.
func matchGrain(key, valPat string, grains map[string]any,
	match func(pattern, value string) (bool, error)) (bool, error) {
	value, present := grains[key]
	if !present || value == nil {
		return false, nil
	}
	// A list grain matches when any of its entries does, so `roles:web`
	// selects a host that has several.
	if list, ok := value.([]any); ok {
		for _, item := range list {
			hit, err := match(valPat, fmt.Sprintf("%v", item))
			if err != nil || hit {
				return hit, err
			}
		}
		return false, nil
	}
	return match(valPat, fmt.Sprintf("%v", value))
}

func globMatch(pattern, value string) (bool, error) {
	ok, err := path.Match(pattern, value)
	if err != nil {
		return false, fmt.Errorf("bad glob %q: %w", pattern, err)
	}
	return ok, nil
}

func regexMatch(pattern, value string) (bool, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, fmt.Errorf("bad regular expression %q: %w", pattern, err)
	}
	return re.MatchString(value), nil
}

// hostID is the name a bare pattern globs: the enrolled identity under a
// control plane, the hostname without one.
func hostID(grains map[string]any) string {
	if name, _ := grains["id"].(string); name != "" {
		return name
	}
	name, _ := grains["host"].(string)
	return name
}

// ValidTarget reports whether a target parses, without a host to match it
// against. The control plane checks a job's target with it so a typo is
// refused at dispatch rather than reported as an empty fleet.
func ValidTarget(pat string) error {
	_, err := MatchTarget(pat, nil)
	return err
}
