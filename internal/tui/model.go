// Package tui is the full-screen interface: Bubble Tea v2, Bubbles v2 and Lip Gloss v2. It
// reads internal/snapshot only, and acts on tmux through the snapshot Collector.
package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/amirhosseinbanaei/tmux-manager-cli/internal/agents"
	"github.com/amirhosseinbanaei/tmux-manager-cli/internal/snapshot"
)

// Options configure the TUI.
type Options struct {
	Collector *snapshot.Collector
	Interval  time.Duration
	Theme     Theme
	Home      string
	Now       func() time.Time
	// LookPath finds an agent's executable for the new-session form (exec.LookPath).
	LookPath func(string) (string, error)
}

type mode int

const (
	modeList mode = iota
	modeDetail
	modeHelp
	modeLog
	modeNew
	modeRename
	modeSend
	modeKill
	modeFilter
)

type rowKind int

const (
	rowSession rowKind = iota
	rowAgent
)

type row struct {
	kind    rowKind
	session string
	pane    string // pane id for agent rows
}

// maxEvents caps the in-memory log.
const maxEvents = 1000

// Model is the Bubble Tea model.
type Model struct {
	opts  Options
	theme Theme

	snap   *snapshot.Snapshot
	events []snapshot.Event
	rows   []row
	cursor int
	offset int

	width, height int
	mode          mode
	preview       bool
	previewPane   string
	previewText   string
	agentsOnly    bool
	filter        string

	inputs     []textinput.Model // the active form's fields
	focus      int
	sendEnter  bool
	killYes    bool
	formTarget string // session the form acts on

	status     string
	statusErr  bool
	refreshing bool
	logOffset  int
	helpOffset int

	// The list's screen region, recorded by View for mouse hits.
	listTop, listRows, listLeft, listWidth int
}

// New returns the model; run it with tea.NewProgram(m).
func New(o Options) Model {
	if o.Interval <= 0 {
		o.Interval = 2 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	return Model{opts: o, theme: o.Theme, preview: true, sendEnter: true, width: 80, height: 24}
}

// Messages.
type (
	snapMsg struct {
		snap *snapshot.Snapshot
		err  error
	}
	tickMsg    struct{}
	previewMsg struct {
		pane, text string
	}
	actionMsg struct {
		text string
		err  error
	}
	attachPlanMsg struct {
		cmd     *exec.Cmd
		inPlace bool
		session string
	}
	attachDoneMsg struct{ err error }
)

// Init starts the first refresh and the timer, and asks for the background colour.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refreshCmd(), m.tickCmd(), tea.RequestBackgroundColor)
}

func (m Model) refreshCmd() tea.Cmd {
	c := m.opts.Collector
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s, err := c.Collect(ctx)
		return snapMsg{s, err}
	}
}

func (m Model) tickCmd() tea.Cmd {
	return tea.Tick(m.opts.Interval, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) previewCmd() tea.Cmd {
	pane := m.selectedPaneID()
	if !m.preview || pane == "" || m.height < 16 {
		return nil
	}
	c := m.opts.Collector
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		text, err := c.Capture(ctx, pane, 0)
		if err != nil {
			text = ""
		}
		return previewMsg{pane, text}
	}
}

func (m Model) actionCmd(done string, fn func(ctx context.Context) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return actionMsg{done, fn(ctx)}
	}
}

