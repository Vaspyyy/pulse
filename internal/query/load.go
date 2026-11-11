package query

import (
	"fmt"
	"strings"
)

// Thresholds say when the machine counts as busy.
type Thresholds struct {
	CPUShare      float64 // busy above this share of all CPUs
	MemPressure   float64 // busy above this "some avg10" memory pressure, in percent
	AgentHeadroom uint64  // busy when T3's agents are this close to being throttled, in bytes
}

// DefaultThresholds are the ones in docs/design.md, "The load verdict".
var DefaultThresholds = Thresholds{CPUShare: 0.5, MemPressure: 10, AgentHeadroom: 2 << 30}

// Load is the verdict on whether the machine is busy, with its reasons.
type Load struct {
	Busy    bool
	Reasons []string // why it's busy; empty when quiet
	Quiet   string   // what was checked, when quiet
}

// Line is the verdict in one line.
func (l Load) Line() string {
	if l.Busy {
		return "busy: " + strings.Join(l.Reasons, "; ")
	}
	return "quiet: " + l.Quiet
}

// Load says whether the machine is busy: a game runs, the CPUs or memory
// are under load, or T3's agents are close to their memory limit. Timing
// measurements and heavy work should wait while it is.
func (v *View) Load(th Thresholds) Load {
	var l Load
	for _, g := range v.Games {
		l.Reasons = append(l.Reasons, fmt.Sprintf("a game (%s) uses %s and %s", g.Name, Cores(g.Total.Cores), Bytes(g.Total.Private)))
	}
	if v.Window > 0 && v.Cores > th.CPUShare*float64(v.NCPU) {
		r := fmt.Sprintf("cpu at %.1f of %d cores", v.Cores, v.NCPU)
		for _, g := range v.Groups("cpu") {
			if g.Kind != "game" {
				r += fmt.Sprintf(", most from %s (%s)", g.Label, Cores(g.Total.Cores))
				break
			}
		}
		l.Reasons = append(l.Reasons, r)
	}
	if p := v.Snap.Pressure.Memory; p > th.MemPressure {
		l.Reasons = append(l.Reasons, fmt.Sprintf("memory pressure %.0f%%", p))
	}
	if t := v.T3; t != nil && t.Limits.MemoryHigh > 0 && t.Memory > 0 {
		switch high := t.Limits.MemoryHigh; {
		case t.Memory >= high:
			l.Reasons = append(l.Reasons, fmt.Sprintf("t3 agents use %s and are throttled (limit %s)", Bytes(t.Memory), Bytes(high)))
		case high-t.Memory < th.AgentHeadroom:
			l.Reasons = append(l.Reasons, fmt.Sprintf("t3 agents use %s, %s below their throttle limit", Bytes(t.Memory), Bytes(high-t.Memory)))
		}
	}
	l.Busy = len(l.Reasons) > 0
	if !l.Busy {
		cpu := "cpu not measured"
		if v.Window > 0 {
			cpu = fmt.Sprintf("cpu %.1f of %d cores", v.Cores, v.NCPU)
		}
		l.Quiet = cpu + ", no game, no memory pressure"
	}
	return l
}

// Cores formats a CPU rate.
func Cores(c float64) string { return fmt.Sprintf("%.1f cores", c) }

// Bytes formats a size in binary units, as free and most monitors do
// (1 GB is 2^30 bytes).
func Bytes(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%d MB", b>>20)
	}
	return fmt.Sprintf("%d KB", b>>10)
}
