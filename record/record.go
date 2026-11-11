// Package record runs a command and records what it used: CPU time,
// memory, stalls, IO, GPU, and what else the machine was doing meanwhile.
// It also keeps the recorded runs (see Store). docs/design.md, "Recording
// runs", says where each number comes from and how exact it is.
package record

import (
	"compress/gzip"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Run is one recorded run of a command.
type Run struct {
	V       int      `json:"v"` // format version, 1
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"` // given with pulse run -n
	Command []string `json:"command"`
	Cwd     string   `json:"cwd"`
	Repo    *Repo    `json:"repo,omitempty"`
	Session string   `json:"session,omitempty"` // the agent session it ran in, as "t3:<thread id>"

	Start  time.Time `json:"start"`
	WallMS int64     `json:"wall_ms"`
	Exit   int       `json:"exit"`             // the exit code, 128 plus the signal for a killed command
	Signal string    `json:"signal,omitempty"` // the signal that killed it

	// Method is how the run was measured: "cgroup" (in a cgroup of its
	// own: CPU time, members and stalls are exact) or "tree" (the process
	// tree was sampled and CPU time comes from wait4: processes that left
	// the tree are missed).
	Method string `json:"method"`
	NCPU   int    `json:"ncpu"`

	UserMS   int64 `json:"user_ms"`
	SystemMS int64 `json:"system_ms"`

	// PeakPrivate is the most private memory the run's processes used
	// together, in bytes, as sampled: a spike between two samples is missed.
	PeakPrivate uint64 `json:"peak_private_bytes"`
	SampleMS    int    `json:"sample_ms"` // the first sampling interval
	Samples     int    `json:"samples"`   // 0: the run was over before the first one
	// LargestRSS is the biggest any one process got, in bytes: the larger
	// of what wait4 reported and what was sampled. LargestName is "" when
	// that process was too short-lived to be sampled.
	LargestRSS  uint64 `json:"largest_rss_bytes"`
	LargestName string `json:"largest_name,omitempty"`

	Stall *Stall `json:"stall,omitempty"` // only with Method "cgroup"

	// ReadBytes and WrittenBytes are disk IO as wait4 counted it, for the
	// command and the descendants it waited for.
	ReadBytes    uint64 `json:"read_bytes"`
	WrittenBytes uint64 `json:"written_bytes"`

	GPU   *GPU  `json:"gpu,omitempty"` // only when the run used a GPU
	Other Other `json:"other"`

	// Procs are the run's processes by what they ran, heaviest first, as
	// sampled. UnsampledMS is the CPU time of processes that were never
	// caught by a sample, or that used CPU after their last one.
	Procs       []ProcStat `json:"procs"`
	MoreProcs   int        `json:"more_procs,omitempty"` // kinds left out of Procs
	UnsampledMS int64      `json:"unsampled_ms"`
	// Left are processes the command left running.
	Left []string `json:"left,omitempty"`

	Timeline Timeline `json:"timeline"`
}

// Repo is the git state the run started in.
type Repo struct {
	Root   string `json:"root"`
	Head   string `json:"head"`
	Branch string `json:"branch,omitempty"`
	// State is "clean", "dirty" (tracked files changed), or "" when git
	// didn't answer in time.
	State string `json:"state,omitempty"`
}

// Stall is how long at least one of the run's processes waited for a
// resource (the "some" pressure total of the run's cgroup).
type Stall struct {
	CPUMS    int64 `json:"cpu_ms"`
	MemoryMS int64 `json:"memory_ms"`
	IOMS     int64 `json:"io_ms"`
}

// GPU is the run's GPU use, sampled every two seconds.
type GPU struct {
	PeakVRAM uint64  `json:"peak_vram_bytes"`
	AvgUtil  float64 `json:"avg_util_percent"`
}

// Other is what else ran during the run.
type Other struct {
	// Known is false for a run too short to tell what else ran: the
	// kernel counts CPU time in hundredths of a second.
	Known bool `json:"known"`
	// AvgCores is the CPU everything else used, on average: exact. Peak
	// is the highest two-second average.
	AvgCores  float64  `json:"avg_cores"`
	PeakCores float64  `json:"peak_cores"`
	Games     []string `json:"games,omitempty"`
	// Disturbed says the run's wall time isn't comparable with a quiet
	// run's: a game ran, or other work used a quarter of the CPUs.
	Disturbed bool `json:"disturbed"`
}

// ProcStat sums the processes that ran the same thing.
type ProcStat struct {
	Label   string `json:"label"`
	Count   int    `json:"count"`
	CPUMS   int64  `json:"cpu_ms"`
	PeakRSS uint64 `json:"peak_rss_bytes"` // the largest single one
}

// Timeline is the run over time, in at most 600 steps.
type Timeline struct {
	StepMS  int       `json:"step_ms"`
	Cores   []float32 `json:"cores"`      // average over the step
	Private []uint32  `json:"private_mb"` // highest in the step, in MiB
}

// CPUMS is the run's CPU time.
func (r *Run) CPUMS() int64 { return r.UserMS + r.SystemMS }

// Store keeps recorded runs: one gzipped JSON file per run in Dir.
type Store struct {
	Dir string
	// MaxRuns and MaxBytes bound the store; the oldest runs go first.
	// Zero means 200 runs and 200 MiB.
	MaxRuns  int
	MaxBytes int64
}

const suffix = ".json.gz"

var (
	idForm   = regexp.MustCompile(`^r[a-hjkmnp-z2-9]{5}$`)
	nameForm = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)
)

