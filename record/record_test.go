package record

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Vaspyyy/pulse/collect"
)

// The test binary doubles as the programs the tests measure: with
// PULSE_RECORD_HELPER set it does what that says and exits.
//
//	work:<MB>:<ms>     hold <MB> of memory and keep one CPU busy for <ms>
//	spawn:<n>:<ms>     start <n> children that each keep a CPU busy for <ms>
//	exit:<code>
func TestMain(m *testing.M) {
	if h := os.Getenv("PULSE_RECORD_HELPER"); h != "" {
		os.Exit(helper(h))
	}
	os.Exit(m.Run())
}

func helper(spec string) int {
	f := strings.Split(spec, ":")
	num := func(i int) int { n, _ := strconv.Atoi(f[i]); return n }
	switch f[0] {
	case "work":
		mem := make([]byte, num(1)<<20)
		for i := range mem {
			mem[i] = byte(i) // touched, so it's resident and private
		}
		spin(time.Duration(num(2)) * time.Millisecond)
		return int(mem[len(mem)-1]) * 0
	case "spawn":
		var cmds []*exec.Cmd
		for range num(1) {
			c := exec.Command(os.Args[0])
			c.Env = append(os.Environ(), "PULSE_RECORD_HELPER=work:1:"+f[2])
			if c.Start() != nil {
				return 1
			}
			cmds = append(cmds, c)
		}
		for _, c := range cmds {
			if c.Wait() != nil {
				return 1
			}
		}
	case "exit":
		return num(1)
	}
	return 0
}

func spin(d time.Duration) {
	for end := time.Now().Add(d); time.Now().Before(end); {
	}
}

func run(t *testing.T, noCgroup bool, spec string) *Run {
	t.Helper()
	t.Setenv("PULSE_RECORD_HELPER", spec)
	r, err := Exec([]string{os.Args[0]}, Options{
		Name: "t", Roots: collect.Live(), Getenv: os.Getenv, NoCgroup: noCgroup,
		Stdout: os.Stderr, Stderr: os.Stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestExec(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real processes for a second or two")
	}
	for _, noCgroup := range []bool{false, true} {
		t.Run(fmt.Sprintf("noCgroup=%v", noCgroup), func(t *testing.T) {
			r := run(t, noCgroup, "work:96:700")
			t.Logf("%s: wall %d ms, cpu %d ms, peak %d MiB, largest %d MiB (%s), %d samples, other %.1f cores",
				r.Method, r.WallMS, r.CPUMS(), r.PeakPrivate>>20, r.LargestRSS>>20, r.LargestName, r.Samples, r.Other.AvgCores)
			if noCgroup && r.Method != "tree" {
				t.Errorf("method %s, want tree", r.Method)
			}
			if r.Exit != 0 || r.Signal != "" || r.V != 1 || !idForm.MatchString(r.ID) || r.Name != "t" {
				t.Errorf("exit %d, signal %q, v %d, id %q, name %q", r.Exit, r.Signal, r.V, r.ID, r.Name)
			}
			if r.WallMS < 700 || r.WallMS > 30000 {
				t.Errorf("wall %d ms; the program runs for 700", r.WallMS)
			}
			// One CPU busy for 0.7 s, plus touching the memory.
			if r.CPUMS() < 600 || r.CPUMS() > r.WallMS+500 {
				t.Errorf("cpu %d ms over %d ms of wall time", r.CPUMS(), r.WallMS)
			}
			// Under the race detector the program itself is several times
			// larger, hence the wide upper bound.
			if r.PeakPrivate < 96<<20 || r.PeakPrivate > 1<<30 {
				t.Errorf("peak private memory %d MiB; the program holds 96", r.PeakPrivate>>20)
			}
			if r.LargestRSS < 96<<20 || r.LargestName == "" {
				t.Errorf("largest process %d MiB (%q)", r.LargestRSS>>20, r.LargestName)
			}
			if len(r.Procs) != 1 || r.Procs[0].Count != 1 || r.Procs[0].PeakRSS < 96<<20 {
				t.Errorf("processes %+v; want the one program", r.Procs)
			}
			if r.Samples < 2 || len(r.Timeline.Cores) == 0 || len(r.Timeline.Cores) != len(r.Timeline.Private) {
				t.Errorf("%d samples, timeline %d/%d points", r.Samples, len(r.Timeline.Cores), len(r.Timeline.Private))
			}
			for _, c := range r.Timeline.Cores {
				if c > float32(r.NCPU)+0.5 {
					t.Errorf("timeline has %.1f cores on %d CPUs", c, r.NCPU)
				}
			}
			if !r.Other.Known || r.Other.AvgCores < 0 || r.Other.AvgCores > float64(r.NCPU) {
				t.Errorf("other load %+v", r.Other)
			}
			if r.Method == "cgroup" {
				if r.Stall == nil || len(r.Left) != 0 {
					t.Errorf("stall %v, left %v", r.Stall, r.Left)
				}
				if m, _ := filepath.Glob(filepath.Join(cgroupRoot, ownCgroup(), "pulse-"+r.ID)); len(m) != 0 {
					t.Errorf("the run's cgroup is still there: %v", m)
				}
			}
			if r.Repo == nil || r.Repo.Head == "" || r.Cwd == "" {
				t.Errorf("repo %+v, cwd %q; the tests run inside Pulse's repository", r.Repo, r.Cwd)
			}
		})
	}
}

// With a cgroup, CPU time is exact however the command forks: children
// the sampler barely sees still count in full.
func TestExecCountsChildren(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real processes")
	}
	r := run(t, false, "spawn:3:300")
	if r.Method != "cgroup" {
		t.Skip("no delegated cgroup here (not inside a T3 agent scope)")
	}
	if r.CPUMS() < 850 {
		t.Errorf("cpu %d ms; three children kept a CPU busy for 300 ms each", r.CPUMS())
	}
	if r.UserMS+r.SystemMS != r.CPUMS() || r.UserMS < 800 {
		t.Errorf("user %d ms, system %d ms", r.UserMS, r.SystemMS)
	}
}

func TestExecExit(t *testing.T) {
	if r := run(t, false, "exit:3"); r.Exit != 3 || r.Signal != "" {
		t.Errorf("exit %d, signal %q; want 3", r.Exit, r.Signal)
	}
	r, err := Exec([]string{"sh", "-c", "kill -9 $$"}, Options{Roots: collect.Live(), Getenv: os.Getenv})
	if err != nil || r.Exit != 137 || r.Signal != "killed" {
		t.Errorf("killed command: exit %d, signal %q, err %v; want 137, killed", r.Exit, r.Signal, err)
	}
	_, err = Exec([]string{"/nonexistent/pulse-test-command"}, Options{Roots: collect.Live(), Getenv: os.Getenv})
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing command: %v", err)
	}
}

