package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPath(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if p, _ := Path(env(map[string]string{"XDG_CONFIG_HOME": "/x"}), "/home/u"); p != "/x/perch/config.json" {
		t.Errorf("XDG: %s", p)
	}
	if p, _ := Path(env(map[string]string{"XDG_CONFIG_HOME": "rel"}), "/home/u"); p != "/home/u/.config/perch/config.json" {
		t.Errorf("relative XDG must be ignored: %s", p)
	}
	if _, err := Path(env(nil), ""); err == nil {
		t.Error("no home and no XDG: want an error")
	}
}

func TestLoadSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "perch", "config.json")

	f, err := Load(path)
	if err != nil || f.Theme != "" {
		t.Fatalf("missing file: %+v %v", f, err)
	}
	if err := SaveTheme(path, "teal"); err != nil {
		t.Fatal(err)
	}
	if f, err = Load(path); err != nil || f.Theme != "teal" {
		t.Fatalf("after save: %+v %v", f, err)
	}

	// Unknown keys survive a rewrite.
	if err := os.WriteFile(path, []byte(`{"theme":"teal","future":{"a":[1,2]},"n":3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveTheme(path, "violet"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["theme"] != "violet" || got["n"] != 3.0 || got["future"] == nil {
		t.Fatalf("rewrite lost keys: %s", b)
	}

	// The write is atomic: no temporary file is left behind, and the file is whole.
	ents, _ := os.ReadDir(filepath.Dir(path))
	if len(ents) != 1 || ents[0].Name() != "config.json" {
		t.Fatalf("leftovers: %v", ents)
	}
}

func TestSaveAtomicOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"theme":"teal"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory where the file should be: rename fails, the old content is untouched elsewhere.
	bad := filepath.Join(dir, "sub")
	if err := os.MkdirAll(filepath.Join(bad, "config.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Save(filepath.Join(bad, "config.json"), File{Theme: "coral"}); err == nil {
		t.Fatal("rename over a directory succeeded")
	}
	ents, _ := os.ReadDir(bad)
	if len(ents) != 1 {
		t.Fatalf("a failed save left files behind: %v", ents)
	}
	if f, _ := Load(path); f.Theme != "teal" {
		t.Fatalf("other file changed: %+v", f)
	}
}

func TestBadFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"theme":`), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err == nil {
		t.Fatal("bad JSON: no error")
	}
	if th, warn := ResolveTheme("", "", f, err); th != DefaultTheme || warn == "" {
		t.Fatalf("bad JSON: %q %q", th, warn)
	}
	if err := os.WriteFile(path, []byte(`{"theme":42}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a theme that is not a string: no error")
	}
}

func TestResolveTheme(t *testing.T) {
	for _, c := range []struct {
		flag, env, file string
		loadErr         error
		want            string
		warn            string
	}{
		{"", "", "", nil, "coral", ""},
		{"", "", "violet", nil, "violet", ""},
		{"", "teal", "violet", nil, "teal", ""},
		{"violet", "teal", "coral", nil, "violet", ""},
		{"pink", "", "", nil, "coral", `unknown theme "pink" from --theme`},
		{"pink", "teal", "", nil, "teal", `unknown theme "pink" from --theme; using teal`},
		{"", "", "neon", nil, "coral", `unknown theme "neon" from config.json`},
		{"", "", "", errors.New("config: boom"), "coral", "config: boom"},
	} {
		th, warn := ResolveTheme(c.flag, c.env, File{Theme: c.file}, c.loadErr)
		if th != c.want || !strings.HasPrefix(warn, c.warn) || (c.warn == "") != (warn == "") {
			t.Errorf("%+v: got %q %q", c, th, warn)
		}
	}
	if !ValidTheme("teal") || ValidTheme("Teal") || len(Themes()) != 3 || Themes()[0] != DefaultTheme {
		t.Error("ValidTheme/Themes")
	}
}
