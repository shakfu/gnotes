package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shakfu/gwiki/internal/markdown"
	"github.com/shakfu/gwiki/internal/vim"
	"github.com/shakfu/gwiki/internal/wiki"
)

// editing is the open page's buffer: the page it holds, the hash it was read
// at, and the completion list when one is open.
type editing struct {
	ed      *vim.Editor
	page    string
	base    string // the hash the buffer was read from
	preview bool

	// outside marks a page changed on disk under a modified buffer.
	outside bool

	// broken are the links in the buffer whose target is missing, as written,
	// refreshed when the page is opened, saved or reloaded.
	broken map[string]bool

	complete struct {
		active bool
		at     vim.Pos // where the replaced text starts
		items  []string
		index  int
	}

	// draftPending marks changes the draft file does not hold yet; the poll
	// writes it, so a burst of typing costs one write a second.
	draftPending bool

	// links are the buffer's links when it held linksText; see bufferLinks.
	links     []wiki.Link
	linksText string
}

// draftsDir holds a buffer's autosaved text, so an interrupted edit survives.
func (m *WikiModel) draftsDir() string {
	return filepath.Join(m.w.Root, wiki.DirName, "drafts")
}

func draftName(page string) string {
	sum := sha256.Sum256([]byte(page))
	return hex.EncodeToString(sum[:8]) + ".md"
}

// openBuffer puts a page's source in the buffer.
func (m *WikiModel) openBuffer(page string, src []byte, hash string) {
	ed := vim.New(string(src))
	ed.Width, ed.Height = m.bufferWidth(), m.contentHeight()
	m.edit = &editing{ed: ed, page: page, base: hash}
	ed.Hooks = vim.Hooks{
		Save:   m.editSave,
		Quit:   m.editQuit,
		Reload: m.editReload,
		Follow: func() error {
			l, ok := m.linkAtCursor()
			if !ok {
				return errors.New("no link under the cursor")
			}
			return m.follow(l)
		},
		Back:    m.back,
		Command: m.editCommand,
		Changed: m.editChanged,
		Complete: func(previous bool) {
			m.editComplete(previous)
		},
		Clipboard: func(text string) { m.copy(text) },
	}
	m.checkLinks()
	m.offerDraft()
}

// offerDraft asks about a draft left by an interrupted edit.
func (m *WikiModel) offerDraft() {
	raw, err := os.ReadFile(filepath.Join(m.draftsDir(), draftName(m.edit.page)))
	if err != nil {
		return
	}
	base, text, ok := strings.Cut(string(raw), "\n")
	if !ok || text == m.edit.ed.Text() {
		return
	}
	stale := ""
	if base != m.edit.base {
		stale = "; the page has changed since"
	}
	m.askWiki(fmt.Sprintf("a draft of %s was not saved%s; restore it? y/n: ", m.edit.page, stale), "", func(answer string) error {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y") {
			m.edit.ed.Load(text)
			m.edit.ed.Dirty = true
			m.setStatus("draft restored; :w writes it")
			return nil
		}
		m.dropDraft()
		return nil
	})
}

func (m *WikiModel) dropDraft() {
	os.Remove(filepath.Join(m.draftsDir(), draftName(m.edit.page)))
}

// editChanged notes that the draft is behind the buffer.
func (m *WikiModel) editChanged() { m.edit.draftPending = true }

// writeDraft saves the buffer beside the wiki, so an interrupted edit is not
// lost. It runs on the poll rather than on every keystroke.
func (m *WikiModel) writeDraft() {
	e := m.edit
	if e == nil || !e.draftPending {
		return
	}
	e.draftPending = false
	if err := os.MkdirAll(m.draftsDir(), 0o755); err != nil {
		return
	}
	os.WriteFile(filepath.Join(m.draftsDir(), draftName(e.page)), []byte(e.base+"\n"+e.ed.Text()), 0o644)
}

// editSave writes the buffer to the page. force writes over a page that
// changed on disk.
func (m *WikiModel) editSave(force bool) error {
	e := m.edit
	text := e.ed.Text()
	base := e.base
	if force {
		if _, hash, err := m.w.Read(e.page); err == nil {
			base = hash
		}
	}
	var conflict *wiki.ErrConflict
	switch err := m.w.Write(e.page, []byte(text), base); {
	case errors.As(err, &conflict):
		return errors.New(e.page + " changed on disk; :w! overwrites it, :e! loads it and loses your edits")
	case err != nil:
		return err
	}
	e.base = wiki.Hash([]byte(text))
	e.outside = false
	m.dropDraft()
	m.afterWrite()
	m.checkLinks()
	return nil
}

