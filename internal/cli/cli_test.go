package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Vaspyyy/pulse/collect/collecttest"
	"github.com/Vaspyyy/pulse/collect/gpu"
	"github.com/Vaspyyy/pulse/collect/t3"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

const (
	gib         = 1 << 30
	mib         = 1 << 20
	base        = "user.slice/user-1000.slice/user@1000.service/app.slice/app-t3code.slice"
	agentsSlice = base + "/app-t3code-agents.slice"
	owned       = agentsSlice + "/app-t3code-agents-0123456789ab.slice"
	claude      = owned + "/t3code-claude-deadbeef.scope"
	shared      = owned + "/t3code-opencode-11111111.scope"
	thread      = "1a2b3c4d-1111-4111-8111-111111111111"
)

// desk is a machine like the one Pulse is for: a browser, a Steam game,
// and two T3 agent scopes, one of them building.
func desk() *collecttest.Machine {
	m := collecttest.New(16, 62*gib, 38*gib)
	p := func(pid, ppid int, comm string, rssMB, sharedMB uint64, args ...string) collecttest.P {
		return collecttest.P{PID: pid, PPID: ppid, Comm: comm, Args: args, Start: uint64(pid) * 100,
			RSS: rssMB * mib, Shared: sharedMB * mib, Cgroup: "user.slice/session.scope"}
	}
	add := func(p collecttest.P, change ...func(*collecttest.P)) {
		for _, c := range change {
			c(&p)
		}
		m.Add(p)
	}
	in := func(cgroup string) func(*collecttest.P) { return func(p *collecttest.P) { p.Cgroup = cgroup } }
	env := func(k, v string) func(*collecttest.P) {
		return func(p *collecttest.P) { p.Env = map[string]string{k: v} }
	}

	add(p(1, 0, "systemd", 14, 10, "/usr/lib/systemd/systemd"), in(""))
	m.Add(collecttest.P{PID: 2, Comm: "kthreadd", Kernel: true})
	m.Add(collecttest.P{PID: 3, PPID: 2, Comm: "kworker/0:1", Kernel: true})

	add(p(100, 1, "firefox", 600, 200, "/usr/lib/firefox/firefox"))
	for pid := 101; pid <= 103; pid++ {
		add(p(pid, 100, "Web Content", 400, 150, "/usr/lib/firefox/firefox", "-contentproc"))
	}

	// Steam's client has no app id; the game under its reaper has.
	add(p(190, 1, "steam", 300, 100, "/home/u/.local/share/Steam/ubuntu12_32/steam"))
	add(p(200, 190, "reaper", 2, 1, "/home/u/.local/share/Steam/ubuntu12_32/reaper", "SteamLaunch", "AppId=1145360", "--", "proton", "run"))
	add(p(201, 200, "Hades.exe", 4100, 300, `Z:\games\Hades\Hades.exe`), env("SteamAppId", "1145360"))
	add(p(202, 200, "wineserver", 30, 10, "/usr/bin/wineserver"), env("SteamAppId", "1145360"))

	// An agent that builds, and runs a test under a headless gamescope:
	// that's no game.
	add(p(300, 1, "claude", 500, 60, "/usr/bin/claude", "--output-format", "stream-json"), in(claude), env("T3CODE_THREAD_ID", thread),
		func(p *collecttest.P) { p.Cwd = "/home/u/.t3/worktrees/pulse/t3-8fd0e007" })
	add(p(301, 300, "zsh", 6, 4, "/usr/bin/zsh", "-c", "cargo build\nexit"), in(claude), env("T3CODE_THREAD_ID", thread))
	add(p(302, 301, "cargo", 90, 20, "cargo", "build", "--release"), in(claude), env("T3CODE_THREAD_ID", thread))
	add(p(303, 302, "rustc", 2100, 40, "rustc", "--crate-name", "syn", "--edition=2021"), in(claude))
	add(p(304, 302, "rustc", 1400, 40, "rustc", "--crate-name", "pulse"), in(claude))
	for pid := 305; pid <= 308; pid++ {
		add(p(pid, 302, "cc", 60, 8, "cc", "-c", "x.c"), in(claude))
	}
	add(p(309, 301, "gamescope", 80, 30, "gamescope", "--backend", "headless", "--", "godot", "--headless"), in(claude))
	add(p(310, 1, "opencode", 180, 30, "/usr/bin/opencode", "serve", "--port=34253"), in(shared))

	// Another user's process: nothing but stat and statm can be read.
	m.Add(collecttest.P{PID: 400, PPID: 1, Comm: "postgres", Start: 40000, RSS: 200 * mib, Shared: 150 * mib, NoEnv: true})

	m.SysFile("class/hwmon/hwmon2/name", "k10temp\n")
	m.SysFile("class/hwmon/hwmon2/temp1_input", "61250\n")
	m.Cgroup(base+"/cpu.weight", "10\n")
	m.Cgroup(base+"/io.weight", "default 10\n")
	m.Cgroup(base+"/pids.max", "127955\n")
	m.Cgroup(agentsSlice+"/memory.high", fmt.Sprint(58061*mib)+"\n")
	m.Cgroup(agentsSlice+"/memory.max", fmt.Sprint(60109*mib)+"\n")
	m.Cgroup(agentsSlice+"/memory.current", fmt.Sprint(6963*mib)+"\n")
	m.Cgroup(claude+"/cgroup.procs", "300\n301\n302\n303\n304\n305\n306\n307\n308\n309\n")
	m.Cgroup(claude+"/memory.current", fmt.Sprint(6656*mib)+"\n")
	m.Cgroup(claude+"/memory.events", "oom 0\noom_kill 1\n")
	m.Cgroup(shared+"/cgroup.procs", "310\n")
	m.Cgroup(shared+"/memory.current", fmt.Sprint(300*mib)+"\n")
	return m
}

