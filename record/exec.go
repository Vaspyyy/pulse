package record

import (
	"cmp"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Vaspyyy/pulse/collect"
	"github.com/Vaspyyy/pulse/collect/gpu"
	"github.com/Vaspyyy/pulse/internal/query"
)

// Options say how to run and what to read.
type Options struct {
	Name           string // the run's name, may be ""
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Roots          collect.Roots
	Query          query.Options       // how to tell games; its GPUs are ignored
	GPU            func() []gpu.Device // may be nil
	Getenv         func(string) string
	// NoCgroup measures by sampling the process tree even where a cgroup
	// could be made. For tests.
	NoCgroup bool
}

const (
	fastStep  = 100 * time.Millisecond // the first sampling interval with a cgroup
	fastMax   = 800 * time.Millisecond // what it backs off to
	treeStep  = 500 * time.Millisecond // without a cgroup every sample reads all of /proc
	treeMax   = time.Second
	slowEvery = 2 * time.Second // other load and GPU
	maxPoints = 600
	keepProcs = 12
)

// Exec runs argv to its end and returns the record. The error is non-nil
// only when the command couldn't be started; a command that fails is a
// run like any other, with its exit code in the record.
func Exec(argv []string, o Options) (*Run, error) {
	r := &Run{V: 1, ID: newID(), Name: o.Name, Command: argv, NCPU: 1}
	r.Cwd, _ = os.Getwd()
	for _, v := range []struct{ env, kind string }{
		{"T3CODE_THREAD_ID", "t3"}, {"CLAUDE_CODE_SESSION_ID", "claude"}, {"PRISM_SESSION", "prism"},
	} {
		if id := o.Getenv(v.env); id != "" {
			r.Session = v.kind + ":" + id
			break
		}
	}
	r.Repo = repoHead(r.Cwd)
	dirty := make(chan string, 1)
	if r.Repo != nil {
		go func() { dirty <- repoState(r.Cwd) }()
	}

	c := collect.New(o.Roots)
	c.Markers = append(slices.Clone(c.Markers), o.Query.GameEnv...)
	first := c.Snapshot()
	r.NCPU = max(len(first.Cores), 1)

	var cg *runCgroup
	if !o.NoCgroup {
		cg = openCgroup(r.ID)
	}
	start := func() (*exec.Cmd, error) {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = o.Stdin, o.Stdout, o.Stderr
		if cg != nil {
			// Started straight into the cgroup (clone3 with
			// CLONE_INTO_CGROUP): it never runs outside it.
			cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(cg.fd.Fd())}
		}
		return cmd, cmd.Start()
	}
	cmd, err := start()
	if err != nil && cg != nil && !errors.Is(err, exec.ErrNotFound) && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission) {
		// The cgroup didn't take the command: measure without one.
		cg.close()
		cg = nil
		cmd, err = start()
	}
	if err != nil {
		if cg != nil {
			cg.close()
		}
		return nil, err
	}
	r.Start = time.Now()
	r.Method = "tree"
	if cg != nil {
		r.Method = "cgroup"
	}

	// The terminal sends Ctrl-C to the command as well: leave the reaction
	// to it, and outlive it to write the record. A plain kill of Pulse is
	// passed on.
	signal.Ignore(syscall.SIGINT, syscall.SIGQUIT)
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	defer signal.Reset(syscall.SIGINT, syscall.SIGQUIT)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	s := newSampler(c, cg, cmd.Process.Pid, first, o, r.Start)
	timer := time.NewTimer(25 * time.Millisecond) // early, so that short commands are seen at all
	defer timer.Stop()
wait:
	for {
		select {
		case <-done:
			break wait
		case sig := <-sigs:
			_ = cmd.Process.Signal(sig)
		case <-timer.C:
			s.sample()
			timer.Reset(s.interval)
		}
	}
	r.WallMS = time.Since(r.Start).Milliseconds()

	state := cmd.ProcessState
	r.Exit = state.ExitCode()
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		r.Signal = ws.Signal().String()
		r.Exit = 128 + int(ws.Signal())
	}
	s.finish(r, state)
	if r.Repo != nil {
		select {
		case r.Repo.State = <-dirty:
		case <-time.After(2 * time.Second):
		}
	}
	return r, nil
}

