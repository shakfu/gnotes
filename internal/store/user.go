package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakfu/gnotes/internal/ulid"
)

// User identity lives outside any project, in the user's configuration
// directory, because it is a property of the person rather than of the notes.
// One id follows them across every project, so their history stays theirs.
const (
	userConfigDir  = "gnotes"
	userConfigFile = "user.json"
)

// UserConfigPath returns the path of the global identity file. The GNOTES_HOME
// environment variable overrides it, which is what lets the tests run without
// touching the real one.
func UserConfigPath() (string, error) {
	if home := os.Getenv("GNOTES_HOME"); home != "" {
		return filepath.Join(home, userConfigFile), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate the user configuration directory: %w", err)
	}
	return filepath.Join(dir, userConfigDir, userConfigFile), nil
}

// LoadUser reads the global identity. A missing file is not an error; it
// returns the zero Actor, which Valid reports as unconfigured.
func LoadUser() (Actor, error) {
	path, err := UserConfigPath()
	if err != nil {
		return Actor{}, err
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Actor{}, nil
		}
		return Actor{}, fmt.Errorf("read %s: %w", path, err)
	}

	var a Actor
	if err := json.Unmarshal(raw, &a); err != nil {
		return Actor{}, fmt.Errorf("parse %s: %w", path, err)
	}
	a.ID = ulid.Canonical(a.ID)
	return a, nil
}

// SaveUser writes the global identity, minting an id if the actor does not
// have one yet. It returns the stored actor.
//
// An existing id is never replaced. It is the anchor for every event that
// person has ever written, in every project, so regenerating it would split
// their history in two.
func SaveUser(a Actor) (Actor, error) {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		return Actor{}, fmt.Errorf("a user name is required")
	}
	if len(a.Name) > 80 {
		return Actor{}, fmt.Errorf("that user name is too long (%d characters, limit 80)", len(a.Name))
	}
	if !ulid.Valid(a.ID) {
		a.ID = ulid.NewGenerator().New()
	}

	path, err := UserConfigPath()
	if err != nil {
		return Actor{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Actor{}, fmt.Errorf("create the configuration directory: %w", err)
	}

	raw, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return Actor{}, err
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return Actor{}, fmt.Errorf("write %s: %w", path, err)
	}
	return a, nil
}

// MarshalJSON and the field tags keep the on-disk identity readable.
func (a Actor) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}{a.ID, a.Name})
}

// UnmarshalJSON reads the identity file.
func (a *Actor) UnmarshalJSON(raw []byte) error {
	var v struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	a.ID, a.Name = v.ID, v.Name
	return nil
}