// work is a quarter of a second of the desk's load.
var work = map[int]uint64{201: 50, 202: 2, 303: 25, 304: 23, 305: 22, 306: 22, 307: 22, 308: 22, 302: 3, 101: 9, 100: 1}

// deskGPU is the desk's GPU while the game runs.
func deskGPU(env *Env) {
	env.GPU = func() []gpu.Device {
		return []gpu.Device{{Name: "RTX 3070", Vendor: "nvidia", Util: 71, Power: 168, PowerLimit: 240,
			MemUsed: 5939 * mib, MemTotal: 8 * gib, Temp: 64,
			Procs: []gpu.Proc{{PID: 100, Mem: 300 * mib, Util: 3}, {PID: 201, Mem: 5222 * mib, Util: 68}, {PID: 99999, Mem: 50 * mib}}}}
	}
}

func run(t *testing.T, m *collecttest.Machine, used map[int]uint64, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runEnv(t, m, used, nil, args...)
}

func runEnv(t *testing.T, m *collecttest.Machine, used map[int]uint64, change func(*Env), args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := Env{
		Getenv: func(string) string { return "" },
		UID:    1000,
		Stdout: &out,
		Stderr: &errOut,
		Sleep: func(time.Duration) {
			if m != nil {
				m.Run(25, used)
			}
		},
		// 01:00:00 after boot.
		Now: func() time.Time { return time.Unix(collecttest.BootTime+3600, 0) },
		SteamName: func(id string) string {
			if id == "1145360" {
				return "Hades"
			}
			return ""
		},
		Threads: func(ids []string) map[string]t3.Thread {
			return map[string]t3.Thread{thread: {ID: thread, Title: "Make the build faster, please, without breaking anything", Model: "claude-opus-5-5"}}
		},
		ConfigPath: "/home/u/.config/pulse-monitor/config",
	}
	if m != nil {
		env.Roots = m.Roots()
	}
	if change != nil {
		change(&env)
	}
	code = Run(args, env)
	return out.String(), errOut.String(), code
}

func TestGolden(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"card", nil},
		{"card-b6", []string{"-b", "6"}},
		{"load", []string{"load"}},
		{"top", []string{"top"}},
		{"top-mem", []string{"top", "--by=mem", "-b", "20"}},
		{"top-gpu", []string{"top", "--by", "gpu"}},
		{"tree-game", []string{"tree", "200"}},
		{"help-config", []string{"help", "config"}},
		{"tree-build", []string{"tree", "cargo", "-b", "20"}},
		{"tree-name", []string{"tree", "rustc"}},
		{"tree-one", []string{"tree", "310"}},
		{"agents", []string{"agents"}},
		{"help", []string{"--help"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, errOut, code := runEnv(t, desk(), work, deskGPU, c.args...)
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
			// The design's budget: 12 lines unless more were asked for.
			if n := strings.Count(out, "\n"); !strings.Contains(strings.Join(c.args, " "), "-b") && !strings.HasPrefix(c.name, "help") && n > defaultBudget {
				t.Errorf("%d lines, over the budget of %d", n, defaultBudget)
			}
		})
	}
}

func TestTinyBudget(t *testing.T) {
	out, _, code := run(t, desk(), work, "-b", "1")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if code != 0 || len(lines) != 5 || !strings.HasPrefix(lines[0], "busy: ") || !strings.HasPrefix(lines[1], "… 6 more lines; -b 10") {
		t.Errorf("exit %d, got:\n%s", code, out)
	}
}

