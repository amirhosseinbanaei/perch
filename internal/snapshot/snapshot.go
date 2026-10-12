// Package snapshot is the one model the TUI and the CLI read. A snapshot is two tmux calls
// (list-sessions and list-panes -a, however many sessions exist), one ps run, and one visible
// screen capture per agent pane, run concurrently. `tmux-manager ls --json` prints the same
// Snapshot struct the TUI draws.
//
// The Collector is also the only way the TUI and the CLI act on tmux: it passes exact-name and
// pane-id actions through to internal/tmux.
package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/amirhosseinbanaei/tmux-manager-cli/internal/agents"
	"github.com/amirhosseinbanaei/tmux-manager-cli/internal/tmux"
)

// Snapshot is the whole tmux server at one moment.
type Snapshot struct {
	Taken    time.Time `json:"taken"`
	Server   Server    `json:"server"`
	Sessions []Session `json:"sessions"`
}

// Server says which tmux server was read.
type Server struct {
	Socket  string `json:"socket"` // "default", "-L name" or "-S path"
	Running bool   `json:"running"`
}

// Session is one tmux session.
type Session struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Created  time.Time `json:"created"`
	Attached int       `json:"attached"`
	Activity time.Time `json:"activity"`
	Windows  []Window  `json:"windows"`
}

// Window is one window of a session.
type Window struct {
	Index    int       `json:"index"`
	Name     string    `json:"name"`
	Activity time.Time `json:"activity"`
	Panes    []Pane    `json:"panes"`
}

// Pane is one pane, with the agent running in it, if any.
type Pane struct {
	ID      string       `json:"id"`
	Index   int          `json:"index"`
	Target  string       `json:"target"` // session:window.pane
	PID     int          `json:"pid"`
	Command string       `json:"command"`
	Path    string       `json:"path"`
	Title   string       `json:"title"`
	Active  bool         `json:"active"`
	Dead    bool         `json:"dead"`
	Width   int          `json:"width"`
	Height  int          `json:"height"`
	Agent   *agents.Info `json:"agent"`
}

// AgentPane is a pane with an agent, with its place in the tree.
type AgentPane struct {
	Session *Session
	Window  *Window
	Pane    *Pane
}

// Agents lists every pane with an agent, in session/window/pane order.
func (s *Snapshot) Agents() []AgentPane {
	var out []AgentPane
	for i := range s.Sessions {
		ss := &s.Sessions[i]
		for j := range ss.Windows {
			w := &ss.Windows[j]
			for k := range w.Panes {
				if w.Panes[k].Agent != nil {
					out = append(out, AgentPane{Session: ss, Window: w, Pane: &w.Panes[k]})
				}
			}
		}
	}
	return out
}

// SessionAgents lists the agent panes of one session.
func (s *Session) SessionAgents() []*Pane {
	var out []*Pane
	for j := range s.Windows {
		for k := range s.Windows[j].Panes {
			if s.Windows[j].Panes[k].Agent != nil {
				out = append(out, &s.Windows[j].Panes[k])
			}
		}
	}
	return out
}

// ActivePane is the session's best guess at a current pane: the active pane of its
// lowest-numbered window with one, else its first pane.
func (s *Session) ActivePane() *Pane {
	var first *Pane
	for j := range s.Windows {
		for k := range s.Windows[j].Panes {
			p := &s.Windows[j].Panes[k]
			if first == nil {
				first = p
			}
			if p.Active {
				return p
			}
		}
	}
	return first
}

// exitedKeep is how long an exited agent stays listed.
const exitedKeep = 60 * time.Second

// Collector takes snapshots. It is safe for concurrent use: it remembers the previous
// refresh's agents so that one which disappears from a live pane is shown as exited for 60 s.
type Collector struct {
	tmux      tmux.Runner
	inspector *agents.Inspector
	table     []agents.Detector
	listProcs func(context.Context) (*agents.ProcTable, error)
	now       func() time.Time

	mu     sync.Mutex
	last   map[string]agents.Info // pane id → the live agent seen last refresh
	exited map[string]agents.Info // pane id → an exited agent still shown
}

// Options adjust a Collector, mostly for tests.
type Options struct {
	Inspector *agents.Inspector
	Table     []agents.Detector
	ListProcs func(context.Context) (*agents.ProcTable, error)
	Now       func() time.Time
}

