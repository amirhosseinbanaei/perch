package tui

import (
	"errors"
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
	"github.com/amirhosseinbanaei/perch/internal/config"
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
	return &snapshot.Snapshot{Taken: t0.Add(-2 * time.Second), Server: snapshot.Server{Socket: "default", Running: true}, Sessions: []snapshot.Session{
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

func fakeLook(name string) (string, error) {
	if name == "claude" || name == "codex" {
		return "/usr/bin/" + name, nil
	}
	return "", errors.New("not found")
}

// newModel is a model on a fixed clock, with no tmux behind it.
func newModel(th Theme, opts ...func(*Options)) Model {
	o := Options{
		Collector: snapshot.New(tmux.Runner{Socket: "perch-test-unused"}, snapshot.Options{}),
		Theme:     th,
		Home:      "/home/u",
		Now:       func() time.Time { return t0 },
		LookPath:  fakeLook,
	}
	for _, f := range opts {
		f(&o)
	}
	return New(o)
}

func dark() *bool { d := true; return &d }

// themed is a colour theme on a known dark background, as after BackgroundColorMsg.
func themed(name string) Theme { return Theme{Color: true, Dark: dark(), Name: name} }

func testModel(w, h int, th Theme) Model {
	m := newModel(th).Resize(w, h).Snapshot(fixedSnapshot(), false)
	m.previewPane, m.previewText = "%0", "$ claude\n╭───────────╮\n│ > hello   │\n╰───────────╯\n  ? for shortcuts\n"
	m.cursor = 1 // the Claude row
	m.syncLayout()
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

func sizeName(w, h int) string { return fmt.Sprintf("%dx%d", w, h) }

var sizes = [][2]int{{40, 12}, {80, 24}, {140, 40}}

func TestGoldenThemes(t *testing.T) {
	for _, name := range config.Themes() {
		for _, sz := range sizes {
			m := testModel(sz[0], sz[1], themed(name))
			checkScreen(t, name+"-"+sizeName(sz[0], sz[1]), m.Render(), sz[0], sz[1])
		}
	}
}

func TestGoldenModes(t *testing.T) {
	m := testModel(80, 24, Theme{ASCII: true, Name: "coral"})
	checkScreen(t, "ascii-80x24", m.Render(), 80, 24)
	m = testModel(80, 24, Theme{Name: "coral"})
	checkScreen(t, "nocolor-80x24", m.Render(), 80, 24)
	// Before the background is known: the dark variant, and no Surface background.
	m = testModel(80, 24, Theme{Color: true, Name: "violet"})
	if strings.Contains(m.Render(), "\x1b[48;") {
		t.Error("a Surface background was drawn before the background was known")
	}
	for _, sz := range sizes {
		m := testModel(sz[0], sz[1], Theme{ASCII: true, Name: "teal"})
		m.previewText = "$ claude\n> hello\n"
		for i, l := range strings.Split(m.Render(), "\n") {
			if strings.ContainsFunc(ansi.Strip(l), func(r rune) bool { return r > 127 && !strings.ContainsRune("ünïcode🙂", r) }) {
				t.Errorf("ascii %s line %d: %q", sizeName(sz[0], sz[1]), i, ansi.Strip(l))
			}
		}
	}
}

func TestGoldenEmpty(t *testing.T) {
	empty := &snapshot.Snapshot{Taken: t0, Server: snapshot.Server{Socket: "default"}}
	m := newModel(themed("coral")).Resize(80, 24).Snapshot(empty, false)
	checkScreen(t, "empty-80x24", m.Render(), 80, 24)
	if !strings.Contains(ansi.Strip(m.Render()), "No tmux sessions yet") {
		t.Fatal("empty state text")
	}
	m = newModel(themed("coral")).Resize(40, 10).Snapshot(empty, false)
	checkScreen(t, "empty-40x10", m.Render(), 40, 10)
	if strings.Contains(ansi.Strip(m.Render()), logoArt(false)[0]) {
		t.Fatal("the logo is drawn under 12 rows")
	}
}

func TestGoldenOverlays(t *testing.T) {
	for _, sz := range [][2]int{{80, 24}, {40, 12}} {
		w, h := sz[0], sz[1]
		name := func(s string) string { return s + "-" + sizeName(w, h) }
		base := testModel(w, h, themed("coral"))

		checkScreen(t, name("cmdbar"), press(base, "/", "r", "e").Render(), w, h)
		checkScreen(t, name("modal-kill"), press(base, "x").Render(), w, h)
		checkScreen(t, name("modal-new"), press(base, "n").Render(), w, h)
		checkScreen(t, name("modal-rename"), press(base, "r").Render(), w, h)
		checkScreen(t, name("modal-send"), press(base, "s").Render(), w, h)
		checkScreen(t, name("modal-help"), press(base, "?").Render(), w, h)
	}
	m := testModel(80, 24, themed("teal"))
	m.showToast("theme: teal", toastInfo)
	checkScreen(t, "toast-80x24", m.Render(), 80, 24)
	m = testModel(140, 40, themed("teal"))
	m.showToast("◆ codex in api-server needs you", toastWaiting)
	checkScreen(t, "toast-140x40", m.Render(), 140, 40)
}

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
		case "up":
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "right":
			msg = tea.KeyPressMsg{Code: tea.KeyRight}
		case "backspace":
			msg = tea.KeyPressMsg{Code: tea.KeyBackspace}
		case "space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		default:
			r := []rune(k)[0]
			msg = tea.KeyPressMsg{Code: r, Text: k}
		}
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

func typeText(m Model, s string) Model {
	for _, r := range s {
		if r == ' ' {
			m = press(m, "space")
		} else {
			m = press(m, string(r))
		}
	}
	return m
}

func TestNarrowDetailAndKill(t *testing.T) {
	m := testModel(40, 12, Theme{ASCII: true, Name: "coral"})
	m.cursor = 0
	m = press(m, "j", "enter")
	if m.mode != modeDetail {
		t.Fatalf("enter on a narrow screen: mode %v", m.mode)
	}
	checkScreen(t, "40x12-detail-ascii", m.Render(), 40, 12)
	m = press(m, "esc", "x")
	if m.mode != modeKill || m.formTarget != "api-server" || m.killYes {
		t.Fatalf("kill dialog: mode %v target %q yes %v", m.mode, m.formTarget, m.killYes)
	}
	m = press(m, "enter") // the default is No
	if m.mode != modeList {
		t.Fatalf("enter on kill dialog left mode %v", m.mode)
	}
}

func TestFocusAndPreviewScroll(t *testing.T) {
	m := testModel(140, 40, themed("coral"))
	m = press(m, "tab")
	if !m.focusDetail {
		t.Fatal("tab did not focus Detail")
	}
	cur := m.cursor
	m = press(m, "up", "up")
	if m.cursor != cur || m.previewOff != 2 {
		t.Fatalf("arrows in Detail: cursor %d offset %d", m.cursor, m.previewOff)
	}
	m = press(m, "tab", "down")
	if m.focusDetail || m.cursor != cur+1 {
		t.Fatalf("tab back: focus %v cursor %d", m.focusDetail, m.cursor)
	}
}

func TestTypingFilters(t *testing.T) {
	m := testModel(80, 24, themed("coral"))
	m = press(m, "g")
	for _, r := range m.rows {
		if r.session == "notes" {
			t.Fatal("agents-only still lists a session without agents")
		}
	}
	m = press(m, "g", "e", "m")
	if !m.barOn || m.filter != "em" {
		t.Fatalf("typing did not filter: bar %v filter %q", m.barOn, m.filter)
	}
	if len(m.rows) != 2 || m.rows[1].pane != "%4" {
		t.Fatalf("rows = %+v", m.rows)
	}
	m = press(m, "enter")
	if m.barOn || m.filter != "em" {
		t.Fatalf("after enter: bar %v filter %q", m.barOn, m.filter)
	}
	m = press(m, "esc")
	if m.filter != "" || len(m.rows) != 6 {
		t.Fatalf("esc did not clear the filter: %q %d", m.filter, len(m.rows))
	}
	m = press(m, "e", "esc")
	if m.barOn || m.filter != "" {
		t.Fatalf("esc in the bar: bar %v filter %q", m.barOn, m.filter)
	}
	// /filter, and a "/word" that names no command, both filter.
	m = typeText(press(m, "/"), "filter srv")
	m = press(m, "enter")
	if m.filter != "srv" || m.barOn {
		t.Fatalf("/filter: %q %v", m.filter, m.barOn)
	}
	m = press(typeText(press(m, ":"), "zzz"), "enter")
	if m.filter != "zzz" || len(m.rows) != 0 {
		t.Fatalf("/zzz: %q %d", m.filter, len(m.rows))
	}
}

func TestCommandMatching(t *testing.T) {
	names := func(cs []command) string {
		var s []string
		for _, c := range cs {
			s = append(s, c.name)
		}
		return strings.Join(s, " ")
	}
	if got := names(matchCommands("")); len(strings.Fields(got)) != len(commandList()) {
		t.Errorf("empty query: %s", got)
	}
	if got := names(matchCommands("re")); !strings.HasPrefix(got, "rename refresh ") || !strings.Contains(got, "preview") {
		t.Errorf("re: %s", got)
	}
	if got := names(matchCommands("kl")); got != "kill" {
		t.Errorf("kl (fuzzy): %s", got)
	}
	if got := names(matchCommands("xyz")); got != "" {
		t.Errorf("xyz: %s", got)
	}
	for _, c := range []struct {
		line, cmd, rest, filter, err string
		sel                          int
	}{
		{line: "/kill", cmd: "kill"},
		{line: "/kill notes", cmd: "kill", rest: "notes"},
		{line: "/ki", cmd: "kill"},
		{line: "/re", sel: 1, cmd: "refresh"},
		{line: "/ren new name", cmd: "rename", rest: "new name"},
		{line: "/re x", err: "unknown command /re"},
		{line: "/foo bar", err: "unknown command /foo"},
		{line: "/gem", filter: "gem"},
		{line: "/", cmd: "attach"},
		{line: "/ ", cmd: "attach"},
		{line: "/THEME teal", cmd: "theme", rest: "teal"},
	} {
		p, err := parseCommand(c.line, c.sel)
		switch {
		case c.err != "":
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: err %v, want %q", c.line, err, c.err)
			}
		case err != nil || p.cmd.name != c.cmd || p.rest != c.rest || p.filter != c.filter:
			t.Errorf("%q: %+v %v", c.line, p, err)
		}
	}
	args, err := splitArgs(`a "b c" 'd e' f`)
	if err != nil || strings.Join(args, "|") != "a|b c|d e|f" {
		t.Errorf("splitArgs: %q %v", args, err)
	}
	if _, err := splitArgs(`"open`); err == nil {
		t.Error("splitArgs: unclosed quote")
	}
}

func TestCommandBar(t *testing.T) {
	m := testModel(80, 24, themed("coral"))

	// /kill only ever opens the confirm, with or without a name.
	for _, line := range []string{"kill", "kill notes", "ki"} {
		next, cmd := press(typeText(press(m, "/"), line), "enter"), tea.Cmd(nil)
		_ = cmd
		if next.mode != modeKill || next.killYes {
			t.Fatalf("/%s: mode %v yes %v", line, next.mode, next.killYes)
		}
	}
	mm := typeText(press(m, "/"), "kill notes")
	next, cmd := mm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if n := next.(Model); n.formTarget != "notes" || n.mode != modeKill {
		t.Fatalf("/kill notes: %v %q", n.mode, n.formTarget)
	}
	if cmd != nil {
		if _, isAction := cmd().(actionMsg); isAction {
			t.Fatal("/kill ran an action")
		}
	}

	// Errors stay in the bar.
	for line, want := range map[string]string{
		"kill nosuch": "no session named",
		"theme pink":  "unknown theme",
		"foo bar":     "unknown command",
		"attach nope": "not found",
		"new -c":      "needs a value",
		"new a b":     "one name only",
	} {
		e := press(typeText(press(m, "/"), line), "enter")
		if !e.barOn || !strings.Contains(e.barErr, want) {
			t.Errorf("/%s: bar %v err %q, want %q", line, e.barOn, e.barErr, want)
		}
		if !strings.Contains(ansi.Strip(e.Render()), want) {
			t.Errorf("/%s: the error is not shown", line)
		}
	}

	// Tab completes the selected command; Up and Down move the selection.
	e := press(typeText(press(m, "/"), "re"), "tab")
	if e.bar.Value() != "/rename " {
		t.Fatalf("tab: %q", e.bar.Value())
	}
	e = press(typeText(press(m, "/"), "re"), "down", "tab")
	if e.bar.Value() != "/refresh" {
		t.Fatalf("down, tab: %q", e.bar.Value())
	}

	// Commands that open modals, toggle state, or switch the theme.
	for line, check := range map[string]func(Model) bool{
		"new":          func(n Model) bool { return n.mode == modeNew },
		"rename":       func(n Model) bool { return n.mode == modeRename },
		"send":         func(n Model) bool { return n.mode == modeSend },
		"help":         func(n Model) bool { return n.mode == modeHelp },
		"log":          func(n Model) bool { return n.mode == modeLog },
		"source":       func(n Model) bool { return n.showSource },
		"preview":      func(n Model) bool { return !n.preview },
		"agents":       func(n Model) bool { return n.agentsOnly },
		"theme":        func(n Model) bool { return n.theme.Name == "teal" && n.toast.text == "theme: teal" },
		"theme violet": func(n Model) bool { return n.theme.Name == "violet" },
	} {
		n := press(typeText(press(m, "/"), line), "enter")
		if n.barOn || !check(n) {
			t.Errorf("/%s: bar %v err %q mode %v", line, n.barOn, n.barErr, n.mode)
		}
	}
}

func TestThemeCycleAndSave(t *testing.T) {
	var saved []string
	fail := false
	m := newModel(themed("coral"), func(o *Options) {
		o.SaveTheme = func(n string) error {
			if fail {
				return errors.New("read-only")
			}
			saved = append(saved, n)
			return nil
		}
	}).Resize(80, 24).Snapshot(fixedSnapshot(), false)
	save := func(m Model) Model { // what the batch from setTheme runs in the background
		next, _ := m.Update(m.saveThemeCmd(m.theme.Name)())
		return next.(Model)
	}
	for _, want := range []string{"teal", "violet", "coral"} {
		m = save(press(m, "t"))
		if m.theme.Name != want || m.toast.text != "theme: "+want {
			t.Fatalf("t: theme %q toast %q, want %q", m.theme.Name, m.toast.text, want)
		}
	}
	if strings.Join(saved, " ") != "teal violet coral" {
		t.Fatalf("saved %v", saved)
	}
	fail = true
	m = save(press(m, "t"))
	if m.theme.Name != "teal" || m.toast.kind != toastError || !strings.Contains(m.toast.text, "read-only") {
		t.Fatalf("failed save: theme %q toast %+v", m.theme.Name, m.toast)
	}
}

func TestForms(t *testing.T) {
	m := testModel(80, 24, themed("coral"))
	m.cursor = 0
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
	if m.mode != modeNew || len(m.inputs) != 2 || m.inputs[1].Value() != "/home/u/src/api" {
		t.Fatalf("new: %v %d", m.mode, len(m.inputs))
	}
	if strings.Join(m.agentChoices, " ") != "shell claude codex" {
		t.Fatalf("agent picker: %v", m.agentChoices)
	}
	m = press(m, "enter", "enter", "right", "right")
	if m.focus != 2 || m.agentChoices[m.agentPick] != "codex" {
		t.Fatalf("picker: focus %d pick %d", m.focus, m.agentPick)
	}
	m = press(m, "enter") // no name: refused, form stays
	if m.mode != modeNew || !m.statusErr {
		t.Fatalf("empty name accepted: %v %q", m.mode, m.status)
	}
}

func TestMouse(t *testing.T) {
	m := testModel(140, 40, themed("coral"))
	m.cursor = 0
	top := m.lay.sessions.y + 1 // the first row, under the panel's top border
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 3, Y: top + 3})
	m = next.(Model)
	if m.cursor != 3 {
		t.Fatalf("click selected row %d, want 3", m.cursor)
	}
	next, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 3, Y: top})
	if next.(Model).cursor != 4 {
		t.Fatalf("wheel moved to %d", next.(Model).cursor)
	}
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: m.lay.detail.x + 5, Y: top + 2})
	if !next.(Model).focusDetail {
		t.Fatal("a click in Detail did not focus it")
	}
}