func TestQuiet(t *testing.T) {
	m := collecttest.New(16, 62*gib, 50*gib)
	m.Add(collecttest.P{PID: 100, PPID: 1, Comm: "bash", Args: []string{"-bash"}, Start: 100, RSS: 8 * mib})
	out, _, code := run(t, m, map[int]uint64{100: 5}, "load")
	if want := "quiet: cpu 0.2 of 16 cores, no game, no memory pressure\n"; code != 0 || out != want {
		t.Errorf("exit %d, got %q, want %q", code, out, want)
	}
	// Without T3, agents says so and fails.
	_, errOut, code := run(t, m, nil, "agents")
	if code != 1 || !strings.Contains(errOut, "runs no agents") {
		t.Errorf("agents without T3: exit %d, stderr %q", code, errOut)
	}
}

func TestBusyReasons(t *testing.T) {
	m := collecttest.New(4, 62*gib, 50*gib)
	m.Pressure(0, 14.2, 0)
	m.Add(collecttest.P{PID: 100, PPID: 1, Comm: "stress", Args: []string{"stress"}, Start: 100, RSS: 8 * mib})
	m.Cgroup(agentsSlice+"/memory.high", fmt.Sprint(56*gib)+"\n")
	m.Cgroup(agentsSlice+"/memory.max", fmt.Sprint(58*gib)+"\n")
	m.Cgroup(agentsSlice+"/memory.current", fmt.Sprint(55*gib)+"\n")
	out, _, _ := run(t, m, map[int]uint64{100: 75}, "load")
	want := "busy: cpu at 3.0 of 4 cores, most from stress (3.0 cores); memory pressure 14%; t3 agents use 55.0 GB, 1.0 GB below their throttle limit\n"
	if out != want {
		t.Errorf("got  %q\nwant %q", out, want)
	}
}

func TestGPULoad(t *testing.T) {
	m := collecttest.New(16, 62*gib, 50*gib)
	m.Add(collecttest.P{PID: 100, PPID: 1, Comm: "python3", Args: []string{"python3", "train.py"}, Start: 100, RSS: 8 * mib})
	m.Add(collecttest.P{PID: 101, PPID: 1, Comm: "kwin_wayland", Args: []string{"kwin_wayland"}, Start: 100, RSS: 8 * mib})
	dev := gpu.Device{Name: "RTX 3070", Util: 45, Power: 33, PowerLimit: 240, MemTotal: 8 * gib,
		Procs: []gpu.Proc{{PID: 100, Util: 5}, {PID: 101, Util: 27}}}
	with := func(d gpu.Device, config string) func(*Env) {
		return func(env *Env) {
			env.GPU = func() []gpu.Device { return []gpu.Device{d} }
			env.Config = config
		}
	}
	// A desktop at rest: the GPU says 45 % because it clocked down, but
	// draws an eighth of its power.
	out, _, _ := runEnv(t, m, nil, with(dev, ""), "load")
	if want := "quiet: cpu 0.0 of 16 cores, gpu draws 33 of 240 W, no game, no memory pressure\n"; out != want {
		t.Errorf("at rest: %q, want %q", out, want)
	}
	// The limit is the config file's to set.
	out, _, _ = runEnv(t, m, nil, with(dev, "gpu_busy = 10\n"), "load")
	if want := "busy: gpu draws 33 of 240 W, most from kwin_wayland (27%)\n"; out != want {
		t.Errorf("with gpu_busy = 10: %q, want %q", out, want)
	}
	// Real load, by something that isn't a game.
	dev.Power, dev.Procs[0].Util = 190, 88
	out, _, _ = runEnv(t, m, nil, with(dev, ""), "load")
	if want := "busy: gpu draws 190 of 240 W, most from python3 (88%)\n"; out != want {
		t.Errorf("under load: %q, want %q", out, want)
	}
	// A GPU that reports no power is judged by its utilisation.
	dev.Power, dev.PowerLimit = 0, 0
	out, _, _ = runEnv(t, m, nil, with(dev, ""), "load")
	if want := "busy: gpu at 45%, most from python3 (88%)\n"; out != want {
		t.Errorf("without power: %q, want %q", out, want)
	}
}

