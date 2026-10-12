package tui

// The empty-state logo: a bird on a branch, at most 5 lines and 24 columns, drawn in the
// Accent → AccentSoft gradient. Two candidates were drawn (docs/screenshots/logo-candidates.png):
// A from block elements and B from rounded box-drawing lines. A was kept; B read as a box, not a
// bird, at this size.

// logoBlocks is the logo, from block elements.
func logoBlocks() []string {
	return []string{
		"      ▄▀▀▀▄",
		"     █  ● ▀▶",
		"  ◀▄▄█    █",
		"      ▀▄▄▀",
		"  ━━━━━┻┻━━━━━━",
	}
}

// logoASCII is the ASCII form.
func logoASCII() []string {
	return []string{
		"       __",
		"     <(o )___",
		"      ( ._> /",
		"       `---'",
		"  ======\"\"=======",
	}
}

func logoArt(ascii bool) []string {
	if ascii {
		return logoASCII()
	}
	return logoBlocks()
}
