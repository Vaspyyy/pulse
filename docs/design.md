# Pulse: design

Status: draft, 2026-11-09. Built from a pitch for "Pulse as Prism's observability layer", then checked against Prism v0.8.1 (`docs/refs.md`, `output.md`, `context.md`, `evidence.md`, `roadmap.md`), crux 0.7.0, recap 0.4.1, T3 Code's agent scopes, Heroic 2.22.4 and the machine it's for. Second pass the same day: the open questions on handles, run cgroups, NVIDIA, memory, game detection and the name are decided, most of them by tests on the machine. Third pass, 2026-11-10: thread titles come from T3's own CLI, and the first code exists: `collect/t3`, with a live test of everything this doc assumes about T3. Fourth pass, 2026-11-11: M0 is built (collector, handles, the card, `load`, `top`, `tree`, `agents`, SKILL.md), and the live test found that not every agent carries a thread id (see [T3 Code](#t3-code)).

Pulse is a personal tool for one machine, where agents always run in T3 Code. So T3 is a first-class target, not an optional extra.

## In one paragraph

Pulse is a Linux system monitor with two front ends on one collector: a TUI for people, and a short, budgeted text interface for agents and scripts. It works on its own, with no repo and no other tools. Inside a git repo it also says which processes belong to that repo, to the current agent session or to a crux run, and how it knows. It can record what a command used (`pulse run`) and compare two recordings (`pulse diff`). That's where it joins Prism: crux runs the command, Pulse measures it, recap shows what changed between two measured runs.

## Goals

1. **A monitor worth opening.** Instant launch; CPU, memory, GPU, temperatures, disks, network and a process tree; keyboard driven, themed, smooth graphs.
2. **Answers for agents in a few lines.** "What is using the machine?", "how heavy is my build?", "is the machine busy right now?", each within a line budget and with `next:` lines, never a 200-line `ps aux`.
3. **Measured runs.** Record a command's CPU time, peak memory, GPU and IO, and compare two runs.
4. **Honest attribution.** Group processes by repo, worktree, agent session and crux run, and always say what the grouping is based on.
5. **Cheap.** Pulse must never be the reason a game on the same machine stutters.

## Non-goals

- A permanent daemon or background service (also a Prism non-goal). See [history without a daemon](#history-without-a-daemon).
- Profiling. Pulse says which process got heavier, not which function. perf, pprof and friends stay separate tools.
- Remote hosts, Prometheus export, alerting.
- macOS and Windows in v1. The collector sits behind an interface so they can come later.
- Needing root. Everything works as the user; what can't be read (other users' processes) shows as unknown.
- Other people's setups. Without T3, NVIDIA or systemd, Pulse shows less but never breaks; making those setups as good isn't a goal.

## Where it fits

| Tool | Answers |
| --- | --- |
| glint | what is here (files, structure) |
| seek | where is it used (code search) |
| recap | what changed (git history) |
| crux | did it build and pass (builds, tests, lint) |
| **pulse** | what is it using (processes, memory, CPU, GPU) |

The four existing tools are about code, and Prism's shared layer (refs, the JSON envelope, context, evidence) is built around naming code. Pulse is the first tool about runtime. That's what makes it a coherent fifth tool, and also what needs care: processes are not code, so Pulse can't just borrow refs for them.

Prism stays a Go library (v1 has no `prism` binary). Pulse imports `prism/repo` and `prism/report` like the other tools.

## Changes from the pitch

The pitch holds up. Checking it against Prism's actual docs changes five things:

