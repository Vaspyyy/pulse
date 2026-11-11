package tui

import (
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Vaspyyy/pulse/collect/collecttest"
	"github.com/Vaspyyy/pulse/collect/gpu"
	"github.com/Vaspyyy/pulse/collect/t3"
	"github.com/Vaspyyy/pulse/internal/cli"
	"github.com/Vaspyyy/pulse/internal/query"
	"github.com/Vaspyyy/pulse/record"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

const mib = 1 << 20

func TestMain(m *testing.M) {
	time.Local = time.UTC // recorded runs are shown in local time
	os.Exit(m.Run())
}

// screen drives a model by hand: no program, no terminal, no clock. The
// machine is collecttest's desk.
type screen struct {
	t      *testing.T
	m      *model
	mach   *collecttest.Machine
	killed []string // the signals sent, as "pid signal"
}

func open(t *testing.T, w, h int) *screen {
	t.Helper()
	s := &screen{t: t, mach: collecttest.Desk()}
	st := record.Store{Dir: filepath.Join(t.TempDir(), "runs")}
	for _, r := range sampleRuns() {
		if err := st.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	threads := map[string]t3.Thread{collecttest.DeskThread: {ID: collecttest.DeskThread,
		Title: "Make the build faster, please, without breaking anything", Model: "claude-opus-5-5"}}
	s.m = newModel(Options{
		Roots: s.mach.Roots(),
		UID:   1000,
		GPU: func() []gpu.Device {
			return []gpu.Device{{Name: "RTX 3070", Vendor: "nvidia", Util: 71, Power: 168, PowerLimit: 240,
				MemUsed: 5939 * mib, MemTotal: 8192 * mib, Temp: 64,
				Procs: []gpu.Proc{{PID: 100, Mem: 300 * mib, Util: 3}, {PID: 201, Mem: 5222 * mib, Util: 68}}}}
		},
		SteamName: func(id string) string {
			if id == "1145360" {
				return "Hades"
			}
			return ""
		},
		Threads:   func([]string) map[string]t3.Thread { return threads },
		Config:    cli.Config{Thresholds: query.DefaultThresholds},
		Store:     st,
		StateFile: filepath.Join(t.TempDir(), "tui.json"),
		Now:       func() time.Time { return time.Unix(collecttest.BootTime+3600, 0) }, // 01:00:00 after boot
		Kill: func(pid int, sig syscall.Signal) error {
			s.killed = append(s.killed, strconv.Itoa(pid)+" "+sig.String())
			return nil
		},
	})
	s.m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	s.m.Update(s.m.loadRuns()())
	s.reading()
	s.m.Update(titlesMsg(threads))
	for range 12 {
		s.second()
	}
	return s
}

// reading takes a reading now. The model has asked for its next one
// already, in a command nobody runs here.
func (s *screen) reading() {
	s.m.sampling = false
	s.m.Update(s.m.sample(0)())
}

// second lets the desk work for a second and takes the next reading.
func (s *screen) second() {
	work := map[int]uint64{}
	for pid, ticks := range collecttest.DeskWork {
		work[pid] = 4 * ticks
	}
	s.mach.Run(100, work)
	s.reading()
}

// keys presses keys: a name like "enter" or "esc", or one character.
func (s *screen) keys(names ...string) {
	for _, n := range names {
		k := tea.KeyPressMsg{Code: []rune(n)[0], Text: n}
		switch n {
		case "enter":
			k = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			k = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "up":
			k = tea.KeyPressMsg{Code: tea.KeyUp}
		case "down":
			k = tea.KeyPressMsg{Code: tea.KeyDown}
		case "tab":
			k = tea.KeyPressMsg{Code: tea.KeyTab}
		case "backspace":
			k = tea.KeyPressMsg{Code: tea.KeyBackspace}
		case "space":
			k = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		}
		s.m.Update(k)
	}
}

// frame is what the terminal would show, without the colours. It must
// fill the terminal exactly: a longer line would wrap and tear the screen.
func (s *screen) frame() string {
	s.t.Helper()
	text := ansi.Strip(s.m.View().Content)
	lines := strings.Split(text, "\n")
	if len(lines) != s.m.h {
		s.t.Errorf("%d lines on a terminal of %d", len(lines), s.m.h)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != s.m.w {
			s.t.Errorf("line %d is %d cells wide on a terminal of %d: %q", i+1, w, s.m.w, l)
		}
	}
	// Goldens without the spaces that fill each line.
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.Join(lines, "\n") + "\n"
}

func (s *screen) golden(name string) {
	s.t.Helper()
	got := s.frame()
	file := filepath.Join("testdata", name+".txt")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			s.t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
			s.t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		s.t.Fatal(err)
	}
	if got != string(want) {
		s.t.Errorf("%s differs (go test ./internal/tui -update rewrites it)\n--- got\n%s--- want\n%s", file, got, want)
	}
}

func sampleRuns() []*record.Run {
	var cores []float32
	var mem []uint32
	for i := range 120 {
		x := float32(i%40) / 40
		cores = append(cores, 2+13*x)
		mem = append(mem, uint32(400+float32(i)/120*8700))
	}
	before := &record.Run{
		V: 1, ID: "r7k2mq", Name: "base", Command: []string{"cargo", "test"}, Cwd: "/home/u/pulse",
		Repo:  &record.Repo{Root: "/home/u/pulse", Head: "abc123f0000000000000000000000000000000ab", Branch: "main", State: "clean"},
		Start: time.Date(2026, 11, 8, 21, 14, 0, 0, time.UTC), WallMS: 48200, Method: "cgroup", NCPU: 16,
		UserMS: 612000, SystemMS: 41000, PeakPrivate: 9100 * mib, SampleMS: 100, Samples: 480,
		LargestRSS: 2150 * mib, LargestName: "rustc syn",
		Stall:     &record.Stall{CPUMS: 2100, MemoryMS: 0, IOMS: 400},
		ReadBytes: 1229 * mib, WrittenBytes: 3482 * mib,
		Other: record.Other{Known: true, AvgCores: 0.4, PeakCores: 0.9},
		Procs: []record.ProcStat{
			{Label: "rustc syn", Count: 1, CPUMS: 31000, PeakRSS: 2150 * mib},
			{Label: "rustc", Count: 211, CPUMS: 559000, PeakRSS: 1400 * mib},
			{Label: "cc", Count: 40, CPUMS: 41000, PeakRSS: 210 * mib},
			{Label: "cargo test", Count: 1, CPUMS: 2000, PeakRSS: 90 * mib},
		},
		Timeline: record.Timeline{StepMS: 400, Cores: cores, Private: mem},
	}
	after := *before
	after.ID, after.Name = "r9q4xt", "change"
	after.Start = time.Date(2026, 11, 9, 22, 40, 0, 0, time.UTC)
	after.WallMS, after.UserMS = 61000, 660000
	after.PeakPrivate, after.LargestRSS, after.LargestName = 11776*mib, 4506*mib, "pulse-test"
	after.Stall = &record.Stall{CPUMS: 9800, MemoryMS: 1200, IOMS: 600}
	after.Other = record.Other{Known: true, AvgCores: 2.1, PeakCores: 2.6, Games: []string{"Hades"}, Disturbed: true}
	failed := &record.Run{
		V: 1, ID: "rmmmm2", Command: []string{"make", "-j8"}, Cwd: "/home/u/other",
		Start: time.Date(2026, 11, 7, 9, 5, 0, 0, time.UTC), WallMS: 1200, Exit: 2, Method: "tree", NCPU: 16,
		UserMS: 2100, SystemMS: 300, PeakPrivate: 300 * mib, SampleMS: 500, Samples: 3, LargestRSS: 310 * mib,
		Other:    record.Other{Known: true, AvgCores: 5.5, PeakCores: 5.5, Disturbed: true},
		Timeline: record.Timeline{StepMS: 500, Cores: []float32{1.5, 2.2, 1.1}, Private: []uint32{100, 300, 250}},
	}
	return []*record.Run{failed, before, &after}
}

// TestScreens holds every screen to its golden file, at one size.
func TestScreens(t *testing.T) {
	for _, c := range []struct {
		name string
		keys []string
	}{
		{"overview", []string{"1"}},
		{"groups", []string{"2"}},
		{"group-open", []string{"2", "enter"}},
		{"processes", []string{"2", "v", "s"}},
		{"tree", []string{"2", "v", "v", "G"}},
		{"filter", []string{"2", "/", "r", "u", "s", "t", "enter"}},
		{"end", []string{"2", "v", "x"}},
		{"help", []string{"?"}},
		{"agents", []string{"3"}},
		{"runs", []string{"4"}},
		{"compare", []string{"4", "space", "down", "space"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := open(t, 120, 38)
			s.keys(c.keys...)
			s.golden(c.name)
		})
	}
}

// TestSizes draws every tab, in every theme's turn, on small, odd and
// huge terminals: each frame must fit exactly.
func TestSizes(t *testing.T) {
	for _, size := range [][2]int{{72, 20}, {80, 24}, {97, 31}, {103, 25}, {110, 30}, {160, 50}, {261, 71}} {
		s := open(t, size[0], size[1])
		for _, keys := range [][]string{{"1"}, {"2"}, {"enter"}, {"v"}, {"v", "G"}, {"x"}, {"esc", "?"}, {"esc", "3"}, {"4"}, {"space", "down", "space"}} {
			s.keys(keys...)
			s.keys("t")
			s.frame()
			if t.Failed() {
				t.Fatalf("at %d×%d after %v", size[0], size[1], keys)
			}
		}
	}
	s := open(t, 60, 12)
	if got := ansi.Strip(s.m.View().Content); !strings.Contains(got, "pulse needs 72×20 cells, this is 60×12") {
		t.Errorf("a small terminal shows:\n%s", got)
	}
}

// TestEmptyMachine: no GPU, no T3, no recorded runs, nothing to select.
func TestEmptyMachine(t *testing.T) {
	mach := collecttest.New(2, 4096*mib, 2048*mib)
	mach.Add(collecttest.P{PID: 1, Comm: "init", Args: []string{"/sbin/init"}, Start: 1, RSS: 4 * mib})
	m := newModel(Options{Roots: mach.Roots(), UID: 1000, Config: cli.Config{Thresholds: query.DefaultThresholds},
		Store: record.Store{Dir: filepath.Join(t.TempDir(), "runs")},
		Now:   func() time.Time { return time.Unix(collecttest.BootTime+60, 0) }})
	s := &screen{t: t, m: m, mach: mach}
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 26})
	m.Update(m.loadRuns()())
	s.reading()
	mach.Run(100, nil)
	s.reading()
	for _, k := range []string{"1", "2", "v", "v", "x", "esc", "3", "down", "4", "space", "down"} {
		s.keys(k)
		s.frame()
	}
	s.keys("3")
	if got := s.frame(); !strings.Contains(got, "T3 Code runs no agents") {
		t.Errorf("the agents tab without T3 shows:\n%s", got)
	}
}

