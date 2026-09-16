package wiki

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// A directory's own page is its README.md, the file GitHub shows when the
// directory is browsed. [[lexer]] and a markdown link to lexer/ reach
// lexer/README; the page is titled and found by the directory's name.

// isReadme reports whether a page is a directory's own page. The wiki's root
// has index for that, so a README there is an ordinary page.
func isReadme(page string) bool {
	return strings.Contains(page, "/") && strings.EqualFold(path.Base(page), "readme")
}

// ReadmeDir is the directory a page is the README of, or "" when it is not one.
func ReadmeDir(page string) string {
	if !isReadme(page) {
		return ""
	}
	return path.Dir(page)
}

// pageName is the name a page is found by: its file name, or its directory's
// for a README.
func pageName(page string) string {
	if isReadme(page) {
		return path.Base(path.Dir(page))
	}
	return path.Base(page)
}

// pageStem is a page's name as the index keys it.
func pageStem(page string) string { return strings.ToLower(pageName(page)) }

// sectionReadme is the README file name of a directory holding pages, at any
// depth: README.md as its file is spelled, or README.md when it has none yet.
// ok is false for a directory without pages, such as one of images.
func sectionReadme(dir string) (name string, ok bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(e.Name(), "readme.md") {
			return e.Name(), true
		}
	}
	errFound := errors.New("found")
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return errFound
		}
		return nil
	})
	return "README.md", err == errFound
}
