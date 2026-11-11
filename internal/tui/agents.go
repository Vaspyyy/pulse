package tui

import (
	"cmp"
	"fmt"
	"image/color"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/Vaspyyy/pulse/collect/t3"
	"github.com/Vaspyyy/pulse/internal/query"
)

type agentsState struct {
	titles   map[string]t3.Thread // "" title: asked, no answer yet
	fetching bool
	cursor   string // the selected scope's unit
	index    int
}

// agentLine is one agent scope with what its processes use.
type agentLine struct {
	scope *t3.Scope
	total query.Total
}

func (m *model) agentLines() []agentLine {
	v := m.view
	if v.T3 == nil {
		return nil
	}
	lines := make([]agentLine, len(v.T3.Scopes))
	for i := range v.T3.Scopes {
		lines[i].scope = &v.T3.Scopes[i]
	}
	for i := range v.Procs {
		if p := &v.Procs[i]; p.Scope >= 0 {
			t := &lines[p.Scope].total
			t.Count++
			t.Cores += p.Cores
			t.Private += p.Private()
			t.GPU += p.GPU
			t.VRAM += p.VRAM
		}
	}
	// By name, not by load: lines that swap places every second can't be read.
	slices.SortStableFunc(lines, func(a, b agentLine) int {
		return cmp.Or(cmp.Compare(m.agentTitle(a.scope), m.agentTitle(b.scope)), cmp.Compare(a.scope.Unit, b.scope.Unit))
	})
	return lines
}

func (m *model) agentTitle(s *t3.Scope) string {
	switch th := m.agents.titles[s.ThreadID]; {
	case s.ThreadID == "":
		return "shared server"
	case th.Title != "":
		return th.Title
	}
	return "thread " + s.ThreadID[:min(len(s.ThreadID), 8)]
}

func (s *agentsState) move(m *model, by int) {
	lines := m.agentLines()
	if len(lines) == 0 {
		return
	}
	s.index = min(max(s.index+by, 0), len(lines)-1)
	s.cursor = lines[s.index].scope.Unit
}

// project names what a directory belongs to: its name, or the project's
// for a T3 worktree (…/worktrees/<project>/<worktree>).
func project(dir string) string {
	parts := strings.Split(strings.Trim(dir, "/"), "/")
	if n := len(parts); n >= 3 && parts[n-3] == "worktrees" {
		return parts[n-2]
	}
	return parts[len(parts)-1]
}

