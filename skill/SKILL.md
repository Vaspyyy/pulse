---
name: pulse
description: See what is using the machine with `pulse` (CPU, memory, GPU, games, T3 agents) in a dozen lines instead of `ps aux`, `top`, `free` or `htop`. Run `pulse load` before any timing or fps measurement or heavy benchmark, to know whether the machine is busy (a game is running, CPU or GPU loaded, memory pressure). Use `pulse tree <pid|name>` to see what a process started and what it uses, and `pulse agents` for T3 Code's agents and their memory limit. Use `pulse run -- <command>` to record what a build or test run costs (CPU time, peak memory, stalls) and `pulse diff` to compare two runs, for questions like "did this get heavier?" or "which step needs the memory?".
---

# pulse for agents

`pulse` reads `/proc` and the cgroups twice, a quarter of a second apart, asks the GPU driver once, and prints a short report. It needs no root and no daemon, and takes about 0.3 s.

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

## Commands

| Command | Prints |
| --- | --- |
| `pulse` | the card: load verdict, machine summary, the five heaviest groups |
| `pulse load` | one line: `quiet: …` or `busy: …` with every reason |
| `pulse top [--by cpu\|mem\|gpu]` | all groups that use something, heaviest first, with a handle each |
| `pulse tree <handle\|pid\|name>` | a process, what it started, and the totals |
| `pulse agents` | each T3 agent: thread title, model, CPU, memory, project; then the agents' memory against T3's limits |

| `pulse run [-n name] -- <command>` | runs the command and records what it used |
| `pulse runs` | the recorded runs, newest first |
| `pulse show [run]` | one run: numbers, processes, timeline (the newest without an argument) |
| `pulse diff [run] [run]` | two runs side by side |

`-b <lines>` raises the line budget (12 by default; 24 for `show`, 16 for `diff`). Output that doesn't fit ends in `… N more lines; -b N shows all`.

## Before you measure

Run `pulse load` before timing a build or test, measuring fps, or starting a heavy benchmark.

- `quiet: cpu 0.8 of 16 cores, gpu draws 31 of 240 W, no game, no memory pressure`: go ahead.
- `busy: …`: skip the measurement and say why, quoting the reason. Numbers taken next to a running game or another agent's build aren't comparable, and heavy work makes the game stutter.

Busy means any of: a game is running (Steam, Heroic, or anything under gamescope), more than half the CPUs are in use, a GPU draws more than 30 % of its power limit, memory pressure is above 10 %, or T3's agents are within 2 GB of the memory at which T3 throttles them. Your own build counts as load too: the reason names who uses the CPUs or the GPU. The limits can differ on this machine: `pulse help config` shows where they're set.

## Measuring a command

`pulse run -- <command>` runs the command as it is: its output, input and exit code pass through untouched, so `pulse run -- cargo test && …` behaves like `cargo test && …`. Afterwards it prints what the run used, on stderr, and keeps the record:

```
$ pulse run -n base -- cargo test
  …cargo's own output…
pulse: cargo test, exit 0, 48.2 s, recorded as r7k2mq (base)
  cpu    653 s (612 user, 41.0 sys), 13.5 cores on average
  peak   8.9 GB private memory (sampled every 100 ms); largest process 2.1 GB RSS (rustc syn)
  stall  cpu 2.1 s, memory 0.0 s, io 0.4 s
  io     1.2 GB read, 3.4 GB written
  other  0.4 cores on average, no game
next: pulse show r7k2mq   per-process breakdown and timeline
```

