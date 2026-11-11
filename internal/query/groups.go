package query

import (
	"cmp"
	"fmt"
	"slices"
)

// Group is a set of processes shown as one line: a game, all T3 agents,
// the kernel's threads, or every process of one program.
type Group struct {
	Kind  string // game, agents, kernel or app
	Name  string // what the group is, the same from one reading to the next
	Label string // Name with the count, as shown: "firefox ×24"
	Total Total
	// Members are the group's processes, ascending by pid.
	Members []*Proc
	// Lead is the group's top process, the heaviest one whose parent isn't
	// in the group: where to look next. nil for the agents and the kernel.
	Lead *Proc
}

// Groups returns every group, heaviest first: by "cpu" (then memory), by
// "mem", or by "gpu" (then GPU memory).
func (v *View) Groups(by string) []Group {
	index := map[string]int{}
	var groups []Group
	member := make([]int, len(v.Procs))
	for i := range v.Procs {
		p := &v.Procs[i]
		kind, key := "app", Name(p)
		switch {
		case p.Game >= 0:
			kind, key = "game", v.Games[p.Game].Name+" (game)"
		case p.Scope >= 0:
			kind, key = "agents", "t3code agents"
		case p.Kernel:
			kind, key = "kernel", "kernel"
		}
		g, ok := index[kind+"\x00"+key]
		if !ok {
			g = len(groups)
			index[kind+"\x00"+key] = g
			groups = append(groups, Group{Kind: kind, Name: key, Label: key})
		}
		groups[g].Total.add(p)
		groups[g].Members = append(groups[g].Members, p)
		member[i] = g
	}
	for i := range v.Procs {
		p, g := &v.Procs[i], &groups[member[i]]
		if g.Kind == "agents" || g.Kind == "kernel" {
			continue
		}
		if pi, ok := v.byPID[p.PPID]; ok && member[pi] == member[i] {
			continue
		}
		if g.Lead == nil || heavier(v.total(p), v.total(g.Lead)) < 0 {
			g.Lead = p
		}
	}
	for i := range groups {
		g := &groups[i]
		switch {
		case g.Kind == "agents":
			g.Label = fmt.Sprintf("%s ×%d", g.Label, len(v.T3.Scopes))
		case g.Kind == "app" && g.Total.Count > 1:
			g.Label = fmt.Sprintf("%s ×%d", g.Label, g.Total.Count)
		}
	}
	slices.SortStableFunc(groups, func(a, b Group) int {
		switch by {
		case "mem":
			return cmp.Compare(b.Total.Private, a.Total.Private)
		case "gpu":
			if c := cmp.Compare(int(b.Total.GPU), int(a.Total.GPU)); c != 0 {
				return c
			}
			if c := cmp.Compare(b.Total.VRAM, a.Total.VRAM); c != 0 {
				return c
			}
		}
		return heavier(a.Total, b.Total)
	})
	return groups
}

// Node is a line of a process tree: one process, or several leaves that
// run the same thing, with what is below it.
type Node struct {
	Label    string
	Procs    []*Proc // more than one when leaves were merged
	Own      Total   // the processes on this line
	Children []*Node
}

// Tree returns the tree below root, heaviest branches first.
func (v *View) Tree(root *Proc) *Node {
	n := &Node{Label: Label(root), Procs: []*Proc{root}}
	n.Own.add(root)
	merged := map[string]*Node{}
	for _, pid := range v.children[root.PID] {
		c := v.Proc(pid)
		if len(v.children[pid]) > 0 {
			n.Children = append(n.Children, v.Tree(c))
			continue
		}
		label := Label(c)
		m := merged[label]
		if m == nil {
			m = &Node{Label: label}
			merged[label] = m
			n.Children = append(n.Children, m)
		}
		m.Procs = append(m.Procs, c)
		m.Own.add(c)
	}
	slices.SortStableFunc(n.Children, func(a, b *Node) int { return heavier(a.sum(), b.sum()) })
	return n
}

// sum is what the node and everything below it uses.
func (n *Node) sum() Total {
	t := n.Own
	for _, c := range n.Children {
		t.merge(c.sum())
	}
	return t
}

// Sum is what the node and everything below it uses.
func (n *Node) Sum() Total { return n.sum() }