// agentsTab is the third tab: T3's agents, how close they are to their
// memory limit, and what the selected one runs.
func (m *model) agentsTab(w, h, y0 int) []string {
	t, v := m.theme, m.view
	if v.T3 == nil {
		msg := []string{"", fg(t.Sub).Render("T3 Code runs no agents on this machine right now."),
			fg(t.Dim).Render("When it does, each agent's CPU, memory and limits show here.")}
		return m.panel("Agents", "", w, h, msg)
	}
	lines := m.agentLines()
	s := &m.agents
	for i, l := range lines {
		if l.scope.Unit == s.cursor {
			s.index = i
		}
	}
	s.index = min(max(s.index, 0), max(len(lines)-1, 0))
	if len(lines) > 0 {
		s.cursor = lines[s.index].scope.Unit
	}
	iw := w - 4
	lim := v.T3.Limits

	// The limit: one bar from nothing to where a process gets killed, with
	// a mark where throttling starts.
	var top []string
	if lim.MemoryHigh > 0 && lim.MemoryMax > 0 {
		barW := iw
		used := int(float64(v.T3.Memory) / float64(lim.MemoryMax) * float64(barW))
		high := min(int(float64(lim.MemoryHigh)/float64(lim.MemoryMax)*float64(barW)), barW-1)
		share := float64(v.T3.Memory) / float64(lim.MemoryHigh)
		var b strings.Builder
		for i := range barW {
			switch {
			case i == high:
				b.WriteString(fg(t.Warn).Render("┃"))
			case i < used:
				b.WriteString(fg(t.heat(share)).Render("━"))
			case i > high:
				b.WriteString(fg(t.Bad).Faint(true).Render("━"))
			default:
				b.WriteString(fg(t.Line).Render("━"))
			}
		}
		left := fg(t.Text).Bold(true).Render(query.Bytes(v.T3.Memory)) + fg(t.Sub).Render(" used by all agents")
		right := fg(t.Warn).Render("┃ ") + fg(t.Sub).Render("slowed down at "+query.Bytes(lim.MemoryHigh)) +
			fg(t.Dim).Render("  ·  ") + fg(t.Sub).Render("one is killed at "+query.Bytes(lim.MemoryMax))
		if width(left)+width(right)+2 > iw {
			right = fg(t.Sub).Render("limit " + query.Bytes(lim.MemoryHigh) + ", " + query.Bytes(lim.MemoryMax))
		}
		top = []string{left + strings.Repeat(" ", max(iw-width(left)-width(right), 1)) + right, b.String()}
	} else {
		top = []string{fg(t.Text).Bold(true).Render(query.Bytes(v.T3.Memory)) + fg(t.Sub).Render(" used by all agents; T3 set no memory limit")}
	}
	note := fg(t.Dim).Render(fmt.Sprintf("cpu weight %d, io weight %d of 100: they yield to everything else", lim.CPUWeight, lim.IOWeight))
	out := m.panel("Memory limit", note, w, len(top)+2, top)

	// The table.
	tableH := min(max(len(lines)+3, 6), (h-len(out))/2)
	// The model and the project are the first to go on a narrow terminal.
	const provW = 9
	modelW, projW := 14, 16
	if iw < 100 {
		projW = 0
	}
	if iw < 82 {
		modelW = 0
	}
	opt := func(s string, w int, c color.Color) seg {
		if w == 0 {
			return seg{}
		}
		return seg{s: "  " + fit(s, w, false), c: c}
	}
	titleW := iw - provW - 2 - 2 - cpuW - 2 - memW
	for _, cw := range []int{modelW, projW} {
		if cw > 0 {
			titleW -= cw + 2
		}
	}
	titleW = max(titleW, 12)
	rows := []string{m.row([]seg{
		{s: fit("AGENT", provW, false), c: t.Dim}, {s: "  " + fit("THREAD", titleW, false), c: t.Dim},
		opt("MODEL", modelW, t.Dim), {s: "  " + fit("CPU", cpuW, true), c: t.Dim},
		{s: "  " + fit("MEMORY", memW, true), c: t.Dim}, opt("PROJECT", projW, t.Dim)}, iw, false)}
	for i, l := range lines {
		if i >= tableH-3 {
			break
		}
		sc := l.scope
		sel := i == s.index
		share := l.total.Cores / float64(v.NCPU)
		mem := sc.Memory
		if mem == 0 {
			mem = l.total.Private
		}
		memShare := 0.0
		if lim.MemoryHigh > 0 {
			memShare = float64(mem) / float64(lim.MemoryHigh)
		}
		model := strings.TrimPrefix(m.agents.titles[sc.ThreadID].Model, "claude-")
		proj := ""
		if dir, err := fs.ReadLink(m.o.Roots.Proc, path.Join(strconv.Itoa(sc.Agent), "cwd")); err == nil && sc.Agent != 0 {
			proj = project(dir)
		}
		titleColor := t.Text
		if sc.ThreadID == "" {
			titleColor = t.Dim
		}
		// One that lost a process to the memory limit says so in its title.
		segs := []seg{{s: fit(sc.Name, provW, false), c: t.Accent, bold: true}}
		if sc.OOMKills > 0 {
			killed := fmt.Sprintf("%d killed for memory", sc.OOMKills)
			segs = append(segs, seg{s: "  " + fit(m.agentTitle(sc), titleW-len(killed)-2, false), c: titleColor}, seg{s: "  " + killed, c: t.Bad})
		} else {
			segs = append(segs, seg{s: "  " + fit(m.agentTitle(sc), titleW, false), c: titleColor})
		}
		segs = append(segs, opt(model, modelW, t.Sub), seg{s: "  "})
		track := t.Sel
		if sel {
			track = t.Line
		}
		segs = append(segs, meterSegs(share, meterW, t.heat(share*1.5), track)...)
		segs = append(segs, seg{s: fmt.Sprintf(" %5.1f", l.total.Cores), c: t.Text}, seg{s: "  "})
		segs = append(segs, meterSegs(memShare, meterW, t.heat(memShare), track)...)
		segs = append(segs, seg{s: " " + fit(query.Bytes(mem), 7, true), c: t.Sub}, opt(proj, projW, t.Dim))
		rows = append(rows, m.row(segs, iw, sel))
	}
	m.zones = append(m.zones, zone{x: 2, y: y0 + len(out) + 2, w: iw, h: len(lines), do: func(row int) {
		if row < len(lines) {
			s.index, s.cursor = row, lines[row].scope.Unit
		}
	}})
	out = append(out, m.panel("Agents", fg(t.Dim).Render(fmt.Sprintf("%d running", len(lines))), w, tableH, rows)...)

	// What the selected agent runs, and beside it how it went lately.
	rest := h - len(out)
	graphW := 0
	if w >= 110 {
		graphW = min(max(w*36/100, 40), 60)
	}
	iw = w - graphW - 4
	var tree, graphs []string
	title := "Runs"
	if len(lines) > 0 {
		sc := lines[s.index].scope
		title = "What " + sc.Name + " runs"
		if a := m.hist.agents[sc.Unit]; a != nil && graphW > 0 {
			gw, gh := graphW-4, max((rest-2-3)/2, 2)
			topCores := ceiling(a.cores, gw, 1)
			topMem := ceiling(a.mem, gw, 256<<20)
			now := func(vals []float64) float64 {
				if len(vals) == 0 {
					return 0
				}
				return vals[len(vals)-1]
			}
			graphs = append(graphs, fg(t.Dim).Render("cpu ")+fg(t.Text).Bold(true).Render(fmt.Sprintf("%.1f cores", now(a.cores)))+
				fg(t.Dim).Render(fmt.Sprintf("  ·  graph to %.0f", topCores)))
			graphs = append(graphs, m.area(scaled(a.cores, topCores), gw, gh, t.CPU)...)
			graphs = append(graphs, "", fg(t.Dim).Render("memory ")+fg(t.Text).Bold(true).Render(query.Bytes(uint64(now(a.mem))))+
				fg(t.Dim).Render("  ·  graph to "+query.Bytes(uint64(topMem))))
			graphs = append(graphs, m.area(scaled(a.mem, topMem), gw, gh, t.Mem)...)
		}
		if p := v.Proc(sc.Agent); p != nil {
			c := m.columns(iw, handleW)
			var walk func(n *query.Node, prefix string)
			walk = func(n *query.Node, prefix string) {
				for i, ch := range n.Children {
					branch, below := "├─ ", "│  "
					if i == len(n.Children)-1 {
						branch, below = "└─ ", "   "
					}
					label, mem, handle := ch.Label, ch.Procs[0].RSS, v.Handle(ch.Procs[0])
					if len(ch.Procs) > 1 {
						label, mem, handle = fmt.Sprintf("%s ×%d", ch.Label, len(ch.Procs)), ch.Own.Private, ""
					}
					if len(tree) < rest-2 {
						tree = append(tree, m.usageRow(c, []seg{{s: prefix + branch, c: t.Line}, {s: label, c: t.Text}},
							ch.Own.Cores, mem, ch.Own.GPU, ch.Own.VRAM, []seg{{s: fit(handle, c.last, true), c: t.Dim}}, iw, false))
					}
					walk(ch, prefix+below)
				}
			}
			n := v.Tree(p)
			tree = append(tree, m.usageRow(c, []seg{{s: n.Label, c: t.Text, bold: true}}, p.Cores, p.RSS, p.GPU, p.VRAM,
				[]seg{{s: fit(v.Handle(p), c.last, true), c: t.Dim}}, iw, false))
			walk(n, "")
		}
	}
	if graphW == 0 {
		return append(out, m.panel(title, "", w, rest, tree)...)
	}
	return append(out, beside(m.panel(title, "", w-graphW, rest, tree), m.panel("Lately", fg(t.Dim).Render("a column a second"), graphW, rest, graphs))...)
}