func TestEndProcess(t *testing.T) {
	s := open(t, 120, 38)
	// A group of several processes doesn't say which one is meant.
	s.keys("2", "down", "x")
	if s.m.confirm != nil || !strings.Contains(s.frame(), "pick one process") {
		t.Fatal("x on a group of many asked, or said nothing")
	}
	// Opened, the game's group shows its processes by CPU: Hades.exe first.
	s.keys("enter", "down", "x")
	if s.m.confirm == nil || !strings.Contains(s.frame(), "End this process?") {
		t.Fatal("x didn't ask")
	}
	s.keys("esc")
	if s.m.confirm != nil || len(s.killed) != 0 {
		t.Fatalf("esc left the question open or sent %v", s.killed)
	}

	s.keys("x", "enter")
	if len(s.killed) != 1 || s.killed[0] != "201 terminated" {
		t.Fatalf("enter sent %v; want SIGTERM to 201", s.killed)
	}
	s.keys("x", "K")
	if len(s.killed) != 2 || s.killed[1] != "201 killed" {
		t.Fatalf("K sent %v; want SIGKILL to 201", s.killed)
	}

	// The process ends while the question is open, and its pid goes to
	// another: nothing is sent.
	s.keys("x")
	s.mach.Remove(201)
	s.mach.Add(collecttest.P{PID: 201, PPID: 1, Comm: "innocent", Args: []string{"innocent"}, Start: 999999, RSS: mib})
	s.second()
	s.keys("enter")
	if len(s.killed) != 2 {
		t.Errorf("a reused pid was sent a signal: %v", s.killed)
	}
	if got := s.frame(); !strings.Contains(got, "is gone") {
		t.Errorf("no word about the process being gone:\n%s", got)
	}
}

