// Package t3 reads how T3 Code runs its agents: the systemd slices and
// scopes it puts them in, the limits on those slices, and the thread each
// agent works for. It reads cgroup and /proc files only, through fs.FS
// roots, so tests run on captured fixtures.
//
// What it relies on, as T3 did it on 2026-11-10 (docs/design.md, "T3 Code"):
// agents run in transient scopes named t3code-<name>-<8 hex>.scope, inside
// app-t3code-agents-<12 hex>.slice or directly in app-t3code-agents.slice,
// which sits in app-t3code.slice in the user manager's app.slice. Systemd
// slice names spell their place in the tree, so those names fix the paths.
// TestLive checks all of it against the running machine.
package t3

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// ErrNoT3 means the user manager has no T3 agents slice: T3 isn't running
// agents in scopes for this user.
var ErrNoT3 = errors.New("t3: no agents slice for this user")

const (
	parentSlice = "app-t3code.slice"
	agentsSlice = "app-t3code-agents.slice"
	ownedPrefix = "app-t3code-agents-"
	threadVar   = "T3CODE_THREAD_ID"
)

var scopeName = regexp.MustCompile(`^t3code-(.+)-[0-9a-f]{8}\.scope$`)

// Layout is T3's agent cgroups for one user.
type Layout struct {
	// Root is app-t3code.slice's path below the cgroup root, without a
	// leading slash.
	Root   string  `json:"root"`
	Limits Limits  `json:"limits"`
	Scopes []Scope `json:"scopes"`
}

// Limits are the resource controls T3 sets on its slices. Zero means the
// file was missing, unreadable or "max" (no limit).
type Limits struct {
	// MemoryHigh is the agents slice's memory.high in bytes: above it, the
	// agents are throttled.
	MemoryHigh uint64 `json:"memory_high"`
	// MemoryMax is the agents slice's memory.max in bytes: above it, the
	// kernel kills a process.
	MemoryMax uint64 `json:"memory_max"`
	// CPUWeight, IOWeight and PidsMax are set on app-t3code.slice.
	// Systemd's default weight is 100.
	CPUWeight uint64 `json:"cpu_weight"`
	IOWeight  uint64 `json:"io_weight"`
	PidsMax   uint64 `json:"pids_max"`
}

// Scope is one agent's transient scope.
type Scope struct {
	Unit string `json:"unit"` // t3code-claude-bca6fe37.scope
	Name string `json:"name"` // the name T3 gave it, its provider: claude
	// Slice is the slice the scope is in: app-t3code-agents-<12 hex>.slice,
	// or app-t3code-agents.slice for scopes that outlive a T3 restart.
	Slice string `json:"slice"`
	Path  string `json:"path"` // below the cgroup root
	// PIDs are the processes directly in the scope (cgroup.procs), ascending.
	PIDs []int `json:"pids"`
	// Agent is the scope's top process, the one T3 started: its parent is
	// outside the scope. 0 when the scope is empty or unreadable.
	Agent int `json:"agent"`
	// ThreadID is T3CODE_THREAD_ID from Agent's environment, "" when it
	// can't be read.
	ThreadID string `json:"thread_id"`
}

// Discover reads T3's agent cgroups for uid. cgroup is the cgroup2 root
// (/sys/fs/cgroup) and proc is /proc.
func Discover(cgroup, proc fs.FS, uid int) (*Layout, error) {
	root := path.Join("user.slice",
		fmt.Sprintf("user-%d.slice", uid),
		fmt.Sprintf("user@%d.service", uid),
		"app.slice", parentSlice)
	agents := path.Join(root, agentsSlice)
	entries, err := fs.ReadDir(cgroup, agents)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoT3
	}
	if err != nil {
		return nil, err
	}

	l := &Layout{
		Root: root,
		Limits: Limits{
			MemoryHigh: readLimit(cgroup, path.Join(agents, "memory.high")),
			MemoryMax:  readLimit(cgroup, path.Join(agents, "memory.max")),
			CPUWeight:  readLimit(cgroup, path.Join(root, "cpu.weight")),
			IOWeight:   readIOWeight(cgroup, path.Join(root, "io.weight")),
			PidsMax:    readLimit(cgroup, path.Join(root, "pids.max")),
		},
		Scopes: []Scope{},
	}

	add := func(slice, unit string) {
		m := scopeName.FindStringSubmatch(unit)
		if m == nil {
			return
		}
		dir := path.Join(agents, unit)
		if slice != agentsSlice {
			dir = path.Join(agents, slice, unit)
		}
		s := Scope{Unit: unit, Name: m[1], Slice: slice, Path: dir, PIDs: readPIDs(cgroup, path.Join(dir, "cgroup.procs"))}
		s.Agent = topProcess(proc, s.PIDs)
		if s.Agent != 0 {
			s.ThreadID = envVar(proc, s.Agent, threadVar)
		}
		l.Scopes = append(l.Scopes, s)
	}
	for _, e := range entries {
		switch name := e.Name(); {
		case !e.IsDir():
		case scopeName.MatchString(name):
			add(agentsSlice, name)
		case strings.HasPrefix(name, ownedPrefix) && strings.HasSuffix(name, ".slice"):
			sub, err := fs.ReadDir(cgroup, path.Join(agents, name))
			if err != nil {
				continue
			}
			for _, s := range sub {
				if s.IsDir() {
					add(name, s.Name())
				}
			}
		}
	}
	slices.SortFunc(l.Scopes, func(a, b Scope) int { return strings.Compare(a.Path, b.Path) })
	return l, nil
}

