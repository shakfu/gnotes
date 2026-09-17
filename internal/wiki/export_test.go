package wiki

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readExport(t *testing.T, dir, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestExport(t *testing.T) {
	w, root := emptyWiki(t)
	write(t, filepath.Join(root, "src", "lexer.go"), numbered(5))
	write(t, filepath.Join(root, DirName, PagesDir, "img", "flow.png"), "png")
	write(t, filepath.Join(root, DirName, PagesDir, ".hidden", "x.md"), "# Hidden\n")
	index := "# Top\n\n" +
		"- [[Design sketch]], [[lexer/design-sketch|the sketch]] and [[Design sketch#Token Kinds]]\n" +
		"- [[Lexer]], [[My page]] and [[#Top]]\n" +
		"- [[My page|x] y \\z]]\n" +
		"- [[Nowhere]] and [[Dup]]\n" +
		"- `[[Design sketch]]` in code\n" +
		"- [code](../../src/lexer.go#L2-L3), [dir](../../src/), [rooted](/src/lexer.go) and ![flow](img/flow.png)\n" +
		"- [ref][r] and [again][r]\n\n" +
		"[r]: ../../src/lexer.go#L4\n"
	put(t, w, root, map[string]string{
		"index":               index,
		"lexer/design-sketch": "---\ntitle: Design sketch\n---\n\n## Token Kinds\n\n[[index]] and [code](../../../src/lexer.go#L1)\n",
		"lexer/README":        "# Lexer\n",
		"notes/My page":       "# My page\n",
		"a/dup":               "# Dup\n",
		"b/dup":               "# Dup\n",
	})

	// Inside the repository, links into the code stay relative, from one
	// directory fewer than the pages.
	out := filepath.Join(root, "site")
	res, err := w.Export(out)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pages != 6 || res.Files != 1 || res.Links != 12 || len(res.Removed) != 0 {
		t.Fatalf("result = %+v", res)
	}
	var skipped []string
	for _, l := range res.Skipped {
		skipped = append(skipped, l.Written()+" "+l.Status)
	}
	if got := strings.Join(skipped, ", "); got != "[[Nowhere]] missing-page, [[Dup]] ambiguous" {
		t.Fatalf("skipped = %s", got)
	}
	want := "# Top\n\n" +
		"- [Design sketch](lexer/design-sketch.md), [the sketch](lexer/design-sketch.md) and [Design sketch#Token Kinds](lexer/design-sketch.md#token-kinds)\n" +
		"- [Lexer](lexer/README.md), [My page](notes/My%20page.md) and [#Top](#top)\n" +
		"- [x\\] y \\\\z](notes/My%20page.md)\n" +
		"- [[Nowhere]] and [[Dup]]\n" +
		"- `[[Design sketch]]` in code\n" +
		"- [code](../src/lexer.go#L2-L3), [dir](../src/), [rooted](/src/lexer.go) and ![flow](img/flow.png)\n" +
		"- [ref][r] and [again][r]\n\n" +
		"[r]: ../src/lexer.go#L4\n"
	if got := readExport(t, out, "index.md"); got != want {
		t.Fatalf("index.md:\n%s\nwant:\n%s", got, want)
	}
	if got := readExport(t, out, "lexer/design-sketch.md"); got != "---\ntitle: Design sketch\n---\n\n## Token Kinds\n\n[index](../index.md) and [code](../../src/lexer.go#L1)\n" {
		t.Fatalf("lexer/design-sketch.md:\n%s", got)
	}
	if got := readExport(t, out, "img/flow.png"); got != "png" {
		t.Fatalf("flow.png = %q", got)
	}
	if _, err := os.Stat(filepath.Join(out, ".hidden")); !os.IsNotExist(err) {
		t.Fatalf("a hidden directory was exported: %v", err)
	}
	if got := source(t, root, "index"); got != index {
		t.Fatalf("the page itself changed:\n%s", got)
	}

	// A second export removes what it no longer writes, and nothing else.
	write(t, filepath.Join(out, "CNAME"), "wiki.example.com\n")
	if err := os.Remove(pageFile(root, "a/dup")); err != nil {
		t.Fatal(err)
	}
	if res, err = w.Export(out); err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Removed, ",") != "a/dup.md" {
		t.Fatalf("removed = %v", res.Removed)
	}
	if _, err := os.Stat(filepath.Join(out, "a", "dup.md")); !os.IsNotExist(err) {
		t.Fatalf("a deleted page is still exported: %v", err)
	}
	if got := readExport(t, out, "CNAME"); got != "wiki.example.com\n" {
		t.Fatalf("CNAME = %q", got)
	}
	// [[Dup]] resolves once one of the two pages is gone.
	if !strings.Contains(readExport(t, out, "index.md"), "- [[Nowhere]] and [Dup](b/dup.md)\n") {
		t.Fatalf("index.md after the second export:\n%s", readExport(t, out, "index.md"))
	}
}

// Outside the repository, a relative link into the code is rooted at the
// repository instead, since no relative path can reach it.
func TestExportOutsideTheRepository(t *testing.T) {
	w, root := emptyWiki(t)
	write(t, filepath.Join(root, "src", "lexer.go"), numbered(5))
	put(t, w, root, map[string]string{"lexer/p": "[code](../../../src/lexer.go#L2) and [[q]]\n", "lexer/q": "# Q\n"})
	out := t.TempDir()
	if _, err := w.Export(out); err != nil {
		t.Fatal(err)
	}
	if got := readExport(t, out, "lexer/p.md"); got != "[code](/src/lexer.go#L2) and [q](q.md)\n" {
		t.Fatalf("lexer/p.md = %q", got)
	}
}

func TestExportRefusesADirectoryItDidNotWrite(t *testing.T) {
	w, root := emptyWiki(t)
	put(t, w, root, map[string]string{"p": "# P\n"})

	busy := t.TempDir()
	write(t, filepath.Join(busy, "mine.txt"), "keep\n")
	if _, err := w.Export(busy); !errors.Is(err, ErrExportDir) {
		t.Fatalf("export to a directory with other files: %v", err)
	}
	if got := readExport(t, busy, "mine.txt"); got != "keep\n" {
		t.Fatalf("mine.txt = %q", got)
	}

	for _, dir := range []string{w.PagesPath(), filepath.Join(w.PagesPath(), "sub"), root} {
		if _, err := w.Export(dir); err == nil {
			t.Errorf("exported into %s, which holds or is inside the pages", dir)
		}
	}

	tampered := t.TempDir()
	write(t, filepath.Join(tampered, ExportManifest), "../outside.txt\n")
	if _, err := w.Export(tampered); err == nil || !strings.Contains(err.Error(), "outside the directory") {
		t.Fatalf("a manifest naming a path outside the directory: %v", err)
	}
}