func TestKeysAndMouse(t *testing.T) {
	s := open(t, 120, 38)
	s.keys("tab")
	if s.m.tab != tabProcs {
		t.Errorf("tab went to tab %d", s.m.tab)
	}
	// A click on a tab's name in the header.
	x := strings.Index(strings.Split(s.frame(), "\n")[0], "Runs")
	s.m.Update(tea.MouseClickMsg{X: lipgloss.Width(strings.Split(s.frame(), "\n")[0][:x]) + 1, Y: 0, Button: tea.MouseLeft})
	if s.m.tab != tabRuns {
		t.Errorf("a click on Runs went to tab %d", s.m.tab)
	}
	s.keys("2")
	first := s.m.procs.cursor
	s.m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if s.m.procs.cursor == first || s.m.procs.index != 3 {
		t.Errorf("the wheel moved the cursor to line %d", s.m.procs.index)
	}
	// The cursor stays on its group while the order changes under it.
	s.keys("g", "down")
	on := s.m.procs.cursor
	s.keys("s")
	if s.m.procs.sort != "mem" || s.m.procs.cursor != on {
		t.Errorf("after s: sorted by %s, cursor on %s, was on %s", s.m.procs.sort, s.m.procs.cursor, on)
	}
	// The filter narrows the list, esc clears it.
	all := len(s.m.procs.rows)
	s.keys("/", "f", "i", "r", "e", "enter")
	if n := len(s.m.procs.rows); n != 1 || s.m.procs.filter != "fire" {
		t.Errorf("filter %q leaves %d lines", s.m.procs.filter, n)
	}
	s.keys("esc")
	if len(s.m.procs.rows) != all {
		t.Errorf("esc left %d of %d lines", len(s.m.procs.rows), all)
	}
	// q inside the filter is a letter, outside it quits.
	s.keys("/", "q")
	if s.m.procs.filter != "q" {
		t.Errorf("q in the filter gave %q", s.m.procs.filter)
	}
	s.keys("esc")
	if _, cmd := s.m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd == nil {
		t.Error("q didn't quit")
	}
}