func TestAnimationTick(t *testing.T) {
	idle := fixedSnapshot()
	idle.Sessions[0].Windows[0].Panes[0].Agent.Status = agents.Idle

	m := newModel(themed("coral")).Resize(80, 24)
	next, _ := m.Update(snapMsg{snap: idle})
	if n := next.(Model); n.animPending {
		t.Fatal("an animation tick was scheduled with no working agent")
	}
	next, _ = next.(Model).Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if next.(Model).animPending {
		t.Fatal("a key press scheduled an animation tick")
	}
	next, _ = next.(Model).Update(snapMsg{snap: fixedSnapshot()})
	n := next.(Model)
	if !n.animPending {
		t.Fatal("no animation tick with a working agent")
	}
	next, _ = n.Update(animMsg{})
	if n2 := next.(Model); n2.frame != 1 || !n2.animPending {
		t.Fatalf("anim step: frame %d pending %v", n2.frame, n2.animPending)
	}

	off := newModel(themed("coral"), func(o *Options) { o.NoAnim = true }).Resize(80, 24)
	next, _ = off.Update(snapMsg{snap: fixedSnapshot()})
	if next.(Model).animPending || strings.Contains(next.(Model).Render(), "⠋") {
		t.Fatal("--no-anim still animates")
	}
	plain := newModel(Theme{Name: "coral"}).Resize(80, 24)
	if next, _ = plain.Update(snapMsg{snap: fixedSnapshot()}); next.(Model).animPending {
		t.Fatal("animation without colour")
	}
}

