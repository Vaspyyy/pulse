// Package cli is Pulse's text interface: short reports within a line
// budget, each ending in up to three next: lines that run as printed.
package cli

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Vaspyyy/pulse/collect"
	"github.com/Vaspyyy/pulse/collect/gpu"
	"github.com/Vaspyyy/pulse/collect/t3"
	"github.com/Vaspyyy/pulse/internal/query"
)

// Window is how far apart a command's two readings are: rates need two.
const Window = 250 * time.Millisecond

const defaultBudget = 12

// Env is what a command runs on.
type Env struct {
	Roots  collect.Roots
	UID    int
	Stdin  io.Reader // for the command pulse run starts, and pulse watch's pids
	Stdout io.Writer
	Stderr io.Writer
	// Whether Stdout and Stderr are terminals: only a terminal gets links
	// and notifications.
	StdoutTerminal, StderrTerminal bool
	// Getenv reads the environment; StateDir is where runs are kept.
	Getenv   func(string) string
	StateDir string
	// Sleep waits between the two readings.
	Sleep func(time.Duration)
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// SteamName returns a Steam app's name, "" when unknown. May be nil.
	SteamName func(appID string) string
	// GPU reads the GPUs. May be nil: then there is no GPU reading.
	GPU func() []gpu.Device
	// Config is the config file's content, "" when there is none, and
	// ConfigPath where it was looked for.
	Config, ConfigPath string
	// Threads returns what T3 says about threads, by id; ids it doesn't
	// answer for in time are left out. May be nil.
	Threads func(ids []string) map[string]t3.Thread
}

const usage = `pulse: what is using this machine

  pulse                       on a terminal: the full-screen monitor (? shows its keys)
  pulse -A                    the card: load verdict, machine, top groups; it is
                              also what pulse prints when its output is no terminal
  pulse load                  one line: quiet, or busy with the reasons
  pulse top [--by cpu|mem|gpu]  the heaviest groups of processes
  pulse tree <handle|pid|name>  a process and what it started, with totals
  pulse agents                T3's agents: thread, CPU, memory, limits
  pulse watch [--json] [--every <seconds>] [--pids <pid,pid>] [-n <readings>]
                              a reading every 3 seconds, one line each, until
                              stopped: the load, and what each pid runs and uses

  pulse run [-n name] -- <command>  run a command and record what it uses
  pulse runs                  the recorded runs
  pulse show [run]            one run: numbers, processes, timeline
  pulse diff [run] [run]      two runs side by side

A run is named by its id (r7k2mq), the name given with -n, or "last".
pulse diff alone compares the newest run with the one of the same
command before it.

  -b <lines>   line budget (default 12)

Busy means: a game runs, or CPU, GPU or memory are loaded. The limits and
more game markers can be set in the config file (see pulse help config).

A handle is a pid, a dot and three letters (4821.kfx): it names one
process for good, and the pid in front works with kill and strace.
Run pulse load before timing measurements: if it says busy, skip them.
`

type options struct {
	budget    int
	budgetSet bool
	by        string
	args      []string
}

