package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amirhosseinbanaei/perch/internal/agents"
)

// Theme is how text is drawn, shared by the TUI and the CLI tables: one badge renderer for both.
type Theme struct {
	Color bool // false: no colour at all (--no-color, NO_COLOR, not a terminal)
	ASCII bool // --ascii, or a locale that is not UTF-8
	// Dark is the terminal's background once it is known; until then the TUI uses the dark
	// variant and draws no Surface background.
	Dark *bool
	// Name is the TUI's palette (coral, teal, violet). The CLI leaves it empty and never
	// compiles the theme, so its tables keep the 16 basic ANSI colours, which every terminal
	// theme tunes for its own background.
	Name string
	st   *styles // compiled palette styles; nil in the CLI
}

// Compile builds the palette's styles. The TUI calls it when the theme, the background or the
// colour mode changes, never per frame.
func (t Theme) Compile() Theme {
	t.st = newStyles(t)
	return t
}

// UTF8Locale reports whether the first of LC_ALL, LC_CTYPE and LANG that is set names UTF-8.
func UTF8Locale(getenv func(string) string) bool {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := getenv(k); v != "" {
			v = strings.ToLower(v)
			return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
		}
	}
	return false
}

// Symbol is the status glyph: ● ◆ ○ ✕ …, or * ! o x ~ in ASCII mode.
func (t Theme) Symbol(s agents.Status) string {
	uni, asc := "?", "?"
	switch s {
	case agents.Working:
		uni, asc = "●", "*"
	case agents.Waiting:
		uni, asc = "◆", "!"
	case agents.Idle:
		uni, asc = "○", "o"
	case agents.Exited:
		uni, asc = "✕", "x"
	case agents.Starting:
		uni, asc = "…", "~"
	}
	if t.ASCII {
		return asc
	}
	return uni
}

// BadgeWidth is the widest plain badge ("◆ starting").
const BadgeWidth = 10

// Badge pairs the symbol and the word with the status colour: never colour alone.
func (t Theme) Badge(s agents.Status) string {
	text := t.Symbol(s) + " " + string(s)
	if t.st != nil {
		if s == agents.Waiting {
			return t.st.waitingBold.S(text)
		}
		return t.st.statusPaint(s).S(text)
	}
	if !t.Color {
		return text
	}
	st := lipgloss.NewStyle().Foreground(statusColor(s))
	if s == agents.Waiting {
		st = st.Bold(true)
	}
	return st.Render(text)
}

// statusColor is the CLI's status colour, one of the 16 ANSI colours.
func statusColor(s agents.Status) color.Color {
	switch s {
	case agents.Working:
		return lipgloss.Green
	case agents.Waiting:
		return lipgloss.Yellow
	case agents.Idle:
		return lipgloss.Cyan
	case agents.Starting:
		return lipgloss.Blue
	default:
		return lipgloss.BrightBlack
	}
}

// Dim renders secondary text: faint in the CLI, Muted in the TUI.
func (t Theme) Dim(s string) string {
	if t.st != nil {
		return t.st.muted.S(s)
	}
	if !t.Color {
		return s
	}
	return lipgloss.NewStyle().Faint(true).Render(s)
}

// Bold renders emphasis. It is an attribute, not a colour, so it stays with --no-color.
func (t Theme) Bold(s string) string {
	if t.st != nil {
		return t.st.bold.S(s)
	}
	return lipgloss.NewStyle().Bold(true).Render(s)
}

// Error renders an error message.
func (t Theme) Error(s string) string {
	if t.st != nil {
		return t.st.danger.S(s)
	}
	if !t.Color {
		return s
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Red).Render(s)
}

// Ellipsis is "…", or "..." in ASCII mode.
func (t Theme) Ellipsis() string {
	if t.ASCII {
		return "..."
	}
	return "…"
}

// Dash stands in for a missing value: "—", or "-" in ASCII mode.
func (t Theme) Dash() string {
	if t.ASCII {
		return "-"
	}
	return "—"
}

// Truncate cuts s (which may hold ANSI styles) to w cells, ending with the ellipsis.
func (t Theme) Truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= w {
		return s
	}
	tail := t.Ellipsis()
	if ansi.StringWidth(tail) >= w {
		return ansi.Truncate(s, w, "")
	}
	return ansi.Truncate(s, w, tail)
}

// Pad truncates or pads s to exactly w cells.
func (t Theme) Pad(s string, w int) string {
	s = t.Truncate(s, w)
	if n := ansi.StringWidth(s); n < w {
		s += strings.Repeat(" ", w-n)
	}
	return s
}

// Age formats a duration compactly: 12s, 5m, 3h, 2d.
func Age(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// AgeSince is Age(now - t), or "" for a zero time.
func AgeSince(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return Age(now.Sub(t))
}

// ShortPath turns the home directory into "~".
func ShortPath(p, home string) string {
	if home == "" || home == "/" || p == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// AgentIDWidth fits the longest detector id ("opencode").
const AgentIDWidth = 8