func TestConfig(t *testing.T) {
	cfg, problems := parseConfig(`
# my limits
cpu_busy = 75          # percent
memory_pressure=5
agent_headroom = 0.5
game_process = lutris-wrapper
game_process = retroarch
game_env = MY_GAME
gpu_busy = 250
cpu_busy
frobnicate = 1
`)
	if cfg.Thresholds.CPUShare != 0.75 || cfg.Thresholds.MemPressure != 5 || cfg.Thresholds.AgentHeadroom != 512*mib {
		t.Errorf("thresholds %+v", cfg.Thresholds)
	}
	// A bad line is reported and leaves the default.
	if cfg.Thresholds.GPULoad != 30 {
		t.Errorf("gpu limit %v after a bad line, want the default 30", cfg.Thresholds.GPULoad)
	}
	if strings.Join(cfg.GameProcess, ",") != "lutris-wrapper,retroarch" || strings.Join(cfg.GameEnv, ",") != "MY_GAME" {
		t.Errorf("markers %v %v", cfg.GameProcess, cfg.GameEnv)
	}
	want := []string{
		"line 9: gpu_busy = 250: want a number from 0 to 100",
		"line 10: want key = value",
		`line 11: unknown key "frobnicate"`,
	}
	if strings.Join(problems, "\n") != strings.Join(want, "\n") {
		t.Errorf("problems:\n%s\nwant:\n%s", strings.Join(problems, "\n"), strings.Join(want, "\n"))
	}

	// Configured markers make games, and problems go to stderr without
	// stopping the command.
	m := collecttest.New(16, 62*gib, 50*gib)
	m.Add(collecttest.P{PID: 100, PPID: 1, Comm: "retroarch", Args: []string{"/usr/bin/retroarch"}, Start: 100, RSS: 300 * mib})
	m.Add(collecttest.P{PID: 101, PPID: 1, Comm: "wine", Args: []string{"wine"}, Start: 100, RSS: 100 * mib, Env: map[string]string{"MY_GAME": "Quail"}})
	out, errOut, code := runEnv(t, m, nil, func(env *Env) {
		env.Config = "game_process = retroarch\ngame_env = MY_GAME\nnonsense\n"
	}, "load")
	if want := "busy: a game (retroarch) uses 0.0 cores and 300 MB; a game (Quail) uses 0.0 cores and 100 MB\n"; code != 0 || out != want {
		t.Errorf("exit %d, got %q, want %q", code, out, want)
	}
	if want := "pulse: /home/u/.config/pulse-monitor/config: line 3: want key = value\n"; errOut != want {
		t.Errorf("stderr %q, want %q", errOut, want)
	}
}

func TestErrors(t *testing.T) {
	m := desk()
	cases := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"tree", "300.aaa"}, 1, `300.aaa is gone; pid 300 is now "claude" (300.`},
		{[]string{"tree", "999.abc"}, 1, "999.abc is gone, and nothing has pid 999 now"},
		{[]string{"tree", "999"}, 1, "no process with pid 999"},
		{[]string{"tree", "nosuch"}, 1, `no process named "nosuch"`},
		{[]string{"tree"}, 2, "tree takes one"},
		{[]string{"bogus"}, 2, "unknown command"},
		{[]string{"top", "--by", "io"}, 2, "want cpu, mem or gpu"},
		{[]string{"-b"}, 2, "needs a value"},
		{[]string{"--frob"}, 2, "unknown flag"},
	}
	for _, c := range cases {
		out, errOut, code := run(t, m, nil, c.args...)
		if code != c.code || out != "" || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: exit %d, stdout %q, stderr %q; want exit %d and %q", c.args, code, out, errOut, c.code, c.want)
		}
	}
}

func TestThreadCache(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state", "t3-threads.json")
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	var asked atomic.Int32 // show runs in parallel
	show := func(_ context.Context, id string) (t3.Thread, error) {
		asked.Add(1)
		if id == "bad" {
			return t3.Thread{}, errors.New("no such thread")
		}
		return t3.Thread{ID: id, Title: "Title of " + id, Model: "m", Status: "running"}, nil
	}
	got := lookupThreads([]string{"a", "bad"}, file, now, show)
	if len(got) != 1 || got["a"].Title != "Title of a" || asked.Load() != 2 {
		t.Fatalf("first lookup: %v after %d calls", got, asked.Load())
	}
	// Within ten minutes the answer comes from the file; failures aren't kept.
	got = lookupThreads([]string{"a", "bad"}, file, now.Add(9*time.Minute), show)
	if got["a"].Title != "Title of a" || got["a"].Status != "" || asked.Load() != 3 {
		t.Errorf("second lookup: %v after %d calls; want the kept title, no status, and one call for the failed id", got, asked.Load())
	}
	got = lookupThreads([]string{"a"}, file, now.Add(11*time.Minute), show)
	if got["a"].Title != "Title of a" || asked.Load() != 4 {
		t.Errorf("after ten minutes: %v after %d calls; want T3 asked again", got, asked.Load())
	}
}
