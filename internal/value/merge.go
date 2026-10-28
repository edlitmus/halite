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

// Merge combines src into dst and returns the result. Neither argument is
// mutated: the result shares unmodified subtrees with its inputs, and any
// mapping the merge touches is copied first.
func Merge(dst, src any, opts MergeOpts) any {
	return mergeValue(dst, src, opts, 0)
}

// maxMergeDepth is how far a merge recurses before it stops merging.
const maxMergeDepth = 10000

func mergeValue(dst, src any, opts MergeOpts, depth int) any {
	// A pathological tree should not blow the stack, and past this depth
	// src replaces dst whole -- which silently drops whatever dst held
	// there, so the bound has to be one that no honest tree reaches.
	// It used to be 100, "to match the parser". The parser bounds what is
	// read from a file, but a tree built by a template or a runner is not
	// read from one, and a merge of two such trees 120 levels down lost
	// every sibling key of dst without a word. A frame here is a couple
	// of hundred bytes, so this costs a few megabytes of stack at worst.
	if depth > maxMergeDepth {
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

// mergeMaps deep merges src into a copy of dst.
//
// `__replace__`, `__overwrite__` and `__aggregate__` are keys like any
// other. This used to read them as per-mapping directives, which SPEC
// 12.3 said were Salt's. They are not: neither Salt 3006.28's source nor
// 3008.3's has the strings, and salt-call kept each one in the compiled
// pillar as data, merging around it under every strategy. Here
// `__replace__: True` discarded what an earlier pillar file had put
// beside it, and the key vanished -- unless no earlier file had that
// key, when it was kept, so the "directive" did not agree with itself
// either. DIVERGENCE 5.468.
func mergeMaps(dst, src *Map, opts MergeOpts, depth int) *Map {
	out := dst.Clone()
	for _, e := range src.Entries() {
		if cur, ok := out.Get(e.Key); ok {
			out.SetAt(e.Key, mergeValue(cur, e.Val, opts, depth+1), e.KeyPos, e.ValPos)
			continue
		}
		out.SetAt(e.Key, e.Val, e.KeyPos, e.ValPos)
	}
	return out
}
