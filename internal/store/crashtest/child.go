package crashtest

import (
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"time"
)

const maxTrunks = 5

type trunkState struct {
	mainTip uint64
	q       map[string]uint64
	lt      map[string]uint64
	lastRef map[string]uint64
}

func childFail(err error) {
	fmt.Fprintf(os.Stderr, "crashtest child: %v\n", err)
	os.Exit(4)
}

// Each report line is written with a single Write AFTER the operation
// returned: a complete line received by the harness means the op finished.
// The stamp on append lines is taken after the append returns, so it is an
// upper bound on when the record entered the in-memory WAL.
func emit(format string, a ...any) {
	fmt.Fprintf(os.Stdout, format+"\n", a...)
}

func loadTrunkState(st Store, trunk, salt string) (*trunkState, error) {
	ts := &trunkState{q: map[string]uint64{}, lt: map[string]uint64{}, lastRef: map[string]uint64{}}
	tip, err := st.MainTail(trunk)
	if err != nil {
		return nil, err
	}
	ts.mainTip = tip
	for _, ch := range allChans {
		rec, ok, err := st.TailRecord(trunk, ch)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		ts.lt[ch] = rec.ChanLT
		ts.lastRef[ch] = rec.MainLT
		if p, ours, valid := decodePayload(rec.Payload, salt); ours && valid {
			ts.q[ch] = p.Q
		}
	}
	return ts, nil
}

func appendOne(st Store, ts *trunkState, trunk, ch, salt string) {
	if ch == chanMain {
		q, m := ts.q[chanMain]+1, ts.mainTip+1
		lt, err := st.AppendMain(trunk, encodePayload(trunk, chanMain, q, m, salt))
		if err != nil {
			childFail(err)
		}
		if lt != m {
			childFail(fmt.Errorf("main append landed at %d, want %d", lt, m))
		}
		ts.q[chanMain], ts.lt[chanMain], ts.mainTip = q, lt, lt
		emit("a %s %s %d %d %d %d", trunk, chanMain, q, m, lt, time.Now().UnixNano())
		return
	}
	q, m := ts.q[ch]+1, ts.mainTip
	lt, err := st.AppendChannel(trunk, ch, m, encodePayload(trunk, ch, q, m, salt))
	if err != nil {
		childFail(err)
	}
	ts.q[ch], ts.lt[ch], ts.lastRef[ch] = q, lt, m
	emit("a %s %s %d %d %d %d", trunk, ch, q, m, lt, time.Now().UnixNano())
}

func runChild() {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	dir := os.Getenv("CRASH_DIR")
	salt := os.Getenv("CRASH_SALT")
	seed, err := strconv.ParseInt(os.Getenv("CRASH_SEED"), 10, 64)
	if dir == "" || salt == "" || err != nil {
		childFail(fmt.Errorf("bad env: dir=%q salt=%q seed err=%v", dir, salt, err))
	}
	st, err := openStore(dir)
	if err != nil {
		childFail(err)
	}
	trunks, err := st.Trunks()
	if err != nil || len(trunks) == 0 {
		childFail(fmt.Errorf("trunks: %v (n=%d)", err, len(trunks)))
	}
	states := map[string]*trunkState{}
	usable := trunks[:0]
	for _, tr := range trunks {
		ts, err := loadTrunkState(st, tr, salt)
		if err != nil {
			continue
		}
		usable = append(usable, tr)
		states[tr] = ts
	}
	trunks = usable
	if len(trunks) == 0 {
		childFail(fmt.Errorf("no usable trunks"))
	}
	emit("ready")

	// THE DURABILITY RECEIPT. A crash test that infers what survived from a
	// wall clock is measuring the machine's load: a flusher starved past its
	// budget looks exactly like a store that lost a write. This asks the
	// store what it has actually put on disk and reports it, so the parent
	// verifies against a fact the child witnessed rather than a deadline it
	// assumed. It runs slightly faster than the flusher, so a report is
	// never far behind the truth, and it only ever reports MORE durability.
	go func() {
		t := time.NewTicker(flushInterval / 2)
		defer t.Stop()
		for range t.C {
			for _, tr := range trunks {
				for _, ch := range allChans {
					n, err := st.DurableThrough(tr, ch)
					if err != nil || n == 0 {
						continue
					}
					emit("d %s %s %d", tr, ch, n)
				}
			}
		}
	}()

	var mu sync.Mutex
	busy := map[string]bool{}
	burstDone := make(chan string, maxTrunks)

	rng := rand.New(rand.NewSource(seed))
	for iter := 0; ; iter++ {
	drain:
		for {
			select {
			case done := <-burstDone:
				mu.Lock()
				busy[done] = false
				mu.Unlock()
			default:
				break drain
			}
		}
		tr := trunks[rng.Intn(len(trunks))]
		mu.Lock()
		trBusy := busy[tr]
		mu.Unlock()
		if trBusy {
			continue
		}
		ts := states[tr]
		switch p := rng.Intn(100); {
		case p < 52:
			appendOne(st, ts, tr, chanMain, salt)
		case p < 76:
			ch := chanNotes
			if p >= 68 {
				ch = chanState
			}
			// After a coherent-cut repair the channel tail may reference a
			// main LT beyond the repaired main tail; appending below it would
			// violate the channel's non-decreasing main-LT rule.
			if ts.mainTip == 0 || ts.lastRef[ch] > ts.mainTip {
				continue
			}
			appendOne(st, ts, tr, ch, salt)
		case p < 84:
			st.Kick()
		case p < 86:
			if iter < 60 || rng.Intn(30) != 0 {
				continue
			}
			for {
				mu.Lock()
				anyBusy := false
				for _, b := range busy {
					anyBusy = anyBusy || b
				}
				mu.Unlock()
				if !anyBusy {
					break
				}
				time.Sleep(time.Millisecond)
			}
			emit("cb %d", time.Now().UnixNano())
			if err := st.Close(); err != nil {
				childFail(fmt.Errorf("close: %w", err))
			}
			emit("cd")
			os.Exit(0)
		case p < 93:
			if len(trunks) >= maxTrunks || ts.mainTip < 1 {
				continue
			}
			at := uint64(rng.Int63n(int64(ts.mainTip))) + 1
			emit("fb %s %d", tr, at)
			nt, err := st.ForkAt(tr, at)
			if err != nil {
				childFail(err)
			}
			emit("fd %s %d %s", tr, at, nt)
			nts, err := loadTrunkState(st, nt, salt)
			if err != nil {
				childFail(err)
			}
			trunks = append(trunks, nt)
			states[nt] = nts
		case p < 94:
			// Detach: absorb the inherited prefix, then stop pointing at an
			// ancestor. LT-neutral by contract, so the model must see NO
			// change -- which is exactly what makes it worth killing during.
			if err := st.Detach(tr); err != nil {
				childFail(fmt.Errorf("detach %s: %w", tr, err))
			}
		case p < 96:
			if len(trunks) < 2 {
				continue
			}
			other := trunks[rng.Intn(len(trunks))]
			mu.Lock()
			ok := other != tr && !busy[other]
			if ok {
				busy[other] = true
			}
			mu.Unlock()
			if !ok {
				continue
			}
			ots, n := states[other], 10+rng.Intn(30)
			go func() {
				for i := 0; i < n; i++ {
					appendOne(st, ots, other, chanMain, salt)
				}
				burstDone <- other
			}()
		default:
			time.Sleep(time.Duration(rng.Intn(2000)) * time.Microsecond)
		}
		time.Sleep(time.Duration(100+rng.Intn(400)) * time.Microsecond)
	}
}