// ValidName reports whether name can be given to a run: letters, digits,
// dots, dashes and underscores, and nothing that reads as a run id or as
// "last".
func ValidName(name string) bool {
	return nameForm.MatchString(name) && !idForm.MatchString(name) && name != "last"
}

// newID returns a fresh run id: r and five letters or digits that don't
// look like one another.
func newID() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	var b [5]byte
	_, _ = rand.Read(b[:]) // never fails
	id := []byte("r")
	for _, c := range b {
		id = append(id, alphabet[int(c)%len(alphabet)])
	}
	return string(id)
}

// Save writes r and then drops the oldest runs beyond the store's bounds.
func (s Store) Save(r *Run) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".run-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	zw := gzip.NewWriter(tmp)
	err = json.NewEncoder(zw).Encode(r)
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	// The name sorts by start time.
	name := r.Start.UTC().Format("20060102T150405.000") + "-" + r.ID + suffix
	if err := os.Rename(tmp.Name(), filepath.Join(s.Dir, name)); err != nil {
		return err
	}
	s.prune()
	return nil
}

func (s Store) files() []string {
	names, _ := filepath.Glob(filepath.Join(s.Dir, "*-r?????"+suffix))
	slices.Sort(names)
	return names
}

func (s Store) prune() {
	maxRuns, maxBytes := s.MaxRuns, s.MaxBytes
	if maxRuns == 0 {
		maxRuns = 200
	}
	if maxBytes == 0 {
		maxBytes = 200 << 20
	}
	files := s.files()
	var total int64
	sizes := make([]int64, len(files))
	for i, f := range files {
		if fi, err := os.Stat(f); err == nil {
			sizes[i] = fi.Size()
			total += fi.Size()
		}
	}
	for i := 0; i < len(files)-1 && (len(files)-i > maxRuns || total > maxBytes); i++ {
		if os.Remove(files[i]) == nil {
			total -= sizes[i]
		}
	}
}

func load(file string) (*Run, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	r := &Run{}
	if err := json.Unmarshal(b, r); err != nil {
		return nil, err
	}
	return r, nil
}

// List returns the stored runs, newest first. Files it can't read are
// left out.
func (s Store) List() []*Run {
	files := s.files()
	runs := make([]*Run, 0, len(files))
	for _, f := range slices.Backward(files) {
		if r, err := load(f); err == nil {
			runs = append(runs, r)
		}
	}
	return runs
}

// ErrNoRun is returned by Find when no stored run matches.
var ErrNoRun = errors.New("no such run")

// Find returns the run that ref names: a run id, "last" for the newest
// run, or the name given with pulse run -n, which means the newest run of
// that name.
func (s Store) Find(ref string) (*Run, error) {
	if idForm.MatchString(ref) {
		if m, _ := filepath.Glob(filepath.Join(s.Dir, "*-"+ref+suffix)); len(m) > 0 {
			return load(m[len(m)-1])
		}
		return nil, fmt.Errorf("%w: %s", ErrNoRun, ref)
	}
	for _, r := range s.List() {
		if ref == "last" || r.Name == ref {
			return r, nil
		}
	}
	if ref == "last" {
		return nil, fmt.Errorf("%w: nothing is recorded yet", ErrNoRun)
	}
	return nil, fmt.Errorf("%w: %s is neither a run id nor a run's name", ErrNoRun, ref)
}

// Same reports whether two runs are the same command in the same place,
// the precondition for comparing them.
func Same(a, b *Run) bool {
	return a.Cwd == b.Cwd && strings.Join(a.Command, "\x00") == strings.Join(b.Command, "\x00")
}
