package search

import (
	"strings"
	"testing"
)

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