// Run runs one command and returns the exit code.
func Run(args []string, env Env) int {
	if len(args) > 0 && args[0] == "run" {
		// Everything after run's own flags is the command's, untouched.
		cfg, problems := ParseConfig(env.Config)
		for _, p := range problems {
			fmt.Fprintf(env.Stderr, "pulse: %s: %s\n", env.ConfigPath, p)
		}
		return runCommand(args[1:], env, cfg)
	}
	if len(args) > 0 && args[0] == "watch" {
		cfg, problems := ParseConfig(env.Config)
		for _, p := range problems {
			fmt.Fprintf(env.Stderr, "pulse: %s: %s\n", env.ConfigPath, p)
		}
		return watch(args[1:], env, cfg)
	}
	o, err := parse(args)
	if err != nil {
		fmt.Fprintf(env.Stderr, "pulse: %v\n", err)
		return 2
	}
	cmd := ""
	if len(o.args) > 0 {
		cmd, o.args = o.args[0], o.args[1:]
	}
	cfg, problems := ParseConfig(env.Config)
	for _, p := range problems {
		fmt.Fprintf(env.Stderr, "pulse: %s: %s\n", env.ConfigPath, p)
	}
	// One view at most, and the links ask it which handles are real.
	var seen *query.View
	view := func(env Env, cfg Config) *query.View {
		if seen == nil {
			seen = view(env, cfg)
		}
		return seen
	}
	var r report
	switch cmd {
	case "help":
		if len(o.args) == 1 && o.args[0] == "config" {
			fmt.Fprintf(env.Stdout, configHelp, env.ConfigPath)
			return 0
		}
		fmt.Fprint(env.Stdout, usage)
		return 0
	case "":
		r = card(view(env, cfg), cfg)
	case "load":
		r.body = []string{view(env, cfg).Load(cfg.Thresholds).Line()}
	case "top":
		r = top(view(env, cfg), o.by)
	case "tree":
		if len(o.args) != 1 {
			fmt.Fprintln(env.Stderr, "pulse: tree takes one handle, pid or name")
			return 2
		}
		r, err = tree(view(env, cfg), o.args[0])
	case "agents":
		r, err = agents(view(env, cfg), env)
	case "runs":
		r = runs(env)
	case "show":
		ref := "last"
		if len(o.args) > 1 {
			fmt.Fprintln(env.Stderr, "pulse: show takes one run")
			return 2
		}
		if len(o.args) == 1 {
			ref = o.args[0]
		}
		if !o.budgetSet {
			o.budget = 24
		}
		r, err = show(env, ref)
	case "diff":
		// Room for every row and note: a cut-off note would mislead.
		if !o.budgetSet {
			o.budget = 16
		}
		r, err = diff(env, o.args)
	default:
		fmt.Fprintf(env.Stderr, "pulse: unknown command %q; pulse help lists them\n", cmd)
		return 2
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "pulse: %v\n", err)
		return 1
	}
	r.print(env.Stdout, o.budget, env.links(cfg, env.StdoutTerminal, seen, r.ids))
	return 0
}

// parse reads flags wherever they stand, before or after the command.
func parse(args []string) (options, error) {
	o := options{budget: defaultBudget, by: "cpu"}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, val, hasVal := strings.Cut(a, "=")
		value := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", name)
			}
			i++
			return args[i], nil
		}
		switch name {
		case "-b", "--budget":
			v, err := value()
			if err != nil {
				return o, err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return o, fmt.Errorf("-b %s: want a number of lines", v)
			}
			o.budget, o.budgetSet = n, true
		case "--by":
			v, err := value()
			if err != nil {
				return o, err
			}
			if v != "cpu" && v != "mem" && v != "gpu" {
				return o, fmt.Errorf("--by %s: want cpu, mem or gpu", v)
			}
			o.by = v
		case "-A":
			// The card on a terminal too, where pulse alone opens the monitor.
		case "-h", "--help":
			o.args = []string{"help"}
			return o, nil
		default:
			if strings.HasPrefix(a, "-") && len(a) > 1 {
				return o, fmt.Errorf("unknown flag %s", a)
			}
			o.args = append(o.args, a)
		}
	}
	return o, nil
}

// view takes two readings a window apart, and reads the GPUs meanwhile:
// their driver averages over its own sample period, so one reading does.
func view(env Env, cfg Config) *query.View {
	c := collect.New(env.Roots)
	if env.Now != nil {
		c.Now = env.Now
	}
	c.Markers = append(slices.Clone(c.Markers), cfg.GameEnv...)
	gpus := make(chan []gpu.Device, 1)
	go func() {
		if env.GPU == nil {
			gpus <- nil
			return
		}
		gpus <- env.GPU()
	}()
	first := c.Snapshot()
	env.Sleep(Window)
	second := c.Snapshot()
	return query.NewView(first, second, env.Roots, query.Options{
		UID: env.UID, SteamName: env.SteamName, GPUs: <-gpus,
		GameEnv: cfg.GameEnv, GameProcess: cfg.GameProcess,
	})
}

// report is a command's output: the body, then the next: lines.
type report struct {
	body []string
	next []next
	ids  []string // the recorded runs the body names
}

// links turns what a report names into links that put a command at the
// prompt when clicked: a handle shows its tree, a run its record, a next:
// line itself (docs/design.md, "In lumen").
type links struct {
	view *query.View // tells a handle from text that only looks like one; may be nil
	ids  []string    // the run ids that are real
}

