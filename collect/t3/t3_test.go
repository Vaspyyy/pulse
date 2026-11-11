package t3

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

var update = flag.Bool("update", false, "rewrite each fixture's layout.golden.json")

// TestFixtures reads every captured layout in testdata/<name>/ and compares
// it with that fixture's layout.golden.json. A fixture is the cgroup and
// /proc files Discover reads, captured from a real machine by TestLive with
// -capture, plus the output of `t3 thread show --json` with the title and
// link replaced.
func TestFixtures(t *testing.T) {
	roots, _ := filepath.Glob(filepath.Join("testdata", "*", "cgroup"))
	if len(roots) == 0 {
		t.Fatal("no fixtures in testdata")
	}
	for _, cg := range roots {
		dir := filepath.Dir(cg)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			l, err := Discover(os.DirFS(cg), os.DirFS(filepath.Join(dir, "proc")), fixtureUID(t, cg))
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.MarshalIndent(l, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := filepath.Join(dir, "layout.golden.json")
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("layout differs from %s (rerun with -update if the change is right); got:\n%s", golden, got)
			}

			// What Pulse relies on, beyond matching the golden file.
			if len(l.Scopes) == 0 {
				t.Error("no agent scopes")
			}
			for _, s := range l.Scopes {
				if s.Agent == 0 || s.ThreadID == "" {
					t.Errorf("%s: agent %d, thread %q; want both", s.Unit, s.Agent, s.ThreadID)
				}
			}
			if l.Limits.MemoryHigh == 0 || l.Limits.MemoryMax <= l.Limits.MemoryHigh {
				t.Errorf("memory limits %+v; want 0 < high < max", l.Limits)
			}

			b, err := os.ReadFile(filepath.Join(dir, "thread-show.json"))
			if err != nil {
				t.Fatal(err)
			}
			th, err := ParseThread(b)
			if err != nil {
				t.Fatal(err)
			}
			if th.Title != fixtureTitle || th.Status == "" || th.Model == "" {
				t.Errorf("thread %+v; want the fixture title, a status and a model", th)
			}
		})
	}
}

// fixtureUID finds the uid in a fixture's user.slice/user-<uid>.slice.
func fixtureUID(t *testing.T, cg string) int {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(cg, "user.slice", "user-*.slice"))
	if len(m) != 1 {
		t.Fatalf("want one user slice in %s, found %v", cg, m)
	}
	uid, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m[0]), "user-"), ".slice"))
	if err != nil {
		t.Fatal(err)
	}
	return uid
}

const base = "user.slice/user-1000.slice/user@1000.service/app.slice/app-t3code.slice"

