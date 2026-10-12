package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/amirhosseinbanaei/perch/internal/agents"
)

// paint is a compiled style: the escape sequences before and after the text. Building it once
// per theme keeps lipgloss out of the per-frame path.
type paint struct{ pre, post string }

// S wraps s in the style.
func (p paint) S(s string) string {
	if p.pre == "" || s == "" {
		return s
	}
	return p.pre + s + p.post
}

func paintOf(st lipgloss.Style) paint {
	r := st.Render("x")
	i := strings.IndexByte(r, 'x')
	if i <= 0 {
		return paint{}
	}
	return paint{pre: r[:i], post: r[i+1:]}
}

// styles is every TUI style for one palette, compiled by Theme.Compile when the theme, the
// background or the colour mode changes.
type styles struct {
	pal       Palette
	color     bool
	surfaceOn bool // a Surface background is drawn: colour on and the background known

	text, muted, faint, accent, accentBold, soft, danger, success, bold paint
	titleFocus, titleBlur, borderFocus, borderBlur                      paint
	waitingBold, pill, surface, dangerRev, textRev                      paint
	agentRev                                                            map[string]paint

	status map[agents.Status]paint
	agent  map[string]paint
	brand  string   // "✦ perch" in the gradient
	logo   []string // the empty-state logo in the gradient
}

func newStyles(t Theme) *styles {
	dark := t.Dark == nil || *t.Dark
	p := themePalette(t.Name, dark)
	st := &styles{pal: p, color: t.Color, surfaceOn: t.Color && t.Dark != nil,
		status: map[agents.Status]paint{}, agent: map[string]paint{}, agentRev: map[string]paint{}}
	ns := lipgloss.NewStyle
	fg := func(hex string) lipgloss.Style { return ns().Foreground(lipgloss.Color(hex)) }
	st.bold = paintOf(ns().Bold(true))
	if !t.Color {
		// No colour at all: hierarchy from bold and faint, status from the symbols.
		faint := paintOf(ns().Faint(true))
		st.muted, st.faint, st.borderBlur, st.titleBlur = faint, faint, faint, faint
		st.accentBold, st.titleFocus, st.waitingBold = st.bold, st.bold, st.bold
		st.pill = paintOf(ns().Reverse(true))
		st.dangerRev, st.textRev = st.pill, st.pill
		st.brand = st.bold.S(brandText(t.ASCII))
		st.logo = logoArt(t.ASCII)
		return st
	}
	st.text = paintOf(fg(p.Text))
	st.muted = paintOf(fg(p.Muted))
	st.faint = paintOf(fg(p.Faint))
	st.accent = paintOf(fg(p.Accent))
	st.accentBold = paintOf(fg(p.Accent).Bold(true))
	st.soft = paintOf(fg(p.AccentSoft))
	st.danger = paintOf(fg(p.Danger))
	st.success = paintOf(fg(p.Success))
	st.titleFocus = st.accentBold
	st.titleBlur = st.muted
	st.borderFocus = st.accent
	st.borderBlur = st.faint
	st.waitingBold = paintOf(fg(p.Waiting).Bold(true))
	st.pill = paintOf(fg(p.Accent).Reverse(true))
	st.dangerRev = paintOf(fg(p.Danger).Reverse(true))
	st.textRev = paintOf(fg(p.Text).Reverse(true))
	if st.surfaceOn {
		st.surface = paintOf(ns().Background(lipgloss.Color(p.Surface)))
	}
	for s, hex := range map[agents.Status]string{
		agents.Working: p.Working, agents.Waiting: p.Waiting, agents.Idle: p.Idle,
		agents.Exited: p.Exited, agents.Starting: p.AccentSoft, agents.Unknown: p.Muted,
	} {
		st.status[s] = paintOf(fg(hex))
	}
	for _, id := range []string{"claude", "codex", "gemini", "hermes", "opencode"} {
		st.agent[id] = paintOf(fg(p.agentColor(id)))
		st.agentRev[id] = paintOf(fg(p.agentColor(id)).Reverse(true))
	}
	st.brand = gradient([]string{brandText(t.ASCII)}, p, true)[0]
	st.logo = gradient(logoArt(t.ASCII), p, false)
	return st
}

// gradient paints each rune from Accent to AccentSoft by column, one colour per rune.
func gradient(lines []string, p Palette, bold bool) []string {
	w := 0
	for _, l := range lines {
		w = max(w, len([]rune(l)))
	}
	if w == 0 {
		return lines
	}
	cols := lipgloss.Blend1D(max(w, 2), lipgloss.Color(p.Accent), lipgloss.Color(p.AccentSoft))
	out := make([]string, len(lines))
	for i, l := range lines {
		var b strings.Builder
		for j, r := range []rune(l) {
			if r == ' ' {
				b.WriteRune(r)
				continue
			}
			b.WriteString(paintOf(lipgloss.NewStyle().Foreground(cols[j]).Bold(bold)).S(string(r)))
		}
		out[i] = b.String()
	}
	return out
}

func brandText(ascii bool) string {
	if ascii {
		return "* perch"
	}
	return "✦ perch"
}

// statusPaint is the status colour; an unknown status is muted.
func (st *styles) statusPaint(s agents.Status) paint {
	if p, ok := st.status[s]; ok {
		return p
	}
	return st.muted
}

// agentPaint is the agent's colour.
func (st *styles) agentPaint(id string) paint {
	if p, ok := st.agent[id]; ok {
		return p
	}
	return st.text
}

// agentRevPaint is the agent's colour reversed, for the chosen item in the agent picker.
func (st *styles) agentRevPaint(id string) paint {
	if p, ok := st.agentRev[id]; ok {
		return p
	}
	return st.pill
}

// onSurface draws line (exactly w cells) on the Surface background, carrying the background
// through every reset inside it. Without a known background it returns line unchanged.
func (st *styles) onSurface(line string) string {
	if !st.surfaceOn {
		return line
	}
	bg := st.surface.pre
	return bg + strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+bg) + "\x1b[m"
}
