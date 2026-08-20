package value

// Merge strategies, as SPEC section 12.3 defines them for pillar and as
// the configuration loader uses them for drop-in fragments.
//
// The strategies are Salt's, including the parts that surprise people: by
// default `recurse` replaces a list rather than concatenating it, because
// that is what Salt does and what existing pillar trees are written
// against. `pillar_merge_lists` and the `aggregate` strategy are the two
// ways to get concatenation.

// Strategy names a merge behaviour.
type Strategy int

const (
	// Smart recurses into mappings and replaces everything else. The
	// default.
	Smart Strategy = iota
	// Recurse deep merges mappings; later sources replace scalars and,
	// unless MergeLists is set, lists.
	Recurse
	// Aggregate deep merges mappings and concatenates lists.
	Aggregate
	// Overwrite replaces the key entirely, at the top level.
	Overwrite
)

// ParseStrategy resolves a configured strategy name.
func ParseStrategy(s string) (Strategy, bool) {
	switch s {
	case "smart", "":
		return Smart, true
	case "recurse":
		return Recurse, true
	case "aggregate":
		return Aggregate, true
	case "overwrite":
		return Overwrite, true
	}
	return Smart, false
}

func (s Strategy) String() string {
	switch s {
	case Recurse:
		return "recurse"
	case Aggregate:
		return "aggregate"
	case Overwrite:
		return "overwrite"
	default:
		return "smart"
	}
}

// MergeOpts controls a merge.
type MergeOpts struct {
	Strategy Strategy
	// MergeLists concatenates lists under Recurse and Smart. Salt's
	// pillar_merge_lists, default false.
	MergeLists bool
}

// The per-key directives Salt honours inside pillar data. A mapping that
// carries one of these keys overrides the ambient strategy for that
// mapping only.
const (
	directiveOverwrite = "__overwrite__"
	directiveReplace   = "__replace__"
	directiveAggregate = "__aggregate__"
)

// Merge combines src into dst and returns the result. Neither argument is
// mutated: the result shares unmodified subtrees with its inputs, and any
// mapping the merge touches is copied first.
func Merge(dst, src any, opts MergeOpts) any {
	return mergeValue(dst, src, opts, 0)
}

func mergeValue(dst, src any, opts MergeOpts, depth int) any {
	// A pathological pillar tree should not blow the stack. 100 matches
	// the alias depth limit the parser enforces.
	if depth > 100 {
		return src
	}
	if opts.Strategy == Overwrite && depth == 0 {
		return overwriteTop(dst, src)
	}

	dm, dok := dst.(*Map)
	sm, sok := src.(*Map)
	if dok && sok && dm != nil && sm != nil {
		return mergeMaps(dm, sm, opts, depth)
	}

	if opts.Strategy == Aggregate || opts.MergeLists {
		dl, dok := dst.([]any)
		sl, sok := src.([]any)
		if dok && sok {
			out := make([]any, 0, len(dl)+len(sl))
			out = append(out, dl...)
			out = append(out, sl...)
			return out
		}
	}

	// A source that is absent leaves the destination alone; anything else
	// replaces it.
	if src == nil && dst != nil {
		return src
	}
	return src
}

// overwriteTop replaces each top-level key of dst with src's, without
// recursing. This is Salt's `overwrite` strategy.
func overwriteTop(dst, src any) any {
	dm, dok := dst.(*Map)
	sm, sok := src.(*Map)
	if !dok || !sok || dm == nil || sm == nil {
		return src
	}
	out := dm.Clone()
	for _, e := range sm.Entries() {
		out.SetAt(e.Key, e.Val, e.KeyPos, e.ValPos)
	}
	return out
}

func mergeMaps(dst, src *Map, opts MergeOpts, depth int) *Map {
	local := opts
	// A directive inside the source mapping overrides the ambient
	// strategy for this level. The directive key itself is not carried
	// into the result.
	skip := map[string]bool{}
	for _, e := range src.Entries() {
		k, ok := e.Key.(string)
		if !ok {
			continue
		}
		switch k {
		case directiveOverwrite, directiveReplace:
			if Truthy(e.Val) {
				skip[k] = true
				out := NewMap(src.Len())
				for _, se := range src.Entries() {
					if sk, ok := se.Key.(string); ok && isDirective(sk) {
						continue
					}
					out.SetAt(se.Key, se.Val, se.KeyPos, se.ValPos)
				}
				return out
			}
			skip[k] = true
		case directiveAggregate:
			if Truthy(e.Val) {
				local.Strategy = Aggregate
				local.MergeLists = true
			}
			skip[k] = true
		}
	}

	out := dst.Clone()
	for _, e := range src.Entries() {
		if k, ok := e.Key.(string); ok && skip[k] {
			continue
		}
		if cur, ok := out.Get(e.Key); ok {
			out.SetAt(e.Key, mergeValue(cur, e.Val, local, depth+1), e.KeyPos, e.ValPos)
			continue
		}
		out.SetAt(e.Key, e.Val, e.KeyPos, e.ValPos)
	}
	return out
}

func isDirective(k string) bool {
	return k == directiveOverwrite || k == directiveReplace || k == directiveAggregate
}