func TestTimelineMerges(t *testing.T) {
	s := &sampler{interval: fastStep, maxInterval: fastMax, step: fastStep}
	// 100 minutes at one sample per interval, with one spike.
	n := 0
	for elapsed := time.Duration(0); elapsed < 100*time.Minute; elapsed += s.interval {
		private := uint64(100 << 20)
		if n == 4321 {
			private = 9 << 30
		}
		s.addPoint(2, private)
		n++
	}
	if len(s.points) >= maxPoints || len(s.points) < maxPoints/2-1 {
		t.Errorf("%d points, want between %d and %d", len(s.points), maxPoints/2, maxPoints)
	}
	if s.interval != fastMax || s.step < 10*time.Second {
		t.Errorf("interval %v, step %v after 100 minutes", s.interval, s.step)
	}
	var top uint64
	for _, p := range s.points {
		top = max(top, p.private)
		if p.cores < 1.99 || p.cores > 2.01 {
			t.Fatalf("a merged point has %.2f cores, want 2", p.cores)
		}
	}
	if top != 9<<30 {
		t.Errorf("the spike was lost: highest point %d MiB", top>>20)
	}
}

func TestStore(t *testing.T) {
	st := Store{Dir: filepath.Join(t.TempDir(), "runs"), MaxRuns: 3}
	if got := st.List(); len(got) != 0 {
		t.Fatalf("empty store lists %d runs", len(got))
	}
	if _, err := st.Find("last"); !errors.Is(err, ErrNoRun) {
		t.Errorf("last in an empty store: %v", err)
	}
	t0 := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	ids := []string{"raaaaa", "rbbbbb", "rccccc", "rddddd"}
	for i, id := range ids {
		name := ""
		if i%2 == 1 {
			name = "base"
		}
		r := &Run{V: 1, ID: id, Name: name, Command: []string{"make"}, Cwd: "/x", Start: t0.Add(time.Duration(i) * time.Minute), WallMS: int64(i)}
		if err := st.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	// The oldest went; the rest list newest first.
	var got []string
	for _, r := range st.List() {
		got = append(got, r.ID)
	}
	if strings.Join(got, " ") != "rddddd rccccc rbbbbb" {
		t.Errorf("listed %v", got)
	}
	for ref, want := range map[string]string{"last": "rddddd", "rccccc": "rccccc", "base": "rddddd"} {
		if r, err := st.Find(ref); err != nil || r.ID != want {
			t.Errorf("Find(%q) = %v, %v; want %s", ref, r, err, want)
		}
	}
	for _, ref := range []string{"raaaaa", "nosuch", "../x"} {
		if _, err := st.Find(ref); !errors.Is(err, ErrNoRun) {
			t.Errorf("Find(%q): %v, want ErrNoRun", ref, err)
		}
	}
	a, _ := st.Find("rccccc")
	b, _ := st.Find("rddddd")
	if !Same(a, b) {
		t.Error("two runs of make in /x aren't the same command")
	}
	b.Command = []string{"make", "test"}
	if Same(a, b) {
		t.Error("make and make test are the same command")
	}

	for name, want := range map[string]bool{"base": true, "v1.2_x-y": true, "": false, "last": false, "r7k2mq": false, "a b": false, "-x": false, "../x": false} {
		if ValidName(name) != want {
			t.Errorf("ValidName(%q) = %v", name, !want)
		}
	}
	for range 200 {
		if id := newID(); !idForm.MatchString(id) {
			t.Fatalf("newID made %q", id)
		}
	}
}
