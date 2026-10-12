package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amirhosseinbanaei/tmux-manager-cli/internal/agents"
	"github.com/amirhosseinbanaei/tmux-manager-cli/internal/snapshot"
)

// Layout thresholds.
const (
	narrowWidth  = 60  // under: one list, Enter opens a full-screen detail view
	wideWidth    = 100 // from: list left, detail right; between: detail below the list
	previewMinH  = 16  // under: no preview
	listMinWidth = 40
)

// View draws the whole screen. Every line is cut to the width and there are exactly height
// lines, so nothing wraps or scrolls the terminal.
func (m Model) View() tea.View {
	v := tea.NewView(m.Render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "tmux-manager"
	return v
}

// listGeometry is where the list sits for the current size: its first screen row, its left
// column, its width and its height in rows.
func (m Model) listGeometry() (top, left, w, h int) {
	w, h = max(m.width, 1), max(m.height, 1)-2
	switch {
	case h <= 0:
		return 1, 0, w, 0
	case w < narrowWidth:
		return 1, 0, w, h
	case w < wideWidth:
		return 1, 0, w, m.mediumListHeight(h)
	}
	return 1, 0, max(listMinWidth, w*45/100), h
}

// mediumListHeight gives the list what its rows need, up to half the body (three fifths when
// there is no preview), and the rest to the detail.
func (m Model) mediumListHeight(bodyH int) int {
	listH := bodyH / 2
	if m.height < previewMinH || !m.preview {
		listH = bodyH * 3 / 5
	}
	listH = min(listH, max(len(m.rows), 4))
	return max(3, min(listH, bodyH-2))
}

// syncLayout records the list region for mouse hits and scrolls the list to the cursor. Update
// calls it after every message, so View stays a pure function of the model.
func (m *Model) syncLayout() {
	m.listTop, m.listLeft, m.listWidth, m.listRows = m.listGeometry()
	if m.mode != modeList {
		m.listRows = m.listRowsVisible()
	}
	h := max(1, m.listRows)
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	m.offset = max(0, min(m.offset, len(m.rows)-h))
}

func (m Model) listRowsVisible() int {
	if m.mode == modeDetail || m.mode == modeHelp || m.mode == modeLog {
		return 0
	}
	_, _, _, h := m.listGeometry()
	return h
}

// Render returns the screen as text; View wraps it. Tests call it directly.
func (m Model) Render() string {
	w, h := max(m.width, 1), max(m.height, 1)
	var body []string
	bodyH := h - 2 // header and footer
	switch m.mode {
	case modeHelp:
		body = m.helpLines(w, bodyH)
	case modeLog:
		body = m.logLines(w, bodyH)
	case modeDetail:
		body = m.detailLines(w, bodyH)
	default:
		body = m.bodyLines(w, bodyH)
	}
	if overlay := m.formLines(w); len(overlay) > 0 && bodyH > 0 {
		if len(overlay) > bodyH {
			overlay = overlay[len(overlay)-bodyH:]
		}
		body = fit(body, bodyH)
		copy(body[bodyH-len(overlay):], overlay)
	}
	lines := []string{m.header(w)}
	if bodyH > 0 {
		lines = append(lines, fit(body, bodyH)...)
	}
	if h >= 2 {
		lines = append(lines, m.footer(w))
	}
	lines = fit(lines, h)
	for i, l := range lines {
		lines[i] = m.theme.Truncate(l, w)
	}
	return strings.Join(lines, "\n")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// fit pads or cuts lines to exactly n.
func fit(lines []string, n int) []string {
	if n <= 0 {
		return nil
	}
	out := make([]string, n)
	copy(out, lines)
	return out
}

func (m Model) header(w int) string {
	if m.snap == nil {
		return m.theme.Bold("tmux-manager") + " " + m.theme.Dim("loading"+m.theme.Ellipsis())
	}
	ags := m.snap.Agents()
	waiting := 0
	for _, a := range ags {
		if a.Pane.Agent.Status == agents.Waiting {
			waiting++
		}
	}
	parts := []string{plural(len(m.snap.Sessions), "session"), plural(len(ags), "agent")}
	if waiting > 0 {
		parts = append(parts, m.theme.Badge(agents.Waiting)+fmt.Sprintf(" %d", waiting))
	}
	if m.agentsOnly {
		parts = append(parts, "agents only")
	}
	if m.filter != "" {
		parts = append(parts, "/"+m.filter)
	}
	name := "tmux-manager"
	if w < narrowWidth {
		name = "tmm"
		parts[0] = fmt.Sprintf("%d sess", len(m.snap.Sessions))
	}
	left := m.theme.Bold(name) + " " + strings.Join(parts, " · ")
	if m.snap.Server.Socket != "default" {
		right := m.theme.Dim(m.snap.Server.Socket)
		if ansi.StringWidth(left)+ansi.StringWidth(right)+1 <= w {
			left += strings.Repeat(" ", w-ansi.StringWidth(left)-ansi.StringWidth(right)) + right
		}
	}
	return left
}

// helpKeys is the full key list, in the order the help bar shows it.
var helpKeys = [][2]string{
	{"↑↓/jk", "move"}, {"enter", "open"}, {"a", "attach"}, {"n", "new"}, {"r", "rename"}, {"x", "kill"},
	{"s", "send"}, {"p", "preview"}, {"/", "filter"}, {"g", "agents"}, {"l", "log"}, {"R", "refresh"},
	{"y", "copy id"}, {"?", "help"}, {"q", "quit"},
}

func (m Model) footer(w int) string {
	var help string
	switch m.mode {
	case modeList:
		var parts []string
		for _, k := range helpKeys {
			parts = append(parts, k[0]+" "+k[1])
		}
		help = strings.Join(parts, "  ")
		if m.theme.ASCII {
			help = strings.Replace(help, "↑↓/jk", "jk", 1)
		}
	case modeDetail:
		help = "enter attach  y copy id  p preview  esc back"
	case modeHelp:
		help = "any key: back"
	case modeLog:
		help = "jk scroll  esc back"
	case modeKill:
		help = "y yes  n no  enter choose"
	case modeSend:
		help = "enter send  tab toggle Enter  esc cancel"
	case modeNew:
		help = "tab next  enter next/create  esc cancel"
	default:
		help = "enter ok  esc cancel"
	}
	status := m.status
	if status != "" && m.statusErr {
		status = m.theme.Error(status)
	}
	sw := ansi.StringWidth(status)
	if sw+2+ansi.StringWidth(help) > w {
		help = "? help"
		if m.mode != modeList {
			help = "esc back"
		}
	}
	if status == "" {
		return m.theme.Dim(help)
	}
	room := w - ansi.StringWidth(help) - 2
	if room < 8 {
		return m.theme.Truncate(status, w)
	}
	status = m.theme.Pad(status, room)
	return status + "  " + m.theme.Dim(help)
}

// bodyLines lays out the list and the detail by width.
func (m Model) bodyLines(w, h int) []string {
	if h <= 0 {
		return nil
	}
	switch {
	case w < narrowWidth:
		return m.listLines(w, h)
	case w < wideWidth:
		listH := m.mediumListHeight(h)
		lines := m.listLines(w, listH)
		lines = append(lines, m.rule(w, "detail"))
		return append(lines, m.detailLines(w, h-listH-1)...)
	default:
		lw := max(listMinWidth, w*45/100)
		dw := w - lw - 1
		left := m.listLines(lw, h)
		right := m.detailLines(dw, h)
		sep := "│"
		if m.theme.ASCII {
			sep = "|"
		}
		out := make([]string, h)
		for i := range out {
			l, r := "", ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				r = right[i]
			}
			out[i] = m.theme.Pad(l, lw) + m.theme.Dim(sep) + m.theme.Truncate(r, dw)
		}
		return out
	}
}

func (m Model) rule(w int, label string) string {
	ch := "─"
	if m.theme.ASCII {
		ch = "-"
	}
	text := ch + ch + " " + label + " "
	return m.theme.Dim(text + strings.Repeat(ch, max(0, w-ansi.StringWidth(text))))
}

// listLines draws the session list from m.offset, which syncLayout keeps on the cursor.
func (m Model) listLines(w, h int) []string {
	if m.snap == nil {
		return []string{m.theme.Dim("reading tmux" + m.theme.Ellipsis())}
	}
	if len(m.snap.Sessions) == 0 {
		return []string{"", " no tmux sessions" + m.serverNote(), "", " press n to create a session"}
	}
	if len(m.rows) == 0 {
		return []string{"", " nothing matches", " esc clears the filter, g shows all"}
	}
	var out []string
	now := m.opts.Now()
	for i := m.offset; i < len(m.rows) && len(out) < h; i++ {
		line := m.rowLine(m.rows[i], w-1, now)
		if i == m.cursor {
			marker := "›"
			if m.theme.ASCII {
				marker = ">"
			}
			out = append(out, m.theme.Reverse(m.theme.Pad(marker+line, w)))
		} else {
			out = append(out, " "+line)
		}
	}
	return out
}

func (m Model) serverNote() string {
	if m.snap != nil && m.snap.Server.Socket != "default" {
		return " on " + m.snap.Server.Socket
	}
	return ""
}

func (m Model) rowLine(r row, w int, now time.Time) string {
	s := m.snap.Session(r.session)
	if s == nil {
		return ""
	}
	if r.kind == rowSession {
		meta := fmt.Sprintf("%dw", len(s.Windows))
		if s.Attached > 0 {
			meta += " att"
		}
		name := m.theme.Bold(s.Name)
		return m.theme.Truncate(name, max(1, w-len(meta)-1)) + " " + m.theme.Dim(meta)
	}
	var p *snapshot.Pane
	for _, ap := range s.SessionAgents() {
		if ap.ID == r.pane {
			p = ap
		}
	}
	if p == nil || p.Agent == nil {
		return ""
	}
	a := p.Agent
	age := ""
	for _, win := range s.Windows {
		for _, wp := range win.Panes {
			if wp.ID == p.ID {
				age = AgeSince(now, win.Activity)
			}
		}
	}
	if a.ExitedAt != nil {
		age = AgeSince(now, *a.ExitedAt)
	}
	head := "  " + m.theme.Pad(a.Agent, AgentIDWidth) + " " + m.theme.Pad(m.theme.Badge(a.Status), BadgeWidth) + " "
	titleW := w - ansi.StringWidth(head) - len(age) - 1
	title := a.Title
	if title == "" {
		title = m.theme.Dash()
	}
	if titleW < 4 {
		return m.theme.Truncate(head, w)
	}
	return head + m.theme.Pad(title, titleW) + " " + m.theme.Dim(age)
}

// detailLines draws the selected row's details and, room permitting, the pane preview.
func (m Model) detailLines(w, h int) []string {
	if h <= 0 || w <= 0 {
		return nil
	}
	s, p := m.selected()
	if s == nil {
		return nil
	}
	now := m.opts.Now()
	var out []string
	// kv prints "Label    value". A value that does not fit is cut, or with wrap, word-wrapped
	// under itself; an id (whole) goes on its own line, where it can be read and copied.
	const indent = 9
	kv := func(k, v string, wrap bool) {
		label := m.theme.Dim(fmt.Sprintf("%-8s", k)) + " "
		if v == "" {
			v = m.theme.Dash()
		}
		switch {
		case ansi.StringWidth(v) <= w-indent || !wrap:
			out = append(out, label+m.theme.Truncate(v, max(1, w-indent)))
		case k == "Session" && ansi.StringWidth(v) <= w:
			out = append(out, label, v)
		default:
			for i, l := range strings.Split(ansi.Wrap(v, max(1, w-indent), " "), "\n") {
				if i == 0 {
					out = append(out, label+l)
				} else {
					out = append(out, strings.Repeat(" ", indent)+l)
				}
			}
		}
	}
	if p != nil && p.Agent != nil {
		a := p.Agent
		out = append(out, m.theme.Bold(a.DisplayName)+"  "+m.theme.Badge(a.Status))
		kv("Session", a.SessionID, true)
		kv("Title", a.Title, false)
		kv("Path", ShortPath(a.Path, m.opts.Home), false)
		up := ""
		if !a.StartedAt.IsZero() {
			up = " · up " + Age(now.Sub(a.StartedAt))
		}
		kv("PID", fmt.Sprintf("%d%s", a.PID, up), false)
		kv("Target", p.Target+" "+p.ID, false)
		kv("Source", a.SourceString()+" ("+a.Confidence+")", true)
		if a.Note != "" {
			kv("Note", a.Note, true)
		}
	} else {
		att := ""
		if s.Attached > 0 {
			att = "  " + m.theme.Dim("attached")
		}
		out = append(out, m.theme.Bold(s.Name)+att)
		kv("Created", AgeSince(now, s.Created)+" ago · "+plural(len(s.Windows), "window"), false)
		for _, win := range s.Windows {
			out = append(out, fmt.Sprintf("%d: %s", win.Index, win.Name))
			for _, wp := range win.Panes {
				line := fmt.Sprintf("  .%d %s %s %s", wp.Index, wp.ID, wp.Command, ShortPath(wp.Path, m.opts.Home))
				if wp.Agent != nil {
					line = fmt.Sprintf("  .%d %s %s %s", wp.Index, wp.ID, wp.Agent.Agent, m.theme.Badge(wp.Agent.Status))
				}
				out = append(out, line)
			}
		}
	}
	if !m.preview || m.height < previewMinH {
		return fit(out, min(h, len(out)))
	}
	room := h - len(out) - 1
	if room < 2 {
		return fit(out, min(h, len(out)))
	}
	out = append(out, m.rule(w, "preview"))
	var pv []string
	if m.previewPane == m.selectedPaneID() {
		pv = strings.Split(strings.TrimRight(m.previewText, "\n "), "\n")
	}
	if len(pv) > room {
		pv = pv[len(pv)-room:]
	}
	for _, l := range pv {
		out = append(out, m.theme.Truncate(ansi.Strip(l), w))
	}
	return out
}

func (m Model) helpLines(w, h int) []string {
	lines := []string{m.theme.Bold("keys"), ""}
	for _, k := range helpKeys {
		lines = append(lines, fmt.Sprintf("  %-7s %s", k[0], k[1]))
	}
	lines = append(lines, "",
		"  enter on a narrow screen opens the detail; esc goes back",
		"  mouse: wheel scrolls, click selects, click again opens",
		"", m.theme.Bold("statuses"))
	for _, s := range agents.Statuses() {
		lines = append(lines, "  "+m.theme.Badge(s))
	}
	off := max(0, min(m.helpOffset, len(lines)-h))
	return lines[off:]
}

func (m Model) logLines(w, h int) []string {
	if len(m.events) == 0 {
		return []string{m.theme.Bold("log"), "", " no changes yet in this run"}
	}
	var lines []string
	for _, e := range m.events {
		lines = append(lines, EventLine(m.theme, e, m.opts.Home))
	}
	off := max(0, min(m.logOffset, len(lines)-1))
	end := max(1, len(lines)-off)
	start := max(0, end-h)
	return lines[start:end]
}

// EventLine is one log line, shared with `log --follow`.
func EventLine(t Theme, e snapshot.Event, home string) string {
	what := string(e.Status)
	switch e.Kind {
	case snapshot.EventStatus:
		what = string(e.From) + " → " + t.Badge(e.Status)
		if t.ASCII {
			what = string(e.From) + " -> " + t.Badge(e.Status)
		}
	case snapshot.EventSessionID:
		what = "session " + e.SessionID
	case snapshot.EventAppeared, snapshot.EventExited:
		what = t.Badge(e.Status)
	}
	line := fmt.Sprintf("%s %-10s %s %s %s", e.Time.Format("15:04:05"), e.Kind, e.Target, e.Agent, what)
	if e.Title != "" && e.Kind != snapshot.EventSessionID {
		line += " " + t.Dim(e.Title)
	}
	return line
}

// formLines draws the active form or dialog over the bottom of the body.
func (m Model) formLines(w int) []string {
	rule := m.rule(w, "")
	switch m.mode {
	case modeKill:
		yes, no := " yes ", " no "
		if m.killYes {
			yes = m.theme.Reverse("[yes]")
		} else {
			no = m.theme.Reverse("[no]")
		}
		return []string{m.rule(w, "kill"), fmt.Sprintf(" Kill session %q?", m.formTarget), "   " + yes + "  " + no}
	case modeNew:
		lines := []string{m.rule(w, "new session")}
		for _, in := range m.inputs {
			lines = append(lines, " "+in.View())
		}
		return lines
	case modeRename:
		return []string{m.rule(w, "rename "+m.formTarget), " " + m.inputs[0].View()}
	case modeSend:
		box := "[ ]"
		if m.sendEnter {
			box = "[x]"
		}
		return []string{m.rule(w, "send to "+m.formTarget), " " + m.inputs[0].View(), " " + box + " and press Enter (tab)"}
	case modeFilter:
		return []string{rule, " " + m.inputs[0].View()}
	}
	return nil
}
