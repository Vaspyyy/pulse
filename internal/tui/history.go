package tui

import (
	"fmt"
	"image/color"
	"strings"

	"github.com/Vaspyyy/pulse/internal/query"
)

const histLen = 600 // readings kept: ten minutes at one a second

// history is what the graphs draw: the last readings of each number.
type history struct {
	cpu, mem, gpu   []float64 // shares, 0 to 1
	netIn, netOut   []float64 // bytes per second
	diskIn, diskOut []float64
	groups          map[string][]float64     // cores, by group name
	agents          map[string]*agentHistory // by scope unit
}

// agentHistory is one agent scope over time.
type agentHistory struct {
	cores []float64
	mem   []float64 // bytes, as its cgroup counts them
}

func push(s *[]float64, v float64) {
	*s = append(*s, v)
	if len(*s) > histLen {
		*s = (*s)[len(*s)-histLen:]
	}
}

func (h *history) add(v *query.View, groups []query.Group) {
	push(&h.cpu, v.Cores/float64(v.NCPU))
	if m := v.Snap.Mem; m.Total > 0 {
		push(&h.mem, float64(m.Total-min(m.Available, m.Total))/float64(m.Total))
	}
	if len(v.GPUs) > 0 {
		load, _ := v.GPUs[0].Load()
		push(&h.gpu, load/100)
	}
	push(&h.netIn, v.NetIn)
	push(&h.netOut, v.NetOut)
	push(&h.diskIn, v.DiskIn)
	push(&h.diskOut, v.DiskOut)

	if v.T3 != nil {
		if h.agents == nil {
			h.agents = map[string]*agentHistory{}
		}
		cores := make([]float64, len(v.T3.Scopes))
		for i := range v.Procs {
			if p := &v.Procs[i]; p.Scope >= 0 {
				cores[p.Scope] += p.Cores
			}
		}
		live := map[string]bool{}
		for i, s := range v.T3.Scopes {
			live[s.Unit] = true
			a := h.agents[s.Unit]
			if a == nil {
				a = &agentHistory{}
				h.agents[s.Unit] = a
			}
			push(&a.cores, cores[i])
			push(&a.mem, float64(s.Memory))
		}
		for unit := range h.agents {
			if !live[unit] {
				delete(h.agents, unit)
			}
		}
	}

	seen := map[string]bool{}
	for _, g := range groups {
		key := g.Kind + "/" + g.Name
		seen[key] = true
		s := h.groups[key]
		push(&s, g.Total.Cores)
		h.groups[key] = s
	}
	for key := range h.groups {
		if !seen[key] {
			delete(h.groups, key)
		}
	}
}

// scaled returns vals divided by top, for drawing.
func scaled(vals []float64, top float64) []float64 {
	out := make([]float64, len(vals))
	if top <= 0 {
		return out
	}
	for i, v := range vals {
		out[i] = v / top
	}
	return out
}

// ceiling returns a round number at or above the largest of the last n
// values, and at least floor: what a graph of them is scaled to.
func ceiling(vals []float64, n int, floor float64) float64 {
	top := floor
	for i := max(len(vals)-n, 0); i < len(vals); i++ {
		top = max(top, vals[i])
	}
	// The next of 1, 2, 5 times a power of ten.
	step := 1.0
	for step*10 <= top {
		step *= 10
	}
	for _, m := range []float64{1, 2, 5, 10} {
		if step*m >= top {
			return step * m
		}
	}
	return top
}

// rate formats bytes per second.
func rate(b float64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB/s", b/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB/s", b/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f KB/s", b/(1<<10))
	}
	return fmt.Sprintf("%.0f B/s", b)
}

// seg is a piece of a table line: text in one colour.
type seg struct {
	s    string
	c    color.Color
	bold bool
}

// row renders segments as one line of w cells. A selected row gets the
// selection's background from edge to edge.
func (m *model) row(segs []seg, w int, selected bool) string {
	var b strings.Builder
	used := 0
	for _, s := range segs {
		st := fg(s.c).Bold(s.bold)
		if selected {
			st = st.Background(m.theme.Sel)
		}
		b.WriteString(st.Render(s.s))
		used += width(s.s)
	}
	if pad := w - used; pad > 0 {
		st := fg(m.theme.Text)
		if selected {
			st = st.Background(m.theme.Sel)
		}
		b.WriteString(st.Render(strings.Repeat(" ", pad)))
	}
	if used > w {
		return clip(b.String(), w)
	}
	return b.String()
}

// meterSegs is meter as segments, so it can sit in a selected row.
func meterSegs(frac float64, w int, full, empty color.Color) []seg {
	frac = min(max(frac, 0), 1)
	n := int(frac*float64(w) + 0.5)
	if frac > 0.004 && n == 0 {
		n = 1
	}
	return []seg{{s: strings.Repeat("━", n), c: full}, {s: strings.Repeat("━", w-n), c: empty}}
}
