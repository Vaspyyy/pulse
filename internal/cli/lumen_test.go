package cli

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Vaspyyy/pulse/collect"
)

// inLumen is a terminal that is lumen.
func inLumen(env *Env) {
	env.StdoutTerminal, env.StderrTerminal = true, true
	env.Getenv = func(k string) string {
		if k == "TERM_PROGRAM" {
			return "lumen"
		}
		return ""
	}
}

var osc8 = regexp.MustCompile(`\x1b\]8;;[^\x1b]*\x1b\\`)

// TestLinks: in lumen, handles, run ids and next: lines are links that
// put a command at the prompt. Without the links the output is what it
// is everywhere else, to the byte.
func TestLinks(t *testing.T) {
	plain, _, _ := run(t, desk(), work, "top")
	linked, _, code := runEnv(t, desk(), work, inLumen, "top")
	if code != 0 || osc8.ReplaceAllString(linked, "") != plain {
		t.Fatalf("exit %d; without its links the output differs from the plain one:\n%q\n%q", code, linked, plain)
	}
	// The game's handle, and the first next: line.
	for _, want := range []string{
		"\x1b]8;;lumen-prompt:pulse%20tree%20200.",
		"next: \x1b]8;;lumen-prompt:pulse%20tree%20",
		"\x1b]8;;lumen-prompt:pulse%20top%20--by%20mem\x1b\\pulse top --by mem\x1b]8;;\x1b\\",
	} {
		if !strings.Contains(linked, want) {
			t.Errorf("no %q in:\n%q", want, linked)
		}
	}

	// A terminal that isn't lumen, and lumen's output into a pipe, get none.
	other := func(env *Env) { env.StdoutTerminal = true }
	piped := func(env *Env) { inLumen(env); env.StdoutTerminal = false }
	for name, change := range map[string]func(*Env){"another terminal": other, "a pipe": piped} {
		if out, _, _ := runEnv(t, desk(), work, change, "top"); out != plain {
			t.Errorf("%s got links:\n%q", name, out)
		}
	}
	// The config file has the last word, both ways.
	on := func(env *Env) { env.StdoutTerminal, env.Config = true, "links = on\n" }
	if out, _, _ := runEnv(t, desk(), work, on, "top"); !strings.Contains(out, "lumen-prompt:") {
		t.Errorf("links = on gave none:\n%q", out)
	}
	off := func(env *Env) { inLumen(env); env.Config = "links = off\n" }
	if out, _, _ := runEnv(t, desk(), work, off, "top"); out != plain {
		t.Errorf("links = off still links:\n%q", out)
	}

	// Run ids link to their record, and only the ones that exist: the
	// word "reaper" has the form of one.
	m := desk()
	both := func(env *Env) { recorded(t)(env); inLumen(env) }
	out, _, _ := runEnv(t, m, nil, both, "runs")
	if !strings.Contains(out, "\x1b]8;;lumen-prompt:pulse%20show%20r7k2mq\x1b\\r7k2mq\x1b]8;;\x1b\\") {
		t.Errorf("no link on a run id:\n%q", out)
	}
	if l := (&links{ids: []string{"r7k2mq"}}); l.line("reaper r7k2mq 12.5 GB") != "reaper "+promptLink("pulse show r7k2mq", "r7k2mq")+" 12.5 GB" {
		t.Errorf("linked: %q", l.line("reaper r7k2mq 12.5 GB"))
	}
	// A command with a placeholder isn't one to run.
	if got := (&links{}).command("pulse run -- <command>"); got != "pulse run -- <command>" {
		t.Errorf("a placeholder was linked: %q", got)
	}
}

// TestWatchPids: a line on standard input changes what the next
// readings report.
func TestWatchPids(t *testing.T) {
	m := desk()
	in, feed := io.Pipe()
	readings := 0
	change := func(env *Env) {
		env.Stdin = in
		sleep := env.Sleep
		env.Sleep = func(d time.Duration) {
			sleep(d)
			if readings++; readings == 2 {
				// The second line is only taken once the first is acted on.
				io.WriteString(feed, "pids 190, 310\n")
				io.WriteString(feed, "pids 190, 310\n")
			}
		}
	}
	out, errOut, code := runEnv(t, m, work, change, "watch", "--json", "-n", "2", "--pids", "300")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	var got [][]int
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var r Reading
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("%v in %q", err, line)
		}
		var pids []int
		for _, p := range r.Pids {
			pids = append(pids, p.PID)
		}
		got = append(got, pids)
	}
	if len(got) != 2 || len(got[0]) != 1 || got[0][0] != 300 || len(got[1]) != 2 || got[1][0] != 190 || got[1][1] != 310 {
		t.Errorf("pids per reading: %v; want [300] and then [190 310]", got)
	}
}

func TestWatchErrors(t *testing.T) {
	for args, want := range map[string]string{
		"watch --every 0.1":  "want seconds, from 0.25 to 3600",
		"watch --pids 12,x":  `"x" is no pid`,
		"watch -n 0":         "want a number of readings",
		"watch --frob":       "unknown argument --frob",
		"watch --every":      "--every needs a value",
		"watch --json extra": "unknown argument extra",
	} {
		out, errOut, code := run(t, desk(), nil, strings.Fields(args)...)
		if code != 2 || out != "" || !strings.Contains(errOut, want) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want exit 2 and %q", args, code, out, errOut, want)
		}
	}
}

// TestRunNotifies: a run that took long enough tells the terminal when
// it ends, and its id is a link. It runs a real command.
func TestRunNotifies(t *testing.T) {
	real := func(config string, terminal bool) func(*Env) {
		return func(env *Env) {
			inLumen(env)
			env.StderrTerminal = terminal
			env.Roots, env.StateDir, env.Config = collect.Live(), t.TempDir(), config
			env.Sleep = time.Sleep
			env.Now = nil
		}
	}
	_, errOut, code := runEnv(t, nil, nil, real("notify_after = 0.01\n", true), "run", "--", "sh", "-c", "sleep 0.1; exit 3")
	if code != 3 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !regexp.MustCompile(`\x1b\]777;notify;sh -c sleep 0\.1, exit 3;exit 3 after 0\.\d s, peak [^;\x1b]+\x1b\\$`).MatchString(errOut) {
		t.Errorf("no notification at the end of:\n%q", errOut)
	}
	if !strings.Contains(errOut, "recorded as \x1b]8;;lumen-prompt:pulse%20show%20r") {
		t.Errorf("the run's id is no link:\n%q", errOut)
	}
	// A short run says nothing, and neither does one whose output is a file.
	for name, change := range map[string]func(*Env){
		"a short run":      real("", true),
		"notify_after = 0": real("notify_after = 0\n", true),
		"no terminal":      real("notify_after = 0.01\n", false),
	} {
		if _, errOut, _ := runEnv(t, nil, nil, change, "run", "--", "sh", "-c", "sleep 0.1"); strings.Contains(errOut, "\x1b]777") {
			t.Errorf("%s was announced:\n%q", name, errOut)
		}
	}
	if _, errOut, _ := runEnv(t, nil, nil, real("notify_after = 0.01\n", false), "run", "--", "true"); strings.Contains(errOut, "\x1b") {
		t.Errorf("escape sequences without a terminal:\n%q", errOut)
	}
}
