// Package guard is pulse standing in for pkill and pgrep.
//
// With -f both search whole command lines, and so they also find the shell
// that runs them: the pattern is part of that shell's own command line.
// pkill then kills the shell before the rest of its command runs, and
// pgrep reports a process that isn't the one asked about. procps-ng's
// pkill and pgrep have a flag that leaves a command's own ancestors out,
// -A. Linked as pkill or pgrep, pulse adds it to a search with -f and
// hands over to the real tool; everything else it passes on untouched.
package guard

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// Tools are the commands pulse stands in for.
var Tools = []string{"pkill", "pgrep"}

// Is reports whether name is one of the Tools.
func Is(name string) bool { return slices.Contains(Tools, name) }

// The flags that take a value, which is then not a flag itself: pgrep's
// and pkill's short ones, and the long ones of both.
var (
	shortValue = map[string]string{"pkill": "qgGOpPstuUFr", "pgrep": "dgGOpPstuUFr"}
	longFlags  = map[string]bool{ // true: takes a value
		"delimiter": true, "queue": true, "pgroup": true, "group": true, "older": true,
		"pid": true, "parent": true, "session": true, "signal": true, "terminal": true,
		"euid": true, "uid": true, "pidfile": true, "runstates": true, "cgroup": true,
		"ns": true, "nslist": true, "env": true,
		"require-handler": false, "echo": false, "mrelease": false, "count": false,
		"full": false, "ignore-case": false, "newest": false, "oldest": false,
		"exact": false, "logpidfile": false, "ignore-ancestors": false,
		"shell-quote": false, "list-name": false, "list-full": false, "quiet": false,
		"inverse": false, "lightweight": false, "help": false, "version": false,
	}
	signals = strings.Fields("HUP INT QUIT ILL TRAP ABRT IOT BUS FPE KILL USR1 SEGV USR2 PIPE " +
		"ALRM TERM STKFLT CHLD CLD CONT STOP TSTP TTIN TTOU URG XCPU XFSZ VTALRM PROF WINCH " +
		"IO POLL PWR SYS RTMIN RTMAX")
)

// long is the long flag that name stands for: getopt takes any prefix
// that fits only one. "" when it fits none or several.
func long(name string) string {
	if _, ok := longFlags[name]; ok {
		return name
	}
	found := ""
	for f := range longFlags {
		if strings.HasPrefix(f, name) {
			if found != "" {
				return ""
			}
			found = f
		}
	}
	return found
}

// signal reports whether s, an argument without its dash, is a signal as
// pkill takes it anywhere among its arguments: a number or a name, in
// either case, with or without SIG (-9, -TERM, -sigint, -RTMIN+2).
func signal(s string) bool {
	if _, err := strconv.Atoi(s); err == nil {
		return true
	}
	s = strings.TrimPrefix(strings.ToUpper(s), "SIG")
	if rt, n, ok := strings.Cut(s, "+"); ok && rt == "RTMIN" {
		_, err := strconv.Atoi(n)
		return err == nil
	}
	if rt, n, ok := strings.Cut(s, "-"); ok && rt == "RTMAX" {
		_, err := strconv.Atoi(n)
		return err == nil
	}
	return slices.Contains(signals, s)
}

// wants reports whether tool, called with args, should leave its ancestors
// out: it searches whole command lines (-f), and neither says so itself
// (-A) nor asks for everything but the matches (-v), which is a different
// question.
func wants(tool string, args []string) bool {
	full := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return full
		case strings.HasPrefix(a, "--"):
			name, _, hasValue := strings.Cut(a[2:], "=")
			switch name = long(name); name {
			case "full":
				full = true
			case "ignore-ancestors", "inverse":
				return false
			default:
				if longFlags[name] && !hasValue {
					i++
				}
			}
		case len(a) > 1 && a[0] == '-':
			if tool == "pkill" && signal(a[1:]) {
				continue
			}
			for j := 1; j < len(a); j++ {
				if strings.IndexByte(shortValue[tool], a[j]) >= 0 {
					// The rest of the argument is its value, or the next one is.
					if j == len(a)-1 {
						i++
					}
					break
				}
				switch a[j] {
				case 'f':
					full = true
				case 'A', 'v':
					return false
				}
			}
		}
	}
	return full
}