// Update handles one message. Refreshes run in tea.Cmds and never block it.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	nm := next.(Model)
	nm.syncLayout()
	return nm, cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clamp()
		return m, m.previewCmd()
	case tea.BackgroundColorMsg:
		dark := msg.IsDark()
		m.theme.Dark = &dark
		return m, nil
	case tickMsg:
		cmds := []tea.Cmd{m.tickCmd()}
		if !m.refreshing {
			m.refreshing = true
			cmds = append(cmds, m.refreshCmd())
		}
		return m, tea.Batch(cmds...)
	case snapMsg:
		m.refreshing = false
		if msg.err != nil {
			m.status, m.statusErr = "refresh failed: "+firstLine(msg.err.Error()), true
			return m, nil
		}
		if m.statusErr && strings.HasPrefix(m.status, "refresh failed") {
			m.status, m.statusErr = "", false
		}
		m.setSnapshot(msg.snap)
		return m, m.previewCmd()
	case previewMsg:
		m.previewPane, m.previewText = msg.pane, msg.text
		return m, nil
	case actionMsg:
		if msg.err != nil {
			m.status, m.statusErr = firstLine(msg.err.Error()), true
		} else {
			m.status, m.statusErr = msg.text, false
		}
		m.refreshing = true
		return m, m.refreshCmd()
	case attachPlanMsg:
		if msg.inPlace {
			return m, m.actionCmd("switched to "+msg.session, func(context.Context) error { return runErr(msg.cmd) })
		}
		return m, tea.ExecProcess(msg.cmd, func(err error) tea.Msg { return attachDoneMsg{err} })
	case attachDoneMsg:
		if msg.err != nil {
			m.status, m.statusErr = "attach: "+firstLine(msg.err.Error()), true
		}
		m.refreshing = true
		return m, m.refreshCmd()
	case tea.PasteMsg:
		return m.updateInput(msg)
	case tea.MouseWheelMsg:
		return m.mouseWheel(msg)
	case tea.MouseClickMsg:
		return m.mouseClick(msg)
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	if len(m.inputs) > 0 {
		return m.updateInput(msg)
	}
	return m, nil
}

