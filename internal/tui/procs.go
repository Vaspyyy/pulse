package tui

import (
	"cmp"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Vaspyyy/pulse/internal/query"
)

// columns are the widths of a usage table.
type columns struct {
	name  int
	gpu   bool // GPU share and VRAM columns
	last  int  // the trend or handle column, 0 for none
	meter int  // width of the CPU and memory meters
}

const (
	meterW   = 8
	cpuW     = meterW + 1 + 5
	memW     = meterW + 1 + 7
	gpuW     = 4
	vramW    = 7
	trendW   = 30 // half a minute of a group's CPU use
	handleW  = 12
	treeStep = 3 // cells a level of the tree is indented by
)

// columns lays a usage table out in w cells. last is the width of the
// column after the numbers, 0 for none; it is dropped where the name
// would get too little room.
func (m *model) columns(w, last int) columns {
	c := columns{gpu: len(m.view.GPUs) > 0, meter: meterW}
	fixed := 2 + cpuW + 2 + memW
	if c.gpu {
		fixed += 2 + gpuW + 2 + vramW
	}
	// The widest of the wanted width and a handle's that leaves the name
	// its room.
	for _, lw := range []int{last, handleW} {
		if lw > 0 && lw <= last && w-fixed-2-lw >= 26 {
			c.last = lw
			fixed += 2 + lw
			break
		}
	}
	c.name = max(w-fixed, 8)
	// On a wide table the meters take what the names don't need.
	if extra := min((c.name-40)/2, meterW); extra > 0 {
		c.meter += extra
		c.name -= 2 * extra
	}
	return c
}

// trendName heads the trend column, which shows a second a cell.
func trendName(w int) string {
	if w >= trendW {
		return "LAST HALF MINUTE"
	}
	return fmt.Sprintf("LAST %d S", w)
}

// tableHead names the columns; the one sorted by is marked.
func (m *model) tableHead(c columns, w int, sortBy, last string) string {
	t := m.theme
	col := func(name, key string, cw int) seg {
		if key != "" && key == sortBy {
			return seg{s: fit(name+" ▾", cw, true), c: t.Accent, bold: true}
		}
		return seg{s: fit(name, cw, true), c: t.Dim}
	}
	grow := c.meter - meterW
	segs := []seg{{s: fit("NAME", c.name, false), c: t.Dim}, {s: "  "}, col("CPU", "cpu", cpuW+grow), {s: "  "}, col("MEMORY", "mem", memW+grow)}
	if c.gpu {
		segs = append(segs, seg{s: "  "}, col("GPU", "gpu", gpuW), seg{s: "  "}, col("VRAM", "", vramW))
	}
	if c.last > 0 {
		segs = append(segs, seg{s: "  " + fit(last, c.last, true), c: t.Dim})
	}
	return m.row(segs, w, false)
}

// usageRow is a table line: a name, then CPU, memory and GPU use, then
// whatever goes last.
func (m *model) usageRow(c columns, name []seg, cores float64, mem uint64, gpuPct float64, vram uint64, last []seg, w int, selected bool) string {
	t := m.theme
	used := 0
	for _, s := range name {
		used += width(s.s)
	}
	segs := append([]seg{}, name...)
	// Deep in a tree the lines in front would push the name out of its
	// column. They lose whole levels from the left, so that what is left
	// still lines up, and say so with a "…".
	if room := max(c.name-12, 2*treeStep); len(segs) > 1 {
		if lead := []rune(segs[0].s); len(lead) > room {
			cut := (len(lead) - room + treeStep - 1) / treeStep * treeStep
			lead = lead[cut:]
			lead[0] = '…'
			segs[0].s = string(lead)
			used -= cut
		}
	}
	if used > c.name { // the name is always the last of its segments
		n := len(segs) - 1
		segs[n].s = fit(segs[n].s, max(c.name-(used-width(segs[n].s)), 1), false)
	} else {
		segs = append(segs, seg{s: strings.Repeat(" ", c.name-used)})
	}
	share := cores / float64(m.view.NCPU)
	segs = append(segs, seg{s: "  "})
	// On the selected line the track would vanish into the selection.
	track := t.Sel
	if selected {
		track = t.Line
	}
	segs = append(segs, meterSegs(share, c.meter, t.heat(share*1.5), track)...)
	cpuText := seg{s: fmt.Sprintf(" %5.1f", cores), c: t.Text}
	if cores < 0.05 {
		cpuText.c = t.Dim
	}
	segs = append(segs, cpuText, seg{s: "  "})
	memShare := 0.0
	if total := m.view.Snap.Mem.Total; total > 0 {
		memShare = float64(mem) / float64(total)
	}
	segs = append(segs, meterSegs(memShare*2, c.meter, t.Mem[len(t.Mem)-1], track)...)
	segs = append(segs, seg{s: " " + fit(query.Bytes(mem), 7, true), c: t.Sub})
	if c.gpu {
		g, v := "", ""
		if gpuPct >= 1 {
			g = fmt.Sprintf("%.0f%%", gpuPct)
		}
		if vram >= 1<<20 {
			v = query.Bytes(vram)
		}
		segs = append(segs, seg{s: "  " + fit(g, gpuW, true), c: t.GPU[len(t.GPU)-1]}, seg{s: "  " + fit(v, vramW, true), c: t.Dim})
	}
	if c.last > 0 {
		segs = append(segs, seg{s: "  "})
		segs = append(segs, last...)
	}
	return m.row(segs, w, selected)
}

