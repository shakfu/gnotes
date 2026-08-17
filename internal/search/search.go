// Package search provides full-text search over note bodies, titles and tags.
//
// The index is built in memory from a materialized tree and never written to
// disk. A personal project's text measures in megabytes, so building the index
// costs a few milliseconds, and an index on disk would be one more thing to
// keep in step with the log, invalidate on sync, and merge between machines.
// Rebuilding is simply cheaper than maintaining.
package search

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/shakfu/gnotes/internal/state"
)

// Field weights. A hit in a title says far more about relevance than a hit
// buried in a body, so the same word counts for more depending on where it is.
const (
	weightTitle = 8
	weightTag   = 5
	weightBody  = 1

	// A node whose title contains the query as a whole phrase is almost always
	// the one being looked for, so it outranks any accumulation of scattered
	// term hits.
	bonusTitlePhrase = 40

	// Matching a term only by prefix is weaker evidence than matching it
	// whole, so a prefix hit scores a fraction of an exact one.
	prefixDivisor = 3
)

// Index is a searchable snapshot of a set of nodes.
type Index struct {
	// docs are the indexed nodes, addressed by position in postings.
	docs []*state.Node

	// postings maps a term to the documents containing it, with the weighted
	// count of how often. Sorted by document for a cheap merge during search.
	postings map[string][]posting

	// terms is every indexed term, sorted, so a prefix query can be answered
	// with a binary search instead of a scan.
	terms []string
}

// posting is one term occurrence record.
type posting struct {
	doc   int32
	score int32
}

// Build indexes the given nodes. Deleted nodes are skipped, since a search
// that turned up tombstones would be worse than useless.
func Build(nodes []*state.Node) *Index {
	ix := &Index{
		docs:     make([]*state.Node, 0, len(nodes)),
		postings: make(map[string][]posting, len(nodes)*8),
	}

	// Accumulate per document, then flush, so each term gets one posting per
	// document rather than one per occurrence.
	scores := make(map[string]int32, 64)

	for _, n := range nodes {
		if n == nil || n.Deleted {
			continue
		}
		doc := int32(len(ix.docs))
		ix.docs = append(ix.docs, n)

		clear(scores)
		addTokens(scores, n.Title, weightTitle)
		addTokens(scores, n.Body, weightBody)
		for _, tag := range n.Tags {
			addTokens(scores, tag, weightTag)
		}

		for term, score := range scores {
			ix.postings[term] = append(ix.postings[term], posting{doc: doc, score: score})
		}
	}

	ix.terms = make([]string, 0, len(ix.postings))
	for term := range ix.postings {
		ix.terms = append(ix.terms, term)
	}
	sort.Strings(ix.terms)

	return ix
}

// Len reports how many documents are indexed.
func (ix *Index) Len() int { return len(ix.docs) }

// Terms reports how many distinct terms are indexed.
func (ix *Index) Terms() int { return len(ix.terms) }

// addTokens folds every token of text into scores at the given weight.
func addTokens(scores map[string]int32, text string, weight int32) {
	tokenize(text, func(tok string) { scores[tok] += weight })
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

// Result is one matching node and why it matched.
type Result struct {
	Node *state.Node

	// Score is the weighted relevance. It is comparable only within one search.
	Score int

	// Matched lists the query terms this node actually matched, so the
	// interface can highlight them.
	Matched []string
}

// Search returns the nodes matching every term of the query, best first.
//
// Terms are combined with AND rather than OR, because a search that returns
// everything containing any word is noise. The final term also matches by
// prefix, which is what makes typing into a live search box narrow results as
// it goes rather than only at word boundaries.
//
// A limit of zero returns every match.
func (ix *Index) Search(query string, limit int) []Result {
	terms := Tokenize(query)
	if len(terms) == 0 {
		return nil
	}

	// scores accumulates only over documents that matched every term so far,
	// so the working set shrinks with each additional term.
	var scores map[int32]int32
	matched := make(map[int32][]string, 16)

	for i, term := range terms {
		// The last term is treated as a prefix so that a partially typed word
		// still matches. Earlier terms are complete by construction: the user
		// typed a space after them.
		hits := ix.lookup(term, i == len(terms)-1)
		if len(hits) == 0 {
			return nil
		}

		if scores == nil {
			scores = make(map[int32]int32, len(hits))
			for doc, score := range hits {
				scores[doc] = score
				matched[doc] = append(matched[doc], term)
			}
			continue
		}

		// Intersect: drop anything the new term did not also hit.
		for doc := range scores {
			score, ok := hits[doc]
			if !ok {
				delete(scores, doc)
				delete(matched, doc)
				continue
			}
			scores[doc] += score
			matched[doc] = append(matched[doc], term)
		}
		if len(scores) == 0 {
			return nil
		}
	}

	phrase := strings.ToLower(strings.TrimSpace(query))
	out := make([]Result, 0, len(scores))
	for doc, score := range scores {
		n := ix.docs[doc]
		if strings.Contains(strings.ToLower(n.Title), phrase) {
			score += bonusTitlePhrase
		}
		out = append(out, Result{Node: n, Score: int(score), Matched: matched[doc]})
	}

	// Ties break on id so repeating a search never reshuffles the results.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Node.ID < out[j].Node.ID
	})

	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// lookup returns the documents hit by a term, optionally including every term
// it prefixes.
func (ix *Index) lookup(term string, allowPrefix bool) map[int32]int32 {
	hits := make(map[int32]int32, 8)

	for _, p := range ix.postings[term] {
		hits[p.doc] += p.score
	}

	if !allowPrefix {
		return hits
	}

	// terms is sorted, so everything sharing a prefix is one contiguous run.
	start := sort.SearchStrings(ix.terms, term)
	for i := start; i < len(ix.terms) && strings.HasPrefix(ix.terms[i], term); i++ {
		if ix.terms[i] == term {
			continue // already counted at full weight above
		}
		for _, p := range ix.postings[ix.terms[i]] {
			hits[p.doc] += p.score / prefixDivisor
		}
	}
	return hits
}

// Complete returns up to limit indexed terms starting with prefix, for
// suggesting completions as a query is typed.
func (ix *Index) Complete(prefix string, limit int) []string {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix == "" {
		return nil
	}

	var out []string
	for i := sort.SearchStrings(ix.terms, prefix); i < len(ix.terms); i++ {
		if !strings.HasPrefix(ix.terms[i], prefix) {
			break
		}
		out = append(out, ix.terms[i])
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
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