- Flags for Pulse (`-n`) go before the command; `--` ends them. For shell syntax use `pulse run -- sh -c 'make && ./run'`.
- A run is named by its id (`r7k2mq`), by the name given with `-n` (the newest run of that name), or as `last`.
- **To compare before and after a change:** `pulse run -n before -- <command>`, make the change, `pulse run -n after -- <command>`, then `pulse diff before after`. `pulse diff` alone compares the newest run with the previous run of the same command in the same directory.
- **What is exact and what is sampled.** CPU time, the stall times and the list of processes are exact: the run gets a cgroup of its own, so nothing it forks escapes. `largest process` and `io` come from the kernel's accounting for the command and the descendants it waited for. `peak` is sampled, so a spike shorter than the interval can be missed. The per-process list in `pulse show` is sampled too: it says how much CPU time it didn't catch.
- **`stall`** is how long at least one of the run's processes waited: for a CPU (`cpu`, high when the machine is oversubscribed), for memory to be reclaimed (`memory`, above zero means it ran short), or for the disk (`io`).
- **`other`** is what the rest of the machine did meanwhile. When it says `wall time isn't comparable with a quiet run's` (a game ran, or other work used a quarter of the CPUs), don't draw conclusions from the wall time; CPU time and memory still compare. `pulse diff` repeats this as a `note:`. Read the notes before the numbers.
- Compare like with like: the same command, in the same directory, both exited 0. A build after a no-op change is incremental and not a faster build: compare test and benchmark runs, or clean builds.
- A `note   no cgroup could be made here` line means the run was measured by sampling its process tree (outside T3 Code): processes that detach are missed and CPU time comes from the kernel's count for waited-for processes.
- `left` lists processes the command left running (a compiler cache's server, say). Pulse leaves them alone.
- Runs are kept in `~/.local/state/pulse-monitor/runs`, the last 200.

## Reading the report

- **Groups.** All processes of one program are one line (`firefox ×24`). A game and everything under it is one line, named after the game. All T3 agents and what they run are `t3code agents ×N`, where N counts agents, not processes.
- **Memory of a group or tree is private memory**: what would be freed if those processes exited. Shared libraries and mapped files aren't summed, so it's lower than adding up `ps`'s RSS column. A single process line in `pulse tree` shows its RSS.
- **Cores** are CPUs kept busy over the quarter second: `2.1 cores` is 210 % in `top`.
- **GPU.** `gpu RTX 3070 71% at 168/240 W, 5.8/8.0 GB, 64°C` is utilisation, power draw against the power limit, VRAM and temperature. Judge load by the watts, not the percent: an idle desktop can read 45 % at 30 W, because the GPU clocks itself down. The verdict goes by watts for that reason. `N% gpu` and `vram` columns appear when a row has them; per-process GPU numbers exist for NVIDIA only.
- **Handles** look like `4821.kfx`: the pid, a dot and three letters. Copy them as printed into `pulse tree`. The pid in front works with `kill` and `strace -p`. If the process has exited and its pid was reused, Pulse refuses the handle and says what has the pid now, so a handle never points at the wrong process. A bare pid is accepted too.
- **`pulse tree <name>`** shows the tree when one process (with its children) has that name, and a list of candidates with handles when several separate ones do.
- **`pulse agents`** shows each agent's memory as its cgroup counts it, page cache included: that's the number T3's limits apply to, so it's higher than the private memory in `pulse top`. Above the "throttled at" size all agents slow down; at the "killed at" size the kernel kills a process, and T3 then stops that whole agent. `N killed for memory` on a line means that already happened in that agent. An agent without a thread is a server that several threads share (opencode).
- **`next:` lines** run as printed.

## Limits

- Linux only. A game is found by its launcher (or a marker from the config file), not by how it uses the GPU: something heavy on the GPU that isn't a game shows as `gpu draws …`.
- Other users' processes show CPU and memory but can't be attributed to a game or agent.
- On a terminal, `pulse` alone opens a full-screen monitor for people. You get the card, because your output is no terminal; `pulse -A` asks for the card by name. Never run `pulse tui`: it needs a terminal and doesn't exit.
- Rates in the card, `top` and `tree` come from a quarter second, so a short spike can be missed or caught: for a build's real cost, measure the whole run with `pulse run`.