// groupColor tells kinds of groups apart.
func (m *model) groupColor(kind string) (mark string, col seg) {
	t := m.theme
	switch kind {
	case "game":
		return "●", seg{c: t.Accent2, bold: true}
	case "agents":
		return "◆", seg{c: t.Accent, bold: true}
	case "kernel":
		return "·", seg{c: t.Dim}
	}
	return "", seg{c: t.Text}
}

func (m *model) groupRow(c columns, g query.Group, w int, selected, expanded bool) string {
	t := m.theme
	_, style := m.groupColor(g.Kind)
	style.s = g.Label
	name := []seg{style}
	if m.tab == tabProcs {
		arrow := seg{s: "▸ ", c: t.Dim}
		if expanded {
			arrow.s = "▾ "
		}
		if selected {
			arrow.c = t.Accent
		}
		name = []seg{arrow, style}
	}
	var last []seg
	if c.last > 0 {
		h := m.hist.groups[g.Kind+"/"+g.Name]
		top := 1.0
		for _, x := range h[max(len(h)-c.last, 0):] {
			top = max(top, x)
		}
		vals := scaled(h, top)
		var b strings.Builder
		for i := len(vals) - c.last; i < len(vals); i++ {
			if i < 0 {
				b.WriteByte(' ')
				continue
			}
			level := int(min(vals[i], 1)*8 + 0.5)
			if h[i] >= 0.05 && level == 0 {
				level = 1
			}
			b.WriteRune(blocks[level])
		}
		last = []seg{{s: b.String(), c: t.CPU[0]}}
	}
	return m.usageRow(c, name, g.Total.Cores, g.Total.Private, g.Total.GPU, g.Total.VRAM, last, w, selected)
}

const (
	modeGroups = iota
	modeList
	modeTree
)

var modeNames = []string{"groups", "processes", "tree"}

// prow is a line of the Processes table.
type prow struct {
	key    string // what the cursor holds on to between readings
	prefix string // indentation or tree lines
	label  string
	group  *query.Group
	proc   *query.Proc // nil for a group; the first one for merged leaves
	count  int         // processes on this line
	total  query.Total
	mem    uint64 // resident for one process, private for several
}

type procsState struct {
	mode      int
	sort      string
	filter    string
	filtering bool
	expanded  map[string]bool
	rows      []prow
	cursor    string
	index     int
	top       int
}

func compareTotals(by string) func(a, b query.Total) int {
	return func(a, b query.Total) int {
		switch by {
		case "mem":
			return cmp.Compare(b.Private, a.Private)
		case "gpu":
			if c := cmp.Or(cmp.Compare(int(b.GPU), int(a.GPU)), cmp.Compare(b.VRAM, a.VRAM)); c != 0 {
				return c
			}
		}
		return cmp.Or(cmp.Compare(int(b.Cores*20), int(a.Cores*20)), cmp.Compare(b.Private, a.Private))
	}
}

func procTotal(p *query.Proc) query.Total {
	return query.Total{Count: 1, Cores: p.Cores, Private: p.Private(), GPU: p.GPU, VRAM: p.VRAM}
}

