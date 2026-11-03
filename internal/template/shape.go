package template

import (
	"fmt"

	"github.com/edlitmus/halite/internal/value"
)

// A value is walked recursively wherever it is turned into text or
// compared: printing it, `~`, `join`, `tojson`, `json`, `yaml`, `==`, `in`,
// `sort`, and every execution module a `salt[...]` call hands it to. Each
// of those recurses once per level of nesting, and a template can make a
// value as deep as it can loop: `{% set ns.x = [ns.x] %}` in a loop of N
// is a list nested N deep. On the old code printing one 14,500 levels
// deep overflowed an 8 MiB stack, and two million fill the default
// 1 GiB, which MaxIterations allowed and which took 1.3 s to build. A Go
// stack overflow is a fatal error, not a panic, so it ended the hub's
// pillar compile or the node's render (DIVERGENCE 5.312).
//
// Depth is not the only way such a walk runs away. A list holds its
// items by reference, so `{% set ns.x = [ns.x, ns.x] %}` doubles the
// number of items a walk visits at each pass while the memory held grows
// by one list; forty passes is a value whose printed form is a terabyte.
// And a mapping can hold itself: `d.update({'d': d})` is a value with no
// bottom at all.
//
// measureValue is the one check for all three. It walks the value with
// a stack of its own on the heap, so it cannot overflow the Go stack
// whatever it is given, and refuses a value that nests deeper than
// maxValueDepth (which is also how it stops on a cycle) or whose
// rendering could not fit in maxBytes. The engine calls it before every
// recursive walk of a value it did not just build itself, so the walks
// can stay the simple recursive functions they are.

// maxValueDepth is the deepest a value may nest and still be walked. It
// is value.MaxDepth, encoding/json's own limit, which value.DecodeJSON
// already enforces, so nothing that came into a render as a document is
// refused; YAML, the other way in, stops at 100. The deepest literal the
// parser accepts is maxExprDepth (1,000), so a value past that was built
// by a loop. Measured with go1.27.3 on darwin/arm64 and freebsd/amd64,
// with and without -race, and on linux/arm64 and linux/amd64 without
// (value_walk_test.go, TestDeepValuesDoNotOverflowTheStack, with the
// child's stack lowered by hand), for a value at the limit under 99
// nested `if`: the walks that spell the value as a mapping key, through
// fmt.Sprint, are the deepest, and need between 12 and 16 MiB on darwin
// with or without -race and on FreeBSD under -race, and between 8 and 16
// on linux/arm64 (on linux/amd64 they fit in 8), about 1.6 KiB a level
// at the most; every other walk fits in 8 MiB. The test's child has
// 32 MiB, twice the most measured, and the default stack is 1 GiB.
const maxValueDepth = value.MaxDepth

// valueShape is what measureValue learns of a value.
type valueShape struct {
	// bytes is a lower bound on the length of any rendering of the
	// value, whether str, repr, JSON or YAML: a string counts its length,
	// a mapping key its length, a container two for its brackets, and
	// any other scalar one. Every rendering writes at least that much,
	// so a value whose bound is past the output limit cannot render
	// within it, and refusing it refuses nothing that could have.
	bytes int64
	// pad is the number of containers enclosing each item, summed over
	// the items: an indented rendering writes at least indent times this
	// many spaces, which is the one part of its size the item contents
	// do not bound. tojson(indent=1000) of a value a thousand deep is a
	// gigabyte of spaces.
	pad int64
}

// shapeFrame is one container on measureValue's stack.
type shapeFrame struct {
	list    []any
	entries []value.Entry
	i       int
}

var (
	errValueTooDeep = fmt.Errorf("the value nests deeper than %d levels, or contains itself", maxValueDepth)
)

