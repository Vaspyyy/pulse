package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Vaspyyy/pulse/internal/query"
	"github.com/Vaspyyy/pulse/record"
)

func (env Env) store() record.Store {
	return record.Store{Dir: filepath.Join(env.StateDir, "runs")}
}

// runCommand is pulse run [-n name] [--] command…: it runs the command,
// prints what it used on stderr, keeps the record, and exits as the
// command did.
func runCommand(args []string, env Env, cfg Config) int {
	name := ""
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		flag, val, hasVal := strings.Cut(args[0], "=")
		args = args[1:]
		if flag == "--" {
			break
		}
		if flag != "-n" && flag != "--name" {
			fmt.Fprintf(env.Stderr, "pulse: run: unknown flag %s (flags go before the command, and -- ends them)\n", flag)
			return 2
		}
		if !hasVal {
			if len(args) == 0 {
				fmt.Fprintln(env.Stderr, "pulse: run: -n needs a name")
				return 2
			}
			val, args = args[0], args[1:]
		}
		if !record.ValidName(val) {
			fmt.Fprintf(env.Stderr, "pulse: run: %q can't name a run: use letters, digits, dots and dashes (not \"last\", not the form of a run id)\n", val)
			return 2
		}
		name = val
	}
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, "pulse: run needs a command: pulse run [-n name] -- <command> [args]")
		return 2
	}
	r, err := record.Exec(args, record.Options{
		Name: name, Stdin: env.Stdin, Stdout: env.Stdout, Stderr: env.Stderr,
		Roots: env.Roots, GPU: env.GPU, Getenv: env.Getenv,
		Query: query.Options{UID: env.UID, SteamName: env.SteamName, GameEnv: cfg.GameEnv, GameProcess: cfg.GameProcess},
	})
	if err != nil {
		fmt.Fprintf(env.Stderr, "pulse: %v\n", err)
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return 127
		}
		return 126
	}
	head := fmt.Sprintf("pulse: %s, %s, %s", CommandLine(r, 60), ExitText(r), Dur(r.WallMS))
	if err := env.store().Save(r); err != nil {
		head += fmt.Sprintf(", not recorded (%v)", err)
	} else {
		head += ", recorded as " + r.ID
		if r.Name != "" {
			head += " (" + r.Name + ")"
		}
	}
	lk := env.links(cfg, env.StderrTerminal, nil, []string{r.ID})
	fmt.Fprintln(env.Stderr, lk.line(head))
	for _, l := range Summary(r) {
		fmt.Fprintln(env.Stderr, l)
	}
	report{next: []next{{"pulse show " + r.ID, "per-process breakdown and timeline"}}}.print(env.Stderr, defaultBudget, lk)
	// A run long enough to look away from says when it is over: the
	// terminal marks the tab or shows a notification, as it sees fit.
	if after := cfg.NotifyAfter; env.StderrTerminal && after > 0 && r.WallMS >= int64(after*1000) {
		clean := func(s string) string {
			return strings.Map(func(c rune) rune {
				switch {
				case c == ';':
					return ','
				case c < ' ' || c == 0x7f:
					return ' '
				}
				return c
			}, s)
		}
		fmt.Fprintf(env.Stderr, "\x1b]777;notify;%s;%s\x1b\\", clean(CommandLine(r, 48)),
			clean(fmt.Sprintf("%s after %s, peak %s", ExitText(r), Dur(r.WallMS), query.Bytes(r.PeakPrivate))))
	}
	return r.Exit
}

// commandLine is a run's command on one line, at most n characters.
func CommandLine(r *record.Run, n int) string {
	return clip(strings.Join(strings.Fields(strings.Join(r.Command, " ")), " "), n)
}

func ExitText(r *record.Run) string {
	if r.Signal != "" {
		return "killed by " + r.Signal
	}
	return fmt.Sprintf("exit %d", r.Exit)
}

