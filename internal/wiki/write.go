package wiki

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/shakfu/gnotes/internal/markdown"
	"gopkg.in/yaml.v3"
)

// Hash is the content hash writes are checked against.
func Hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ErrConflict reports that a page changed after the writer read it. Nothing
// was written.
type ErrConflict struct {
	Page string

	// Current is the page's content now, and CurrentHash its hash, for the
	// writer to retry against. Both are empty when the page no longer exists.
	Current     []byte
	CurrentHash string
}

func (e *ErrConflict) Error() string {
	if e.CurrentHash == "" {
		return fmt.Sprintf("%s was removed after it was read; nothing was written", e.Page)
	}
	return fmt.Sprintf("%s changed after it was read; nothing was written", e.Page)
}

// ErrExists reports a page that already exists where a new one would go.
var ErrExists = errors.New("a page already exists there")

// fileWrite is one change in a batch: new content for a page, or its removal
// when Data is nil. Base is the hash the writer read, or empty for a page that
// must not exist yet.
type fileWrite struct {
	Page string
	Base string
	Data []byte
}

// Read returns a page's source and its hash.
func (w *Wiki) Read(page string) ([]byte, string, error) {
	src, err := os.ReadFile(w.file(page))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", fmt.Errorf("no page %q", page)
		}
		return nil, "", err
	}
	return src, Hash(src), nil
}

func (w *Wiki) file(page string) string {
	return filepath.Join(w.PagesPath(), filepath.FromSlash(page)+".md")
}

// commit applies a batch of writes, then refreshes the cache.
//
// Every base is checked before anything is written, under the cache's write
// lock, so gnotes processes writing at once take turns and a batch never lands
// half-checked. Each page is written to a temporary file, synced and renamed
// over the old one, so an interrupted write leaves the old page. An editor
// outside gnotes takes no lock; the checks still refuse to overwrite what it
// saved first.
func (w *Wiki) commit(writes []fileWrite) error {
	tx, err := w.db.Begin()
	if err != nil {
		return fmt.Errorf("lock %s: %w", w.Cache(), err)
	}
	defer tx.Rollback()

	for _, fw := range writes {
		current, err := os.ReadFile(w.file(fw.Page))
		switch {
		case os.IsNotExist(err):
			if fw.Base != "" {
				return &ErrConflict{Page: fw.Page}
			}
		case err != nil:
			return err
		case fw.Base == "":
			return fmt.Errorf("%w: %s", ErrExists, fw.Page)
		case Hash(current) != fw.Base:
			return &ErrConflict{Page: fw.Page, Current: current, CurrentHash: Hash(current)}
		}
	}

	for _, fw := range writes {
		if fw.Data == nil {
			if err := os.Remove(w.file(fw.Page)); err != nil {
				return err
			}
			continue
		}
		if err := writeAtomic(w.file(fw.Page), fw.Data); err != nil {
			return err
		}
	}
	tx.Rollback()
	_, err = w.Refresh()
	return err
}

// writeAtomic replaces path with data through a synced temporary file in the
// same directory. The temporary name is hidden, so a scan never reads it.
func writeAtomic(target string, data []byte) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(target)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(f.Name(), mode); err != nil {
		return err
	}
	return os.Rename(f.Name(), target)
}

// CleanPath validates a page path typed by a person or agent and returns it
// relative to the pages directory, slash-separated, without .md.
func CleanPath(p string) (string, error) {
	p = strings.TrimSuffix(strings.TrimSpace(filepath.ToSlash(p)), ".md")
	clean := path.Clean(p)
	switch {
	case p == "" || clean == ".":
		return "", errors.New("a page path is required")
	case path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../"):
		return "", fmt.Errorf("%q is outside the wiki", p)
	}
	for _, part := range strings.Split(clean, "/") {
		if strings.HasPrefix(part, ".") {
			return "", fmt.Errorf("%q has a hidden component, which the wiki skips", p)
		}
		if strings.ContainsFunc(part, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '\\' }) {
			return "", fmt.Errorf("%q contains a control character or backslash", p)
		}
	}
	return clean, nil
}

// Slugify makes a file name from a title: lowercase letters and digits, with
// every run of anything else as one hyphen.
func Slugify(title string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if hyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			hyphen = false
			continue
		}
		hyphen = true
	}
	return b.String()
}

// span is a replacement of src[start:end], whose current text is old.
type span struct {
	start, end int
	old, new   string
}

// applySpans rewrites src. Spans must not overlap; each is checked against the
// text it expects, so a stale offset is an error rather than a corrupt page.
func applySpans(src []byte, spans []span) ([]byte, error) {
	sort.Slice(spans, func(i, j int) bool { return spans[i].start > spans[j].start })
	out := bytes.Clone(src)
	last := len(src) + 1
	for _, s := range spans {
		if s.start < 0 || s.end > len(src) || s.start > s.end || s.end > last {
			return nil, fmt.Errorf("edit at %d-%d does not fit the page", s.start, s.end)
		}
		if string(src[s.start:s.end]) != s.old {
			return nil, fmt.Errorf("expected %q at byte %d, found %q; refresh and retry", s.old, s.start, src[s.start:s.end])
		}
		out = append(out[:s.start], append([]byte(s.new), out[s.end:]...)...)
		last = s.start
	}
	return out, nil
}

// editFront rewrites a page's front matter through a YAML node tree, which
// keeps key order, styles and comments where yaml.v3 can. A page without
// front matter gets a new block.
func editFront(src []byte, edit func(m *yaml.Node) error) ([]byte, error) {
	page := markdown.Parse(src)
	var doc yaml.Node
	start, end := 0, 0
	if page.Front != nil {
		lines := bytes.SplitAfterN(src, []byte("\n"), 2)
		start = len(lines[0])
		end = bytes.LastIndex(src[:page.BodyStart], []byte("\n---"))
		if alt := bytes.LastIndex(src[:page.BodyStart], []byte("\n...")); alt > end {
			end = alt
		}
		end++ // just past the newline before the closing delimiter
		if err := yaml.Unmarshal(src[start:end], &doc); err != nil {
			return nil, err
		}
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	if err := edit(doc.Content[0]); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	enc.Close()
	block := buf.Bytes()
	if len(doc.Content[0].Content) == 0 {
		block = nil
	}

	var out bytes.Buffer
	if page.Front != nil {
		if block == nil {
			// Empty front matter is dropped, delimiters and the blank line
			// that followed them included.
			return bytes.TrimLeft(src[page.BodyStart:], "\n"), nil
		}
		out.Write(src[:start])
		out.Write(block)
		out.Write(src[end:])
		return out.Bytes(), nil
	}
	if block == nil {
		return src, nil
	}
	out.WriteString("---\n")
	out.Write(block)
	out.WriteString("---\n\n")
	out.Write(src)
	return out.Bytes(), nil
}

// mapValue returns the value node for key in a mapping, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setScalar sets key to value in a mapping, adding the key when missing.
func setScalar(m *yaml.Node, key, value string) {
	if v := mapValue(m, key); v != nil {
		v.Kind, v.Tag, v.Value, v.Content = yaml.ScalarNode, "!!str", value, nil
		return
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

// deleteKey removes key from a mapping.
func deleteKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}
