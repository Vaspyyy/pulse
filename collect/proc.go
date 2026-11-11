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
}

const kernelThread = 0x00200000 // PF_KTHREAD in the stat flags

func (c *Collector) readProcs(s *Snapshot) {
	entries, err := fs.ReadDir(c.roots.Proc, ".")
	if err != nil {
		return
	}
	seen := make(map[procKey]*static, len(c.static))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		dir := e.Name()
		b, err := fs.ReadFile(c.roots.Proc, path.Join(dir, "stat"))
		if err != nil {
			continue
		}
		p, err := parseProcStat(b)
		if err != nil {
			continue
		}
		p.PID = pid
		if b, err := fs.ReadFile(c.roots.Proc, path.Join(dir, "statm")); err == nil {
			f := strings.Fields(string(b))
			if len(f) >= 3 {
				rss, _ := strconv.ParseUint(f[1], 10, 64)
				shared, _ := strconv.ParseUint(f[2], 10, 64)
				p.RSS, p.Shared = rss*c.pageSize, shared*c.pageSize
			}
		}
		key := procKey{pid, p.Start, p.Comm}
		st := c.static[key]
		if st == nil {
			st = c.readStatic(dir, p.Kernel)
		}
		seen[key] = st
		p.Args, p.Cgroup, p.Env, p.EnvRead = st.args, st.cgroup, st.env, st.envRead
		s.Procs = append(s.Procs, p)
	}
	c.static = seen // forget processes that are gone
	// ReadDir sorts by name, so "10" came before "9".
	slices.SortFunc(s.Procs, func(a, b Process) int { return a.PID - b.PID })
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
func parseProcStat(b []byte) (Process, error) {
	open, end := bytes.IndexByte(b, '('), bytes.LastIndexByte(b, ')')
	if open < 0 || end < open {
		return Process{}, errors.New("collect: no command name in stat")
	}
	// After ')': state(3) ppid(4) flags(9) utime(14) stime(15)
	// num_threads(20) starttime(22), so indexes 0, 1, 6, 11, 12, 17, 19.
	f := strings.Fields(string(b[end+1:]))
	if len(f) < 20 {
		return Process{}, errors.New("collect: short stat")
	}
	num := func(i int) uint64 {
		n, _ := strconv.ParseUint(f[i], 10, 64)
		return n
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return Process{}, err
	}
	return Process{
		PPID:     ppid,
		Comm:     string(b[open+1 : end]),
		State:    f[0][0],
		Kernel:   num(6)&kernelThread != 0,
		CPUTicks: num(11) + num(12),
		Threads:  int(num(17)),
		Start:    num(19),
	}, nil
}
