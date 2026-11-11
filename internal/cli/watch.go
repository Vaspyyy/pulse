package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Vaspyyy/pulse/collect"
	"github.com/Vaspyyy/pulse/collect/gpu"
	"github.com/Vaspyyy/pulse/internal/query"
)

// Reading is one line of pulse watch --json: the machine at one moment,
// for a program that shows it. Sizes are bytes, cores are CPUs' worth of
// work (1.5 is one and a half CPUs busy), temperatures °C, power watts.
type Reading struct {
	V        int       `json:"v"` // format version, 1
	At       time.Time `json:"at"`
	WindowMS int64     `json:"window_ms"` // what the rates are an average over
	Busy     bool      `json:"busy"`
	Reasons  []string  `json:"reasons"`         // why it is busy; empty when quiet
	Line     string    `json:"line"`            // what pulse load prints
	Games    []string  `json:"games,omitempty"` // the games that run
	CPU      struct {
		Cores float64 `json:"cores"`
		Of    int     `json:"of"`
		Temp  float64 `json:"temp,omitempty"`
	} `json:"cpu"`
	Mem struct {
		Used     uint64  `json:"used"`
		Total    uint64  `json:"total"`
		Pressure float64 `json:"pressure"` // percent of the last 10 s something waited for memory
	} `json:"mem"`
	GPUs []ReadingGPU `json:"gpus,omitempty"`
	// Pids are the processes asked about, in the order asked.
	Pids []ReadingPid `json:"pids,omitempty"`
}

// ReadingGPU is one GPU in a Reading.
type ReadingGPU struct {
	Name       string  `json:"name"`
	Util       float64 `json:"util"`                  // percent; -1 when unknown
	Load       float64 `json:"load"`                  // percent of its power limit, or Util where power can't be read
	Power      float64 `json:"power,omitempty"`       // watts
	PowerLimit float64 `json:"power_limit,omitempty"` // watts
	VRAMUsed   uint64  `json:"vram_used"`
	VRAMTotal  uint64  `json:"vram_total"`
	Temp       float64 `json:"temp,omitempty"`
}

// ReadingPid is a process that was asked about, with everything it
// started: for a terminal, a pane's shell and what runs in it.
type ReadingPid struct {
	PID  int  `json:"pid"`
	Gone bool `json:"gone,omitempty"` // no such process any more; nothing else is set
	// Handle names the process for good ("4821.kfx").
	Handle string `json:"handle,omitempty"`
	// Cores, Memory (private memory) and Processes are totals of the
	// process and all below it.
	Cores     float64 `json:"cores"`
	Memory    uint64  `json:"memory"`
	Processes int     `json:"processes"`
	// Job is the heaviest thing the process started, by all below it
	// ("cargo build"), and Top the single heaviest process below it
	// ("rustc syn"). Both are "" for a process that started nothing.
	Job string `json:"job,omitempty"`
	Top string `json:"top,omitempty"`
}

// watch is pulse watch: a reading every few seconds, one line each, until
// it is stopped or has printed -n lines. A line on its standard input,
// "pids 4821 5133", changes which processes the next lines report.
func watch(args []string, env Env, cfg Config) int {
	every, count, asJSON := 3*time.Second, 0, false
	var pids []int
	for i := 0; i < len(args); i++ {
		name, val, hasVal := strings.Cut(args[i], "=")
		value := func() (string, bool) {
			if hasVal {
				return val, true
			}
			if i+1 >= len(args) {
				fmt.Fprintf(env.Stderr, "pulse: watch: %s needs a value\n", name)
				return "", false
			}
			i++
			return args[i], true
		}
		switch name {
		case "--json":
			asJSON = true
		case "--every":
			v, ok := value()
			if !ok {
				return 2
			}
			s, err := strconv.ParseFloat(v, 64)
			if err != nil || s < 0.25 || s > 3600 {
				fmt.Fprintf(env.Stderr, "pulse: watch: --every %s: want seconds, from 0.25 to 3600\n", v)
				return 2
			}
			every = time.Duration(s * float64(time.Second))
		case "-n":
			v, ok := value()
			if !ok {
				return 2
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				fmt.Fprintf(env.Stderr, "pulse: watch: -n %s: want a number of readings\n", v)
				return 2
			}
			count = n
		case "--pids":
			v, ok := value()
			if !ok {
				return 2
			}
			var bad string
			if pids, bad = parsePids(v); bad != "" {
				fmt.Fprintf(env.Stderr, "pulse: watch: --pids: %q is no pid\n", bad)
				return 2
			}
		default:
			fmt.Fprintf(env.Stderr, "pulse: watch: unknown argument %s; it takes --json, --every <seconds>, --pids <pid,pid>, -n <readings>\n", args[i])
			return 2
		}
	}

	// Lines on standard input replace the pids, without a new start.
	asked := make(chan []int, 64)
	if env.Stdin != nil {
		go func() {
			in := bufio.NewScanner(env.Stdin)
			for in.Scan() {
				if rest, ok := strings.CutPrefix(strings.TrimSpace(in.Text()), "pids"); ok {
					if p, bad := parsePids(rest); bad == "" {
						asked <- p
					}
				}
			}
		}()
	}

	now := time.Now
	if env.Now != nil {
		now = env.Now
	}
	c := collect.New(env.Roots)
	c.Now = now
	c.Markers = append(slices.Clone(c.Markers), cfg.GameEnv...)
	c.SharedEvery = 5
	prev := c.Snapshot()
	// The first line comes after a quarter second, like any command's.
	wait := Window
	for n := 0; count == 0 || n < count; n++ {
		env.Sleep(wait)
		wait = every
		for more := true; more; {
			select {
			case pids = <-asked:
			default:
				more = false
			}
		}
		var gpus []gpu.Device
		if env.GPU != nil {
			gpus = env.GPU()
		}
		cur := c.Snapshot()
		v := query.NewView(prev, cur, env.Roots, query.Options{
			UID: env.UID, SteamName: env.SteamName, GPUs: gpus,
			GameEnv: cfg.GameEnv, GameProcess: cfg.GameProcess,
		})
		prev = cur
		r := reading(v, cfg, pids)
		var err error
		if asJSON {
			var b []byte
			if b, err = json.Marshal(r); err == nil {
				_, err = fmt.Fprintf(env.Stdout, "%s\n", b)
			}
		} else {
			err = r.text(env.Stdout)
		}
		if err != nil {
			return 0 // nobody reads any more
		}
	}
	return 0
}

