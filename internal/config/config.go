// Package config reads and writes perch's settings file, $XDG_CONFIG_HOME/perch/config.json
// (else ~/.config/perch/config.json). Keys perch does not know are kept when it rewrites the
// file, so a newer version's settings survive an older binary.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// Themes returns the TUI's themes in the order t cycles through them, the default first.
func Themes() []string { return []string{"coral", "teal", "violet"} }

// DefaultTheme is the theme used when nothing chooses one.
const DefaultTheme = "coral"

// ValidTheme reports whether name is one of Themes.
func ValidTheme(name string) bool { return slices.Contains(Themes(), name) }

// File is the settings file: the known fields plus every key as read, so unknown ones survive.
type File struct {
	Theme string
	raw   map[string]json.RawMessage
}

// Path is the settings file's location for this environment.
func Path(getenv func(string) string, home string) (string, error) {
	if d := getenv("XDG_CONFIG_HOME"); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, "perch", "config.json"), nil
	}
	if home == "" {
		return "", errors.New("config: no home directory and no XDG_CONFIG_HOME")
	}
	return filepath.Join(home, ".config", "perch", "config.json"), nil
}

// Load reads the file at path. A missing file is an empty File and no error. Any other problem
// returns the error with an empty File, which the caller reports and then uses defaults.
func Load(path string) (File, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, fmt.Errorf("config: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return File{}, fmt.Errorf("config: %s: %w", path, err)
	}
	f := File{raw: raw}
	if v, ok := raw["theme"]; ok {
		if err := json.Unmarshal(v, &f.Theme); err != nil {
			return File{raw: raw}, fmt.Errorf("config: %s: theme: %w", path, err)
		}
	}
	return f, nil
}

// Save writes f to path atomically: a temporary file in the same directory, then a rename.
func Save(path string, f File) error {
	raw := make(map[string]json.RawMessage, len(f.raw)+1)
	for k, v := range f.raw {
		raw[k] = v
	}
	if f.Theme != "" {
		v, err := json.Marshal(f.Theme)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		raw["theme"] = v
	}
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	defer os.Remove(tmp.Name()) // a no-op once the rename has happened
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("config: write %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// SaveTheme loads the file at path, sets its theme and saves it, keeping every other key.
func SaveTheme(path, theme string) error {
	f, err := Load(path)
	if err != nil {
		return err
	}
	f.Theme = theme
	return Save(path, f)
}

// ResolveTheme picks the theme: the --theme flag, then PERCH_THEME, then the file's "theme",
// then DefaultTheme. A bad value at any step, or a file that cannot be read, gives a warning for
// the status bar and falls through to the next step.
func ResolveTheme(flag, env string, file File, loadErr error) (theme, warning string) {
	var warns []string
	for _, c := range []struct{ name, from string }{{flag, "--theme"}, {env, "PERCH_THEME"}, {file.Theme, "config.json"}} {
		if c.name == "" {
			continue
		}
		if ValidTheme(c.name) {
			theme = c.name
			break
		}
		warns = append(warns, fmt.Sprintf("unknown theme %q from %s", c.name, c.from))
	}
	if loadErr != nil {
		warns = append([]string{loadErr.Error()}, warns...)
	}
	if theme == "" {
		theme = DefaultTheme
	}
	if len(warns) > 0 {
		warning = warns[0] + "; using " + theme
	}
	return theme, warning
}
