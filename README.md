<img src="assets/icon.svg" width="64" height="64" alt="">

# Pulse

Pulse says what is using this machine, in a dozen lines: for AI coding agents, which pay for every line they read, and for scripts. A TUI for people comes later.

```
$ pulse
busy: a game (Hades) uses 2.1 cores and 3.7 GB; cpu at 8.0 of 16 cores, most from t3code agents ×2 (5.6 cores)
cpu 8.0/16 cores · mem 24.0/62.0 GB, no pressure
top  t3code agents ×2  5.6 cores  4.2 GB
     Hades (game)      2.1 cores  3.7 GB
     firefox ×4        0.4 cores  1.1 GB
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
| `pulse top [--by cpu\|mem]` | the heaviest groups of processes |
| `pulse tree <handle\|pid\|name>` | a process and what it started, with totals |
| `pulse agents` | T3's agents: thread, model, CPU, memory, and their memory limits |

[skill/SKILL.md](skill/SKILL.md) explains the output. [docs/design.md](docs/design.md) is the plan: what exists is milestone M0, without GPU, recorded runs or the TUI.

## Development

```sh
go test ./...                             # inside a T3 agent, this also checks the live T3 layout
go test ./internal/cli -update            # rewrite the golden outputs
go test ./collect/t3 -capture 2026-12-01  # save today's T3 layout as a fixture
```
