package editor

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeEditor writes a script that replaces the file's content, in a directory
// whose name contains a space.
func fakeEditor(t *testing.T, content string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "my editor")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "ed.sh")
	body := "#!/bin/sh\nprintf '" + content + "' > \"$1\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return "'" + script + "'"
}

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func run(t *testing.T, text, editor string) string {
	t.Helper()
	e, err := Start(text, env(map[string]string{"EDITOR": editor}))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Cmd.Run(); err != nil {
		t.Fatalf("editor: %v", err)
	}
	out, err := e.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.Path); !os.IsNotExist(err) {
		t.Error("the temporary file was left behind")
	}
	return out
}

func TestAQuotedEditorPathWithASpaceRuns(t *testing.T) {
	if got := run(t, "old", fakeEditor(t, "new text")); got != "new text" {
		t.Fatalf("got %q", got)
	}
}

func TestAnUnchangedFileReturnsTheOriginal(t *testing.T) {
	// "true" leaves the file alone, including the body's own trailing newline.
	if got := run(t, "body\n", "true"); got != "body\n" {
		t.Fatalf("got %q, want the original untouched", got)
	}
}

func TestLineEndingsAndTrailingNewlinesAreNormalised(t *testing.T) {
	if got := run(t, "old", fakeEditor(t, `a\r\nb\r\n\n`)); got != "a\nb" {
		t.Fatalf("got %q", got)
	}
}

func TestVisualWinsOverEditorAndNoneIsAnError(t *testing.T) {
	if _, err := Start("x", env(nil)); err != ErrNoEditor {
		t.Fatalf("Start with no editor = %v", err)
	}
	e, err := Start("x", env(map[string]string{"VISUAL": "true", "EDITOR": "false"}))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Cleanup()
	if err := e.Cmd.Run(); err != nil {
		t.Fatalf("VISUAL was not used: %v", err)
	}
}

func TestOpenPassesTheLine(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	script := filepath.Join(dir, "ed.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" > '"+log+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for line, want := range map[int]string{42: "+42 /some file.go\n", 0: "/some file.go\n"} {
		cmd, err := Open("/some file.go", line, env(map[string]string{"EDITOR": script}))
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(log); string(got) != want {
			t.Errorf("line %d: editor got %q, want %q", line, got, want)
		}
	}
	if _, err := Open("x", 1, env(nil)); err != ErrNoEditor {
		t.Fatalf("Open without an editor = %v", err)
	}
}
