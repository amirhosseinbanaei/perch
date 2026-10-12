package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amirhosseinbanaei/perch/internal/agents"
	"github.com/amirhosseinbanaei/perch/internal/snapshot"
)

// Motion: a spinner on working agents, a 3 s pulse on a row that turns waiting, and 4 s
// toasts. The animation tick runs only while something animates; the 2 s refresh tick
// (tickCmd) stays the only thing that collects a snapshot.
const (
	frameEvery = 100 * time.Millisecond
	pulseStep  = 300 * time.Millisecond
	pulseFor   = 3 * time.Second
	toastFor   = 4 * time.Second
)

type (
	animMsg  struct{}
	toastMsg struct{ id int }
)

type toastKind int

const (
	toastInfo toastKind = iota
	toastWaiting
	toastError
)

type toast struct {
	text string
	kind toastKind
}

func spinnerFrames(ascii bool) []string {
	if ascii {
		return []string{"|", "/", "-", "\\"}
	}
	return []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
}

// animOn reports whether anything may move: not with --no-anim, and not without colour.
func (m Model) animOn() bool { return !m.opts.NoAnim && m.theme.Color }

func (m Model) spinnerFrame() string {
	f := spinnerFrames(m.theme.ASCII)
	return f[m.frame%len(f)]
}

// sym is the status symbol, or the spinner frame for a working agent while animation is on.
func (m Model) sym(s agents.Status) string {
	if s == agents.Working && m.animOn() {
		return m.spinnerFrame()
	}
	return m.theme.Symbol(s)
}

// pulseOn reports whether a pane that just turned waiting is in the bright half of its pulse.
func (m Model) pulseOn(pane string, now time.Time) bool {
	start, ok := m.pulses[pane]
	if !ok || !m.animOn() {
		return false
	}
	d := now.Sub(start)
	return d >= 0 && d < pulseFor && (d/pulseStep)%2 == 0
}

// anyWorking reports a working agent on screen. The header counts every agent, so any working
// agent in the snapshot is visible there.
func (m Model) anyWorking() bool {
	if m.snap == nil {
		return false
	}
	for _, a := range m.snap.Agents() {
		if a.Pane.Agent.Status == agents.Working {
			return true
		}
	}
	return false
}

// needAnim reports whether the animation tick should run.
func (m Model) needAnim() bool {
	return m.animOn() && (m.anyWorking() || len(m.pulses) > 0)
}

func animCmd() tea.Cmd {
	return tea.Tick(frameEvery, func(time.Time) tea.Msg { return animMsg{} })
}

// stepAnim advances the spinner and drops finished pulses.
func (m *Model) stepAnim() {
	m.frame++
	now := m.opts.Now()
	for p, start := range m.pulses {
		if now.Sub(start) >= pulseFor {
			delete(m.pulses, p)
		}
	}
}

// showToast replaces any toast and returns the timer that clears it.
func (m *Model) showToast(text string, kind toastKind) tea.Cmd {
	m.toastID++
	m.toast = toast{text, kind}
	id := m.toastID
	return tea.Tick(toastFor, func(time.Time) tea.Msg { return toastMsg{id} })
}

func (m Model) toastText() string {
	st := m.theme.st
	switch m.toast.kind {
	case toastError:
		return st.danger.S(m.toast.text)
	case toastWaiting:
		return st.waitingBold.S(m.toast.text)
	}
	return st.text.S(m.toast.text)
}

// toastBox is the toast as a rounded mini-panel, on wide screens only; narrow screens show it
// as text at the right end of the footer.
func (m Model) toastBox(L layout) []string {
	if m.toast.text == "" || !L.wideToast || L.body.h < 3 {
		return nil
	}
	text := m.theme.Truncate(m.toastText(), max(1, L.body.w/2))
	w := ansi.StringWidth(text) + 4
	return m.panel("", "", w, 3, true, []string{" " + text})
}

// noticeWaiting starts a pulse and a toast for each agent that turned waiting between two
// snapshots, and rings the bell once with --bell.
func (m *Model) noticeWaiting(events []snapshot.Event, first bool) tea.Cmd {
	if first {
		return nil
	}
	var cmds []tea.Cmd
	rang := false
	for _, e := range events {
		if e.Status != agents.Waiting || (e.Kind != snapshot.EventStatus && e.Kind != snapshot.EventAppeared) {
			continue
		}
		if m.animOn() {
			if m.pulses == nil {
				m.pulses = map[string]time.Time{}
			}
			m.pulses[e.Pane] = m.opts.Now()
		}
		session := e.Target
		if i := strings.LastIndex(session, ":"); i > 0 {
			session = session[:i]
		}
		cmds = append(cmds, m.showToast(m.theme.Symbol(agents.Waiting)+" "+e.Agent+" in "+session+" needs you", toastWaiting))
		if m.opts.Bell && !rang {
			rang = true
			cmds = append(cmds, tea.Raw("\a"))
		}
	}
	return tea.Batch(cmds...)
}
