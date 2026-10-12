package cli

import (
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/amirhosseinbanaei/tmux-manager-cli/internal/tui"
)

// table is the one renderer every CLI table uses. Cells may hold styles (badges); widths are
// measured in terminal cells. When the table is wider than the terminal, the flex columns
// (TITLE, PATH, AGENTS) are cut with an ellipsis, widest first, down to a floor.
type table struct {
	headers []string
	flex    map[int]bool
	rows    [][]string
}

const (
	colGap   = 2
	flexMinW = 8
)

func newTable(headers []string, flex ...int) *table {
	t := &table{headers: headers, flex: map[int]bool{}}
	for _, f := range flex {
		t.flex[f] = true
	}
	return t
}

func (t *table) add(cells ...string) { t.rows = append(t.rows, cells) }

// widths fits the columns to width (0: no limit).
func (t *table) widths(width int) []int {
	ws := make([]int, len(t.headers))
	for i, h := range t.headers {
		ws[i] = ansi.StringWidth(h)
	}
	for _, r := range t.rows {
		for i := range ws {
			if i < len(r) {
				ws[i] = max(ws[i], ansi.StringWidth(r[i]))
			}
		}
	}
	if width <= 0 {
		return ws
	}
	total := func() int {
		n := colGap * (len(ws) - 1)
		for _, w := range ws {
			n += w
		}
		return n
	}
	for total() > width {
		widest := -1
		for i, w := range ws {
			if t.flex[i] && w > max(flexMinW, ansi.StringWidth(t.headers[i])) && (widest < 0 || w > ws[widest]) {
				widest = i
			}
		}
		if widest < 0 {
			break // only fixed columns are left: let the terminal wrap
		}
		ws[widest]--
	}
	return ws
}

func (t *table) render(w io.Writer, th tui.Theme, width int) error {
	ws := t.widths(width)
	line := func(cells []string, style func(string) string) string {
		var b strings.Builder
		for i, wd := range ws {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			if i == len(ws)-1 {
				b.WriteString(style(th.Truncate(c, wd)))
			} else {
				b.WriteString(style(th.Pad(c, wd)))
				b.WriteString(strings.Repeat(" ", colGap))
			}
		}
		return strings.TrimRight(b.String(), " ") + "\n"
	}
	head := func(s string) string { return s }
	if th.Color {
		head = th.Bold
	}
	out := line(t.headers, head)
	for _, r := range t.rows {
		out += line(r, func(s string) string { return s })
	}
	_, err := io.WriteString(w, out)
	return err
}
