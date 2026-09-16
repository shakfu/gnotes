package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakfu/gwiki/internal/ulid"
)

// User identity lives outside any project, in the user's configuration
// directory, because it is a property of the person rather than of the notes.
// One id follows them across every project, so their history stays theirs.
const (
	userConfigDir    = "gwiki"
	userConfigFile   = "user.json"
	globalConfigFile = "global.json"
)

// UserConfigPath returns the path of the global identity file. The GWIKI_HOME
// environment variable overrides it, which is what lets the tests run without
// touching the real one.
func UserConfigPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, userConfigFile), nil
}

// configDir returns the directory holding the per-user files.
func configDir() (string, error) {
	if home := os.Getenv("GWIKI_HOME"); home != "" {
		return home, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate the user configuration directory: %w", err)
	}
	return filepath.Join(dir, userConfigDir), nil
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
	// A crash mid-write must not leave a truncated identity, which would mint
	// a new id on the next init and split the person's history in two.
	if err := writeConfigFile(path, a); err != nil {
		return Actor{}, err
	}
	return a, nil
}

// GlobalConfigPath returns the path of the file recording where the global
// notes live. It is kept apart from the identity so that rewriting one cannot
// drop the other.
func GlobalConfigPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, globalConfigFile), nil
}

// LoadGlobal returns the directory holding the global notes project, or ""
// when none has been set up.
func LoadGlobal() (string, error) {
	path, err := GlobalConfigPath()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var v struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	return v.Path, nil
}

// SaveGlobal records dir, which must be absolute, as the global notes
// location.
func SaveGlobal(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("the global notes location must be absolute, got %q", dir)
	}
	path, err := GlobalConfigPath()
	if err != nil {
		return err
	}
	return writeConfigFile(path, struct {
		Path string `json:"path"`
	}{dir})
}

// writeConfigFile writes v as indented JSON through a temporary file and a
// rename, so a crash leaves the previous file intact.
func writeConfigFile(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create the configuration directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
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
