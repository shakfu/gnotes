package search

import (
	"strings"
	"testing"

	"github.com/shakfu/gnotes/internal/state"
)

func node(id, title, body string, tags ...string) *state.Node {
	return &state.Node{ID: id, Kind: state.KindNote, Title: title, Body: body, Tags: tags}
}

func corpus() []*state.Node {
	return []*state.Node{
		node("01A", "parser design", "The lexer tokenizes input before the parser runs.", "design"),
		node("01B", "shopping list", "milk, bread, coffee beans", "personal"),
		node("01C", "lexer bugs", "An ambiguity in the grammar breaks the lexer on nested quotes.", "bug", "parser"),
		node("01D", "meeting notes", "Discussed the parser rewrite and the timeline.", "work"),
	}
}

func ids(results []Result) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.Node.ID
	}
	return out
}

func TestSearchFindsTitleBodyAndTags(t *testing.T) {
	ix := Build(corpus())

	cases := map[string]string{
		"tokenizes": "01A", // body only
		"shopping":  "01B", // title only
		"personal":  "01B", // tag only
		"ambiguity": "01C",
	}

	for query, want := range cases {
		got := ix.Search(query, 0)
		if len(got) == 0 {
			t.Errorf("%q found nothing", query)
			continue
		}
		if got[0].Node.ID != want {
			t.Errorf("%q = %v, want %s first", query, ids(got), want)
		}
	}
}

// A title hit is far stronger evidence than a body hit, so it must outrank it.
func TestTitleHitsOutrankBodyHits(t *testing.T) {
	ix := Build(corpus())

	got := ix.Search("lexer", 0)
	if len(got) < 2 {
		t.Fatalf("expected several matches, got %v", ids(got))
	}
	if got[0].Node.ID != "01C" {
		t.Fatalf("got %v, want the node with lexer in its title first", ids(got))
	}
}

// A whole-phrase title match is almost always the intended result.
func TestTitlePhraseWins(t *testing.T) {
	ix := Build(corpus())

	got := ix.Search("parser design", 0)
	if len(got) == 0 || got[0].Node.ID != "01A" {
		t.Fatalf("got %v, want the exact title phrase first", ids(got))
	}
}

// Terms are combined with AND; a search returning everything containing any
// word would be noise.
func TestMultipleTermsAreAnded(t *testing.T) {
	ix := Build(corpus())

	got := ix.Search("lexer grammar", 0)
	if len(got) != 1 || got[0].Node.ID != "01C" {
		t.Fatalf("got %v, want only the node containing both terms", ids(got))
	}

	if got := ix.Search("lexer nonexistentword", 0); len(got) != 0 {
		t.Fatalf("got %v, want nothing when one term is absent", ids(got))
	}
}

// The last term matches by prefix so a live search box narrows as it is typed.
func TestLastTermMatchesByPrefix(t *testing.T) {
	ix := Build(corpus())

	for _, partial := range []string{"lex", "lexe", "lexer"} {
		got := ix.Search(partial, 0)
		if len(got) == 0 {
			t.Fatalf("%q found nothing", partial)
		}
		found := false
		for _, r := range got {
			if r.Node.ID == "01C" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%q = %v, want the lexer node", partial, ids(got))
		}
	}
}

// Earlier terms are complete by construction: the user typed a space after
// them, so prefix-matching them would return far too much.
func TestOnlyTheLastTermIsPrefixMatched(t *testing.T) {
	ix := Build([]*state.Node{
		node("01A", "alpha", "lexer things"),
		node("01B", "beta", "lex things"),
	})

	// "lex" as a non-final term must match only the exact token "lex".
	got := ix.Search("lex things", 0)
	if len(got) != 1 || got[0].Node.ID != "01B" {
		t.Fatalf("got %v, want only the exact-token match", ids(got))
	}
}

// An exact hit is stronger evidence than a prefix hit.
func TestExactMatchOutranksPrefixMatch(t *testing.T) {
	ix := Build([]*state.Node{
		node("01A", "note one", "parse"),
		node("01B", "note two", "parsing parser parsed"),
	})

	got := ix.Search("parse", 0)
	if len(got) != 2 {
		t.Fatalf("got %v, want both", ids(got))
	}
	if got[0].Node.ID != "01A" {
		t.Fatalf("got %v, want the exact match first", ids(got))
	}
}

func TestSearchIsCaseInsensitive(t *testing.T) {
	ix := Build(corpus())

	lower := ids(ix.Search("lexer", 0))
	upper := ids(ix.Search("LEXER", 0))

	if strings.Join(lower, ",") != strings.Join(upper, ",") {
		t.Fatalf("case changed the results: %v vs %v", lower, upper)
	}
}

func TestSearchIgnoresPunctuation(t *testing.T) {
	ix := Build([]*state.Node{node("01A", "the parser", "handles a.b.c and foo-bar!")})

	for _, query := range []string{"a.b.c", "foo-bar", "foo bar"} {
		if got := ix.Search(query, 0); len(got) == 0 {
			t.Errorf("%q found nothing", query)
		}
	}
}

// Splitting a contraction would make it unfindable by either half.
func TestApostrophesStayInsideWords(t *testing.T) {
	got := Tokenize("don't stop 'quoted' word")
	want := []string{"don't", "stop", "quoted", "word"}

	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Tokenize = %v, want %v", got, want)
	}
}

func TestTokenizeEmptyAndSymbolOnly(t *testing.T) {
	for _, in := range []string{"", "   ", "!!! ---", "\n\t"} {
		if got := Tokenize(in); len(got) != 0 {
			t.Errorf("Tokenize(%q) = %v, want nothing", in, got)
		}
	}
}

