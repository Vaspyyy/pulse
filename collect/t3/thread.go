package t3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
)

// Thread is what `t3 thread show --json` says about a thread.
type Thread struct {
	ID           string `json:"threadId"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	Model        string `json:"model"`
	Branch       string `json:"branch"`
	WorktreePath string `json:"worktreePath"` // "" when the thread works in the project root
	Link         string `json:"link"`
}

var threadID = regexp.MustCompile(`^[0-9A-Fa-f][0-9A-Fa-f-]{7,63}$`)

// CLI returns the t3 command: T3CODE_CLI_PATH, which T3 sets for its
// agents, then ~/.t3/bin/t3, then "t3" from PATH.
func CLI(getenv func(string) string) string {
	if p := getenv("T3CODE_CLI_PATH"); p != "" {
		return p
	}
	if home := getenv("HOME"); home != "" {
		p := filepath.Join(home, ".t3", "bin", "t3")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "t3"
}

// ShowThread asks T3 about a thread with `t3 thread show <id> --json`. It
// takes most of a second, nearly all of it the CLI starting (measured
// 2026-11-10), so callers cache the result.
func ShowThread(ctx context.Context, cli, id string) (Thread, error) {
	b, err := ShowThreadJSON(ctx, cli, id)
	if err != nil {
		return Thread{}, err
	}
	return ParseThread(b)
}

// ShowThreadJSON returns the raw output of `t3 thread show <id> --json`.
func ShowThreadJSON(ctx context.Context, cli, id string) ([]byte, error) {
	if !threadID.MatchString(id) {
		return nil, fmt.Errorf("t3: %q is not a thread id", id)
	}
	cmd := exec.CommandContext(ctx, cli, "thread", "show", id, "--messages", "0", "--json")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("t3 thread show: %w: %s", err, ee.Stderr)
		}
		return nil, fmt.Errorf("t3 thread show: %w", err)
	}
	return out, nil
}

// ParseThread reads the output of `t3 thread show --json`.
func ParseThread(b []byte) (Thread, error) {
	var v struct {
		Thread *Thread `json:"thread"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return Thread{}, fmt.Errorf("t3 thread show: %w", err)
	}
	if v.Thread == nil || v.Thread.ID == "" {
		return Thread{}, errors.New("t3 thread show: no thread in the output")
	}
	return *v.Thread, nil
}