// Args returns the arguments the real tool gets: args, with -A in front
// when the search is by whole command line.
func Args(tool string, args []string) []string {
	if !wants(tool, args) {
		return args
	}
	return append([]string{"-A"}, args...)
}

// same reports whether a and b are one file, links followed.
func same(a, b string) bool {
	x, err := os.Stat(a)
	if err != nil {
		return false
	}
	y, err := os.Stat(b)
	return err == nil && os.SameFile(x, y)
}

// first returns the first file named tool that a shell with this PATH
// would run, "" when there is none.
func first(tool, path string) string {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, tool)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// Real returns the tool to hand over to: the first one on path after
// pulse's own link, or the first that isn't self, the pulse binary, when
// the link isn't on path. Looking only behind the link keeps two pulse
// binaries, each linked in a folder of its own, from handing over to each
// other for ever. /usr/bin and /bin are looked in last, for a PATH that
// names neither.
func Real(tool, self, path string) (string, error) {
	dirs := append(filepath.SplitList(path), "/usr/bin", "/bin")
	for i, dir := range dirs {
		if same(filepath.Join(dir, tool), self) {
			dirs = dirs[i+1:]
			break
		}
	}
	for _, dir := range dirs {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, tool)
		if fi, err := os.Stat(p); err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 || same(p, self) {
			continue
		}
		return p, nil
	}
	return "", fmt.Errorf("no %s on PATH besides pulse's link", tool)
}

// Skips reports whether the tool at real knows -A, as newer procps-ng's
// does. Its help says so.
func Skips(real string) bool {
	out, _ := exec.Command(real, "--help").CombinedOutput()
	return bytes.Contains(out, []byte("--ignore-ancestors"))
}

// Exec runs the real tool in pulse's place, with -A added where it
// belongs. It only returns when that fails, with the exit code.
func Exec(tool string, args []string) int {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", tool, err)
		return 126
	}
	real, err := Real(tool, self, os.Getenv("PATH"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", tool, err)
		return 127
	}
	if with := Args(tool, args); len(with) != len(args) && Skips(real) {
		args = with
	}
	// The real tool reads its name to tell pkill from pgrep.
	err = syscall.Exec(real, append([]string{real}, args...), os.Environ())
	fmt.Fprintf(os.Stderr, "%s: %s: %v\n", tool, real, err)
	return 126
}

// State is how one tool stands.
type State struct {
	Tool    string
	Link    string // where pulse's link is or would be: next to the pulse binary
	Linked  bool   // Link is a link to pulse
	Blocked bool   // something else is at Link
	First   string // what a shell with this PATH runs for Tool, "" for nothing
	Leads   bool   // First is pulse
	Real    string // what pulse hands over to, "" when there is nothing
	Skips   bool   // Real knows -A
}

// On reports whether the tool leaves its ancestors out, called by name
// with this PATH.
func (s State) On() bool { return s.Leads && s.Real != "" && s.Skips }

// Status returns the state of every tool, for the pulse binary self and a
// PATH.
func Status(self, path string) []State {
	var all []State
	for _, tool := range Tools {
		s := State{Tool: tool, Link: filepath.Join(filepath.Dir(self), tool), First: first(tool, path)}
		s.Leads = s.First != "" && same(s.First, self)
		if fi, err := os.Lstat(s.Link); err == nil {
			s.Linked = fi.Mode()&os.ModeSymlink != 0 && same(s.Link, self)
			s.Blocked = !s.Linked
		}
		if real, err := Real(tool, self, path); err == nil {
			s.Real, s.Skips = real, Skips(real)
		}
		all = append(all, s)
	}
	return all
}

// Link links every tool to self, next to it. Something else in a link's
// place is left alone, and then nothing is linked.
func Link(self string) error {
	states := Status(self, "")
	for _, s := range states {
		if s.Blocked {
			return fmt.Errorf("%s is something else, not pulse's link: nothing linked", s.Link)
		}
	}
	for _, s := range states {
		if s.Linked {
			continue
		}
		// By name, so the folder can move.
		if err := os.Symlink(filepath.Base(self), s.Link); err != nil {
			return err
		}
	}
	return nil
}

// Unlink removes the links to self; anything else it leaves alone.
func Unlink(self string) error {
	var errs []error
	for _, s := range Status(self, "") {
		if s.Linked {
			errs = append(errs, os.Remove(s.Link))
		}
	}
	return errors.Join(errs...)
}