// TestState: the theme and the tab come back at the next start.
func TestState(t *testing.T) {
	s := open(t, 120, 38)
	s.keys("3", "t", "t")
	again := newModel(s.m.o)
	if again.tab != tabAgents || again.theme.Name != Themes[2].Name {
		t.Errorf("came back on tab %d with theme %s", again.tab, again.theme.Name)
	}
	// The config file's theme is the one to start with.
	o := s.m.o
	o.StateFile, o.Config.Theme = "", "gruvbox"
	if got := newModel(o).theme.Name; got != "gruvbox" {
		t.Errorf("theme = gruvbox gave %s", got)
	}
}

// TestLumenTheme: inside lumen the monitor starts in lumen's colours,
// unless the config file or the user chose others.
func TestLumenTheme(t *testing.T) {
	s := open(t, 120, 38)
	o := s.m.o
	o.Terminal = "lumen"
	m := newModel(o)
	if m.theme.Name != "lumen" || m.theme.Bg != nil {
		t.Fatalf("in lumen: theme %s, background %v; want lumen on the terminal's own", m.theme.Name, m.theme.Bg)
	}
	// Changing the tab remembers the tab, not a theme nobody picked.
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 38})
	m.Update(tea.KeyPressMsg{Code: '2', Text: "2"})
	o.Terminal = ""
	if again := newModel(o); again.tab != tabProcs || again.theme.Name != Themes[0].Name {
		t.Errorf("outside lumen afterwards: tab %d, theme %s", again.tab, again.theme.Name)
	}
	// A theme picked with t stays, in lumen too.
	m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	o.Terminal = "lumen"
	if again := newModel(o); again.theme.Name != Themes[0].Name {
		t.Errorf("after t in lumen: theme %s, want %s", again.theme.Name, Themes[0].Name)
	}
	o.StateFile, o.Config.Theme = "", "tokyo"
	if got := newModel(o).theme.Name; got != "tokyo" {
		t.Errorf("theme = tokyo in lumen gave %s", got)
	}
	o.Config.Theme, o.NoColor = "", true
	if got := newModel(o).theme.Name; got != "terminal" {
		t.Errorf("NO_COLOR in lumen gave %s", got)
	}
}

func TestGraphs(t *testing.T) {
	m := &model{theme: themeNamed("terminal")}
	vals := []float64{0, 0.25, 0.5, 1}
	up := ansi.Strip(strings.Join(m.area(vals, 6, 2, m.theme.CPU), "|"))
	if up != "     █|▁▁▁▄██" {
		t.Errorf("area: %q", up)
	}
	down := ansi.Strip(strings.Join(m.areaDown(vals, 6, 2, m.theme.CPU), "|"))
	if down != "   ▀██|     █" {
		t.Errorf("areaDown: %q", down)
	}
}