| Pitch | Here | Why |
| --- | --- | --- |
| Processes get Prism refs like `pulse:proc:4821` | Processes get their own handle (`4821.kfx`: the pid plus a check on its start time); refs stay for code | `prism/ref` splits at the first `:`, so `pulse:proc:4821` already means "file `pulse`, target `proc:4821`". Prism JSON also reserves the fields `ref`, `in`, `test`, `target`, `from`, `to` and `old_ref` for code refs: `report.Refs` collects them and the cross-tool test resolves each one in seek. A process id in there breaks both. |
| Agents query with `--json` | Agents read the text output (`-A`, the default when stdout isn't a terminal); `--json` is for programs | Prism's output rule: the text report is shorter, so agents shouldn't read JSON into their context |
| Builds on "the reference protocol in v0.2.1" | Builds on Prism v0.8.x, schema `prism/1` | v0.2.1 predates context, marks, `next:` lines and the evidence rules, which are what Pulse plugs into |
| Test whether agents follow runtime refs | Test whether agents run `pulse` at all, and follow its `next:` lines | The 52-run benchmark (2026-11-08, on prism v0.2.1) found agents never used refs once. `next:` lines came after it, and an agent can follow them without learning any syntax |
| `crux test` attaches a Pulse telemetry ref | crux records CPU time and peak memory itself, saves the run in Pulse's store, and points to it with a `next:` line | crux launches the command, so it knows the exact process tree; no attribution guesswork needed |

Kept as pitched: two interfaces on one collector, standalone first, no daemon, pid plus start time as identity, separate ids for recordings, `pulse project`, `pulse diff`, and the bench idea (later, in crux).

## Use cases

**People**

- Glance at the machine; find and kill what eats memory.
- See what each agent uses, and how close the agents are to T3's memory limit. T3 runs every agent in its own cgroup scope (see [T3 Code](#t3-code)), so Pulse can show per-agent CPU and memory exactly. "Which agent is making my game stutter" gets a one-line answer.

**Agents**

- *Is the machine busy?* Before heavy work or timing measurements. On a machine that's also used for gaming while agents work, agents are asked to skip measurements while it's loaded, and today they have no cheap way to tell. `pulse load` answers in one line: `busy: a game (gamescope) uses 71% GPU and 2.1 cores`.
- *How heavy was this?* `pulse run -- cargo test`, or crux's resource line.
- *Did it get heavier?* `pulse diff` between two runs, then recap between their commits.
- *Why did it die?* OOM kills (from `/proc/vmstat` and cgroup `memory.events`) and memory pressure (PSI) show in the card, so "killed by signal 9" gets a likely cause.

## Architecture

```
  TUI (people)         text (agents)          --json (programs)
        \                    |                       /
         +------------- query layer ---------------+
         |  snapshot, group, tree, attribute, load |
         +-----------------------------------------+
                  |                       |
              collector                recorder
     /proc  cgroup v2  hwmon  GPU    (pulse run, run store)
                  |
          prism adapter: prism/repo, prism/report,
          session detection, crux's last run
```

| Package | Imports | Notes |
| --- | --- | --- |
| `collect` | stdlib | reads the machine. No TUI or Prism deps, so crux can import it. **Exists**: CPU, memory, pressure, processes, handles |
| `collect/collecttest` | `collect` | builds a fake machine (`/proc` and `/sys` trees) for tests |
| `collect/t3` | stdlib | T3's agent slices, scopes, limits, memory use and threads. **Exists**, with its tests |
| `collect/gpu` | `purego` | NVML loaded at runtime; AMD and Intel from sysfs |
| `record` | `collect` | samples a process tree while it runs; owns the run store format |
| `internal/query` | `collect`, `record` | grouping, trees, games, the load verdict, diffs. **Exists**, without diffs |
| `attrib` | `query`, `prism/repo` | repo, session and crux attribution, each with a basis |
| `internal/cli` | `query`, `attrib`, `prism/report` | text and JSON output. **Exists**, text only |
| `tui` | `query`, Bubble Tea, Lip Gloss | does not import Prism |

One binary. `pulse` on a terminal opens the TUI; with `-A`, or when stdout isn't a terminal, it prints the agent card. Agents never need a flag, and the TUI never needs a repo.

Language: Go, like the other four tools. They share `prism/repo` and `prism/report`, one toolchain and one release flow. A Go binary starts in milliseconds, and Bubble Tea is a mature TUI stack.

### Collector

| What | From |
| --- | --- |
| CPU, per core | `/proc/stat`, `/sys/devices/system/cpu/*/cpufreq` |
| Memory, swap, pressure | `/proc/meminfo`, `/proc/pressure/{cpu,memory,io}`, `/proc/vmstat` |
| Processes | `/proc/<pid>/{stat,status,cmdline,cwd,io,cgroup}`; `smaps_rollup` only on request |
| cgroups | `cpu.stat`, `memory.current`, `memory.peak`, `memory.events`, `pids.current` under `/sys/fs/cgroup` |
| Temperatures | `/sys/class/hwmon`, thermal zones as fallback |
| Disks, network | `/proc/diskstats`, `statfs`, `/proc/net/dev` |
| GPU, NVIDIA | NVML (`libnvidia-ml.so.1`) loaded with dlopen, no cgo: utilisation, VRAM, temperature, and per process both VRAM and utilisation |
| GPU, AMD and Intel | `gpu_busy_percent`, `mem_info_vram_*`, hwmon; per-process from DRM `fdinfo` |

The machine it's for: 16 threads, 62 GB, an RTX 3070 on driver 620.70 (open kernel modules), cgroup v2 with cpu, io, memory and pids delegated to the user manager, about 560 processes.

Rules:

- **Reads go through `fs.FS` roots** for `/proc` and `/sys`, so tests run on captured fixture trees, never on the live machine.
- **Rates need two readings.** The TUI samples every second. A one-shot command takes two readings 250 ms apart and says so in JSON (`window_ms`).
- **Memory comes from `statm`.** Per process Pulse shows RSS, like every other monitor. Totals for a group, tree or run add up *private* memory instead (anonymous pages: RSS minus `statm`'s shared column), because summing RSS counts every shared library once per process. Exact PSS from `smaps_rollup` is only read when asked for (`pulse tree --pss`, the TUI's detail pane), and never for a game. Measured on this machine (2026-11-09): `statm` for all 555 processes took 5.6 ms; `smaps_rollup` for the 193 readable ones took 397 ms, up to 24 ms for a single browser. That read walks the process's page tables while holding its memory map lock, which is exactly what a game shouldn't have done to it every second.
- **`environ` is read once per new process**, and only the marker variables are kept (the session variables and the game markers below); everything else is dropped right away.
- **A missing source is absent, not an error.** No NVIDIA library means no GPU section.

**NVIDIA checked (2026-11-09).** The 620.70 open kernel modules don't publish per-client DRM statistics: `fdinfo` for `/dev/nvidia0` and `/dev/dri/renderD128` holds only `pos`, `flags`, `mnt_id` and `ino`, no `drm-*` keys. NVML has everything instead: per-process VRAM for graphics and compute processes alike (kwin, plasmashell, browsers, Steam) and per-process utilisation (`nvidia-smi pmon` showed kwin at 19 %). So NVIDIA goes through NVML only, and `fdinfo` stays the source for AMD and Intel.

### Process identity

PIDs get reused, so a process is `(boot id, pid, start time)`. The start time is field 22 of `/proc/<pid>/stat` (clock ticks since boot); the boot id is `/proc/sys/kernel/random/boot_id`.

- **Printed handle:** the pid, a dot and three letters: `4821.kfx`. The letters are a hash of boot id, pid and start time, written with 23 lowercase letters (no `i`, `l` or `o`, which look like digits).
- **Why this form, for agents:**
  - The pid stays in front, so an agent can still hand it to `kill` or `strace -p`.
  - Letters can't be mistaken for a decimal number or trimmed like one, which `4821.9183520` invites.
  - It's the same short length whatever the uptime.
  - It has none of the characters refs give meaning to (`:`, `@`, `#`).
  - It's deterministic: every Pulse run prints the same handle for the same process, with nothing stored.
- **Strength:** a wrong match needs the pid to come round again (the pid counter wraps at 4,194,304 on this machine) *and* the three letters to collide (1 in 12,167).
- **Accepted:** a handle or a bare pid, everywhere. A handle whose process has exited or been reused is refused with what's there now: `4821.kfx is gone; pid 4821 is now "bash" (4821.wqe), started 23:10:02`. A bare pid means whatever has that pid now (people type pids; agents copy handles).
- **In JSON:** `"id": "4821.kfx", "pid": 4821, "started": "2026-11-09T22:58:01+01:00"`.
- **Recordings** get their own short ids (`r7k2mq`), unrelated to pids.

### Attribution

Saying a process belongs to a repo, session or run is a claim, so it follows Prism's evidence rules: each attribution has a basis, the wording comes from the basis, and weaker bases never add up to a stronger one.

| Basis | How it's known | Wording |
| --- | --- | --- |
| `launched` | is in a `pulse run` cgroup, or descends from a command crux started (identity checked) | "from `crux cargo test`" |
| `cgroup` | is in the cgroup of a unit or agent scope | "in agent scope …", "in unit …" |
| `session` | its environment carries this session's id | "started in this session" |
| `cwd` | its working directory is inside the repo | "running in pulse" |
| `argv` | its command line names a path inside the repo | "mentions pulse" |

Strongest first.

- `cwd` is weak both ways: a shell or editor sitting in the repo counts, a build started elsewhere with `--manifest-path` doesn't.
- `session` uses the variables Prism's context already uses (`PRISM_SESSION`, `CLAUDE_CODE_SESSION_ID`, `T3CODE_THREAD_ID`). Pulse reads only those from `environ` and never prints any other value.
- Other users' processes can't be read without root and show as "other user".

Attribution is not a Prism `link`, because links connect code refs. Pulse defines its own `attribution` object with the same basis-and-wording rule. If it proves itself, that rule can move into `prism/evidence` as a second family.

### T3 Code

Read from T3's `agentScope` module and this machine's cgroups on 2026-11-09, and checked by `TestLive` in `collect/t3` on 2026-11-10:

- Every agent process runs in its own transient scope, `t3code-<provider>-<8 hex>.scope`, started with `systemd-run`, with `Delegate=yes` (cpu, cpuset, io, memory and pids handed to the user) and `OOMPolicy=stop`.
- The scopes sit in `app-t3code-agents-<12 hex>.slice`, one per T3 state directory, under `app-t3code-agents.slice`; scopes that outlive a T3 restart sit directly in `app-t3code-agents.slice`. That slice gets `MemoryHigh` = RAM minus 6 GiB and `MemoryMax` = RAM minus 4 GiB: 56.7 and 58.7 GiB of 62.7 GiB here. On `app-t3code.slice` above it, the agents run at `CPUWeight=10`, `IOWeight=10`, `TasksMax=25%`.
- A Claude agent carries `T3CODE_THREAD_ID` and `T3CODE_CLI_PATH` (the `t3` command) in its environment, and so does everything it starts. **Not every agent does** (found by `TestLive` on 2026-11-11, then read in T3's server bundle): T3 starts opencode as one server (`opencode serve`) that threads share, in a scope it marks `shared`, without a thread id. Codex gets the id through its `shell_environment_policy`, so it's on the commands Codex runs.
- So a scope's thread is: its agent's id, or else the one id that its other processes agree on, or else unknown. `pulse agents` shows a scope with no known thread as a shared server.
- T3 stops an agent with `systemctl --user stop` on its scope, or by writing `cgroup.kill`; it reads OOM kills from the scope's `memory.events`. Its own resource monitor reads `/proc` directly, not `cgroup.procs`.

What Pulse does with that:

- **`pulse agents`:** one line per agent scope, with the thread's title, its model, CPU, memory and project (from the agent's working directory), and how often the kernel killed something in it for memory. Then a headroom line: `agents use 6.8 GB; throttled at 56.7 GB, one is killed at 58.7 GB`. Memory here is the cgroup's `memory.current`, page cache included, because that's what the limits count. No status: it would come from a cache up to ten minutes old, and the CPU column says whether the agent is working.
- **The card and `pulse load`** warn when the agents are within 2 GB of `MemoryHigh`. Past it they're throttled; past `MemoryMax` the kernel kills a process, and with `OOMPolicy=stop` that ends the whole agent, build and all.
- **`pulse run`** records inside the agent's own delegated scope (see [recording runs](#recording-runs)), so T3's stop, kill, limits and OOM handling keep covering the run.
- **CPU priority is already handled:** at `CPUWeight=10`, agents yield the CPU to a game. GPU, memory and memory bandwidth aren't covered by that, which is why the load verdict still matters.

**Thread details come from T3's own CLI.** `t3 thread show <id> --json` returns the thread's title, status (`running`, …), model, branch, worktree and link; the thread id comes from the agent's environment. Each call takes 0.7 to 0.9 s, nearly all of it the CLI starting (`t3 --version` alone took 764 ms on 2026-11-10), so:

- the TUI fetches titles in the background and fills them in as they arrive;
- `pulse agents` keeps title and model in `$XDG_STATE_HOME/pulse-monitor/t3-threads.json` for 10 minutes, fetches missing ones in parallel with a 3 s limit, and shows the thread id when T3 doesn't answer in time;
- the card and `pulse load` never call it.

Everything else Pulse discovers at runtime from unit names, cgroup files and the documented environment variables. It doesn't hardcode T3's numbers, and it doesn't read T3's databases.

## Agent interface

Follows Prism's text conventions: a line budget (`-b`), a `context:` line when context changed something, and up to three `next:` lines, runnable as printed.

| Command | Prints |
| --- | --- |
| `pulse` (not a terminal), `pulse -A` | the card: load verdict, machine summary, top groups, this repo and session |
| `pulse load` | one line: `quiet`, or `busy:` with the reasons |
| `pulse top [--by cpu\|mem\|gpu\|io]` | top consumers, grouped |
| `pulse tree <handle\|pid\|name>` | a process subtree with totals |
| `pulse project [path]` | what runs for this repo or worktree, by basis |
| `pulse agents` | per T3 agent: thread, project, model, CPU, memory; then headroom against T3's limits |
| `pulse run [-n name] -- <cmd>` | runs and records the command; summary on stderr, exit code passed through |
| `pulse runs`, `pulse show <run>` | recorded runs; one run with per-process breakdown and timeline |
| `pulse diff <run> <run>` | two runs side by side |

**Grouping is what saves tokens.** Eight `rustc` processes are one line (`rustc ×8  5.1 cores  5.4 GB`), a browser's 24 processes are one line, and known tools get a label from their arguments (rustc's crate name, python's script, node's entry file).

