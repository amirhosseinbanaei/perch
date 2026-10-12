package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/amirhosseinbanaei/perch/internal/agents"
)

// Modals are centred panels over the screen: new, rename, send, kill and help. Each has a bold
// title, its fields and a dim hint line, an Accent border, and a Surface background when the
// terminal's background is known.

func (m Model) modalWidth() int {
	if m.width < narrowWidth {
		return max(10, m.width-2)
	}
	return min(60, m.width-4)
}

// modalMaxH is the tallest a modal may be: the screen less a line above and below, or the
// whole screen when it is short.
func (m Model) modalMaxH() int {
	if m.height <= 12 {
		return max(3, m.height)
	}
	return m.height - 2
}

// modalBox frames title, body and hint. When it does not fit the height, the blank lines
// around the body go first, then the body is cut.
func (m Model) modalBox(title string, body []string, hint, foot string) []string {
	st := m.theme.st
	mw := m.modalWidth()
	maxH := m.modalMaxH()
	if m.theme.ASCII {
		hint = strings.NewReplacer("←→", "hl", "↑↓", "jk").Replace(hint)
	}
	gap := len(body)+6 <= maxH
	if !gap && len(body)+4 > maxH {
		body = body[:max(0, maxH-4)]
	}
	lines := []string{" " + st.bold.S(title)}
	if gap {
		lines = append(lines, "")
	}
	for _, l := range body {
		lines = append(lines, " "+l)
	}
	if gap {
		lines = append(lines, "")
	}
	lines = append(lines, " "+st.muted.S(hint))
	// The Surface fills the inside only, so it never spills past the border's line.
	for i, l := range lines {
		lines[i] = st.onSurface(m.theme.Pad(l, mw-2))
	}
	return m.panel("", foot, mw, len(lines)+2, true, lines)
}

// modalLines is the open modal, or nil.
func (m Model) modalLines() []string {
	switch m.mode {
	case modeKill:
		return m.killModal()
	case modeNew:
		return m.newModal()
	case modeRename:
		return m.renameModal()
	case modeSend:
		return m.sendModal()
	case modeHelp:
		return m.helpModal()
	}
	return nil
}

// button is "[ label ]", reversed when focused.
func (m Model) button(label string, focused bool, normal, active paint) string {
	text := "[ " + label + " ]"
	if focused {
		return active.S(text)
	}
	return normal.S(text)
}

func (m Model) killModal() []string {
	st := m.theme.st
	cw := m.modalWidth() - 4
	name := m.theme.Truncate(m.formTarget, max(4, cw-15))
	body := []string{
		"Kill session " + st.bold.S(name) + "?",
		st.muted.S("This closes every window in it."),
		"",
		m.button("No", !m.killYes, st.text, st.pill) + "  " + m.button("Yes, kill", m.killYes, st.danger, st.dangerRev),
	}
	return m.modalBox("Kill session", body, "←→ choose "+m.dot()+" enter confirm "+m.dot()+" esc cancel", "")
}

// fieldLabel is a form label, in Accent when its field has focus.
func (m Model) fieldLabel(s string, focused bool) string {
	s = fmt.Sprintf("%-7s", s)
	if focused {
		return m.theme.st.accentBold.S(s)
	}
	return m.theme.st.muted.S(s)
}

func (m Model) newModal() []string {
	if len(m.inputs) < 2 {
		return nil
	}
	cw := m.modalWidth() - 4
	body := []string{
		m.fieldLabel("Name", m.focus == 0) + m.inputs[0].View(),
		m.fieldLabel("Dir", m.focus == 1) + m.inputs[1].View(),
		m.fieldLabel("Agent", m.focus == 2) + m.picker(cw-7),
	}
	return m.modalBox("New session", body, "tab next "+m.dot()+" ←→ agent "+m.dot()+" enter create "+m.dot()+" esc cancel", "")
}

