package t3

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

var capture = flag.String("capture", "", "with TestLive: also save the live layout as the fixture testdata/<name>")

const fixtureTitle = "Fixture thread"

// TestLive checks, on the running machine, everything Pulse assumes about
// how T3 runs agents, so a T3 update that changes it fails here instead of
// making Pulse show wrong numbers. It runs only inside a T3 agent scope
// (and not with -short), so it skips in CI.
//
// With -capture <name>, it also saves the layout as testdata/<name>, the
// fixture TestFixtures reads.
func TestLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live T3 check skipped with -short")
	}
	thread := os.Getenv(threadVar)
	proc := os.DirFS("/proc")
	self, err := CgroupOf(proc, "self")
	if thread == "" || err != nil || !strings.Contains(self, "/"+agentsSlice+"/") {
		t.Skip("not running inside a T3 agent scope")
	}
	cgroup := os.DirFS("/sys/fs/cgroup")
	l, err := Discover(cgroup, proc, os.Getuid())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	var own *Scope
	for i := range l.Scopes {
		if l.Scopes[i].Path == self {
			own = &l.Scopes[i]
		}
	}

	t.Run("scope", func(t *testing.T) {
		if own == nil {
			t.Fatalf("this test runs in %s, which isn't among the %d scopes found", self, len(l.Scopes))
		}
		if own.ThreadID != thread {
			t.Errorf("scope's agent has thread %q, this process %q", own.ThreadID, thread)
		}
		if !ancestor(own.Agent, os.Getpid()) {
			t.Errorf("agent pid %d isn't an ancestor of this test (pid %d)", own.Agent, os.Getpid())
		}
		// Other scopes may have no thread: T3 starts opencode's server,
		// which threads share, without the variable.
		for _, s := range l.Scopes {
			if s.Agent != 0 && s.ThreadID == "" {
				t.Logf("%s: no thread known for agent %d", s.Unit, s.Agent)
			}
		}
		t.Logf("%d agent scopes; this one is %s in %s, agent pid %d", len(l.Scopes), own.Unit, own.Slice, own.Agent)
	})

	t.Run("limits", func(t *testing.T) {
		lim, ram := l.Limits, memTotal(t)
		if lim.MemoryHigh == 0 || lim.MemoryMax <= lim.MemoryHigh || lim.MemoryMax > ram {
			t.Errorf("agents slice memory.high %d, memory.max %d, RAM %d; want 0 < high < max <= RAM", lim.MemoryHigh, lim.MemoryMax, ram)
		}
		// docs/design.md says agents yield the CPU to a game.
		if lim.CPUWeight == 0 || lim.CPUWeight >= 100 {
			t.Errorf("app-t3code.slice cpu.weight %d; want set and below systemd's default 100", lim.CPUWeight)
		}
		if lim.IOWeight == 0 || lim.IOWeight >= 100 {
			t.Errorf("app-t3code.slice io.weight %d; want set and below the default 100", lim.IOWeight)
		}
		if l.Memory == 0 || l.Memory > lim.MemoryMax {
			t.Errorf("agents slice memory.current %d; want above 0 and at most memory.max %d", l.Memory, lim.MemoryMax)
		}
		if own != nil && (own.Memory == 0 || own.Memory > l.Memory) {
			t.Errorf("this scope's memory.current %d; want above 0 and at most the slice's %d", own.Memory, l.Memory)
		}
		if lim.PidsMax == 0 {
			t.Error("app-t3code.slice has no pids.max")
		}
		t.Logf("memory.high %s, memory.max %s, RAM %s; cpu.weight %d, io.weight %d, pids.max %d",
			gib(lim.MemoryHigh), gib(lim.MemoryMax), gib(ram), lim.CPUWeight, lim.IOWeight, lim.PidsMax)
	})

	// pulse run starts each command in a child cgroup of the agent's scope.
	t.Run("delegation", func(t *testing.T) {
		if own == nil {
			t.Skip("no scope for this test")
		}
		dir := filepath.Join("/sys/fs/cgroup", self)
		b, err := os.ReadFile(filepath.Join(dir, "cgroup.controllers"))
		if err != nil {
			t.Fatal(err)
		}
		have := strings.Fields(string(b))
		for _, c := range []string{"cpu", "io", "memory", "pids"} {
			if !slices.Contains(have, c) {
				t.Errorf("controller %s isn't delegated to the scope (has %v)", c, have)
			}
		}

		name := fmt.Sprintf("pulse-livetest-%d", os.Getpid())
		child := filepath.Join(dir, name)
		if err := os.Mkdir(child, 0o755); err != nil {
			t.Fatalf("can't create a child cgroup in the agent's scope: %v", err)
		}
		t.Cleanup(func() { removeCgroup(t, child) })
		fd, err := os.Open(child)
		if err != nil {
			t.Fatal(err)
		}
		defer fd.Close()

		// Start straight into the child (clone3 with CLONE_INTO_CGROUP),
		// report the cgroup, then burn a little CPU to see it counted.
		cmd := exec.Command("/bin/sh", "-c", "cat /proc/self/cgroup; timeout 0.2 sh -c 'while :; do :; done'; true")
		cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(fd.Fd())}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("starting a command in the child cgroup: %v", err)
		}
		want := "0::/" + self + "/" + name
		if !strings.Contains(string(out), want) {
			t.Errorf("command ran in\n%s\nwant %s", out, want)
		}
		st, err := os.ReadFile(filepath.Join(child, "cpu.stat"))
		if err != nil {
			t.Fatal(err)
		}
		usage := statField(string(st), "usage_usec")
		if usage == 0 {
			t.Errorf("child cgroup counted no CPU time:\n%s", st)
		}
		t.Logf("command started in %s; it used %d µs of CPU", name, usage)
	})

	t.Run("systemd", func(t *testing.T) {
		if own == nil {
			t.Skip("no scope for this test")
		}
		if _, err := exec.LookPath("systemctl"); err != nil {
			t.Skip("no systemctl")
		}
		out, err := exec.Command("systemctl", "--user", "show", own.Unit, "-p", "Delegate", "-p", "OOMPolicy").Output()
		if err != nil {
			t.Fatal(err)
		}
		props := map[string]string{}
		for line := range strings.SplitSeq(string(out), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				props[k] = v
			}
		}
		// docs/design.md: an OOM kill in the scope stops the whole agent.
		if props["Delegate"] != "yes" || props["OOMPolicy"] != "stop" {
			t.Errorf("%s: Delegate=%s OOMPolicy=%s; want yes and stop", own.Unit, props["Delegate"], props["OOMPolicy"])
		}
	})

	t.Run("thread", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		start := time.Now()
		th, err := ShowThread(ctx, CLI(os.Getenv), thread)
		if err != nil {
			t.Fatal(err)
		}
		if th.ID != thread || th.Title == "" || th.Status == "" {
			t.Errorf("t3 thread show: id %q, title set %v, status %q; want this thread, a title and a status", th.ID, th.Title != "", th.Status)
		}
		t.Logf("t3 thread show took %v; status %s, model %s", time.Since(start).Round(time.Millisecond), th.Status, th.Model)
	})

	if *capture != "" {
		writeFixture(t, *capture, cgroup, proc, l, thread)
	}
}

