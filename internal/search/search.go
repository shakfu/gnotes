// Package search turns typed text into full-text queries.
//
// The index itself is an FTS5 table in the wiki cache; see package wiki.
package search

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Query turns typed text into an FTS5 MATCH expression, or "" when the text has
// no words.
//
// Every word must match. The last also matches by prefix, so typing into a
// live search box narrows results before the word is finished; its exact form
// is ORed in so that an exact hit scores above a prefix hit. Words are quoted,
// so FTS5 operators in the text are searched as plain words.
func Query(text string) string {
	terms := Tokenize(text)
	var b strings.Builder
	for i, t := range terms {
		// A term holds only letters, digits and apostrophes, so quoting cannot
		// be broken out of.
		q := `"` + t + `"`
		if i > 0 {
			b.WriteString(" AND ")
		}
		if i < len(terms)-1 {
			b.WriteString(q)
		} else {
			b.WriteString("(" + q + " OR " + q + "*)")
		}
	}
	return b.String()
}

// Tokenize splits text into lowercase search terms.
//
// Words break on anything that is not a letter or digit, which keeps
// punctuation and markdown syntax out of the index. Apostrophes are the one
// exception: splitting "don't" into two terms would make it unfindable by
// either half.
func Tokenize(text string) []string {
	var out []string
	tokenize(text, func(tok string) { out = append(out, tok) })
	return out
}

// tokenize walks text and calls fn with each term.
//
// A term that is already lowercase, which is most prose, is handed over as a
// substring of the input with no allocation at all; only a word containing
// something that changes under lowercasing is copied into a buffer. Indexing a
// project is hundreds of thousands of tokens, and this is the difference
// between one allocation per word and almost none.
func tokenize(text string, fn func(string)) {
	var buf []byte
	start := -1
	verbatim := true

	flush := func(end int) {
		if start < 0 {
			return
		}
		tok := text[start:end]
		if !verbatim {
			tok = string(buf)
		}
		// An apostrophe belongs inside a word, not at its edges.
		if tok = strings.Trim(tok, "'"); tok != "" {
			fn(tok)
		}
		start, verbatim, buf = -1, true, buf[:0]
	}

	for i, r := range text {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !(r == '\'' && start >= 0) {
			flush(i)
			continue
		}
		if start < 0 {
			start = i
		}

		lower := unicode.ToLower(r)
		if lower != r && verbatim {
			// The first character needing a change forces a copy; everything
			// already scanned is still verbatim and can be bulk-copied.
			verbatim = false
			buf = append(buf[:0], text[start:i]...)
		}
		if !verbatim {
			buf = utf8.AppendRune(buf, lower)
		}
	}
	flush(len(text))
}
