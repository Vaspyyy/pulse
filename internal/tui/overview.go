package tui

import (
	"fmt"
	"image/color"
	"strings"

	"github.com/Vaspyyy/pulse/internal/query"
)

// overview is the first tab: the machine's graphs above the heaviest
// groups.
func (m *model) overview(w, h int) []string {
	v, t := m.view, m.theme
	topH := min(max(h*2/5, 8), 14)
	midH := 7
	if h < 30 {
		midH = 6
	}
	cpuW := w * 3 / 5
	lines := beside(m.cpuPanel(cpuW, topH), m.memPanel(w-cpuW, topH))

	// GPU, network and disk share the middle row.
	var mid [][]string
	parts := 2
	if len(v.GPUs) > 0 {
		parts = 3
	}
	pw := w / parts
	if len(v.GPUs) > 0 {
		mid = append(mid, m.gpuPanel(w-2*pw, midH))
	}
	netW := pw
	if parts == 2 {
		netW = w - pw
	}
	mid = append(mid,
		m.ioPanel("Network", "↓", "↑", m.hist.netIn, m.hist.netOut, v.NetIn, v.NetOut, t.Net, netW, midH),
		m.ioPanel("Disk", "read", "write", m.hist.diskIn, m.hist.diskOut, v.DiskIn, v.DiskOut, t.Disk, pw, midH))
	lines = append(lines, beside(mid...)...)

	rest := h - len(lines)
	groups := shownGroups(m.groups)
	var rows []string
	cols := m.columns(w-4, trendW)
	rows = append(rows, m.tableHead(cols, w-4, "cpu", trendName(cols.last)))
	for i, g := range groups {
		if i >= rest-3 {
			break
		}
		rows = append(rows, m.groupRow(cols, g, w-4, false, false))
	}
	note := fg(t.Dim).Render(fmt.Sprintf("%d processes", len(v.Procs)))
	return append(lines, m.panel("Heaviest", note, w, rest, rows)...)
}

func shownGroups(groups []query.Group) []query.Group {
	var out []query.Group
	for _, g := range groups {
		if g.Total.Cores >= 0.05 || g.Total.Private >= 1<<20 || g.Total.GPU >= 1 || g.Total.VRAM >= 1<<20 {
			out = append(out, g)
		}
	}
	return out
}

func (m *model) cpuPanel(w, h int) []string {
	v, t := m.view, m.theme
	iw, ih := w-4, h-2
	note := fg(t.Text).Bold(true).Render(fmt.Sprintf("%.1f", v.Cores)) + fg(t.Sub).Render(fmt.Sprintf("/%d cores", v.NCPU))
	if v.Snap.CPUTemp > 0 {
		note += fg(t.Dim).Render(" · ") + fg(t.heat((v.Snap.CPUTemp-40)/50)).Render(fmt.Sprintf("%.0f°C", v.Snap.CPUTemp))
	}

	// Every CPU gets a meter, in as many columns as the height asks for,
	// to the right of the graph. Where that takes too much, a strip of
	// blocks under the graph does.
	const cellW = 15
	n := len(v.CoreBusy)
	cols := (n + ih - 1) / max(ih, 1)
	rows := (n + max(cols, 1) - 1) / max(cols, 1) // the columns are equally long
	if n > 0 && cols*(cellW+2) <= iw/2 {
		gw := iw - cols*(cellW+2)
		graph := m.area(m.hist.cpu, gw, ih, t.CPU)
		lines := make([]string, ih)
		for y := range ih {
			lines[y] = graph[y]
			for col := range cols {
				i := col*rows + y
				if y >= rows || i >= n {
					lines[y] += strings.Repeat(" ", cellW+2)
					continue
				}
				busy := min(max(v.CoreBusy[i], 0), 1)
				lines[y] += "  " + fg(t.Dim).Render(fmt.Sprintf("%2d ", i)) + meter(busy, cellW-8, t.heat(busy), t.Sel) +
					fg(t.Sub).Render(fmt.Sprintf(" %3.0f%%", busy*100))
			}
		}
		return m.panel("CPU", note, w, h, lines)
	}
	lines := m.area(m.hist.cpu, iw, ih-2, t.CPU)
	lines = append(lines, "")
	var b strings.Builder
	b.WriteString(fg(t.Dim).Render("cores "))
	for _, busy := range v.CoreBusy {
		level := int(min(max(busy, 0), 1)*8 + 0.5)
		if level == 0 {
			b.WriteString(fg(t.Sel).Render("▁"))
		} else {
			b.WriteString(fg(t.heat(busy)).Render(string(blocks[level])))
		}
	}
	lines = append(lines, b.String())
	return m.panel("CPU", note, w, h, lines)
}

