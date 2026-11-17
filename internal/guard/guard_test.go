package guard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// Linked as pkill or pgrep, the test binary is that tool, as pulse is.
func TestMain(m *testing.M) {
	if tool := filepath.Base(os.Args[0]); Is(tool) {
		os.Exit(Exec(tool, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func TestArgs(t *testing.T) {
	cases := []struct {
		tool, args string
		added      bool
	}{
		{"pkill", "-f godot", true},
		{"pkill", "--full godot", true},
		{"pkill", "--fu godot", true}, // getopt takes a prefix that fits one flag
		{"pkill", "-9 -f godot", true},
		{"pkill", "-ef godot", true},
		{"pkill", "godot -f", true},       // flags stand anywhere
		{"pkill", "-TERM -f godot", true}, // -TERM is a signal, not -T -E -R -M
		{"pkill", "-int -f godot", true},  // or -i -n -t <tty>
		{"pkill", "-io -f godot", true},   // SIGIO, not -i -o
		{"pkill", "-RTMIN+2 -f godot", true},
		{"pkill", "--signal KILL -f godot", true},
		{"pkill", "-u root -f godot", true},
		{"pgrep", "-af godot", true},
		{"pgrep", "-fa godot", true},
		{"pgrep", "-fc godot", true},
		{"pgrep", "-d, -f godot", true},

		{"pkill", "godot", false},       // by name: the shell's name isn't the pattern
		{"pkill", "-x godot", false},    //
		{"pkill", "-u f godot", false},  // f is -u's value
		{"pkill", "-uf godot", false},   //
		{"pkill", "-t -f godot", false}, // -f is -t's value
		{"pkill", "--signal -f godot", false},
		{"pkill", "--signal=9 godot", false},
		{"pkill", "-- -f", false},       // after --, -f is the pattern
		{"pkill", "-A -f godot", false}, // says so itself
		{"pkill", "-fA godot", false},   //
		{"pkill", "--ignore-ancestors -f godot", false},
		{"pgrep", "-v -f godot", false}, // everything but the matches
		{"pgrep", "--inverse -f godot", false},
		{"pgrep", "-x zsh", false},
		{"pgrep", "-io -f godot", true}, // only pkill takes signals
	}
	for _, c := range cases {
		args := strings.Fields(c.args)
		got := Args(c.tool, args)
		want := args
		if c.added {
			want = append([]string{"-A"}, args...)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s %s: got %q, want %q", c.tool, c.args, got, want)
		}
	}
}

// tool writes a program named name into dir that prints help when asked.
func tool(t *testing.T, dir, name, help string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho '"+help+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLinks(t *testing.T) {
	bin, sys, old := t.TempDir(), t.TempDir(), t.TempDir()
	self := tool(t, bin, "pulse", "")
	realPkill := tool(t, sys, "pkill", " -A, --ignore-ancestors")
	realPgrep := tool(t, sys, "pgrep", " -A, --ignore-ancestors")
	tool(t, old, "pkill", " -f, --full")
	tool(t, old, "pgrep", " -f, --full")
	path := bin + string(os.PathListSeparator) + sys

	for _, s := range Status(self, path) {
		if s.Linked || s.Blocked || s.Leads || s.On() {
			t.Errorf("before: %+v", s)
		}
	}
	if err := Link(self); err != nil {
		t.Fatal(err)
	}
	if err := Link(self); err != nil { // twice is fine
		t.Fatal(err)
	}
	got := Status(self, path)
	want := []State{
		{Tool: "pkill", Link: filepath.Join(bin, "pkill"), Linked: true, First: filepath.Join(bin, "pkill"), Leads: true, Real: realPkill, Skips: true},
		{Tool: "pgrep", Link: filepath.Join(bin, "pgrep"), Linked: true, First: filepath.Join(bin, "pgrep"), Leads: true, Real: realPgrep, Skips: true},
	}
	if !slices.Equal(got, want) {
		t.Errorf("linked:\n got %+v\nwant %+v", got, want)
	}
	for _, s := range got {
		if !s.On() {
			t.Errorf("%s isn't on", s.Tool)
		}
	}
	// The real tool first on PATH: linked, but not what runs.
	for _, s := range Status(self, sys+string(os.PathListSeparator)+bin) {
		if !s.Linked || s.Leads || s.On() || s.First != filepath.Join(sys, s.Tool) {
			t.Errorf("real first: %+v", s)
		}
	}
	// A real tool without -A: nothing to add.
	for _, s := range Status(self, bin+string(os.PathListSeparator)+old) {
		if !s.Leads || s.Skips || s.On() {
			t.Errorf("old tool: %+v", s)
		}
	}
	if err := Unlink(self); err != nil {
		t.Fatal(err)
	}
	for _, s := range Status(self, path) {
		if s.Linked || s.Blocked || s.First != filepath.Join(sys, s.Tool) {
			t.Errorf("after: %+v", s)
		}
	}
}

// Something else named pkill next to pulse stays, and nothing is linked.
func TestLinkBlocked(t *testing.T) {
	bin := t.TempDir()
	self := tool(t, bin, "pulse", "")
	other := tool(t, bin, "pgrep", "mine")
	if err := Link(self); err == nil || !strings.Contains(err.Error(), other) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(bin, "pkill")); err == nil {
		t.Error("pkill was linked all the same")
	}
	if err := Unlink(self); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("the other pgrep is gone")
	}
}

func TestReal(t *testing.T) {
	bin, sys := t.TempDir(), t.TempDir()
	self := tool(t, bin, "pulse", "")
	if err := Link(self); err != nil {
		t.Fatal(err)
	}
	real := tool(t, sys, "pkill", "")
	if got, err := Real("pkill", self, bin+string(os.PathListSeparator)+sys); err != nil || got != real {
		t.Errorf("got %q, %v", got, err)
	}
	// Not a program, and a folder: neither is the tool.
	notes, dirs := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(notes, "pkill"), nil, 0o644)
	os.Mkdir(filepath.Join(dirs, "pkill"), 0o755)
	path := strings.Join([]string{notes, dirs, bin, sys}, string(os.PathListSeparator))
	if got, err := Real("pkill", self, path); err != nil || got != real {
		t.Errorf("got %q, %v", got, err)
	}
	// A second pulse, linked in a folder of its own: each hands over to
	// what comes after it, never back to the one before.
	bin2 := t.TempDir()
	self2 := tool(t, bin2, "pulse", "second")
	if err := Link(self2); err != nil {
		t.Fatal(err)
	}
	path = strings.Join([]string{bin, bin2, sys}, string(os.PathListSeparator))
	if got, err := Real("pkill", self, path); err != nil || got != filepath.Join(bin2, "pkill") {
		t.Errorf("the first pulse: got %q, %v", got, err)
	}
	if got, err := Real("pkill", self2, path); err != nil || got != real {
		t.Errorf("the second pulse: got %q, %v", got, err)
	}
}

// The whole of it on this machine's pkill and pgrep: a shell whose command
// line holds the pattern outlives its own pkill -f, and pgrep -f doesn't
// list it, while the process that was meant is found and killed.
func TestSparesTheShell(t *testing.T) {
	path := os.Getenv("PATH")
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	for _, name := range Tools {
		real, err := Real(name, self, path)
		if err != nil || !Skips(real) {
			t.Skipf("no %s with -A here", name)
		}
	}
	bin := t.TempDir()
	for _, name := range Tools {
		if err := os.Symlink(self, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	// A command line no other process has.
	mark := fmt.Sprintf("sleep 600.%d", os.Getpid())
	victim := exec.Command("sleep", strings.TrimPrefix(mark, "sleep "))
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	done := false
	defer func() {
		if !done {
			victim.Process.Kill()
			victim.Wait()
		}
	}()
	sh := exec.Command("sh", "-c", fmt.Sprintf("pgrep -f '%s'; pkill -f '%s'; echo outlived $?", mark, mark))
	sh.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+path)
	out, err := sh.CombinedOutput()
	if want := strconv.Itoa(victim.Process.Pid) + "\noutlived 0\n"; string(out) != want || err != nil {
		t.Fatalf("got %q (%v), want %q", out, err, want)
	}
	err = victim.Wait()
	done = true
	var exit *exec.ExitError
	if ee, ok := err.(*exec.ExitError); ok {
		exit = ee
	}
	if exit == nil || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
		t.Errorf("the process that was meant: %v, want killed by SIGTERM", err)
	}
}