func TestWaitingPulseAndToast(t *testing.T) {
	before := fixedSnapshot()
	before.Sessions[0].Windows[1].Panes[0].Agent.Status = agents.Idle
	before.Sessions[0].Windows[0].Panes[0].Agent.Status = agents.Idle
	m := newModel(themed("coral"), func(o *Options) { o.Bell = true }).Resize(80, 24)
	next, _ := m.Update(snapMsg{snap: before})
	m = next.(Model)
	if m.toast.text != "" {
		t.Fatalf("toast on the first snapshot: %q", m.toast.text)
	}
	after := fixedSnapshot()
	after.Sessions[0].Windows[0].Panes[0].Agent.Status = agents.Idle
	next, _ = m.Update(snapMsg{snap: after})
	m = next.(Model)
	if m.toast.text != "◆ codex in api-server needs you" || m.toast.kind != toastWaiting {
		t.Fatalf("toast %+v", m.toast)
	}
	if _, ok := m.pulses["%2"]; !ok || !m.animPending {
		t.Fatalf("pulse %v pending %v", m.pulses, m.animPending)
	}
	if !m.pulseOn("%2", t0) || m.pulseOn("%2", t0.Add(pulseStep)) || m.pulseOn("%2", t0.Add(pulseFor)) {
		t.Fatal("pulse phases")
	}
	if m.windowTitle() != "perch · ◆ 1 waiting" {
		t.Fatalf("window title %q", m.windowTitle())
	}
	next, _ = m.Update(toastMsg{id: m.toastID})
	if next.(Model).toast.text != "" {
		t.Fatal("the toast did not expire")
	}
}

