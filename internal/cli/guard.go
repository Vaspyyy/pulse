package cli

import (
	"fmt"
	"path/filepath"

	"github.com/Vaspyyy/pulse/internal/guard"
)

// guardCmd is pulse guard: whether pkill and pgrep are pulse's links, and
// on or off, which make and remove them.
func guardCmd(env Env, args []string) (report, error) {
	if env.Self == "" {
		return report{}, fmt.Errorf("guard: can't tell where the pulse binary is")
	}
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "on":
		if err := guard.Link(env.Self); err != nil {
			return report{}, fmt.Errorf("guard: %v", err)
		}
	case len(args) == 1 && args[0] == "off":
		if err := guard.Unlink(env.Self); err != nil {
			return report{}, fmt.Errorf("guard: %v", err)
		}
	}
	states := guard.Status(env.Self, env.Getenv("PATH"))
	on, linked := 0, 0
	var lines []string
	for _, s := range states {
		if s.On() {
			on++
		}
		if s.Linked {
			linked++
		}
		lines = append(lines, fmt.Sprintf("  %s  %s", s.Tool, guardState(s)))
	}
	dir := filepath.Dir(env.Self)
	var r report
	switch {
	case on == len(states):
		r.body = []string{"guard: on: pkill -f and pgrep -f leave out the shell that runs them, and its parents"}
		r.next = []next{{"pulse guard off", "remove the links"}}
	case linked == 0:
		r.body = []string{"guard: off: pkill -f and pgrep -f also match the shell that runs them, and pkill kills it"}
		r.next = []next{{"pulse guard on", "link pkill and pgrep to pulse in " + dir}}
	default:
		r.body = []string{"guard: linked, but not working here:"}
		if linked < len(states) {
			r.next = []next{{"pulse guard on", "link pkill and pgrep to pulse in " + dir}}
		}
		r.next = append(r.next, next{"pulse guard off", "remove the links"})
	}
	if linked > 0 || on != 0 || hasBlocked(states) {
		r.body = append(r.body, lines...)
	}
	return r, nil
}

func hasBlocked(states []guard.State) bool {
	for _, s := range states {
		if s.Blocked {
			return true
		}
	}
	return false
}

// guardState says how one tool stands, in a line.
func guardState(s guard.State) string {
	switch {
	case s.Blocked:
		return s.Link + " is something else, not pulse's link"
	case !s.Linked:
		return "not linked"
	case s.First == "":
		return s.Link + " is linked, but its folder isn't on PATH"
	case !s.Leads:
		return s.Link + " is linked, but " + s.First + " comes first on PATH"
	case s.Real == "":
		return s.Link + " runs first, but there is no other " + s.Tool + " to hand over to"
	case !s.Skips:
		return s.Link + " runs first, but " + s.Real + " has no -A: nothing is added"
	}
	return s.Link + " runs first, and hands over to " + s.Real + " with -A"
}
