package tui

import (
	"strings"
	"testing"
)

// helpNames maps the help's spelling of a key to tea's.
var helpNames = map[string]string{"ctrl-p": "ctrl+p", "ctrl-d": "ctrl+d", "ctrl-u": "ctrl+u", "space": " ", "shift-tab": "shift+tab"}

// Every key the help lists is bound in its keymap, and every bound key is
// listed, apart from the arrow and paging aliases.
func TestKeymapsMatchTheHelp(t *testing.T) {
	aliases := map[string]bool{"down": true, "up": true, "left": true, "right": true, "home": true, "end": true, "pgdown": true, "pgup": true}
	for _, km := range allKeymaps {
		if km == keysContent {
			continue
		}
		shown := map[string]bool{}
		for _, b := range km.bindings {
			for _, name := range strings.Fields(b.show) {
				if tea, ok := helpNames[name]; ok {
					name = tea
				}
				shown[name] = true
				if name != "ctrl-c" && km.find(name) == nil {
					t.Errorf("%s: the help lists %q, which is not bound", km.title, name)
				}
			}
		}
		for _, b := range km.bindings {
			if b.run == nil {
				t.Errorf("%s: %v does nothing", km.title, b.keys)
			}
			for _, k := range b.keys {
				if !aliases[k] && !shown[k] {
					t.Errorf("%s: %q is bound but not in the help", km.title, k)
				}
			}
		}
	}
}

// The help scrolls to its last section on a short terminal, starts with the
// keys of the screen it was opened from, and returns there.
func TestWikiHelpScrollsAndReturns(t *testing.T) {
	f := newWikiFixture(t)
	f.m.width, f.m.height = 80, 12
	f.press("t", "?")
	if f.m.screen != screenHelp {
		t.Fatalf("? opened screen %d", f.m.screen)
	}
	lines := strings.Split(f.view(), "\n")
	if len(lines) != 12 || !strings.Contains(lines[11], "j k scroll") {
		t.Fatalf("help footer:\n%s", f.view())
	}
	if lines[1] != " tasks" {
		t.Errorf("help opened from tasks starts with %q", lines[1])
	}
	help := f.m.helpLines()
	last := stripANSI(help[len(help)-1])
	f.press("G")
	if v := f.view(); !strings.Contains(v, last) {
		t.Errorf("G did not reach the last line, %q:\n%s", last, v)
	}
	f.press("k")
	if v := f.view(); strings.Contains(v, last) {
		t.Errorf("k did not scroll up:\n%s", v)
	}
	f.press("x")
	if f.m.screen != screenTasks {
		t.Errorf("closing the help went to screen %d, want the tasks", f.m.screen)
	}
}

// The help hint survives on a narrow terminal, on every screen with hints.
func TestWikiHintsKeepTheHelpKey(t *testing.T) {
	f := newWikiFixture(t)
	screens := map[string]func(){
		"tree":   func() { f.m.screen, f.m.focus = screenRead, focusTree },
		"panel":  func() { f.m.screen, f.m.focus = screenRead, focusPanel },
		"latest": func() { f.m.screen = screenLatest },
		"stats":  func() { f.m.screen = screenStats },
		"broken": func() { f.m.screen = screenBroken },
		"tasks":  func() { f.m.screen = screenTasks },
	}
	for _, width := range []int{40, 80, 120} {
		f.m.width = width
		for name, set := range screens {
			set()
			f.m.status = ""
			line := stripANSI(f.m.viewWikiStatus())
			if !strings.HasSuffix(line, helpHint+" ") || len([]rune(line)) != width {
				t.Errorf("%s at %d: hints %q", name, width, line)
			}
		}
	}
}

// r moves a page in the reader and does nothing on the overview; R reloads.
func TestWikiReloadKey(t *testing.T) {
	f := newWikiFixture(t)
	f.press("O", "r")
	if f.m.prompt != nil {
		t.Fatalf("r on the overview opened %q", f.m.prompt.label)
	}
	f.press("R")
	if f.m.status != "reloaded" {
		t.Errorf("R: status %q", f.m.status)
	}
}

// Every command is in the help under the name it runs by.
func TestCommandsAreInTheHelp(t *testing.T) {
	f := newWikiFixture(t)
	help := stripANSI(strings.Join(f.m.helpLines(), "\n"))
	for _, c := range wikiCommands {
		for _, name := range c.names {
			if findCommand(name) != &wikiCommands[indexOfCommand(c.names[0])] {
				t.Errorf(":%s does not find its command", name)
			}
			if !strings.Contains(help, ":"+name) {
				t.Errorf("the help lacks :%s", name)
			}
		}
	}
}

func indexOfCommand(name string) int {
	for i, c := range wikiCommands {
		if c.names[0] == name {
			return i
		}
	}
	return -1
}

// The ':' line runs commands from the tree, the lists and the overview, and
// vim's :w and :q on the open page.
func TestWikiCommandLine(t *testing.T) {
	f := newWikiFixture(t)
	f.m.screen, f.m.focus = screenRead, focusTree

	f.press(":", "tasks", "enter")
	if f.m.screen != screenTasks {
		t.Fatalf(":tasks from the tree reached screen %d", f.m.screen)
	}
	f.press(":", "search tokeniz", "enter")
	if f.m.screen != screenSearch || len(f.m.hits) != 1 {
		t.Fatalf(":search reached screen %d with %d hits", f.m.screen, len(f.m.hits))
	}
	// :overview returns to the tab last shown, tasks.
	f.press("esc", ":", "overview", "enter")
	if f.m.screen != screenTasks {
		t.Fatalf(":overview reached screen %d", f.m.screen)
	}
	f.press(":", "nonsense", "enter")
	if !f.m.statusErr || !strings.Contains(f.m.status, "not a command: nonsense") {
		t.Fatalf("an unknown command: %q", f.m.status)
	}

	// :w writes the page's buffer; :q refuses while it is modified, then quits.
	f.m.screen, f.m.focus = screenRead, focusContent
	f.press("x", "tab", "tab")
	if f.m.focus != focusTree {
		t.Fatalf("focus %d", f.m.focus)
	}
	f.press(":", "q", "enter")
	if f.m.quitting || !strings.Contains(f.m.status, "unsaved changes") {
		t.Fatalf(":q with changes: %q", f.m.status)
	}
	f.press(":", "w", "enter")
	if f.m.edit.ed.Dirty || !strings.HasPrefix(f.source("index"), " Home") {
		t.Fatalf(":w from the tree: %q", f.source("index"))
	}
	f.press(":", "q", "enter")
	if !f.m.quitting {
		t.Fatal(":q did not quit")
	}
}
