// Package tui is Pulse for people: a full-screen monitor with the
// machine's graphs, its processes, T3's agents and the recorded runs.
// It draws what internal/query computes, a reading a second.
package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Vaspyyy/pulse/collect"
	"github.com/Vaspyyy/pulse/collect/gpu"
	"github.com/Vaspyyy/pulse/collect/t3"
	"github.com/Vaspyyy/pulse/internal/cli"
	"github.com/Vaspyyy/pulse/internal/query"
	"github.com/Vaspyyy/pulse/record"
)

// Options are what the TUI runs on.
type Options struct {
	Roots     collect.Roots
	UID       int
	GPU       func() []gpu.Device                     // may be nil
	SteamName func(appID string) string               // may be nil
	Threads   func(ids []string) map[string]t3.Thread // may be nil; slow, so it runs beside the UI
	Config    cli.Config
	Store     record.Store
	// StateFile remembers the theme and the tab between starts. "" for no
	// memory.
	StateFile string
	// Terminal is the terminal's name, as TERM_PROGRAM has it. In "lumen"
	// the theme to start with is lumen's.
	Terminal string
	// NoColor is NO_COLOR: the terminal's own colours, and Pulse paints no
	// background.
	NoColor bool
	// Interval is the time between readings; 0 means a second.
	Interval time.Duration
	Now      func() time.Time                        // nil means time.Now
	Kill     func(pid int, sig syscall.Signal) error // nil means syscall.Kill
}

// Run shows the TUI until the user leaves it.
func Run(o Options) error {
	// What a reading allocates is garbage a second later. Collecting it
	// sooner than Go would keeps the monitor at about half the memory.
	debug.SetGCPercent(50)
	_, err := tea.NewProgram(newModel(o)).Run()
	return err
}

type tab int

const (
	tabOverview tab = iota
	tabProcs
	tabAgents
	tabRuns
)

var tabNames = []string{"Overview", "Processes", "Agents", "Runs"}

const firstWindow = 250 * time.Millisecond // until the second reading, which gives the first rates

type model struct {
	o      Options
	theme  Theme
	picked string // the theme the user chose with t, "" for none yet
	w, h   int
	tab    tab

	c        *collect.Collector
	prev     *collect.Snapshot
	sampling bool
	view     *query.View
	load     query.Load
	groups   []query.Group // by CPU
	hist     history

	procs  procsState
	agents agentsState
	runs   runsState

	help    bool
	confirm *confirm
	toast   string
	toastAt time.Time
	zones   []zone
}

// state is what is remembered between starts. The theme is only in it
// once the user picked one with t: until then the config file and the
// terminal decide.
type state struct {
	Picked string `json:"picked,omitempty"`
	Tab    int    `json:"tab"`
}

