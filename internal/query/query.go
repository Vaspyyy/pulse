// Package query turns snapshots into what Pulse shows: rates, groups,
// games, trees and the load verdict.
package query

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/Vaspyyy/pulse/collect"
	"github.com/Vaspyyy/pulse/collect/gpu"
	"github.com/Vaspyyy/pulse/collect/t3"
)

// View is a snapshot with rates, taken over a window between two readings.
type View struct {
	Snap   *collect.Snapshot
	Window time.Duration // 0 when there was only one reading: no rates
	NCPU   int
	Cores  float64    // CPUs busy over the window, machine-wide
	Procs  []Proc     // ascending by pid
	T3     *t3.Layout // nil when T3 runs no agents here
	Games  []Game
	GPUs   []gpu.Device // empty when there's no GPU reading

	byPID    map[int]int // index into Procs
	children map[int][]int
}

// Proc is a process with its rate.
type Proc struct {
	*collect.Process
	Cores float64 // CPUs it kept busy over the window
	GPU   float64 // percent of the GPUs it kept busy, as the driver sampled it
	VRAM  uint64  // bytes of GPU memory
	Game  int     // index into View.Games, or -1
	Scope int     // index into View.T3.Scopes of the agent it runs in, or -1
}

// Options tell NewView what it can't read from the snapshots.
type Options struct {
	UID int
	// SteamName returns a Steam app's name, "" when unknown. May be nil.
	SteamName func(appID string) string
	// GPUs is the GPU reading taken with cur, if any.
	GPUs []gpu.Device
	// GameEnv and GameProcess are more game markers than the built-in
	// ones: environment variables (which the collector must have kept)
	// and program names.
	GameEnv, GameProcess []string
}

// NewView compares prev with cur. prev may be nil: then there are no rates.
func NewView(prev, cur *collect.Snapshot, roots collect.Roots, o Options) *View {
	v := &View{Snap: cur, NCPU: max(len(cur.Cores), 1), byPID: map[int]int{}, children: map[int][]int{}}
	var perCPU float64 // ticks that passed on each CPU
	old := map[int]*collect.Process{}
	if prev != nil && cur.CPU.All > prev.CPU.All {
		perCPU = float64(cur.CPU.All-prev.CPU.All) / float64(v.NCPU)
		v.Window = time.Duration(perCPU / collect.TicksPerSecond * float64(time.Second))
		v.Cores = float64(cur.CPU.Busy-prev.CPU.Busy) / perCPU
		for i := range prev.Procs {
			old[prev.Procs[i].PID] = &prev.Procs[i]
		}
	}
	v.Procs = make([]Proc, len(cur.Procs))
	for i := range cur.Procs {
		p := &cur.Procs[i]
		v.Procs[i] = Proc{Process: p, Game: -1, Scope: -1}
		v.byPID[p.PID] = i
		// A process that started inside the window counts from zero.
		var before uint64
		if was := old[p.PID]; was != nil && was.Start == p.Start {
			before = was.CPUTicks
		}
		if perCPU > 0 && p.CPUTicks > before {
			v.Procs[i].Cores = float64(p.CPUTicks-before) / perCPU
		}
	}
	for i := range v.Procs {
		p := &v.Procs[i]
		if _, ok := v.byPID[p.PPID]; ok && p.PPID != p.PID {
			v.children[p.PPID] = append(v.children[p.PPID], p.PID)
		}
	}
	if l, err := t3.Discover(roots.Cgroup(), roots.Proc, o.UID); err == nil {
		v.T3 = l
		for i := range v.Procs {
			v.Procs[i].Scope = scopeOf(l, v.Procs[i].Cgroup)
		}
	}
	v.GPUs = o.GPUs
	for _, d := range o.GPUs {
		for _, g := range d.Procs {
			if p := v.Proc(g.PID); p != nil {
				p.GPU += g.Util
				p.VRAM += g.Mem
			}
		}
	}
	v.findGames(o)
	return v
}

// scopeOf returns the agent scope a cgroup path lies in, or -1. A run
// recorded in a child cgroup of the scope still belongs to the agent.
func scopeOf(l *t3.Layout, cgroup string) int {
	for i, s := range l.Scopes {
		if cgroup == s.Path || strings.HasPrefix(cgroup, s.Path+"/") {
			return i
		}
	}
	return -1
}

// Proc returns the process with that pid, or nil.
func (v *View) Proc(pid int) *Proc {
	i, ok := v.byPID[pid]
	if !ok {
		return nil
	}
	return &v.Procs[i]
}

// Children returns the pids of pid's children, ascending.
func (v *View) Children(pid int) []int { return v.children[pid] }

// Subtree returns pid and all its descendants.
func (v *View) Subtree(pid int) []*Proc {
	var out []*Proc
	var walk func(int)
	walk = func(pid int) {
		if p := v.Proc(pid); p != nil {
			out = append(out, p)
		}
		for _, c := range v.children[pid] {
			walk(c)
		}
	}
	walk(pid)
	return out
}

// Handle returns p's handle.
func (v *View) Handle(p *Proc) string { return v.Snap.Handle(p.Process) }

