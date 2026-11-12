package tui

import (
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Vaspyyy/pulse/collect/collecttest"
	"github.com/Vaspyyy/pulse/collect/gpu"
	"github.com/Vaspyyy/pulse/collect/t3"
	"github.com/Vaspyyy/pulse/internal/cli"
	"github.com/Vaspyyy/pulse/internal/query"
	"github.com/Vaspyyy/pulse/record"
)

var shot = flag.String("shot", "", "write the README's screenshot to this file, as text with its colours")

// The size of the README's screenshot, in cells.
const shotW, shotH = 132, 40

// TestShot draws the README's screenshot: the overview on collecttest's
// desk, with a few more programs on it and two and a half minutes of
// made-up load, so the graphs have something to show. It writes the frame
// with its colours; cat it in a terminal of 132 by 40 cells and take a
// picture of that. A screenshot of a real machine shows what its owner
// runs, so the README never has one.
func TestShot(t *testing.T) {
	if *shot == "" {
		t.Skip("no -shot file")
	}
	const ncpu = 16
	const gib = 1 << 30
	rnd := rand.New(rand.NewSource(7))

	mach := collecttest.Desk()
	p := func(pid, ppid int, comm string, rssMB, sharedMB uint64, args ...string) {
		mach.Add(collecttest.P{PID: pid, PPID: ppid, Comm: comm, Args: args, Start: uint64(pid) * 100,
			RSS: rssMB * mib, Shared: sharedMB * mib, Cgroup: "user.slice/session.scope"})
	}
	p(500, 1, "lumen", 210, 60, "/usr/bin/lumen")
	p(501, 500, "fish", 9, 5, "/usr/bin/fish")
	p(520, 1, "code", 420, 120, "/usr/bin/code")
	for pid := 521; pid <= 524; pid++ {
		p(pid, 520, "code", 260, 90, "/usr/bin/code", "--type=renderer")
	}
	p(540, 1, "pipewire", 24, 10, "/usr/bin/pipewire")
	p(541, 1, "wireplumber", 18, 9, "/usr/bin/wireplumber")
	p(560, 1, "kwin_wayland", 300, 110, "/usr/bin/kwin_wayland")
	p(561, 560, "Xwayland", 110, 50, "/usr/bin/Xwayland")
	p(565, 1, "plasmashell", 340, 120, "/usr/bin/plasmashell")
	p(570, 1, "syncthing", 150, 20, "/usr/bin/syncthing")
	mach.SysFile("class/net/enp5s0/device/vendor", "0x8086\n")
	mach.SysFile("block/nvme0n1/size", "1\n")

	// The machine's own files, which collecttest keeps simple: CPUs that
	// are not equally busy, memory that moves, a network and a disk.
	sec := 0
	var rx, tx, rd, wr uint64 // bytes so far
	busy := make([]uint64, ncpu)
	weights := []float64{0.5, 0.6, 1.5, 1.6, 0.7, 0.5, 0.9, 1.1, 0.6, 1.4, 0.4, 1.2, 0.5, 1.0, 0.6, 0.9}
	files := func(ticks uint64) {
		if ticks > 0 {
			got := make([]float64, ncpu)
			var sum float64
			for i := range got {
				got[i] = weights[i] * (0.6 + 0.8*rnd.Float64())
				sum += got[i]
			}
			for i := range got {
				got[i] *= float64(ticks) / sum
			}
			// A CPU has 100 ticks a second: what one can't take goes to
			// those that have room.
			for range 4 {
				var over, room float64
				for i := range got {
					if got[i] > 97 {
						over += got[i] - 97
						got[i] = 97
					} else {
						room += 97 - got[i]
					}
				}
				if over < 0.5 || room <= 0 {
					break
				}
				for i := range got {
					if got[i] < 97 {
						got[i] += over * (97 - got[i]) / room
					}
				}
			}
			for i := range got {
				busy[i] += uint64(math.Round(got[i]))
			}
		}
		var b strings.Builder
		line := func(name string, busy, all uint64) {
			fmt.Fprintf(&b, "%s %d 0 0 %d 0 0 0 0 0 0\n", name, busy, all-busy)
		}
		all := uint64(100000 + 100*sec) // as collecttest counts them
		var total uint64
		for _, v := range busy {
			total += v
		}
		line("cpu ", total, all*ncpu)
		for i, v := range busy {
			line(fmt.Sprintf("cpu%d", i), v, all)
		}
		fmt.Fprintf(&b, "btime %d\n", collecttest.BootTime)
		mach.Proc["stat"] = &fstest.MapFile{Data: []byte(b.String())}

		// Memory climbs while a crate is compiled and falls when it is done.
		saw := math.Mod(float64(sec), 47) / 40
		if saw > 1 {
			saw = (47.0/40 - saw) / (7.0 / 40)
		}
		used := uint64(20.5*gib + 8.5*gib*saw + 0.3*gib*math.Sin(float64(sec)/3))
		mach.Proc["meminfo"] = &fstest.MapFile{Data: []byte(fmt.Sprintf(
			"MemTotal: %d kB\nMemFree: 1 kB\nMemAvailable: %d kB\nSwapTotal: %d kB\nSwapFree: %d kB\n",
			62*gib/1024, (62*gib-used)/1024, 16*gib/1024, (16*gib-900*mib)/1024))}
		mach.Proc["net/dev"] = &fstest.MapFile{Data: []byte("Inter-|   Receive |  Transmit\n face |bytes ...\n" +
			fmt.Sprintf("enp5s0: %d 9 0 0 0 0 0 0 %d 4 0 0 0 0 0 0\n", rx, tx))}
		mach.Proc["diskstats"] = &fstest.MapFile{Data: []byte(
			fmt.Sprintf(" 259 0 nvme0n1 100 0 %d 50 80 0 %d 60 0 0 0\n", rd/512, wr/512))}
	}
	files(0)

	st := record.Store{Dir: filepath.Join(t.TempDir(), "runs")}
	for _, r := range sampleRuns() {
		if err := st.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	threads := map[string]t3.Thread{collecttest.DeskThread: {ID: collecttest.DeskThread,
		Title: "Make the build faster, please, without breaking anything", Model: "claude-opus-5-5"}}
	start := time.Unix(collecttest.BootTime+13*3600+14*60, 0)
	s := &screen{t: t, mach: mach}
	s.m = newModel(Options{
		Roots: mach.Roots(),
		UID:   1000,
		GPU: func() []gpu.Device {
			f := float64(sec)
			power := math.Round(166 + 12*math.Sin(f/7) + 6*math.Sin(f/2.3))
			util := math.Round(69 + 5*math.Sin(f/5+1) + 2*math.Sin(f/1.7))
			return []gpu.Device{{Name: "RTX 3070", Vendor: "nvidia", Util: util, Power: power, PowerLimit: 240,
				MemUsed: 5939 * mib, MemTotal: 8192 * mib, Temp: 64,
				Procs: []gpu.Proc{{PID: 100, Mem: 300 * mib, Util: 3}, {PID: 201, Mem: 5222 * mib, Util: util - 5},
					{PID: 560, Mem: 180 * mib, Util: 2}}}}
		},
		SteamName: func(id string) string {
			if id == "1145360" {
				return "Hades"
			}
			return ""
		},
		Threads:   func([]string) map[string]t3.Thread { return threads },
		Config:    cli.Config{Thresholds: query.DefaultThresholds},
		Store:     st,
		StateFile: filepath.Join(t.TempDir(), "tui.json"),
		Now:       func() time.Time { return start.Add(time.Duration(sec) * time.Second) },
		Kill:      func(int, syscall.Signal) error { return nil },
	})
	s.m.Update(tea.WindowSizeMsg{Width: shotW, Height: shotH})
	s.m.Update(s.m.loadRuns()())
	s.reading()
	s.m.Update(titlesMsg(threads))

	n := func(lo, hi float64) uint64 { return uint64(lo + (hi-lo)*rnd.Float64()) }
	for sec = 1; sec <= 150; sec++ {
		f := float64(sec)
		// The build goes in steps, as crates end and others start; now
		// and then a page loads in the browser.
		build := 0.55 + 0.45*math.Abs(math.Sin(f/17))
		page := 0.0
		if sec%37 > 28 {
			page = 1
		}
		work := map[int]uint64{
			201: n(185, 228), 202: n(4, 10), // the game
			302: n(6, 14),
			303: uint64(float64(n(80, 99)) * build), 304: uint64(float64(n(70, 99)) * build),
			305: uint64(float64(n(55, 97)) * build), 306: uint64(float64(n(50, 97)) * build),
			307: uint64(float64(n(40, 97)) * build), 308: uint64(float64(n(30, 97)) * build),
			100: n(2, 8), 101: n(8, 22) + uint64(page*float64(n(30, 60))), 102: n(1, 9), 103: n(0, 5),
			500: n(1, 4), 520: n(2, 9), 521: n(1, 12), 522: n(0, 6),
			540: n(1, 2), 560: n(18, 30), 561: n(1, 3), 565: n(2, 7), 570: n(0, 5),
		}
		var ticks uint64
		for _, v := range work {
			ticks += v
		}
		mach.Run(100, work)

		down := 150e3 + 60e3*rnd.Float64()
		if sec > 116 && sec < 139 {
			down += 2.6e6 + 1.2e6*math.Sin(f/4) + 500e3*rnd.Float64() // something is fetched
		}
		rx += uint64(down)
		tx += uint64(22e3 + 30e3*rnd.Float64() + 90e3*page)
		read := 300e3 * rnd.Float64()
		if sec%23 < 3 {
			read += 20e6 + 25e6*rnd.Float64()
		}
		rd += uint64(read)
		wr += uint64((3e6 + 9e6*rnd.Float64()) * build)
		if sec%29 == 0 {
			wr += 60e6
		}
		files(ticks)
		s.reading()
	}
	s.frame() // every line must fill the terminal, as in the other tests
	if err := os.WriteFile(*shot, []byte(s.m.View().Content), 0o644); err != nil {
		t.Fatal(err)
	}
}