// summary is a run's numbers, a line each.
func Summary(r *record.Run) []string {
	wall := float64(r.WallMS) / 1000
	avg := 0.0
	if wall > 0 {
		avg = float64(r.CPUMS()) / 1000 / wall
	}
	out := []string{fmt.Sprintf("  cpu    %s (%s user, %s sys), %.1f cores on average",
		Dur(r.CPUMS()), durBare(r.UserMS), durBare(r.SystemMS), avg)}

	peak := fmt.Sprintf("  peak   %s private memory (sampled every %d ms)", query.Bytes(r.PeakPrivate), r.SampleMS)
	if r.Samples == 0 || r.PeakPrivate == 0 {
		peak = "  peak   not sampled, the run was over too quickly"
	}
	if r.LargestRSS > 0 {
		peak += "; largest process " + query.Bytes(r.LargestRSS) + " RSS"
		if r.LargestName != "" {
			peak += " (" + r.LargestName + ")"
		} else {
			peak += " (too short-lived to be sampled)"
		}
	}
	out = append(out, peak)
	if s := r.Stall; s != nil {
		out = append(out, fmt.Sprintf("  stall  cpu %s, memory %s, io %s", Dur(s.CPUMS), Dur(s.MemoryMS), Dur(s.IOMS)))
	}
	if r.ReadBytes > 0 || r.WrittenBytes > 0 {
		out = append(out, fmt.Sprintf("  io     %s read, %s written", query.Bytes(r.ReadBytes), query.Bytes(r.WrittenBytes)))
	}
	if g := r.GPU; g != nil {
		out = append(out, fmt.Sprintf("  gpu    %s vram at most, %.0f%% on average", query.Bytes(g.PeakVRAM), g.AvgUtil))
	}
	out = append(out, "  other  "+otherText(r, true))
	if len(r.Left) > 0 {
		out = append(out, fmt.Sprintf("  left   %s still running: %s", plural(len(r.Left), "process", "processes"), clip(strings.Join(r.Left, ", "), 80)))
	}
	if r.Method == "tree" {
		out = append(out, "  note   no cgroup could be made here, so the process tree was sampled: processes that detached are missed")
	}
	return out
}

// otherText words what else ran during a run.
func otherText(r *record.Run, long bool) string {
	o := r.Other
	if !o.Known && len(o.Games) == 0 {
		return "too short a run to tell"
	}
	s := fmt.Sprintf("%.1f cores on average", o.AvgCores)
	if long && o.PeakCores > 2*o.AvgCores+1 {
		s += fmt.Sprintf(" (up to %.1f)", o.PeakCores)
	}
	switch {
	case len(o.Games) > 0:
		s = "a game (" + strings.Join(o.Games, ", ") + "), " + s
	case !o.Disturbed:
		s += ", no game"
	}
	if long && o.Disturbed {
		s += ": wall time isn't comparable with a quiet run's"
	}
	return s
}

// dur formats milliseconds: 48.2 s, 653 s, 21.5 min, 3.2 h.
func Dur(ms int64) string { return durBare(ms) + durUnit(ms) }

func durUnit(ms int64) string {
	switch s := float64(ms) / 1000; {
	case s < 1000:
		return " s"
	case s < 7200:
		return " min"
	}
	return " h"
}

// durBare is dur without the unit, for a second number in the same unit
// as the first only when both are in seconds: it always adds the unit
// when it isn't seconds.
func durBare(ms int64) string {
	switch s := float64(ms) / 1000; {
	case s < 100:
		return fmt.Sprintf("%.1f", s)
	case s < 1000:
		return fmt.Sprintf("%.0f", s)
	case s < 7200:
		return fmt.Sprintf("%.1f", s/60)
	default:
		return fmt.Sprintf("%.1f", s/3600)
	}
}

func When(t time.Time) string { return t.Local().Format("01-02 15:04") }

// place names where a run ran: the repository (or directory) and commit.
func Place(r *record.Run) string {
	if r.Repo == nil {
		return project(r.Cwd)
	}
	s := project(r.Repo.Root) + "@" + short7(r.Repo.Head)
	if r.Repo.State == "dirty" {
		s += "+"
	}
	return s
}

