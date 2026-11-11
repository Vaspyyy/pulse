// Pulse says what is using this machine: a short report for agents and
// scripts today, a TUI for people later. See docs/design.md.
package main

import (
	"os"
	"path/filepath"
	"time"

	"github.com/Vaspyyy/pulse/collect"
	"github.com/Vaspyyy/pulse/collect/gpu"
	"github.com/Vaspyyy/pulse/collect/t3"
	"github.com/Vaspyyy/pulse/internal/cli"
	"github.com/Vaspyyy/pulse/internal/query"
)

func main() {
	roots := collect.Live()
	configPath := cli.ConfigPath(os.Getenv)
	config, _ := os.ReadFile(configPath) // no file: the defaults
	os.Exit(cli.Run(os.Args[1:], cli.Env{
		Roots:      roots,
		GPU:        func() []gpu.Device { return gpu.Read(roots.Sys) },
		Config:     string(config),
		ConfigPath: configPath,
		UID:        os.Getuid(),
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		Sleep:      time.Sleep,
		SteamName:  query.SteamNames(os.Getenv("HOME")),
		Threads:    cli.ThreadLookup(t3.CLI(os.Getenv), filepath.Join(cli.StateDir(os.Getenv), "t3-threads.json")),
	}))
}