func TestContrast(t *testing.T) {
	for _, name := range config.Themes() {
		for _, dk := range []bool{true, false} {
			p := themePalette(name, dk)
			bg := LightBackground
			if dk {
				bg = DarkBackground
			}
			check := func(role, fg string, min float64, against string) {
				if r := contrastRatio(fg, against); r < min {
					t.Errorf("%s dark=%v: %s %s on %s is %.2f:1, want %.1f:1", name, dk, role, fg, against, r, min)
				}
			}
			for role, fg := range map[string]string{"Text": p.Text, "Muted": p.Muted, "Danger": p.Danger} {
				check(role, fg, 4.5, bg)
			}
			check("Text on Surface", p.Text, 4.5, p.Surface)
			for role, fg := range map[string]string{
				"Accent": p.Accent, "AccentSoft": p.AccentSoft, "Faint": p.Faint, "Working": p.Working,
				"Waiting": p.Waiting, "Idle": p.Idle, "Exited": p.Exited, "Success": p.Success,
				"Claude": p.Claude, "Codex": p.Codex, "Gemini": p.Gemini, "Hermes": p.Hermes, "OpenCode": p.OpenCode,
			} {
				check(role, fg, 3, bg)
				if role != "Faint" && role != "AccentSoft" { // never drawn on Surface
					check(role+" on Surface", fg, 3, p.Surface)
				}
			}
		}
	}
	if r := contrastRatio("#000000", "#FFFFFF"); r < 20.9 || r > 21.1 {
		t.Errorf("black on white: %.2f", r)
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
		if c := themed("teal").Compile().Badge(s); ansi.Strip(c) != uni {
			t.Errorf("palette badge %q lost its text", c)
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

// TestEveryWidth renders each mode at every width from 40 to 200 and a few heights: no line
// may be wider than the screen.
func TestEveryWidth(t *testing.T) {
	for _, th := range []Theme{themed("violet"), {ASCII: true, Name: "coral"}} {
		for w := 40; w <= 200; w += 7 {
			for _, h := range []int{12, 16, 24, 41} {
				base := testModel(w, h, th)
				for _, keys := range [][]string{nil, {"tab"}, {"/"}, {"x"}, {"n"}, {"?"}, {"l"}, {"e"}} {
					m := press(base, keys...)
					for i, l := range strings.Split(m.Render(), "\n") {
						if n := ansi.StringWidth(l); n > w {
							t.Fatalf("%dx%d %v line %d: %d cells: %q", w, h, keys, i, n, l)
						}
					}
				}
			}
		}
	}
}

// bigSnapshot is 35 panes in 10 sessions, 23 of them agents, like the machine AGENTS.md timed.
func bigSnapshot() *snapshot.Snapshot {
	s := &snapshot.Snapshot{Taken: t0, Server: snapshot.Server{Socket: "default", Running: true}}
	ids := []string{"claude", "codex", "gemini", "hermes", "opencode"}
	sts := []agents.Status{agents.Working, agents.Waiting, agents.Idle, agents.Idle}
	n := 0
	for i := range 10 {
		sess := snapshot.Session{ID: fmt.Sprintf("$%d", i), Name: fmt.Sprintf("project-%d", i), Created: t0.Add(-time.Hour), Activity: t0}
		win := snapshot.Window{Index: 0, Name: "main", Activity: t0.Add(-time.Minute)}
		for j := range 3 + i%2 {
			if n >= 35 {
				break
			}
			p := snapshot.Pane{ID: fmt.Sprintf("%%%d", n), Index: j, Target: fmt.Sprintf("project-%d:0.%d", i, j), Command: "zsh", Path: "/home/u/src"}
			if n%3 != 2 && n < 35 {
				p.Agent = &agents.Info{Agent: ids[n%5], DisplayName: ids[n%5], Status: sts[n%4], SessionID: "11111111-2222-4333-8444-555555555555",
					Title: "Some fairly long title for this agent's current task", Path: "/home/u/src", PID: 1000 + n, StartedAt: t0.Add(-time.Hour)}
			}
			win.Panes = append(win.Panes, p)
			n++
		}
		sess.Windows = []snapshot.Window{win}
		s.Sessions = append(s.Sessions, sess)
	}
	return s
}

func BenchmarkRender(b *testing.B) {
	m := newModel(themed("coral")).Resize(140, 40).Snapshot(bigSnapshot(), false)
	m.previewPane, m.previewText = "%0", strings.Repeat("some pane output line with text\n", 40)
	m.cursor = 1
	m.syncLayout()
	b.ResetTimer()
	for b.Loop() {
		_ = m.View()
	}
}
