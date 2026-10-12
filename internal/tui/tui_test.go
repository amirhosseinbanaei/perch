package tui

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amirhosseinbanaei/perch/internal/agents"
	"github.com/amirhosseinbanaei/perch/internal/snapshot"
	"github.com/amirhosseinbanaei/perch/internal/tmux"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var t0 = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func fixedSnapshot() *snapshot.Snapshot {
	info := func(id, name string, st agents.Status, sid, title, path string, src map[string]string, conf string) *agents.Info {
		return &agents.Info{Agent: id, DisplayName: name, Status: st, SessionID: sid, Title: title, Path: path, PID: 4242,
			StartedAt: t0.Add(-42 * time.Minute), Source: src, Confidence: conf}
	}
	pane := func(id string, idx int, target, cmd, path string, active bool, a *agents.Info) snapshot.Pane {
		return snapshot.Pane{ID: id, Index: idx, Target: target, PID: 100, Command: cmd, Path: path, Active: active, Width: 80, Height: 24, Agent: a}
	}
	return &snapshot.Snapshot{Taken: t0, Server: snapshot.Server{Socket: "default", Running: true}, Sessions: []snapshot.Session{
		{ID: "$0", Name: "api-server", Created: t0.Add(-3 * time.Hour), Attached: 1, Activity: t0, Windows: []snapshot.Window{
			{Index: 0, Name: "claude", Activity: t0.Add(-5 * time.Second), Panes: []snapshot.Pane{
				pane("%0", 0, "api-server:0.0", "claude", "/home/u/src/api", true,
					info("claude", "Claude Code", agents.Working, "11111111-2222-4333-8444-555555555555", "Fix flaky integration tests in the payments module",
						"/home/u/src/api", map[string]string{"agent": "argv", "status": "registry", "sessionId": "registry", "title": "state-dir", "path": "registry"}, agents.High)),
				pane("%1", 1, "api-server:0.1", "zsh", "/home/u/src/api", false, nil),
			}},
			{Index: 1, Name: "codex", Activity: t0.Add(-2 * time.Minute), Panes: []snapshot.Pane{
				pane("%2", 0, "api-server:1.0", "node", "/home/u/src/api", true,
					info("codex", "Codex CLI", agents.Waiting, "", "host", "/home/u/src/api",
						map[string]string{"agent": "argv", "status": "screen", "sessionId": "none (no rollout)", "title": "pane-title", "path": "proc-cwd"}, agents.High)),
			}},
		}},
		{ID: "$1", Name: "notes", Created: t0.Add(-26 * time.Hour), Activity: t0.Add(-time.Hour), Windows: []snapshot.Window{
			{Index: 0, Name: "vim", Activity: t0.Add(-time.Hour), Panes: []snapshot.Pane{pane("%3", 0, "notes:0.0", "nvim", "/home/u/notes", true, nil)}},
		}},
		{ID: "$2", Name: "ünïcode 🙂 box", Created: t0.Add(-10 * time.Minute), Activity: t0, Windows: []snapshot.Window{
			{Index: 0, Name: "gemini", Activity: t0.Add(-20 * time.Second), Panes: []snapshot.Pane{
				pane("%4", 0, "ünïcode 🙂 box:0.0", "node", "/srv", true,
					info("gemini", "Gemini CLI", agents.Idle, "9a8b7c6d-1111-4222-8333-444455556666", "Refactor the parser", "/srv",
						map[string]string{"agent": "argv", "status": "activity", "sessionId": "state-dir"}, agents.Low)),
			}},
		}},
	}}
}

func testModel(w, h int, ascii bool) Model {
	m := New(Options{
		Collector: snapshot.New(tmux.Runner{Socket: "perch-test-unused"}, snapshot.Options{}),
		Theme:     Theme{ASCII: ascii},
		Home:      "/home/u",
		Now:       func() time.Time { return t0 },
	})
	m = m.Resize(w, h).Snapshot(fixedSnapshot(), false)
	m.previewPane, m.previewText = "%0", "$ claude\n╭───────────╮\n│ > hello   │\n╰───────────╯\n  ? for shortcuts\n"
	return m
}

func checkScreen(t *testing.T, name, got string, w, h int) {
	t.Helper()
	lines := strings.Split(got, "\n")
	if len(lines) != h {
		t.Errorf("%s: %d lines, want %d", name, len(lines), h)
	}
	for i, l := range lines {
		if n := ansi.StringWidth(l); n > w {
			t.Errorf("%s: line %d is %d cells wide (> %d): %q", name, i, n, w, l)
		}
	}
	path := filepath.Join("testdata", "golden", name+".txt")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/tui -update)", err)
	}
	if got+"\n" != string(want) {
		t.Errorf("%s differs from %s:\n--- got ---\n%s\n--- want ---\n%s", name, path, got, want)
	}
}

func TestGolden(t *testing.T) {
	for _, sz := range [][2]int{{40, 12}, {80, 24}, {140, 40}} {
		w, h := sz[0], sz[1]
		m := testModel(w, h, false)
		m.cursor = 1 // the Claude row
		m.syncLayout()
		checkScreen(t, sizeName(w, h), m.Render(), w, h)
	}
}

func sizeName(w, h int) string { return fmt.Sprintf("%dx%d", w, h) }

