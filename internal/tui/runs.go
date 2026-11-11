package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Vaspyyy/pulse/internal/cli"
	"github.com/Vaspyyy/pulse/internal/query"
	"github.com/Vaspyyy/pulse/record"
)

type runsState struct {
	runs   []*record.Run // newest first
	cursor string        // the selected run's id
	index  int
	top    int
	marked []string // at most two ids, in the order they were marked
}

func (s *runsState) set(runs []*record.Run) {
	s.runs = runs
	for i, r := range runs {
		if r.ID == s.cursor {
			s.index = i
		}
	}
	s.index = min(max(s.index, 0), max(len(runs)-1, 0))
	if len(runs) > 0 {
		s.cursor = runs[s.index].ID
	}
	s.marked = slices.DeleteFunc(s.marked, func(id string) bool {
		return !slices.ContainsFunc(runs, func(r *record.Run) bool { return r.ID == id })
	})
}

func (s *runsState) move(by int) {
	if len(s.runs) == 0 {
		return
	}
	s.index = min(max(s.index+by, 0), len(s.runs)-1)
	s.cursor = s.runs[s.index].ID
}

// mark marks or unmarks the selected run. A third mark replaces the
// older of the two.
func (s *runsState) mark() {
	if len(s.runs) == 0 {
		return
	}
	id := s.runs[s.index].ID
	if i := slices.Index(s.marked, id); i >= 0 {
		s.marked = slices.Delete(s.marked, i, i+1)
		return
	}
	s.marked = append(s.marked, id)
	if len(s.marked) > 2 {
		s.marked = s.marked[1:]
	}
}

func (s *runsState) key(m *model, k string) {
	if k == "esc" {
		s.marked = nil
	}
}

