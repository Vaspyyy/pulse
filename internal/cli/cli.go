// Package cli is Pulse's text interface: short reports within a line
// budget, each ending in up to three next: lines that run as printed.
package cli

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Vaspyyy/pulse/collect"
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
	Stdout io.Writer
	Stderr io.Writer
	// Sleep waits between the two readings.
	Sleep func(time.Duration)
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// SteamName returns a Steam app's name, "" when unknown. May be nil.
	SteamName func(appID string) string
	// Threads returns what T3 says about threads, by id; ids it doesn't
	// answer for in time are left out. May be nil.
	Threads func(ids []string) map[string]t3.Thread
}

const usage = `pulse: what is using this machine

  pulse                       the card: load verdict, machine, top groups
  pulse load                  one line: quiet, or busy with the reasons
  pulse top [--by cpu|mem]    the heaviest groups of processes
  pulse tree <handle|pid|name>  a process and what it started, with totals
  pulse agents                T3's agents: thread, CPU, memory, limits

  -b <lines>   line budget (default 12)

A handle is a pid, a dot and three letters (4821.kfx): it names one
process for good, and the pid in front works with kill and strace.
Run pulse load before timing measurements: if it says busy, skip them.
`

type options struct {
	budget int
	by     string
	args   []string
}

// Run runs one command and returns the exit code.
func Run(args []string, env Env) int {
	o, err := parse(args)
	if err != nil {
		fmt.Fprintf(env.Stderr, "pulse: %v\n", err)
		return 2
	}
	cmd := ""
	if len(o.args) > 0 {
		cmd, o.args = o.args[0], o.args[1:]
	}
	var r report
	switch cmd {
	case "help":
		fmt.Fprint(env.Stdout, usage)
		return 0
	case "":
		r = card(view(env))
	case "load":
		r.body = []string{view(env).Load(query.DefaultThresholds).Line()}
	case "top":
		r = top(view(env), o.by)
	case "tree":
		if len(o.args) != 1 {
			fmt.Fprintln(env.Stderr, "pulse: tree takes one handle, pid or name")
			return 2
		}
		r, err = tree(view(env), o.args[0])
	case "agents":
		r, err = agents(view(env), env)
	default:
		fmt.Fprintf(env.Stderr, "pulse: unknown command %q; pulse help lists them\n", cmd)
		return 2
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "pulse: %v\n", err)
		return 1
	}
	r.print(env.Stdout, o.budget)
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
			o.budget = n
		case "--by":
			v, err := value()
			if err != nil {
				return o, err
			}
			if v != "cpu" && v != "mem" {
				return o, fmt.Errorf("--by %s: want cpu or mem", v)
			}
			o.by = v
		case "-A":
			// The agent card is what Pulse prints anyway until the TUI exists.
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

// view takes two readings a window apart.
func view(env Env) *query.View {
	c := collect.New(env.Roots)
	if env.Now != nil {
		c.Now = env.Now
	}
	first := c.Snapshot()
	env.Sleep(Window)
	return query.NewView(first, c.Snapshot(), env.Roots, query.Options{UID: env.UID, SteamName: env.SteamName})
}

// report is a command's output: the body, then the next: lines.
type report struct {
	body []string
	next []next
}

type next struct{ cmd, why string }

// print writes the report within budget lines. When the body doesn't fit,
// its end is cut and a line says how to see all of it.
func (r report) print(w io.Writer, budget int) {
	r.next = r.next[:min(len(r.next), 3)]
	body := r.body
	// Whatever the budget, the first line and the note about the cut stay.
	if room := max(budget-len(r.next), 2); len(body) > room {
		keep := room - 1
		body = append(body[:keep:keep], fmt.Sprintf("… %d more lines; -b %d shows all", len(r.body)-keep, len(r.body)+len(r.next)))
	}
	for _, l := range body {
		fmt.Fprintln(w, strings.TrimRight(l, " "))
	}
	width := 0
	for _, n := range r.next {
		width = max(width, len(n.cmd))
	}
	for _, n := range r.next {
		fmt.Fprintf(w, "next: %-*s   %s\n", width, n.cmd, n.why)
	}
}