// editQuit leaves gwiki, as :q leaves vim.
func (m *WikiModel) editQuit(force bool) error {
	if m.edit.ed.Dirty && !force {
		return errors.New("the page has unsaved changes; :w writes them, :q! discards them")
	}
	if m.edit.ed.Dirty {
		m.dropDraft()
	}
	m.quitting = true
	return nil
}

func (m *WikiModel) editReload() error {
	src, hash, err := m.w.Read(m.edit.page)
	if err != nil {
		return err
	}
	m.edit.ed.Load(string(src))
	m.edit.base, m.edit.outside = hash, false
	m.dropDraft()
	m.checkLinks()
	return nil
}

// checkLinks marks the buffer's broken links, for the editor to underline.
func (m *WikiModel) checkLinks() {
	e := m.edit
	if e == nil {
		return
	}
	e.broken = map[string]bool{}
	snap, err := m.w.Snapshot()
	if err != nil {
		return
	}
	for _, l := range snap.Links(e.page, []byte(e.ed.Text())) {
		if l.Status != wiki.StatusOK && l.Form == "wiki" {
			e.broken["[["+l.Target+"]]"] = true
			e.broken["[["+l.Written()[2:len(l.Written())-2]+"]]"] = true
		}
	}
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// ---------------------------------------------------------------- completion

// editComplete offers page titles after "[[", headings after "#", and paths
// after "](", replacing what has been typed.
func (m *WikiModel) editComplete(previous bool) {
	e := m.edit
	c := &e.complete
	if c.active {
		// Cycle through the list, replacing the last insertion.
		step := 1
		if previous {
			step = -1
		}
		c.index = (c.index + step + len(c.items)) % len(c.items)
		m.insertCompletion()
		return
	}
	cur := e.ed.Cursor
	before := string(e.ed.Buf.Line(cur.Line)[:cur.Col])

	items, at := m.completionsFor(before, cur)
	if len(items) == 0 {
		e.ed.Message, e.ed.Err = "nothing to complete", true
		return
	}
	c.active, c.at, c.items, c.index = true, at, items, 0
	m.insertCompletion()
}

// completionsFor returns what could follow the text before the cursor, and
// where the replacement starts.
func (m *WikiModel) completionsFor(before string, cur vim.Pos) ([]string, vim.Pos) {
	if i := strings.LastIndex(before, "[["); i >= 0 && !strings.Contains(before[i:], "]]") {
		partial := before[i+2:]
		if target, anchor, found := strings.Cut(partial, "#"); found {
			page := m.edit.page
			if strings.TrimSpace(target) != "" {
				snap, err := m.w.Snapshot()
				if err != nil {
					return nil, cur
				}
				got, status := snap.WikiTarget(target)
				if status != wiki.StatusOK {
					return nil, cur
				}
				page = got
			}
			return m.headingNames(page, anchor, false), vim.Pos{Line: cur.Line, Col: cur.Col - len([]rune(anchor))}
		}
		var out []string
		for _, p := range m.pages {
			if matchesPartial(p.Title+" "+p.Path, partial) {
				out = append(out, p.Title)
			}
		}
		return out, vim.Pos{Line: cur.Line, Col: cur.Col - len([]rune(partial))}
	}

	if i := strings.LastIndex(before, "]("); i >= 0 && !strings.ContainsAny(before[i:], ") ") {
		partial := before[i+2:]
		if dest, anchor, found := strings.Cut(partial, "#"); found {
			page := m.edit.page
			if dest != "" {
				file := filepath.Join(m.w.PagesPath(), filepath.FromSlash(path.Dir(m.edit.page)), filepath.FromSlash(dest))
				got, ok := m.w.PageOf(file)
				if !ok {
					return nil, cur
				}
				page = got
			}
			return m.headingNames(page, anchor, true), vim.Pos{Line: cur.Line, Col: cur.Col - len([]rune(anchor))}
		}
		dir, base := "", partial
		if i := strings.LastIndexByte(partial, '/'); i >= 0 {
			dir, base = partial[:i+1], partial[i+1:]
		}
		entries, err := os.ReadDir(filepath.Join(m.w.PagesPath(), filepath.FromSlash(path.Dir(m.edit.page)), filepath.FromSlash(dir)))
		if err != nil {
			return nil, cur
		}
		var out []string
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") || !strings.HasPrefix(strings.ToLower(name), strings.ToLower(base)) {
				continue
			}
			if entry.IsDir() {
				name += "/"
			}
			out = append(out, name)
		}
		return out, vim.Pos{Line: cur.Line, Col: cur.Col - len([]rune(base))}
	}
	return nil, cur
}

