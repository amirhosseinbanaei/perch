package agents

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMatcher(t *testing.T) {
	table := Table()
	tests := []struct {
		args []string
		want string // detector id, "" for no match
	}{
		// Claude Code
		{[]string{"/root/.local/bin/claude"}, "claude"},
		{[]string{"/root/.vscode-server/extensions/anthropic.claude-code-2.1.296-linux-x64/resources/native-binary/claude", "--output-format", "stream-json"}, "claude"},
		{[]string{"claude", "--resume", "11111111-2222-4333-8444-555555555555"}, "claude"},
		{[]string{"node", "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js"}, "claude"},
		{[]string{"node", "--max-old-space-size=4096", "/home/u/.nvm/versions/node/v24/bin/claude"}, "claude"},
		// Codex
		{[]string{"codex"}, "codex"},
		{[]string{"node", "/usr/local/lib/node_modules/@openai/codex/bin/codex.js", "resume"}, "codex"},
		{[]string{"/usr/local/lib/node_modules/@openai/codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex"}, "codex"},
		{[]string{"/opt/codex-x86_64-unknown-linux-musl"}, "codex"},
		{[]string{"codex-aarch64-apple-darwin", "exec"}, "codex"},
		// Hermes
		{[]string{"/home/u/.local/bin/hermes", "chat"}, "hermes"},
		{[]string{"python3", "/home/u/.venv/bin/hermes"}, "hermes"},
		{[]string{"/usr/bin/python3.12", "-m", "hermes_cli.main"}, "hermes"},
		{[]string{"/store/python", "-I", "-c", "import os, sys, runpy; import hermes_bootstrap; runpy.run_module('hermes_cli.main', run_name='__main__', alter_sys=True)", "--resume", "x"}, "hermes"},
		{[]string{"uv", "run", "hermes"}, "hermes"},
		// Gemini
		{[]string{"gemini"}, "gemini"},
		{[]string{"node", "/usr/local/bin/gemini", "--resume", "latest"}, "gemini"},
		{[]string{"node", "--max-old-space-size=8192", "/usr/lib/node_modules/@google/gemini-cli/bundle/gemini.js"}, "gemini"},
		// OpenCode
		{[]string{"opencode"}, "opencode"},
		{[]string{"/usr/lib/node_modules/opencode-ai/bin/opencode.exe", "-s", "ses_123"}, "opencode"},
		{[]string{"bun", "run", "/x/node_modules/opencode-ai/bin/opencode"}, "opencode"},
		{[]string{"node", "/x/node_modules/opencode-ai/bin/opencode"}, "opencode"},
		// exec -a renames argv[0]: that is what the user sees, and what counts.
		{[]string{"codex", "600"}, "codex"},

		// Negatives: a name anywhere but the start of args never counts.
		{[]string{"grep", "claude"}, ""},
		{[]string{"nvim", "claude.md"}, ""},
		{[]string{"python3", "/root/Chats/.claude/skills/tmux-claude/scripts/tmux-claude.py", "start"}, ""},
		{[]string{"python3", "tmux-claude.py"}, ""},
		{[]string{"bash", "-c", "claude"}, ""},
		{[]string{"less", "/var/log/codex.log"}, ""},
		{[]string{"node", "/srv/app/claude.js"}, ""},
		{[]string{"node", "/srv/app/server.js", "--name", "gemini"}, ""},
		{[]string{"node", "/srv/node_modules/@openai/codex-like/bin/x.js"}, ""},
		{[]string{"python3", "-c", "print('hermes_cli.main')"}, ""},
		{[]string{"python3", "-m", "hermes_clix"}, ""},
		{[]string{"tmux", "new-session", "-s", "claude"}, ""},
		{[]string{"sleep", "600"}, ""},
		{[]string{"codex-wrapper"}, ""},
		{[]string{"claude-desktop"}, ""},
		{nil, ""},
	}
	for _, tt := range tests {
		got := ""
		for _, d := range table {
			if d.Match(Proc{Args: tt.args}) {
				got = d.ID
				break
			}
		}
		if got != tt.want {
			t.Errorf("match %q = %q, want %q", tt.args, got, tt.want)
		}
	}
}