func newModel(o Options) *model {
	if o.Interval == 0 {
		o.Interval = time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Kill == nil {
		o.Kill = syscall.Kill
	}
	m := &model{o: o, theme: themeNamed(o.Config.Theme), c: collect.New(o.Roots)}
	if o.Config.Theme == "" && o.Terminal == "lumen" {
		m.theme = themeNamed("lumen")
	}
	if o.NoColor {
		m.theme = themeNamed("terminal")
	}
	m.c.Markers = append(append([]string{}, m.c.Markers...), o.Config.GameEnv...)
	m.c.Now = o.Now
	m.c.SharedEvery = 5
	m.hist.groups = map[string][]float64{}
	m.procs.expanded = map[string]bool{}
	m.agents.titles = map[string]t3.Thread{}
	if b, err := os.ReadFile(o.StateFile); err == nil && o.StateFile != "" {
		var st state
		if json.Unmarshal(b, &st) == nil {
			if st.Picked != "" && !o.NoColor {
				m.theme, m.picked = themeNamed(st.Picked), st.Picked
			}
			if st.Tab >= 0 && st.Tab < len(tabNames) {
				m.tab = tab(st.Tab)
			}
		}
	}
	return m
}

func (m *model) saveState() {
	if m.o.StateFile == "" {
		return
	}
	b, _ := json.Marshal(state{Picked: m.picked, Tab: int(m.tab)})
	if os.MkdirAll(filepath.Dir(m.o.StateFile), 0o700) == nil {
		_ = os.WriteFile(m.o.StateFile, b, 0o600)
	}
}

type (
	sampleMsg struct {
		snap *collect.Snapshot
		view *query.View
	}
	titlesMsg map[string]t3.Thread
	runsMsg   []*record.Run
)

func (m *model) Init() tea.Cmd { return tea.Batch(m.sample(0), m.loadRuns()) }

// sample waits and then takes a reading, beside the UI. The wait is part
// of it, so that a second brings one message and one frame, not two.
// Readings never overlap: the collector isn't made for that.
func (m *model) sample(wait time.Duration) tea.Cmd {
	if m.sampling {
		return nil
	}
	m.sampling = true
	prev := m.prev
	return func() tea.Msg {
		time.Sleep(wait)
		gpus := make(chan []gpu.Device, 1)
		go func() {
			if m.o.GPU == nil {
				gpus <- nil
				return
			}
			gpus <- m.o.GPU()
		}()
		snap := m.c.Snapshot()
		return sampleMsg{snap, query.NewView(prev, snap, m.o.Roots, query.Options{
			UID: m.o.UID, SteamName: m.o.SteamName, GPUs: <-gpus,
			GameEnv: m.o.Config.GameEnv, GameProcess: m.o.Config.GameProcess,
		})}
	}
}

func (m *model) loadRuns() tea.Cmd {
	return func() tea.Msg { return runsMsg(m.o.Store.List()) }
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case sampleMsg:
		m.sampling = false
		m.prev, m.view = msg.snap, msg.view
		m.load = msg.view.Load(m.o.Config.Thresholds)
		m.groups = msg.view.Groups("cpu")
		wait := m.o.Interval
		if msg.view.Window == 0 {
			wait = firstWindow
		} else {
			m.hist.add(msg.view, m.groups)
		}
		m.procs.rebuild(m)
		cmds := []tea.Cmd{m.sample(wait)}
		if c := m.fetchTitles(); c != nil {
			cmds = append(cmds, c)
		}
		if m.tab == tabRuns && m.o.Now().Unix()%5 == 0 {
			cmds = append(cmds, m.loadRuns())
		}
		return m, tea.Batch(cmds...)
	case titlesMsg:
		m.agents.fetching = false
		for id, th := range msg {
			m.agents.titles[id] = th
		}
	case runsMsg:
		m.runs.set(msg)
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			m.click(msg.X, msg.Y)
		}
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.move(-3)
		case tea.MouseWheelDown:
			m.move(3)
		}
	}
	return m, nil
}

// fetchTitles asks T3 for the titles of threads not asked about yet.
func (m *model) fetchTitles() tea.Cmd {
	if m.o.Threads == nil || m.view.T3 == nil || m.agents.fetching {
		return nil
	}
	var ids []string
	for _, s := range m.view.T3.Scopes {
		if _, ok := m.agents.titles[s.ThreadID]; s.ThreadID != "" && !ok {
			ids = append(ids, s.ThreadID)
			m.agents.titles[s.ThreadID] = t3.Thread{} // asked: don't ask again each second
		}
	}
	if len(ids) == 0 {
		return nil
	}
	m.agents.fetching = true
	return func() tea.Msg { return titlesMsg(m.o.Threads(ids)) }
}

func (m *model) setTab(t tab) tea.Cmd {
	m.tab = t
	m.saveState()
	if t == tabRuns {
		return m.loadRuns()
	}
	return nil
}