// rebuild makes the table's lines from the newest reading.
func (s *procsState) rebuild(m *model) {
	v := m.view
	if s.sort == "" {
		s.sort = "cpu"
	}
	needle := strings.ToLower(s.filter)
	match := func(text string) bool { return needle == "" || strings.Contains(strings.ToLower(text), needle) }
	byUse := compareTotals(s.sort)
	procRow := func(prefix string, p *query.Proc) prow {
		return prow{key: "p:" + v.Handle(p), prefix: prefix, label: query.Label(p), proc: p, count: 1, total: procTotal(p), mem: p.RSS}
	}
	s.rows = s.rows[:0]
	switch s.mode {
	case modeGroups:
		groups := m.groups
		if s.sort != "cpu" {
			groups = v.Groups(s.sort)
		}
		for i := range groups {
			g := &groups[i]
			if !match(g.Label) || !(g.Total.Cores >= 0.05 || g.Total.Private >= 1<<20 || g.Total.VRAM >= 1<<20) {
				continue
			}
			key := "g:" + g.Kind + "/" + g.Name
			s.rows = append(s.rows, prow{key: key, label: g.Label, group: g, count: g.Total.Count, total: g.Total, mem: g.Total.Private})
			if !s.expanded[key] {
				continue
			}
			members := slices.Clone(g.Members)
			slices.SortStableFunc(members, func(a, b *query.Proc) int { return byUse(procTotal(a), procTotal(b)) })
			for _, p := range members {
				s.rows = append(s.rows, procRow("    ", p))
			}
		}
	case modeList:
		var procs []*query.Proc
		for i := range v.Procs {
			if p := &v.Procs[i]; !p.Kernel && (match(query.Label(p)) || match(v.Handle(p))) {
				procs = append(procs, p)
			}
		}
		slices.SortStableFunc(procs, func(a, b *query.Proc) int { return byUse(procTotal(a), procTotal(b)) })
		for _, p := range procs {
			s.rows = append(s.rows, procRow("", p))
		}
	case modeTree:
		root := v.Proc(1)
		if root == nil {
			break
		}
		var walk func(n *query.Node, prefix, branch string)
		walk = func(n *query.Node, prefix, branch string) {
			p := n.Procs[0]
			if match(n.Label) {
				r := procRow(prefix+branch, p)
				r.label = n.Label
				if len(n.Procs) > 1 {
					r.label += fmt.Sprintf(" ×%d", len(n.Procs))
					r.count, r.total, r.mem = len(n.Procs), n.Own, n.Own.Private
				}
				if needle != "" {
					r.prefix = ""
				}
				s.rows = append(s.rows, r)
			}
			below := prefix
			switch branch {
			case "├─ ":
				below += "│  "
			case "└─ ":
				below += "   "
			}
			for i, c := range n.Children {
				b := "├─ "
				if i == len(n.Children)-1 {
					b = "└─ "
				}
				walk(c, below, b)
			}
		}
		walk(v.Tree(root), "", "")
	}
	// The cursor stays on what it was on; if that is gone, it stays put.
	for i, r := range s.rows {
		if r.key == s.cursor {
			s.index = i
			break
		}
	}
	s.index = min(max(s.index, 0), max(len(s.rows)-1, 0))
	if len(s.rows) > 0 {
		s.cursor = s.rows[s.index].key
	}
}

func (s *procsState) move(by int) {
	if len(s.rows) == 0 {
		return
	}
	s.index = min(max(s.index+by, 0), len(s.rows)-1)
	s.cursor = s.rows[s.index].key
}

func (s *procsState) selected() *prow {
	if s.index < 0 || s.index >= len(s.rows) {
		return nil
	}
	return &s.rows[s.index]
}

func (s *procsState) key(m *model, k string) {
	switch k {
	case "/":
		s.filtering = true
	case "esc":
		s.filter = ""
	case "s":
		order := []string{"cpu", "mem"}
		if len(m.view.GPUs) > 0 {
			order = append(order, "gpu")
		}
		s.sort = order[(slices.Index(order, s.sort)+1)%len(order)]
	case "v":
		s.mode = (s.mode + 1) % len(modeNames)
		s.index, s.top = 0, 0
	case "enter":
		if r := s.selected(); r != nil && r.group != nil {
			s.expanded[r.key] = !s.expanded[r.key]
		}
	case "x":
		m.askKill()
		return
	default:
		return
	}
	s.rebuild(m)
}

