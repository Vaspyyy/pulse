package query

import (
	"strings"
	"testing"

	"github.com/Vaspyyy/pulse/collect"
	"github.com/Vaspyyy/pulse/collect/collecttest"
)

const (
	gib = 1 << 30
	mib = 1 << 20
)

func viewOf(m *collecttest.Machine, used map[int]uint64) *View {
	c := collect.New(m.Roots())
	first := c.Snapshot()
	m.Run(25, used)
	return NewView(first, c.Snapshot(), m.Roots(), Options{UID: 1000})
}

func TestNameAndLabel(t *testing.T) {
	cases := []struct {
		comm       string
		args       []string
		name, want string
	}{
		{"kworker/0:1", nil, "kworker/0:1", "kworker/0:1"},
		{"bash", []string{"-bash"}, "bash", "bash"},
		{"chrome", []string{"/opt/google/chrome/chrome --type=renderer --lang=en"}, "chrome", "chrome"},
		{"nginx", []string{"nginx: master process /usr/bin/nginx"}, "nginx", "nginx"},
		{"Hades.exe", []string{`Z:\games\Hades\Hades.exe`}, "Hades.exe", "Hades.exe"},
		{"rustc", []string{"/usr/bin/rustc", "--edition=2021", "--crate-name", "syn"}, "rustc", "rustc syn"},
		{"cargo", []string{"cargo", "+nightly", "test", "--release"}, "cargo", "cargo test"},
		{"go", []string{"go", "test", "./..."}, "go", "go test"},
		{"python3", []string{"/usr/bin/python3.13", "-u", "tools/horizon.py", "build"}, "python3.13", "python3.13 horizon.py"},
		{"node", []string{"node", "/home/u/bin/rea", "mcp"}, "node", "node rea"},
		// A script given with -c is never shown: it can be long and private.
		{"sh", []string{"sh", "-c", "curl -H 'token: x' …\nexit"}, "sh", "sh -c"},
		{"nodejs-thing", []string{"nodejs-thing", "x"}, "nodejs-thing", "nodejs-thing"},
	}
	for _, c := range cases {
		p := &Proc{Process: &collect.Process{Comm: c.comm, Args: c.args}}
		if got := Name(p); got != c.name {
			t.Errorf("Name(%q) = %q, want %q", c.args, got, c.name)
		}
		if got := Label(p); got != c.want {
			t.Errorf("Label(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestRates(t *testing.T) {
	m := collecttest.New(8, 16*gib, 8*gib)
	m.Add(collecttest.P{PID: 10, PPID: 1, Comm: "a", Args: []string{"a"}, Start: 5, Ticks: 1000})
	c := collect.New(m.Roots())
	first := c.Snapshot()
	// pid 10 exits and its pid is reused by a process that started within
	// the window: its CPU time counts from zero, not from the old one's.
	m.Add(collecttest.P{PID: 10, PPID: 1, Comm: "b", Args: []string{"b"}, Start: 99999, Ticks: 0})
	m.Add(collecttest.P{PID: 11, PPID: 10, Comm: "c", Args: []string{"c"}, Start: 99999})
	m.Run(50, map[int]uint64{10: 10, 11: 100})
	v := NewView(first, c.Snapshot(), m.Roots(), Options{})
	if v.Window.Milliseconds() != 500 || v.NCPU != 8 {
		t.Errorf("window %v on %d CPUs; want 500ms on 8", v.Window, v.NCPU)
	}
	if got := v.Proc(10).Cores; got != 0.2 {
		t.Errorf("reused pid: %.2f cores, want 0.20", got)
	}
	if got := v.Proc(11).Cores; got != 2 {
		t.Errorf("new process: %.2f cores, want 2", got)
	}
	if v.Cores != 2.2 {
		t.Errorf("machine: %.2f cores, want 2.2", v.Cores)
	}
	if sub := v.Subtree(10); len(sub) != 2 {
		t.Errorf("subtree of 10 has %d processes, want 2", len(sub))
	}

	// One reading gives no rates, and no CPU verdict.
	one := NewView(nil, c.Snapshot(), m.Roots(), Options{})
	if one.Window != 0 || one.Proc(11).Cores != 0 {
		t.Errorf("one reading: window %v, %.1f cores", one.Window, one.Proc(11).Cores)
	}
	if l := one.Load(DefaultThresholds); l.Busy || !strings.Contains(l.Line(), "cpu not measured") {
		t.Errorf("one reading: %q", l.Line())
	}
}

func TestGames(t *testing.T) {
	scope := "user.slice/user-1000.slice/user@1000.service/app.slice/app-t3code.slice/app-t3code-agents.slice/t3code-claude-deadbeef.scope"
	m := collecttest.New(8, 16*gib, 8*gib)
	add := func(pid, ppid int, comm string, env map[string]string, args ...string) {
		m.Add(collecttest.P{PID: pid, PPID: ppid, Comm: comm, Args: args, Env: env, Start: uint64(pid), RSS: 100 * mib})
	}
	// Steam's client and its helper tools: no game.
	add(10, 1, "steam", nil, "steam")
	add(11, 10, "reaper", nil, "reaper", "SteamLaunch", "AppId=0", "--", "x")
	add(12, 10, "steamwebhelper", map[string]string{"SteamAppId": "0"}, "steamwebhelper")
	// A Steam game known only from its environment, with a child that has none.
	add(20, 10, "game", map[string]string{"SteamGameId": "620"}, "/games/portal2")
	add(21, 20, "helper", nil, "helper")
	// A Heroic game, and winetricks that Heroic started (GAMEID only).
	add(30, 1, "wine", map[string]string{"HEROIC_APP_NAME": "Quail", "HEROIC_APP_SOURCE": "epic", "HEROIC_APP_RUNNER": "legendary"}, "wine", "game.exe")
	add(31, 1, "winetricks", nil, "winetricks")
	// gamescope around something that has no launcher.
	add(40, 1, "gamescope", nil, "gamescope", "-W", "2560", "--", "/opt/mygame/run.sh", "--fast")
	add(41, 40, "run.sh", nil, "/bin/sh", "/opt/mygame/run.sh")
	// The same inside an agent's scope is a test, not a game.
	m.Add(collecttest.P{PID: 50, PPID: 1, Comm: "gamescope", Args: []string{"gamescope", "--", "godot"}, Cgroup: scope, Start: 50,
		Env: map[string]string{"SteamAppId": "620"}})
	m.Add(collecttest.P{PID: 51, PPID: 50, Comm: "godot", Args: []string{"godot"}, Cgroup: scope + "/pulse-r7k2mq", Start: 51})
	m.Cgroup(scope+"/cgroup.procs", "50\n")

	v := viewOf(m, nil)
	var got []string
	for _, g := range v.Games {
		got = append(got, g.Launcher+" "+g.Name+" ×"+string(rune('0'+g.Total.Count)))
	}
	want := []string{"steam Steam app 620 ×2", "heroic Quail (epic) ×1", "gamescope run.sh ×2"}
	if strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Errorf("games %v\nwant  %v", got, want)
	}
	for _, pid := range []int{10, 11, 12, 31, 50, 51} {
		if v.Proc(pid).Game != -1 {
			t.Errorf("pid %d (%s) counts as a game", pid, v.Proc(pid).Comm)
		}
	}
	// A run recorded in a child cgroup of the scope still is the agent's.
	if v.Proc(51).Scope != 0 || v.Proc(50).Scope != 0 || v.Proc(40).Scope != -1 {
		t.Errorf("scopes: 50 in %d, 51 in %d, 40 in %d", v.Proc(50).Scope, v.Proc(51).Scope, v.Proc(40).Scope)
	}
	if l := v.Load(DefaultThresholds); !l.Busy || len(l.Reasons) != 3 {
		t.Errorf("load %q; want busy with three games", l.Line())
	}
}

func TestSteamNames(t *testing.T) {
	home := t.TempDir()
	lib := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := mkfile(name, content); err != nil {
			t.Fatal(err)
		}
	}
	write(home+"/.local/share/Steam/steamapps/libraryfolders.vdf", "\"libraryfolders\"\n{\n\t\"1\"\n\t{\n\t\t\"path\"\t\t\""+lib+"\"\n\t}\n}\n")
	write(home+"/.local/share/Steam/steamapps/appmanifest_620.acf", "\"AppState\"\n{\n\t\"appid\"\t\t\"620\"\n\t\"name\"\t\t\"Portal 2\"\n}\n")
	write(lib+"/steamapps/appmanifest_1145360.acf", "\"AppState\"\n{\n\t\"name\"\t\t\"Hades\"\n}\n")
	name := SteamNames(home)
	for id, want := range map[string]string{"620": "Portal 2", "1145360": "Hades", "7": "", "../620": "", "": ""} {
		if got := name(id); got != want {
			t.Errorf("app %q: %q, want %q", id, got, want)
		}
	}
}