func TestDescendantsNearestFirst(t *testing.T) {
	pt := NewProcTable(
		Proc{PID: 10, PPID: 1, Args: []string{"zsh"}},
		Proc{PID: 20, PPID: 10, Args: []string{"bash", "launch.sh"}},
		Proc{PID: 21, PPID: 10, Args: []string{"grep", "claude"}},
		Proc{PID: 30, PPID: 20, Args: []string{"claude"}},
		Proc{PID: 40, PPID: 30, Args: []string{"node", "/x/@openai/codex/bin/codex.js"}},
	)
	if got := pt.Descendants(10); len(got) != 5 || got[0] != 10 || got[3] != 30 || got[4] != 40 {
		t.Fatalf("Descendants = %v", got)
	}
	in := &Inspector{Home: t.TempDir(), Now: fixedNow}
	info := in.Detect(context.Background(), Table(), pt, 10, Pane{ID: "%1", Path: "/p", Title: "host"})
	if info == nil || info.Agent != "claude" || info.PID != 30 {
		t.Fatalf("Detect = %+v", info)
	}
	if info.Source["agent"] != SrcArgv || !strings.HasPrefix(info.Source["sessionId"], "none (") {
		t.Errorf("Source = %v", info.Source)
	}
	if in.Detect(context.Background(), Table(), pt, 999, Pane{}) != nil {
		t.Error("unknown pane pid matched")
	}
}

func TestParsePSAndStat(t *testing.T) {
	pt := ParsePS("    1     0 /sbin/init splash\n  200     1 /root/.local/bin/claude --resume x\n garbage\n")
	p, ok := pt.Get(200)
	if !ok || p.PPID != 1 || p.Args[0] != "/root/.local/bin/claude" {
		t.Fatalf("Get(200) = %+v, %v", p, ok)
	}
	stat := "1234 (my (odd) name) S 1 1234 1234 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 172825673 1000 50"
	if got := statField(stat, 22); got != "172825673" {
		t.Errorf("statField = %q", got)
	}
	for in, want := range map[string]time.Duration{"05:03": 303 * time.Second, "01:00:00": time.Hour, "2-00:00:01": 48*time.Hour + time.Second} {
		if got, ok := ParseEtime(in); !ok || got != want {
			t.Errorf("ParseEtime(%q) = %v, %v", in, got, ok)
		}
	}
	if _, ok := ParseEtime("x"); ok {
		t.Error("ParseEtime accepted garbage")
	}
}

// ── fixtures ─────────────────────────────────────────────────────────────────

var fixedNow = func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.Local) }

// fixture copies testdata/<dir> into a temp dir and sets every file's mtime to mtime.
func fixture(t *testing.T, dir string, mtime time.Time) string {
	t.Helper()
	dst := t.TempDir()
	src := filepath.Join("testdata", dir)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, b, 0o644); err != nil {
			return err
		}
		return os.Chtimes(target, mtime, mtime)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func envOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func screenOfText(s string) func(context.Context) string {
	return func(context.Context) string { return s }
}

func TestClaudeRegistryAndTitle(t *testing.T) {
	dir := fixture(t, "claude", fixedNow().Add(-time.Hour))
	in := &Inspector{Home: "/nonexistent", Getenv: envOf(map[string]string{"CLAUDE_CONFIG_DIR": dir}), Now: fixedNow}
	if in.ClaudeDir() != dir {
		t.Fatalf("ClaudeDir = %s", in.ClaudeDir())
	}
	p := Proc{PID: 4242, Args: []string{"claude"}, StartedAt: fixedNow().Add(-time.Hour), StartTicks: "172825673"}
	info := enrichClaude(context.Background(), in, p, Pane{Title: "host", Activity: fixedNow()})
	if info.SessionID != "11111111-2222-4333-8444-555555555555" || info.Status != Working || info.Path != "/work/proj" {
		t.Fatalf("info = %+v", info)
	}
	if info.Title != "Release prep" || info.Source["title"] != SrcStateDir {
		t.Errorf("title = %q from %s, want the last customTitle", info.Title, info.Source["title"])
	}
	if info.Source["status"] != SrcRegistry || info.Confidence != High {
		t.Errorf("source = %v %s", info.Source, info.Confidence)
	}

	// A registry whose procStart differs belongs to an earlier process with the same pid.
	stale := p
	stale.StartTicks = "999"
	if info := enrichClaude(context.Background(), in, stale, Pane{}); info.SessionID != "" {
		t.Errorf("stale registry used: %+v", info)
	}

	// No transcript: the registry name is the title.
	other := enrichClaude(context.Background(), in, Proc{PID: 4343, Args: []string{"claude"}}, Pane{Title: "host"})
	if other.Title != "other-2" || other.Source["title"] != SrcRegistry || other.Status != Idle {
		t.Errorf("other = %+v", other)
	}

	// A dialog on screen overrides the registry.
	trust := " Accessing workspace:\n /tmp/x\n Is this a project you created or one you trust?\n ❯ No, exit\n   Yes, I trust this folder\n Enter to confirm · Esc to cancel\n"
	w := enrichClaude(context.Background(), in, p, Pane{Screen: screenOfText(trust)})
	if w.Status != Waiting || w.Source["status"] != SrcScreen || !strings.HasPrefix(w.Note, "trust") {
		t.Errorf("waiting = %+v", w)
	}

	// No registry yet, process just started: starting. Later: unknown → activity, low.
	young := Proc{PID: 1, Args: []string{"claude"}, StartedAt: fixedNow().Add(-2 * time.Second)}
	if s := enrichClaude(context.Background(), in, young, Pane{}); s.Status != Starting {
		t.Errorf("young = %+v", s)
	}
	old := Proc{PID: 1, Args: []string{"claude", "--resume", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}, StartedAt: fixedNow().Add(-time.Minute)}
	s := enrichClaude(context.Background(), in, old, Pane{Activity: fixedNow().Add(-time.Second), Path: "/pane"})
	if s.Status != Working || s.Confidence != Low || s.SessionID != "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" || s.Source["sessionId"] != SrcArgv || s.Path != "/pane" {
		t.Errorf("old = %+v", s)
	}
}