The examples below are illustrative. Sizes are binary (1 GB is 2^30 bytes), as `free -g` and most monitors print them.

The card:

```
$ pulse
context: repo pulse (main); session 1a2b3c4d
busy: a game (gamescope) uses 71% GPU and 2.1 cores
cpu 5.8/16 cores 61°C · mem 24.1/62 GB, no pressure · gpu RTX 3070 71%, 5.8/8 GB, 64°C
top  gamescope tree       2.1 cores   4.2 GB   5.1 GB vram
     t3code agents ×3     1.9 cores   6.8 GB
     firefox ×24          0.4 cores   3.9 GB
this repo
     cargo build 4821.kfx   1.2 cores  3.8 GB  [this session]
next: pulse tree 4821.kfx   the build's 9 processes
next: pulse agents          the 3 agents, by thread
```

A tree:

```
$ pulse tree 4821.kfx
cargo build --release 4821.kfx, running 41 s, started in this session
total 9 processes, 6.1 cores, 3.6 GB private memory (shared libraries and files not summed)
├─ rustc syn         1.0 cores  2.1 GB
├─ rustc pulse       0.9 cores  1.4 GB
├─ cc ×4             3.6 cores  210 MB
└─ ld.lld            0.6 cores  312 MB
next: pulse run -- cargo build --release   record a whole run to compare later
```

