package search

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/shakfu/gwiki/internal/state"
)

func node(id, title, body string, tags ...string) *state.Node {
	return &state.Node{ID: id, Kind: state.KindNote, Title: title, Body: body, Tags: tags}
}

func TestQueryQuotesWordsAndPrefixesTheLast(t *testing.T) {
	for in, want := range map[string]string{
		"lexer":               `("lexer" OR "lexer"*)`,
		"Parser LEX":          `"parser" AND ("lex" OR "lex"*)`,
		`c++ "NEAR" foo-bar*`: `"c" AND "near" AND "foo" AND ("bar" OR "bar"*)`,
		"don't":               `("don't" OR "don't"*)`,
		"  !!! ":              "",
	} {
		if got := Query(in); got != want {
			t.Errorf("Query(%q) = %s, want %s", in, got, want)
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

// Lowercasing can change a string's byte length, so offsets found in the
// lowercased body must not cut the original through a character.
func TestSnippetNeverSplitsACharacter(t *testing.T) {
	body := strings.Repeat("İ", 40) + " needle " + strings.Repeat("K", 40)
	n := &state.Node{Body: body}
	for width := 5; width < 60; width++ {
		if got := Snippet(n, "needle", width); !utf8.ValidString(got) {
			t.Fatalf("width %d: snippet is not valid UTF-8: %q", width, got)
		}
	}
}
