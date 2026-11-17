package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pulse guard on a made-up PATH: a folder with pulse in it, and one with
// the system's pkill and pgrep, which know -A.
func TestGuard(t *testing.T) {
	bin, sys := t.TempDir(), t.TempDir()
	write := func(dir, name, help string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho '"+help+"'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	self := write(bin, "pulse", "")
	write(sys, "pkill", " -A, --ignore-ancestors")
	write(sys, "pgrep", " -A, --ignore-ancestors")
	path := bin + string(os.PathListSeparator) + sys
	guard := func(path string, args ...string) (string, string, int) {
		out, errOut, code := runEnv(t, nil, nil, func(env *Env) {
			env.Self = self
			env.Getenv = func(k string) string {
				if k == "PATH" {
					return path
				}
				return ""
			}
		}, append([]string{"guard"}, args...)...)
		return strings.NewReplacer(bin, "BIN", sys, "SYS").Replace(out), errOut, code
	}
	const off = `guard: off: pkill -f and pgrep -f also match the shell that runs them, and pkill kills it
next: pulse guard on   link pkill and pgrep to pulse in BIN
`
	const on = `guard: on: pkill -f and pgrep -f leave out the shell that runs them, and its parents
  pkill  BIN/pkill runs first, and hands over to SYS/pkill with -A
  pgrep  BIN/pgrep runs first, and hands over to SYS/pgrep with -A
next: pulse guard off   remove the links
`
	const behind = `guard: linked, but not working here:
  pkill  BIN/pkill is linked, but SYS/pkill comes first on PATH
  pgrep  BIN/pgrep is linked, but SYS/pgrep comes first on PATH
next: pulse guard off   remove the links
`
	steps := []struct {
		name, path string
		args       []string
		want       string
	}{
		{"before", path, nil, off},
		{"on", path, []string{"on"}, on},
		{"status", path, nil, on},
		{"behind", sys + string(os.PathListSeparator) + bin, nil, behind},
		{"off", path, []string{"off"}, off},
	}
	for _, s := range steps {
		if out, errOut, code := guard(s.path, s.args...); out != s.want || errOut != "" || code != 0 {
			t.Errorf("%s: exit %d, stderr %q, printed\n%s\nwant\n%s", s.name, code, errOut, out, s.want)
		}
	}
	if out, errOut, code := guard(path, "maybe"); out != "" || errOut != "pulse: guard takes on, off or nothing\n" || code != 2 {
		t.Errorf("guard maybe printed %q and %q, exit %d", out, errOut, code)
	}
	// Something else in a link's place: it stays, and guard on says so.
	other := write(bin, "pgrep", "mine")
	out, errOut, code := guard(path, "on")
	if want := "pulse: guard: " + other + " is something else, not pulse's link: nothing linked\n"; out != "" || errOut != want || code != 1 {
		t.Errorf("guard on over a file printed %q and %q, exit %d", out, errOut, code)
	}
	const blocked = `guard: off: pkill -f and pgrep -f also match the shell that runs them, and pkill kills it
  pkill  not linked
  pgrep  BIN/pgrep is something else, not pulse's link
next: pulse guard on   link pkill and pgrep to pulse in BIN
`
	if out, _, _ := guard(path); out != blocked {
		t.Errorf("blocked printed\n%s\nwant\n%s", out, blocked)
	}
}