// measureValue walks v without recursing. The stack holds one frame per
// level, not per item, so a wide value costs no more memory to measure
// than a narrow one, and the walk visits each item at most once per path
// to it, stopping as soon as the lower bound passes maxBytes: a value
// that shares its items is refused after maxBytes visits at the most.
func measureValue(v any, maxBytes int64) (valueShape, error) {
	var sh valueShape
	var stack []shapeFrame
	add := func(x any) error {
		// Every rendering writes an item inside depth containers after
		// at least that many units of indentation.
		sh.pad += int64(len(stack))
		switch t := x.(type) {
		case string:
			sh.bytes += int64(len(t))
		case []any:
			sh.bytes += 2
			if len(t) > 0 {
				stack = append(stack, shapeFrame{list: t})
			}
		case Tuple:
			sh.bytes += 2
			if len(t) > 0 {
				stack = append(stack, shapeFrame{list: t})
			}
		case *value.Map:
			sh.bytes += 2
			if t != nil && t.Len() > 0 {
				stack = append(stack, shapeFrame{entries: t.Entries()})
			}
		default:
			sh.bytes++
		}
		if len(stack) > maxValueDepth {
			return errValueTooDeep
		}
		if sh.bytes > maxBytes {
			return fmt.Errorf("the value would render to more than %d bytes", maxBytes)
		}
		return nil
	}
	if err := add(v); err != nil {
		return sh, err
	}
	for len(stack) > 0 {
		top := &stack[len(stack)-1]
		var next any
		switch {
		case top.list != nil && top.i < len(top.list):
			next = top.list[top.i]
		case top.entries != nil && top.i < len(top.entries):
			e := top.entries[top.i]
			if s, ok := e.Key.(string); ok {
				sh.bytes += int64(len(s))
			} else {
				sh.bytes++
			}
			next = e.Val
		default:
			stack = stack[:len(stack)-1]
			continue
		}
		// add may grow the stack and move it, so top is not used after.
		top.i++
		if err := add(next); err != nil {
			return sh, err
		}
	}
	return sh, nil
}

// isContainer reports whether walking v could recurse. A scalar needs no
// measuring, which keeps the check off the path of nearly every value a
// template prints.
func isContainer(v any) bool {
	switch v.(type) {
	case []any, Tuple, *value.Map:
		return true
	}
	return false
}

// checkValue refuses a value no recursive walk should be given, with the
// position of the expression that asked for the walk.
func (r *renderer) checkValue(v any, what string, pos Pos) (valueShape, error) {
	if !isContainer(v) {
		return valueShape{}, nil
	}
	sh, err := measureValue(v, r.opts.MaxOutput)
	if err != nil {
		return sh, errorf(pos, "%s: %v", what, err)
	}
	return sh, nil
}

// checkPair is checkValue for the two operands of a comparison.
func (r *renderer) checkPair(a, b any, what string, pos Pos) error {
	if _, err := r.checkValue(a, what, pos); err != nil {
		return err
	}
	_, err := r.checkValue(b, what, pos)
	return err
}

// checkCompare is checkPair for a comparison, made only when the
// comparison will recurse: equality descends only into two lists or two
// mappings of the same length, ordering only into two lists, and every
// other pair is answered at the top. Measuring costs as much as walking,
// so measuring a large pillar mapping for `pillar == {}` would have made
// an answer that takes no time take as long as the mapping is big.
func (r *renderer) checkCompare(equality bool, a, b any, what string, pos Pos) error {
	a, b = untuple(a), untuple(b)
	switch x := a.(type) {
	case []any:
		if y, ok := b.([]any); ok && (!equality || len(x) == len(y)) {
			return r.checkPair(x, y, what, pos)
		}
	case *value.Map:
		if y, ok := b.(*value.Map); ok && equality && x.Len() == y.Len() {
			return r.checkPair(x, y, what, pos)
		}
	}
	return nil
}

// checkIn is checkPair for `needle in haystack`. A list is searched by
// comparing the needle with each item, so both are measured; a mapping
// is searched by spelling the needle as a key, which walks the needle
// alone, and `'k' in pillar` must not cost the size of pillar.
func (r *renderer) checkIn(haystack, needle any, pos Pos) error {
	if _, ok := untuple(haystack).([]any); ok {
		return r.checkPair(haystack, needle, "in", pos)
	}
	_, err := r.checkValue(needle, "in", pos)
	return err
}

// checkPairBytes is checkValueBytes for two operands.
func checkPairBytes(a, b any, what string, maxBytes int64) error {
	if err := checkValueBytes(a, what, maxBytes); err != nil {
		return err
	}
	return checkValueBytes(b, what, maxBytes)
}

// checkValueBytes is checkValue for the places that have a byte limit
// but no renderer: the bound methods, which are built before they are
// called and are handed the limit when they are.
func checkValueBytes(v any, what string, maxBytes int64) error {
	if !isContainer(v) {
		return nil
	}
	if _, err := measureValue(v, maxBytes); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}