// links returns the linker for a stream, nil where links don't belong:
// anything but a terminal that understands them. That is lumen, or
// whatever the config file says with links = on.
func (env Env) links(cfg Config, terminal bool, v *query.View, ids []string) *links {
	on := cfg.Links == "on" || cfg.Links != "off" && env.Getenv != nil && env.Getenv("TERM_PROGRAM") == "lumen"
	if !on || !terminal {
		return nil
	}
	return &links{view: v, ids: ids}
}

// A handle (4821.kfx) or a run id (r7k2mq), by their looks.
var linkable = regexp.MustCompile(`\b(?:[0-9]+\.[a-hjkmnp-z]{3}|r[a-hjkmnp-z2-9]{5})\b`)

// promptLink is text as a link that puts cmd at the prompt.
func promptLink(cmd, text string) string {
	return "\x1b]8;;lumen-prompt:" + url.PathEscape(cmd) + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// line links the handles and run ids in a line of a report.
func (l *links) line(s string) string {
	if l == nil {
		return s
	}
	return linkable.ReplaceAllStringFunc(s, func(tok string) string {
		if tok[0] == 'r' {
			if slices.Contains(l.ids, tok) {
				return promptLink("pulse show "+tok, tok)
			}
			return tok
		}
		if l.view == nil {
			return tok
		}
		if _, err := l.view.Find(tok); err != nil {
			return tok
		}
		return promptLink("pulse tree "+tok, tok)
	})
}

// command links a next: line's command to itself.
func (l *links) command(cmd string) string {
	if l == nil || strings.ContainsAny(cmd, "<>") { // a placeholder isn't a command yet
		return cmd
	}
	return promptLink(cmd, cmd)
}

type next struct{ cmd, why string }

// print writes the report within budget lines. When the body doesn't fit,
// its end is cut and a line says how to see all of it.
func (r report) print(w io.Writer, budget int, l *links) {
	r.next = r.next[:min(len(r.next), 3)]
	body := r.body
	// Whatever the budget, the first line and the note about the cut stay.
	if room := max(budget-len(r.next), 2); len(body) > room {
		keep := room - 1
		body = append(body[:keep:keep], fmt.Sprintf("… %d more lines; -b %d shows all", len(r.body)-keep, len(r.body)+len(r.next)))
	}
	for _, line := range body {
		fmt.Fprintln(w, l.line(strings.TrimRight(line, " ")))
	}
	width := 0
	for _, n := range r.next {
		width = max(width, len(n.cmd))
	}
	for _, n := range r.next {
		fmt.Fprintf(w, "next: %s%s   %s\n", l.command(n.cmd), strings.Repeat(" ", width-len(n.cmd)), n.why)
	}
}

// machine is the one-line summary of CPU and memory.
func machine(v *query.View) string {
	m := v.Snap.Mem
	pressure := "no pressure"
	if p := v.Snap.Pressure.Memory; p >= 1 {
		pressure = fmt.Sprintf("pressure %.0f%%", p)
	}
	s := fmt.Sprintf("cpu %.1f/%d cores", v.Cores, v.NCPU)
	if t := v.Snap.CPUTemp; t > 0 {
		s += fmt.Sprintf(" %.0f°C", t)
	}
	s += fmt.Sprintf(" · mem %.1f/%.1f GB, %s", gb(m.Total-min(m.Available, m.Total)), gb(m.Total), pressure)
	if used := m.SwapTotal - min(m.SwapFree, m.SwapTotal); used > 0 {
		s += " · swap " + query.Bytes(used)
	}
	for _, d := range v.GPUs {
		s += " · gpu " + d.Name
		if d.Util >= 0 {
			s += fmt.Sprintf(" %.0f%%", d.Util)
		}
		if d.Power > 0 && d.PowerLimit > 0 {
			s += fmt.Sprintf(" at %.0f/%.0f W", d.Power, d.PowerLimit)
		}
		if d.MemTotal > 0 {
			s += fmt.Sprintf(", %.1f/%.1f GB", gb(d.MemUsed), gb(d.MemTotal))
		}
		if d.Temp > 0 {
			s += fmt.Sprintf(", %.0f°C", d.Temp)
		}
	}
	return s
}

func gb(b uint64) float64 { return float64(b) / (1 << 30) }

// table lines up rows of cells. align has a letter per column: l for
// left, r for right.
func table(rows [][]string, align string) []string {
	var width []int
	for _, r := range rows {
		for i, c := range r {
			if i >= len(width) {
				width = append(width, 0)
			}
			width[i] = max(width[i], len([]rune(c)))
		}
	}
	out := make([]string, len(rows))
	for ri, r := range rows {
		var b strings.Builder
		for i, c := range r {
			pad := strings.Repeat(" ", width[i]-len([]rune(c)))
			if i > 0 {
				b.WriteString("  ")
			}
			if i < len(align) && align[i] == 'r' {
				b.WriteString(pad + c)
			} else {
				b.WriteString(c + pad)
			}
		}
		out[ri] = b.String()
	}
	return out
}

// columns says which GPU columns a table has: only those that hold
// something in at least one of its rows.
type columns struct{ gpu, vram bool }

func (c *columns) see(t query.Total) {
	c.gpu = c.gpu || t.GPU >= 1
	c.vram = c.vram || t.VRAM >= 1<<20
}

// cells are a row's numbers: CPU, memory, and the GPU columns in use.
func (c columns) cells(cores float64, mem uint64, t query.Total) []string {
	out := []string{query.Cores(cores), query.Bytes(mem)}
	if c.gpu {
		cell := ""
		if t.GPU >= 1 {
			cell = fmt.Sprintf("%.0f%% gpu", t.GPU)
		}
		out = append(out, cell)
	}
	if c.vram {
		cell := ""
		if t.VRAM >= 1<<20 {
			cell = query.Bytes(t.VRAM) + " vram"
		}
		out = append(out, cell)
	}
	return out
}

// align is the table alignment for a label, the cells, and trailing
// left-aligned columns.
func (c columns) align(trailing string) string {
	return "l" + strings.Repeat("r", len(c.cells(0, 0, query.Total{}))) + trailing
}

func groupColumns(groups []query.Group) columns {
	var c columns
	for _, g := range groups {
		c.see(g.Total)
	}
	return c
}

func groupRow(g query.Group, c columns) []string {
	return append([]string{g.Label}, c.cells(g.Total.Cores, g.Total.Private, g.Total)...)
}

// shown drops groups that use nothing worth a line.
func shown(groups []query.Group) []query.Group {
	var out []query.Group
	for _, g := range groups {
		if g.Total.Cores >= 0.05 || g.Total.Private >= 1<<20 || g.Total.GPU >= 1 || g.Total.VRAM >= 1<<20 {
			out = append(out, g)
		}
	}
	return out
}

// bare is a group's label without its count.
func bare(g query.Group) string {
	return strings.TrimSuffix(g.Label, fmt.Sprintf(" ×%d", g.Total.Count))
}

func card(v *query.View, cfg Config) report {
	r := report{body: []string{v.Load(cfg.Thresholds).Line(), machine(v)}}
	groups := shown(v.Groups("cpu"))
	groups = groups[:min(len(groups), 5)]
	cols := groupColumns(groups)
	var rows [][]string
	for _, g := range groups {
		rows = append(rows, groupRow(g, cols))
	}
	for i, l := range table(rows, cols.align("")) {
		prefix := "     "
		if i == 0 {
			prefix = "top  "
		}
		r.body = append(r.body, prefix+l)
	}
	for _, g := range groups {
		if g.Lead != nil && g.Total.Count > 1 {
			r.next = append(r.next, next{"pulse tree " + v.Handle(g.Lead), fmt.Sprintf("the %d processes of %s", g.Total.Count, bare(g))})
			break
		}
	}
	if v.T3 != nil && len(v.T3.Scopes) > 0 {
		r.next = append(r.next, next{"pulse agents", fmt.Sprintf("the %d agents, by thread", len(v.T3.Scopes))})
	}
	r.next = append(r.next, next{"pulse top --by mem", "the biggest memory users"})
	return r
}

func top(v *query.View, by string) report {
	r := report{body: []string{machine(v)}}
	var rows [][]string
	groups := shown(v.Groups(by))
	cols := groupColumns(groups)
	for _, g := range groups {
		row := groupRow(g, cols)
		if g.Lead != nil {
			row = append(row, v.Handle(g.Lead))
			if len(r.next) == 0 {
				r.next = append(r.next, next{"pulse tree " + v.Handle(g.Lead), "the processes of " + bare(g)})
			}
		}
		rows = append(rows, row)
	}
	r.body = append(r.body, table(rows, cols.align("l"))...)
	if by == "cpu" {
		r.next = append(r.next, next{"pulse top --by mem", "sorted by memory instead"})
	}
	return r
}

func tree(v *query.View, arg string) (report, error) {
	found, err := v.Find(arg)
	if err != nil {
		return report{}, err
	}
	if len(found) > 1 {
		// A name that several separate processes have: list them.
		r := report{body: []string{fmt.Sprintf("%d separate processes are named %s:", len(found), arg)}}
		var rows [][]string
		for _, p := range found {
			t := v.Tree(p).Sum()
			rows = append(rows, []string{v.Handle(p), query.Label(p), query.Cores(t.Cores), query.Bytes(t.Private), plural(t.Count, "process", "processes")})
		}
		r.body = append(r.body, table(rows, "llrrl")...)
		r.next = []next{{"pulse tree " + v.Handle(found[0]), "the heaviest of them"}}
		return r, nil
	}
	root := found[0]
	n := v.Tree(root)
	sum := n.Sum()
	head := fmt.Sprintf("%s %s, running %s", strings.Join(clipArgs(root.Args, query.Name(root)), " "), v.Handle(root),
		age(v.Snap.At.Sub(v.Started(root))))
	if root.Scope >= 0 {
		head += ", in agent scope " + v.T3.Scopes[root.Scope].Unit
	}
	r := report{body: []string{head}}
	if sum.Count == 1 {
		line := fmt.Sprintf("1 process, %s, %s resident, %s of it private", query.Cores(sum.Cores), query.Bytes(root.RSS), query.Bytes(root.Private()))
		if sum.GPU >= 1 {
			line += fmt.Sprintf(", %.0f%% of the GPU", sum.GPU)
		}
		if sum.VRAM >= 1<<20 {
			line += ", " + query.Bytes(sum.VRAM) + " of GPU memory"
		}
		r.body = append(r.body, line)
		return r, nil
	}
	r.body = append(r.body, fmt.Sprintf("total %d processes, %s, %s private memory (shared libraries and files not summed)",
		sum.Count, query.Cores(sum.Cores), query.Bytes(sum.Private)))
	if sum.GPU >= 1 || sum.VRAM >= 1<<20 {
		gpu := query.Bytes(sum.VRAM) + " of GPU memory"
		if sum.GPU >= 1 {
			gpu = fmt.Sprintf("%.0f%% of the GPU, ", sum.GPU) + gpu
		}
		r.body = append(r.body, "      "+gpu)
	}
	var cols columns
	var see func(n *query.Node)
	see = func(n *query.Node) {
		for _, c := range n.Children {
			cols.see(c.Own)
			see(c)
		}
	}
	see(n)
	var rows [][]string
	var walk func(n *query.Node, prefix string)
	walk = func(n *query.Node, prefix string) {
		for i, c := range n.Children {
			branch, below := "├─ ", "│  "
			if i == len(n.Children)-1 {
				branch, below = "└─ ", "   "
			}
			rows = append(rows, nodeRow(v, c, cols, prefix+branch))
			walk(c, prefix+below)
		}
	}
	walk(n, "")
	r.body = append(r.body, table(rows, cols.align("l"))...)
	return r, nil
}

// nodeRow is a tree line. One process shows its resident memory, as every
// monitor does; merged processes show their private memory, which adds up.
func nodeRow(v *query.View, n *query.Node, cols columns, prefix string) []string {
	if len(n.Procs) > 1 {
		row := append([]string{fmt.Sprintf("%s%s ×%d", prefix, n.Label, len(n.Procs))}, cols.cells(n.Own.Cores, n.Own.Private, n.Own)...)
		return append(row, "")
	}
	p := n.Procs[0]
	row := append([]string{prefix + n.Label}, cols.cells(p.Cores, p.RSS, n.Own)...)
	return append(row, v.Handle(p))
}

// clipArgs shortens a command line for a heading.
func clipArgs(args []string, name string) []string {
	if len(args) == 0 {
		return []string{name}
	}
	out := []string{name}
	n := len(name)
	for _, a := range args[1:] {
		a = strings.Join(strings.Fields(a), " ") // a script given with -c has line breaks
		if n += len(a) + 1; n > 60 {
			return append(out, "…")
		}
		out = append(out, a)
	}
	return out
}

func age(d time.Duration) string {
	switch {
	case d < 0:
		return "0 s"
	case d < 2*time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < 2*time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

var errNoT3 = errors.New("T3 Code runs no agents in scopes here")
