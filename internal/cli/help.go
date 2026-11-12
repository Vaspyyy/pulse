package cli

import (
	"fmt"
	"regexp"
	"strings"
)

// help colors, the same 256-color palette as the help of glint, crux, recap
// and seek
const (
	hTitle   = "\x1b[38;5;183;1m"
	hDim     = "\x1b[38;5;59m"
	hTagline = "\x1b[38;5;146;3m"
	hSection = "\x1b[38;5;111;1m"
	hReset   = "\x1b[0m"
)

const helpBody = `
  USAGE
    pulse                           on a terminal: the full-screen monitor (? shows its keys)
    pulse -A                        the card: load verdict, machine, top groups; it is
                                    also what pulse prints when its output is no terminal
    pulse load                      one line: quiet, or busy with the reasons
    pulse top [--by cpu|mem|gpu]    the heaviest groups of processes
    pulse tree <handle|pid|name>    a process and what it started, with totals
    pulse agents                    T3's agents: thread, CPU, memory, limits
    pulse watch [--json] [--every <seconds>] [--pids <pid,pid>] [-n <readings>]
                                    a reading every 3 seconds, one line each, until
                                    stopped: the load, and what each pid runs and uses

  RUNS
    pulse run [-n name] -- <command>  run a command and record what it uses
    pulse runs                      the recorded runs
    pulse show [run]                one run: numbers, processes, timeline
    pulse diff [run] [run]          two runs side by side

    A run is named by its id (r7k2mq), the name given with -n, or "last".
    pulse diff alone compares the newest run with the one of the same
    command before it.

  FLAGS
    -b <lines>                      line budget (default 12)
    -h, --help                      show this help
    -V, --version                   show the version

  GOOD TO KNOW
    busy      a game runs, or CPU, GPU or memory are loaded; the limits and more
              game markers can be set in the config file (see pulse help config)
    handle    a pid, a dot and three letters (4821.kfx): it names one process
              for good, and the pid in front works with kill and strace
    timing    run pulse load before timing measurements: if it says busy, skip them
    NO_COLOR  set it to turn colors off
`

var helpSection = regexp.MustCompile(`(?m)^  ([A-Z][A-Z ]+[A-Z]|[A-Z]+)$`)

// helpText renders the help, its banner and headings colored when color is
// true, like the help of the other tools.
func helpText(color bool) string {
	paint := func(code, s string) string {
		if !color {
			return s
		}
		return code + s + hReset
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %s %s  %s\n", paint(hTitle, "✦ pulse"), paint(hDim, "v"+Version),
		paint(hTagline, "what is using this machine"))
	b.WriteString(helpSection.ReplaceAllStringFunc(helpBody, func(h string) string {
		return "  " + paint(hSection, strings.TrimSpace(h))
	}))
	b.WriteString("\n")
	return b.String()
}
