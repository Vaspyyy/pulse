package collect_test

import (
	"testing"
	"testing/fstest"

	"github.com/Vaspyyy/pulse/collect"
	"github.com/Vaspyyy/pulse/collect/collecttest"
)

const gib = 1 << 30

func TestSnapshot(t *testing.T) {
	m := collecttest.New(4, 16*gib, 10*gib)
	m.Pressure(1.5, 12.25, 0)
	m.Add(collecttest.P{PID: 2, Comm: "kthreadd", Kernel: true})
	m.Add(collecttest.P{PID: 10, PPID: 1, Comm: "cargo", Args: []string{"cargo", "build"}, Start: 500,
		RSS: 300 << 20, Shared: 100 << 20, Cgroup: "user.slice/a.scope",
		Env: map[string]string{"T3CODE_THREAD_ID": "abc"}})
	m.Add(collecttest.P{PID: 9, PPID: 1, Comm: "a b) c", Args: []string{"/usr/bin/x"}, Start: 400, NoEnv: true})
	m.SysFile("class/hwmon/hwmon0/name", "nvme\n")
	m.SysFile("class/hwmon/hwmon0/temp1_input", "33000\n")
	m.SysFile("class/hwmon/hwmon2/name", "k10temp\n")
	m.SysFile("class/hwmon/hwmon2/temp1_input", "61500\n")
	m.Proc["net/dev"] = &fstest.MapFile{Data: []byte("Inter-|   Receive |  Transmit\n face |bytes ...\n" +
		"    lo: 900 1 0 0 0 0 0 0 900 1 0 0 0 0 0 0\n" +
		"enp5s0: 5000 9 0 0 0 0 0 0 700 4 0 0 0 0 0 0\n" +
		"docker0: 300 2 0 0 0 0 0 0 300 2 0 0 0 0 0 0\n")}
	m.SysFile("class/net/enp5s0/device/vendor", "0x8086\n")
	m.SysFile("class/net/docker0/mtu", "1500\n")
	m.Proc["diskstats"] = &fstest.MapFile{Data: []byte(
		" 259 0 nvme0n1 100 0 2000 50 80 0 4000 60 0 0 0\n" +
			" 259 1 nvme0n1p1 90 0 1800 40 70 0 3000 50 0 0 0\n" +
			" 254 0 zram0 10 0 80 1 10 0 80 1 0 0 0\n")}
	m.SysFile("block/nvme0n1/size", "1\n")
	m.SysFile("block/zram0/size", "1\n")
	c := collect.New(m.Roots())
	s := c.Snapshot()
	// Hardware only: no loopback or bridge, no partition or zram.
	if s.Net != (collect.IO{In: 5000, Out: 700}) || s.Disk != (collect.IO{In: 2000 * 512, Out: 4000 * 512}) {
		t.Errorf("net %+v, disk %+v", s.Net, s.Disk)
	}
	if s.CPUTemp != 61.5 {
		t.Errorf("CPU at %.1f°C, want 61.5 from k10temp", s.CPUTemp)
	}

	if s.BootID != collecttest.BootID || s.BootTime.Unix() != collecttest.BootTime {
		t.Errorf("boot id %q, boot time %v", s.BootID, s.BootTime)
	}
	if len(s.Cores) != 4 || s.CPU.All != 400000 {
		t.Errorf("%d cores, %d ticks; want 4 and 400000", len(s.Cores), s.CPU.All)
	}
	if s.Mem.Total != 16*gib || s.Mem.Available != 10*gib {
		t.Errorf("memory %+v", s.Mem)
	}
	if s.Pressure != (collect.Pressure{CPU: 1.5, Memory: 12.25}) {
		t.Errorf("pressure %+v", s.Pressure)
	}
	if len(s.Procs) != 3 || s.Procs[0].PID != 2 || s.Procs[1].PID != 9 || s.Procs[2].PID != 10 {
		t.Fatalf("processes %+v; want pids 2, 9, 10 in that order", s.Procs)
	}
	k, x, cargo := s.Procs[0], s.Procs[1], s.Procs[2]
	if !k.Kernel || k.Args != nil || k.EnvRead {
		t.Errorf("kernel thread %+v", k)
	}
	if x.Comm != "a b) c" || x.PPID != 1 || x.Start != 400 || x.EnvRead {
		t.Errorf("process with an odd name and no readable environment: %+v", x)
	}
	if cargo.RSS != 300<<20 || cargo.Private() != 200<<20 || cargo.Cgroup != "user.slice/a.scope" {
		t.Errorf("cargo %+v", cargo)
	}
	// Only marker variables are kept.
	if !cargo.EnvRead || len(cargo.Env) != 1 || cargo.Env["T3CODE_THREAD_ID"] != "abc" {
		t.Errorf("cargo's environment %v; want only the thread id", cargo.Env)
	}

	// CPU time moves between snapshots; what was remembered stays.
	m.Run(25, map[int]uint64{10: 20})
	s2 := c.Snapshot()
	if got := s2.Procs[2].CPUTicks - cargo.CPUTicks; got != 20 {
		t.Errorf("cargo used %d ticks, want 20", got)
	}
	if s2.CPU.All-s.CPU.All != 100 || s2.CPU.Busy-s.CPU.Busy != 20 {
		t.Errorf("machine moved %d ticks, %d busy; want 100 and 20", s2.CPU.All-s.CPU.All, s2.CPU.Busy-s.CPU.Busy)
	}

	// exec keeps the pid and start time but changes the rest.
	m.Add(collecttest.P{PID: 10, PPID: 1, Comm: "rustc", Args: []string{"rustc", "--crate-name", "syn"}, Start: 500})
	m.Remove(9)
	s3 := c.Snapshot()
	if len(s3.Procs) != 2 || s3.Procs[1].Args[0] != "rustc" || len(s3.Procs[1].Env) != 0 {
		t.Errorf("after exec and exit: %+v", s3.Procs)
	}
}