// picker is the agent choice: "shell" and each agent found on PATH, in its own colour. The
// chosen one is reversed; the row scrolls so it stays in view.
func (m Model) picker(w int) string {
	st := m.theme.st
	items := make([]string, len(m.agentChoices))
	widths := make([]int, len(m.agentChoices))
	for i, id := range m.agentChoices {
		text := " " + id + " "
		p, rev := st.agentPaint(id), st.agentRevPaint(id)
		if id == "shell" {
			p, rev = st.text, st.textRev
		}
		if i == m.agentPick {
			items[i] = rev.S(text)
		} else {
			items[i] = p.S(text)
		}
		widths[i] = ansi.StringWidth(text) + 1
	}
	start, total := 0, 0
	for i := 0; i <= m.agentPick && i < len(widths); i++ {
		total += widths[i]
	}
	for total > w && start < m.agentPick {
		total -= widths[start]
		start++
	}
	return strings.Join(items[start:], " ")
}

func (m Model) renameModal() []string {
	if len(m.inputs) < 1 {
		return nil
	}
	st := m.theme.st
	body := []string{
		m.fieldLabel("Now", false) + st.bold.S(m.formTarget),
		m.fieldLabel("New", true) + m.inputs[0].View(),
	}
	return m.modalBox("Rename session", body, "enter rename "+m.dot()+" esc cancel", "")
}

func (m Model) sendModal() []string {
	if len(m.inputs) < 1 {
		return nil
	}
	st := m.theme.st
	box := "[ ]"
	if m.sendEnter {
		box = "[x]"
	}
	target := m.formTarget
	if s, _ := m.selected(); s != nil {
		target += " in " + s.Name
	}
	body := []string{
		m.fieldLabel("Text", true) + m.inputs[0].View(),
		strings.Repeat(" ", 7) + st.text.S(box+" and press Enter") + st.muted.S("  tab"),
	}
	return m.modalBox("Send to "+target, body, "enter send "+m.dot()+" esc cancel", "")
}

// helpGroups is the help table: Navigate, Act, View and Commands.
func (m Model) helpGroups() [][]string {
	move := "↑↓ jk"
	if m.theme.ASCII {
		move = "jk"
	}
	cmds := []string{"Commands", "/ or :", "open the command bar", "typing", "filter the list; esc clears"}
	for _, c := range commandList() {
		cmds = append(cmds, "/"+c.name, c.desc)
	}
	return [][]string{
		{"Navigate", move, "move, or scroll the focused panel", "pgup pgdn", "a page", "home end G", "first, last",
			"tab", "focus Sessions or Detail", "enter", "attach; on a narrow screen, open Detail", "esc", "back, or clear the filter"},
		{"Act", "a", "attach", "n", "new session", "r", "rename", "x", "kill (asks first)", "s", "send text", "y", "copy the session id"},
		{"View", "p", "live preview", "g", "agents only", "l", "log", "t", "next theme", "R", "refresh", "?", "this help", "q", "quit"},
		cmds,
	}
}

func (m Model) helpModal() []string {
	st := m.theme.st
	cw := m.modalWidth() - 4
	var lines []string
	for gi, g := range m.helpGroups() {
		if gi > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, st.accentBold.S(g[0]))
		for i := 1; i+1 < len(g); i += 2 {
			lines = append(lines, st.text.S(fmt.Sprintf("  %-11s", g[i]))+st.muted.S(m.theme.Truncate(g[i+1], max(1, cw-13))))
		}
	}
	lines = append(lines, "", st.accentBold.S("Statuses"))
	for _, s := range agents.Statuses() {
		lines = append(lines, "  "+m.theme.Badge(s))
	}
	room := max(1, min(m.modalMaxH(), 40)-6)
	off := max(0, min(m.helpOffset, len(lines)-room))
	end := min(len(lines), off+room)
	foot := ""
	if len(lines) > room {
		foot = st.muted.S(fmt.Sprintf("%d/%d", end, len(lines)))
	}
	return m.modalBox("Help", lines[off:end], "↑↓ scroll "+m.dot()+" any key closes", foot)
}