// sampler follows a run while it lasts.
type sampler struct {
	c     *collect.Collector
	cg    *runCgroup // nil: the process tree is sampled
	root  int
	o     Options
	start time.Time

	interval, maxInterval time.Duration
	step                  time.Duration // what one timeline point covers
	inStep                int           // samples in the current point
	stepCores             float64
	stepPrivate           uint64
	points                []point

	last       time.Time // the previous sample
	lastUsage  int64     // cgroup CPU time then, in microseconds
	samples    int
	members    map[memberKey]*member
	peak       uint64
	largest    uint64
	largestIs  string
	firstSnap  *collect.Snapshot
	slowSnap   *collect.Snapshot // the previous full reading
	slowAt     time.Time
	slowUsage  int64 // run CPU time then, in microseconds
	otherPeak  float64
	games      []string
	gpuSamples int
	gpuUtil    float64
	gpuPeak    uint64
}

type point struct {
	cores   float64
	private uint64
}

type memberKey struct {
	pid   int
	start uint64
}

type member struct {
	label string
	ticks uint64 // CPU time when last seen
	rss   uint64 // the most it had
}

func newSampler(c *collect.Collector, cg *runCgroup, root int, first *collect.Snapshot, o Options, start time.Time) *sampler {
	s := &sampler{c: c, cg: cg, root: root, o: o, start: start, last: start, members: map[memberKey]*member{},
		firstSnap: first, slowSnap: first, slowAt: start,
		interval: fastStep, maxInterval: fastMax}
	if cg == nil {
		s.interval, s.maxInterval = treeStep, treeMax
	}
	s.step = s.interval
	return s
}

// sampledUsage is the CPU time the sampled processes have used so far, in
// microseconds: what stands in for the cgroup's exact count without one.
func (s *sampler) sampledUsage() int64 {
	var ticks uint64
	for _, m := range s.members {
		ticks += m.ticks
	}
	return int64(ticks) * (1e6 / collect.TicksPerSecond)
}

// sample reads the run's processes once.
func (s *sampler) sample() {
	// The clock and the CPU time are read together: the rest of a sample
	// takes a while, and time that passes between the two would show up
	// as CPU use that wasn't there.
	now := time.Now()
	var usage int64
	var snap *collect.Snapshot
	slow := now.Sub(s.slowAt) >= slowEvery || s.samples == 0
	var pids []int
	if s.cg != nil {
		usage, _, _ = s.cg.cpu()
		pids = s.cg.pids()
		if slow {
			snap = s.c.Snapshot()
		}
	} else {
		// No cgroup says who belongs to the run: read everything and take
		// what descends from the command.
		snap = s.c.Snapshot()
		now = time.Now()
		pids = descendants(snap, s.root)
	}

	var private uint64
	for _, pid := range pids {
		p, ok := s.c.Process(pid)
		if !ok {
			continue
		}
		private += p.Private()
		key := memberKey{pid, p.Start}
		m := s.members[key]
		if m == nil {
			m = &member{}
			s.members[key] = m
		}
		// exec keeps the process and changes what it runs: the last name wins.
		m.label = query.Label(&query.Proc{Process: &p})
		m.ticks = max(m.ticks, p.CPUTicks)
		m.rss = max(m.rss, p.RSS)
		if p.RSS > s.largest {
			s.largest, s.largestIs = p.RSS, m.label
		}
	}
	s.peak = max(s.peak, private)
	s.samples++

	if s.cg == nil {
		usage = s.sampledUsage()
	}
	var cores float64
	if dt := now.Sub(s.last).Seconds(); dt > 0 {
		cores = float64(usage-s.lastUsage) / 1e6 / dt
	}
	s.last, s.lastUsage = now, usage
	s.addPoint(cores, private)

	if slow && snap != nil {
		s.slowSample(now, snap, usage, pids)
	}
}

