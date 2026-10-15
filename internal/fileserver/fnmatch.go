package fileserver

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// fnmatch is Python's fnmatch.fnmatchcase, which is what Salt's
// file_ignore_glob means.
//
// Salt applies the setting in salt/fileserver/__init__.py
// is_file_ignored with fnmatch.fnmatch, and on the POSIX hosts a hub
// runs on fnmatch.fnmatch is fnmatchcase: os.path.normcase is the
// identity there, so the match is case-sensitive. The rules that matter,
// all of them different from Go's path.Match, which this replaced:
//
//   - `*` matches any run of characters, `/` included. path.Match's `*`
//     stops at `/`, so `secrets/*` hid `secrets/a.txt` and served
//     `secrets/deep/b.txt` -- a hide rule migrated from Salt silently
//     stopped hiding everything below its first level. DIVERGENCE 5.290.
//   - `?` matches any one character, `/` included.
//   - `[seq]` and `[!seq]` are sets, with `a-z` ranges. `^` is not
//     negation, and `]` first in the set is a literal.
//   - A backslash is a literal character, not an escape, inside a set
//     and out of one.
//   - A `[` with no closing `]` is a literal `[`, where path.Match
//     reports ErrBadPattern -- which the old code read as "no match", so
//     a malformed hide rule hid nothing and said nothing.
//
// It is a port of fnmatch._translate (CPython 3.15), producing an RE2
// expression. CPython 3.10 to 3.14 give the same answers on the whole
// test table; 3.9 differs only by raising on a backward range such as
// `[z-a]`, which 3.10 and later treat as empty, as this does. The tests hold its answers against Python's own,
// captured by running fnmatch.fnmatchcase, not written from the
// documentation.
func fnmatch(pattern, name string) bool {
	return fnmatchRegexp(pattern).MatchString(name)
}

var fnmatchCache sync.Map // pattern -> *regexp.Regexp

func fnmatchRegexp(pattern string) *regexp.Regexp {
	if re, ok := fnmatchCache.Load(pattern); ok {
		return re.(*regexp.Regexp)
	}
	re := regexp.MustCompile(fnmatchTranslate(pattern))
	fnmatchCache.Store(pattern, re)
	return re
}

// fnmatchTranslate follows fnmatch._translate step for step, so that it
// can be read against it. Every literal character is written as
// \x{...}, which is a literal in RE2 both inside a class and outside one
// and so needs no case analysis of what RE2 would treat as special.
func fnmatchTranslate(pattern string) string {
	pat := []rune(pattern)
	n := len(pat)
	var b strings.Builder
	b.WriteString(`(?s)\A`)
	lit := func(r rune) string { return fmt.Sprintf(`\x{%x}`, r) }

	i := 0
	for i < n {
		c := pat[i]
		i++
		switch c {
		case '*':
			b.WriteString(`.*`)
			for i < n && pat[i] == '*' {
				i++
			}
		case '?':
			b.WriteString(`.`)
		case '[':
			j := i
			if j < n && pat[j] == '!' {
				j++
			}
			if j < n && pat[j] == ']' {
				j++
			}
			for j < n && pat[j] != ']' {
				j++
			}
			if j >= n {
				b.WriteString(lit('['))
				continue
			}
			b.WriteString(fnmatchSet(pat, i, j, lit))
			i = j + 1
		default:
			b.WriteString(lit(c))
		}
	}
	b.WriteString(`\z`)
	return b.String()
}

// fnmatchSet translates pat[i:j], the inside of a `[...]`.
//
// Python splits the inside at each hyphen that forms a range into
// chunks; the last character of one chunk and the first of the next are
// a range's ends, and every other character -- hyphens included -- is a
// literal. A chunk boundary whose range runs backwards (`z-a`) is
// dropped, as Python drops it, because it is an empty range.
func fnmatchSet(pat []rune, i, j int, lit func(rune) string) string {
	var chunks [][]rune
	if !containsRune(pat[i:j], '-') {
		chunks = [][]rune{append([]rune(nil), pat[i:j]...)}
	} else {
		k := i + 1
		if pat[i] == '!' {
			k = i + 2
		}
		for {
			k = indexRune(pat, '-', k, j)
			if k < 0 {
				break
			}
			chunks = append(chunks, append([]rune(nil), pat[i:k]...))
			i = k + 1
			k += 3
		}
		chunk := append([]rune(nil), pat[i:j]...)
		if len(chunk) > 0 {
			chunks = append(chunks, chunk)
		} else {
			chunks[len(chunks)-1] = append(chunks[len(chunks)-1], '-')
		}
		for k := len(chunks) - 1; k > 0; k-- {
			prev, cur := chunks[k-1], chunks[k]
			if prev[len(prev)-1] > cur[0] {
				chunks[k-1] = append(append([]rune(nil), prev[:len(prev)-1]...), cur[1:]...)
				chunks = append(chunks[:k], chunks[k+1:]...)
			}
		}
	}

	// Python decides negation on the joined text's first character,
	// which is the first chunk's first character.
	negate := len(chunks) > 0 && len(chunks[0]) > 0 && chunks[0][0] == '!'
	if negate {
		chunks[0] = chunks[0][1:]
	}
	empty := true
	for _, ch := range chunks {
		if len(ch) > 0 {
			empty = false
		}
	}
	if empty {
		if negate {
			// `[!]`-shaped after range removal: any one character.
			return `.`
		}
		// An empty set never matches.
		return `[^\x00-\x{10FFFF}]`
	}

	var b strings.Builder
	b.WriteString("[")
	if negate {
		b.WriteString("^")
	}
	for ci, ch := range chunks {
		if ci > 0 {
			// The joint between two chunks is a range.
			b.WriteString("-")
		}
		for _, r := range ch {
			b.WriteString(lit(r))
		}
	}
	b.WriteString("]")
	return b.String()
}

func containsRune(rs []rune, r rune) bool {
	for _, x := range rs {
		if x == r {
			return true
		}
	}
	return false
}

// indexRune is Python's str.find(sub, start, end) for one rune.
func indexRune(rs []rune, r rune, start, end int) int {
	for k := start; k < end && k < len(rs); k++ {
		if rs[k] == r {
			return k
		}
	}
	return -1
}
