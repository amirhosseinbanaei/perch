package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amirhosseinbanaei/perch/internal/config"
)

// The command bar is the Claude Code-style input under the body. "/" or ":" opens it on a
// command, and any other key that is not a shortcut opens it as a live filter.

type command struct{ name, args, desc string }

// commandList is every command in the order the popup lists them.
func commandList() []command {
	return []command{
		{"attach", "[target]", "attach to the selection, or to a target"},
		{"new", "<name> [-c dir] [--agent id]", "new session; alone, opens the form"},
		{"rename", "<new name>", "rename the selected session"},
		{"kill", "[name]", "kill a session, after a confirm"},
		{"send", "<text>", "send text and Enter to the selected pane"},
		{"filter", "<text>", "filter the list; empty clears it"},
		{"agents", "", "only sessions with agents, or all"},
		{"theme", "[coral|teal|violet]", "switch theme; alone, the next one"},
		{"preview", "", "show or hide the live preview"},
		{"source", "", "show or hide where each field came from"},
		{"log", "", "the changes seen in this run"},
		{"refresh", "", "refresh now"},
		{"help", "", "keys and commands"},
		{"quit", "", "quit perch"},
	}
}

// matchCommands lists the commands for q (the text after "/"): prefix matches first, then
// subsequence (fuzzy) matches, each group in table order.
func matchCommands(q string) []command {
	q = strings.ToLower(q)
	var prefix, fuzzy []command
	for _, c := range commandList() {
		switch {
		case strings.HasPrefix(c.name, q):
			prefix = append(prefix, c)
		case subsequence(q, c.name):
			fuzzy = append(fuzzy, c)
		}
	}
	return append(prefix, fuzzy...)
}

// subsequence reports whether every rune of q appears in s, in order.
func subsequence(q, s string) bool {
	i := 0
	rs := []rune(q)
	for _, r := range s {
		if i < len(rs) && r == rs[i] {
			i++
		}
	}
	return i == len(rs)
}

// splitArgs splits a command's arguments on spaces; '…' and "…" group words.
func splitArgs(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	var quote rune
	in := false
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote, in = r, true
		case r == ' ' || r == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed %c", quote)
	}
	if in {
		out = append(out, cur.String())
	}
	return out, nil
}

// parsed is a command line split into the command and the rest.
type parsed struct {
	cmd    command
	rest   string // the raw text after the command
	filter string // set when the line names no command and has no arguments: filter by it
}

// parseCommand resolves a "/…" line: an exact name, else the popup's selected match (sel) when
// there are no arguments, else the one command the name is a prefix of.
func parseCommand(line string, sel int) (parsed, error) {
	body := strings.TrimPrefix(line, "/")
	name, rest, _ := strings.Cut(strings.TrimLeft(body, " "), " ")
	rest = strings.TrimSpace(rest)
	name = strings.ToLower(name)
	for _, c := range commandList() {
		if c.name == name {
			return parsed{cmd: c, rest: rest}, nil
		}
	}
	matches := matchCommands(name)
	if rest == "" {
		if len(matches) > 0 {
			return parsed{cmd: matches[max(0, min(sel, len(matches)-1))]}, nil
		}
		if name != "" {
			return parsed{filter: name}, nil
		}
		return parsed{}, errors.New("type a command; tab completes")
	}
	var prefix []command
	for _, c := range matches {
		if strings.HasPrefix(c.name, name) {
			prefix = append(prefix, c)
		}
	}
	if len(prefix) == 1 {
		return parsed{cmd: prefix[0], rest: rest}, nil
	}
	return parsed{}, fmt.Errorf("unknown command /%s", name)
}

// ── the bar ──────────────────────────────────────────────────────────────────

func (m Model) newBar() textinput.Model {
	in := m.newInput("", "")
	in.SetWidth(max(8, m.width-8))
	return in
}

// barCommand reports whether the bar holds a command rather than a filter.
func (m Model) barCommand() bool { return strings.HasPrefix(m.bar.Value(), "/") }

// openBar focuses the bar with text typed into it.
func (m Model) openBar(text string) (tea.Model, tea.Cmd) {
	m.bar = m.newBar()
	m.bar.SetValue(text)
	m.bar.CursorEnd()
	m.barOn, m.barErr, m.barSel, m.barOff = true, "", 0, 0
	cmd := m.bar.Focus()
	m.barChanged()
	return m, cmd
}

func (m *Model) closeBar() {
	m.bar.Blur()
	m.barOn, m.barErr = false, ""
}

// barChanged follows the bar's text: a filter updates the list as it is typed.
func (m *Model) barChanged() {
	if m.barCommand() {
		return
	}
	if v := m.bar.Value(); v != m.filter {
		m.filter = v
		m.buildRows()
		m.clamp()
	}
}

