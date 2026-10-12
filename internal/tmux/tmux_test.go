package tmux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParseSessionsRoundTrip(t *testing.T) {
	names := []string{"plain", "with space", "dot.ted", "co:lon", "ünïcødé 🙂", "a b.c:d"}
	var b strings.Builder
	for i, n := range names {
		b.WriteString(strings.Join([]string{"$" + string(rune('0'+i)), n, "1791799684", "1", "3", "1791799690"}, "\t"))
		b.WriteString("\n")
	}
	got, err := ParseSessions(b.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(names) {
		t.Fatalf("got %d sessions, want %d", len(got), len(names))
	}
	for i, s := range got {
		if s.Name != names[i] {
			t.Errorf("name %d = %q, want %q", i, s.Name, names[i])
		}
		if s.Windows != 3 || s.Attached != 1 || s.Created.Unix() != 1791799684 || s.Activity.Unix() != 1791799690 {
			t.Errorf("session %d fields = %+v", i, s)
		}
	}
}

func TestParsePanes(t *testing.T) {
	line := strings.Join([]string{"ünï co:de.x", "2", "win name", "1", "%7", "4242", "node", "/home/a b/c", "my\ttitle", "1", "0", "1791799690", "80", "24"}, "\t")
	got, err := ParsePanes(line + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d panes", len(got))
	}
	p := got[0]
	want := Pane{Session: "ünï co:de.x", WindowIndex: 2, WindowName: "win name", Index: 1, ID: "%7", PID: 4242,
		Command: "node", Path: "/home/a b/c", Title: "my\ttitle", Active: true, WindowActivity: time.Unix(1791799690, 0),
		Width: 80, Height: 24}
	if p != want {
		t.Errorf("pane = %+v\nwant %+v", p, want)
	}
	if _, err := ParsePanes("too\tfew\n"); err == nil {
		t.Error("short line parsed without error")
	}
}

func TestArgs(t *testing.T) {
	if got := strings.Join(Runner{Socket: "x"}.Args("ls"), " "); got != "-u -L x ls" {
		t.Errorf("Args = %q", got)
	}
	if got := strings.Join(Runner{Socket: "x", Path: "/p"}.Args("ls"), " "); got != "-u -S /p ls" {
		t.Errorf("Args = %q", got)
	}
}

// privateRunner returns a runner on a fresh tmm-test-<random> socket. Cleanup removes that
// server only, with kill-server on that -L socket.
func privateRunner(t *testing.T) Runner {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	r := Runner{Socket: "tmm-test-" + hex.EncodeToString(b)}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", r.Socket, "kill-server").Run()
	})
	return r
}

func TestNoServer(t *testing.T) {
	r := privateRunner(t)
	ctx := context.Background()
	ss, err := r.ListSessions(ctx)
	if err != nil || len(ss) != 0 {
		t.Fatalf("ListSessions on no server = %v, %v", ss, err)
	}
	ps, err := r.ListPanes(ctx)
	if err != nil || len(ps) != 0 {
		t.Fatalf("ListPanes on no server = %v, %v", ps, err)
	}
	if err := r.KillSession(ctx, "nope"); !errors.Is(err, ErrNoServer) {
		t.Fatalf("KillSession on no server = %v", err)
	}
}

func TestLifecycle(t *testing.T) {
	r := privateRunner(t)
	ctx := context.Background()
	dir := t.TempDir()

	name, err := r.NewSession(ctx, "ünï space", dir, []string{"cat"})
	if err != nil {
		t.Fatal(err)
	}
	if name != "ünï space" {
		t.Fatalf("created %q", name)
	}
	// tmux rewrites "." and ":"; NewSession reports the real name.
	odd, err := r.NewSession(ctx, "a.b:c", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if odd != "a_b_c" {
		t.Errorf("tmux named %q, want a_b_c", odd)
	}

	if err := r.RenameSession(ctx, name, "renamed 🙂"); err != nil {
		t.Fatal(err)
	}
	ss, err := r.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range ss {
		found = found || s.Name == "renamed 🙂"
	}
	if !found {
		t.Fatalf("renamed session missing from %+v", ss)
	}

	panes, err := r.ListPanes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var pane Pane
	for _, p := range panes {
		if p.Session == "renamed 🙂" {
			pane = p
		}
	}
	if pane.ID == "" || pane.Path != dir || pane.PID == 0 {
		t.Fatalf("pane = %+v", pane)
	}
	active, err := r.ActivePane(ctx, "renamed 🙂")
	if err != nil || active != pane.ID {
		t.Fatalf("ActivePane = %q, %v; want %s", active, err, pane.ID)
	}

	if err := r.SendText(ctx, pane.ID, "hello 'quoted' $HOME", true); err != nil {
		t.Fatal(err)
	}
	var screen string
	for i := 0; i < 50; i++ {
		screen, err = r.Capture(ctx, pane.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(screen, "hello 'quoted' $HOME") >= 2 { // typed, then echoed by cat
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Count(screen, "hello 'quoted' $HOME") < 2 {
		t.Fatalf("capture = %q", screen)
	}

	if err := r.KillSession(ctx, "renamed 🙂"); err != nil {
		t.Fatal(err)
	}
	// Exact names: killing "a" must not touch "a_b_c".
	if err := r.KillSession(ctx, "a"); err == nil {
		t.Error("kill of a prefix succeeded")
	}
	ss, _ = r.ListSessions(ctx)
	if len(ss) != 1 || ss[0].Name != "a_b_c" {
		t.Fatalf("after kill: %+v", ss)
	}
}
