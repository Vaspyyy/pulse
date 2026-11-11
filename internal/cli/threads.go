package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Vaspyyy/pulse/collect/t3"
)

const (
	threadTTL   = 10 * time.Minute
	threadLimit = 3 * time.Second
)

type cachedThread struct {
	Title string    `json:"title"`
	Model string    `json:"model"`
	At    time.Time `json:"at"`
}

// ThreadLookup returns a lookup of T3 threads for Env.Threads. T3's CLI
// takes most of a second to answer, so answers are kept in cacheFile for
// ten minutes and missing ones are asked for in parallel, for at most
// three seconds. Only the title and the model are kept: a thread's status
// would be stale long before the ten minutes are over.
func ThreadLookup(t3cli, cacheFile string) func(ids []string) map[string]t3.Thread {
	return func(ids []string) map[string]t3.Thread {
		return lookupThreads(ids, cacheFile, time.Now(), func(ctx context.Context, id string) (t3.Thread, error) {
			return t3.ShowThread(ctx, t3cli, id)
		})
	}
}

func lookupThreads(ids []string, cacheFile string, now time.Time, show func(context.Context, string) (t3.Thread, error)) map[string]t3.Thread {
	cache := map[string]cachedThread{}
	if b, err := os.ReadFile(cacheFile); err == nil {
		_ = json.Unmarshal(b, &cache) // a broken cache is an empty one
	}
	out := map[string]t3.Thread{}
	ctx, cancel := context.WithTimeout(context.Background(), threadLimit)
	defer cancel()
	// Sort out first what's missing: the fetches below write to the cache.
	var missing []string
	for _, id := range ids {
		if c, ok := cache[id]; ok && now.Sub(c.At) < threadTTL {
			out[id] = t3.Thread{ID: id, Title: c.Title, Model: c.Model}
		} else {
			missing = append(missing, id)
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	fetched := false
	for _, id := range missing {
		wg.Go(func() {
			th, err := show(ctx, id)
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			out[id] = t3.Thread{ID: id, Title: th.Title, Model: th.Model}
			cache[id] = cachedThread{Title: th.Title, Model: th.Model, At: now}
			fetched = true
		})
	}
	wg.Wait()
	if fetched {
		for id, c := range cache {
			if now.Sub(c.At) >= threadTTL {
				delete(cache, id)
			}
		}
		writeCache(cacheFile, cache)
	}
	return out
}

// writeCache replaces the cache file in one step. Failing to is harmless:
// the next run asks T3 again.
func writeCache(name string, cache map[string]cachedThread) {
	b, err := json.Marshal(cache)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), ".t3-threads-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), name) != nil {
		os.Remove(tmp.Name())
	}
}

// StateDir is where Pulse keeps what it remembers between runs:
// $XDG_STATE_HOME/pulse-monitor. Not "pulse", which is PulseAudio's name.
func StateDir(getenv func(string) string) string {
	base := getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(getenv("HOME"), ".local", "state")
	}
	return filepath.Join(base, "pulse-monitor")
}