A recorded run:

```
$ pulse run -- cargo test
  …cargo's own output, unchanged…
pulse: cargo test, exit 0, 48.2 s, recorded as r7k2mq
  cpu    653 s (612 user, 41 sys), 13.5 cores on average
  peak   8.9 GB private memory (sampled every 100 ms); largest process 2.1 GB RSS (rustc syn)
  stall  memory 0.0 s, io 0.4 s
  io     1.2 GB read, 3.4 GB written
  other  no other load during the run
next: pulse show r7k2mq   per-process breakdown and timeline
```

A diff:

```
$ pulse diff r7k2mq r9q4xt
cargo test in pulse: abc123f (r7k2mq, 11-08 21:14) vs def4567 (r9q4xt, 11-09 22:40)
              before         after              change
wall          48.2 s         61.0 s             +27 %
cpu           653 s          701 s              +7 %
peak          8.9 GB         11.5 GB            +2.6 GB
largest       rustc 2.1 GB   pulse-test 4.4 GB
other load    none           a game, 2.1 cores
note: r9q4xt overlapped other load, so its wall time isn't comparable
next: recap abc123f..def4567   what changed between the two runs
```

Wording follows the evidence rules: Pulse reports what was measured and what else was running. It never says a commit *caused* a change.

### JSON

