package template

import (
	"math"
	"strings"

	"github.com/edlitmus/halite/internal/value"
)

// asSeq coerces a filter operand into a sequence, treating a mapping as
// its keys the way iteration does.
func asSeq(fc *FilterContext, v any) ([]any, error) {
	v = untuple(v)

	switch t := v.(type) {
	case []any:
		return t, nil
	case nil:
		return nil, nil
	case Undefined:
		if err := fc.r.undefinedError(t, fc.Pos); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return fc.r.iterate(v, fc.Pos)
}

func addSequenceFilters(f map[string]FilterFunc) {
	f["first"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return Undefined{Name: "first", Pos: fc.Pos, Hint: "the sequence is empty"}, nil
		}
		return items[0], nil
	}

	f["last"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return Undefined{Name: "last", Pos: fc.Pos, Hint: "the sequence is empty"}, nil
		}
		return items[len(items)-1], nil
	}

	f["list"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(items))
		copy(out, items)
		return out, nil
	}

	f["join"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		sep := ""
		if s, ok := argString(args, kwargs, 0, "d"); ok {
			sep = s
		} else if s, ok := kwargs["separator"].(string); ok {
			sep = s
		}
		attr, hasAttr := argString(args, kwargs, 1, "attribute")
		parts := make([]string, len(items))
		for i, item := range items {
			if hasAttr {
				a, err := fc.r.getAttr(item, attr, fc.Pos)
				if err != nil {
					return nil, err
				}
				item = a
			}
			parts[i] = renderValue(item)
		}
		return strings.Join(parts, sep), nil
	}

	f["reverse"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		if s, ok := v.(string); ok {
			rs := []rune(s)
			for i, j := 0, len(rs)-1; i < j; i, j = i+1, j-1 {
				rs[i], rs[j] = rs[j], rs[i]
			}
			return string(rs), nil
		}
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(items))
		for i, item := range items {
			out[len(items)-1-i] = item
		}
		return out, nil
	}

	f["sort"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(items))
		copy(out, items)
		reverse := false
		if b, ok := arg(args, kwargs, 0, "reverse"); ok {
			reverse = truthy(b)
		}
		caseSensitive := false
		if b, ok := arg(args, kwargs, 1, "case_sensitive"); ok {
			caseSensitive = truthy(b)
		}
		key := identity
		if attr, ok := argString(args, kwargs, 2, "attribute"); ok {
			key = attrKey(fc, attr)
		}
		sortAny(out, reverse, caseSensitive, key)
		return out, nil
	}

	f["unique"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		key := identity
		if attr, ok := argString(args, kwargs, 1, "attribute"); ok {
			key = attrKey(fc, attr)
		}
		out := []any{}
		for _, item := range items {
			k := key(item)
			dup := false
			for _, seen := range out {
				if equalValues(key(seen), k) {
					dup = true
					break
				}
			}
			if !dup {
				out = append(out, item)
			}
		}
		return out, nil
	}

	f["min"] = extremum(false)
	f["max"] = extremum(true)

	f["sum"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		key := identity
		if attr, ok := argString(args, kwargs, 0, "attribute"); ok {
			key = attrKey(fc, attr)
		}
		var start any = int64(0)
		if s, ok := arg(args, kwargs, 1, "start"); ok {
			start = s
		}
		total := start
		for _, item := range items {
			sum, err := arith("+", total, key(item), fc.Pos)
			if err != nil {
				return nil, err
			}
			total = sum
		}
		return total, nil
	}

	f["batch"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		n, ok := arg(args, kwargs, 0, "linecount")
		size, _ := asInt(n)
		if !ok || size <= 0 {
			return nil, fc.Errorf("batch needs a positive item count")
		}
		fill, hasFill := arg(args, kwargs, 1, "fill_with")
		out := []any{}
		for i := 0; i < len(items); i += int(size) {
			end := i + int(size)
			var chunk []any
			if end > len(items) {
				chunk = append([]any{}, items[i:]...)
				if hasFill {
					for len(chunk) < int(size) {
						chunk = append(chunk, fill)
					}
				}
			} else {
				chunk = append([]any{}, items[i:end]...)
			}
			out = append(out, chunk)
		}
		return out, nil
	}

	f["slice"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		n, ok := arg(args, kwargs, 0, "slices")
		count, _ := asInt(n)
		if !ok || count <= 0 {
			return nil, fc.Errorf("slice needs a positive slice count")
		}
		fill, hasFill := arg(args, kwargs, 1, "fill_with")
		per := len(items) / int(count)
		extra := len(items) % int(count)
		out := []any{}
		idx := 0
		for i := 0; i < int(count); i++ {
			size := per
			if i < extra {
				size++
			}
			chunk := append([]any{}, items[idx:idx+size]...)
			idx += size
			if hasFill && extra > 0 && i >= extra {
				chunk = append(chunk, fill)
			}
			out = append(out, chunk)
		}
		return out, nil
	}

	f["map"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		// `attribute` is a keyword argument only. A positional string is
		// the name of a filter to apply to each item, and confusing the
		// two turns map('upper') into an attribute lookup.
		if attr, ok := kwargs["attribute"].(string); ok {
			out := make([]any, len(items))
			for i, item := range items {
				a, err := fc.r.getAttr(item, attr, fc.Pos)
				if err != nil {
					return nil, err
				}
				out[i] = a
			}
			return out, nil
		}
		// map('filtername', extra args...)
		name, ok := argString(args, kwargs, 0, "filter")
		if !ok {
			return nil, fc.Errorf("map needs an attribute= or a filter name")
		}
		fn, ok := fc.r.env.filters[name]
		if !ok {
			return nil, fc.Errorf("unknown filter %q in map", name)
		}
		rest := []any{}
		if len(args) > 1 {
			rest = args[1:]
		}
		out := make([]any, len(items))
		for i, item := range items {
			mv, err := fn(fc, item, rest, kwargs)
			if err != nil {
				return nil, err
			}
			out[i] = mv
		}
		return out, nil
	}

	f["select"] = selectFilter(false, false)
	f["reject"] = selectFilter(true, false)
	f["selectattr"] = selectFilter(false, true)
	f["rejectattr"] = selectFilter(true, true)

	f["dictsort"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		m, ok := v.(*value.Map)
		if !ok {
			return nil, fc.Errorf("dictsort expects a mapping, found %s", typeName(v))
		}
		caseSensitive := false
		if b, ok := arg(args, kwargs, 0, "case_sensitive"); ok {
			caseSensitive = truthy(b)
		}
		by := "key"
		if s, ok := argString(args, kwargs, 1, "by"); ok {
			by = s
		}
		reverse := false
		if b, ok := arg(args, kwargs, 2, "reverse"); ok {
			reverse = truthy(b)
		}
		pairs := make([]any, 0, m.Len())
		for _, e := range m.Entries() {
			pairs = append(pairs, []any{e.Key, e.Val})
		}
		idx := 0
		if by == "value" {
			idx = 1
		}
		sortAny(pairs, reverse, caseSensitive, func(p any) any { return p.([]any)[idx] })
		return pairs, nil
	}

	f["groupby"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		attr, ok := argString(args, kwargs, 0, "attribute")
		if !ok {
			return nil, fc.Errorf("groupby needs an attribute name")
		}
		key := attrKey(fc, attr)
		sorted := make([]any, len(items))
		copy(sorted, items)
		sortAny(sorted, false, true, key)

		out := []any{}
		var curKey any
		var group []any
		started := false
		flush := func() {
			if started {
				out = append(out, []any{curKey, group})
			}
		}
		for _, item := range sorted {
			k := key(item)
			if !started || !equalValues(k, curKey) {
				flush()
				curKey, group, started = k, []any{}, true
			}
			group = append(group, item)
		}
		flush()
		return out, nil
	}

	f["random"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return Undefined{Name: "random", Pos: fc.Pos, Hint: "the sequence is empty"}, nil
		}
		return items[fc.Rand().Intn(len(items))], nil
	}

	f["shuffle"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(items))
		copy(out, items)
		for i := len(out) - 1; i > 0; i-- {
			j := fc.Rand().Intn(i + 1)
			out[i], out[j] = out[j], out[i]
		}
		return out, nil
	}
}

