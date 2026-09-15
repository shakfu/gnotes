// Package search turns typed text into full-text queries and shows why a
// result matched.
//
// The index itself is an FTS5 table in the project database; see package
// store. This package keeps the tokenizer, so a query and a snippet split
// words the same way.
package search

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/shakfu/gnotes/internal/state"
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

// Snippet returns a fragment of a node's body around the first query term
// found in it, for showing why a result matched.
//
// It returns the empty string when nothing in the body matches, which is the
// case whenever a node matched on its title or tags alone; the caller already
// shows those, so repeating them as a snippet would say nothing.
func Snippet(n *state.Node, query string, width int) string {
	if n.Body == "" || width <= 0 {
		return ""
	}
	body := n.Body
	lower := strings.ToLower(body)

	at := -1
	for _, term := range Tokenize(query) {
		if i := strings.Index(lower, term); i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	if at < 0 {
		return ""
	}

	// Centre the window on the hit, then pull back to word boundaries so the
	// fragment does not begin or end mid-word.
	start := at - width/3
	if start < 0 {
		start = 0
	}
	end := start + width
	if end > len(body) {
		end = len(body)
		start = max(0, end-width)
	}
	// The offsets were found in the lowercased body, whose byte length can
	// differ from the original's, so they are moved to character boundaries
	// before slicing rather than trusted to be on one.
	start = min(start, len(body))
	for start > 0 && !utf8.RuneStart(body[start]) {
		start--
	}
	for end < len(body) && !utf8.RuneStart(body[end]) {
		end++
	}

	frag := body[start:end]
	if start > 0 {
		if i := strings.IndexAny(frag, " \n\t"); i >= 0 && i < width/4 {
			frag = frag[i+1:]
		}
	}
	if end < len(body) {
		if i := strings.LastIndexAny(frag, " \n\t"); i > len(frag)-width/4 {
			frag = frag[:i]
		}
	}

	frag = strings.Join(strings.Fields(frag), " ")
	if start > 0 {
		frag = "..." + frag
	}
	if end < len(body) {
		frag += "..."
	}
	return frag
}