// New returns a Collector for one tmux server.
func New(r tmux.Runner, o Options) *Collector {
	c := &Collector{tmux: r, inspector: o.Inspector, table: o.Table, listProcs: o.ListProcs, now: o.Now}
	if c.inspector == nil {
		c.inspector = agents.NewInspector()
	}
	if c.table == nil {
		c.table = agents.Table()
	}
	if c.listProcs == nil {
		c.listProcs = agents.ListProcs
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c
}

// Table is the detector table in use.
func (c *Collector) Table() []agents.Detector { return c.table }

// Inspector is the agent inspector in use.
func (c *Collector) Inspector() *agents.Inspector { return c.inspector }

// SocketLabel names the server: "default", "-L name" or "-S path".
func (c *Collector) SocketLabel() string {
	switch {
	case c.tmux.Path != "":
		return "-S " + c.tmux.Path
	case c.tmux.Socket != "":
		return "-L " + c.tmux.Socket
	}
	return "default"
}

// workers bounds the concurrent pane inspections (each may run one capture-pane).
const workers = 8

// Collect takes one snapshot.
func (c *Collector) Collect(ctx context.Context) (*Snapshot, error) {
	var (
		sessions []tmux.Session
		panes    []tmux.Pane
		procs    *agents.ProcTable
		errS     error
		errP     error
		errPS    error
		wg       sync.WaitGroup
	)
	wg.Add(3)
	go func() { defer wg.Done(); sessions, errS = c.tmux.ListSessions(ctx) }()
	go func() { defer wg.Done(); panes, errP = c.tmux.ListPanes(ctx) }()
	go func() { defer wg.Done(); procs, errPS = c.listProcs(ctx) }()
	wg.Wait()
	if err := errors.Join(errS, errP); err != nil {
		return nil, fmt.Errorf("read tmux: %w", err)
	}
	now := c.now()
	snap := &Snapshot{Taken: now, Server: Server{Socket: c.SocketLabel(), Running: len(sessions) > 0}}
	snap.Sessions = assemble(sessions, panes)

	if errPS == nil {
		c.detect(ctx, snap, procs)
	}
	c.track(snap, now)
	if errPS != nil {
		return snap, fmt.Errorf("list processes: %w", errPS)
	}
	return snap, nil
}

// assemble builds the session → window → pane tree from the two tmux listings.
func assemble(sessions []tmux.Session, panes []tmux.Pane) []Session {
	idx := map[string]int{}
	out := make([]Session, 0, len(sessions))
	for _, s := range sessions {
		idx[s.Name] = len(out)
		out = append(out, Session{ID: s.ID, Name: s.Name, Created: s.Created, Attached: s.Attached, Activity: s.Activity})
	}
	for _, p := range panes {
		i, ok := idx[p.Session]
		if !ok { // a session created between the two calls
			continue
		}
		s := &out[i]
		var w *Window
		for j := range s.Windows {
			if s.Windows[j].Index == p.WindowIndex {
				w = &s.Windows[j]
			}
		}
		if w == nil {
			s.Windows = append(s.Windows, Window{Index: p.WindowIndex, Name: p.WindowName, Activity: p.WindowActivity})
			w = &s.Windows[len(s.Windows)-1]
		}
		w.Panes = append(w.Panes, Pane{
			ID: p.ID, Index: p.Index, PID: p.PID, Command: p.Command, Path: p.Path, Title: p.Title,
			Active: p.Active, Dead: p.Dead, Width: p.Width, Height: p.Height,
			Target: p.Session + ":" + strconv.Itoa(p.WindowIndex) + "." + strconv.Itoa(p.Index),
		})
	}
	for i := range out {
		ws := out[i].Windows
		sort.Slice(ws, func(a, b int) bool { return ws[a].Index < ws[b].Index })
		for j := range ws {
			ps := ws[j].Panes
			sort.Slice(ps, func(a, b int) bool { return ps[a].Index < ps[b].Index })
		}
	}
	return out
}

// detect runs the detector table on every live pane, a bounded number at a time.
func (c *Collector) detect(ctx context.Context, snap *Snapshot, procs *agents.ProcTable) {
	type job struct {
		pane     *Pane
		activity time.Time
	}
	var jobs []job
	for i := range snap.Sessions {
		for j := range snap.Sessions[i].Windows {
			w := &snap.Sessions[i].Windows[j]
			for k := range w.Panes {
				if !w.Panes[k].Dead && w.Panes[k].PID > 0 {
					jobs = append(jobs, job{&w.Panes[k], w.Activity})
				}
			}
		}
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, jb := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(jb job) {
			defer func() { <-sem; wg.Done() }()
			p := jb.pane
			pane := agents.Pane{ID: p.ID, Path: p.Path, Title: p.Title, Activity: jb.activity, Screen: c.screen(p.ID)}
			p.Agent = c.inspector.Detect(ctx, c.table, procs, p.PID, pane)
		}(jb)
	}
	wg.Wait()
}

// screen captures a pane's visible screen at most once per refresh.
func (c *Collector) screen(paneID string) func(context.Context) string {
	var once sync.Once
	var out string
	return func(ctx context.Context) string {
		once.Do(func() {
			s, err := c.tmux.Capture(ctx, paneID, 0)
			if err == nil {
				out = s
			}
		})
		return out
	}
}

// track marks agents that vanished from a still-open pane as exited, keeps them for 60 s, and
// remembers this refresh's agents for the next.
func (c *Collector) track(snap *Snapshot, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	live := map[string]agents.Info{}
	exited := map[string]agents.Info{}
	for i := range snap.Sessions {
		for j := range snap.Sessions[i].Windows {
			w := &snap.Sessions[i].Windows[j]
			for k := range w.Panes {
				p := &w.Panes[k]
				if p.Agent != nil {
					live[p.ID] = *p.Agent
					continue
				}
				if prev, ok := c.last[p.ID]; ok {
					t := now
					prev.Status, prev.ExitedAt = agents.Exited, &t
					prev.Source = copySource(prev.Source)
					prev.Source["status"] = agents.SrcActivity
					prev.Note = "the agent process ended; the pane is still open"
					exited[p.ID] = prev
				} else if prev, ok := c.exited[p.ID]; ok && now.Sub(*prev.ExitedAt) < exitedKeep {
					exited[p.ID] = prev
				}
				if e, ok := exited[p.ID]; ok {
					e := e
					p.Agent = &e
				}
			}
		}
	}
	c.last, c.exited = live, exited
}

func copySource(m map[string]string) map[string]string {
	out := make(map[string]string, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ── actions: the only way the TUI and the CLI touch tmux ─────────────────────

// Capture returns a pane's screen plus history lines of scrollback.
func (c *Collector) Capture(ctx context.Context, paneID string, history int) (string, error) {
	return c.tmux.Capture(ctx, paneID, history)
}

// NewSession creates a detached session and returns the name tmux gave it.
func (c *Collector) NewSession(ctx context.Context, name, dir string, cmd []string) (string, error) {
	return c.tmux.NewSession(ctx, name, dir, cmd)
}

// RenameSession renames one session by its exact name.
func (c *Collector) RenameSession(ctx context.Context, oldName, newName string) error {
	return c.tmux.RenameSession(ctx, oldName, newName)
}

// KillSession closes one session by its exact name.
func (c *Collector) KillSession(ctx context.Context, name string) error {
	return c.tmux.KillSession(ctx, name)
}

// SendText types text into a pane, then Enter when enter is set.
func (c *Collector) SendText(ctx context.Context, paneID, text string, enter bool) error {
	return c.tmux.SendText(ctx, paneID, text, enter)
}

// ActivePane asks tmux for a session's current pane.
func (c *Collector) ActivePane(ctx context.Context, session string) (string, error) {
	return c.tmux.ActivePane(ctx, session)
}

// AttachCommand is switch-client inside a client of the same server (inPlace), else
// attach-session.
func (c *Collector) AttachCommand(ctx context.Context, session string) (cmd *exec.Cmd, inPlace bool) {
	return c.tmux.AttachCommand(ctx, session)
}

// TmuxVersion is `tmux -V`.
func (c *Collector) TmuxVersion(ctx context.Context) (string, error) { return c.tmux.Version(ctx) }

// SocketPath is the running server's socket path.
func (c *Collector) SocketPath(ctx context.Context) (string, error) { return c.tmux.SocketPath(ctx) }
