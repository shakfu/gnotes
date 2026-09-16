package wiki

import (
	"strings"
	"testing"
)

// linkStatus is where a page's only link resolves, and its status.
func linkStatus(t *testing.T, w *Wiki, page string) (string, string) {
	t.Helper()
	p, err := w.Page(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Links) != 1 {
		t.Fatalf("%s has %d links: %+v", page, len(p.Links), p.Links)
	}
	return p.Links[0].Resolved, p.Links[0].Status
}

// A directory's README is its page: titled, found and linked by the directory.
func TestReadmeIsTheDirectorysPage(t *testing.T) {
	w, root := emptyWiki(t)
	put(t, w, root, map[string]string{
		"by-wiki":               "[[architecture]]\n",
		"by-name":               "[[tui]]\n",
		"by-dir":                "[section](architecture/)\n",
		"by-dir-no-slash":       "[section](architecture/tui)\n",
		"architecture/tui/keys": "# Keys\n\nUp to [[architecture/tui]].\n",
	})

	// Before the READMEs exist, the links are broken, and the directory link is
	// a missing page rather than a directory.
	for page, want := range map[string]string{"by-wiki": "", "by-dir": "architecture/README"} {
		if got, status := linkStatus(t, w, page); got != want || status != StatusMissingPage {
			t.Errorf("%s before: %q %s", page, got, status)
		}
	}

	put(t, w, root, map[string]string{
		"architecture/README":     "# Architecture\n",
		"architecture/tui/README": "Untitled.\n",
	})
	for page, want := range map[string]string{
		"by-wiki":               "architecture/README",
		"by-name":               "architecture/tui/README",
		"by-dir":                "architecture/README",
		"by-dir-no-slash":       "architecture/tui/README",
		"architecture/tui/keys": "architecture/tui/README",
	} {
		if got, status := linkStatus(t, w, page); got != want || status != StatusOK {
			t.Errorf("%s: %q %s, want %q", page, got, status, want)
		}
	}

	// A README without a title takes the directory's name, and Find reaches
	// it by the directory's path.
	info, err := w.Find("architecture/tui")
	if err != nil || info.Path != "architecture/tui/README" || info.Title != "tui" {
		t.Fatalf("Find: %+v, %v", info, err)
	}

	// A page beside the directory with the same path makes [[architecture]]
	// ambiguous rather than picking one.
	put(t, w, root, map[string]string{"architecture": "# Also architecture\n"})
	if _, status := linkStatus(t, w, "by-wiki"); status != StatusAmbiguous {
		t.Errorf("[[architecture]] beside architecture.md: %s", status)
	}

	// A directory of something other than pages stays a directory.
	write(t, pageFile(root, "images/diagram")[:len(pageFile(root, "images/diagram"))-3]+".png", "png")
	put(t, w, root, map[string]string{"by-images": "[pictures](images/)\n"})
	if _, status := linkStatus(t, w, "by-images"); status != StatusOK {
		t.Errorf("a link to a directory of images: %s", status)
	}
	if p, _ := w.Page("by-images"); p.Links[0].Kind != KindFile {
		t.Errorf("a link to a directory of images is a %s", p.Links[0].Kind)
	}
}

// Moving a README rewrites links in their form: [[dir]] by the new directory,
// and a directory link as a directory.
func TestMoveReadmeKeepsDirectoryLinks(t *testing.T) {
	w, root := emptyWiki(t)
	put(t, w, root, map[string]string{
		"architecture/README": "# How it fits together\n",
		"a":                   "[[architecture]]\n",
		"b":                   "[section](architecture/)\n",
	})
	if _, err := w.Move("architecture/README", "design/README"); err != nil {
		t.Fatal(err)
	}
	if got := source(t, root, "a"); !strings.Contains(got, "[[design|architecture]]") {
		t.Errorf("wiki link after the move: %q", got)
	}
	if got := source(t, root, "b"); !strings.Contains(got, "[section](design/)") {
		t.Errorf("directory link after the move: %q", got)
	}
	for _, page := range []string{"a", "b"} {
		if got, status := linkStatus(t, w, page); got != "design/README" || status != StatusOK {
			t.Errorf("%s after the move: %q %s", page, got, status)
		}
	}
}