// filterKey edits the filter while it is being typed.
func (s *procsState) filterKey(m *model, k tea.KeyPressMsg) {
	switch key := k.String(); key {
	case "enter":
		s.filtering = false
	case "esc":
		s.filtering, s.filter = false, ""
	case "backspace":
		if r := []rune(s.filter); len(r) > 0 {
			s.filter = string(r[:len(r)-1])
		}
	default:
		if k.Text != "" {
			s.filter += k.Text
		}
	}
	s.index, s.top = 0, 0
	s.cursor = ""
	s.rebuild(m)
}

// procsTab is the second tab: the table, and beside it what is known
// about the selected line.
func (m *model) procsTab(w, h, y0 int) []string {
	t, s := m.theme, &m.procs
	detailW := 0
	if w >= 104 {
		detailW = min(max(w*34/100, 38), 56)
	}
	tw := w - detailW
	iw := tw - 4
	c := m.columns(iw, handleW)
	if s.mode == modeGroups {
		c = m.columns(iw, trendW)
	}
	visible := h - 3
	if s.index < s.top {
		s.top = s.index
	}
	if s.index >= s.top+visible {
		s.top = s.index - visible + 1
	}
	s.top = min(max(s.top, 0), max(len(s.rows)-visible, 0))

	lastName := "HANDLE"
	if s.mode == modeGroups {
		lastName = trendName(c.last)
	}
	lines := []string{m.tableHead(c, iw, s.sort, lastName)}
	m.zones = append(m.zones, zone{x: 2, y: y0 + 1, w: iw, h: 1, do: func(int) { s.key(m, "s") }})
	for i := s.top; i < min(s.top+visible, len(s.rows)); i++ {
		r := &s.rows[i]
		sel := i == s.index
		if r.group != nil {
			lines = append(lines, m.groupRow(c, *r.group, iw, sel, s.expanded[r.key]))
			continue
		}
		name := []seg{{s: r.prefix, c: t.Line}, {s: r.label, c: t.Text, bold: sel}}
		var last []seg
		if c.last > 0 {
			handle := m.view.Handle(r.proc)
			if r.count > 1 {
				handle = ""
			}
			last = []seg{{s: fit(handle, c.last, true), c: t.Dim}}
		}
		lines = append(lines, m.usageRow(c, name, r.total.Cores, r.mem, r.total.GPU, r.total.VRAM, last, iw, sel))
	}
	top := s.top
	m.zones = append(m.zones, zone{x: 2, y: y0 + 2, w: iw, h: visible, do: func(row int) {
		if i := top + row; i < len(s.rows) {
			s.index, s.cursor = i, s.rows[i].key
		}
	}})

	note := fg(t.Sub).Render(modeNames[s.mode]) + fg(t.Dim).Render(" by ") + fg(t.Sub).Render(s.sort)
	if s.filter != "" {
		note = fg(t.Accent2).Render("/"+s.filter) + fg(t.Dim).Render(" · ") + note
	}
	if len(s.rows) > visible {
		note += fg(t.Dim).Render(fmt.Sprintf(" · %d–%d of %d", s.top+1, min(s.top+visible, len(s.rows)), len(s.rows)))
	}
	table := m.panel("Processes", note, tw, h, lines)
	if detailW == 0 {
		return table
	}
	return beside(table, m.panel("Selected", "", detailW, h, m.detail(detailW-4, h-2)))
}

// wrap cuts s into at most maxLines lines of at most w cells, between
// words where a line has one in its second half.
func wrap(s string, w, maxLines int) []string {
	var out []string
	r := []rune(s)
	for len(r) > 0 && len(out) < maxLines && w > 0 {
		n := min(w, len(r))
		if n < len(r) && r[n] != ' ' {
			if at := strings.LastIndexByte(string(r[:n]), ' '); at >= 0 {
				if cut := len([]rune(string(r[:n])[:at])); cut > n/2 {
					n = cut
				}
			}
		}
		out = append(out, strings.TrimRight(string(r[:n]), " "))
		r = []rune(strings.TrimLeft(string(r[n:]), " "))
	}
	if len(r) > 0 && len(out) > 0 {
		out[len(out)-1] = fit(out[len(out)-1]+"…", w, false)
	}
	return out
}

var stateNames = map[byte]string{'R': "running", 'S': "sleeping", 'D': "waiting for IO", 'Z': "zombie", 'T': "stopped", 'I': "idle"}

