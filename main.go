// Pulse says what is using this machine: a full-screen monitor on a
// terminal, and a short report for agents and scripts everywhere else.
// See docs/design.md.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"time"

	"github.com/Vaspyyy/pulse/collect"
	"github.com/Vaspyyy/pulse/collect/gpu"
	"github.com/Vaspyyy/pulse/collect/t3"
	"github.com/Vaspyyy/pulse/internal/cli"
	"github.com/Vaspyyy/pulse/internal/query"
	"github.com/Vaspyyy/pulse/internal/tui"
	"github.com/Vaspyyy/pulse/record"
)

func main() {
	roots := collect.Live()
	configPath := cli.ConfigPath(os.Getenv)
	config, _ := os.ReadFile(configPath) // no file: the defaults
	stateDir := cli.StateDir(os.Getenv)
	readGPU := func() []gpu.Device { return gpu.Read(roots.Sys) }
	steam := query.SteamNames(os.Getenv("HOME"))
	threads := cli.ThreadLookup(t3.CLI(os.Getenv), filepath.Join(stateDir, "t3-threads.json"))

	// Pulse alone on a terminal is the TUI; "pulse tui" asks for it by name.
	args := os.Args[1:]
	if (len(args) == 0 && terminal(os.Stdin) && terminal(os.Stdout)) || (len(args) == 1 && args[0] == "tui") {
		cfg, problems := cli.ParseConfig(string(config))
		// For working on Pulse: PULSE_PROFILE=<file> writes the monitor's CPU
		// profile there, and its heap at the end to <file>.heap.
		if name := os.Getenv("PULSE_PROFILE"); name != "" {
			if f, err := os.Create(name); err == nil && pprof.StartCPUProfile(f) == nil {
				defer func() {
					pprof.StopCPUProfile()
					f.Close()
					if h, err := os.Create(name + ".heap"); err == nil {
						pprof.WriteHeapProfile(h)
						h.Close()
					}
				}()
			}
		}
		err := tui.Run(tui.Options{
			Roots: roots, UID: os.Getuid(), GPU: readGPU, SteamName: steam, Threads: threads, Config: cfg,
			Store:     record.Store{Dir: filepath.Join(stateDir, "runs")},
			StateFile: filepath.Join(stateDir, "tui.json"),
			NoColor:   os.Getenv("NO_COLOR") != "",
			Terminal:  os.Getenv("TERM_PROGRAM"),
		})
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "pulse: %s: %s\n", configPath, p)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "pulse: %v\n", err)
			os.Exit(1)
		}
		return
	}
	os.Exit(cli.Run(args, cli.Env{
		Roots:          roots,
		GPU:            readGPU,
		Config:         string(config),
		ConfigPath:     configPath,
		UID:            os.Getuid(),
		Stdin:          os.Stdin,
		Getenv:         os.Getenv,
		StateDir:       stateDir,
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
		StdoutTerminal: terminal(os.Stdout),
		StderrTerminal: terminal(os.Stderr),
		Sleep:          time.Sleep,
		SteamName:      steam,
		Threads:        threads,
	}))
}

// terminal reports whether f is a terminal, not a file or a pipe.
func terminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