func identity(v any) any { return v }

func attrKey(fc *FilterContext, attr string) func(any) any {
	return func(item any) any {
		v, err := resolveDotted(fc, item, attr)
		if err != nil {
			return nil
		}
		return v
	}
}

// resolveDotted walks `a.b.c` on a value, which is what the attribute
// arguments of sort, groupby, and selectattr accept.
func resolveDotted(fc *FilterContext, item any, attr string) (any, error) {
	cur := item
	for _, part := range strings.Split(attr, ".") {
		v, err := fc.r.getAttr(cur, part, fc.Pos)
		if err != nil {
			return nil, err
		}
		cur = v
	}
	return cur, nil
}

func extremum(wantMax bool) FilterFunc {
	return func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return Undefined{Name: "min/max", Pos: fc.Pos, Hint: "the sequence is empty"}, nil
		}
		caseSensitive := false
		if b, ok := arg(args, kwargs, 0, "case_sensitive"); ok {
			caseSensitive = truthy(b)
		}
		key := identity
		if attr, ok := argString(args, kwargs, 1, "attribute"); ok {
			key = attrKey(fc, attr)
		}
		best := items[0]
		for _, item := range items[1:] {
			if lessValue(key(best), key(item), caseSensitive) == wantMax {
				best = item
			}
		}
		return best, nil
	}
}

