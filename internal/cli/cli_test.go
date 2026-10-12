package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/amirhosseinbanaei/perch/internal/tui"
)

func TestTableFitsWidth(t *testing.T) {
	tb := newTable([]string{"TARGET", "AGENT", "STATUS", "SESSION ID", "TITLE", "PATH", "AGE"}, 4, 5)
	tb.add("api:0.0", "claude", tui.Theme{}.Badge("working"), "11111111-2222-4333-8444-555555555555",
		"A very long title that goes on and on about the work in progress", "/home/u/src/some/deeply/nested/project", "3m")
	tb.add("x:1.2", "codex", tui.Theme{}.Badge("waiting"), "—", "short", "~/x", "12s")
	var b bytes.Buffer
	if err := tb.render(&b, tui.Theme{}, 120); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > 120 {
			t.Errorf("line too wide (%d): %q", ansi.StringWidth(l), l)
		}
	}
	if !strings.Contains(lines[1], "…") || !strings.Contains(lines[1], "11111111-2222-4333-8444-555555555555") {
		t.Errorf("flex columns not cut, or the id was: %q", lines[1])
	}
	// Columns line up.
	if strings.Index(lines[0], "AGENT") != strings.Index(lines[1], "claude") {
		t.Errorf("misaligned:\n%s", b.String())
	}
	// No width limit: nothing is cut.
	b.Reset()
	_ = tb.render(&b, tui.Theme{}, 0)
	if strings.Contains(b.String(), "…") {
		t.Errorf("cut without a width: %s", b.String())
	}
}

type run struct {
	code   int
	stdout string
	stderr string
}

func execute(t *testing.T, tty bool, args ...string) run {
	t.Helper()
	var out, errb bytes.Buffer
	env := Env{
		Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
		Getenv: func(k string) string { return map[string]string{"LANG": "C.UTF-8"}[k] },
		IsTTY:  func() bool { return tty }, Width: func() int { return 0 }, Now: time.Now,
	}
	code := Execute(args, env)
	return run{code, out.String(), errb.String()}
}

func privateSocket(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	s := "perch-test-" + hex.EncodeToString(b)
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", s, "kill-server").Run() })
	return s
}

func TestCommands(t *testing.T) {
	s := privateSocket(t)
	if r := execute(t, false, "-L", s, "ls"); r.code != 0 || !strings.Contains(r.stdout, "no tmux sessions") {
		t.Fatalf("ls on no server = %+v", r)
	}
	if r := execute(t, false, "-L", s); r.code != 0 || !strings.Contains(r.stdout, "no tmux sessions") {
		t.Fatalf("no args, not a tty = %+v", r)
	}
	if r := execute(t, false, "-L", s, "new", "job one", "-c", t.TempDir(), "--", "cat"); r.code != 0 {
		t.Fatalf("new = %+v", r)
	}
	r := execute(t, false, "-L", s, "ls", "--json")
	var snap struct {
		Sessions []struct{ Name string } `json:"sessions"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &snap) != nil || len(snap.Sessions) != 1 || snap.Sessions[0].Name != "job one" {
		t.Fatalf("ls --json = %+v", r)
	}
	if r := execute(t, false, "-L", s, "send", "job one", "hello there", "--enter"); r.code != 0 {
		t.Fatalf("send = %+v", r)
	}
	var peek run
	for i := 0; i < 50; i++ {
		peek = execute(t, false, "-L", s, "peek", "job one:0.0", "--lines", "5")
		if strings.Count(peek.stdout, "hello there") >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if peek.code != 0 || strings.Count(peek.stdout, "hello there") < 2 {
		t.Fatalf("peek = %+v", peek)
	}
	if r := execute(t, false, "-L", s, "rename", "job one", "job two"); r.code != 0 {
		t.Fatalf("rename = %+v", r)
	}
	if r := execute(t, false, "-L", s, "kill", "job two"); r.code != ExitUsage {
		t.Fatalf("kill without --yes off a tty = %+v", r)
	}
	if r := execute(t, false, "-L", s, "kill", "job"); r.code != ExitNotFound {
		t.Fatalf("kill of a prefix = %+v", r)
	}
	if r := execute(t, false, "-L", s, "peek", "%999"); r.code != ExitNotFound {
		t.Fatalf("peek of a missing pane = %+v", r)
	}
	if r := execute(t, false, "-L", s, "agents", "--json"); r.code != 0 || strings.TrimSpace(r.stdout) != "[]" {
		t.Fatalf("agents --json = %+v", r)
	}
	if r := execute(t, false, "-L", s, "log", "--json"); r.code != 0 || !strings.Contains(r.stdout, `"sessions": 1`) {
		t.Fatalf("log --json = %+v", r)
	}
	if r := execute(t, false, "-L", s, "kill", "job two", "--yes"); r.code != 0 {
		t.Fatalf("kill --yes = %+v", r)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"bogus"},
		{"ls", "extra"},
		{"agents", "--status", "sleepy"},
		{"agents", "--agent", "copilot"},
		{"--nope"},
		{"new"},
		{"new", "x", "--agent", "claude", "--", "sh"},
		{"-L", "a", "-S", "/b", "ls"},
	} {
		if r := execute(t, false, args...); r.code != ExitUsage {
			t.Errorf("%q exit %d, want %d (%s)", args, r.code, ExitUsage, r.stderr)
		}
	}
	if r := execute(t, false, "version"); r.code != 0 || !strings.HasPrefix(r.stdout, "perch dev") {
		t.Errorf("version = %+v", r)
	}
}
