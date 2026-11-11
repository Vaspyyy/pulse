package record

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const cgroupRoot = "/sys/fs/cgroup"

// runCgroup is a cgroup made for one run, below the cgroup Pulse itself
// runs in. Everything the command starts stays in it, however it forks.
type runCgroup struct {
	dir string
	fd  *os.File
}

// openCgroup makes the run's cgroup, or returns nil where Pulse may not:
// it only makes cgroups inside a systemd unit that is delegated, which
// means systemd leaves the unit's subtree to its owner. T3 Code's agent
// scopes are (docs/design.md, "Recording runs").
func openCgroup(id string) *runCgroup {
	own := ownCgroup()
	if own == "" || !delegated(own) {
		return nil
	}
	parent := filepath.Join(cgroupRoot, own)
	removeStale(parent)
	dir := filepath.Join(parent, "pulse-"+id)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil
	}
	fd, err := os.Open(dir)
	if err != nil {
		_ = syscall.Rmdir(dir)
		return nil
	}
	return &runCgroup{dir: dir, fd: fd}
}

// ownCgroup returns this process's cgroup2 path without the leading slash.
func ownCgroup() string {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if p, ok := strings.CutPrefix(line, "0::/"); ok {
			return p
		}
	}
	return ""
}

// delegated asks systemd whether the unit a cgroup path lies in has
// Delegate=yes. Only units of the user's own manager are asked about.
func delegated(cgroup string) bool {
	if !strings.Contains(cgroup, "/user@"+strconv.Itoa(os.Getuid())+".service/") {
		return false
	}
	parts := strings.Split(cgroup, "/")
	unit := ""
	for _, p := range parts {
		if strings.HasSuffix(p, ".scope") || strings.HasSuffix(p, ".service") {
			unit = p // the innermost one
		}
	}
	if unit == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "show", unit, "-p", "Delegate", "--value").Output()
	return err == nil && strings.TrimSpace(string(out)) == "yes"
}

// removeStale removes the empty cgroups earlier runs left behind when
// their command had left processes running. A cgroup made in the last
// minute may belong to a run that is just starting, so it is kept.
func removeStale(parent string) {
	old, _ := filepath.Glob(filepath.Join(parent, "pulse-r?????"))
	for _, dir := range old {
		if fi, err := os.Stat(dir); err == nil && time.Since(fi.ModTime()) > time.Minute {
			_ = syscall.Rmdir(dir) // fails while it has processes
		}
	}
}

// pids returns the processes in the cgroup and in cgroups below it.
func (c *runCgroup) pids() []int {
	var out []int
	var walk func(dir string)
	walk = func(dir string) {
		b, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
		if err != nil {
			return
		}
		for f := range strings.FieldsSeq(string(b)) {
			if pid, err := strconv.Atoi(f); err == nil {
				out = append(out, pid)
			}
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() {
				walk(filepath.Join(dir, e.Name()))
			}
		}
	}
	walk(c.dir)
	return out
}

// cpu returns the CPU time everything in the cgroup has used so far,
// in microseconds: total, user and system.
func (c *runCgroup) cpu() (usage, user, system int64) {
	b, _ := os.ReadFile(filepath.Join(c.dir, "cpu.stat"))
	for line := range strings.SplitSeq(string(b), "\n") {
		k, v, _ := strings.Cut(line, " ")
		n, _ := strconv.ParseInt(v, 10, 64)
		switch k {
		case "usage_usec":
			usage = n
		case "user_usec":
			user = n
		case "system_usec":
			system = n
		}
	}
	return
}

// stall returns how long at least one process in the cgroup waited for
// the resource, in microseconds ("some … total=" of a pressure file).
func (c *runCgroup) stall(resource string) int64 {
	b, _ := os.ReadFile(filepath.Join(c.dir, resource+".pressure"))
	for line := range strings.SplitSeq(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "some "); ok {
			for f := range strings.FieldsSeq(rest) {
				if v, ok := strings.CutPrefix(f, "total="); ok {
					n, _ := strconv.ParseInt(v, 10, 64)
					return n
				}
			}
		}
	}
	return 0
}

// close removes the cgroup. If the command left processes behind, the
// cgroup stays with them (a later run removes it once it's empty) and
// their pids are returned.
func (c *runCgroup) close() (left []int) {
	c.fd.Close()
	// The kernel can report an emptied cgroup busy for a moment.
	for i := range 25 {
		if left = c.pids(); len(left) > 0 {
			// Stragglers that are on their way out get a moment.
			if i < 5 {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			return left
		}
		if err := syscall.Rmdir(c.dir); err == nil || os.IsNotExist(err) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}
