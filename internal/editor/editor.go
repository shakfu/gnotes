// Package editor opens text in the user's editor, for the command line and the
// interactive interface.
package editor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// ErrNoEditor reports that neither $VISUAL nor $EDITOR is set.
var ErrNoEditor = errors.New("no editor configured; set $EDITOR")

// Edit is one editing session: a temporary file holding the text, and the
// command that opens it. The caller runs Cmd, with whatever terminal handling
// it needs, then calls Finish.
type Edit struct {
	Cmd *exec.Cmd

	// Path is the temporary file.
	Path string

	original string
	written  string
}

// Start writes text to a temporary file and prepares the editor command, taken
// from $VISUAL or $EDITOR as getenv reports them.
//
// The command runs through sh, as git runs core.editor, so a setting with
// arguments ("code -w") or a quoted path containing spaces both work. The
// variable is the user's own configuration, not input from the log.
func Start(text string, getenv func(string) string) (*Edit, error) {
	editor := strings.TrimSpace(getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(getenv("EDITOR"))
	}
	if editor == "" {
		return nil, ErrNoEditor
	}

	// A .md suffix so the editor turns on markdown highlighting. CreateTemp
	// makes the file readable by its owner only.
	f, err := os.CreateTemp("", "gnotes-*.md")
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return nil, err
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		fields := strings.Fields(editor)
		cmd = exec.Command(fields[0], append(fields[1:], f.Name())...)
	} else {
		cmd = exec.Command("sh", "-c", editor+` "$@"`, "sh", f.Name())
	}
	return &Edit{Cmd: cmd, Path: f.Name(), original: text, written: text}, nil
}

// Finish reads the edited text back and removes the temporary file.
//
// A file the editor left byte for byte as written returns the original text,
// so saving without a change writes nothing. Otherwise line endings are
// normalised to \n and trailing newlines dropped: editors add a final newline
// that nobody typed.
func (e *Edit) Finish() (string, error) {
	defer e.Cleanup()

	raw, err := os.ReadFile(e.Path)
	if err != nil {
		return "", fmt.Errorf("read the edited text: %w", err)
	}
	if string(raw) == e.written {
		return e.original, nil
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	return strings.TrimRight(text, "\n"), nil
}

// Cleanup removes the temporary file. It is safe to call more than once.
func (e *Edit) Cleanup() { os.Remove(e.Path) }

// Open prepares the editor command for an existing file, at a line when line
// is positive. The line is passed as +N, which vi, vim, nano, emacs, micro and
// kakoune accept.
func Open(path string, line int, getenv func(string) string) (*exec.Cmd, error) {
	editor := strings.TrimSpace(getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(getenv("EDITOR"))
	}
	if editor == "" {
		return nil, ErrNoEditor
	}
	args := []string{path}
	if line > 0 {
		args = []string{fmt.Sprintf("+%d", line), path}
	}
	if runtime.GOOS == "windows" {
		fields := strings.Fields(editor)
		return exec.Command(fields[0], append(fields[1:], args...)...), nil
	}
	return exec.Command("sh", append([]string{"-c", editor + ` "$@"`, "sh"}, args...)...), nil
}