// selectFilter builds select, reject, selectattr, and rejectattr, which
// share everything but which value the test is applied to and whether the
// result is inverted.
func selectFilter(invert, byAttr bool) FilterFunc {
	return func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}

		var attr string
		if byAttr {
			var ok bool
			attr, ok = argString(args, kwargs, 0, "attribute")
			if !ok {
				return nil, fc.Errorf("this filter needs an attribute name")
			}
			args = args[1:]
		}

		// With no test named, the value's own truthiness decides.
		testName := ""
		var testArgs []any
		if len(args) > 0 {
			s, ok := args[0].(string)
			if !ok {
				return nil, fc.Errorf("the test name must be a string")
			}
			testName, testArgs = s, args[1:]
		}

		out := []any{}
		for _, item := range items {
			subject := item
			if byAttr {
				subject, err = resolveDotted(fc, item, attr)
				if err != nil {
					return nil, err
				}
			}
			var keep bool
			if testName == "" {
				keep = truthy(subject)
			} else {
				fn, ok := fc.r.env.tests[testName]
				if !ok {
					return nil, fc.Errorf("unknown test %q", testName)
				}
				keep, err = fn(fc, subject, testArgs)
				if err != nil {
					return nil, err
				}
			}
			if keep != invert {
				out = append(out, item)
			}
		}
		return out, nil
	}
}

// setOps backs the union, intersect, difference, and symmetric_difference
// filters Salt adds.
func setOp(name string, keepInA, keepInBoth, keepInB bool) FilterFunc {
	return func(fc *FilterContext, v any, args []any, _ map[string]any) (any, error) {
		a, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		if len(args) != 1 {
			return nil, fc.Errorf("%s takes one sequence argument", name)
		}
		b, err := asSeq(fc, args[0])
		if err != nil {
			return nil, err
		}
		inB := func(x any) bool {
			for _, y := range b {
				if equalValues(x, y) {
					return true
				}
			}
			return false
		}
		inA := func(x any) bool {
			for _, y := range a {
				if equalValues(x, y) {
					return true
				}
			}
			return false
		}
		out := []any{}
		appendUnique := func(x any) {
			for _, y := range out {
				if equalValues(x, y) {
					return
				}
			}
			out = append(out, x)
		}
		for _, x := range a {
			if inB(x) {
				if keepInBoth {
					appendUnique(x)
				}
				continue
			}
			if keepInA {
				appendUnique(x)
			}
		}
		if keepInB {
			for _, x := range b {
				if !inA(x) {
					appendUnique(x)
				}
			}
		}
		return out, nil
	}
}

// stats backs the avg and stdev filters.
func numbersOf(fc *FilterContext, v any) ([]float64, error) {
	items, err := asSeq(fc, v)
	if err != nil {
		return nil, err
	}
	out := make([]float64, 0, len(items))
	for _, item := range items {
		f, ok := asFloat(item)
		if !ok {
			return nil, fc.Errorf("expected a sequence of numbers, found %s", typeName(item))
		}
		out = append(out, f)
	}
	return out, nil
}

func mean(ns []float64) float64 {
	if len(ns) == 0 {
		return 0
	}
	total := 0.0
	for _, n := range ns {
		total += n
	}
	return total / float64(len(ns))
}

func stdev(ns []float64) float64 {
	if len(ns) < 2 {
		return 0
	}
	m := mean(ns)
	sum := 0.0
	for _, n := range ns {
		sum += (n - m) * (n - m)
	}
	return math.Sqrt(sum / float64(len(ns)-1))
}
