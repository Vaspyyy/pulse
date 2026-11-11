package collect

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
)

// handleLetters are the 23 lowercase letters that don't look like digits
// (no i, l or o).
const handleLetters = "abcdefghjkmnpqrstuvwxyz"

// Handle names a process for good: its pid, a dot and three letters that
// are a hash of the boot id, the pid and the start time, as in "4821.kfx".
// The pid stays usable for kill and strace, and a reused pid gets other
// letters (docs/design.md, "Process identity").
func Handle(bootID string, pid int, start uint64) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s/%d/%d", bootID, pid, start)
	n := h.Sum64() % (23 * 23 * 23)
	return strconv.Itoa(pid) + "." + string([]byte{
		handleLetters[n/(23*23)], handleLetters[n/23%23], handleLetters[n%23],
	})
}

// Handle returns p's handle in snapshot s.
func (s *Snapshot) Handle(p *Process) string {
	return Handle(s.BootID, p.PID, p.Start)
}

// ParseHandle splits "4821.kfx" into the pid and the letters. A bare pid
// is accepted too and has no letters.
func ParseHandle(h string) (pid int, letters string, ok bool) {
	num, letters, dotted := strings.Cut(h, ".")
	pid, err := strconv.Atoi(num)
	if err != nil || pid <= 0 {
		return 0, "", false
	}
	if dotted {
		if len(letters) != 3 || strings.Trim(letters, handleLetters) != "" {
			return 0, "", false
		}
	}
	return pid, letters, true
}
