package collecttest

import "fmt"

const (
	gib = 1 << 30
	mib = 1 << 20

	deskBase  = "user.slice/user-1000.slice/user@1000.service/app.slice/app-t3code.slice"
	deskOwned = DeskAgents + "/app-t3code-agents-0123456789ab.slice"

	// The desk's T3 Code cgroups and its one thread.
	DeskAgents = deskBase + "/app-t3code-agents.slice"
	DeskClaude = deskOwned + "/t3code-claude-deadbeef.scope"
	DeskShared = deskOwned + "/t3code-opencode-11111111.scope"
	DeskThread = "1a2b3c4d-1111-4111-8111-111111111111"
)

// Desk is a machine like the one Pulse is for: a browser, a Steam game,
// and two T3 agent scopes, one of them building.
func Desk() *Machine {
	m := New(16, 62*gib, 38*gib)
	p := func(pid, ppid int, comm string, rssMB, sharedMB uint64, args ...string) P {
		return P{PID: pid, PPID: ppid, Comm: comm, Args: args, Start: uint64(pid) * 100,
			RSS: rssMB * mib, Shared: sharedMB * mib, Cgroup: "user.slice/session.scope"}
	}
	add := func(p P, change ...func(*P)) {
		for _, c := range change {
			c(&p)
		}
		m.Add(p)
	}
	in := func(cgroup string) func(*P) { return func(p *P) { p.Cgroup = cgroup } }
	env := func(k, v string) func(*P) {
		return func(p *P) { p.Env = map[string]string{k: v} }
	}

	add(p(1, 0, "systemd", 14, 10, "/usr/lib/systemd/systemd"), in(""))
	m.Add(P{PID: 2, Comm: "kthreadd", Kernel: true})
	m.Add(P{PID: 3, PPID: 2, Comm: "kworker/0:1", Kernel: true})

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
	add(p(300, 1, "claude", 500, 60, "/usr/bin/claude", "--output-format", "stream-json"), in(DeskClaude), env("T3CODE_THREAD_ID", DeskThread),
		func(p *P) { p.Cwd = "/home/u/.t3/worktrees/pulse/t3-8fd0e007" })
	add(p(301, 300, "zsh", 6, 4, "/usr/bin/zsh", "-c", "cargo build\nexit"), in(DeskClaude), env("T3CODE_THREAD_ID", DeskThread))
	add(p(302, 301, "cargo", 90, 20, "cargo", "build", "--release"), in(DeskClaude), env("T3CODE_THREAD_ID", DeskThread))
	add(p(303, 302, "rustc", 2100, 40, "rustc", "--crate-name", "syn", "--edition=2021"), in(DeskClaude))
	add(p(304, 302, "rustc", 1400, 40, "rustc", "--crate-name", "pulse"), in(DeskClaude))
	for pid := 305; pid <= 308; pid++ {
		add(p(pid, 302, "cc", 60, 8, "cc", "-c", "x.c"), in(DeskClaude))
	}
	add(p(309, 301, "gamescope", 80, 30, "gamescope", "--backend", "headless", "--", "godot", "--headless"), in(DeskClaude))
	add(p(310, 1, "opencode", 180, 30, "/usr/bin/opencode", "serve", "--port=34253"), in(DeskShared))

	// Another user's process: nothing but stat and statm can be read.
	m.Add(P{PID: 400, PPID: 1, Comm: "postgres", Start: 40000, RSS: 200 * mib, Shared: 150 * mib, NoEnv: true})

	m.SysFile("class/hwmon/hwmon2/name", "k10temp\n")
	m.SysFile("class/hwmon/hwmon2/temp1_input", "61250\n")
	m.Cgroup(deskBase+"/cpu.weight", "10\n")
	m.Cgroup(deskBase+"/io.weight", "default 10\n")
	m.Cgroup(deskBase+"/pids.max", "127955\n")
	m.Cgroup(DeskAgents+"/memory.high", fmt.Sprint(58061*mib)+"\n")
	m.Cgroup(DeskAgents+"/memory.max", fmt.Sprint(60109*mib)+"\n")
	m.Cgroup(DeskAgents+"/memory.current", fmt.Sprint(6963*mib)+"\n")
	m.Cgroup(DeskClaude+"/cgroup.procs", "300\n301\n302\n303\n304\n305\n306\n307\n308\n309\n")
	m.Cgroup(DeskClaude+"/memory.current", fmt.Sprint(6656*mib)+"\n")
	m.Cgroup(DeskClaude+"/memory.events", "oom 0\noom_kill 1\n")
	m.Cgroup(DeskShared+"/cgroup.procs", "310\n")
	m.Cgroup(DeskShared+"/memory.current", fmt.Sprint(300*mib)+"\n")
	return m
}

// DeskWork is a quarter of a second of the desk's load: the ticks each
// process uses, for Machine.Run.
var DeskWork = map[int]uint64{201: 50, 202: 2, 303: 25, 304: 23, 305: 22, 306: 22, 307: 22, 308: 22, 302: 3, 101: 9, 100: 1}
