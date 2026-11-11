package tui

import (
	"image/color"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// paint is how a piece of text looks. It does the part of lipgloss.Style
// that is used here at a fraction of the cost: a frame is a few thousand
// pieces, and Pulse may not be what loads the machine.
type paint struct {
	fg, bg      color.Color
	bold, faint bool
}

func fg(c color.Color) paint { return paint{fg: c} }

func (p paint) Foreground(c color.Color) paint { p.fg = c; return p }
func (p paint) Background(c color.Color) paint { p.bg = c; return p }
func (p paint) Bold(on bool) paint             { p.bold = on; return p }
func (p paint) Faint(on bool) paint            { p.faint = on; return p }

// sequences holds the escape sequence of every paint used so far.
var sequences sync.Map

// Render returns s in this paint. The colours go out as they are: Bubble
// Tea turns them into what the terminal can show.
func (p paint) Render(s string) string {
	if s == "" {
		return ""
	}
	seq, ok := sequences.Load(p)
	if !ok {
		var st ansi.Style
		if p.bold {
			st = st.Bold()
		}
		if p.faint {
			st = st.Faint()
		}
		if p.fg != nil {
			st = st.ForegroundColor(p.fg)
		}
		if p.bg != nil {
			st = st.BackgroundColor(p.bg)
		}
		seq = ""
		if len(st) > 0 {
			seq = st.String()
		}
		sequences.Store(p, seq)
	}
	if seq == "" {
		return s
	}
	return seq.(string) + s + ansi.ResetStyle
}

// width is the number of cells a plain string takes.
func width(s string) int { return lipgloss.Width(s) }

// clip cuts a styled line that is wider than w cells.
func clip(s string, w int) string {
	if width(s) <= w {
		return s
	}
	return ansi.Truncate(s, max(w, 0), "…")
}

// fill makes a styled line exactly w cells wide.
func fill(s string, w int) string {
	n := width(s)
	if n > w {
		return ansi.Truncate(s, max(w, 0), "…")
	}
	return s + strings.Repeat(" ", w-n)
}

// fit makes s exactly w cells wide: cut with an ellipsis, or padded. With
// right set, the padding goes in front.
func fit(s string, w int, right bool) string {
	if w <= 0 {
		return ""
	}
	if n := width(s); n > w {
		r := []rune(s)
		for len(r) > 0 && width(string(r))+1 > w {
			r = r[:len(r)-1]
		}
		s = string(r) + "…"
	}
	pad := strings.Repeat(" ", max(w-width(s), 0))
	if right {
		return pad + s
	}
	return s + pad
}

var blocks = []rune(" ▁▂▃▄▅▆▇█")

// area draws values from 0 to 1 as a filled graph of w by h cells, one
// value per column, newest on the right, eight levels to a cell. stops
// colour it from its floor to its ceiling. Columns without a value yet
// get a faint floor line, so an empty graph still has a shape.
func (m *model) area(vals []float64, w, h int, stops []color.Color) []string {
	if w <= 0 || h <= 0 {
		return nil
	}
	rows := ramp(stops, h)
	eighths := make([]int, w) // -1: no value yet
	for x := range eighths {
		j := len(vals) - w + x
		if j < 0 {
			eighths[x] = -1
			continue
		}
		v := min(max(vals[j], 0), 1)
		eighths[x] = int(v*float64(8*h) + 0.5)
		if v > 0.004 && eighths[x] == 0 {
			eighths[x] = 1 // something happened: show it
		}
	}
	out := make([]string, h)
	for y := range h { // 0 is the bottom row
		var b strings.Builder
		for x := 0; x < w; {
			// A run of cells in one colour.
			empty := eighths[x] <= 0 && y == 0
			end := x
			var run strings.Builder
			for end < w && (eighths[end] <= 0 && y == 0) == empty {
				switch level := min(max(eighths[end]-8*y, 0), 8); {
				case empty:
					run.WriteRune('▁')
				default:
					run.WriteRune(blocks[level])
				}
				end++
			}
			if empty {
				b.WriteString(fg(m.theme.Sel).Render(run.String()))
			} else {
				b.WriteString(fg(rows[y]).Render(run.String()))
			}
			x = end
		}
		out[h-1-y] = b.String()
	}
	return out
}

// areaDown is area hanging from the top: the mirror image, for the second
// of two graphs that share a middle line. Blocks that hang from the top
// of a cell come in three sizes only (an eighth, a half, all of it), so
// it is coarser than area.
func (m *model) areaDown(vals []float64, w, h int, stops []color.Color) []string {
	if w <= 0 || h <= 0 {
		return nil
	}
	rows := ramp(stops, h)
	out := make([]string, h)
	for y := range h { // 0 is the top row
		var b strings.Builder
		for x := range w {
			j := len(vals) - w + x
			eighths := 0
			if j >= 0 {
				v := min(max(vals[j], 0), 1)
				eighths = int(v*float64(8*h) + 0.5)
				if v > 0.004 && eighths == 0 {
					eighths = 1
				}
			}
			switch level := eighths - 8*y; {
			case level <= 0:
				b.WriteByte(' ')
			case level < 3:
				b.WriteRune('▔')
			case level < 7:
				b.WriteRune('▀')
			default:
				b.WriteRune('█')
			}
		}
		out[y] = fg(rows[y]).Render(b.String())
	}
	return out
}

// meter is a horizontal bar of w cells, filled to frac: a thick line in
// colour, the rest of it faint.
func meter(frac float64, w int, full, empty color.Color) string {
	frac = min(max(frac, 0), 1)
	n := int(frac*float64(w) + 0.5)
	if frac > 0 && n == 0 {
		n = 1
	}
	return fg(full).Render(strings.Repeat("━", n)) + fg(empty).Render(strings.Repeat("━", w-n))
}

// spark draws the last w values, each from 0 to 1, as a row of blocks.
func spark(vals []float64, w int, c color.Color) string {
	var b strings.Builder
	for i := len(vals) - w; i < len(vals); i++ {
		if i < 0 {
			b.WriteByte(' ')
			continue
		}
		v := min(max(vals[i], 0), 1)
		level := int(v*8 + 0.5)
		if v > 0.02 && level == 0 {
			level = 1
		}
		b.WriteRune(blocks[level])
	}
	return fg(c).Render(b.String())
}

// panel frames lines in a rounded border w cells wide and h tall, with a
// title set into the top edge and, when it fits, a note at its right end.
// Lines are cut or padded to the inside width; note may carry colour.
func (m *model) panel(title, note string, w, h int, lines []string) []string {
	return m.frame(m.theme.Line, title, note, w, h, lines)
}

// frame is panel with the border in a colour of its own, for dialogs.
func (m *model) frame(border color.Color, title, note string, w, h int, lines []string) []string {
	t := m.theme
	line := fg(border)
	inner := max(w-4, 0)
	head := line.Render("╭─ ") + fg(t.Accent).Bold(true).Render(title) + " "
	used := 4 + width(title)
	if note != "" && used+width(note)+4 <= w {
		gap := w - used - width(note) - 4
		head += line.Render(strings.Repeat("─", gap)+" ") + note + line.Render(" ─╮")
	} else {
		head += line.Render(strings.Repeat("─", max(w-used-1, 0)) + "╮")
	}
	out := []string{head}
	for i := range max(h-2, 0) {
		content := ""
		if i < len(lines) {
			content = lines[i]
		}
		// Content is styled, so it is cut and padded by its visible width.
		n := width(content)
		if n > inner {
			content = ansi.Truncate(content, inner, "…")
		}
		if n < inner {
			content += strings.Repeat(" ", inner-n)
		}
		out = append(out, line.Render("│ ")+content+line.Render(" │"))
	}
	return append(out, line.Render("╰"+strings.Repeat("─", max(w-2, 0))+"╯"))
}

// beside joins blocks of lines left to right. Each block's lines all have
// the same width; shorter blocks are padded with blank lines.
func beside(blocks ...[]string) []string {
	h := 0
	for _, b := range blocks {
		h = max(h, len(b))
	}
	out := make([]string, h)
	for _, b := range blocks {
		w := 0
		if len(b) > 0 {
			w = width(b[0])
		}
		for i := range h {
			if i < len(b) {
				out[i] += b[i]
			} else {
				out[i] += strings.Repeat(" ", w)
			}
		}
	}
	return out
}
