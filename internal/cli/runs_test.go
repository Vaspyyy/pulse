package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Vaspyyy/pulse/collect"
	"github.com/Vaspyyy/pulse/collect/collecttest"
	"github.com/Vaspyyy/pulse/record"
)

func init() { time.Local = time.UTC } // run times are printed in local time

// recorded fills a store with three runs: a test suite before and after a
// change (the second next to a running game), and a failed make elsewhere.
func recorded(t *testing.T) func(*Env) {
	t.Helper()
	dir := t.TempDir()
	st := record.Store{Dir: filepath.Join(dir, "runs")}
	wave := func(n int, top float32) (cores []float32, mem []uint32) {
		for i := range n {
			x := float32(i%20) / 19
			cores = append(cores, top*x)
			mem = append(mem, uint32(float32(i)/float32(n)*top*600))
		}
		return
	}
	cores, mem := wave(120, 15.2)
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
		UnsampledMS: 20000,
		Timeline:    record.Timeline{StepMS: 400, Cores: cores, Private: mem},
	}
	after := *before
	after.ID, after.Name = "r9q4xt", "change"
	after.Repo = &record.Repo{Root: "/home/u/pulse", Head: "def4567000000000000000000000000000000cd", Branch: "main", State: "dirty"}
	after.Start = time.Date(2026, 11, 9, 22, 40, 0, 0, time.UTC)
	after.WallMS, after.UserMS = 61000, 660000
	after.PeakPrivate, after.LargestRSS, after.LargestName = 11776*mib, 4506*mib, "pulse-test"
	after.Stall = &record.Stall{CPUMS: 9800, MemoryMS: 1200, IOMS: 600}
	after.Other = record.Other{Known: true, AvgCores: 2.1, PeakCores: 2.6, Games: []string{"Hades"}, Disturbed: true}
	after.GPU = nil
	make := &record.Run{
		V: 1, ID: "rmmmm2", Command: []string{"make", "-j8"}, Cwd: "/home/u/other",
		Start: time.Date(2026, 11, 7, 9, 5, 0, 0, time.UTC), WallMS: 1200, Exit: 2, Method: "tree", NCPU: 16,
		UserMS: 2100, SystemMS: 300, PeakPrivate: 300 * mib, SampleMS: 500, Samples: 3, LargestRSS: 310 * mib,
		Other:    record.Other{Known: true, AvgCores: 5.5, PeakCores: 5.5, Disturbed: true},
		Left:     []string{"sccache (4821.kfx)"},
		Timeline: record.Timeline{StepMS: 500, Cores: []float32{1.5, 2.2, 1.1}, Private: []uint32{100, 300, 250}},
	}
	for _, r := range []*record.Run{make, before, &after} {
		if err := st.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	return func(env *Env) { env.StateDir = dir }
}

func TestRunsGolden(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"runs", []string{"runs"}},
		{"show-last", []string{"show"}},
		{"show-name", []string{"show", "base"}},
		{"show-tree", []string{"show", "rmmmm2"}},
		{"diff", []string{"diff"}},
		{"diff-names", []string{"diff", "change", "base"}},
		{"diff-other", []string{"diff", "rmmmm2"}},
	}
	m := collecttest.New(16, 62*gib, 50*gib)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, errOut, code := runEnv(t, m, nil, recorded(t), c.args...)
			if code != 0 || errOut != "" {
				t.Fatalf("exit %d, stderr %q", code, errOut)
			}
			golden := filepath.Join("testdata", c.name+".txt")
			if *update {
				if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if out != string(want) {
				t.Errorf("output differs from %s (rerun with -update if the change is right); got:\n%s", golden, out)
			}
		})
	}
}

func TestRunsErrors(t *testing.T) {
	m := collecttest.New(16, 62*gib, 50*gib)
	cases := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"show", "rzzzzz"}, 1, "no such run: rzzzzz"},
		{[]string{"show", "nope"}, 1, "nope is neither a run id nor a run's name"},
		{[]string{"show", "a", "b"}, 2, "show takes one run"},
		{[]string{"diff", "a", "b", "c"}, 1, "diff takes two runs"},
		{[]string{"run"}, 2, "run needs a command"},
		{[]string{"run", "-n"}, 2, "-n needs a name"},
		{[]string{"run", "-n", "last", "--", "true"}, 2, "can't name a run"},
		{[]string{"run", "--frob", "true"}, 2, "unknown flag --frob"},
	}
	for _, c := range cases {
		out, errOut, code := runEnv(t, m, nil, recorded(t), c.args...)
		if code != c.code || out != "" || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: exit %d, stdout %q, stderr %q; want exit %d and %q", c.args, code, out, errOut, c.code, c.want)
		}
	}
	// An empty store says so; the only run of a command has nothing to
	// be compared with.
	empty := func(env *Env) { env.StateDir = t.TempDir() }
	if out, _, code := runEnv(t, m, nil, empty, "runs"); code != 0 || !strings.HasPrefix(out, "nothing is recorded yet\n") {
		t.Errorf("runs in an empty store: exit %d, %q", code, out)
	}
	if _, errOut, code := runEnv(t, m, nil, empty, "diff"); code != 1 || !strings.Contains(errOut, "nothing is recorded yet") {
		t.Errorf("diff in an empty store: exit %d, %q", code, errOut)
	}
}

// pulse run on the real machine: the command's output and exit code pass
// through, the summary goes to stderr, and the run is kept.
func TestRunCommand(t *testing.T) {
	dir := t.TempDir()
	live := func(env *Env) {
		env.Roots, env.StateDir, env.Getenv = collect.Live(), dir, os.Getenv
	}
	out, errOut, code := runEnv(t, nil, nil, live, "run", "-n", "smoke", "--", "sh", "-c", "echo hi; exit 3")
	if code != 3 || out != "hi\n" {
		t.Errorf("exit %d, stdout %q; want the command's 3 and its output", code, out)
	}
	for _, want := range []string{"pulse: sh -c echo hi; exit 3, exit 3, ", ", recorded as r", " (smoke)\n", "\n  cpu    ", "\nnext: pulse show r"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut)
		}
	}
	st := record.Store{Dir: filepath.Join(dir, "runs")}
	r, err := st.Find("smoke")
	if err != nil || r.Exit != 3 || strings.Join(r.Command, " ") != "sh -c echo hi; exit 3" {
		t.Errorf("stored run %+v, %v", r, err)
	}
	// Flags after the command's name are the command's own.
	out, _, code = runEnv(t, nil, nil, live, "run", "echo", "-n", "x")
	if code != 0 || out != "x" {
		t.Errorf("run echo -n x: exit %d, stdout %q", code, out)
	}
	if _, errOut, code := runEnv(t, nil, nil, live, "run", "--", "/nonexistent/cmd"); code != 127 || !strings.Contains(errOut, "no such file") {
		t.Errorf("missing command: exit %d, %q", code, errOut)
	}
}