// TestDiscoverShapes covers what today's fixture doesn't: a scope directly
// in the agents slice (T3 does that for scopes that outlive a restart),
// directories that aren't T3 scopes, "max" limits, an empty scope, and
// agents that carry no thread id themselves.
func TestDiscoverShapes(t *testing.T) {
	agents := base + "/app-t3code-agents.slice"
	cg := fstest.MapFS{
		base + "/cpu.weight":       {Data: []byte("10\n")},
		base + "/io.weight":        {Data: []byte("default 10\n8:0 200\n")},
		base + "/pids.max":         {Data: []byte("max\n")},
		agents + "/memory.high":    {Data: []byte("max\n")},
		agents + "/memory.max":     {Data: []byte("63015084032\n")},
		agents + "/memory.current": {Data: []byte("7000000000\n")},
		agents + "/app-t3code-agents-0123456789ab.slice/t3code-claude-deadbeef.scope/memory.current": {Data: []byte("3000000000\n")},
		agents + "/app-t3code-agents-0123456789ab.slice/t3code-claude-deadbeef.scope/memory.events":  {Data: []byte("low 0\nhigh 4\nmax 0\noom 1\noom_kill 2\noom_group_kill 0\n")},
		agents + "/t3code-codex-0a1b2c3d.scope/cgroup.procs":                                         {Data: []byte("40\n")},
		agents + "/app-t3code-agents-0123456789ab.slice/t3code-claude-deadbeef.scope/cgroup.procs":   {Data: []byte("31\n30\n")},
		agents + "/app-t3code-agents-0123456789ab.slice/t3code-claude-00000000.scope/cgroup.procs":   {Data: []byte("")},
		agents + "/app-t3code-agents-0123456789ab.slice/pulse-r7k2mq/cgroup.procs":                   {Data: []byte("50\n")},
		agents + "/other.scope/cgroup.procs":                                                         {Data: []byte("60\n")},
		// opencode's server is shared by threads and has no thread id.
		agents + "/app-t3code-agents-0123456789ab.slice/t3code-opencode-11111111.scope/cgroup.procs": {Data: []byte("70\n")},
		agents + "/app-t3code-agents-0123456789ab.slice/t3code-opencode-22222222.scope/cgroup.procs": {Data: []byte("80\n81\n82\n")},
		agents + "/app-t3code-agents-0123456789ab.slice/t3code-opencode-33333333.scope/cgroup.procs": {Data: []byte("90\n91\n92\n")},
	}
	proc := fstest.MapFS{
		// 30 is the agent (its parent 1 is outside); 31 is its child.
		"30/stat":    {Data: []byte(statLine(30, "claude", 1, 500))},
		"30/environ": {Data: []byte("HOME=/home/u\x00T3CODE_THREAD_ID=1a2b3c4d-1111-4111-8111-111111111111\x00")},
		"31/stat":    {Data: []byte(statLine(31, "bash", 30, 900))},
		"31/environ": {Data: []byte("T3CODE_THREAD_ID=wrong\x00")},
		"40/stat":    {Data: []byte(statLine(40, "codex", 2, 100))},
		// 40's environ is unreadable: no thread id, still a scope.
		"70/stat":    {Data: []byte(statLine(70, "opencode", 2, 100))},
		"70/environ": {Data: []byte("HOME=/home/u\x00")},
		// 80's children agree on a thread, so the scope gets it.
		"80/stat":    {Data: []byte(statLine(80, "opencode", 2, 100))},
		"80/environ": {Data: []byte("HOME=/home/u\x00")},
		"81/stat":    {Data: []byte(statLine(81, "bash", 80, 200))},
		"81/environ": {Data: []byte("T3CODE_THREAD_ID=aaaaaaaa-0000-4000-8000-000000000001\x00")},
		"82/stat":    {Data: []byte(statLine(82, "go", 81, 300))},
		"82/environ": {Data: []byte("T3CODE_THREAD_ID=aaaaaaaa-0000-4000-8000-000000000001\x00")},
		// 90's children work for two threads, so the scope has none.
		"90/stat":    {Data: []byte(statLine(90, "opencode", 2, 100))},
		"91/stat":    {Data: []byte(statLine(91, "bash", 90, 200))},
		"91/environ": {Data: []byte("T3CODE_THREAD_ID=aaaaaaaa-0000-4000-8000-000000000001\x00")},
		"92/stat":    {Data: []byte(statLine(92, "bash", 90, 300))},
		"92/environ": {Data: []byte("T3CODE_THREAD_ID=aaaaaaaa-0000-4000-8000-000000000002\x00")},
	}
	l, err := Discover(cg, proc, 1000)
	if err != nil {
		t.Fatal(err)
	}
	want := Limits{MemoryMax: 63015084032, CPUWeight: 10, IOWeight: 10}
	if l.Limits != want {
		t.Errorf("limits %+v, want %+v", l.Limits, want)
	}
	if l.Memory != 7000000000 {
		t.Errorf("agents use %d bytes, want 7000000000", l.Memory)
	}
	if s := l.Scopes[1]; s.Memory != 3000000000 || s.OOMKills != 2 {
		t.Errorf("%s: memory %d, %d OOM kills; want 3000000000 and 2", s.Unit, s.Memory, s.OOMKills)
	}
	var got []string
	for _, s := range l.Scopes {
		got = append(got, s.Slice+"/"+s.Unit+" name="+s.Name+" agent="+strconv.Itoa(s.Agent)+" thread="+s.ThreadID)
	}
	wantScopes := []string{
		"app-t3code-agents-0123456789ab.slice/t3code-claude-00000000.scope name=claude agent=0 thread=",
		"app-t3code-agents-0123456789ab.slice/t3code-claude-deadbeef.scope name=claude agent=30 thread=1a2b3c4d-1111-4111-8111-111111111111",
		"app-t3code-agents-0123456789ab.slice/t3code-opencode-11111111.scope name=opencode agent=70 thread=",
		"app-t3code-agents-0123456789ab.slice/t3code-opencode-22222222.scope name=opencode agent=80 thread=aaaaaaaa-0000-4000-8000-000000000001",
		"app-t3code-agents-0123456789ab.slice/t3code-opencode-33333333.scope name=opencode agent=90 thread=",
		"app-t3code-agents.slice/t3code-codex-0a1b2c3d.scope name=codex agent=40 thread=",
	}
	if strings.Join(got, "\n") != strings.Join(wantScopes, "\n") {
		t.Errorf("scopes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantScopes, "\n"))
	}
}

func statLine(pid int, comm string, ppid int, start uint64) string {
	// pid (comm) state ppid pgrp session tty tpgid flags minflt cminflt
	// majflt cmajflt utime stime cutime cstime priority nice threads
	// itrealvalue starttime vsize ...
	return strconv.Itoa(pid) + " (" + comm + ") S " + strconv.Itoa(ppid) +
		" 1 1 0 -1 4194304 0 0 0 0 0 0 0 0 20 0 1 0 " + strconv.FormatUint(start, 10) + " 1000 10\n"
}

func TestDiscoverNoT3(t *testing.T) {
	if _, err := Discover(fstest.MapFS{}, fstest.MapFS{}, 1000); !errors.Is(err, ErrNoT3) {
		t.Fatalf("err %v, want ErrNoT3", err)
	}
}

func TestParseStat(t *testing.T) {
	// A command name with spaces and a ')' of its own.
	b := []byte("77 (a b) c)) S 12 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 98765 0 0\n")
	st, err := parseStat(b)
	if err != nil {
		t.Fatal(err)
	}
	if st.ppid != 12 || st.start != 98765 {
		t.Errorf("got %+v, want ppid 12, start 98765", st)
	}
	if _, err := parseStat([]byte("77 (x) S 12")); err == nil {
		t.Error("short stat accepted")
	}
}

func TestCgroupOf(t *testing.T) {
	proc := fstest.MapFS{"self/cgroup": {Data: []byte("1:net_cls:/\n0::/user.slice/a.scope\n")}}
	got, err := CgroupOf(proc, "self")
	if err != nil || got != "user.slice/a.scope" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestCLI(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := CLI(env(map[string]string{"T3CODE_CLI_PATH": "/x/t3", "HOME": t.TempDir()})); got != "/x/t3" {
		t.Errorf("with T3CODE_CLI_PATH: %q", got)
	}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".t3", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".t3", "bin", "t3"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := CLI(env(map[string]string{"HOME": home})); got != filepath.Join(home, ".t3", "bin", "t3") {
		t.Errorf("with ~/.t3/bin/t3: %q", got)
	}
	if got := CLI(env(map[string]string{"HOME": t.TempDir()})); got != "t3" {
		t.Errorf("with nothing: %q", got)
	}
}

func TestShowThreadRejectsNonIDs(t *testing.T) {
	for _, id := range []string{"", "--help", "-x", "abc", "1a2b3c4d ; rm"} {
		if _, err := ShowThreadJSON(t.Context(), "/nonexistent/t3", id); err == nil || !strings.Contains(err.Error(), "not a thread id") {
			t.Errorf("%q: err %v, want a refusal before running anything", id, err)
		}
	}
}