func TestClaudeTitleTailExtends(t *testing.T) {
	dir := t.TempDir()
	id := "22222222-2222-4333-8444-555555555555"
	proj := filepath.Join(dir, "projects", "-x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(`{"type":"ai-title","aiTitle":"Early title"}` + "\n")
	filler := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", 1000) + `"}}` + "\n"
	for b.Len() < 300<<10 {
		b.WriteString(filler)
	}
	path := filepath.Join(proj, id+".jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	in := &Inspector{Getenv: envOf(map[string]string{"CLAUDE_CONFIG_DIR": dir}), Now: fixedNow}
	if got, ok := in.claudeTitle(id); !ok || got != "Early title" {
		t.Fatalf("title = %q, %v", got, ok)
	}
	// Cached until the file changes.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"type":"custom-title","customTitle":"Set by user"}` + "\n")
	_ = f.Close()
	if got, _ := in.claudeTitle(id); got != "Set by user" {
		t.Fatalf("after append title = %q", got)
	}
}

func TestClaudeDialogs(t *testing.T) {
	tests := map[string]string{
		" Read file\n Read(/etc/hostname)\n Do you want to proceed?\n ❯ 1. Yes\n   2. Yes, allow reading from /etc during this session\n   3. No\n": "outside",
		" Bash command\n rm -rf build\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n":                                                             "permission",
		" Enable auto mode?\n ❯ 1. Yes\n   2. No\n":                                                                                                 "auto-mode",
		"> Do you want to proceed? was asked earlier, answered\n> next prompt\n":                                                                    "",
		"": "",
	}
	for screen, want := range tests {
		kind, _, ok := ClaudeDialog(screen)
		if kind != want || ok != (want != "") {
			t.Errorf("ClaudeDialog(%q) = %q, %v; want %q", screen, kind, ok, want)
		}
	}
}

func TestCodexState(t *testing.T) {
	home := fixture(t, "codex", fixedNow().Add(-time.Hour))
	in := &Inspector{Getenv: envOf(map[string]string{"CODEX_HOME": home}), Now: fixedNow}
	d, _ := Lookup(Table(), "codex")
	p := Proc{PID: 7, Args: []string{"codex"}, Cwd: "/work/proj", StartedAt: fixedNow().Add(-3 * time.Hour)}
	info := d.Enrich(context.Background(), in, p, Pane{Title: "host", Activity: fixedNow().Add(-time.Minute)})
	if info.SessionID != "0199aaaa-1111-7222-8333-444455556666" || info.Source["sessionId"] != SrcStateDir {
		t.Fatalf("info = %+v", info)
	}
	if info.Title != "Renamed thread" || info.Status != Idle || info.Confidence != Low {
		t.Errorf("info = %+v", info)
	}
	// Started after the rollout was written: never guess an older session.
	late := p
	late.StartedAt = fixedNow().Add(-time.Minute)
	if info := d.Enrich(context.Background(), in, late, Pane{}); info.SessionID != "" || info.Source["sessionId"] == "" {
		t.Errorf("late = %+v", info)
	}
	// argv wins.
	argv := p
	argv.Args = []string{"codex", "resume", "0199cccc-1111-7222-8333-444455556666"}
	if info := d.Enrich(context.Background(), in, argv, Pane{}); info.SessionID != "0199cccc-1111-7222-8333-444455556666" || info.Source["sessionId"] != SrcArgv {
		t.Errorf("argv = %+v", info)
	}
	// Screen: working footer, then an approval dialog.
	if info := d.Enrich(context.Background(), in, p, Pane{Screen: screenOfText("• Working (12s • esc to interrupt)\n")}); info.Status != Working || info.Confidence != High {
		t.Errorf("working = %+v", info)
	}
	dialog := "Would you like to run the following command?\n  $ rm -rf build\n› 1. Yes, proceed\n  2. No, and tell Codex what to do differently\nPress enter to confirm or esc to cancel\n"
	if info := d.Enrich(context.Background(), in, p, Pane{Screen: screenOfText(dialog)}); info.Status != Waiting {
		t.Errorf("waiting = %+v", info)
	}
}