// ancestor reports whether a is pid or one of its ancestors.
func ancestor(a, pid int) bool {
	proc := os.DirFS("/proc")
	for range 64 {
		if pid == a {
			return true
		}
		if pid <= 1 {
			return false
		}
		st, err := readStat(proc, pid)
		if err != nil {
			return false
		}
		pid = st.ppid
	}
	return false
}

func memTotal(t *testing.T) uint64 {
	t.Helper()
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			kb, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return kb * 1024
		}
	}
	t.Fatal("no MemTotal in /proc/meminfo")
	return 0
}

func statField(s, key string) uint64 {
	for line := range strings.SplitSeq(s, "\n") {
		if v, ok := strings.CutPrefix(line, key+" "); ok {
			n, _ := strconv.ParseUint(v, 10, 64)
			return n
		}
	}
	return 0
}

func gib(b uint64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }

// removeCgroup removes an emptied cgroup. The kernel can report it busy
// for a moment after its last process was reaped, so it retries briefly.
func removeCgroup(t *testing.T, dir string) {
	var err error
	for range 50 {
		if err = syscall.Rmdir(dir); err == nil || os.IsNotExist(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("removing %s: %v", dir, err)
}

// writeFixture saves the files Discover reads as testdata/<name>. Only
// cgroup files, /proc/<pid>/stat and the thread variable from each
// process's environment are kept; the thread's title and link are replaced.
func writeFixture(t *testing.T, name string, cgroup, proc fs.FS, l *Layout, thread string) {
	t.Helper()
	dst := filepath.Join("testdata", name)
	if _, err := os.Stat(dst); err == nil {
		t.Fatalf("%s exists; pick another name", dst)
	}
	cgDst, procDst := filepath.Join(dst, "cgroup"), filepath.Join(dst, "proc")
	write := func(to string, b []byte) {
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	copyCgroup := func(dir string, files ...string) {
		for _, f := range files {
			if b, err := fs.ReadFile(cgroup, path.Join(dir, f)); err == nil {
				write(filepath.Join(cgDst, dir, f), b)
			}
		}
	}
	agents := path.Join(l.Root, agentsSlice)
	copyCgroup(l.Root, "cpu.weight", "io.weight", "pids.max", "cgroup.controllers", "cgroup.subtree_control")
	copyCgroup(agents, "memory.high", "memory.max", "memory.current", "cgroup.controllers", "cgroup.subtree_control")
	for _, s := range l.Scopes {
		copyCgroup(s.Path, "cgroup.procs", "cgroup.controllers", "memory.current", "memory.events", "cpu.stat")
		b, err := fs.ReadFile(cgroup, path.Join(s.Path, "cgroup.procs"))
		if err != nil {
			continue
		}
		for f := range strings.FieldsSeq(string(b)) {
			st, err := fs.ReadFile(proc, path.Join(f, "stat"))
			if err != nil {
				continue // exited since
			}
			write(filepath.Join(procDst, f, "stat"), st)
			env := ""
			if pid, err := strconv.Atoi(f); err == nil {
				if v := envVar(proc, pid, threadVar); v != "" {
					env = threadVar + "=" + v + "\x00"
				}
			}
			write(filepath.Join(procDst, f, "environ"), []byte(env))
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	raw, err := ShowThreadJSON(ctx, CLI(os.Getenv), thread)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if th, ok := doc["thread"].(map[string]any); ok {
		th["title"] = fixtureTitle
		th["link"] = "https://app.t3.codes/threads/fixture"
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dst, "thread-show.json"), append(b, '\n'))

	got, err := Discover(os.DirFS(cgDst), os.DirFS(procDst), os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	b, err = json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dst, "layout.golden.json"), append(b, '\n'))
	t.Logf("saved fixture %s: %d scopes", dst, len(got.Scopes))
}