func TestHandle(t *testing.T) {
	h := collect.Handle(collecttest.BootID, 4821, 123456)
	pid, letters, ok := collect.ParseHandle(h)
	if !ok || pid != 4821 || len(letters) != 3 {
		t.Fatalf("handle %q parsed as %d %q %v", h, pid, letters, ok)
	}
	if h != collect.Handle(collecttest.BootID, 4821, 123456) {
		t.Error("handle isn't deterministic")
	}
	// A reused pid, or the same pid after a reboot, gets other letters.
	if h == collect.Handle(collecttest.BootID, 4821, 123457) || h == collect.Handle("other-boot", 4821, 123456) {
		t.Errorf("handle %q doesn't depend on start time and boot id", h)
	}
	for _, bad := range []string{"", "abc", "0", "-4", "12.ab", "12.abcd", "12.aib", "12.a1b", "12.ABC", "1.2.3"} {
		if _, _, ok := collect.ParseHandle(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if pid, letters, ok := collect.ParseHandle("77"); !ok || pid != 77 || letters != "" {
		t.Errorf("bare pid: %d %q %v", pid, letters, ok)
	}
}

// TestSharedEvery: a monitor reads statm only every few readings. The
// resident size is fresh every time, the shared part follows.
func TestSharedEvery(t *testing.T) {
	const mib = 1 << 20
	m := collecttest.New(4, 8192*mib, 4096*mib)
	p := collecttest.P{PID: 10, PPID: 1, Comm: "grow", Args: []string{"grow"}, Start: 500, RSS: 100 * mib, Shared: 40 * mib}
	m.Add(p)
	c := collect.New(m.Roots())
	c.SharedEvery = 3
	find := func() collect.Process {
		for _, p := range c.Snapshot().Procs {
			if p.PID == 10 {
				return p
			}
		}
		t.Fatal("pid 10 is missing")
		return collect.Process{}
	}
	if got := find(); got.RSS != 100*mib || got.Shared != 40*mib {
		t.Fatalf("first reading: %d MB resident, %d MB shared", got.RSS/mib, got.Shared/mib)
	}
	p.RSS, p.Shared = 200*mib, 90*mib
	m.Add(p)
	var shared []uint64
	for range 3 {
		got := find()
		if got.RSS != 200*mib {
			t.Errorf("resident %d MB, want the 200 of now", got.RSS/mib)
		}
		shared = append(shared, got.Shared/mib)
	}
	// One of three readings asks statm again, and what it says stays.
	if shared[0] != 40 || shared[2] != 90 {
		t.Errorf("shared over three readings: %v MB; want 40 first and 90 in the end", shared)
	}

	// It never says more is shared than is there.
	p.RSS, p.Shared = 10*mib, 5*mib
	m.Add(p)
	if got := find(); got.RSS != 10*mib || got.Shared > got.RSS {
		t.Errorf("after shrinking: %d MB resident, %d MB shared", got.RSS/mib, got.Shared/mib)
	}
}
