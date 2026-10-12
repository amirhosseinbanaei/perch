// Package tui is the full-screen interface: Bubble Tea v2, Bubbles v2 and Lip Gloss v2. It
// reads internal/snapshot only, and acts on tmux through the snapshot Collector.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/amirhosseinbanaei/perch/internal/agents"
	"github.com/amirhosseinbanaei/perch/internal/config"
	"github.com/amirhosseinbanaei/perch/internal/snapshot"
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
	// SaveTheme persists a theme chosen with t or /theme; nil saves nothing.
	SaveTheme func(name string) error
	Warning   string // shown in the footer at start, e.g. an unreadable config file
	NoAnim    bool   // no spinner and no pulse (--no-anim, PERCH_NO_ANIM=1)
	Bell      bool   // ring the terminal bell when an agent turns waiting (--bell)
	Verbose   bool   // show Source in the detail from the start (--verbose)
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

	focusDetail bool // Tab: arrows act on Detail (scroll the preview) instead of Sessions
	previewOff  int  // lines the live preview is scrolled up from its bottom
	showSource  bool

	inputs       []textinput.Model // the active form's text fields
	focus        int               // the form's focused field; in new, 2 is the agent picker
	agentChoices []string          // new: "shell" and the agents found on PATH
	agentPick    int
	sendEnter    bool
	killYes      bool
	formTarget   string // session the form acts on

	bar            textinput.Model // the command bar
	barOn          bool
	barErr         string
	barSel, barOff int // the popup's selected command

	status       string
	statusErr    bool
	refreshing   bool
	refreshStart time.Time
	logOffset    int
	helpOffset   int

	frame       int                  // spinner frame
	animPending bool                 // an animation tick is scheduled
	pulses      map[string]time.Time // pane → when it turned waiting
	toast       toast
	toastID     int

	// The layout as last drawn, for mouse hits, and the list's visible rows.
	lay      layout
	listRows int
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
	if o.Theme.Name == "" {
		o.Theme.Name = config.DefaultTheme
	}
	m := Model{opts: o, theme: o.Theme.Compile(), preview: true, sendEnter: true, showSource: o.Verbose, width: 80, height: 24}
	m.bar = m.newBar()
	if o.Warning != "" {
		m.status, m.statusErr = o.Warning, true
	}
	return m
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
	themeSavedMsg struct {
		name string
		err  error
	}
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

func (m *Model) startRefresh() tea.Cmd {
	if m.refreshing {
		return nil
	}
	m.refreshing, m.refreshStart = true, m.opts.Now()
	return m.refreshCmd()
}