func runErr(cmd *exec.Cmd) error {
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) > 0 {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return err
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// setSnapshot replaces the snapshot, logs the changes and keeps the selection on the same row.
func (m *Model) setSnapshot(s *snapshot.Snapshot) {
	if m.snap != nil || s != nil {
		m.events = append(m.events, snapshot.Diff(m.snap, s)...)
		if len(m.events) > maxEvents {
			m.events = m.events[len(m.events)-maxEvents:]
		}
	}
	var keep row
	hadSel := m.cursor < len(m.rows)
	if hadSel {
		keep = m.rows[m.cursor]
	}
	m.snap = s
	m.buildRows()
	if hadSel {
		for i, r := range m.rows {
			if r == keep {
				m.cursor = i
				break
			}
			if r.kind == rowSession && r.session == keep.session && keep.kind == rowAgent {
				m.cursor = i // the agent row went away; stay on its session
			}
		}
	}
	m.clamp()
}

// buildRows flattens the snapshot into list rows: each session, then its agent panes.
func (m *Model) buildRows() {
	m.rows = m.rows[:0]
	if m.snap == nil {
		return
	}
	f := strings.ToLower(m.filter)
	for i := range m.snap.Sessions {
		s := &m.snap.Sessions[i]
		panes := s.SessionAgents()
		if m.agentsOnly && len(panes) == 0 {
			continue
		}
		sessionHit := f == "" || strings.Contains(strings.ToLower(s.Name), f)
		var agentRows []row
		for _, p := range panes {
			if sessionHit || agentMatches(p.Agent, f) {
				agentRows = append(agentRows, row{kind: rowAgent, session: s.Name, pane: p.ID})
			}
		}
		if !sessionHit && len(agentRows) == 0 {
			continue
		}
		m.rows = append(m.rows, row{kind: rowSession, session: s.Name})
		m.rows = append(m.rows, agentRows...)
	}
}

func agentMatches(a *agents.Info, f string) bool {
	for _, s := range []string{a.Agent, string(a.Status), a.Title, a.Path, a.SessionID} {
		if strings.Contains(strings.ToLower(s), f) {
			return true
		}
	}
	return false
}

func (m *Model) clamp() {
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// selected returns the selected session and pane (nil for a session row).
func (m Model) selected() (*snapshot.Session, *snapshot.Pane) {
	if m.snap == nil || m.cursor >= len(m.rows) {
		return nil, nil
	}
	r := m.rows[m.cursor]
	s := m.snap.Session(r.session)
	if s == nil {
		return nil, nil
	}
	if r.kind == rowSession {
		return s, nil
	}
	for _, p := range s.SessionAgents() {
		if p.ID == r.pane {
			return s, p
		}
	}
	return s, nil
}

// selectedPaneID is the pane the preview shows: the agent's, or the session's current pane.
func (m Model) selectedPaneID() string {
	s, p := m.selected()
	if p != nil {
		return p.ID
	}
	if s != nil {
		if ap := s.ActivePane(); ap != nil {
			return ap.ID
		}
	}
	return ""
}

func (m Model) narrow() bool { return m.width < 60 }

// ── keys ─────────────────────────────────────────────────────────────────────

func (m Model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.mode {
	case modeHelp:
		switch k {
		case "j", "down":
			m.helpOffset++
		case "k", "up":
			if m.helpOffset > 0 {
				m.helpOffset--
			}
		default:
			m.mode = modeList
		}
		return m, nil
	case modeLog:
		switch k {
		case "j", "down":
			m.logOffset--
		case "k", "up":
			m.logOffset++
		case "pgup":
			m.logOffset += m.height / 2
		case "pgdown":
			m.logOffset -= m.height / 2
		case "q", "esc", "l":
			m.mode = modeList
		}
		if m.logOffset < 0 {
			m.logOffset = 0
		}
		return m, nil
	case modeKill:
		switch k {
		case "y", "Y":
			return m.doKill()
		case "left", "right", "tab", "h":
			m.killYes = !m.killYes
		case "enter":
			if m.killYes {
				return m.doKill()
			}
			m.mode = modeList
		case "n", "N", "esc", "q":
			m.mode = modeList
		}
		return m, nil
	case modeNew, modeRename, modeSend, modeFilter:
		return m.formKey(msg)
	case modeDetail:
		switch k {
		case "esc", "q", "backspace":
			m.mode = modeList
			return m, nil
		case "enter":
			return m.attach()
		}
	}

	switch k {
	case "q":
		if m.mode == modeDetail {
			m.mode = modeList
			return m, nil
		}
		return m, tea.Quit
	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.buildRows()
			m.clamp()
		}
		return m, nil
	case "up", "k":
		m.move(-1)
		return m, m.previewCmd()
	case "down", "j":
		m.move(1)
		return m, m.previewCmd()
	case "pgup":
		m.move(-max(1, m.listRows-1))
		return m, m.previewCmd()
	case "pgdown":
		m.move(max(1, m.listRows-1))
		return m, m.previewCmd()
	case "home":
		m.move(-len(m.rows))
		return m, m.previewCmd()
	case "end", "G":
		m.move(len(m.rows))
		return m, m.previewCmd()
	case "enter":
		if m.narrow() && m.mode == modeList {
			if len(m.rows) > 0 {
				m.mode = modeDetail
			}
			return m, m.previewCmd()
		}
		return m.attach()
	case "a":
		return m.attach()
	case "n":
		return m.openNew()
	case "r":
		return m.openRename()
	case "x":
		if s, _ := m.selected(); s != nil {
			m.mode, m.killYes, m.formTarget = modeKill, false, s.Name
		}
		return m, nil
	case "s":
		return m.openSend()
	case "p":
		m.preview = !m.preview
		return m, m.previewCmd()
	case "/":
		in := m.newInput("filter: ", m.filter)
		m.inputs, m.focus, m.mode = []textinput.Model{in}, 0, modeFilter
		return m, m.inputs[0].Focus()
	case "g":
		m.agentsOnly = !m.agentsOnly
		m.buildRows()
		m.clamp()
		return m, m.previewCmd()
	case "l":
		m.mode, m.logOffset = modeLog, 0
		return m, nil
	case "R":
		if !m.refreshing {
			m.refreshing = true
			return m, m.refreshCmd()
		}
		return m, nil
	case "?":
		m.mode, m.helpOffset = modeHelp, 0
		return m, nil
	case "y":
		_, p := m.selected()
		if p != nil && p.Agent != nil && p.Agent.SessionID != "" {
			m.status, m.statusErr = "copied session id (OSC 52)", false
			return m, tea.SetClipboard(p.Agent.SessionID)
		}
		m.status, m.statusErr = "no session id to copy", true
		return m, nil
	}
	return m, nil
}

func (m *Model) move(d int) {
	m.cursor += d
	m.clamp()
}

func (m Model) attach() (tea.Model, tea.Cmd) {
	s, _ := m.selected()
	if s == nil {
		return m, nil
	}
	c, name := m.opts.Collector, s.Name
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd, inPlace := c.AttachCommand(ctx, name)
		return attachPlanMsg{cmd: cmd, inPlace: inPlace, session: name}
	}
}

