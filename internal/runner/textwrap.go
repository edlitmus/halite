package runner

import (
	"strings"
	"unicode"
)

// pyFill is Python's textwrap.fill(text, width, initial_indent=initial,
// subsequent_indent=subsequent) with every other argument at its default,
// which is how Salt's highstate outputter wraps a state's warnings.
//
// The renderer wrapped on whitespace only, and Python also breaks inside
// a hyphenated word -- "re-" at the end of one line, "issued" at the start
// of the next -- and inside a word too long for a line at its last hyphen.
// That was the one place 5.245 recorded the renderer knew it differed
// from Salt's. This is a port of TextWrapper's own steps rather than an
// approximation of their result: the text is munged (tabs expanded, every
// whitespace character made a space), split into chunks by wordsep_re,
// and the chunks packed greedily into lines. The expected outputs in
// testdata/textwrap.json were made by Python's textwrap itself.
// DIVERGENCE 5.272.
func pyFill(text string, width int, initial, subsequent string) string {
	chunks := pySplit(pyMunge(text))
	return strings.Join(pyWrapChunks(chunks, width, initial, subsequent), "\n")
}

// pyWhitespace is textwrap's _whitespace: ASCII only, not Unicode's.
const pyWhitespace = "\t\n\x0b\x0c\r "

func isPyWS(r rune) bool { return strings.ContainsRune(pyWhitespace, r) }

// pyMunge is _munge_whitespace: str.expandtabs(8), then every whitespace
// character becomes a space.
func pyMunge(text string) string {
	var b strings.Builder
	col := 0
	for _, r := range text {
		switch {
		case r == '\t':
			n := 8 - col%8
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		case r == '\n' || r == '\r':
			col = 0
		default:
			col++
		}
		if isPyWS(r) {
			b.WriteRune(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isWord is \w for a str pattern: a letter, a digit or an underscore.
func isWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// isLetter is [^\d\W]: a word character that is not a digit.
func isLetter(r rune) bool { return isWord(r) && !unicode.IsDigit(r) }

// isWordPunct is word_punct, [\w!"'&.,?].
func isWordPunct(r rune) bool { return isWord(r) || strings.ContainsRune(`!"'&.,?`, r) }

// pySplit is wordsep_re.split, with the empty strings dropped: whitespace
// runs, em-dashes between words, and words broken after a hyphen that
// has two letters, or a letter-hyphen-letter, before it and a letter
// (perhaps across one more hyphen) after. Go's regexp has no lookaround,
// so the alternatives are tried by hand, in the pattern's order, at
// each position.
func pySplit(text string) []string {
	s := []rune(text)
	at := func(i int) rune {
		if i < 0 || i >= len(s) {
			return 0
		}
		return s[i]
	}
	// emDashAt is (?<=wp) -{2,} (?=\w) starting at i: the length of the
	// dash run, or 0.
	emDashAt := func(i int) int {
		if i == 0 || !isWordPunct(s[i-1]) {
			return 0
		}
		j := i
		for j < len(s) && s[j] == '-' {
			j++
		}
		if j-i >= 2 && j < len(s) && isWord(s[j]) {
			return j - i
		}
		return 0
	}
	var out []string
	for i := 0; i < len(s); {
		// Any whitespace.
		if isPyWS(s[i]) {
			j := i
			for j < len(s) && isPyWS(s[j]) {
				j++
			}
			out = append(out, string(s[i:j]))
			i = j
			continue
		}
		// An em-dash between words.
		if n := emDashAt(i); n > 0 {
			out = append(out, string(s[i:i+n]))
			i += n
			continue
		}
		// A word, possibly hyphenated: the shortest run of non-space that
		// ends at a breakable hyphen, before whitespace or the end, or
		// before an em-dash.
		end := -1
		for k := i + 1; k <= len(s); k++ {
			if k > i && isPyWS(s[k-1]) {
				break // nws+? cannot take whitespace
			}
			// Hyphenated: the k chars, then '-', with lt{2}- or lt-lt-
			// behind the hyphen's end and lt -? lt ahead of it.
			if at(k) == '-' {
				h := k + 1 // position after the hyphen
				behind := (isLetter(at(h-3)) && isLetter(at(h-2))) ||
					(isLetter(at(h-4)) && at(h-3) == '-' && isLetter(at(h-2)))
				ahead := isLetter(at(h)) &&
					(isLetter(at(h+1)) || (at(h+1) == '-' && isLetter(at(h+2))))
				if behind && ahead {
					end = h
					break
				}
			}
			// The end of the word.
			if k == len(s) || isPyWS(s[k]) {
				end = k
				break
			}
			// Before an em-dash.
			if isWordPunct(s[k-1]) && emDashAt(k) > 0 {
				end = k
				break
			}
		}
		if end < 0 {
			end = len(s)
		}
		out = append(out, string(s[i:end]))
		i = end
	}
	return out
}

// pyWrapChunks is _wrap_chunks with drop_whitespace and break_long_words
// and break_on_hyphens on, no max_lines, and lengths counted in
// characters, as Python's len counts them.
func pyWrapChunks(chunks []string, width int, initial, subsequent string) []string {
	n := func(s string) int { return len([]rune(s)) }
	isBlank := func(s string) bool { return strings.TrimSpace(s) == "" }
	// Reversed, so the next chunk is the last, as Python keeps them.
	rev := make([]string, len(chunks))
	for i, c := range chunks {
		rev[len(chunks)-1-i] = c
	}
	var lines []string
	for len(rev) > 0 {
		var cur []string
		curLen := 0
		indent := initial
		if len(lines) > 0 {
			indent = subsequent
		}
		w := width - n(indent)
		// Whitespace at the start of a line is dropped, except on the
		// first line.
		if isBlank(rev[len(rev)-1]) && len(lines) > 0 {
			rev = rev[:len(rev)-1]
		}
		for len(rev) > 0 {
			l := n(rev[len(rev)-1])
			if curLen+l > w {
				break
			}
			cur = append(cur, rev[len(rev)-1])
			curLen += l
			rev = rev[:len(rev)-1]
		}
		// A chunk longer than a whole line is broken: at its last hyphen
		// inside the room left, if that hyphen has something other than
		// hyphens before it, and otherwise at the room left.
		if len(rev) > 0 && n(rev[len(rev)-1]) > w {
			spaceLeft := w - curLen
			if spaceLeft < 1 {
				spaceLeft = 1
			}
			chunk := []rune(rev[len(rev)-1])
			end := spaceLeft
			if len(chunk) > spaceLeft {
				if h := lastIndex(chunk[:spaceLeft], '-'); h > 0 && hasNonHyphen(chunk[:h]) {
					end = h + 1
				}
			}
			cur = append(cur, string(chunk[:end]))
			rev[len(rev)-1] = string(chunk[end:])
		}
		// And at the end of one.
		if len(cur) > 0 && isBlank(cur[len(cur)-1]) {
			cur = cur[:len(cur)-1]
		}
		if len(cur) > 0 {
			lines = append(lines, indent+strings.Join(cur, ""))
		}
	}
	return lines
}

func lastIndex(s []rune, r rune) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == r {
			return i
		}
	}
	return -1
}

func hasNonHyphen(s []rune) bool {
	for _, r := range s {
		if r != '-' {
			return true
		}
	}
	return false
}