func TestHermesState(t *testing.T) {
	home := fixture(t, "hermes", fixedNow())
	in := &Inspector{Getenv: envOf(map[string]string{"HERMES_HOME": home}), Now: fixedNow}
	d, _ := Lookup(Table(), "hermes")
	info := d.Enrich(context.Background(), in, Proc{PID: 5151, Args: []string{"hermes"}}, Pane{Title: "t"})
	if info.SessionID != "20261012_091502_a1b2c3" || info.Source["sessionId"] != SrcStateDir {
		t.Errorf("cli = %+v", info)
	}
	// TUI mode: the registry names the gateway grandchild, with a live session id.
	info = d.Enrich(context.Background(), in, Proc{PID: 5300, Args: []string{"hermes"}, Descendants: []int{5301, 5353}}, Pane{})
	if info.SessionID != "20261012_100001_abcdef" {
		t.Errorf("tui = %+v", info)
	}
	info = d.Enrich(context.Background(), in, Proc{PID: 1, Args: []string{"hermes", "--resume", "latest"}}, Pane{})
	if info.SessionID != "" || !strings.Contains(info.Source["sessionId"], "pid not in") {
		t.Errorf("unknown = %+v", info)
	}
	dialog := "⚠️  Dangerous Command\n  rm -rf /tmp/x\n ❯ Allow once\n   Allow for this session\n   Deny\n"
	if info := d.Enrich(context.Background(), in, Proc{PID: 5151}, Pane{Screen: screenOfText(dialog)}); info.Status != Waiting {
		t.Errorf("waiting = %+v", info)
	}
}

func TestGeminiState(t *testing.T) {
	home := fixture(t, "gemini-home", fixedNow().Add(-time.Hour))
	in := &Inspector{Home: "/nonexistent", Getenv: envOf(map[string]string{"GEMINI_CLI_HOME": home}), Now: fixedNow}
	d, _ := Lookup(Table(), "gemini")
	p := Proc{PID: 8, Args: []string{"node", "/usr/local/bin/gemini"}, Cwd: "/work/proj", StartedAt: fixedNow().Add(-2 * time.Hour)}
	info := d.Enrich(context.Background(), in, p, Pane{Screen: screenOfText("⠋ Thinking... (esc to cancel, 12s)\n")})
	if info.SessionID != "9a8b7c6d-1111-4222-8333-444455556666" || info.Title != "Refactor the parser" || info.Status != Working {
		t.Fatalf("info = %+v", info)
	}
	other := p
	other.Cwd = "/work/other"
	if info := d.Enrich(context.Background(), in, other, Pane{}); info.SessionID != "" {
		t.Errorf("other cwd got %+v", info)
	}
	dialog := "Action Required\n Allow execution of [Shell]?\n ● 1. Allow once\n   2. Allow for this session\n"
	if info := d.Enrich(context.Background(), in, p, Pane{Screen: screenOfText(dialog)}); info.Status != Waiting {
		t.Errorf("waiting = %+v", info)
	}
}

func TestOpencode(t *testing.T) {
	in := &Inspector{Home: "/h", Getenv: envOf(map[string]string{"XDG_DATA_HOME": "/data"}), Now: fixedNow}
	d, _ := Lookup(Table(), "opencode")
	info := d.Enrich(context.Background(), in, Proc{PID: 9, Args: []string{"opencode", "-s", "ses_01JABCDEFGHJKMNPQRSTVWXYZ"}}, Pane{Screen: screenOfText("esc interrupt\n")})
	if info.SessionID != "ses_01JABCDEFGHJKMNPQRSTVWXYZ" || info.Status != Working {
		t.Errorf("info = %+v", info)
	}
	info = d.Enrich(context.Background(), in, Proc{PID: 9, Args: []string{"opencode"}}, Pane{Screen: screenOfText("△ Permission required\n  Shell command\n  Allow once   Allow always   Reject\n")})
	if info.SessionID != "" || !strings.Contains(info.Source["sessionId"], "/data/opencode/opencode.db") || info.Status != Waiting {
		t.Errorf("info = %+v", info)
	}
}