// machine is the one-line summary of CPU and memory.
func machine(v *query.View) string {
	m := v.Snap.Mem
	pressure := "no pressure"
	if p := v.Snap.Pressure.Memory; p >= 1 {
		pressure = fmt.Sprintf("pressure %.0f%%", p)
	}
	s := fmt.Sprintf("cpu %.1f/%d cores · mem %.1f/%.1f GB, %s", v.Cores, v.NCPU,
		gb(m.Total-min(m.Available, m.Total)), gb(m.Total), pressure)
	if used := m.SwapTotal - min(m.SwapFree, m.SwapTotal); used > 0 {
		s += " · swap " + query.Bytes(used)
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

func groupRow(g query.Group) []string {
	return []string{g.Label, query.Cores(g.Total.Cores), query.Bytes(g.Total.Private)}
}

// shown drops groups that use nothing worth a line.
func shown(groups []query.Group) []query.Group {
	var out []query.Group
	for _, g := range groups {
		if g.Total.Cores >= 0.05 || g.Total.Private >= 1<<20 {
			out = append(out, g)
		}
	}
	return out
}

// bare is a group's label without its count.
func bare(g query.Group) string {
	return strings.TrimSuffix(g.Label, fmt.Sprintf(" ×%d", g.Total.Count))
}

func card(v *query.View) report {
	r := report{body: []string{v.Load(query.DefaultThresholds).Line(), machine(v)}}
	groups := shown(v.Groups("cpu"))
	groups = groups[:min(len(groups), 5)]
	var rows [][]string
	for _, g := range groups {
		rows = append(rows, groupRow(g))
	}
	for i, l := range table(rows, "lrr") {
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
	for _, g := range shown(v.Groups(by)) {
		row := groupRow(g)
		if g.Lead != nil {
			row = append(row, v.Handle(g.Lead))
			if len(r.next) == 0 {
				r.next = append(r.next, next{"pulse tree " + v.Handle(g.Lead), "the processes of " + bare(g)})
			}
		}
		rows = append(rows, row)
	}
	r.body = append(r.body, table(rows, "lrrl")...)
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
		r.body = append(r.body, fmt.Sprintf("1 process, %s, %s resident, %s of it private", query.Cores(sum.Cores), query.Bytes(root.RSS), query.Bytes(root.Private())))
		return r, nil
	}
	r.body = append(r.body, fmt.Sprintf("total %d processes, %s, %s private memory (shared libraries and files not summed)",
		sum.Count, query.Cores(sum.Cores), query.Bytes(sum.Private)))
	var rows [][]string
	var walk func(n *query.Node, prefix string)
	walk = func(n *query.Node, prefix string) {
		for i, c := range n.Children {
			branch, below := "├─ ", "│  "
			if i == len(n.Children)-1 {
				branch, below = "└─ ", "   "
			}
			rows = append(rows, nodeRow(v, c, prefix+branch))
			walk(c, prefix+below)
		}
	}
	walk(n, "")
	r.body = append(r.body, table(rows, "lrrl")...)
	return r, nil
}

// nodeRow is a tree line. One process shows its resident memory, as every
// monitor does; merged processes show their private memory, which adds up.
func nodeRow(v *query.View, n *query.Node, prefix string) []string {
	if len(n.Procs) > 1 {
		return []string{fmt.Sprintf("%s%s ×%d", prefix, n.Label, len(n.Procs)), query.Cores(n.Own.Cores), query.Bytes(n.Own.Private), ""}
	}
	p := n.Procs[0]
	return []string{prefix + n.Label, query.Cores(p.Cores), query.Bytes(p.RSS), v.Handle(p)}
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
