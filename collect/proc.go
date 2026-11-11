package collect

import (
	"bytes"
	"errors"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
)

// Markers are the only environment variables the collector keeps: the
// ones that say which agent session or game a process belongs to. Every
// other value is dropped as soon as the environment has been read. A
// Collector starts with these and can be given more (Collector.Markers).
var Markers = []string{
	"T3CODE_THREAD_ID", "CLAUDE_CODE_SESSION_ID", "PRISM_SESSION",
	"SteamAppId", "SteamGameId",
	"HEROIC_APP_NAME", "HEROIC_APP_RUNNER", "HEROIC_APP_SOURCE",
}

// Process is one process's reading.
type Process struct {
	PID, PPID int
	// Start is when the process started, in ticks after boot. With the
	// boot id and the pid it identifies the process for good (see Handle).
	Start    uint64
	Comm     string // the kernel's name for it, at most 15 bytes
	State    byte   // R, S, D, Z, ...
	Kernel   bool   // a kernel thread
	Threads  int
	CPUTicks uint64 // user plus system time so far
	// RSS is the resident memory in bytes, and Shared the part of it that
	// is backed by files or shared memory. RSS minus Shared is private.
	RSS, Shared uint64
	Args        []string // nil for kernel threads and when unreadable
	Cgroup      string   // cgroup2 path without the leading slash
	// Env holds the Markers the process has. EnvRead is false when its
	// environment couldn't be read (another user's process).
	Env     map[string]string
	EnvRead bool
}

// Private is the memory only this process uses, in bytes.
func (p *Process) Private() uint64 {
	if p.Shared > p.RSS {
		return 0
	}
	return p.RSS - p.Shared
}

// procKey says when remembered facts still hold: exec keeps the pid and
// the start time but changes the name, arguments and environment.
type procKey struct {
	pid   int
	start uint64
	comm  string
}

type static struct {
	args    []string
	cgroup  string
	env     map[string]string
	envRead bool
	// The shared memory as statm last had it, in bytes.
	shared      uint64
	sharedKnown bool
}

const kernelThread = 0x00200000 // PF_KTHREAD in the stat flags

func (c *Collector) readProcs(s *Snapshot) {
	entries, err := fs.ReadDir(c.roots.Proc, ".")
	if err != nil {
		return
	}
	seen := make(map[procKey]*static, len(c.static))
	s.Procs = make([]Process, 0, len(c.static)+16)
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		p, ok := c.Process(pid)
		if !ok {
			continue
		}
		key := procKey{pid, p.Start, p.Comm}
		seen[key] = c.static[key]
		s.Procs = append(s.Procs, p)
	}
	c.static = seen // forget processes that are gone
	// ReadDir sorts by name, so "10" came before "9".
	slices.SortFunc(s.Procs, func(a, b Process) int { return a.PID - b.PID })
}

// Process reads one process. ok is false when it's gone. What doesn't
// change while it runs is remembered until the next Snapshot that doesn't
// see the process.
func (c *Collector) Process(pid int) (p Process, ok bool) {
	dir := strconv.Itoa(pid)
	b, err := c.readProc(dir + "/stat")
	if err != nil {
		return Process{}, false
	}
	p, rssPages, err := parseProcStat(b)
	if err != nil {
		return Process{}, false
	}
	p.PID = pid
	key := procKey{pid, p.Start, p.Comm}
	st := c.static[key]
	if st == nil {
		st = c.readStatic(dir, p.Kernel)
		c.static[key] = st
	}
	p.Args, p.Cgroup, p.Env, p.EnvRead = st.args, st.cgroup, st.env, st.envRead
	if p.Kernel {
		return p, true // a kernel thread has no memory of its own to report
	}
	// stat has the resident size too. Only the shared part needs statm, and
	// with SharedEvery set that is read for a process every few readings:
	// it changes slowly, and statm is a third of what a reading costs.
	if every := uint64(max(c.SharedEvery, 1)); st.sharedKnown && rssPages >= 0 && (c.readings+uint64(pid))%every != 0 {
		p.RSS = uint64(rssPages) * c.pageSize
		p.Shared = min(st.shared, p.RSS)
		return p, true
	}
	if b, err := c.readProc(dir + "/statm"); err == nil {
		if rss, shared, ok := parseStatm(b); ok {
			p.RSS, p.Shared = rss*c.pageSize, shared*c.pageSize
			st.shared, st.sharedKnown = p.Shared, true
		}
	}
	return p, true
}

