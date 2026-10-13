// Package mark is the timing sink the transcript benchmarks read: one JSON
// line per event, appended to the file named by FIGARO_MARKS. Unset, a call
// is one atomic load. Names and fields: skills/figaro/contributing/marks.md.
package mark

import (
	"encoding/json"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const Env = "FIGARO_MARKS"

var (
	enabled atomic.Bool
	once    sync.Once
	mu      sync.Mutex
	out     *os.File
	proc    string
	pid     string
	seq     atomic.Uint64
)

// Init names the process and opens the sink if FIGARO_MARKS is set; the
// first call wins.
func Init(process string) {
	once.Do(func() {
		path := os.Getenv(Env)
		if path == "" {
			return
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		out, proc, pid = f, process, strconv.Itoa(os.Getpid())
		enabled.Store(true)
	})
}

func Enabled() bool { return enabled.Load() }

// Now is the clock the sink stamps with.
func Now() time.Time { return time.Now() }

// Mark: kv alternates keys and values.
func Mark(name string, kv ...any) {
	if !enabled.Load() {
		return
	}
	at(time.Now(), name, kv...)
}

func at(t time.Time, name string, kv ...any) {
	rec := make(map[string]any, len(kv)/2+5)
	rec["t"] = t.UnixNano()
	rec["seq"] = seq.Add(1)
	rec["proc"] = proc
	rec["pid"] = pid
	rec["m"] = name
	for i := 0; i+1 < len(kv); i += 2 {
		k, _ := kv[i].(string)
		if k == "" {
			continue
		}
		rec[k] = kv[i+1]
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	b = append(b, '\n')
	mu.Lock()
	_, _ = out.Write(b)
	mu.Unlock()
}

// Span returns a func that marks name with the milliseconds since the call.
func Span(name string, kv ...any) func(more ...any) {
	if !enabled.Load() {
		return func(...any) {}
	}
	start := time.Now()
	return func(more ...any) {
		all := append(append([]any{"ms", float64(time.Since(start).Microseconds()) / 1000}, kv...), more...)
		at(time.Now(), name, all...)
	}
}
