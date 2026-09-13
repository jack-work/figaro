// Package mark is the timing sink the transcript benchmarks read.
//
// One line per event, JSON, nanosecond timestamps, appended to the file named
// by FIGARO_MARKS. Unset, every call is one atomic load and nothing else, so
// the instrumented paths cost the same in production as they did before.
//
// The daemon and the CLI both write here; the reader joins them on time. The
// names and their fields are listed in skills/figaro/contributing/marks.md,
// which is the one authoritative place.
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

// Init names the process ("cli" or "angelus") and opens the sink if the
// environment asks for one. Safe to call more than once; the first wins.
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

// Enabled reports whether marks are being written.
func Enabled() bool { return enabled.Load() }

// Now is the clock every mark uses, so a caller measuring a span reads the
// same clock the sink stamps.
func Now() time.Time { return time.Now() }

// Mark writes one event. kv alternates keys and values; a value that fails to
// marshal is written as its %v.
func Mark(name string, kv ...any) {
	if !enabled.Load() {
		return
	}
	at(time.Now(), name, kv...)
}

// At writes one event stamped with a time the caller already took, for spans
// whose start must not include the cost of deciding to mark.
func At(t time.Time, name string, kv ...any) {
	if !enabled.Load() {
		return
	}
	at(t, name, kv...)
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

// Span returns a func that marks name with the elapsed milliseconds since the
// call, plus kv, so a phase is measured in one line at the site.
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