func short7(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// runs lists the recorded runs, newest first.
func runs(env Env) report {
	all := env.store().List()
	if len(all) == 0 {
		return report{body: []string{"nothing is recorded yet"}, next: []next{{"pulse run -- <command>", "runs a command and records what it uses"}}}
	}
	var rows [][]string
	for _, r := range all {
		mark := ""
		if r.Other.Disturbed {
			mark = "other load"
		}
		rows = append(rows, []string{r.ID, When(r.Start), CommandLine(r, 32), ExitText(r),
			Dur(r.WallMS), Dur(r.CPUMS()) + " cpu", query.Bytes(r.PeakPrivate), Place(r), r.Name, mark})
	}
	rep := report{body: table(rows, "llllrrrlll")}
	for _, r := range all {
		rep.ids = append(rep.ids, r.ID)
	}
	rep.next = append(rep.next, next{"pulse show " + all[0].ID, "the newest run in detail"})
	if prev := earlier(all, all[0]); prev != nil {
		rep.next = append(rep.next, next{"pulse diff " + prev.ID + " " + all[0].ID, "against the run of the same command before it"})
	}
	return rep
}

// earlier returns the newest run before r that ran the same command in
// the same directory, or nil. all is newest first.
func earlier(all []*record.Run, r *record.Run) *record.Run {
	for _, o := range all {
		if o.ID != r.ID && o.Start.Before(r.Start) && record.Same(o, r) {
			return o
		}
	}
	return nil
}

const sparks = "▁▂▃▄▅▆▇█"

// spark draws values as a row of at most width blocks, each the highest
// of the values it covers.
func spark[T float32 | uint32](vals []T, width int) string {
	if len(vals) == 0 {
		return ""
	}
	width = min(width, len(vals))
	cols := make([]float64, width)
	top := 0.0
	for i, v := range vals {
		c := i * width / len(vals)
		cols[c] = max(cols[c], float64(v))
		top = max(top, float64(v))
	}
	var b strings.Builder
	for _, c := range cols {
		level := 0
		if top > 0 {
			level = min(int(c/top*8), 7)
		}
		b.WriteString(string([]rune(sparks)[level]))
	}
	return b.String()
}

// show prints one run: the summary, its processes and its timeline.
func show(env Env, ref string) (report, error) {
	st := env.store()
	r, err := st.Find(ref)
	if err != nil {
		return report{}, err
	}
	head := fmt.Sprintf("%s in %s", CommandLine(r, 60), Place(r))
	if r.Repo != nil && r.Repo.Branch != "" {
		head += " (" + r.Repo.Branch + ")"
	}
	head += ", " + When(r.Start) + ", " + r.ID
	if r.Name != "" {
		head += " (" + r.Name + ")"
	}
	rep := report{body: []string{head, ExitText(r) + ", " + Dur(r.WallMS)}, ids: []string{r.ID}}
	rep.body = append(rep.body, Summary(r)...)

	if len(r.Procs) > 0 {
		rep.body = append(rep.body, "processes, as sampled")
		var rows [][]string
		for _, p := range r.Procs[:min(len(r.Procs), 6)] {
			label := p.Label
			if p.Count > 1 {
				label += fmt.Sprintf(" ×%d", p.Count)
			}
			rows = append(rows, []string{"  " + label, Dur(p.CPUMS) + " cpu", query.Bytes(p.PeakRSS) + " at most"})
		}
		rep.body = append(rep.body, table(rows, "lrr")...)
		if more := len(r.Procs) - min(len(r.Procs), 6) + r.MoreProcs; more > 0 {
			rep.body = append(rep.body, fmt.Sprintf("  and %d more kinds", more))
		}
		if r.UnsampledMS*20 > r.CPUMS() && r.UnsampledMS > 100 {
			rep.body = append(rep.body, fmt.Sprintf("  %s cpu wasn't caught: processes too short-lived to sample, or after their last sample", Dur(r.UnsampledMS)))
		}
	}
	if t := r.Timeline; len(t.Cores) > 1 {
		var topCores float32
		for _, c := range t.Cores {
			topCores = max(topCores, c)
		}
		rep.body = append(rep.body,
			fmt.Sprintf("over time, %s per block", Dur(int64(t.StepMS)*int64(len(t.Cores))/int64(min(len(t.Cores), 60)))),
			fmt.Sprintf("  cores  %s  up to %.1f", spark(t.Cores, 60), topCores),
			fmt.Sprintf("  memory %s  up to %s", spark(t.Private, 60), query.Bytes(r.PeakPrivate)))
	}
	if prev := earlier(st.List(), r); prev != nil {
		rep.next = append(rep.next, next{"pulse diff " + prev.ID + " " + r.ID, "against the run of the same command before it"})
	}
	return rep, nil
}

// diff compares two runs side by side.
func diff(env Env, refs []string) (report, error) {
	st := env.store()
	var a, b *record.Run
	var err error
	switch len(refs) {
	case 0, 1:
		if b, err = st.Find("last"); err != nil {
			return report{}, err
		}
		if len(refs) == 1 {
			a, err = st.Find(refs[0])
		} else if a = earlier(st.List(), b); a == nil {
			err = fmt.Errorf("%s (%s) is the only recorded run of that command here; pulse runs lists what there is",
				b.ID, CommandLine(b, 40))
		}
	case 2:
		if a, err = st.Find(refs[0]); err == nil {
			b, err = st.Find(refs[1])
		}
	default:
		err = errors.New("diff takes two runs, one (against the newest), or none (the newest against the one before it)")
	}
	if err != nil {
		return report{}, err
	}

	head, rows, notes := DiffRows(a, b)
	rep := report{body: append([]string{head}, table(rows, "llll")...), ids: []string{a.ID, b.ID}}
	for _, n := range notes {
		rep.body = append(rep.body, "note: "+n)
	}
	if a.Repo != nil && b.Repo != nil && a.Repo.Root == b.Repo.Root && a.Repo.Head != b.Repo.Head {
		rep.next = append(rep.next, next{"recap " + short7(a.Repo.Head) + ".." + short7(b.Repo.Head), "what changed between the two runs"})
	}
	rep.next = append(rep.next, next{"pulse show " + b.ID, "the second run in detail"})
	return rep, nil
}

// DiffRows compares two runs: a heading, a table (its first row names the
// columns) and notes on what doesn't compare.
func DiffRows(a, b *record.Run) (head string, rows [][]string, notes []string) {
	side := func(r *record.Run) string {
		id := r.ID + ", " + When(r.Start)
		if r.Repo != nil {
			return short7(r.Repo.Head) + " (" + id + ")"
		}
		return id
	}
	head = side(a) + " vs " + side(b)
	if record.Same(a, b) {
		in := project(a.Cwd)
		if a.Repo != nil {
			in = project(a.Repo.Root)
		}
		head = CommandLine(a, 40) + " in " + in + ": " + head
	}
	percent := func(x, y int64) string {
		if x <= 0 {
			return ""
		}
		return fmt.Sprintf("%+.0f %%", float64(y-x)/float64(x)*100)
	}
	bytes := func(x, y uint64) string {
		switch {
		case max(x, y)-min(x, y) <= max(x, y)/100:
			return "about the same"
		case y > x:
			return "+" + query.Bytes(y-x)
		case y < x:
			return "-" + query.Bytes(x-y)
		}
		return "same"
	}
	largest := func(r *record.Run) string {
		return strings.TrimSpace(r.LargestName + " " + query.Bytes(r.LargestRSS))
	}
	rows = [][]string{
		{"", "before", "after", "change"},
		{"wall", Dur(a.WallMS), Dur(b.WallMS), percent(a.WallMS, b.WallMS)},
		{"cpu", Dur(a.CPUMS()), Dur(b.CPUMS()), percent(a.CPUMS(), b.CPUMS())},
		{"peak memory", query.Bytes(a.PeakPrivate), query.Bytes(b.PeakPrivate), bytes(a.PeakPrivate, b.PeakPrivate)},
		{"largest process", largest(a), largest(b), bytes(a.LargestRSS, b.LargestRSS)},
	}
	if a.Stall != nil && b.Stall != nil {
		stalled := func(s *record.Stall) string {
			return fmt.Sprintf("cpu %s, memory %s, io %s", Dur(s.CPUMS), Dur(s.MemoryMS), Dur(s.IOMS))
		}
		rows = append(rows, []string{"stalled", stalled(a.Stall), stalled(b.Stall), ""})
	}
	if a.ReadBytes+a.WrittenBytes+b.ReadBytes+b.WrittenBytes > 0 {
		rows = append(rows, []string{"written", query.Bytes(a.WrittenBytes), query.Bytes(b.WrittenBytes), bytes(a.WrittenBytes, b.WrittenBytes)})
	}
	rows = append(rows, []string{"other load", otherText(a, false), otherText(b, false), ""})
	note := func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }
	if !record.Same(a, b) {
		note("these aren't the same command in the same directory, so the numbers compare only loosely")
	}
	for _, r := range []*record.Run{a, b} {
		if r.Exit != 0 {
			note("%s ended with %s, so it may have done less work", r.ID, ExitText(r))
		}
	}
	switch {
	case a.Other.Disturbed && b.Other.Disturbed:
		note("both runs overlapped other load, so their wall times aren't comparable")
	case a.Other.Disturbed:
		note("%s overlapped other load, so its wall time isn't comparable", a.ID)
	case b.Other.Disturbed:
		note("%s overlapped other load, so its wall time isn't comparable", b.ID)
	}
	if a.Method != b.Method {
		note("the runs were measured differently (%s and %s): cpu time of a sampled tree can miss processes", a.Method, b.Method)
	}
	return head, rows, notes
}
