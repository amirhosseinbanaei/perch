package snapshot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/amirhosseinbanaei/perch/internal/agents"
	"github.com/amirhosseinbanaei/perch/internal/tmux"
)

func sample() *Snapshot {
	t0 := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	return &Snapshot{Taken: t0, Server: Server{Socket: "default", Running: true}, Sessions: assemble(
		[]tmux.Session{{ID: "$0", Name: "work"}, {ID: "$1", Name: "work:1"}},
		[]tmux.Pane{
			{Session: "work", WindowIndex: 1, Index: 0, ID: "%2", Active: true},
			{Session: "work", WindowIndex: 0, Index: 1, ID: "%1", Active: true},
			{Session: "work", WindowIndex: 0, Index: 0, ID: "%0"},
			{Session: "work:1", WindowIndex: 0, Index: 0, ID: "%3", Active: true},
			{Session: "gone", WindowIndex: 0, Index: 0, ID: "%9"},
		})}
}

func TestAssemble(t *testing.T) {
	s := sample()
	if len(s.Sessions) != 2 || len(s.Sessions[0].Windows) != 2 {
		t.Fatalf("sessions = %+v", s.Sessions)
	}
	w0 := s.Sessions[0].Windows[0]
	if w0.Index != 0 || w0.Panes[0].ID != "%0" || w0.Panes[1].Target != "work:0.1" {
		t.Errorf("window 0 = %+v", w0)
	}
	if p := s.Sessions[0].ActivePane(); p.ID != "%1" {
		t.Errorf("ActivePane = %s", p.ID)
	}
	if _, err := json.Marshal(s); err != nil {
		t.Fatal(err)
	}
}

func TestResolve(t *testing.T) {
	s := sample()
	cases := []struct {
		target  string
		session string
		pane    string
		err     error
	}{
		{"work", "work", "", nil},
		{"%2", "work", "%2", nil},
		{"work:0.0", "work", "%0", nil},
		{"work:0", "work", "%1", nil}, // a window means its active pane
		{"nope", "", "", ErrNotFound},
		{"work:7.0", "", "", ErrNotFound},
		{"%99", "", "", ErrNotFound},
		{"work:1", "", "", ErrAmbiguous}, // a session named "work:1" and window 1 of "work"
	}
	for _, c := range cases {
		got, err := s.Resolve(c.target)
		if c.err != nil {
			if !errors.Is(err, c.err) {
				t.Errorf("Resolve(%q) err = %v, want %v", c.target, err, c.err)
			}
			continue
		}
		if err != nil || got.Session.Name != c.session || (c.pane != "" && got.Pane.ID != c.pane) || (c.pane == "" && got.Pane != nil) {
			t.Errorf("Resolve(%q) = %+v, %v", c.target, got, err)
		}
	}
}

func withAgent(s *Snapshot, paneID string, info agents.Info) {
	for i := range s.Sessions {
		for j := range s.Sessions[i].Windows {
			for k := range s.Sessions[i].Windows[j].Panes {
				if p := &s.Sessions[i].Windows[j].Panes[k]; p.ID == paneID {
					in := info
					p.Agent = &in
				}
			}
		}
	}
}

func TestDiff(t *testing.T) {
	a := sample()
	withAgent(a, "%0", agents.Info{Agent: "claude", PID: 10, Status: agents.Starting})
	withAgent(a, "%2", agents.Info{Agent: "codex", PID: 20, Status: agents.Idle})
	ev := Diff(nil, a)
	if len(ev) != 2 || ev[0].Kind != EventAppeared {
		t.Fatalf("first diff = %+v", ev)
	}
	b := sample()
	b.Taken = a.Taken.Add(2 * time.Second)
	withAgent(b, "%0", agents.Info{Agent: "claude", PID: 10, Status: agents.Working, SessionID: "abc"})
	ev = Diff(a, b)
	kinds := map[string]int{}
	for _, e := range ev {
		kinds[e.Kind]++
	}
	if kinds[EventStatus] != 1 || kinds[EventSessionID] != 1 || kinds[EventExited] != 1 || len(ev) != 3 {
		t.Fatalf("diff = %+v", ev)
	}
	if len(Diff(b, b)) != 0 {
		t.Error("no change reported changes")
	}
}

func TestTrackExited(t *testing.T) {
	c := New(tmux.Runner{}, Options{})
	t0 := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	a := sample()
	withAgent(a, "%0", agents.Info{Agent: "claude", PID: 10, Status: agents.Working, SessionID: "sid", Source: map[string]string{"status": "registry"}})
	c.track(a, t0)

	b := sample() // the pane is still there, the agent is not
	c.track(b, t0.Add(2*time.Second))
	p := b.Sessions[0].Windows[0].Panes[0]
	if p.Agent == nil || p.Agent.Status != agents.Exited || p.Agent.SessionID != "sid" {
		t.Fatalf("after exit: %+v", p.Agent)
	}
	if a.Sessions[0].Windows[0].Panes[0].Agent.Source["status"] != "registry" {
		t.Error("track changed the previous snapshot's source map")
	}
	c2 := sample()
	c.track(c2, t0.Add(30*time.Second))
	if c2.Sessions[0].Windows[0].Panes[0].Agent == nil {
		t.Fatal("exited agent dropped before 60 s")
	}
	d := sample()
	c.track(d, t0.Add(63*time.Second))
	if d.Sessions[0].Windows[0].Panes[0].Agent != nil {
		t.Fatal("exited agent kept after 60 s")
	}
}

// TestCollectPrivateServer runs a fake agent on a private socket and finds it.
func TestCollectPrivateServer(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	r := tmux.Runner{Socket: "perch-test-" + hex.EncodeToString(b)}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", r.Socket, "kill-server").Run() })
	ctx := context.Background()
	c := New(r, Options{Inspector: &agents.Inspector{Home: t.TempDir(), Getenv: func(string) string { return "" }, Now: time.Now}})

	s, err := c.Collect(ctx)
	if err != nil || s.Server.Running || len(s.Sessions) != 0 {
		t.Fatalf("empty server: %+v, %v", s, err)
	}
	if _, err := c.NewSession(ctx, "fake codex", t.TempDir(), []string{"bash", "-c", "exec -a codex sleep 600"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.NewSession(ctx, "plain", "", nil); err != nil {
		t.Fatal(err)
	}
	var found *agents.Info
	for i := 0; i < 50 && found == nil; i++ {
		s, err = c.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ag := s.Agents(); len(ag) == 1 && ag[0].Pane.Agent.Agent == "codex" {
			found = ag[0].Pane.Agent
		}
		time.Sleep(20 * time.Millisecond)
	}
	if found == nil {
		t.Fatalf("fake codex not found in %+v", s)
	}
	if found.SessionID != "" || found.Source["agent"] != agents.SrcArgv {
		t.Errorf("codex = %+v", found)
	}
	if len(s.Sessions) != 2 || !s.Server.Running {
		t.Errorf("sessions = %+v", s.Sessions)
	}
	if err := c.KillSession(ctx, "fake codex"); err != nil {
		t.Fatal(err)
	}
	s, _ = c.Collect(ctx)
	if len(s.Sessions) != 1 || len(s.Agents()) != 0 {
		t.Errorf("after kill: %+v", s.Sessions)
	}
}
