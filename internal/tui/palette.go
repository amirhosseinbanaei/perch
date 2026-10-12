package tui

import (
	"math"
	"strconv"
	"strings"
)

// Palette is one theme variant as named roles. It is the only place in perch that holds hex
// colours: every TUI style is built from these roles (styles.go). The CLI tables do not use it;
// they keep the 16 ANSI colours (Theme.statusColor).
type Palette struct {
	Accent     string // focused borders and titles, the selection bar, the logo
	AccentSoft string // the gradient's far end
	Text       string // body text
	Muted      string // labels, ages, hints
	Faint      string // unfocused borders
	Surface    string // modal and selected-row background, used only when the background is known
	Working    string
	Waiting    string
	Idle       string
	Exited     string
	Danger     string // errors, the kill button
	Success    string
	// Agent colours, by detector id.
	Claude, Codex, Gemini, Hermes, OpenCode string
}

// ContrastBackgrounds are the typical dark and light backgrounds the palettes are tuned against.
const (
	DarkBackground  = "#1E1E1E"
	LightBackground = "#FFFFFF"
)

// themePalette returns a theme's dark or light variant. An unknown name is coral.
//
// Status and agent colours are shared by the three themes, so a status reads the same in each.
// Every value is checked by TestContrast: text roles at 4.5:1 or more, and borders, badges and
// agent names at 3:1 or more, against DarkBackground or LightBackground.
func themePalette(name string, dark bool) Palette {
	p := Palette{
		Working: "#1A7F37", Waiting: "#9A6700", Idle: "#6E7781", Exited: "#CF222E",
		Danger: "#CF222E", Success: "#1A7F37",
		Claude: "#B85A3A", Codex: "#0B66C3", Gemini: "#6E56CF", Hermes: "#B4366A", OpenCode: "#0B7A87",
	}
	if dark {
		p = Palette{
			Working: "#4EC27A", Waiting: "#E5B84B", Idle: "#A39E99", Exited: "#FF7B72",
			Danger: "#FF7B72", Success: "#4EC27A",
			Claude: "#E0896A", Codex: "#6CB6FF", Gemini: "#A9A1FF", Hermes: "#F28FAD", OpenCode: "#56C8D8",
		}
	}
	type roles struct{ accent, soft, text, muted, faint, surface string }
	var r roles
	switch name {
	case "teal": // slate greys
		r = roles{"#0F8B8D", "#2B6CB0", "#1E293B", "#526173", "#7F8FA3", "#EEF3F6"}
		if dark {
			r = roles{"#2EC4B6", "#7BDFF2", "#E2E8F0", "#94A3B8", "#64748B", "#243040"}
		}
	case "violet": // neutral greys, the pink gradient end
		r = roles{"#7C3AED", "#DB2777", "#18181B", "#52525B", "#8B8B94", "#F4F0FF"}
		if dark {
			r = roles{"#A78BFA", "#F472B6", "#E4E4E7", "#A1A1AA", "#71717A", "#2A2633"}
		}
	default: // coral, warm greys
		r = roles{"#C15F3C", "#C9772E", "#2B2622", "#6B635C", "#8E8780", "#F3EFEA"}
		if dark {
			r = roles{"#D97757", "#F4B183", "#E8E4DF", "#9C958E", "#736C66", "#2D2A27"}
		}
	}
	p.Accent, p.AccentSoft, p.Text, p.Muted, p.Faint, p.Surface = r.accent, r.soft, r.text, r.muted, r.faint, r.surface
	return p
}

// agentColor is the agent's role colour, or Text for an agent without one.
func (p Palette) agentColor(id string) string {
	switch id {
	case "claude":
		return p.Claude
	case "codex":
		return p.Codex
	case "gemini":
		return p.Gemini
	case "hermes":
		return p.Hermes
	case "opencode":
		return p.OpenCode
	}
	return p.Text
}

// relativeLuminance is WCAG 2's relative luminance of a "#RRGGBB" colour.
func relativeLuminance(hex string) float64 {
	v, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil {
		return 0
	}
	lin := func(c uint64) float64 {
		s := float64(c) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(v>>16&0xff) + 0.7152*lin(v>>8&0xff) + 0.0722*lin(v&0xff)
}

// contrastRatio is WCAG 2's contrast ratio between two "#RRGGBB" colours, from 1 to 21.
func contrastRatio(a, b string) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}
