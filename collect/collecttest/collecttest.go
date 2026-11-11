// Package collecttest builds a small fake machine, as /proc and /sys
// trees, for tests of the collector and of what's built on it.
package collecttest

import (
	"fmt"
	"io/fs"
	"strings"
	"testing/fstest"

	"github.com/Vaspyyy/pulse/collect"
)

// BootID is the fake machine's boot id.
const BootID = "5a1e0c1d-0000-4000-8000-00000000b007"

// BootTime is its btime: 2026-11-09 08:00:00 UTC.
const BootTime = 1794211200

// Machine is a fake machine. Change it between two snapshots to get rates.
type Machine struct {
	Proc, Sys fstest.MapFS
	ncpu      int
	busy, all uint64 // ticks, all CPUs together
	procs     map[int]*P
}

// P describes a process.
type P struct {
	PID, PPID   int
	Comm        string
	Args        []string
	Start       uint64 // ticks after boot
	Ticks       uint64 // CPU time used so far
	RSS, Shared uint64 // bytes
	Cgroup      string // without the leading slash
	Cwd         string // "" when it can't be read
	Env         map[string]string
	NoEnv       bool // the environment can't be read (another user's)
	Kernel      bool
}

// New returns a machine with ncpu CPUs and the given memory in bytes.
func New(ncpu int, memTotal, memAvailable uint64) *Machine {
	m := &Machine{Proc: fstest.MapFS{}, Sys: fstest.MapFS{}, ncpu: ncpu, procs: map[int]*P{}}
	m.all = uint64(ncpu) * 100000
	m.put(m.Proc, "sys/kernel/random/boot_id", BootID+"\n")
	m.put(m.Proc, "meminfo", fmt.Sprintf("MemTotal: %d kB\nMemFree: 1 kB\nMemAvailable: %d kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n",
		memTotal/1024, memAvailable/1024))
	m.Pressure(0, 0, 0)
	m.writeStat()
	return m
}

// Roots returns the machine's trees, with 4 KiB pages.
func (m *Machine) Roots() collect.Roots {
	return collect.Roots{Proc: m.Proc, Sys: m.Sys, PageSize: 4096}
}

// Add adds or replaces a process.
func (m *Machine) Add(p P) {
	m.procs[p.PID] = &p
	m.writeProc(&p)
}

// Remove makes a process exit.
func (m *Machine) Remove(pid int) {
	delete(m.procs, pid)
	prefix := fmt.Sprintf("%d/", pid)
	for name := range m.Proc {
		if strings.HasPrefix(name, prefix) {
			delete(m.Proc, name)
		}
	}
}

// Run lets ticks pass on every CPU (25 is a quarter of a second) while
// the named processes use the given CPU ticks.
func (m *Machine) Run(ticks uint64, used map[int]uint64) {
	m.all += ticks * uint64(m.ncpu)
	for pid, t := range used {
		p := m.procs[pid]
		p.Ticks += t
		m.busy += t
		m.writeProc(p)
	}
	m.writeStat()
}

// Pressure sets "some avg10" of the three pressure files, in percent.
func (m *Machine) Pressure(cpu, memory, io float64) {
	for name, v := range map[string]float64{"cpu": cpu, "memory": memory, "io": io} {
		m.put(m.Proc, "pressure/"+name, fmt.Sprintf(
			"some avg10=%.2f avg60=0.00 avg300=0.00 total=1\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n", v))
	}
}

// SysFile writes a file below /sys.
func (m *Machine) SysFile(name, content string) {
	m.put(m.Sys, name, content)
}

// Cgroup writes a file below /sys/fs/cgroup.
func (m *Machine) Cgroup(name, content string) {
	m.put(m.Sys, "fs/cgroup/"+name, content)
}

func (m *Machine) put(fsys fstest.MapFS, name, content string) {
	fsys[name] = &fstest.MapFile{Data: []byte(content)}
}

func (m *Machine) writeStat() {
	var b strings.Builder
	line := func(name string, busy, all uint64) {
		// user nice system idle iowait irq softirq steal
		fmt.Fprintf(&b, "%s %d 0 0 %d 0 0 0 0 0 0\n", name, busy, all-busy)
	}
	line("cpu ", m.busy, m.all)
	n := uint64(m.ncpu)
	for i := range m.ncpu {
		line(fmt.Sprintf("cpu%d", i), m.busy/n, m.all/n)
	}
	fmt.Fprintf(&b, "btime %d\n", BootTime)
	m.put(m.Proc, "stat", b.String())
}

func (m *Machine) writeProc(p *P) {
	dir := fmt.Sprintf("%d/", p.PID)
	flags := 0x400000
	if p.Kernel {
		flags = 0x00200000
	}
	// pid (comm) state ppid pgrp session tty tpgid flags minflt cminflt
	// majflt cmajflt utime stime cutime cstime priority nice threads
	// itrealvalue starttime vsize rss
	m.put(m.Proc, dir+"stat", fmt.Sprintf("%d (%s) S %d 1 1 0 -1 %d 0 0 0 0 %d 0 0 0 20 0 1 0 %d 1000 %d\n",
		p.PID, p.Comm, p.PPID, flags, p.Ticks, p.Start, p.RSS/4096))
	m.put(m.Proc, dir+"statm", fmt.Sprintf("%d %d %d 1 0 1 0\n", p.RSS/4096+100, p.RSS/4096, p.Shared/4096))
	m.put(m.Proc, dir+"cgroup", "0::/"+p.Cgroup+"\n")
	cmdline := ""
	if len(p.Args) > 0 {
		cmdline = strings.Join(p.Args, "\x00") + "\x00"
	}
	m.put(m.Proc, dir+"cmdline", cmdline)
	delete(m.Proc, dir+"cwd")
	if p.Cwd != "" {
		m.Proc[dir+"cwd"] = &fstest.MapFile{Data: []byte(p.Cwd), Mode: fs.ModeSymlink}
	}
	delete(m.Proc, dir+"environ")
	if !p.NoEnv && !p.Kernel {
		env := "HOME=/home/u\x00SECRET_TOKEN=do-not-keep\x00"
		for k, v := range p.Env {
			env += k + "=" + v + "\x00"
		}
		m.put(m.Proc, dir+"environ", env)
	}
}