// CgroupOf returns the cgroup2 path of pid ("self" for the caller) below
// the cgroup root, without a leading slash, from /proc/<pid>/cgroup.
func CgroupOf(proc fs.FS, pid string) (string, error) {
	b, err := fs.ReadFile(proc, path.Join(pid, "cgroup"))
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			return strings.TrimPrefix(p, "/"), nil
		}
	}
	return "", fmt.Errorf("t3: no cgroup2 entry in /proc/%s/cgroup", pid)
}

// readLimit reads a cgroup file holding one number or "max".
func readLimit(cgroup fs.FS, name string) uint64 {
	b, err := fs.ReadFile(cgroup, name)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0 // "max", or something this package doesn't know
	}
	return n
}

// readIOWeight reads the "default N" line of io.weight.
func readIOWeight(cgroup fs.FS, name string) uint64 {
	b, err := fs.ReadFile(cgroup, name)
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "default "); ok {
			n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			if err == nil {
				return n
			}
		}
	}
	return 0
}

func readPIDs(cgroup fs.FS, name string) []int {
	pids := []int{}
	b, err := fs.ReadFile(cgroup, name)
	if err != nil {
		return pids
	}
	for f := range strings.FieldsSeq(string(b)) {
		if n, err := strconv.Atoi(f); err == nil {
			pids = append(pids, n)
		}
	}
	slices.Sort(pids)
	return pids
}

// topProcess returns the earliest-started process among pids whose parent
// isn't among them, or 0.
func topProcess(proc fs.FS, pids []int) int {
	top, topStart := 0, uint64(0)
	for _, pid := range pids {
		st, err := readStat(proc, pid)
		if err != nil || slices.Contains(pids, st.ppid) {
			continue
		}
		if top == 0 || st.start < topStart {
			top, topStart = pid, st.start
		}
	}
	return top
}

type stat struct {
	ppid  int
	start uint64 // clock ticks after boot
}

// readStat reads the parent and start time from /proc/<pid>/stat. The
// command name is in parentheses and may contain spaces and parentheses
// itself, so fields are counted after its last ')'.
func readStat(proc fs.FS, pid int) (stat, error) {
	b, err := fs.ReadFile(proc, path.Join(strconv.Itoa(pid), "stat"))
	if err != nil {
		return stat{}, err
	}
	return parseStat(b)
}

func parseStat(b []byte) (stat, error) {
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return stat{}, errors.New("t3: no command name in stat")
	}
	// After ')': state(3) ppid(4) ... starttime(22), so ppid is field 1
	// here and starttime field 19.
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 {
		return stat{}, errors.New("t3: short stat")
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return stat{}, err
	}
	start, err := strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return stat{}, err
	}
	return stat{ppid: ppid, start: start}, nil
}

// envVar returns one variable from /proc/<pid>/environ, "" if absent or
// unreadable. Nothing else from the environment is kept.
func envVar(proc fs.FS, pid int, name string) string {
	b, err := fs.ReadFile(proc, path.Join(strconv.Itoa(pid), "environ"))
	if err != nil {
		return ""
	}
	prefix := name + "="
	for kv := range strings.SplitSeq(string(b), "\x00") {
		if v, ok := strings.CutPrefix(kv, prefix); ok {
			return v
		}
	}
	return ""
}