func since(d time.Duration) string {
	switch {
	case d < 2*time.Minute:
		return fmt.Sprintf("%d s", int(max(d.Seconds(), 0)))
	case d < 2*time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

// detail describes the selected line.
func (m *model) detail(w, h int) []string {
	t, v := m.theme, m.view
	r := m.procs.selected()
	if r == nil {
		return []string{fg(t.Dim).Render("nothing matches")}
	}
	dim, sub := fg(t.Dim), fg(t.Sub)
	field := func(name, value string) string {
		return dim.Render(fit(name, 9, false)) + sub.Render(fit(value, w-9, false))
	}
	var out []string
	use := func(total query.Total, mem uint64, memName string) {
		share := total.Cores / float64(v.NCPU)
		out = append(out,
			dim.Render(fit("cpu", 9, false))+meter(share, w-9-12, t.heat(share*1.5), t.Sel)+fg(t.Text).Render(fmt.Sprintf(" %5.1f cores", total.Cores)),
			dim.Render(fit("memory", 9, false))+meter(float64(mem)/float64(max(v.Snap.Mem.Total, 1))*2, w-9-12, t.Mem[len(t.Mem)-1], t.Sel)+fg(t.Text).Render(" "+fit(query.Bytes(mem), 11, true)))
		if memName != "" {
			out = append(out, field("", memName))
		}
		if total.VRAM >= 1<<20 || total.GPU >= 1 {
			out = append(out, field("gpu", fmt.Sprintf("%.0f%% · %s vram", total.GPU, query.Bytes(total.VRAM))))
		}
	}
	if g := r.group; g != nil {
		_, style := m.groupColor(g.Kind)
		out = append(out, fg(style.c).Bold(true).Render(fit(g.Label, w, false)), "")
		use(g.Total, g.Total.Private, "private, shared files not summed")
		out = append(out, "", field("made of", fmt.Sprintf("%d processes", g.Total.Count)))
		if g.Lead != nil {
			out = append(out, field("top", query.Label(g.Lead)+" "+v.Handle(g.Lead)))
		}
		// Its heaviest processes.
		members := slices.Clone(g.Members)
		slices.SortStableFunc(members, func(a, b *query.Proc) int { return compareTotals(m.procs.sort)(procTotal(a), procTotal(b)) })
		out = append(out, "")
		for i, p := range members {
			if len(out) >= h-1 && i < len(members)-1 {
				out = append(out, dim.Render(fmt.Sprintf("and %d more", len(members)-i)))
				break
			}
			out = append(out, sub.Render(fit(query.Label(p), w-22, false))+fg(t.Text).Render(fit(fmt.Sprintf("%.1f", p.Cores), 6, true))+
				dim.Render(fit(query.Bytes(p.RSS), 9, true))+"  "+dim.Render(fit(strconv.Itoa(p.PID), 5, true)))
		}
		return out
	}
	p := r.proc
	out = append(out, fg(t.Text).Bold(true).Render(fit(r.label, w, false)))
	if r.count > 1 {
		out = append(out, dim.Render(fmt.Sprintf("%d processes that run the same", r.count)), "")
		use(r.total, r.mem, "private")
		return out
	}
	out = append(out, fg(t.Accent).Render(v.Handle(p)), "")
	use(r.total, p.RSS, query.Bytes(p.Private())+" of it private")
	state := stateNames[p.State]
	if state == "" {
		state = string(p.State)
	}
	out = append(out, "",
		field("state", fmt.Sprintf("%s · %d threads", state, p.Threads)),
		field("started", since(v.Snap.At.Sub(v.Started(p)))+" ago"))
	if p.Game >= 0 {
		out = append(out, field("game", v.Games[p.Game].Name))
	}
	if p.Scope >= 0 {
		sc := v.T3.Scopes[p.Scope]
		who := sc.Name + " agent"
		if th, ok := m.agents.titles[sc.ThreadID]; ok && th.Title != "" {
			who += " · " + th.Title
		}
		out = append(out, field("agent", who))
	}
	if dir, err := fs.ReadLink(m.o.Roots.Proc, path.Join(strconv.Itoa(p.PID), "cwd")); err == nil {
		out = append(out, field("in", dir))
	}
	if len(p.Args) > 0 {
		out = append(out, "", dim.Render("command"))
		for _, l := range wrap(strings.Join(strings.Fields(strings.Join(p.Args, " ")), " "), w, max(h-len(out)-4, 1)) {
			out = append(out, sub.Render(l))
		}
	}
	if p.Cgroup != "" && len(out)+3 < h {
		out = append(out, "", dim.Render("cgroup"))
		for _, l := range wrap(path.Base(p.Cgroup), w, 2) {
			out = append(out, sub.Render(l))
		}
	}
	return out
}

// confirm is a question that is open on the screen.
type confirm struct {
	handle, label string
	below         int // processes it started
}

// askKill opens the question for the selected process. A line that
// stands for several processes is no answer to "which one": the game's
// group starts at Steam's reaper, not at the game.
func (m *model) askKill() {
	r := m.procs.selected()
	if r == nil {
		return
	}
	p := r.proc
	if r.group != nil {
		p = nil
		if len(r.group.Members) == 1 {
			p = r.group.Members[0]
		}
	}
	if p == nil || r.count > 1 && r.group == nil {
		m.say("pick one process: enter opens a group, v lists every process")
		return
	}
	m.confirm = &confirm{handle: m.view.Handle(p), label: query.Label(p), below: len(m.view.Subtree(p.PID)) - 1}
}

func (m *model) confirmKey(k string) {
	c := m.confirm
	var sig syscall.Signal
	switch k {
	case "enter", "y":
		sig = syscall.SIGTERM
	case "K":
		sig = syscall.SIGKILL
	case "esc", "n", "q":
		m.confirm = nil
		return
	default:
		return
	}
	m.confirm = nil
	// The handle is looked up again: if the process is gone and its pid
	// was given to another, nothing is sent.
	found, err := m.view.Find(c.handle)
	if err != nil {
		m.say(err.Error())
		return
	}
	name := map[syscall.Signal]string{syscall.SIGTERM: "asked", syscall.SIGKILL: "killed"}[sig]
	if err := m.o.Kill(found[0].PID, sig); err != nil {
		m.say(fmt.Sprintf("%s %s: %v", c.label, c.handle, err))
		return
	}
	if sig == syscall.SIGTERM {
		m.say(fmt.Sprintf("%s %s %s to quit", name, c.label, c.handle))
	} else {
		m.say(fmt.Sprintf("%s %s %s", name, c.label, c.handle))
	}
}

// overlay returns the dialog that is open, if any.
func (m *model) overlay() []string {
	t := m.theme
	key := func(k, what string) string {
		return fg(t.Accent).Bold(true).Render(fit(k, 11, false)) + fg(t.Sub).Render(what)
	}
	switch {
	case m.confirm != nil:
		c := m.confirm
		lines := []string{"", fg(t.Text).Bold(true).Render(c.label) + "  " + fg(t.Accent).Render(c.handle)}
		if c.below > 0 {
			lines = append(lines, fg(t.Dim).Render(fmt.Sprintf("only this one, not the %d it started", c.below)))
		}
		lines = append(lines, "", key("enter", "ask it to quit (SIGTERM)"), key("shift+k", "force it (SIGKILL)"), key("esc", "leave it alone"), "")
		return m.frame(t.Accent2, "End this process?", "", 50, len(lines)+2, lines)
	case m.help:
		lines := []string{"",
			key("1 2 3 4", "Overview, Processes, Agents, Runs"),
			key("tab", "the next tab (shift+tab: the one before)"),
			key("↑ ↓  j k", "move; pgup, pgdn, g and G go further"),
			key("mouse", "click a tab or a line, wheel to scroll"),
			"",
			fg(t.Dim).Render("Processes"),
			key("enter", "open or close a group"),
			key("v", "groups, all processes, or the tree"),
			key("s", "sort by CPU, memory or GPU"),
			key("/", "filter by name (esc clears it)"),
			key("x", "end the selected process"),
			"",
			fg(t.Dim).Render("Runs"),
			key("space", "mark a run; two marked runs are compared"),
			"",
			key("t", "the next theme"),
			key("q", "quit"),
			"",
			fg(t.Dim).Render("Memory of a group is private memory: what"),
			fg(t.Dim).Render("leaving would free. One process shows RSS."),
			""}
		return m.frame(t.Accent2, "Keys", fg(t.Dim).Render("any key closes"), 58, len(lines)+2, lines)
	}
	return nil
}
