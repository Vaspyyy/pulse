<img src="assets/icon.svg" width="64" height="64" alt="">

# Pulse

Pulse says what is using this machine, in a dozen lines: for AI coding agents, which pay for every line they read, and for scripts. A TUI for people comes later.

```
$ pulse
busy: a game (Hades) uses 68% GPU, 2.1 cores and 3.7 GB; cpu at 8.0 of 16 cores, most from t3code agents ×2 (5.6 cores)
cpu 8.0/16 cores 61°C · mem 24.0/62.0 GB, no pressure · gpu RTX 3070 71% at 168/240 W, 5.8/8.0 GB, 64°C
top  t3code agents ×2  5.6 cores  4.2 GB
     Hades (game)      2.1 cores  3.7 GB  68% gpu  5.1 GB vram
     firefox ×4        0.4 cores  1.1 GB   3% gpu  300 MB vram
next: pulse tree 200.yqw   the 3 processes of Hades (game)
next: pulse agents         the 2 agents, by thread
next: pulse top --by mem   the biggest memory users
```

It's a personal tool for one Linux machine where agents run in [T3 Code](https://t3.codes) while the same machine is used for gaming. Elsewhere it shows less, but doesn't break.

## Install

```sh
go install github.com/Vaspyyy/pulse@latest
ln -s "$PWD/skill" ~/.claude/skills/pulse   # from a checkout, for agents
```

## Commands

| Command | Prints |
| --- | --- |
| `pulse` | the card: load verdict, machine summary, heaviest groups |
| `pulse load` | one line: `quiet`, or `busy` with the reasons |
| `pulse top [--by cpu\|mem\|gpu]` | the heaviest groups of processes |
| `pulse tree <handle\|pid\|name>` | a process and what it started, with totals |
| `pulse agents` | T3's agents: thread, model, CPU, memory, and their memory limits |
| `pulse run [-n name] -- <command>` | runs a command and records what it used |
| `pulse runs` | the recorded runs |
| `pulse show [run]` | one run: numbers, processes, timeline |
| `pulse diff [run] [run]` | two runs side by side |

```
$ pulse run -n after -- cargo test
pulse: cargo test, exit 0, 61.0 s, recorded as r9q4xt (after)
  cpu    701 s (660 user, 41.0 sys), 11.5 cores on average
  peak   11.5 GB private memory (sampled every 100 ms); largest process 4.4 GB RSS (pulse-test)
  stall  cpu 9.8 s, memory 1.2 s, io 0.6 s
  other  a game (Hades), 2.1 cores on average: wall time isn't comparable with a quiet run's

$ pulse diff before after
cargo test in pulse: abc123f (r7k2mq, 11-08 21:14) vs def4567 (r9q4xt, 11-09 22:40)
                 before             after              change
wall             48.2 s             61.0 s             +27 %
cpu              653 s              701 s              +7 %
peak memory      8.9 GB             11.5 GB            +2.6 GB
largest process  rustc syn 2.1 GB   pulse-test 4.4 GB  +2.3 GB
note: r9q4xt overlapped other load, so its wall time isn't comparable
next: recap abc123f..def4567   what changed between the two runs
```

Inside a T3 agent, a run gets a cgroup of its own below the agent's scope, so its CPU time and stall times are exact however it forks, and T3's limits and stop button still cover it.

GPU numbers come from NVML for NVIDIA (loaded at runtime, no cgo) and from sysfs for AMD. When "busy" starts, and which other programs count as games, is set in `~/.config/pulse-monitor/config`: `pulse help config` lists the keys.

[skill/SKILL.md](skill/SKILL.md) explains the output. [docs/design.md](docs/design.md) is the plan: what exists is milestones M0 and M2 plus GPU and temperatures; the TUI and the Prism integration are still to come.

## Development

```sh
go test ./...                             # inside a T3 agent, this also checks the live T3 layout; with an NVIDIA GPU, a live NVML reading
go test ./internal/cli -update            # rewrite the golden outputs
go test ./collect/t3 -capture 2026-12-01  # save today's T3 layout as a fixture
```