func press(m Model, keys ...string) Model {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		default:
			r := []rune(k)[0]
			msg = tea.KeyPressMsg{Code: r, Text: k}
		}
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

func TestNarrowDetailAndDialogs(t *testing.T) {
	m := testModel(40, 12, true)
	m = press(m, "j", "enter")
	if m.mode != modeDetail {
		t.Fatalf("enter on a narrow screen: mode %v", m.mode)
	}
	checkScreen(t, "40x12-detail-ascii", m.Render(), 40, 12)
	m = press(m, "esc", "x")
	if m.mode != modeKill || m.formTarget != "api-server" || m.killYes {
		t.Fatalf("kill dialog: mode %v target %q yes %v", m.mode, m.formTarget, m.killYes)
	}
	checkScreen(t, "40x12-kill", m.Render(), 40, 12)
	m = press(m, "enter") // the default is No
	if m.mode != modeList {
		t.Fatalf("enter on kill dialog left mode %v", m.mode)
	}
}

func TestFilterAndAgentsOnly(t *testing.T) {
	m := testModel(80, 24, false)
	m = press(m, "g")
	for _, r := range m.rows {
		if r.session == "notes" {
			t.Fatal("agents-only still lists a session without agents")
		}
	}
	m = press(m, "g", "/", "g", "e", "m")
	if m.mode != modeFilter || m.filter != "gem" {
		t.Fatalf("filter = %q mode %v", m.filter, m.mode)
	}
	if len(m.rows) != 2 || m.rows[1].pane != "%4" {
		t.Fatalf("rows = %+v", m.rows)
	}
	m = press(m, "enter")
	if m.mode != modeList || m.filter != "gem" {
		t.Fatalf("after enter: %v %q", m.mode, m.filter)
	}
	m = press(m, "esc")
	if m.filter != "" || len(m.rows) != 6 {
		t.Fatalf("esc did not clear the filter: %q %d", m.filter, len(m.rows))
	}
}

func TestForms(t *testing.T) {
	m := testModel(80, 24, false)
	m = press(m, "r")
	if m.mode != modeRename || m.inputs[0].Value() != "api-server" {
		t.Fatalf("rename: %v %q", m.mode, m.inputs[0].Value())
	}
	m = press(m, "esc", "j", "s")
	if m.mode != modeSend || m.formTarget != "%0" || !m.sendEnter {
		t.Fatalf("send: %v %q %v", m.mode, m.formTarget, m.sendEnter)
	}
	m = press(m, "tab")
	if m.sendEnter {
		t.Fatal("tab did not toggle 'and press Enter'")
	}
	m = press(m, "esc", "n")
	if m.mode != modeNew || len(m.inputs) != 3 || m.inputs[1].Value() != "/home/u/src/api" {
		t.Fatalf("new: %v %d", m.mode, len(m.inputs))
	}
	checkScreen(t, "80x24-new", m.Render(), 80, 24)
	m = press(m, "enter", "enter", "enter") // no name: refused, form stays
	if m.mode != modeNew || !m.statusErr {
		t.Fatalf("empty name accepted: %v %q", m.mode, m.status)
	}
}

func TestEmptyAndMouse(t *testing.T) {
	m := New(Options{Collector: snapshot.New(tmux.Runner{Socket: "x"}, snapshot.Options{}), Now: func() time.Time { return t0 }})
	m = m.Resize(40, 12).Snapshot(&snapshot.Snapshot{Taken: t0, Server: snapshot.Server{Socket: "default"}}, false)
	if !strings.Contains(m.Render(), "press n to create a session") {
		t.Fatalf("empty state:\n%s", m.Render())
	}

	m = testModel(140, 40, false)
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 3, Y: 4})
	m = next.(Model)
	if m.cursor != 3 {
		t.Fatalf("click selected row %d, want 3", m.cursor)
	}
	next, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if next.(Model).cursor != 4 {
		t.Fatalf("wheel moved to %d", next.(Model).cursor)
	}
}

func TestBadge(t *testing.T) {
	for _, s := range agents.Statuses() {
		uni := Theme{}.Badge(s)
		asc := Theme{ASCII: true}.Badge(s)
		if !strings.HasSuffix(uni, string(s)) || !strings.HasSuffix(asc, string(s)) || ansi.StringWidth(uni) > BadgeWidth {
			t.Errorf("badge %s: %q %q", s, uni, asc)
		}
		if strings.ContainsFunc(asc, func(r rune) bool { return r > 127 }) {
			t.Errorf("ascii badge %q", asc)
		}
		if c := (Theme{Color: true}).Badge(s); ansi.Strip(c) != uni {
			t.Errorf("coloured badge %q lost its text", c)
		}
	}
	if !UTF8Locale(func(k string) string { return map[string]string{"LANG": "en_US.UTF-8"}[k] }) ||
		UTF8Locale(func(k string) string { return map[string]string{"LC_ALL": "C", "LANG": "en_US.UTF-8"}[k] }) ||
		UTF8Locale(func(string) string { return "" }) {
		t.Error("UTF8Locale")
	}
	if ShortPath("/home/u/x", "/home/u") != "~/x" || ShortPath("/home/uu", "/home/u") != "/home/uu" {
		t.Error("ShortPath")
	}
}
