package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amirhosseinbanaei/perch/internal/agents"
	"github.com/amirhosseinbanaei/perch/internal/snapshot"
)

// Layout thresholds.
const (
	narrowWidth  = 60  // under: Sessions only, Enter opens Detail full-screen
	wideWidth    = 100 // from: Sessions beside Detail; between: Detail below Sessions
	previewMinH  = 16  // under: no live preview
	chromeMinH   = 16  // under: one-line header and command bar, no borders on them
	sessionsMinW = 34
	logoMinH     = 12 // under: the empty state drops the logo
)

// rect is a screen region in cells.
type rect struct{ x, y, w, h int }

func (r rect) has(x, y int) bool {
	return r.w > 0 && r.h > 0 && x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// layout is where everything goes for the current size and mode. Render and the mouse code
// both read it, so a click lands where the row was drawn.
type layout struct {
	full                  bool // the command bar has a border
	header, body, bar     rect
	sessions, detail      rect // zero when not shown
	footer                int
	fullHeader, wideToast bool
}

func (m Model) layout() layout {
	w, h := max(m.width, 1), max(m.height, 1)
	var L layout
	L.full = h >= chromeMinH
	L.fullHeader = L.full && w >= narrowWidth
	L.wideToast = L.full && w >= wideWidth
	hh, bh := 1, 1
	if L.full {
		bh = 3
	}
	if L.fullHeader {
		hh = 4
	}
	if h < 4 {
		bh = 0
	}
	if h < 3 {
		hh = 0
	}
	L.footer = h - 1
	L.header = rect{0, 0, w, hh}
	bodyH := max(0, h-hh-bh-1)
	L.body = rect{0, hh, w, bodyH}
	L.bar = rect{0, hh + bodyH, w, bh}
	if bodyH == 0 {
		return L
	}
	switch {
	case m.mode == modeLog || m.empty():
	case m.mode == modeDetail:
		L.detail = L.body
	case w < narrowWidth:
		L.sessions = L.body
	case w < wideWidth:
		sh := m.mediumListHeight(bodyH)
		L.sessions = rect{0, hh, w, sh}
		if bodyH-sh >= 4 {
			L.detail = rect{0, hh + sh, w, bodyH - sh}
		} else {
			L.sessions.h = bodyH
		}
	default:
		sw := max(sessionsMinW, w*40/100)
		L.sessions = rect{0, hh, sw, bodyH}
		L.detail = rect{sw, hh, w - sw, bodyH}
	}
	return L
}

// empty reports a snapshot with no sessions at all.
func (m Model) empty() bool { return m.snap != nil && len(m.snap.Sessions) == 0 }

// mediumListHeight gives Sessions what its rows need, up to half the body (three fifths with no
// preview), and the rest to Detail.
func (m Model) mediumListHeight(bodyH int) int {
	listH := bodyH / 2
	if m.height < previewMinH || !m.preview {
		listH = bodyH * 3 / 5
	}
	listH = min(listH, max(len(m.rows), 2)+2)
	return max(4, min(listH, bodyH))
}

// syncLayout records the layout for mouse hits and scrolls the list to the cursor. Update calls
// it after every message, so View stays a pure function of the model.
func (m *Model) syncLayout() {
	m.lay = m.layout()
	m.listRows = max(0, m.lay.sessions.h-2)
	h := max(1, m.listRows)
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	m.offset = max(0, min(m.offset, len(m.rows)-h))
}

// View draws the whole screen. Every line is cut to the width and there are exactly height
// lines, so nothing wraps or scrolls the terminal.
func (m Model) View() tea.View {
	v := tea.NewView(m.Render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = m.windowTitle()
	return v
}

func (m Model) windowTitle() string {
	if n := m.count(agents.Waiting); n > 0 {
		return fmt.Sprintf("perch %s %s %d waiting", m.dot(), m.theme.Symbol(agents.Waiting), n)
	}
	return "perch"
}

// Render returns the screen as text; View wraps it. Tests call it directly.
func (m Model) Render() string {
	w, h := max(m.width, 1), max(m.height, 1)
	L := m.layout()
	lines := make([]string, h)
	put := func(y int, ls []string) {
		for i, l := range ls {
			if y+i >= 0 && y+i < h {
				lines[y+i] = l
			}
		}
	}
	put(L.header.y, m.headerLines(w, L.header.h))
	put(L.body.y, m.bodyLines(L))
	put(L.bar.y, m.barLines(w, L.bar.h))
	lines[L.footer] = m.footer(w, L)
	for i, l := range lines {
		lines[i] = m.theme.Pad(l, w)
	}
	// Overlays, the topmost last. They never move the layout.
	if pop := m.popupLines(L); len(pop) > 0 {
		overlay(lines, pop, 0, L.bar.y-len(pop), w)
	}
	if box := m.toastBox(L); len(box) > 0 {
		overlay(lines, box, max(0, w-ansi.StringWidth(box[0])-1), L.bar.y-len(box), w)
	}
	if box := m.modalLines(); len(box) > 0 {
		bw := ansi.StringWidth(box[0])
		overlay(lines, box, max(0, (w-bw)/2), max(0, (h-len(box))/2), w)
	}
	for i, l := range lines {
		lines[i] = m.theme.Truncate(l, w)
	}
	return strings.Join(lines, "\n")
}

// overlay draws box over base (lines exactly w cells wide) with its top-left corner at x, y.
func overlay(base, box []string, x, y, w int) {
	for i, b := range box {
		row := y + i
		if row < 0 || row >= len(base) {
			continue
		}
		bw := ansi.StringWidth(b)
		left := ansi.Truncate(base[row], x, "")
		if lw := ansi.StringWidth(left); lw < x {
			left += strings.Repeat(" ", x-lw)
		}
		right := ""
		if x+bw < w {
			right = ansi.TruncateLeft(base[row], x+bw, "")
			if over := ansi.StringWidth(right) - (w - x - bw); over > 0 {
				right = ansi.TruncateLeft(base[row], x+bw+over, strings.Repeat(" ", over))
			}
		}
		base[row] = left + "\x1b[m" + b + "\x1b[m" + right
	}
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

// dot is the separator in running text: "·", or "-" in ASCII mode.
func (m Model) dot() string {
	if m.theme.ASCII {
		return "-"
	}
	return "·"
}

// spread puts left at the start and right at the end of w cells, dropping right when both
// do not fit.
func (m Model) spread(left, right string, w int) string {
	lw, rw := ansi.StringWidth(left), ansi.StringWidth(right)
	if right == "" || lw+rw+2 > w {
		return m.theme.Truncate(left, w)
	}
	return left + strings.Repeat(" ", w-lw-rw) + right
}

// ── panels ───────────────────────────────────────────────────────────────────

func (m Model) border() (tl, tr, bl, br, hz, vt string) {
	if m.theme.ASCII {
		return "+", "+", "+", "+", "-", "|"
	}
	return "╭", "╮", "╰", "╯", "─", "│"
}

// panel draws a w×h box: title in the top edge, foot at the right of the bottom edge, and body
// lines inside, each cut or padded to w-2. The border and title use Accent when focused.
func (m Model) panel(title, foot string, w, h int, focused bool, body []string) []string {
	if h <= 0 {
		return nil
	}
	if w < 2 {
		return make([]string, h)
	}
	tl, tr, bl, br, hz, vt := m.border()
	bp := m.theme.st.borderBlur
	if focused {
		bp = m.theme.st.borderFocus
	}
	out := make([]string, 0, h)
	out = append(out, m.edge(tl, hz, tr, title, w, bp, false))
	for i := 0; i < h-2; i++ {
		l := ""
		if i < len(body) {
			l = body[i]
		}
		out = append(out, bp.S(vt)+m.theme.Pad(l, w-2)+bp.S(vt))
	}
	if h >= 2 {
		out = append(out, m.edge(bl, hz, br, foot, w, bp, true))
	}
	return out
}

// edge is a top or bottom border with an optional label: "╭─ label ───╮" or "╰─── label ─╯".
func (m Model) edge(l, hz, r, label string, w int, bp paint, right bool) string {
	if label == "" || w < 8 {
		return bp.S(l + strings.Repeat(hz, max(0, w-2)) + r)
	}
	label = m.theme.Truncate(label, w-6)
	fill := w - ansi.StringWidth(label) - 5
	if right {
		return bp.S(l+strings.Repeat(hz, fill)) + " " + label + " " + bp.S(hz+r)
	}
	return bp.S(l+hz) + " " + label + " " + bp.S(strings.Repeat(hz, fill)+r)
}

// titleText is a panel title in Accent (focused) or Muted.
func (m Model) titleText(s string, focused bool) string {
	if focused {
		return m.theme.st.titleFocus.S(s)
	}
	return m.theme.st.titleBlur.S(s)
}

// ── header ───────────────────────────────────────────────────────────────────

func (m Model) count(s agents.Status) int {
	if m.snap == nil {
		return 0
	}
	n := 0
	for _, a := range m.snap.Agents() {
		if a.Pane.Agent.Status == s {
			n++
		}
	}
	return n
}

// countBadges are the non-zero status counts: "◆ 1 waiting  ⠋ 1 working  ○ 1 idle".
func (m Model) countBadges(words bool) []string {
	var out []string
	for _, s := range []agents.Status{agents.Waiting, agents.Working, agents.Idle, agents.Starting, agents.Exited, agents.Unknown} {
		n := m.count(s)
		if n == 0 {
			continue
		}
		text := m.sym(s) + " " + strconv.Itoa(n)
		if words {
			text += " " + string(s)
		}
		p := m.theme.st.statusPaint(s)
		if s == agents.Waiting {
			p = m.theme.st.waitingBold
		}
		out = append(out, p.S(text))
	}
	return out
}

func (m Model) headerLines(w, h int) []string {
	if h <= 0 {
		return nil
	}
	st := m.theme.st
	now := m.opts.Now()
	info := st.muted.S("loading" + m.theme.Ellipsis())
	if m.snap != nil {
		info = st.muted.S(plural(len(m.snap.Sessions), "session") + " " + m.dot() + " " + plural(len(m.snap.Agents()), "agent"))
		if m.snap.Server.Socket != "default" {
			info += st.muted.S(" " + m.dot() + " " + m.snap.Server.Socket)
		}
	}
	if h < 4 {
		left := st.brand
		if c := m.countBadges(false); len(c) > 0 {
			left += "  " + strings.Join(c, "  ")
		}
		return []string{m.spread(left, info, w)}
	}
	counts := strings.Join(m.countBadges(true), "  ")
	if counts == "" && m.snap != nil {
		counts = st.muted.S("no agents")
	}
	fresh := ""
	switch {
	case m.refreshing && !m.refreshStart.IsZero() && now.Sub(m.refreshStart) > 500*time.Millisecond:
		fresh = st.accent.S(m.spinnerFrame()) + st.muted.S(" refreshing")
	case m.snap != nil:
		arrow := "↻"
		if m.theme.ASCII {
			arrow = "updated"
		}
		fresh = st.muted.S(arrow + " " + Age(now.Sub(m.snap.Taken)) + " ago")
	}
	iw := w - 4
	body := []string{" " + m.spread(st.brand, info, iw), " " + m.spread(counts, fresh, iw)}
	return m.panel("", "", w, 4, false, body)
}

// ── body ─────────────────────────────────────────────────────────────────────

func (m Model) bodyLines(L layout) []string {
	r := L.body
	if r.h <= 0 {
		return nil
	}
	switch {
	case m.mode == modeLog:
		return m.logPanel(r)
	case m.empty():
		return m.emptyLines(r)
	case L.detail.w > 0 && L.sessions.w > 0 && L.detail.y == L.sessions.y:
		left, right := m.sessionsPanel(L.sessions), m.detailPanel(L.detail)
		out := make([]string, r.h)
		for i := range out {
			l, rt := "", ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				rt = right[i]
			}
			out[i] = m.theme.Pad(l, L.sessions.w) + rt
		}
		return out
	}
	var out []string
	if L.sessions.h > 0 {
		out = append(out, m.sessionsPanel(L.sessions)...)
	}
	if L.detail.h > 0 {
		out = append(out, m.detailPanel(L.detail)...)
	}
	return out
}

func (m Model) sessionsPanel(r rect) []string {
	st := m.theme.st
	focused := !m.focusDetail && m.mode == modeList
	title := m.titleText("Sessions", focused)
	var extra []string
	if m.agentsOnly {
		extra = append(extra, "agents only")
	}
	if m.filter != "" {
		extra = append(extra, "filter "+m.filter)
	}
	if len(extra) > 0 {
		title += st.muted.S(" " + m.dot() + " " + strings.Join(extra, " "+m.dot()+" "))
	}
	foot := ""
	if rows := r.h - 2; rows > 0 && len(m.rows) > rows {
		foot = st.muted.S(fmt.Sprintf("%d/%d", m.cursor+1, len(m.rows)))
	}
	return m.panel(title, foot, r.w, r.h, focused, m.listLines(r.w-2, r.h-2))
}

// listLines draws the session list from m.offset, which syncLayout keeps on the cursor.
func (m Model) listLines(iw, h int) []string {
	st := m.theme.st
	if m.snap == nil {
		return []string{" " + st.muted.S("reading tmux"+m.theme.Ellipsis())}
	}
	if len(m.rows) == 0 {
		return []string{"", " nothing matches", " " + st.muted.S("esc clears the filter, g shows all")}
	}
	var out []string
	now := m.opts.Now()
	for i := m.offset; i < len(m.rows) && len(out) < h; i++ {
		out = append(out, m.rowLine(m.rows[i], iw, i == m.cursor, now))
	}
	return out
}

// rowLine is one list row, exactly iw cells: the selection bar, a space, the content, a space.
func (m Model) rowLine(r row, iw int, sel bool, now time.Time) string {
	st := m.theme.st
	cw := iw - 3
	if cw < 1 {
		return m.theme.Pad("", iw)
	}
	bar := " "
	if sel {
		mark := "▌"
		if m.theme.ASCII {
			mark = ">"
		}
		bar = st.accent.S(mark)
	}
	line := bar + " " + m.theme.Pad(m.rowContent(r, cw, now), cw) + " "
	if sel {
		line = st.onSurface(line)
	}
	return line
}

func (m Model) rowContent(r row, cw int, now time.Time) string {
	st := m.theme.st
	s := m.snap.Session(r.session)
	if s == nil {
		return ""
	}
	if r.kind == rowSession {
		meta := fmt.Sprintf("%dw", len(s.Windows))
		tail := " " + st.muted.S(meta)
		tw := 1 + len(meta)
		if s.Attached > 0 {
			tail += " " + st.pill.S(" att ")
			tw += 6
		}
		return st.bold.S(m.theme.Truncate(s.Name, max(1, cw-tw))) + tail
	}
	p := m.paneOf(s, r.pane)
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
	sp := st.statusPaint(a.Status)
	name, text := st.agentPaint(a.Agent), st.text
	if m.pulseOn(p.ID, now) {
		name, text = st.statusPaint(agents.Waiting), st.statusPaint(agents.Waiting)
	}
	head := "  " + sp.S(m.sym(a.Status)) + " " + name.S(m.theme.Pad(a.Agent, AgentIDWidth)) + " "
	headW := 4 + AgentIDWidth + 1
	needs, needsW := "", 0
	if a.Status == agents.Waiting {
		needs, needsW = st.waitingBold.S("needs you")+" ", 10
	}
	titleW := cw - headW - needsW - len(age) - 1
	if titleW < 4 && needsW > 0 {
		titleW += needsW
		needs = ""
	}
	if titleW < 4 {
		return head
	}
	title := a.Title
	if title == "" {
		title = m.theme.Dash()
	}
	return head + needs + text.S(m.theme.Pad(title, titleW)) + " " + st.muted.S(age)
}

func (m Model) paneOf(s *snapshot.Session, id string) *snapshot.Pane {
	for _, p := range s.SessionAgents() {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// ── detail ───────────────────────────────────────────────────────────────────

func (m Model) detailPanel(r rect) []string {
	st := m.theme.st
	focused := m.focusDetail && m.mode == modeList || m.mode == modeDetail
	s, p := m.selected()
	if s == nil {
		return m.panel(m.titleText("Detail", focused), "", r.w, r.h, focused, nil)
	}
	cw, ih := r.w-4, r.h-2
	var title string
	var body []string
	if p != nil && p.Agent != nil {
		a := p.Agent
		title = st.statusPaint(a.Status).S(m.sym(a.Status)) + " " + m.titleText(a.DisplayName, focused)
		body = m.agentDetail(p, cw, ih)
	} else {
		title = m.titleText("Session "+s.Name, focused)
		body = m.sessionDetail(s, cw, ih)
	}
	for i, l := range body {
		body[i] = " " + l
	}
	return m.panel(title, "", r.w, r.h, focused, body)
}

// labelW is the label column of the detail grid: "Session" and two spaces.
const labelW = 9

// grid appends "Label    value" rows. A value that does not fit is cut, or with wrap,
// word-wrapped under itself.
func (m Model) grid(out []string, cw int, k, v string, wrap bool) []string {
	st := m.theme.st
	label := st.muted.S(fmt.Sprintf("%-*s", labelW, k))
	if v == "" {
		v = m.theme.Dash()
	}
	vw := max(1, cw-labelW)
	if ansi.StringWidth(v) <= vw || !wrap {
		return append(out, label+m.theme.Truncate(v, vw))
	}
	for i, l := range strings.Split(ansi.Wrap(v, vw, " "), "\n") {
		if i == 0 {
			out = append(out, label+l)
		} else {
			out = append(out, strings.Repeat(" ", labelW)+l)
		}
	}
	return out
}

func (m Model) agentDetail(p *snapshot.Pane, cw, ih int) []string {
	st := m.theme.st
	a := p.Agent
	now := m.opts.Now()
	up := ""
	if !a.StartedAt.IsZero() {
		up = " " + m.dot() + " up " + Age(now.Sub(a.StartedAt))
	}
	out := []string{st.statusPaint(a.Status).S(string(a.Status)) + st.muted.S(up)}
	if ih >= 14 {
		out = append(out, "")
	}
	// The session id is whole wherever it fits, so it can be read and copied.
	label := st.muted.S(fmt.Sprintf("%-*s", labelW, "Session"))
	id := a.SessionID
	switch iw := ansi.StringWidth(id); {
	case id == "":
		out = append(out, label+m.theme.Dash())
	case labelW+iw+8 <= cw:
		out = append(out, label+id+"  "+st.muted.S("y copy"))
	case labelW+iw <= cw:
		out = append(out, label+id)
	case iw <= cw:
		out = append(out, label, id)
	default:
		out = append(out, label+m.theme.Truncate(id, max(1, cw-labelW)))
	}
	out = m.grid(out, cw, "Title", a.Title, false)
	out = m.grid(out, cw, "Path", ShortPath(a.Path, m.opts.Home), false)
	out = m.grid(out, cw, "PID", strconv.Itoa(a.PID), false)
	out = m.grid(out, cw, "Target", p.Target+" "+p.ID, false)
	if m.showSource {
		out = m.grid(out, cw, "Source", a.SourceString()+" ("+a.Confidence+")", true)
	}
	if a.Note != "" {
		out = m.grid(out, cw, "Note", a.Note, true)
	}
	return m.livePanel(out, cw, ih)
}

func (m Model) sessionDetail(s *snapshot.Session, cw, ih int) []string {
	st := m.theme.st
	now := m.opts.Now()
	meta := "created " + AgeSince(now, s.Created) + " ago " + m.dot() + " " + plural(len(s.Windows), "window")
	line := st.muted.S(meta)
	if s.Attached > 0 {
		line += st.muted.S(" "+m.dot()+" ") + st.accent.S("attached")
	}
	out := []string{line}
	if ih >= 14 {
		out = append(out, "")
	}
	for _, win := range s.Windows {
		out = append(out, st.bold.S(fmt.Sprintf("%d: %s", win.Index, win.Name)))
		for _, wp := range win.Panes {
			head := fmt.Sprintf("  .%d %s ", wp.Index, wp.ID)
			if wp.Agent != nil {
				out = append(out, head+st.agentPaint(wp.Agent.Agent).S(wp.Agent.Agent)+" "+m.badge(wp.Agent.Status))
			} else {
				out = append(out, head+st.text.S(wp.Command)+"  "+st.muted.S(ShortPath(wp.Path, m.opts.Home)))
			}
		}
	}
	return m.livePanel(out, cw, ih)
}

// livePanel adds the nested "live" panel with the pane's last lines, when there is room.
func (m Model) livePanel(out []string, cw, ih int) []string {
	if !m.preview || m.height < previewMinH {
		return out
	}
	room := ih - len(out) - 1
	if room < 4 || cw < 10 {
		return out
	}
	var pv []string
	if m.previewPane == m.selectedPaneID() {
		pv = strings.Split(strings.TrimRight(m.previewText, "\n "), "\n")
	}
	n := room - 2
	off := max(0, min(m.previewOff, len(pv)-n))
	end := len(pv) - off
	start := max(0, end-n)
	var lines []string
	for _, l := range pv[start:end] {
		lines = append(lines, " "+m.theme.Truncate(ansi.Strip(l), cw-4))
	}
	foot := ""
	if off > 0 {
		up := "↑"
		if m.theme.ASCII {
			up = "^"
		}
		foot = m.theme.st.muted.S(fmt.Sprintf("%s%d", up, off))
	}
	out = append(out, "")
	return append(out, m.panel(m.theme.st.muted.S("live"), foot, cw, room, false, lines)...)
}

// ── log ──────────────────────────────────────────────────────────────────────

func (m Model) logPanel(r rect) []string {
	st := m.theme.st
	ih := r.h - 2
	var lines []string
	for _, e := range m.events {
		lines = append(lines, " "+EventLine(m.theme, e, m.opts.Home))
	}
	if len(lines) == 0 {
		lines = []string{"", " " + st.muted.S("no changes yet in this run")}
	}
	off := max(0, min(m.logOffset, len(lines)-1))
	end := max(1, len(lines)-off)
	start := max(0, end-ih)
	foot := ""
	if len(lines) > ih {
		foot = st.muted.S(fmt.Sprintf("%d/%d", end, len(lines)))
	}
	return m.panel(m.titleText("Log", true), foot, r.w, r.h, true, lines[start:end])
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

// ── empty state ──────────────────────────────────────────────────────────────

func (m Model) emptyLines(r rect) []string {
	st := m.theme.st
	var block []string
	if m.height >= logoMinH && r.h >= 9 {
		lw := 0
		for _, l := range st.logo {
			lw = max(lw, ansi.StringWidth(l))
		}
		pad := strings.Repeat(" ", max(0, (r.w-lw)/2))
		for _, l := range st.logo {
			block = append(block, pad+l)
		}
		block = append(block, "")
	}
	centre := func(s string) string {
		return strings.Repeat(" ", max(0, (r.w-ansi.StringWidth(s))/2)) + s
	}
	block = append(block, centre(st.bold.S("No tmux sessions yet")), centre(st.muted.S("press n to start one")))
	if m.snap.Server.Socket != "default" {
		block = append(block, centre(st.muted.S("on "+m.snap.Server.Socket)))
	}
	top := max(0, (r.h-len(block))/2)
	return fit(append(make([]string, top), block...), r.h)
}

// ── command bar and footer ───────────────────────────────────────────────────

func (m Model) barLines(w, h int) []string {
	if h <= 0 {
		return nil
	}
	st := m.theme.st
	mark := "› "
	if m.theme.ASCII {
		mark = "> "
	}
	var content string
	switch {
	case m.barOn:
		content = st.accent.S(mark) + m.bar.View()
	case m.filter != "":
		content = st.muted.S(mark) + st.text.S("filter "+m.filter) + st.muted.S("  "+m.dot()+" esc clears")
	case w < narrowWidth:
		content = st.muted.S(mark + "/ commands " + m.dot() + " type to filter")
	default:
		content = st.muted.S(mark + "type / for commands " + m.dot() + " or start typing to filter")
	}
	if h >= 3 {
		return m.panel("", "", w, 3, m.barOn, []string{" " + content})
	}
	return []string{content}
}

// hints are the footer's key hints for the current state.
func (m Model) hints() []string {
	move := "↑↓ move"
	if m.theme.ASCII {
		move = "jk move"
	}
	switch {
	case m.barOn && m.barCommand():
		return []string{"tab complete", "↑↓ choose", "enter run", "esc cancel"}
	case m.barOn:
		return []string{"enter keep", move, "esc clear"}
	}
	switch m.mode {
	case modeDetail:
		return []string{"↑↓ scroll", "enter attach", "y copy id", "esc back"}
	case modeHelp:
		return []string{"↑↓ scroll", "any key closes"}
	case modeLog:
		return []string{"↑↓ scroll", "esc back"}
	case modeKill:
		return []string{"←→ choose", "enter confirm", "esc cancel"}
	case modeSend:
		return []string{"enter send", "tab toggle Enter", "esc cancel"}
	case modeNew:
		return []string{"tab next", "←→ agent", "enter next/create", "esc cancel"}
	case modeRename:
		return []string{"enter rename", "esc cancel"}
	}
	if m.empty() {
		return []string{"n new", "/ commands", "t theme", "? help", "q quit"}
	}
	if m.focusDetail {
		return []string{"↑↓ scroll", "tab focus", "enter attach", "/ commands", "? help"}
	}
	return []string{move, "enter attach", "tab focus", "/ commands", "t theme", "? help", "q quit"}
}

func (m Model) footer(w int, L layout) string {
	st := m.theme.st
	if m.barOn && m.barErr != "" {
		return st.danger.S(m.theme.Truncate(m.barErr, w))
	}
	help := strings.Join(m.hints(), " "+m.dot()+" ")
	if m.theme.ASCII {
		help = strings.NewReplacer("↑↓", "jk", "←→", "hl").Replace(help)
	}
	right := ""
	switch {
	case m.toast.text != "" && !L.wideToast:
		right = m.toastText()
	case m.status != "" && m.statusErr:
		right = st.danger.S(m.status)
	case m.status != "":
		right = st.text.S(m.status)
	}
	rw := ansi.StringWidth(right)
	if rw+2+ansi.StringWidth(help) > w {
		help = "? help"
		if m.mode != modeList || m.barOn {
			help = "esc back"
		}
	}
	if right == "" {
		return st.muted.S(help)
	}
	room := w - ansi.StringWidth(help) - 2
	if room < 8 {
		return m.theme.Truncate(right, w)
	}
	right = m.theme.Truncate(right, room)
	return st.muted.S(help) + strings.Repeat(" ", w-ansi.StringWidth(help)-ansi.StringWidth(right)) + right
}

// badge is Theme.Badge with the spinner frame for a working agent.
func (m Model) badge(s agents.Status) string {
	p := m.theme.st.statusPaint(s)
	if s == agents.Waiting {
		p = m.theme.st.waitingBold
	}
	return p.S(m.sym(s) + " " + string(s))
}