`--json` prints the `prism/1` envelope with `"tool": "pulse"`. The result type goes into `prism/report` as `report.Pulse`, next to the other tools' results, so the shape is defined once and covered by golden tests there.

- No process handle, path or run id goes in a ref field (`ref`, `in`, `test`, `target`, `from`, `to`, `old_ref`). Pulse uses `id`, `parent`, `run` and `root`.
- Sizes in bytes (`rss_bytes`, `private_bytes`), CPU in cores (`cpu_cores`) or seconds (`cpu_s`), durations in `_ms`, times RFC 3339 with the offset.
- `links` stays `[]` unless a result names code (a crux run's failing tests, for example).

```json
"result": {
  "at": "2026-11-09T23:04:11+01:00",
  "window_ms": 250,
  "load": {
    "verdict": "busy",
    "reasons": [{ "what": "game", "label": "gamescope", "gpu_percent": 71, "cpu_cores": 2.1 }]
  },
  "groups": [
    { "label": "rustc", "count": 8, "cpu_cores": 5.1, "private_bytes": 5798205849, "ids": ["4822.pwd"] }
  ],
  "attributed": [
    { "id": "4821.kfx", "root": "/home/u/src/pulse", "basis": "session" }
  ]
}
```

## The load verdict

"Busy" means any of:

- a game is running (markers below);
- CPU above 50 % of all cores, or GPU above 30 %, over the window;
- memory pressure: PSI `some avg10` above 10 %;
- T3's agents are within 2 GB of their memory limit.

Game markers:

| Launcher | Marker | Name shown |
| --- | --- | --- |
| Steam | a `reaper SteamLaunch AppId=<id>` process (argv, no environment needed), or `SteamAppId` / `SteamGameId` in a process's environment | the game's `name` from `steamapps/appmanifest_<id>.acf` |
| Heroic | `HEROIC_APP_NAME` in a process's environment, with `HEROIC_APP_RUNNER` (`legendary`, `gog`, `nile`, `sideload`, `zoom`) and `HEROIC_APP_SOURCE` (`epic`, `gog`, `amazon`, `sideload`, `zoom`) | the app name, plus the store |
| Any | a gamescope session | the process gamescope runs |

Checked 2026-11-09: Heroic 2.22.4 sets those variables on every game it starts, in one function that all its launch paths call (Epic through legendary, GOG, Amazon through nile, sideloaded, Zoom). `GAMEID` alone isn't a marker, because Heroic also sets it for winetricks. Steam's own client processes carry no `SteamAppId`; only its games do. Both are to be confirmed on real Steam and Heroic games once the collector exists; other launchers get markers once they're seen on a real run.

Markers inside a T3 agent scope don't count: an agent's test under a headless gamescope isn't somebody playing.

The thresholds and the marker list will live in the config file (constants until then). The verdict always lists its reasons, so an agent never has to trust a bare "busy", and a quiet verdict says what was checked: `quiet: cpu 0.8 of 16 cores, no game, no memory pressure`. A CPU reason names the heaviest group, since the asking agent's own build counts too. Pulse's SKILL.md tells agents: run `pulse load` before timing measurements or heavy benchmarks, and if it says busy, skip the measurement and say why.

## Recording runs

**Each run gets its own cgroup, inside the agent's scope.** T3 delegates every agent's scope to the user, so Pulse can create a child cgroup there (`<agent scope>/pulse-<run id>`) and start the command straight into it (Go's `SysProcAttr.UseCgroupFD`, which uses `clone3`'s `CLONE_INTO_CGROUP`, so there's no window where the command runs outside it). No `systemd-run`, no D-Bus, and the run never leaves the agent's scope:

- T3's stop (`systemctl stop` on the scope) and kill (`cgroup.kill`) take the run down with the agent, because both act on the whole subtree.
- The agents slice's memory limits and CPU weight still apply.
- OOM kills still show in the scope's `memory.events`, which counts its whole subtree.

Tested on this machine (2026-11-09): creating a child cgroup in the agent's scope works, a process can be moved in, and its `cpu.stat` read 0.996 s of CPU for a 1 s busy loop. The child cgroup was removed afterwards.

What that gives:

| Number | Source | How exact |
| --- | --- | --- |
| CPU time, user and system | the run cgroup's `cpu.stat` | exact, for every process in the run, including ones that double-forked or were never waited for |
| Which processes belong to the run | the run cgroup's `cgroup.procs` | exact |
| Stall time | the run cgroup's `cpu.pressure`, `memory.pressure`, `io.pressure` | exact |
| Largest single process's peak RSS | `wait4` rusage (`ru_maxrss`) | exact, among waited descendants |
| Peak private memory, summed | `statm` of the members, every 100 ms (backing off to 1 s for long runs) | can miss spikes shorter than the interval; output says "sampled" |
| GPU | NVML, per member | as sampled |
| IO | `/proc/<pid>/io` of the members | as sampled; misses processes that exit between samples |
| Other load | samples of everything outside the run | marks runs that aren't comparable |

**What it can't give: the kernel's exact memory peak.** `memory.peak` needs the memory controller enabled below the agent's scope, and the kernel refuses that while the scope has processes of its own (tested: `EBUSY`; the scope was unchanged). Making room would mean moving T3's agent process into a child cgroup, which is meddling with T3's layout. Not done; memory stays sampled.

**After the run:** Pulse removes the run cgroup once it's empty. If the command left processes behind (a compiler cache's server, say), Pulse leaves them alone, lists them in the record, and the next `pulse run` removes empty `pulse-*` cgroups.

**Without a delegated cgroup** (outside T3, or no systemd): Pulse samples the process tree instead and takes CPU time from `wait4`, which is exact only for descendants that were waited for. The record says which method it used.

### Run store

- `$XDG_STATE_HOME/pulse-monitor/runs/`: one compressed JSON file per run, plus an index. Not `pulse/`, because `~/.config/pulse` is PulseAudio's and PipeWire's directory (it exists on this machine). The config file goes in `$XDG_CONFIG_HOME/pulse-monitor/`.
- Each record holds: command, cwd, repo root, HEAD, branch, dirty flag, session, start time, exit, the summary, a downsampled timeline (at most 600 points), the top processes, and other load.
- Retention: the last 200 runs or 200 MB, oldest first out.
- Not in the git dir with Prism's context: records must outlive Prism's 60-minute expiry and branch switches, since comparing before and after a change across branches is the point.

## History without a daemon

"Historical metrics" in v1 means two things: the TUI keeps history in memory while it's open, and recorded runs keep theirs on disk. There is no "what happened at 3 a.m.". If that's wanted later, the light way is an opt-in systemd user timer that samples once a minute into a fixed-size ring file, not a resident daemon.

## TUI

- **Overview:** per-core CPU and frequency, memory and swap, GPU, temperatures, disks and network as braille graphs, with a compact process list under them.
- **Processes:** sortable list or tree (`t`), grouping (`g`), filter (`/`), and a detail pane with argv, cwd, cgroup, attribution and open files. Kill or signal with confirmation (`k`).
- **Agents and projects:** the attribution views, the same data as `pulse agents` and `pulse project`.
- **Runs:** recorded runs with timeline charts, and a diff of two selected runs.
- **Input and looks:** arrows and vim keys, mouse, `?` for help. A few built-in themes plus one built from the terminal's 16 colours. Respects `NO_COLOR`.
- **Launch:** the first frame shows within 100 ms with what one reading gives; graphs fill in as samples arrive.

## Prism integration

Optional by construction. Outside a git repo, or with `--no-context` or `PRISM_NO_CONTEXT=1`, Pulse is a plain system monitor, and the only difference is the missing "this repo" section. Context follows Prism's rules: it may rank, mark and add, never hide a process that would show without it.

**What Pulse uses**

- `prism/repo`: root, worktree, branch and HEAD, for attribution and run records.
- `prism/report`: the envelope and `report.Pulse`.
- Prism's session detection.
- crux's last-run record: when it carries a Pulse run id, the card mentions that run's resources.

**What the other tools gain**

| Tool | Change |
| --- | --- |
| crux | a `resources:` line on every run: CPU time and the largest process's peak, from `wait4`, with no sampling and no Pulse needed. With `pulse/record`: the summed peak and other load, the run saved in Pulse's store, and `next: pulse show <run>` when the run was heavy or overlapped other load |
| crux, later | compares with the last recorded run of the same command at another commit: `peak 12.0 GB, +2.6 GB vs abc123f` and `next: recap abc123f..HEAD` |
| recap, seek, glint | nothing in v1 |

Caveat for comparisons: builds are only comparable when their caches were in the same state, and Pulse can't see that. An incremental `cargo build` after a no-op change isn't a faster build. So comparisons start with test and bench runs, and only between runs that exited 0, ran the same command in the same directory, and had no other load.

**crux bench (later, in crux).** crux runs a command N times and records each run with `pulse/record`; Pulse shows the spread and compares two sets; recap shows the changes between their commits. It belongs in crux, since Prism leaves running things to crux, and gets designed there once the run store exists.

## Will agents use it?

The 2026-11-08 benchmark (52 runs, Sonnet 5.5, 13 tasks on 9 repos) found agents never used refs, although refs worked. Pulse's value to agents rests on three things that benchmark says not to assume: that agents know it's there, that they run it at the right moment, and that they follow its `next:` lines. So the plan tests this before building much Prism integration.

- **Eval A, after M0** (card, `load`, `top`, `tree`, SKILL.md): does an agent reach for `pulse` instead of `ps`, `top` and `free`, and does it read less?
- **Eval B, after M4** (runs, diff, crux's line): seeded tasks such as "this test suite got 2 GB heavier since abc123f, find out why" or "this build is OOM-killed under a 4 GB limit, which step peaks?". Does the agent follow crux's `next: pulse show` and Pulse's `next: recap`?
- **Measured** like the earlier benchmark (tasks solved, tool calls, bytes of tool output read), plus whether `pulse` and its `next:` lines were used.
- **Pass:** no drop in tasks solved, fewer bytes read on the resource tasks, and `pulse` used in most runs where it's relevant. If agents ignore it even with the SKILL.md, fix discoverability before adding more integration.
- Seeded loads run under a CPU and memory quota, so they can't disturb anything else on the machine.

## Milestones

| # | What | Done when |
| --- | --- | --- |
| M0 | collector (no GPU; T3 discovery is done in `collect/t3`), identity, card, `load`, `top`, `tree`, `agents`, SKILL.md | **done 2026-11-11**: golden tests on a built `/proc` tree pass; the card is 10 lines on the target machine. `agents` came forward from M3, since it needs no Prism. Not in M0: `--json`, the `context:` line and "this repo" (M3), `--by gpu\|io`, the config file. Steam and Heroic detection is tested on built processes only, until real games are tried |
| A | eval A | results written up: go, or fix discoverability first |
| M1 | TUI (overview, processes, tree, kill, filter, themes), GPU (NVML, sysfs), temperatures | first frame under 100 ms; steady state under 1 % of a core at 1 s with 1,000 processes |
| M2 | `pulse run`, run store, `runs`, `show`, `diff`, other-load marks | the diff of two fixture runs matches its golden file |
| M3 | Prism adapter: envelope, `report.Pulse`, attribution, `project`, `agents`, `next:` lines | e2e: every ref Pulse JSON contains is valid (normally none); context off shows the same processes |
| M4 | crux: `resources:` line, run store, `next: pulse show` | e2e: crux's `next: pulse show` and Pulse's `next: recap` run as printed |
| B | eval B | results written up |

crux's `resources:` line from `wait4` needs nothing from Pulse and is small; it can ship any time, even first.

If the TUI matters more than the agent side at first, do M1 before M0's commands. The collector comes first either way.

## Performance budgets

| What | Budget |
| --- | --- |
| TUI first frame | under 100 ms |
| TUI steady state, 1 s refresh, 1,000 processes | under 1 % of one core, under 30 MB RSS |
| One-shot command | about 300 ms (the 250 ms window plus reading); default output 12 lines or fewer |
| `pulse run` sampling | under 1 % of the measured command's CPU time |

Pulse measures its own cost with `pulse run`. Like any timing measurement, that only happens while the machine is otherwise idle.

## Testing

- Collector tests run through the `fs.FS` roots on trees that `collect/collecttest` builds: PID reuse, exec, exited and unreadable processes, a T3 scope layout, Steam, Heroic and gamescope games. Built trees keep the tests readable and hold nothing private. Captured trees are for what can't be guessed: T3's layout has one (`collect/t3/testdata`), and GPU sources and real games get them when they're seen.
- The run cgroup path needs a delegated cgroup, which CI doesn't have: CI tests the sampling fallback, and the cgroup path is tested locally inside a T3 agent scope.
- **T3 contract test** (exists: `TestLive` in `collect/t3`). Run inside a T3 agent scope, it checks every T3 assumption in this doc against the live machine:
  - the test finds its own scope, and that scope's agent carries this thread's id;
  - the agents slice has `memory.high` below `memory.max`, at most RAM, and the CPU and IO weights are below systemd's default;
  - cpu, io, memory and pids are delegated, and a command started straight into a child cgroup lands there and has its CPU counted;
  - the scope has `Delegate=yes` and `OOMPolicy=stop`;
  - `t3 thread show` returns this thread, with a title and a status.

  Outside T3, or with `-short`, it skips, so CI skips it. `-capture <name>` also saves the live layout as the fixture `testdata/<name>`: cgroup files, `/proc/<pid>/stat`, only the thread variable from each environment, and the thread's JSON with title and link replaced. `TestFixtures` reads every fixture and runs everywhere. Faking a T3 rename of its scopes fails both tests. After a T3 update: run the tests; if they fail, adapt `collect/t3`, capture a new fixture and keep the old one.
- Golden files for every text output and for the `report.Pulse` JSON.
- TUI tests render frames in memory at a fixed size; no test opens a window or a terminal.
- `pulse run` tests use small fixture programs (allocate N MB, spawn K children, double-fork) with known peaks.
- CI on GitHub Actions, like Prism's, with the race detector.

## Decisions

Each can be revisited; the reason says what would change it.

| Decision | Why |
| --- | --- |
| Go, laid out like the other tools | shares `prism/repo` and `prism/report`; one toolchain and release flow |
| Linux only in v1 | the machine it's for; `/proc` and cgroup v2 give the most for the least |
| No daemon | Prism non-goal; the TUI and `pulse run` only need data while they run |
| One binary: TUI on a terminal, card otherwise | instant launch, one install, agents need no flags |
| TUI and collector don't import Prism | Pulse stays standalone |
| Agents read text, programs read JSON | Prism's output rule |
| Processes get their own ids, never refs | the ref grammar and the reserved ref fields |
| Attribution states its basis | Prism's evidence rules |
| NVML through dlopen, no cgo | a static binary that also runs where no NVIDIA library is installed; the 620 driver has no DRM `fdinfo` stats, and NVML covers per-process VRAM and utilisation |
| Handles are `<pid>.<3 letters>` | short, keeps the pid usable, can't be misread as a number, deterministic (see [process identity](#process-identity)) |
| `pulse run` records in a child cgroup of the agent's scope | exact CPU time and membership without leaving T3's control; `systemd-run --scope` would have moved builds out of the agent's scope |
| Totals use private memory from `statm`; PSS only on request | summed RSS overcounts shared libraries; PSS took 397 ms for a third of the processes that `statm` read in 5.6 ms, and locks each process's memory map while it reads |
| T3 is a first-class target | a personal tool on a machine where agents always run in T3 |
| Thread details from `t3 thread show --json`, cached | T3's own CLI, which it hands to every agent; nothing internal is read. At about 0.8 s a call, it can't run on every refresh |
| A live contract test for T3 | T3 internals can change with any update; a failing test is better than wrong numbers |
| The name stays Pulse; files go under `pulse-monitor/` | the binary name `pulse` is free and fits the other tools' short names; `~/.config/pulse` belongs to PulseAudio and PipeWire |

## Open questions

None right now. The two from the second pass are answered: thread titles come from `t3 thread show` (see [T3 Code](#t3-code)), and T3 changes are caught by the contract test (see [testing](#testing)).