func (s *runsState) byID(id string) *record.Run {
	for _, r := range s.runs {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// runsTab is the fourth tab: the recorded runs, and beside them the
// selected run or the two marked ones compared.
func (m *model) runsTab(w, h, y0 int) []string {
	t, s := m.theme, &m.runs
	if len(s.runs) == 0 {
		msg := []string{"", fg(t.Sub).Render("Nothing is recorded yet."), "",
			fg(t.Dim).Render("Record what a command uses with"), fg(t.Accent).Render("  pulse run -- <command>"),
			fg(t.Dim).Render("and compare two runs here or with pulse diff.")}
		return m.panel("Runs", "", w, h, msg)
	}
	listW := min(max(w*42/100, 44), 64)
	iw := listW - 4
	visible := h - 3
	if s.index < s.top {
		s.top = s.index
	}
	if s.index >= s.top+visible {
		s.top = s.index - visible + 1
	}
	const whenW, wallW, peakW = 11, 8, 7
	cmdW := max(iw-2-whenW-wallW-peakW-6, 8)
	rows := []string{m.row([]seg{{s: "  " + fit("WHEN", whenW, false), c: t.Dim}, {s: "  " + fit("COMMAND", cmdW, false), c: t.Dim},
		{s: "  " + fit("TIME", wallW, true), c: t.Dim}, {s: "  " + fit("PEAK", peakW, true), c: t.Dim}}, iw, false)}
	for i := s.top; i < min(s.top+visible, len(s.runs)); i++ {
		r := s.runs[i]
		mark, markColor := "  ", t.Dim
		if n := slices.Index(s.marked, r.ID); n >= 0 {
			mark, markColor = []string{"① ", "② "}[n], t.Accent2
		}
		cmdColor := t.Text
		if r.Exit != 0 {
			cmdColor = t.Bad
		}
		wallColor := t.Sub
		if r.Other.Disturbed {
			wallColor = t.Warn // its wall time isn't comparable
		}
		rows = append(rows, m.row([]seg{{s: mark, c: markColor, bold: true}, {s: fit(cli.When(r.Start), whenW, false), c: t.Dim},
			{s: "  " + fit(cli.CommandLine(r, cmdW), cmdW, false), c: cmdColor},
			{s: "  " + fit(cli.Dur(r.WallMS), wallW, true), c: wallColor},
			{s: "  " + fit(query.Bytes(r.PeakPrivate), peakW, true), c: t.Sub}}, iw, i == s.index))
	}
	top := s.top
	m.zones = append(m.zones, zone{x: 2, y: y0 + 2, w: iw, h: visible, do: func(row int) {
		if i := top + row; i < len(s.runs) {
			s.index, s.cursor = i, s.runs[i].ID
		}
	}})
	list := m.panel("Runs", fg(t.Dim).Render(fmt.Sprintf("%d recorded", len(s.runs))), listW, h, rows)

	dw := w - listW
	if len(s.marked) == 2 {
		a, b := s.byID(s.marked[0]), s.byID(s.marked[1])
		return beside(list, m.panel("Compared", fg(t.Dim).Render("esc lets go"), dw, h, m.compare(a, b, dw-4)))
	}
	r := s.runs[s.index]
	title := r.ID
	if r.Name != "" {
		title += " " + r.Name
	}
	return beside(list, m.panel(title, fg(t.Dim).Render(cli.Place(r)), dw, h, m.runDetail(r, dw-4, h-2)))
}

// runDetail shows one run: its numbers, how it went over time, and its
// processes.
func (m *model) runDetail(r *record.Run, w, h int) []string {
	t := m.theme
	exitColor := t.Good
	if r.Exit != 0 {
		exitColor = t.Bad
	}
	out := []string{fg(t.Text).Bold(true).Render(fit(cli.CommandLine(r, w), w, false)),
		fg(exitColor).Render(cli.ExitText(r)) + fg(t.Dim).Render(" · ") + fg(t.Sub).Render(cli.Dur(r.WallMS)) +
			fg(t.Dim).Render(" · "+cli.When(r.Start)), ""}
	for _, l := range cli.Summary(r) {
		l = strings.TrimPrefix(l, "  ")
		name, rest, _ := strings.Cut(l, " ")
		out = append(out, fg(t.Dim).Render(fit(name, 7, false))+fg(t.Sub).Render(fit(strings.TrimSpace(rest), w-7, false)))
	}
	if tl := r.Timeline; len(tl.Cores) > 1 && h-len(out) >= 8 {
		gh := min(max((h-len(out)-8)/2, 2), 5)
		var topCores float64
		cores := make([]float64, len(tl.Cores))
		mem := make([]float64, len(tl.Private))
		for i, c := range tl.Cores {
			cores[i] = float64(c)
			topCores = max(topCores, float64(c))
		}
		var topMem float64
		for i, p := range tl.Private {
			mem[i] = float64(p)
			topMem = max(topMem, float64(p))
		}
		out = append(out, "", fg(t.Dim).Render("cores over time, up to ")+fg(t.Sub).Render(fmt.Sprintf("%.1f", topCores)))
		out = append(out, m.area(stretch(scaled(cores, topCores), w), w, gh, t.CPU)...)
		out = append(out, fg(t.Dim).Render("memory over time, up to ")+fg(t.Sub).Render(query.Bytes(r.PeakPrivate)))
		out = append(out, m.area(stretch(scaled(mem, topMem), w), w, gh, t.Mem)...)
	}
	if len(r.Procs) > 0 && h-len(out) >= 4 {
		out = append(out, "", fg(t.Dim).Render("processes, as sampled"))
		for _, p := range r.Procs {
			if len(out) >= h {
				break
			}
			label := p.Label
			if p.Count > 1 {
				label += fmt.Sprintf(" ×%d", p.Count)
			}
			out = append(out, fg(t.Text).Render(fit(label, max(w-24, 8), false))+fg(t.Sub).Render(fit(cli.Dur(p.CPUMS)+" cpu", 13, true))+
				fg(t.Dim).Render(fit(query.Bytes(p.PeakRSS), 9, true)))
		}
	}
	return out
}

// stretch resamples vals to n values, so a timeline of any length fills
// a graph: the highest value where several fall together.
func stretch(vals []float64, n int) []float64 {
	if len(vals) == 0 || n <= 0 {
		return nil
	}
	out := make([]float64, n)
	if len(vals) >= n {
		for i, v := range vals {
			j := i * n / len(vals)
			out[j] = max(out[j], v)
		}
		return out
	}
	for j := range out {
		out[j] = vals[j*len(vals)/n]
	}
	return out
}

// compare shows two runs side by side.
func (m *model) compare(a, b *record.Run, w int) []string {
	t := m.theme
	head, rows, notes := cli.DiffRows(a, b)
	out := []string{}
	for _, l := range wrap(head, w, 2) {
		out = append(out, fg(t.Text).Bold(true).Render(l))
	}
	out = append(out, "")
	nameW := 0
	for _, r := range rows {
		nameW = max(nameW, width(r[0]))
	}
	colW := max((w-nameW-10-6)/2, 10)
	for i, r := range rows {
		if i == 0 {
			out = append(out, m.row([]seg{{s: fit("", nameW, false)}, {s: "  " + fit("① "+r[1], colW, false), c: t.Accent2, bold: true},
				{s: "  " + fit("② "+r[2], colW, false), c: t.Accent2, bold: true}, {s: "  " + fit(r[3], 10, true), c: t.Dim}}, w, false))
			continue
		}
		change := t.Dim
		switch {
		case strings.HasPrefix(r[3], "+"):
			change = t.Bad
		case strings.HasPrefix(r[3], "-"):
			change = t.Good
		}
		out = append(out, m.row([]seg{{s: fit(r[0], nameW, false), c: t.Dim}, {s: "  " + fit(r[1], colW, false), c: t.Sub},
			{s: "  " + fit(r[2], colW, false), c: t.Text}, {s: "  " + fit(r[3], 10, true), c: change, bold: true}}, w, false))
	}
	if len(notes) > 0 {
		out = append(out, "")
	}
	for _, n := range notes {
		for i, l := range wrap(n, w-2, 3) {
			mark := "  "
			if i == 0 {
				mark = "! "
			}
			out = append(out, fg(t.Warn).Render(mark)+fg(t.Sub).Render(l))
		}
	}
	return out
}
