package snapshot

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/amirhosseinbanaei/perch/internal/agents"
)

// Event kinds: the changes `log --follow` and the TUI's log view report.
const (
	EventAppeared  = "appeared"
	EventStatus    = "status"
	EventSessionID = "session-id"
	EventExited    = "exited"
)

// Event is one change between two snapshots.
type Event struct {
	Time      time.Time     `json:"time"`
	Kind      string        `json:"kind"`
	Target    string        `json:"target"`
	Pane      string        `json:"pane"`
	Agent     string        `json:"agent"`
	Status    agents.Status `json:"status"`
	From      agents.Status `json:"from,omitempty"`
	SessionID string        `json:"sessionId,omitempty"`
	Title     string        `json:"title,omitempty"`
}

// Diff lists the agent changes from prev to cur, in cur's pane order. A nil prev reports every
// agent as appeared.
func Diff(prev, cur *Snapshot) []Event {
	if cur == nil {
		return nil
	}
	before := map[string]AgentPane{}
	if prev != nil {
		for _, ap := range prev.Agents() {
			before[ap.Pane.ID] = ap
		}
	}
	var out []Event
	seen := map[string]bool{}
	for _, ap := range cur.Agents() {
		a, p := ap.Pane.Agent, ap.Pane
		seen[p.ID] = true
		ev := Event{Time: cur.Taken, Target: p.Target, Pane: p.ID, Agent: a.Agent, Status: a.Status, SessionID: a.SessionID, Title: a.Title}
		old, ok := before[p.ID]
		switch {
		case !ok || old.Pane.Agent.Agent != a.Agent || old.Pane.Agent.PID != a.PID:
			if a.Status == agents.Exited {
				continue // an exited agent this run never saw alive
			}
			ev.Kind = EventAppeared
			out = append(out, ev)
		case old.Pane.Agent.Status != a.Status:
			ev.From = old.Pane.Agent.Status
			ev.Kind = EventStatus
			if a.Status == agents.Exited {
				ev.Kind = EventExited
			}
			out = append(out, ev)
		}
		if ok && old.Pane.Agent.SessionID == "" && a.SessionID != "" && a.Status != agents.Exited {
			ev.Kind, ev.From = EventSessionID, ""
			out = append(out, ev)
		}
	}
	if prev != nil {
		for _, ap := range prev.Agents() {
			a := ap.Pane.Agent
			if seen[ap.Pane.ID] || a.Status == agents.Exited {
				continue
			}
			out = append(out, Event{Time: cur.Taken, Kind: EventExited, Target: ap.Pane.Target, Pane: ap.Pane.ID,
				Agent: a.Agent, Status: agents.Exited, From: a.Status, SessionID: a.SessionID, Title: a.Title})
		}
	}
	return out
}

// ── targets ──────────────────────────────────────────────────────────────────

// ErrNotFound and ErrAmbiguous are the two ways a TARGET fails to resolve.
var (
	ErrNotFound  = errors.New("target not found")
	ErrAmbiguous = errors.New("target is ambiguous")
)

// Target is a resolved TARGET: a session, and a pane when the target named one.
type Target struct {
	Session *Session
	Pane    *Pane // nil for a whole session
}

var paneTargetRE = regexp.MustCompile(`^(.*):(\d+)(?:\.(\d+))?$`)

// Resolve accepts a session name, session:window[.pane], or a pane id %N. A string that matches
// more than one thing is rejected.
func (s *Snapshot) Resolve(target string) (Target, error) {
	if target == "" {
		return Target{}, fmt.Errorf("empty target: %w", ErrNotFound)
	}
	var found []Target
	if target[0] == '%' {
		for i := range s.Sessions {
			for j := range s.Sessions[i].Windows {
				for k := range s.Sessions[i].Windows[j].Panes {
					if p := &s.Sessions[i].Windows[j].Panes[k]; p.ID == target {
						found = append(found, Target{&s.Sessions[i], p})
					}
				}
			}
		}
	}
	if ss := s.Session(target); ss != nil {
		found = append(found, Target{Session: ss})
	}
	if m := paneTargetRE.FindStringSubmatch(target); m != nil {
		if ss := s.Session(m[1]); ss != nil {
			win, _ := strconv.Atoi(m[2])
			for j := range ss.Windows {
				w := &ss.Windows[j]
				if w.Index != win {
					continue
				}
				for k := range w.Panes {
					p := &w.Panes[k]
					if (m[3] == "" && p.Active) || (m[3] != "" && strconv.Itoa(p.Index) == m[3]) {
						found = append(found, Target{ss, p})
					}
				}
			}
		}
	}
	switch len(found) {
	case 0:
		return Target{}, fmt.Errorf("%q: %w", target, ErrNotFound)
	case 1:
		return found[0], nil
	}
	return Target{}, fmt.Errorf("%q names both a session and a pane: %w", target, ErrAmbiguous)
}

// Session returns the session with exactly this name.
func (s *Snapshot) Session(name string) *Session {
	for i := range s.Sessions {
		if s.Sessions[i].Name == name {
			return &s.Sessions[i]
		}
	}
	return nil
}