func (m Model) doKill() (tea.Model, tea.Cmd) {
	name := m.formTarget
	m.mode = modeList
	c := m.opts.Collector
	return m, m.actionCmd("killed "+name, func(ctx context.Context) error { return c.KillSession(ctx, name) })
}

// ── mouse ────────────────────────────────────────────────────────────────────

func (m Model) mouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	up := msg.Button == tea.MouseWheelUp
	switch m.mode {
	case modeLog:
		if up {
			m.logOffset++
		} else if m.logOffset > 0 {
			m.logOffset--
		}
		return m, nil
	case modeHelp:
		if up && m.helpOffset > 0 {
			m.helpOffset--
		} else if !up {
			m.helpOffset++
		}
		return m, nil
	case modeList, modeDetail:
		if up {
			m.move(-1)
		} else {
			m.move(1)
		}
		return m, m.previewCmd()
	}
	return m, nil
}

func (m Model) mouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft || m.mode != modeList {
		return m, nil
	}
	mo := msg.Mouse()
	if mo.Y < m.listTop || mo.Y >= m.listTop+m.listRows || mo.X < m.listLeft || mo.X >= m.listLeft+m.listWidth {
		return m, nil
	}
	i := m.offset + mo.Y - m.listTop
	if i < 0 || i >= len(m.rows) {
		return m, nil
	}
	if i == m.cursor { // a click on the selected row opens it
		return m.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	m.cursor = i
	return m, m.previewCmd()
}

// Snapshot feeds a snapshot in directly, as a refresh would; tests use it.
func (m Model) Snapshot(s *snapshot.Snapshot, events bool) Model {
	m.setSnapshot(s)
	if !events {
		m.events = nil
	}
	m.syncLayout()
	return m
}

// Resize sets the terminal size, as a WindowSizeMsg would.
func (m Model) Resize(w, h int) Model {
	m.width, m.height = w, h
	m.syncLayout()
	return m
}

// ── forms ────────────────────────────────────────────────────────────────────

func (m Model) newInput(prompt, value string) textinput.Model {
	in := textinput.New()
	// Plain input styles: no colour is needed, and none is used when colour is off.
	st := in.Styles()
	plain := textinput.StyleState{Placeholder: lipgloss.NewStyle().Faint(true)}
	st.Focused, st.Blurred = plain, plain
	in.SetStyles(st)
	in.Prompt = prompt
	in.SetValue(value)
	in.CursorEnd()
	in.SetWidth(max(8, m.width-len(prompt)-4))
	return in
}

func (m Model) openNew() (tea.Model, tea.Cmd) {
	dir := m.opts.Home
	if s, p := m.selected(); p != nil && p.Agent != nil && p.Agent.Path != "" {
		dir = p.Agent.Path
	} else if s != nil {
		if ap := s.ActivePane(); ap != nil && ap.Path != "" {
			dir = ap.Path
		}
	}
	m.inputs = []textinput.Model{m.newInput("name:  ", ""), m.newInput("dir:   ", dir), m.newInput("agent: ", "")}
	m.inputs[2].Placeholder = "none, or " + strings.Join(m.agentIDs(), " ")
	m.focus, m.mode = 0, modeNew
	return m, m.inputs[0].Focus()
}

func (m Model) agentIDs() []string {
	var ids []string
	for _, d := range m.opts.Collector.Table() {
		ids = append(ids, d.ID)
	}
	return ids
}

func (m Model) openRename() (tea.Model, tea.Cmd) {
	s, _ := m.selected()
	if s == nil {
		return m, nil
	}
	m.inputs, m.focus, m.mode, m.formTarget = []textinput.Model{m.newInput("new name: ", s.Name)}, 0, modeRename, s.Name
	return m, m.inputs[0].Focus()
}

func (m Model) openSend() (tea.Model, tea.Cmd) {
	pane := m.selectedPaneID()
	if pane == "" {
		return m, nil
	}
	m.inputs, m.focus, m.mode, m.formTarget = []textinput.Model{m.newInput("send: ", "")}, 0, modeSend, pane
	return m, m.inputs[0].Focus()
}