func TestSearchExcludesDeletedNodes(t *testing.T) {
	nodes := corpus()
	nodes[2].Deleted = true

	ix := Build(nodes)
	for _, r := range ix.Search("lexer", 0) {
		if r.Node.ID == "01C" {
			t.Fatal("a deleted node was returned")
		}
	}
}

func TestEmptyQueryAndEmptyIndex(t *testing.T) {
	ix := Build(corpus())
	for _, query := range []string{"", "   ", "!!!"} {
		if got := ix.Search(query, 0); got != nil {
			t.Errorf("Search(%q) = %v, want nothing", query, ids(got))
		}
	}

	empty := Build(nil)
	if empty.Len() != 0 || empty.Terms() != 0 {
		t.Fatal("an empty corpus produced a non-empty index")
	}
	if got := empty.Search("anything", 0); got != nil {
		t.Fatalf("searching an empty index returned %v", ids(got))
	}
}

func TestSearchLimit(t *testing.T) {
	ix := Build(corpus())

	all := ix.Search("parser", 0)
	if len(all) < 2 {
		t.Fatalf("expected several matches, got %v", ids(all))
	}
	limited := ix.Search("parser", 1)
	if len(limited) != 1 {
		t.Fatalf("limit ignored: %v", ids(limited))
	}
	if limited[0].Node.ID != all[0].Node.ID {
		t.Fatal("the limit dropped the best result")
	}
}

// Repeating a search must not reshuffle equally scored results.
func TestSearchOrderIsStable(t *testing.T) {
	var nodes []*state.Node
	for _, id := range []string{"01A", "01B", "01C", "01D", "01E"} {
		nodes = append(nodes, node(id, "identical title", "identical body text"))
	}
	ix := Build(nodes)

	first := strings.Join(ids(ix.Search("identical", 0)), ",")
	for i := 0; i < 30; i++ {
		if got := strings.Join(ids(ix.Search("identical", 0)), ","); got != first {
			t.Fatalf("run %d reordered: %s vs %s", i, got, first)
		}
	}
}

func TestResultReportsMatchedTerms(t *testing.T) {
	ix := Build(corpus())

	got := ix.Search("lexer grammar", 0)
	if len(got) != 1 {
		t.Fatalf("got %v", ids(got))
	}
	if len(got[0].Matched) != 2 {
		t.Fatalf("Matched = %v, want both terms", got[0].Matched)
	}
}

func TestComplete(t *testing.T) {
	ix := Build(corpus())

	got := ix.Complete("par", 0)
	if len(got) == 0 {
		t.Fatal("Complete found nothing for a known prefix")
	}
	for _, term := range got {
		if !strings.HasPrefix(term, "par") {
			t.Fatalf("Complete returned %q, which does not start with the prefix", term)
		}
	}
	// Sorted, so completion lists do not jump around.
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("Complete is not sorted at %d: %v", i, got)
		}
	}

	if got := ix.Complete("par", 1); len(got) != 1 {
		t.Fatalf("Complete limit ignored: %v", got)
	}
	if got := ix.Complete("", 0); got != nil {
		t.Fatalf("Complete(\"\") = %v", got)
	}
	if got := ix.Complete("zzzznothing", 0); got != nil {
		t.Fatalf("Complete on an unknown prefix = %v", got)
	}
}

func TestSnippetCentresOnTheMatch(t *testing.T) {
	n := node("01A", "long note",
		"Some preamble text that goes on for a while before the interesting bit. "+
			"The distinctive word is xylophone and then more filler continues afterwards for a while.")

	got := Snippet(n, "xylophone", 60)
	if !strings.Contains(got, "xylophone") {
		t.Fatalf("Snippet = %q, does not contain the match", got)
	}
	if !strings.HasPrefix(got, "...") || !strings.HasSuffix(got, "...") {
		t.Fatalf("Snippet = %q, want elision markers on both sides", got)
	}
	if len(got) > 80 {
		t.Fatalf("Snippet is %d chars, well over the requested width: %q", len(got), got)
	}
}

// A node that matched on its title or tags has nothing to add in a snippet,
// and the caller already shows both.
func TestSnippetIsEmptyWhenTheBodyDoesNotMatch(t *testing.T) {
	n := node("01A", "parser design", "unrelated body text", "design")

	if got := Snippet(n, "parser", 60); got != "" {
		t.Fatalf("Snippet = %q, want empty", got)
	}
	if got := Snippet(node("01B", "titled", ""), "titled", 60); got != "" {
		t.Fatalf("Snippet of an empty body = %q", got)
	}
}

func TestSnippetCollapsesWhitespace(t *testing.T) {
	n := node("01A", "note", "line one\n\n   line two with target here\n\nline three")

	got := Snippet(n, "target", 60)
	if strings.Contains(got, "\n") || strings.Contains(got, "  ") {
		t.Fatalf("Snippet = %q, want whitespace collapsed", got)
	}
}

func benchCorpus(n int) []*state.Node {
	words := strings.Fields("the parser lexer grammar token ambiguity rewrite timeline design " +
		"benchmark allocation latency throughput cache index query replay event log merge")

	out := make([]*state.Node, n)
	for i := range out {
		var body strings.Builder
		for j := 0; j < 80; j++ {
			body.WriteString(words[(i+j)%len(words)])
			body.WriteByte(' ')
		}
		out[i] = node(
			string(rune('A'+i%26))+string(rune('0'+i%10)),
			words[i%len(words)]+" note "+words[(i+3)%len(words)],
			body.String(),
			words[(i+5)%len(words)],
		)
	}
	return out
}

func BenchmarkBuild(b *testing.B) {
	nodes := benchCorpus(5000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Build(nodes)
	}
}

func BenchmarkSearch(b *testing.B) {
	ix := Build(benchCorpus(5000))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ix.Search("parser lexer", 20)
	}
}