func (c *Collector) readStatic(dir string, kernel bool) *static {
	st := &static{}
	if b, err := fs.ReadFile(c.roots.Proc, path.Join(dir, "cgroup")); err == nil {
		for line := range strings.SplitSeq(string(b), "\n") {
			if p, ok := strings.CutPrefix(line, "0::"); ok {
				st.cgroup = strings.TrimPrefix(p, "/")
			}
		}
	}
	if kernel {
		return st
	}
	if b, err := fs.ReadFile(c.roots.Proc, path.Join(dir, "cmdline")); err == nil && len(b) > 0 {
		st.args = strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
	}
	if b, err := fs.ReadFile(c.roots.Proc, path.Join(dir, "environ")); err == nil {
		st.envRead = true
		for kv := range strings.SplitSeq(string(b), "\x00") {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || !slices.Contains(c.Markers, k) {
				continue
			}
			if st.env == nil {
				st.env = map[string]string{}
			}
			st.env[k] = v
		}
	}
	return st
}

// parseProcStat reads /proc/<pid>/stat. The name is in parentheses and may
// itself hold spaces and parentheses, so fields count from its last ')'.
// rssPages is the resident size in pages, -1 where the line is too short
// to have it. It makes no garbage but the name: it runs for every process
// at every reading.
func parseProcStat(b []byte) (p Process, rssPages int64, err error) {
	open, end := bytes.IndexByte(b, '('), bytes.LastIndexByte(b, ')')
	if open < 0 || end < open {
		return Process{}, -1, errors.New("collect: no command name in stat")
	}
	// After ')': state(3) ppid(4) flags(9) utime(14) stime(15)
	// num_threads(20) starttime(22) rss(24), so indexes 0, 1, 6, 11, 12, 17,
	// 19 and 21.
	var f [22][]byte
	n := 0
	for rest := b[end+1:]; n < len(f); n++ {
		rest = bytes.TrimLeft(rest, " \n")
		if len(rest) == 0 {
			break
		}
		i := bytes.IndexAny(rest, " \n")
		if i < 0 {
			i = len(rest)
		}
		f[n], rest = rest[:i], rest[i:]
	}
	if n < 20 {
		return Process{}, -1, errors.New("collect: short stat")
	}
	num := func(i int) uint64 {
		v, _ := parseUint(f[i])
		return v
	}
	ppid, ok := parseUint(f[1])
	if !ok {
		return Process{}, -1, errors.New("collect: no parent in stat")
	}
	rssPages = -1
	if n > 21 {
		if v, ok := parseUint(f[21]); ok {
			rssPages = int64(v)
		}
	}
	return Process{
		PPID:     int(ppid),
		Comm:     string(b[open+1 : end]),
		State:    f[0][0],
		Kernel:   num(6)&kernelThread != 0,
		CPUTicks: num(11) + num(12),
		Threads:  int(num(17)),
		Start:    num(19),
	}, rssPages, nil
}

// parseStatm reads the resident and the shared size from
// /proc/<pid>/statm, in pages: its second and third number.
func parseStatm(b []byte) (rss, shared uint64, ok bool) {
	var f [3][]byte
	n := 0
	for rest := b; n < len(f); n++ {
		rest = bytes.TrimLeft(rest, " \n")
		if len(rest) == 0 {
			break
		}
		i := bytes.IndexAny(rest, " \n")
		if i < 0 {
			i = len(rest)
		}
		f[n], rest = rest[:i], rest[i:]
	}
	if n < 3 {
		return 0, 0, false
	}
	rss, ok1 := parseUint(f[1])
	shared, ok2 := parseUint(f[2])
	return rss, shared, ok1 && ok2
}

// parseUint reads a decimal number.
func parseUint(b []byte) (uint64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	var n uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
	}
	return n, true
}
