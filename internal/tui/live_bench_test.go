// Measurements on the running machine, for working on Pulse. They are
// skipped unless PULSE_LIVE_BENCH is set:
//
//	PULSE_LIVE_BENCH=1 go test ./internal/tui -run LiveFirstFrame -v
//	PULSE_LIVE_BENCH=1 go test ./internal/tui -run xxx -bench Live -cpuprofile cpu.prof
package tui

import (
	"os"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Vaspyyy/pulse/collect"
	"github.com/Vaspyyy/pulse/collect/gpu"
	"github.com/Vaspyyy/pulse/internal/cli"
	"github.com/Vaspyyy/pulse/internal/query"
)

func liveOptions(t testing.TB) Options {
	if os.Getenv("PULSE_LIVE_BENCH") == "" {
		t.Skip("set PULSE_LIVE_BENCH=1")
	}
	roots := collect.Live()
	return Options{Roots: roots, UID: os.Getuid(), GPU: func() []gpu.Device { return gpu.Read(roots.Sys) },
		Config: cli.Config{Thresholds: query.DefaultThresholds}}
}

// TestLiveFirstFrame times a start: the first reading, with the GPU
// library loading, and the first frame.
func TestLiveFirstFrame(t *testing.T) {
	o := liveOptions(t)
	start := time.Now()
	m := newModel(o)
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 45})
	m.Update(m.sample(0)())
	frame := m.View().Content
	t.Logf("first frame after %v (%d bytes, %d processes)", time.Since(start).Round(time.Millisecond), len(frame), len(m.view.Procs))
}

func liveModel(b *testing.B) *model {
	m := newModel(liveOptions(b))
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 45})
	for range 2 {
		m.sampling = false
		m.Update(m.sample(0)())
	}
	return m
}

func BenchmarkLiveSample(b *testing.B) {
	m := liveModel(b)
	b.ReportAllocs()
	for b.Loop() {
		m.sampling = false
		m.Update(m.sample(0)())
	}
}

func BenchmarkLiveView(b *testing.B) {
	m := liveModel(b)
	b.ReportAllocs()
	for _, tab := range []tab{tabOverview, tabProcs, tabAgents} {
		m.tab = tab
		b.Run(tabNames[tab], func(b *testing.B) {
			for b.Loop() {
				_ = m.View()
			}
		})
	}
}
