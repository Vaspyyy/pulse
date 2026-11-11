package tui

import (
	"image/color"
	"sync"

	"charm.land/lipgloss/v2"
)

// Theme is the TUI's colours.
type Theme struct {
	Name string
	// Bg is the screen's background, nil to leave the terminal's own.
	Bg color.Color
	// Text, Sub and Dim are foregrounds, from strongest to faintest; Line
	// is for borders, and Sel the background of the selected row and the
	// empty part of meters.
	Text, Sub, Dim, Line, Sel color.Color
	// Accent marks what is active: the tab, a panel's title, a key.
	Accent, Accent2 color.Color
	// Ink is the text on an accent, nil for the background's colour.
	Ink             color.Color
	Good, Warn, Bad color.Color
	// The gradients colour graphs from their floor to their ceiling.
	CPU, Mem, GPU, Net, Disk []color.Color
}

func hex(s ...string) []color.Color {
	out := make([]color.Color, len(s))
	for i, c := range s {
		out[i] = lipgloss.Color(c)
	}
	return out
}

func c(s string) color.Color { return lipgloss.Color(s) }

// Themes are the built-in themes, the first being the default.
var Themes = []Theme{
	{
		Name: "mocha", Bg: c("#1e1e2e"),
		Text: c("#cdd6f4"), Sub: c("#a6adc8"), Dim: c("#6c7086"), Line: c("#45475a"), Sel: c("#313244"),
		Accent: c("#b4befe"), Accent2: c("#cba6f7"),
		Good: c("#a6e3a1"), Warn: c("#f9e2af"), Bad: c("#f38ba8"),
		CPU:  hex("#89b4fa", "#b4befe", "#cba6f7", "#f5c2e7"),
		Mem:  hex("#94e2d5", "#89dceb", "#74c7ec"),
		GPU:  hex("#a6e3a1", "#f9e2af", "#fab387"),
		Net:  hex("#74c7ec", "#89b4fa"),
		Disk: hex("#f5c2e7", "#cba6f7"),
	},
	{
		Name: "tokyo", Bg: c("#1a1b26"),
		Text: c("#c0caf5"), Sub: c("#a9b1d6"), Dim: c("#565f89"), Line: c("#3b4261"), Sel: c("#283457"),
		Accent: c("#7aa2f7"), Accent2: c("#bb9af7"),
		Good: c("#9ece6a"), Warn: c("#e0af68"), Bad: c("#f7768e"),
		CPU:  hex("#7aa2f7", "#bb9af7", "#f7768e"),
		Mem:  hex("#73daca", "#7dcfff"),
		GPU:  hex("#9ece6a", "#e0af68", "#ff9e64"),
		Net:  hex("#7dcfff", "#7aa2f7"),
		Disk: hex("#bb9af7", "#f7768e"),
	},
	{
		Name: "gruvbox", Bg: c("#282828"),
		Text: c("#ebdbb2"), Sub: c("#bdae93"), Dim: c("#928374"), Line: c("#504945"), Sel: c("#3c3836"),
		Accent: c("#fabd2f"), Accent2: c("#fe8019"),
		Good: c("#b8bb26"), Warn: c("#fabd2f"), Bad: c("#fb4934"),
		CPU:  hex("#83a598", "#d3869b", "#fb4934"),
		Mem:  hex("#8ec07c", "#83a598"),
		GPU:  hex("#b8bb26", "#fabd2f", "#fe8019"),
		Net:  hex("#83a598", "#8ec07c"),
		Disk: hex("#d3869b", "#fe8019"),
	},
	{
		Name: "latte", Bg: c("#eff1f5"),
		Text: c("#4c4f69"), Sub: c("#5c5f77"), Dim: c("#8c8fa1"), Line: c("#bcc0cc"), Sel: c("#dce0e8"),
		Accent: c("#1e66f5"), Accent2: c("#8839ef"),
		Good: c("#40a02b"), Warn: c("#df8e1d"), Bad: c("#d20f39"),
		CPU:  hex("#1e66f5", "#8839ef", "#ea76cb"),
		Mem:  hex("#179299", "#04a5e5"),
		GPU:  hex("#40a02b", "#df8e1d", "#fe640b"),
		Net:  hex("#04a5e5", "#1e66f5"),
		Disk: hex("#ea76cb", "#8839ef"),
	},
	{
		// The terminal's own sixteen colours, on its own background.
		Name: "terminal",
		Text: c("15"), Sub: c("7"), Dim: c("8"), Line: c("8"), Sel: c("0"),
		Accent: c("12"), Accent2: c("13"),
		Good: c("10"), Warn: c("11"), Bad: c("9"),
		CPU:  hex("12", "13"),
		Mem:  hex("14"),
		GPU:  hex("10", "11"),
		Net:  hex("14", "12"),
		Disk: hex("13"),
	},
	{
		// lumen's own look, a night sky with a warm lamp in it: its colours,
		// on the terminal's background, so that the blur shows through. It
		// is the default inside lumen.
		Name: "lumen",
		Text: c("#e4e1ee"), Sub: c("#b7b2cb"), Dim: c("#7b7693"), Line: c("#4b4662"), Sel: c("#322d45"),
		Accent: c("#ffc97a"), Accent2: c("#c9a4ff"), Ink: c("#12111a"),
		Good: c("#8fdfa8"), Warn: c("#ffd08a"), Bad: c("#f27a93"),
		CPU:  hex("#8cb4ff", "#c9a4ff", "#ffc97a"),
		Mem:  hex("#7fd9e6", "#8cb4ff"),
		GPU:  hex("#8fdfa8", "#ffd08a", "#ff9d7a"),
		Net:  hex("#7fd9e6", "#8cb4ff"),
		Disk: hex("#c9a4ff", "#f27a93"),
	},
}

// themeNamed returns the theme of that name, or the default.
func themeNamed(name string) Theme {
	for _, t := range Themes {
		if t.Name == name {
			return t
		}
	}
	return Themes[0]
}

// ramps remembers the gradients blended so far: blending is slow, and
// every frame asks for the same few.
var ramps sync.Map

type rampKey struct {
	first *color.Color // the gradient, by where its stops are
	stops int
	n     int
}

// ramp returns n colours along a gradient, from its first to its last.
// The result is shared: don't change it.
func ramp(stops []color.Color, n int) []color.Color {
	if n <= 0 || len(stops) == 0 {
		return nil
	}
	key := rampKey{&stops[0], len(stops), n}
	if r, ok := ramps.Load(key); ok {
		return r.([]color.Color)
	}
	var out []color.Color
	if len(stops) == 1 || n == 1 {
		out = make([]color.Color, n)
		for i := range out {
			out[i] = stops[0]
		}
	} else {
		out = lipgloss.Blend1D(n, stops...)
	}
	ramps.Store(key, out)
	return out
}

// heats are the themes' colours for a load, by theme name.
var heats sync.Map

// heat is the colour for a load from 0 to 1: good, then warn, then bad.
func (t Theme) heat(load float64) color.Color {
	steps, ok := heats.Load(t.Name)
	if !ok {
		steps = lipgloss.Blend1D(21, t.Good, t.Warn, t.Bad)
		heats.Store(t.Name, steps)
	}
	return steps.([]color.Color)[min(max(int(load*20+0.5), 0), 20)]
}
