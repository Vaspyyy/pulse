package cli

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Vaspyyy/pulse/internal/query"
)

// Config is what the config file can change.
type Config struct {
	Thresholds  query.Thresholds
	GameEnv     []string // more environment variables that mark a game
	GameProcess []string // more program names that are games
	Theme       string   // the TUI's colours, "" for the default
}

const configHelp = `The config file is %s
One "key = value" per line; # starts a comment. Every key is optional:

  cpu_busy = 50          busy above this percent of all CPUs
  gpu_busy = 30          busy above this percent of a GPU's power limit (of its
                         utilisation where the power can't be read); a game
                         counts as busy whatever the GPU does
  memory_pressure = 10   busy above this memory pressure (some avg10, percent)
  agent_headroom = 2     busy when T3's agents are within this many GB of being throttled
  game_process = name    a program that counts as a game; may be repeated
  game_env = NAME        an environment variable that marks a game's processes
                         (its value is shown as the game's name); may be repeated
  theme = mocha          the TUI's colours: mocha, tokyo, gruvbox, latte or terminal
                         (the TUI's t key switches and remembers it)
`

// ConfigPath is $XDG_CONFIG_HOME/pulse-monitor/config. Not "pulse", which
// is PulseAudio's and PipeWire's directory.
func ConfigPath(getenv func(string) string) string {
	base := getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(getenv("HOME"), ".config")
	}
	return filepath.Join(base, "pulse-monitor", "config")
}

// ParseConfig reads the config file's text. A line it can't use is
// reported and skipped: a typo must not take the monitor down.
func ParseConfig(text string) (cfg Config, problems []string) {
	cfg.Thresholds = query.DefaultThresholds
	for i, line := range strings.Split(text, "\n") {
		line, _, _ = strings.Cut(line, "#")
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		bad := func(format string, args ...any) {
			problems = append(problems, fmt.Sprintf("line %d: ", i+1)+fmt.Sprintf(format, args...))
		}
		key, val, ok := strings.Cut(line, "=")
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if !ok || val == "" {
			bad("want key = value")
			continue
		}
		number := func(lo, hi float64) (float64, bool) {
			n, err := strconv.ParseFloat(val, 64)
			if err != nil || n < lo || n > hi {
				bad("%s = %s: want a number from %g to %g", key, val, lo, hi)
				return 0, false
			}
			return n, true
		}
		switch key {
		case "cpu_busy":
			if n, ok := number(0, 100); ok {
				cfg.Thresholds.CPUShare = n / 100
			}
		case "gpu_busy":
			if n, ok := number(0, 100); ok {
				cfg.Thresholds.GPULoad = n
			}
		case "memory_pressure":
			if n, ok := number(0, 100); ok {
				cfg.Thresholds.MemPressure = n
			}
		case "agent_headroom":
			if n, ok := number(0, 1024); ok {
				cfg.Thresholds.AgentHeadroom = uint64(n * (1 << 30))
			}
		case "game_process":
			cfg.GameProcess = append(cfg.GameProcess, val)
		case "game_env":
			cfg.GameEnv = append(cfg.GameEnv, val)
		case "theme":
			cfg.Theme = val
		default:
			bad("unknown key %q", key)
		}
	}
	return cfg, problems
}