func (m Model) barKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		if !m.barCommand() && m.filter != "" {
			m.filter = ""
			m.buildRows()
			m.clamp()
		}
		m.closeBar()
		return m, m.previewCmd()
	case "enter":
		if !m.barCommand() {
			m.closeBar()
			return m, m.previewCmd()
		}
		return m.runLine(m.bar.Value())
	case "tab":
		if m.barCommand() {
			q, _, args := strings.Cut(strings.TrimPrefix(m.bar.Value(), "/"), " ")
			if ms := matchCommands(q); !args && len(ms) > 0 {
				c := ms[max(0, min(m.barSel, len(ms)-1))]
				v := "/" + c.name
				if c.args != "" {
					v += " "
				}
				m.bar.SetValue(v)
				m.bar.CursorEnd()
				m.barSel, m.barErr = 0, ""
			}
		}
		return m, nil
	case "up", "down", "ctrl+p", "ctrl+n":
		up := msg.String() == "up" || msg.String() == "ctrl+p"
		if m.barCommand() {
			n := len(m.popupMatches())
			if n > 0 {
				d := 1
				if up {
					d = n - 1
				}
				m.barSel = (m.barSel + d) % n
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
	before := m.bar.Value()
	var cmd tea.Cmd
	m.bar, cmd = m.bar.Update(msg)
	if m.bar.Value() != before {
		m.barErr, m.barSel, m.barOff = "", 0, 0
		m.barChanged()
	}
	return m, cmd
}

// popupMatches are the commands the popup lists for the bar's text.
func (m Model) popupMatches() []command {
	q, _, args := strings.Cut(strings.TrimPrefix(m.bar.Value(), "/"), " ")
	if args {
		p, err := parseCommand(m.bar.Value(), 0)
		if err != nil || p.filter != "" {
			return nil
		}
		return []command{p.cmd}
	}
	return matchCommands(q)
}

// popupLines is the command popup: a panel of up to 8 rows directly above the bar, drawn over
// the body.
func (m Model) popupLines(L layout) []string {
	if !m.barOn || !m.barCommand() {
		return nil
	}
	st := m.theme.st
	rows := min(8, L.body.h-2)
	if rows < 1 {
		return nil
	}
	w := L.body.w
	iw := w - 2
	ms := m.popupMatches()
	var lines []string
	foot := ""
	if len(ms) == 0 {
		q, _, _ := strings.Cut(strings.TrimPrefix(m.bar.Value(), "/"), " ")
		hint := "no command matches"
		if q != "" && !strings.Contains(m.bar.Value(), " ") {
			hint += " " + m.dot() + " enter filters by " + q
		}
		lines = []string{" " + st.muted.S(hint)}
	} else {
		sel := max(0, min(m.barSel, len(ms)-1))
		off := 0
		if sel >= rows {
			off = sel - rows + 1
		}
		usageW := 0
		for _, c := range ms {
			usageW = max(usageW, ansi.StringWidth(usage(c)))
		}
		usageW = min(usageW, max(8, (iw-3)/2))
		for i := off; i < len(ms) && i < off+rows; i++ {
			c := ms[i]
			name := st.accentBold.S("/"+c.name) + st.muted.S(strings.TrimPrefix(usage(c), "/"+c.name))
			if i != sel {
				name = st.text.S("/"+c.name) + st.muted.S(strings.TrimPrefix(usage(c), "/"+c.name))
			}
			if ansi.StringWidth(usage(c)) > usageW {
				name = m.theme.Truncate(name, usageW)
			}
			line := " " + m.theme.Pad(name, usageW) + "  " + st.muted.S(c.desc)
			if i == sel {
				mark := "▌"
				if m.theme.ASCII {
					mark = ">"
				}
				line = st.accent.S(mark) + m.theme.Pad(line, iw-1)
				line = st.onSurface(line)
			} else {
				line = " " + line
			}
			lines = append(lines, line)
		}
		if len(ms) > rows {
			foot = st.muted.S(fmt.Sprintf("%d/%d", sel+1, len(ms)))
		}
	}
	return m.panel(m.titleText("Commands", true), foot, w, len(lines)+2, true, lines)
}

func usage(c command) string {
	if c.args == "" {
		return "/" + c.name
	}
	return "/" + c.name + " " + c.args
}

// runLine runs a command line from the bar. An error stays in the bar, shown under it.
func (m Model) runLine(line string) (tea.Model, tea.Cmd) {
	p, err := parseCommand(line, m.barSel)
	if err == nil {
		var next Model
		var cmd tea.Cmd
		next, cmd, err = m.run(p)
		if err == nil {
			return next, cmd
		}
	}
	m.barErr = err.Error()
	return m, nil
}

// run executes a parsed command. Without an argument a command acts on the selection.
func (m Model) run(p parsed) (Model, tea.Cmd, error) {
	c := m.opts.Collector
	if p.filter != "" {
		m.closeBar()
		m.filter = p.filter
		m.buildRows()
		m.clamp()
		return m, m.previewCmd(), nil
	}
	sel, _ := m.selected()
	needSel := func() error {
		if sel == nil {
			return errors.New("select a session first")
		}
		return nil
	}
	switch p.cmd.name {
	case "attach":
		name := ""
		if p.rest == "" {
			if err := needSel(); err != nil {
				return m, nil, err
			}
			name = sel.Name
		} else {
			if m.snap == nil {
				return m, nil, errors.New("no snapshot yet")
			}
			t, err := m.snap.Resolve(p.rest)
			if err != nil {
				return m, nil, err
			}
			name = t.Session.Name
		}
		m.closeBar()
		return m, m.attachCmd(name), nil
	case "new":
		if p.rest == "" {
			m.closeBar()
			next, cmd := m.openNew()
			return next.(Model), cmd, nil
		}
		args, err := splitArgs(p.rest)
		if err != nil {
			return m, nil, err
		}
		var name, dir, agent string
		for i := 0; i < len(args); i++ {
			switch a := args[i]; {
			case (a == "-c" || a == "--agent") && i+1 >= len(args):
				return m, nil, fmt.Errorf("%s needs a value", a)
			case a == "-c":
				i++
				dir = args[i]
			case a == "--agent":
				i++
				agent = args[i]
			case strings.HasPrefix(a, "-"):
				return m, nil, fmt.Errorf("unknown option %s; /new <name> [-c dir] [--agent id]", a)
			case name != "":
				return m, nil, fmt.Errorf("one name only; quote a name with spaces")
			default:
				name = a
			}
		}
		if dir == "" {
			dir = m.defaultDir()
		}
		cmd, err := m.createCmd(name, dir, agent)
		if err != nil {
			return m, nil, err
		}
		m.closeBar()
		return m, cmd, nil
	case "rename":
		if err := needSel(); err != nil {
			return m, nil, err
		}
		m.closeBar()
		if p.rest == "" {
			next, cmd := m.openRename()
			return next.(Model), cmd, nil
		}
		return m, m.renameCmd(sel.Name, p.rest), nil
	case "kill":
		// Typing /kill never kills: it only opens the confirm.
		name := p.rest
		if name == "" {
			if err := needSel(); err != nil {
				return m, nil, err
			}
			name = sel.Name
		} else if m.snap == nil || m.snap.Session(name) == nil {
			return m, nil, fmt.Errorf("no session named %q", name)
		}
		m.closeBar()
		m.mode, m.killYes, m.formTarget = modeKill, false, name
		return m, nil, nil
	case "send":
		pane := m.selectedPaneID()
		if pane == "" {
			return m, nil, errors.New("select a pane first")
		}
		m.closeBar()
		if p.rest == "" {
			next, cmd := m.openSend()
			return next.(Model), cmd, nil
		}
		text := p.rest
		return m, m.actionCmd("sent to "+pane, func(ctx context.Context) error { return c.SendText(ctx, pane, text, true) }), nil
	case "filter":
		m.closeBar()
		m.filter = p.rest
		m.buildRows()
		m.clamp()
		return m, m.previewCmd(), nil
	case "agents":
		m.closeBar()
		m.agentsOnly = !m.agentsOnly
		m.buildRows()
		m.clamp()
		return m, m.previewCmd(), nil
	case "theme":
		name := p.rest
		if name == "" {
			name = m.nextTheme()
		} else if !config.ValidTheme(name) {
			return m, nil, fmt.Errorf("unknown theme %q; one of %s", name, strings.Join(config.Themes(), " "))
		}
		m.closeBar()
		cmd := m.setTheme(name)
		return m, cmd, nil
	case "preview":
		m.closeBar()
		m.preview = !m.preview
		return m, m.previewCmd(), nil
	case "source":
		m.closeBar()
		m.showSource = !m.showSource
		return m, nil, nil
	case "log":
		m.closeBar()
		m.mode, m.logOffset = modeLog, 0
		return m, nil, nil
	case "refresh":
		m.closeBar()
		return m, m.startRefresh(), nil
	case "help":
		m.closeBar()
		m.mode, m.helpOffset = modeHelp, 0
		return m, nil, nil
	case "quit":
		return m, tea.Quit, nil
	}
	return m, nil, fmt.Errorf("unknown command /%s", p.cmd.name)
}