func (m *model) memPanel(w, h int) []string {
	v, t := m.view, m.theme
	iw, ih := w-4, h-2
	mem := v.Snap.Mem
	used := mem.Total - min(mem.Available, mem.Total)
	note := fg(t.Text).Bold(true).Render(fmt.Sprintf("%.1f", gb(used))) + fg(t.Sub).Render(fmt.Sprintf("/%.1f GB", gb(mem.Total)))

	var foot []string
	bar := func(label string, part, whole uint64, c color.Color) {
		text := fmt.Sprintf("%5.1f GB", gb(part))
		mw := iw - len(label) - len(text) - 2
		frac := 0.0
		if whole > 0 {
			frac = float64(part) / float64(whole)
		}
		foot = append(foot, fg(t.Dim).Render(label)+" "+meter(frac, mw, c, t.Sel)+" "+fg(t.Sub).Render(text))
	}
	bar("used", used, mem.Total, t.Mem[len(t.Mem)-1])
	if mem.SwapTotal > 0 {
		bar("swap", mem.SwapTotal-min(mem.SwapFree, mem.SwapTotal), mem.SwapTotal, t.Accent2)
	}
	p := v.Snap.Pressure
	press := fg(t.Dim).Render("pressure ")
	for i, x := range []struct {
		name string
		v    float64
	}{{"cpu", p.CPU}, {"mem", p.Memory}, {"io", p.IO}} {
		if i > 0 {
			press += fg(t.Line).Render(" · ")
		}
		press += fg(t.Dim).Render(x.name+" ") + fg(t.heat(x.v/25)).Render(fmt.Sprintf("%.0f%%", x.v))
	}
	foot = append(foot, press)

	gh := ih - len(foot) - 1
	lines := m.area(m.hist.mem, iw, gh, t.Mem)
	lines = append(lines, "")
	return m.panel("Memory", note, w, h, append(lines, foot...))
}

func (m *model) gpuPanel(w, h int) []string {
	t := m.theme
	d := m.view.GPUs[0]
	iw, ih := w-4, h-2
	var parts []string
	if d.Power > 0 && d.PowerLimit > 0 {
		parts = append(parts, fg(t.Text).Bold(true).Render(fmt.Sprintf("%.0f", d.Power))+fg(t.Sub).Render(fmt.Sprintf("/%.0f W", d.PowerLimit)))
	} else if d.Util >= 0 {
		parts = append(parts, fg(t.Text).Bold(true).Render(fmt.Sprintf("%.0f%%", d.Util)))
	}
	if d.Temp > 0 {
		parts = append(parts, fg(t.heat((d.Temp-40)/45)).Render(fmt.Sprintf("%.0f°C", d.Temp)))
	}
	note := strings.Join(parts, fg(t.Dim).Render(" · "))

	gh := ih - 1
	lines := m.area(m.hist.gpu, iw, gh, t.GPU)
	text := fmt.Sprintf("%.1f/%.1f GB", gb(d.MemUsed), gb(d.MemTotal))
	frac := 0.0
	if d.MemTotal > 0 {
		frac = float64(d.MemUsed) / float64(d.MemTotal)
	}
	lines = append(lines, fg(t.Dim).Render("vram")+" "+meter(frac, iw-len(text)-6, t.GPU[0], t.Sel)+" "+fg(t.Sub).Render(text))
	return m.panel("GPU "+d.Name, note, w, h, lines)
}

// ioPanel draws two rates over time, one above the other, on one scale.
func (m *model) ioPanel(title, inName, outName string, in, out []float64, nowIn, nowOut float64, cs []color.Color, w, h int) []string {
	t := m.theme
	iw, ih := w-4, h-2
	note := fg(cs[0]).Render(inName+" ") + fg(t.Text).Render(rate(nowIn)) + "  " + fg(cs[len(cs)-1]).Render(outName+" ") + fg(t.Text).Render(rate(nowOut))
	// One scale for both, the first growing up from the middle and the
	// second down from it.
	top := max(ceiling(in, iw, 64<<10), ceiling(out, iw, 64<<10))
	upper := (ih + 1) / 2
	lines := m.area(scaled(in, top), iw, upper, cs[:1])
	lines = append(lines, m.areaDown(scaled(out, top), iw, ih-upper, cs[len(cs)-1:])...)
	return m.panel(title, note, w, h, lines)
}

func gb(b uint64) float64 { return float64(b) / (1 << 30) }