func (m *model) key(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "ctrl+c" {
		return tea.Quit
	}
	switch {
	case m.confirm != nil:
		m.confirmKey(s)
		return nil
	case m.help:
		m.help = false
		return nil
	case m.procs.filtering:
		m.procs.filterKey(m, k)
		return nil
	}
	switch s {
	case "q":
		return tea.Quit
	case "?":
		m.help = true
	case "1", "2", "3", "4":
		return m.setTab(tab(s[0] - '1'))
	case "tab", "right", "l":
		return m.setTab((m.tab + 1) % tab(len(tabNames)))
	case "shift+tab", "left", "h":
		return m.setTab((m.tab + tab(len(tabNames)) - 1) % tab(len(tabNames)))
	case "t":
		for i, t := range Themes {
			if t.Name == m.theme.Name {
				m.theme = Themes[(i+1)%len(Themes)]
				break
			}
		}
		m.picked = m.theme.Name
		m.saveState()
		m.say("theme: " + m.theme.Name)
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup":
		m.move(-m.page())
	case "pgdown", "space":
		if s == "space" && m.tab == tabRuns {
			m.runs.mark()
		} else {
			m.move(m.page())
		}
	case "home", "g":
		m.move(-1 << 30)
	case "end", "G":
		m.move(1 << 30)
	default:
		switch m.tab {
		case tabProcs:
			m.procs.key(m, s)
		case tabRuns:
			m.runs.key(m, s)
		case tabOverview:
			if s == "enter" {
				return m.setTab(tabProcs)
			}
		}
	}
	return nil
}

func (m *model) page() int { return max(m.h-8, 1) }

// move moves the active tab's cursor.
func (m *model) move(by int) {
	switch m.tab {
	case tabProcs, tabOverview:
		m.procs.move(by)
	case tabAgents:
		m.agents.move(m, by)
	case tabRuns:
		m.runs.move(by)
	}
}

// say shows a message in the footer for a few seconds.
func (m *model) say(s string) {
	m.toast, m.toastAt = s, m.o.Now()
}

// zone is a rectangle of the screen that does something when clicked.
type zone struct {
	x, y, w, h int
	do         func(row int) // row is the click's line within the zone
}

func (m *model) click(x, y int) {
	for _, z := range m.zones {
		if x >= z.x && x < z.x+z.w && y >= z.y && y < z.y+z.h {
			z.do(y - z.y)
			return
		}
	}
}

func (m *model) View() tea.View {
	m.zones = m.zones[:0]
	var lines []string
	switch {
	case m.w == 0 || m.view == nil:
		lines = []string{""}
	case m.w < 72 || m.h < 20:
		lines = []string{fg(m.theme.Sub).Render(fmt.Sprintf("pulse needs 72×20 cells, this is %d×%d", m.w, m.h))}
	default:
		body := m.h - 2
		lines = append(lines, m.header())
		switch m.tab {
		case tabOverview:
			lines = append(lines, m.overview(m.w, body)...)
		case tabProcs:
			lines = append(lines, m.procsTab(m.w, body, 1)...)
		case tabAgents:
			lines = append(lines, m.agentsTab(m.w, body, 1)...)
		case tabRuns:
			lines = append(lines, m.runsTab(m.w, body, 1)...)
		}
		lines = append(lines, m.footer())
	}
	// Every line fills the terminal: a short one would leave the last
	// frame's cells behind it. The panels do by themselves.
	if n := len(lines) - 1; n > 0 {
		lines[0], lines[n] = fill(lines[0], m.w), fill(lines[n], m.w)
	}
	// A dialog lies over the middle, with a cell of air around it so that
	// what is behind doesn't touch it.
	if box := m.overlay(); box != nil && len(lines) > 1 {
		blank := strings.Repeat(" ", width(box[0]))
		box = append(append([]string{blank}, box...), blank)
		bw := width(blank) + 4
		x, y := max((m.w-bw)/2, 0), max((m.h-len(box))/2, 0)
		for i, b := range box {
			if y+i >= len(lines) {
				break
			}
			l := lines[y+i]
			lines[y+i] = ansi.Truncate(l, x, "") + "\x1b[m  " + b + "  " + ansi.TruncateLeft(l, x+bw, "")
		}
	}
	content := strings.Join(lines, "\n")
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "pulse"
	v.BackgroundColor = m.theme.Bg
	if m.theme.Bg != nil {
		v.ForegroundColor = m.theme.Text
	}
	return v
}

