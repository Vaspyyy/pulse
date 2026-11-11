package cli

import (
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/Vaspyyy/pulse/collect/t3"
	"github.com/Vaspyyy/pulse/internal/query"
)

// agents lists T3's agent scopes, heaviest first, and how far the agents
// are from T3's memory limits.
func agents(v *query.View, env Env) (report, error) {
	if v.T3 == nil {
		return report{}, errNoT3
	}
	type line struct {
		scope *t3.Scope
		total query.Total
	}
	lines := make([]line, len(v.T3.Scopes))
	var ids []string
	for i := range v.T3.Scopes {
		s := &v.T3.Scopes[i]
		lines[i].scope = s
		if s.ThreadID != "" && !slices.Contains(ids, s.ThreadID) {
			ids = append(ids, s.ThreadID)
		}
	}
	for i := range v.Procs {
		if p := &v.Procs[i]; p.Scope >= 0 {
			t := &lines[p.Scope].total
			t.Count++
			t.Cores += p.Cores
			t.Private += p.Private()
		}
	}
	var threads map[string]t3.Thread
	if env.Threads != nil && len(ids) > 0 {
		threads = env.Threads(ids)
	}
	slices.SortStableFunc(lines, func(a, b line) int {
		if ac, bc := int(a.total.Cores*20), int(b.total.Cores*20); ac != bc {
			return bc - ac
		}
		return int(min(max(int64(b.scope.Memory)-int64(a.scope.Memory), -1), 1))
	})

	var r report
	var rows [][]string
	for _, l := range lines {
		s := l.scope
		what, model := "(no thread: shared by threads, or just started)", ""
		if s.ThreadID != "" {
			what = "thread " + short(s.ThreadID)
			if th, ok := threads[s.ThreadID]; ok && th.Title != "" {
				what, model = strconv.Quote(clip(th.Title, 40)), th.Model
			}
		}
		// The scope's own count is what T3's limits apply to; it includes
		// the page cache the agent filled.
		mem := s.Memory
		if mem == 0 {
			mem = l.total.Private
		}
		note := clip(project(env.Roots.Proc, s.Agent), 24)
		if s.OOMKills > 0 {
			note = strings.TrimSpace(note + fmt.Sprintf("  %d killed for memory", s.OOMKills))
		}
		rows = append(rows, []string{s.Name, what, model, query.Cores(l.total.Cores), query.Bytes(mem), note})
	}
	r.body = table(rows, "lllrrl")
	lim := v.T3.Limits
	switch {
	case len(lines) == 0:
		r.body = append(r.body, "no agents are running")
	case lim.MemoryHigh > 0 && lim.MemoryMax > 0:
		r.body = append(r.body, fmt.Sprintf("agents use %s; throttled at %s, one is killed at %s",
			query.Bytes(v.T3.Memory), query.Bytes(lim.MemoryHigh), query.Bytes(lim.MemoryMax)))
	default:
		r.body = append(r.body, fmt.Sprintf("agents use %s; T3 set no memory limit", query.Bytes(v.T3.Memory)))
	}
	if len(lines) > 0 && lines[0].scope.Agent != 0 {
		if p := v.Proc(lines[0].scope.Agent); p != nil {
			r.next = append(r.next, next{"pulse tree " + v.Handle(p), "what the heaviest agent runs"})
		}
	}
	return r, nil
}

// project names what an agent works on, from its working directory: the
// directory's name, or the project's for a T3 worktree
// (…/worktrees/<project>/<worktree>).
func project(proc fs.FS, pid int) string {
	if pid == 0 {
		return ""
	}
	cwd, err := fs.ReadLink(proc, path.Join(strconv.Itoa(pid), "cwd"))
	if err != nil || cwd == "" {
		return ""
	}
	parts := strings.Split(strings.Trim(cwd, "/"), "/")
	if n := len(parts); n >= 3 && parts[n-3] == "worktrees" {
		return parts[n-2]
	}
	return parts[len(parts)-1]
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