// addPoint adds a sample to the timeline. When the timeline is full, its
// points are merged in pairs and sampling slows down, up to a limit.
func (s *sampler) addPoint(cores float64, private uint64) {
	s.stepCores += cores
	s.stepPrivate = max(s.stepPrivate, private)
	s.inStep++
	if s.inStep < int(s.step/s.interval) {
		return
	}
	s.points = append(s.points, point{s.stepCores / float64(s.inStep), s.stepPrivate})
	s.inStep, s.stepCores, s.stepPrivate = 0, 0, 0
	if len(s.points) < maxPoints {
		return
	}
	half := s.points[:0]
	for i := 0; i+1 < len(s.points); i += 2 {
		a, b := s.points[i], s.points[i+1]
		half = append(half, point{(a.cores + b.cores) / 2, max(a.private, b.private)})
	}
	s.points = half
	s.step *= 2
	s.interval = min(s.step, s.maxInterval)
}

// slowSample looks at the rest of the machine: what else used the CPUs
// since the last look, whether a game runs, and the GPUs.
func (s *sampler) slowSample(now time.Time, snap *collect.Snapshot, usage int64, pids []int) {
	opts := s.o.Query
	if s.o.GPU != nil {
		opts.GPUs = s.o.GPU()
	} else {
		opts.GPUs = nil
	}
	v := query.NewView(s.slowSnap, snap, s.o.Roots, opts)
	if dt := now.Sub(s.slowAt).Seconds(); dt >= 1 && v.Window > 0 {
		run := float64(usage-s.slowUsage) / 1e6 / dt
		s.otherPeak = max(s.otherPeak, v.Cores-run)
	}
	s.slowSnap, s.slowAt, s.slowUsage = snap, now, usage
	for _, g := range v.Games {
		if !slices.Contains(s.games, g.Name) {
			s.games = append(s.games, g.Name)
		}
	}
	if len(opts.GPUs) > 0 {
		var util float64
		var vram uint64
		for _, pid := range pids {
			if p := v.Proc(pid); p != nil {
				util += p.GPU
				vram += p.VRAM
			}
		}
		s.gpuSamples++
		s.gpuUtil += util
		s.gpuPeak = max(s.gpuPeak, vram)
	}
}

// descendants returns pid and everything below it in the snapshot.
func descendants(snap *collect.Snapshot, pid int) []int {
	children := map[int][]int{}
	for i := range snap.Procs {
		p := &snap.Procs[i]
		children[p.PPID] = append(children[p.PPID], p.PID)
	}
	out := []int{pid}
	for i := 0; i < len(out); i++ {
		out = append(out, children[out[i]]...)
	}
	return out
}