// parsePids reads pids separated by commas or spaces. bad is the first
// piece that isn't one.
func parsePids(s string) (pids []int, bad string) {
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		pid, err := strconv.Atoi(f)
		if err != nil || pid <= 0 {
			return nil, f
		}
		pids = append(pids, pid)
	}
	return pids, ""
}

// round2 keeps two decimals: more is noise, and it is in every line.
func round2(x float64) float64 { return math.Round(x*100) / 100 }

func reading(v *query.View, cfg Config, pids []int) Reading {
	load := v.Load(cfg.Thresholds)
	r := Reading{V: 1, At: v.Snap.At.Round(time.Millisecond), WindowMS: v.Window.Milliseconds(), Busy: load.Busy, Reasons: load.Reasons, Line: load.Line()}
	if r.Reasons == nil {
		r.Reasons = []string{}
	}
	for _, g := range v.Games {
		r.Games = append(r.Games, g.Name)
	}
	r.CPU.Cores, r.CPU.Of, r.CPU.Temp = round2(v.Cores), v.NCPU, round2(v.Snap.CPUTemp)
	m := v.Snap.Mem
	r.Mem.Used, r.Mem.Total, r.Mem.Pressure = m.Total-min(m.Available, m.Total), m.Total, round2(v.Snap.Pressure.Memory)
	for _, d := range v.GPUs {
		g := ReadingGPU{Name: d.Name, Util: round2(d.Util), Power: round2(d.Power), PowerLimit: round2(d.PowerLimit),
			VRAMUsed: d.MemUsed, VRAMTotal: d.MemTotal, Temp: round2(d.Temp)}
		if l, ok := d.Load(); ok {
			g.Load = round2(l)
		}
		r.GPUs = append(r.GPUs, g)
	}
	for _, pid := range pids {
		r.Pids = append(r.Pids, readingPid(v, pid))
	}
	return r
}

func readingPid(v *query.View, pid int) ReadingPid {
	root := v.Proc(pid)
	if root == nil {
		return ReadingPid{PID: pid, Gone: true}
	}
	out := ReadingPid{PID: pid, Handle: v.Handle(root)}
	// The heavier of two: by CPU, and below a twentieth of a core by memory.
	heavier := func(cores float64, mem uint64, thanCores float64, thanMem uint64) bool {
		if a, b := int(cores*20), int(thanCores*20); a != b {
			return a > b
		}
		return mem > thanMem
	}
	var top *query.Proc
	for _, p := range v.Subtree(pid) {
		out.Processes++
		out.Cores += p.Cores
		out.Memory += p.Private()
		if p != root && (top == nil || heavier(p.Cores, p.Private(), top.Cores, top.Private())) {
			top = p
		}
	}
	out.Cores = round2(out.Cores)
	if top != nil {
		out.Top = query.Label(top)
	}
	var jobCores float64
	var jobMem uint64
	for _, child := range v.Children(pid) {
		var cores float64
		var mem uint64
		for _, p := range v.Subtree(child) {
			cores += p.Cores
			mem += p.Private()
		}
		if p := v.Proc(child); p != nil && (out.Job == "" || heavier(cores, mem, jobCores, jobMem)) {
			out.Job, jobCores, jobMem = query.Label(p), cores, mem
		}
	}
	return out
}

// text writes the reading for a person: the time and the verdict, then a
// line for each process asked about.
func (r Reading) text(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "%s  %s\n", r.At.Local().Format("15:04:05"), r.Line); err != nil {
		return err
	}
	for _, p := range r.Pids {
		line := fmt.Sprintf("          %d: gone", p.PID)
		if !p.Gone {
			what := p.Job
			if what == "" {
				what = "runs nothing"
			}
			line = fmt.Sprintf("          %s: %s, %s, %s", p.Handle, what, query.Cores(p.Cores), query.Bytes(p.Memory))
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}