func (m *WikiModel) headingNames(page, partial string, slug bool) []string {
	var heads []markdown.Heading
	if page == m.edit.page {
		heads = markdown.Parse([]byte(m.edit.ed.Text())).Headings
	} else if src, _, err := m.w.Read(page); err == nil {
		heads = markdown.Parse(src).Headings
	}
	var out []string
	for _, h := range heads {
		name := h.Text
		if slug {
			name = h.Slug
		}
		if matchesPartial(h.Text+" "+h.Slug, partial) {
			out = append(out, name)
		}
	}
	return out
}

func matchesPartial(text, partial string) bool {
	return partial == "" || subsequence(strings.ToLower(text), strings.ToLower(partial))
}

// insertCompletion replaces the typed text with the chosen item.
func (m *WikiModel) insertCompletion() {
	e := m.edit
	c := &e.complete
	item := c.items[c.index]
	e.ed.Buf.Replace(c.at, e.ed.Cursor, item)
	e.ed.SetCursor(vim.Pos{Line: c.at.Line, Col: c.at.Col + len([]rune(item))})
	e.ed.Dirty = true
	e.ed.Message = fmt.Sprintf("%d of %d: ctrl-n and ctrl-p cycle", c.index+1, len(c.items))
}

// ---------------------------------------------------------------- keys

// keyContent sends a key to the page's buffer. In NORMAL mode, with no
// command half typed, the wiki takes a few keys first: tab and shift-tab move
// between panes, < and > jump between links, [ and ] move half a screen,
// enter follows the link under the cursor, and ctrl-p opens a page.
func (m *WikiModel) keyContent(msg tea.KeyMsg) {
	k := msg.String()
	e := m.edit
	m.status = ""
	if e.ed.Mode == vim.Normal && e.ed.Pending() == "" && !e.preview {
		switch k {
		case "tab":
			m.cycleFocus(1)
			return
		case "shift+tab":
			m.cycleFocus(-1)
			return
		case "ctrl+p":
			m.startOpen()
			return
		case "<", ">":
			by := 1
			if k == "<" {
				by = -1
			}
			if err := m.jumpLink(by); err != nil {
				e.ed.Message, e.ed.Err = err.Error(), true
			}
			return
		case "[":
			k = "ctrl+u"
		case "]":
			k = "ctrl+d"
		case "enter":
			if l, ok := m.linkAtCursor(); ok {
				if err := m.follow(l); err != nil {
					e.ed.Message, e.ed.Err = err.Error(), true
				}
				return
			}
			// Elsewhere, as vim's enter: the first character of the next line.
			e.ed.Key("j")
			k = "^"
		}
	}
	// Any key but the cycling ones ends a completion.
	if e.complete.active && k != "ctrl+n" && k != "ctrl+p" {
		e.complete.active = false
	}
	if e.preview && e.ed.Mode == vim.Normal {
		switch k {
		case "esc", "q", ":":
			e.preview = false
			if k != ":" {
				return
			}
		case "j", "k", "ctrl+d", "ctrl+u", "g", "G":
		default:
			return
		}
	}
	e.ed.Width, e.ed.Height = m.bufferWidth(), m.contentHeight()
	// Fast typing and pastes arrive as one message holding several runes.
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 {
		for _, r := range msg.Runes {
			e.ed.Key(string(r))
		}
	} else {
		e.ed.Key(k)
	}
	// Undoing back to the text as read leaves nothing unsaved, which the
	// editor does not track.
	if e.ed.Dirty && m.edit == e && wiki.Hash([]byte(e.ed.Text())) == e.base {
		e.ed.Dirty = false
	}
}

// bufferWidth is the text beside the line numbers in the page pane.
func (m *WikiModel) bufferWidth() int { return max(10, m.contentWidth()-gutterWidth) }

// gutterWidth is the line numbers and the space after them.
const gutterWidth = 5