// finish fills the record once the command has exited.
func (s *sampler) finish(r *Run, state *os.ProcessState) {
	end := s.c.Snapshot()
	ru, _ := state.SysUsage().(*syscall.Rusage)

	if s.cg != nil {
		_, user, system := s.cg.cpu()
		r.UserMS, r.SystemMS = user/1000, system/1000
		r.Stall = &Stall{CPUMS: s.cg.stall("cpu") / 1000, MemoryMS: s.cg.stall("memory") / 1000, IOMS: s.cg.stall("io") / 1000}
		for _, pid := range s.cg.close() {
			label := "pid " + strconv.Itoa(pid)
			if p, ok := s.c.Process(pid); ok {
				label = query.Label(&query.Proc{Process: &p}) + " (" + end.Handle(&p) + ")"
			}
			r.Left = append(r.Left, label)
		}
	} else if ru != nil {
		// wait4 counts the command and the descendants it waited for.
		r.UserMS = ru.Utime.Nano() / 1e6
		r.SystemMS = ru.Stime.Nano() / 1e6
	}

	r.PeakPrivate = s.peak
	r.Samples = s.samples
	r.SampleMS = int(fastStep / time.Millisecond)
	if s.cg == nil {
		r.SampleMS = int(treeStep / time.Millisecond)
	}
	r.LargestRSS, r.LargestName = s.largest, s.largestIs
	if ru != nil {
		// ru_maxrss is in KiB. When it is well above anything sampled, the
		// largest process was one no sample caught, so it has no name.
		if exact := uint64(ru.Maxrss) << 10; exact > r.LargestRSS {
			if exact > r.LargestRSS+r.LargestRSS/4 {
				r.LargestName = ""
			}
			r.LargestRSS = exact
		}
		// In 512-byte blocks.
		r.ReadBytes, r.WrittenBytes = uint64(ru.Inblock)<<9, uint64(ru.Oublock)<<9
	}

	if s.gpuPeak > 0 || s.gpuUtil >= 1 {
		r.GPU = &GPU{PeakVRAM: s.gpuPeak, AvgUtil: s.gpuUtil / float64(max(s.gpuSamples, 1))}
	}

	// Everything the machine did, minus the run: exact for the whole run.
	// The machine's own tick count is the clock, and under a quarter of a
	// second of it the hundredths the kernel counts in are too coarse.
	if end.CPU.All > s.firstSnap.CPU.All && end.CPU.Busy >= s.firstSnap.CPU.Busy {
		window := float64(end.CPU.All-s.firstSnap.CPU.All) / float64(r.NCPU) / collect.TicksPerSecond
		if window >= 0.25 {
			busy := float64(end.CPU.Busy-s.firstSnap.CPU.Busy) / collect.TicksPerSecond
			r.Other.Known = true
			r.Other.AvgCores = max((busy-float64(r.CPUMS())/1000)/window, 0)
		}
	}
	r.Other.PeakCores = max(s.otherPeak, r.Other.AvgCores)
	r.Other.Games = s.games
	r.Other.Disturbed = len(s.games) > 0 || r.Other.AvgCores > float64(r.NCPU)/4

	byLabel := map[string]*ProcStat{}
	var sampledMS int64
	for _, m := range s.members {
		p := byLabel[m.label]
		if p == nil {
			p = &ProcStat{Label: m.label}
			byLabel[m.label] = p
		}
		ms := int64(m.ticks) * (1000 / collect.TicksPerSecond)
		p.Count++
		p.CPUMS += ms
		p.PeakRSS = max(p.PeakRSS, m.rss)
		sampledMS += ms
	}
	for _, p := range byLabel {
		r.Procs = append(r.Procs, *p)
	}
	slices.SortFunc(r.Procs, func(a, b ProcStat) int {
		return cmp.Or(cmp.Compare(b.CPUMS, a.CPUMS), cmp.Compare(b.PeakRSS, a.PeakRSS), strings.Compare(a.Label, b.Label))
	})
	if len(r.Procs) > keepProcs {
		r.MoreProcs = len(r.Procs) - keepProcs
		r.Procs = r.Procs[:keepProcs]
	}
	r.UnsampledMS = max(r.CPUMS()-sampledMS, 0)

	if s.inStep > 0 {
		s.points = append(s.points, point{s.stepCores / float64(s.inStep), s.stepPrivate})
	}
	r.Timeline.StepMS = int(s.step / time.Millisecond)
	for _, p := range s.points {
		r.Timeline.Cores = append(r.Timeline.Cores, float32(int(p.cores*10+0.5))/10)
		r.Timeline.Private = append(r.Timeline.Private, uint32(p.private>>20))
	}
}

// repoHead returns the git repository dir is in and its HEAD, or nil.
func repoHead(dir string) *Repo {
	out, err := git(dir, time.Second, "rev-parse", "--show-toplevel", "HEAD", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return nil
	}
	r := &Repo{Root: lines[0], Head: lines[1]}
	if len(lines) > 2 && lines[2] != "HEAD" {
		r.Branch = lines[2]
	}
	return r
}

// repoState says whether tracked files have uncommitted changes. It runs
// next to the measured command, so it must be gentle: git doesn't take
// the index lock, and a slow answer is given up on.
func repoState(dir string) string {
	out, err := git(dir, 5*time.Second, "status", "--porcelain", "--untracked-files=no")
	switch {
	case err != nil:
		return ""
	case strings.TrimSpace(out) == "":
		return "clean"
	}
	return "dirty"
}

func git(dir string, limit time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	return string(out), err
}