func (m Model) previewCmd() tea.Cmd {
	pane := m.selectedPaneID()
	if !m.preview || pane == "" || m.height < previewMinH {
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
	if nm.needAnim() && !nm.animPending {
		nm.animPending = true
		cmd = tea.Batch(cmd, animCmd())
	}
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
		m.theme = m.theme.Compile()
		return m, nil
	case tickMsg:
		return m, tea.Batch(m.tickCmd(), m.startRefresh())
	case animMsg:
		m.animPending = false
		m.stepAnim()
		return m, nil
	case toastMsg:
		if msg.id == m.toastID {
			m.toast = toast{}
		}
		return m, nil
	case themeSavedMsg:
		if msg.err != nil {
			return m, m.showToast("theme "+msg.name+" not saved: "+firstLine(msg.err.Error()), toastError)
		}
		return m, nil
	case snapMsg:
		m.refreshing = false
		if msg.err != nil {
			m.status, m.statusErr = "refresh failed: "+firstLine(msg.err.Error()), true
			return m, nil
		}
		if m.statusErr && strings.HasPrefix(m.status, "refresh failed") {
			m.status, m.statusErr = "", false
		}
		cmd := m.setSnapshot(msg.snap)
		return m, tea.Batch(cmd, m.previewCmd())
	case previewMsg:
		m.previewPane, m.previewText = msg.pane, msg.text
		return m, nil
	case actionMsg:
		if msg.err != nil {
			m.status, m.statusErr = firstLine(msg.err.Error()), true
		} else {
			m.status, m.statusErr = msg.text, false
		}
		m.refreshing = false
		return m, m.startRefresh()
	case attachPlanMsg:
		if msg.inPlace {
			return m, m.actionCmd("switched to "+msg.session, func(context.Context) error { return runErr(msg.cmd) })
		}
		return m, tea.ExecProcess(msg.cmd, func(err error) tea.Msg { return attachDoneMsg{err} })
	case attachDoneMsg:
		if msg.err != nil {
			m.status, m.statusErr = "attach: "+firstLine(msg.err.Error()), true
		}
		m.refreshing = false
		return m, m.startRefresh()
	case tea.PasteMsg:
		if m.barOn {
			var cmd tea.Cmd
			m.bar, cmd = m.bar.Update(msg)
			m.barChanged()
			return m, cmd
		}
		return m.updateInput(msg)
	case tea.MouseWheelMsg:
		return m.mouseWheel(msg)
	case tea.MouseClickMsg:
		return m.mouseClick(msg)
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	if m.barOn {
		var cmd tea.Cmd
		m.bar, cmd = m.bar.Update(msg)
		return m, cmd
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
// An agent that turned waiting gets a pulse and a toast.
func (m *Model) setSnapshot(s *snapshot.Snapshot) tea.Cmd {
	var cmd tea.Cmd
	if m.snap != nil || s != nil {
		evs := snapshot.Diff(m.snap, s)
		cmd = m.noticeWaiting(evs, m.snap == nil)
		m.events = append(m.events, evs...)
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
	return cmd
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

func (m Model) narrow() bool { return m.width < narrowWidth }

// ── keys ─────────────────────────────────────────────────────────────────────

// shortcuts are the single keys that act in the list; any other printable key starts a filter
// in the command bar.
const shortcuts = "jkGanrxspglRy?tq"

func (m Model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		return m, tea.Quit
	}
	if m.barOn {
		return m.barKey(msg)
	}
	switch m.mode {
	case modeHelp:
		switch k {
		case "j", "down":
			m.helpOffset++
		case "k", "up":
			m.helpOffset = max(0, m.helpOffset-1)
		case "pgdown":
			m.helpOffset += m.height / 2
		case "pgup":
			m.helpOffset = max(0, m.helpOffset-m.height/2)
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
		m.logOffset = max(0, m.logOffset)
		return m, nil
	case modeKill:
		switch k {
		case "y", "Y":
			return m.doKill()
		case "left", "right", "tab", "shift+tab", "h", "l":
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
	case modeNew, modeRename, modeSend:
		return m.formKey(msg)
	case modeDetail:
		switch k {
		case "esc", "q", "backspace":
			m.mode = modeList
			return m, nil
		case "enter":
			return m.attach()
		case "up", "k", "down", "j", "pgup", "pgdown":
			m.scrollPreview(k)
			return m, nil
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
		switch {
		case m.filter != "":
			m.filter = ""
			m.buildRows()
			m.clamp()
		case m.focusDetail:
			m.focusDetail = false
		}
		return m, nil
	case "tab", "shift+tab":
		if m.mode == modeList && m.lay.detail.w > 0 {
			m.focusDetail = !m.focusDetail
		}
		return m, nil
	case "up", "k", "down", "j", "pgup", "pgdown":
		if m.focusDetail && m.lay.detail.w > 0 {
			m.scrollPreview(k)
			return m, nil
		}
		switch k {
		case "up", "k":
			m.move(-1)
		case "down", "j":
			m.move(1)
		case "pgup":
			m.move(-max(1, m.listRows-1))
		case "pgdown":
			m.move(max(1, m.listRows-1))
		}
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
				m.mode, m.previewOff = modeDetail, 0
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
	case "/", ":":
		if m.mode == modeList {
			return m.openBar("/")
		}
		return m, nil
	case "g":
		m.agentsOnly = !m.agentsOnly
		m.buildRows()
		m.clamp()
		return m, m.previewCmd()
	case "l":
		m.mode, m.logOffset = modeLog, 0
		return m, nil
	case "R":
		return m, m.startRefresh()
	case "?":
		m.mode, m.helpOffset = modeHelp, 0
		return m, nil
	case "t":
		return m, m.setTheme(m.nextTheme())
	case "y":
		_, p := m.selected()
		if p != nil && p.Agent != nil && p.Agent.SessionID != "" {
			return m, tea.Batch(m.showToast("copied session id", toastInfo), tea.SetClipboard(p.Agent.SessionID))
		}
		return m, m.showToast("no session id to copy", toastError)
	}
	// Any other printable key starts typing a filter.
	if m.mode == modeList && msg.Text != "" && msg.Text != " " && !strings.ContainsAny(msg.Text, shortcuts) &&
		msg.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
		return m.openBar(msg.Text)
	}
	return m, nil
}

// scrollPreview moves the live preview: up shows older lines.
func (m *Model) scrollPreview(k string) {
	switch k {
	case "up", "k":
		m.previewOff++
	case "down", "j":
		m.previewOff--
	case "pgup":
		m.previewOff += max(1, m.height/3)
	case "pgdown":
		m.previewOff -= max(1, m.height/3)
	}
	lines := strings.Count(strings.TrimRight(m.previewText, "\n "), "\n") + 1
	m.previewOff = max(0, min(m.previewOff, lines-1))
}

func (m *Model) move(d int) {
	m.cursor += d
	m.clamp()
	m.previewOff = 0
}

// nextTheme is the theme after the current one: coral → teal → violet → coral.
func (m Model) nextTheme() string {
	names := config.Themes()
	for i, n := range names {
		if n == m.theme.Name {
			return names[(i+1)%len(names)]
		}
	}
	return names[0]
}

// setTheme applies a theme at once, shows a toast and saves it in the background. A failed save
// keeps the theme for this run.
func (m *Model) setTheme(name string) tea.Cmd {
	m.theme.Name = name
	m.theme = m.theme.Compile()
	return tea.Batch(m.showToast("theme: "+name, toastInfo), m.saveThemeCmd(name))
}

// saveThemeCmd saves the theme off the update loop; nil when there is nowhere to save it.
func (m Model) saveThemeCmd(name string) tea.Cmd {
	save := m.opts.SaveTheme
	if save == nil {
		return nil
	}
	return func() tea.Msg { return themeSavedMsg{name, save(name)} }
}

func (m Model) attach() (tea.Model, tea.Cmd) {
	s, _ := m.selected()
	if s == nil {
		return m, nil
	}
	return m, m.attachCmd(s.Name)
}

func (m Model) attachCmd(name string) tea.Cmd {
	c := m.opts.Collector
	return func() tea.Msg {
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

func (m Model) renameCmd(oldName, newName string) tea.Cmd {
	c := m.opts.Collector
	return m.actionCmd("renamed "+oldName+" → "+newName, func(ctx context.Context) error {
		return c.RenameSession(ctx, oldName, newName)
	})
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
		mo := msg.Mouse()
		if m.lay.detail.has(mo.X, mo.Y) {
			if up {
				m.scrollPreview("up")
			} else {
				m.scrollPreview("down")
			}
			return m, nil
		}
		if up {
			m.move(-1)
		} else {
			m.move(1)
		}
		return m, m.previewCmd()
	}
	return m, nil
}

// mouseClick focuses the panel it lands in; on a row it selects, and on the selected row it
// opens, as Enter does.
func (m Model) mouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft || m.mode != modeList || m.barOn {
		return m, nil
	}
	mo := msg.Mouse()
	if m.lay.detail.has(mo.X, mo.Y) {
		m.focusDetail = true
		return m, nil
	}
	sr := m.lay.sessions
	if !sr.has(mo.X, mo.Y) {
		return m, nil
	}
	m.focusDetail = false
	i := m.offset + mo.Y - (sr.y + 1)
	if mo.Y <= sr.y || mo.Y >= sr.y+sr.h-1 || i < 0 || i >= len(m.rows) {
		return m, nil
	}
	if i == m.cursor {
		return m.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	m.cursor = i
	m.previewOff = 0
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
	// Plain input styles from the palette. The cursor does not blink, so a focused input does
	// not keep a timer running.
	st := in.Styles()
	plain := textinput.StyleState{Placeholder: lipgloss.NewStyle().Faint(true)}
	st.Focused, st.Blurred = plain, plain
	st.Cursor.Blink = false
	in.SetStyles(st)
	in.Prompt = prompt
	in.SetValue(value)
	in.CursorEnd()
	in.SetWidth(max(8, m.modalWidth()-4-7-len(prompt)-1))
	return in
}

// defaultDir is the selection's path, else the home directory.
func (m Model) defaultDir() string {
	dir := m.opts.Home
	if s, p := m.selected(); p != nil && p.Agent != nil && p.Agent.Path != "" {
		dir = p.Agent.Path
	} else if s != nil {
		if ap := s.ActivePane(); ap != nil && ap.Path != "" {
			dir = ap.Path
		}
	}
	return dir
}

func (m Model) openNew() (tea.Model, tea.Cmd) {
	m.inputs = []textinput.Model{m.newInput("", ""), m.newInput("", m.defaultDir())}
	m.agentChoices, m.agentPick = []string{"shell"}, 0
	for _, d := range m.opts.Collector.Table() {
		if _, err := lookAgent(d, m.opts.LookPath); err == nil {
			m.agentChoices = append(m.agentChoices, d.ID)
		}
	}
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
	m.inputs, m.focus, m.mode, m.formTarget = []textinput.Model{m.newInput("", s.Name)}, 0, modeRename, s.Name
	return m, m.inputs[0].Focus()
}

func (m Model) openSend() (tea.Model, tea.Cmd) {
	pane := m.selectedPaneID()
	if pane == "" {
		return m, nil
	}
	m.inputs, m.focus, m.mode, m.formTarget = []textinput.Model{m.newInput("", "")}, 0, modeSend, pane
	return m, m.inputs[0].Focus()
}

// fields is the number of focusable fields in the form: the text inputs, plus new's picker.
func (m Model) fields() int {
	if m.mode == modeNew {
		return len(m.inputs) + 1
	}
	return len(m.inputs)
}

// setFocus moves the form's focus to field i.
func (m *Model) setFocus(i int) tea.Cmd {
	if m.focus < len(m.inputs) {
		m.inputs[m.focus].Blur()
	}
	m.focus = i
	if i < len(m.inputs) {
		return m.inputs[i].Focus()
	}
	return nil
}

func (m Model) formKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	switch k {
	case "esc":
		m.closeForm()
		return m, nil
	case "tab", "down", "shift+tab", "up":
		if m.mode == modeSend && (k == "tab" || k == "shift+tab") {
			m.sendEnter = !m.sendEnter
			return m, nil
		}
		if n := m.fields(); n > 1 {
			if k == "up" || k == "shift+tab" {
				return m, m.setFocus((m.focus + n - 1) % n)
			}
			return m, m.setFocus((m.focus + 1) % n)
		}
		return m, nil
	case "enter":
		if m.mode == modeNew && m.focus < m.fields()-1 {
			return m, m.setFocus(m.focus + 1)
		}
		return m.submit()
	}
	if m.mode == modeNew && m.focus == len(m.inputs) {
		switch k {
		case "left", "h":
			m.agentPick = (m.agentPick + len(m.agentChoices) - 1) % len(m.agentChoices)
		case "right", "l", "space":
			m.agentPick = (m.agentPick + 1) % len(m.agentChoices)
		}
		return m, nil
	}
	return m.updateInput(msg)
}

func (m Model) updateInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.focus >= len(m.inputs) {
		return m, nil
	}
	var cmd tea.Cmd
	m.inputs[m.focus], cmd = m.inputs[m.focus].Update(msg)
	return m, cmd
}

func (m *Model) closeForm() {
	m.inputs, m.focus, m.mode = nil, 0, modeList
}

// createCmd checks a new session's fields and returns the command that creates it.
func (m Model) createCmd(name, dir, agent string) (tea.Cmd, error) {
	c := m.opts.Collector
	if name == "" {
		return nil, errors.New("a session needs a name")
	}
	var cmd []string
	if agent != "" && agent != "none" && agent != "shell" {
		d, ok := agents.Lookup(c.Table(), agent)
		if !ok {
			return nil, fmt.Errorf("unknown agent %s; one of: %s", agent, strings.Join(m.agentIDs(), " "))
		}
		path, err := lookAgent(d, m.opts.LookPath)
		if err != nil {
			return nil, err
		}
		cmd = AgentCommand(path)
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		got, err := c.NewSession(ctx, name, dir, cmd)
		return actionMsg{"created " + got, err}
	}, nil
}

func (m Model) submit() (tea.Model, tea.Cmd) {
	c := m.opts.Collector
	switch m.mode {
	case modeRename:
		oldName, newName := m.formTarget, strings.TrimSpace(m.inputs[0].Value())
		m.closeForm()
		if newName == "" || newName == oldName {
			return m, nil
		}
		return m, m.renameCmd(oldName, newName)
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
		agent := ""
		if m.agentPick < len(m.agentChoices) {
			agent = m.agentChoices[m.agentPick]
		}
		cmd, err := m.createCmd(name, dir, agent)
		if err != nil {
			m.status, m.statusErr = err.Error(), true
			return m, nil
		}
		m.closeForm()
		return m, cmd
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