func (m Model) formKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		if m.mode == modeFilter {
			m.filter = ""
			m.buildRows()
			m.clamp()
		}
		m.closeForm()
		return m, nil
	case "tab", "down", "shift+tab", "up":
		if m.mode == modeSend && (msg.String() == "tab" || msg.String() == "shift+tab") {
			m.sendEnter = !m.sendEnter
			return m, nil
		}
		if len(m.inputs) > 1 {
			m.inputs[m.focus].Blur()
			if msg.String() == "up" || msg.String() == "shift+tab" {
				m.focus = (m.focus + len(m.inputs) - 1) % len(m.inputs)
			} else {
				m.focus = (m.focus + 1) % len(m.inputs)
			}
			return m, m.inputs[m.focus].Focus()
		}
		return m, nil
	case "enter":
		if m.mode == modeNew && m.focus < len(m.inputs)-1 {
			m.inputs[m.focus].Blur()
			m.focus++
			return m, m.inputs[m.focus].Focus()
		}
		return m.submit()
	}
	return m.updateInput(msg)
}

func (m Model) updateInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	if len(m.inputs) == 0 {
		return m, nil
	}
	var cmd tea.Cmd
	m.inputs[m.focus], cmd = m.inputs[m.focus].Update(msg)
	if m.mode == modeFilter {
		m.filter = m.inputs[0].Value()
		m.buildRows()
		m.clamp()
	}
	return m, cmd
}

func (m *Model) closeForm() {
	m.inputs, m.focus, m.mode = nil, 0, modeList
}

func (m Model) submit() (tea.Model, tea.Cmd) {
	c := m.opts.Collector
	switch m.mode {
	case modeFilter:
		m.closeForm()
		return m, m.previewCmd()
	case modeRename:
		oldName, newName := m.formTarget, strings.TrimSpace(m.inputs[0].Value())
		m.closeForm()
		if newName == "" || newName == oldName {
			return m, nil
		}
		return m, m.actionCmd("renamed "+oldName+" → "+newName, func(ctx context.Context) error {
			return c.RenameSession(ctx, oldName, newName)
		})
	case modeSend:
		pane, text, enter := m.formTarget, m.inputs[0].Value(), m.sendEnter
		m.closeForm()
		if text == "" && !enter {
			return m, nil
		}
		return m, m.actionCmd("sent to "+pane, func(ctx context.Context) error { return c.SendText(ctx, pane, text, enter) })
	case modeNew:
		name := strings.TrimSpace(m.inputs[0].Value())
		dir := strings.TrimSpace(m.inputs[1].Value())
		agent := strings.TrimSpace(m.inputs[2].Value())
		if name == "" {
			m.status, m.statusErr = "a session needs a name", true
			return m, nil
		}
		var cmd []string
		if agent != "" && agent != "none" {
			d, ok := agents.Lookup(c.Table(), agent)
			if !ok {
				m.status, m.statusErr = "unknown agent "+agent+"; one of: "+strings.Join(m.agentIDs(), " "), true
				return m, nil
			}
			path, err := lookAgent(d, m.opts.LookPath)
			if err != nil {
				m.status, m.statusErr = err.Error(), true
				return m, nil
			}
			cmd = AgentCommand(path)
		}
		m.closeForm()
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			got, err := c.NewSession(ctx, name, dir, cmd)
			return actionMsg{"created " + got, err}
		}
	}
	m.closeForm()
	return m, nil
}

// lookAgent finds a detector's executable on PATH.
func lookAgent(d agents.Detector, lookPath func(string) (string, error)) (string, error) {
	for _, exe := range d.Executables {
		if p, err := lookPath(exe); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s is not on PATH (looked for %s)", d.DisplayName, strings.Join(d.Executables, ", "))
}

// LookAgent is lookAgent for the CLI.
func LookAgent(d agents.Detector, lookPath func(string) (string, error)) (string, error) {
	return lookAgent(d, lookPath)
}

// AgentCommand is the pane command that starts an agent executable. tmux runs a single
// argument through the shell, so a path with shell characters goes through env instead, which
// tmux executes directly as two arguments.
func AgentCommand(path string) []string {
	if strings.Trim(path, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./+-") == "" {
		return []string{path}
	}
	return []string{"env", path}
}
