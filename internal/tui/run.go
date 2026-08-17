package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shakfu/gnotes/internal/session"
)

// Run opens the interactive interface and returns when the user leaves.
//
// Any events still staged when the program exits are committed. Every action
// already commits as it goes, so this only catches an unusual exit path, but
// losing a note to a stray ctrl-c would be the worst possible failure for a
// notes tool.
func Run(s *session.Session) error {
	m := New(s)

	program := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("interface: %w", err)
	}

	if err := s.Commit(); err != nil {
		return fmt.Errorf("saving on exit: %w", err)
	}
	return nil
}