// header is the top line: the name, the tabs, the load verdict and the
// clock.
func (m *model) header() string {
	t := m.theme
	onAccent := t.Ink
	if onAccent == nil {
		onAccent = t.Bg
	}
	if onAccent == nil {
		onAccent = lipgloss.Color("0")
	}
	left := fg(onAccent).Background(t.Accent2).Bold(true).Render(" ◆ pulse ") + " "
	x := 10
	for i, name := range tabNames {
		label := " " + name + " "
		i := i
		m.zones = append(m.zones, zone{x: x, y: 0, w: len(label), h: 1, do: func(int) { m.setTab(tab(i)) }})
		if tab(i) == m.tab {
			left += fg(onAccent).Background(t.Accent).Bold(true).Render(label)
		} else {
			left += fg(t.Sub).Render(label)
		}
		left += " "
		x += len(label) + 1
	}
	dot, word, why := t.Good, "quiet", ""
	if m.load.Busy {
		dot, word = t.Warn, "busy"
		why = m.load.Reasons[0]
		if len(m.load.Reasons) > 1 {
			why += fmt.Sprintf(" +%d", len(m.load.Reasons)-1)
		}
	}
	clock := m.o.Now().Format("15:04:05")
	room := m.w - x - len(word) - len(clock) - 10
	pill := paint{bg: t.Sel}
	right := pill.Foreground(dot).Render(" ● ") + pill.Foreground(t.Text).Bold(true).Render(word+" ")
	used := 4 + len(word)
	if why != "" && room > 12 {
		why = strings.TrimRight(fit(why, min(room, width(why)), false), " ")
		right += " " + fg(t.Sub).Render(why)
		used += 1 + width(why)
	}
	right += "  " + fg(t.Dim).Render(clock) + " "
	used += 3 + len(clock)
	return clip(left+strings.Repeat(" ", max(m.w-x-used, 0))+right, m.w)
}

// footer is the bottom line: the keys that do something here, or a
// message, or the filter being typed.
func (m *model) footer() string {
	t := m.theme
	if m.procs.filtering {
		return clip(" "+fg(t.Accent).Bold(true).Render("/")+" "+fg(t.Text).Render(m.procs.filter)+fg(t.Accent).Render("▏")+
			fg(t.Dim).Render("   enter keeps it · esc clears it"), m.w)
	}
	if m.toast != "" && m.o.Now().Sub(m.toastAt) < 4*time.Second {
		return clip(" "+fg(t.Accent2).Render(m.toast), m.w)
	}
	keys := [][2]string{{"1-4", "tabs"}}
	switch m.tab {
	case tabOverview:
		keys = append(keys, [2]string{"enter", "processes"})
	case tabProcs:
		keys = append(keys, [2]string{"↑↓", "move"}, [2]string{"enter", "open"}, [2]string{"/", "filter"},
			[2]string{"s", "sort"}, [2]string{"v", "view"}, [2]string{"x", "end"})
	case tabAgents:
		keys = append(keys, [2]string{"↑↓", "move"})
	case tabRuns:
		keys = append(keys, [2]string{"↑↓", "move"}, [2]string{"space", "mark two to compare"})
	}
	keys = append(keys, [2]string{"t", "theme"}, [2]string{"?", "help"}, [2]string{"q", "quit"})
	// On a narrow terminal the first ones go: they are the ones a user
	// finds without being told.
	const between = 5
	need := func() int {
		n := 1
		for _, k := range keys {
			n += width(k[0]) + 1 + width(k[1]) + between
		}
		return n - between
	}
	for len(keys) > 2 && need() > m.w {
		keys = keys[1:]
	}
	var b strings.Builder
	b.WriteString(" ")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(fg(t.Line).Render("  ·  "))
		}
		b.WriteString(fg(t.Accent).Render(k[0]) + " " + fg(t.Dim).Render(k[1]))
	}
	return b.String()
}
