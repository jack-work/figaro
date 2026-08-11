package angelus

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"testing"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

// heapDelta reports live-heap growth across fn, holding the result alive so
// the GC cannot reclaim what we are weighing. Two GCs each side: the first
// sweeps, the second collects what the first's finalizers freed.
func heapDelta(fn func() any) (bytes uint64, keep any) {
	runtime.GC()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	keep = fn()
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)
	if after.HeapAlloc < before.HeapAlloc {
		return 0, keep
	}
	return after.HeapAlloc - before.HeapAlloc, keep
}

func human(b uint64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d B", b)
}

func ratio(a, b uint64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// TestRealAriaMemory weighs a REAL aria's holders. It must be a real store:
// synthetic prose gets the ratio BETWEEN rows backwards: uniform small
// messages make the composed UI look like 1.5x the decoded IR, where real
// arias (tool calls, large results, multi-provider lineage) put it at 0.2x.
//
// Point it at a COPY. It opens the store for reading only, but a live daemon
// holds the lock and there is no reason to gamble a 300 MB store on that.
//
//	cp -r ~/.local/state/figaro/arias /tmp/arias-probe
//	FIGARO_PROBE_ROOT=/tmp/arias-probe FIGARO_PROBE_ARIA=<id> \
//	  go test ./internal/angelus/ -run RealAriaMemory -v
//
// Pick a fat aria by message_count:
//
//	jq -r '[.message_count, input_filename] | @tsv' /tmp/arias-probe/_meta/*.json | sort -rn | head
//
// Measured on two real arias (2026-08), for the record:
//
//	                        cf3fc17d (2556 rows)   e83ae209 (1760 rows)
//	decoded IR (cachedLog)   12.5 MiB  86%          14.3 MiB  63%
//	composed UI (aria.Srv)    2.0 MiB  14%           2.4 MiB  10%
//	translations              1.2 KiB   0%           6.0 MiB  26%
//	form               48.1 KiB               43.3 KiB
//	TOTAL                    14.5 MiB               22.7 MiB
//
// The decoded IR runs 4.2-5.4x the encoded wire bytes and dominates. That
// is why hibernation matters: EvictIdle already knows how to drop it and is
// forbidden to while an agent is live.
func TestRealAriaMemory(t *testing.T) {
	root, id := os.Getenv("FIGARO_PROBE_ROOT"), os.Getenv("FIGARO_PROBE_ARIA")
	if root == "" || id == "" {
		t.Skip("set FIGARO_PROBE_ROOT and FIGARO_PROBE_ARIA (see the doc comment)")
	}
	backend, err := store.NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	// FIGARO_PROBE_WINDOW measures the IR window's effect on the row that
	// dominates. Unset retains everything, which is the before.
	if w := os.Getenv("FIGARO_PROBE_WINDOW"); w != "" {
		n, perr := strconv.Atoi(w)
		if perr != nil {
			t.Fatalf("FIGARO_PROBE_WINDOW: %v", perr)
		}
		backend.SetIRWindow(n)
		t.Logf("ir_window = %d rows", n)
	}
	if mb := os.Getenv("FIGARO_PROBE_WINDOW_MB"); mb != "" {
		n, perr := strconv.Atoi(mb)
		if perr != nil {
			t.Fatalf("FIGARO_PROBE_WINDOW_MB: %v", perr)
		}
		backend.SetIRBudget(n << 20)
		t.Logf("ir_window_mb = %d", n)
	}

	// RESIDENCY, not the cost of one read. The daemon's steady state is the
	// cache holding rows while nobody is reading, so the log handle is what we
	// keep alive and the Read result is deliberately discarded: retaining it
	// would measure a caller's copy and, under a window, would pull the whole
	// log back off disk to do so.
	// TotalAlloc measures bytes EVER allocated, so it catches the transient
	// decode a GC-bracketed HeapAlloc delta hides. That transient is the whole
	// point of the tail read: the old path decoded every record and dropped
	// most of them.
	var allocPre runtime.MemStats
	runtime.ReadMemStats(&allocPre)

	irBytes, logKeep := heapDelta(func() any {
		log, err := backend.Open(id)
		if err != nil {
			t.Fatal(err)
		}
		return log
	})
	var allocPost runtime.MemStats
	runtime.ReadMemStats(&allocPost)
	t.Logf("open allocated %s total (transient + retained)",
		human(allocPost.TotalAlloc-allocPre.TotalAlloc))

	irLog := logKeep.(store.Log[message.Message])
	t.Logf("resident: %d of %d rows, %s estimated",
		backend.ResidentRows(), irLog.Len(), human(uint64(backend.ResidentIRBytes())))

	// Isolate what a TRIM releases, on the same object, so construction's
	// transient decode cannot be mistaken for retained bytes.
	if keep := 512; irLog.Len() > keep {
		if tr, ok := irLog.(interface {
			Trim(int) int
			Resident() int
		}); ok {
			var pre, post runtime.MemStats
			runtime.GC()
			runtime.GC()
			runtime.ReadMemStats(&pre)
			dropped := tr.Trim(keep)
			runtime.GC()
			runtime.GC()
			runtime.ReadMemStats(&post)
			freed := uint64(0)
			if pre.HeapAlloc > post.HeapAlloc {
				freed = pre.HeapAlloc - post.HeapAlloc
			}
			t.Logf("trim to %d: dropped %d rows, released %s (now %d resident)",
				keep, dropped, human(freed), tr.Resident())
		}
	}

	// A separate read for the content stats below. Not folded into the
	// measurement above, for the reason just stated.
	rows := irLog.Read()

	proj := uiir.New(nil)
	uiBytes, uiKeep := heapDelta(func() any {
		flat := make([]message.Message, 0, len(rows))
		for _, e := range rows {
			flat = append(flat, e.Payload)
		}
		srv := aria.NewServer()
		for _, tn := range proj.Turns(flat) {
			srv.Commit(tn)
		}
		return srv
	})

	// One cachedLog of []json.RawMessage per provider the aria has spoken
	// to. Wildly variable: kilobytes on a single-provider aria, megabytes
	// on one with a switch in its lineage: so it is measured, not assumed.
	transBytes, transKeep := heapDelta(func() any {
		var held []any
		for _, prov := range []string{"anthropic", "copilot-messages", "copilot-responses", "openai"} {
			if tl, err := backend.OpenTranslation(id, prov); err == nil {
				held = append(held, tl.Read())
			}
		}
		return held
	})

	cbBytes, cbKeep := heapDelta(func() any {
		snap, err := backend.FormState(id)
		if err != nil {
			t.Fatal(err)
		}
		return snap
	})

	var payload, encoded uint64
	for _, e := range rows {
		for _, c := range e.Payload.Content {
			payload += uint64(len(c.Text) + len(c.Data))
		}
		if b, err := json.Marshal(e.Payload); err == nil {
			encoded += uint64(len(b))
		}
	}

	total := irBytes + uiBytes + cbBytes + transBytes
	t.Logf("aria %s: %d rows, %s content, %s encoded", id, len(rows), human(payload), human(encoded))
	t.Logf("  decoded IR (cachedLog)     %10s  %4.1fx encoded", human(irBytes), ratio(irBytes, encoded))
	t.Logf("  composed UI (aria.Server)  %10s  %4.1fx the IR", human(uiBytes), ratio(uiBytes, irBytes))
	t.Logf("  translations (all providers)%9s", human(transBytes))
	t.Logf("  form                 %10s", human(cbBytes))
	t.Logf("  TOTAL per live aria        %10s", human(total))
	t.Logf("  shares: IR %.0f%%  UI %.0f%%  trans %.0f%%",
		100*ratio(irBytes, total), 100*ratio(uiBytes, total), 100*ratio(transBytes, total))

	runtime.KeepAlive(logKeep)
	runtime.KeepAlive(uiKeep)
	runtime.KeepAlive(cbKeep)
	runtime.KeepAlive(transKeep)
}
