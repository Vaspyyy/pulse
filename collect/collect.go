// Package collect reads the machine: CPU, memory, pressure and processes,
// from /proc. It reads through fs.FS roots, so tests run on built or
// captured trees and never on the live machine. It imports only the
// standard library, so other tools can import it.
//
// A Snapshot holds counters, not rates: a rate needs two snapshots (see
// docs/design.md, "Collector").
package collect

import (
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// TicksPerSecond is USER_HZ, the unit of the CPU times in /proc. Linux
// fixes it at 100 on every architecture it runs on today.
const TicksPerSecond = 100

// Roots are the file trees the collector reads.
type Roots struct {
	Proc fs.FS // /proc
	Sys  fs.FS // /sys
	// PageSize is the size of the pages statm counts, in bytes. 0 means
	// the running kernel's.
	PageSize int
	// procDir is where Proc is on disk, for the running machine's roots:
	// the files read for every process at every reading are then read
	// with three system calls each instead of the six of os.ReadFile.
	procDir string
}

// Live returns the running machine's roots.
func Live() Roots {
	return Roots{Proc: os.DirFS("/proc"), Sys: os.DirFS("/sys"), procDir: "/proc"}
}

// Cgroup returns the cgroup2 root, /sys/fs/cgroup.
func (r Roots) Cgroup() fs.FS {
	sub, err := fs.Sub(r.Sys, "fs/cgroup")
	if err != nil {
		return emptyFS{}
	}
	return sub
}

type emptyFS struct{}

func (emptyFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// Snapshot is one reading of the machine.
type Snapshot struct {
	At       time.Time
	BootID   string    // /proc/sys/kernel/random/boot_id
	BootTime time.Time // btime in /proc/stat
	CPU      CPUTimes  // all CPUs together
	CPUTemp  float64   // °C, 0 when no sensor is known
	Cores    []CPUTimes
	Mem      Memory
	Pressure Pressure
	Net      IO        // bytes received and sent on physical network interfaces since boot
	Disk     IO        // bytes read from and written to whole disks since boot
	Procs    []Process // ascending by pid
}

// IO counts bytes in and out since boot.
type IO struct {
	In, Out uint64
}

// CPUTimes are tick counters since boot.
type CPUTimes struct {
	Busy uint64 // everything but idle and iowait
	All  uint64
}

// Memory is in bytes.
type Memory struct {
	Total, Available    uint64
	SwapTotal, SwapFree uint64
}

// Pressure is the share of the last 10 seconds in which at least one task
// waited for the resource ("some avg10" in /proc/pressure), in percent.
// All zero on a kernel without pressure information.
type Pressure struct {
	CPU, Memory, IO float64
}

// Collector takes snapshots. It remembers what doesn't change while a
// process runs (arguments, cgroup, marker variables), so each process's
// environment is read once.
type Collector struct {
	// Now is the clock snapshots are stamped with: time.Now unless a test
	// sets another.
	Now func() time.Time
	// Markers are the environment variables to keep. Set it before the
	// first snapshot.
	Markers []string
	// SharedEvery makes Snapshot read a process's shared memory (statm)
	// only at every n-th reading, a different share of the processes each
	// time, and take the one before in between; the resident size is fresh
	// at every reading either way. 0 reads it every time. A monitor sets
	// it: at one reading a second, statm is a third of what Pulse costs.
	SharedEvery int

	roots       Roots
	pageSize    uint64
	cpuTempFile string // "" until looked for, "-" when there is none
	bootID      string
	static      map[procKey]*static
	buf         []byte // readProc's, reused
	readings    uint64 // snapshots so far
}

// readProc reads a small single-record file of /proc, such as a process's
// stat. The result is valid until the next call.
func (c *Collector) readProc(name string) ([]byte, error) {
	if c.roots.procDir == "" {
		return fs.ReadFile(c.roots.Proc, name)
	}
	fd, err := syscall.Open(c.roots.procDir+"/"+name, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	if c.buf == nil {
		c.buf = make([]byte, 4096)
	}
	for n := 0; ; {
		got, err := syscall.Read(fd, c.buf[n:])
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		n += got
		// The kernel gives such a file whole in one read, if there is room.
		if got == 0 || n < len(c.buf) {
			return c.buf[:n], nil
		}
		c.buf = append(c.buf, make([]byte, len(c.buf))...)
	}
}

// New returns a collector for roots.
func New(roots Roots) *Collector {
	ps := roots.PageSize
	if ps == 0 {
		ps = os.Getpagesize()
	}
	return &Collector{Now: time.Now, Markers: Markers, roots: roots, pageSize: uint64(ps), static: map[procKey]*static{}}
}

// Snapshot reads the machine. Sources that are missing or unreadable are
// left zero; a process that exits while it's read is left out.
func (c *Collector) Snapshot() *Snapshot {
	s := &Snapshot{At: c.Now()}
	if c.bootID == "" {
		b, _ := fs.ReadFile(c.roots.Proc, "sys/kernel/random/boot_id")
		c.bootID = strings.TrimSpace(string(b))
	}
	s.BootID = c.bootID
	c.readStat(s)
	c.readMeminfo(s)
	s.Pressure = Pressure{
		CPU:    c.readPressure("pressure/cpu"),
		Memory: c.readPressure("pressure/memory"),
		IO:     c.readPressure("pressure/io"),
	}
	s.CPUTemp = c.readCPUTemp()
	c.readNet(s)
	c.readDisks(s)
	c.readProcs(s)
	c.readings++
	return s
}

// readNet sums /proc/net/dev over the interfaces that are hardware:
// those with a device in /sys/class/net. Loopback, bridges and tunnels
// would count the same bytes again.
func (c *Collector) readNet(s *Snapshot) {
	b, err := fs.ReadFile(c.roots.Proc, "net/dev")
	if err != nil {
		return
	}
	_, known := fs.Stat(c.roots.Sys, "class/net")
	for line := range strings.SplitSeq(string(b), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if !ok || len(f) < 9 || name == "lo" {
			continue
		}
		if known == nil {
			if _, err := fs.Stat(c.roots.Sys, path.Join("class/net", name, "device")); err != nil {
				continue
			}
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		s.Net.In += rx
		s.Net.Out += tx
	}
}

// virtualDisks are block devices that sit on top of others or on memory.
var virtualDisks = []string{"loop", "ram", "zram", "dm-", "md", "sr", "nbd"}

// readDisks sums /proc/diskstats over whole physical disks (the entries
// of /sys/block, without the virtual ones), in bytes.
func (c *Collector) readDisks(s *Snapshot) {
	b, err := fs.ReadFile(c.roots.Proc, "diskstats")
	if err != nil {
		return
	}
next:
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		for _, v := range virtualDisks {
			if strings.HasPrefix(f[2], v) {
				continue next
			}
		}
		if _, err := fs.Stat(c.roots.Sys, path.Join("block", f[2])); err != nil {
			continue // a partition
		}
		// In 512-byte sectors, whatever the disk's own sector size.
		rd, _ := strconv.ParseUint(f[5], 10, 64)
		wr, _ := strconv.ParseUint(f[9], 10, 64)
		s.Disk.In += rd * 512
		s.Disk.Out += wr * 512
	}
}

// cpuSensors are the hwmon drivers whose first temperature is the CPU's:
// AMD's (k10temp, zenpower) and Intel's (coretemp, the package).
var cpuSensors = []string{"k10temp", "zenpower", "coretemp"}

// readCPUTemp returns the CPU's temperature from /sys/class/hwmon, or
// from the x86 package thermal zone when no hwmon driver has it.
func (c *Collector) readCPUTemp() float64 {
	milli := func(name string) float64 {
		b, err := fs.ReadFile(c.roots.Sys, name)
		if err != nil {
			return 0
		}
		n, _ := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		return n / 1000
	}
	if c.cpuTempFile == "" {
		c.cpuTempFile = "-" // looked, found nothing
		find := func(dir, nameFile, valueFile string, names ...string) {
			entries, _ := fs.ReadDir(c.roots.Sys, dir)
			for _, e := range entries {
				b, err := fs.ReadFile(c.roots.Sys, path.Join(dir, e.Name(), nameFile))
				if err == nil && slices.Contains(names, strings.TrimSpace(string(b))) && c.cpuTempFile == "-" {
					c.cpuTempFile = path.Join(dir, e.Name(), valueFile)
				}
			}
		}
		find("class/hwmon", "name", "temp1_input", cpuSensors...)
		find("class/thermal", "type", "temp", "x86_pkg_temp")
	}
	if c.cpuTempFile == "-" {
		return 0
	}
	return milli(c.cpuTempFile)
}

func (c *Collector) readStat(s *Snapshot) {
	b, err := fs.ReadFile(c.roots.Proc, "stat")
	if err != nil {
		return
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch {
		case f[0] == "btime":
			if n, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				s.BootTime = time.Unix(n, 0)
			}
		case f[0] == "cpu":
			s.CPU = cpuTimes(f[1:])
		case strings.HasPrefix(f[0], "cpu"):
			s.Cores = append(s.Cores, cpuTimes(f[1:]))
		}
	}
}

// cpuTimes sums a /proc/stat cpu line: user nice system idle iowait irq
// softirq steal. Guest time is already part of user, so it stops there.
func cpuTimes(f []string) CPUTimes {
	var t CPUTimes
	for i, v := range f {
		if i >= 8 {
			break
		}
		n, _ := strconv.ParseUint(v, 10, 64)
		t.All += n
		if i != 3 && i != 4 {
			t.Busy += n
		}
	}
	return t
}

func (c *Collector) readMeminfo(s *Snapshot) {
	b, err := fs.ReadFile(c.roots.Proc, "meminfo")
	if err != nil {
		return
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		var dst *uint64
		switch key {
		case "MemTotal":
			dst = &s.Mem.Total
		case "MemAvailable":
			dst = &s.Mem.Available
		case "SwapTotal":
			dst = &s.Mem.SwapTotal
		case "SwapFree":
			dst = &s.Mem.SwapFree
		default:
			continue
		}
		kb, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
		*dst = kb * 1024
	}
}

// readPressure returns "some avg10" from a /proc/pressure file.
func (c *Collector) readPressure(name string) float64 {
	b, err := fs.ReadFile(c.roots.Proc, name)
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		rest, ok := strings.CutPrefix(line, "some ")
		if !ok {
			continue
		}
		for f := range strings.FieldsSeq(rest) {
			if v, ok := strings.CutPrefix(f, "avg10="); ok {
				n, _ := strconv.ParseFloat(v, 64)
				return n
			}
		}
	}
	return 0
}