// Started returns when p started, by the wall clock.
func (v *View) Started(p *Proc) time.Time {
	return v.Snap.BootTime.Add(time.Duration(p.Start) * time.Second / collect.TicksPerSecond)
}

// Find resolves what a person or agent typed: a handle, a pid or a name.
// A handle is refused when its process is gone, with what has the pid now.
// A name can match several processes: Find then returns the matching
// processes whose parent doesn't match, the tops of their trees.
func (v *View) Find(arg string) ([]*Proc, error) {
	if pid, letters, ok := collect.ParseHandle(arg); ok {
		p := v.Proc(pid)
		switch {
		case p == nil && letters == "":
			return nil, fmt.Errorf("no process with pid %d", pid)
		case p == nil:
			return nil, fmt.Errorf("%s is gone, and nothing has pid %d now", arg, pid)
		case letters != "" && v.Handle(p) != arg:
			return nil, fmt.Errorf("%s is gone; pid %d is now %q (%s), started %s",
				arg, pid, Name(p), v.Handle(p), v.Started(p).Format("2006-01-02 15:04:05"))
		}
		return []*Proc{p}, nil
	}
	var tops []*Proc
	for i := range v.Procs {
		p := &v.Procs[i]
		if Name(p) != arg && p.Comm != arg {
			continue
		}
		if parent := v.Proc(p.PPID); parent != nil && (Name(parent) == arg || parent.Comm == arg) {
			continue
		}
		tops = append(tops, p)
	}
	if len(tops) == 0 {
		return nil, fmt.Errorf("no process named %q", arg)
	}
	slices.SortStableFunc(tops, func(a, b *Proc) int { return heavier(v.total(a), v.total(b)) })
	return tops, nil
}

// Total is what a set of processes uses together.
type Total struct {
	Count   int
	Cores   float64
	Private uint64  // bytes; shared libraries and files aren't summed
	GPU     float64 // percent of the GPUs
	VRAM    uint64  // bytes
}

func (t *Total) add(p *Proc) {
	t.Count++
	t.Cores += p.Cores
	t.Private += p.Private()
	t.GPU += p.GPU
	t.VRAM += p.VRAM
}

func (t *Total) merge(o Total) {
	t.Count += o.Count
	t.Cores += o.Cores
	t.Private += o.Private
	t.GPU += o.GPU
	t.VRAM += o.VRAM
}

func (v *View) total(root *Proc) Total {
	var t Total
	for _, p := range v.Subtree(root.PID) {
		t.add(p)
	}
	return t
}

// heavier sorts the bigger CPU user first, then the bigger memory user.
func heavier(a, b Total) int {
	// Below a twentieth of a core, CPU use is noise: sort by memory.
	ac, bc := int(a.Cores*20), int(b.Cores*20)
	if ac != bc {
		return bc - ac
	}
	switch {
	case a.Private > b.Private:
		return -1
	case a.Private < b.Private:
		return 1
	}
	return 0
}

// Name is the program a process runs: the base name of its first
// argument, or the kernel's name for it when it has no arguments.
func Name(p *Proc) string {
	if len(p.Args) == 0 || p.Args[0] == "" {
		return p.Comm
	}
	a := p.Args[0]
	// Some programs rewrite their arguments into one string: "nginx:
	// master process", or a browser's "/opt/x/chrome --type=renderer".
	if i := strings.IndexByte(a, ' '); i > 0 {
		a = a[:i]
	}
	a = strings.TrimSuffix(a, ":")
	a = strings.TrimPrefix(path.Base(strings.ReplaceAll(a, `\`, "/")), "-") // "-bash" is a login shell
	if a == "" || a == "." || a == "/" {
		return p.Comm
	}
	return a
}

var interpreters = []string{"python", "node", "bash", "sh", "zsh", "fish", "ruby", "perl", "deno", "bun", "java"}

// Label is Name plus what tells apart processes of the same program: an
// interpreter's script, rustc's crate, the subcommand of cargo, go and git.
func Label(p *Proc) string {
	name := Name(p)
	detail := ""
	switch {
	case len(p.Args) < 2:
	case name == "rustc":
		if i := slices.Index(p.Args, "--crate-name"); i >= 0 && i+1 < len(p.Args) {
			detail = p.Args[i+1]
		}
	case name == "cargo" || name == "go" || name == "git" || name == "npm":
		detail = firstPlain(p.Args[1:])
	case isInterpreter(name):
		// The script's name, but never the script itself (sh -c '…').
		if a := firstPlain(p.Args[1:]); slices.Contains(p.Args[1:], "-c") {
			detail = "-c"
		} else if a != "" {
			detail = path.Base(a)
		}
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if detail == "" {
		return name
	}
	return name + " " + clip(detail, 32)
}

func isInterpreter(name string) bool {
	for _, i := range interpreters {
		// python3.13, node22
		if rest, ok := strings.CutPrefix(name, i); ok && strings.Trim(rest, "0123456789.") == "" {
			return true
		}
	}
	return false
}

// firstPlain returns the first argument that isn't a flag (or cargo's
// +toolchain).
func firstPlain(args []string) string {
	for _, a := range args {
		if a != "" && !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "+") {
			return a
		}
	}
	return ""
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
