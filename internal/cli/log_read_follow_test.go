package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) turns(t *testing.T) []uint64 {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []uint64
	for _, line := range strings.Split(strings.TrimSpace(l.b.String()), "\n") {
		if line == "" {
			continue
		}
		var e logEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("not an entry: %q: %v", line, err)
		}
		out = append(out, e.Turn)
	}
	return out
}

func waitTurns(t *testing.T, w *lockedBuf, want []uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := w.turns(t)
		if len(got) == len(want) {
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("printed turns %v, want %v", got, want)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("printed turns %v, want %v", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type followRun struct {
	cancel context.CancelFunc
	pokes  chan struct{}
	gone   chan struct{}
	out    *lockedBuf
	done   chan error
}

func startFollow(t *testing.T, f *fakeLogReads, from uint64) *followRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &followRun{cancel: cancel, pokes: make(chan struct{}, 1), gone: make(chan struct{}), out: &lockedBuf{}, done: make(chan error, 1)}
	go func() { r.done <- followEntries(ctx, f, "log", from, r.pokes, r.gone, r.out, true) }()
	t.Cleanup(cancel)
	return r
}

func (r *followRun) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("follow did not return")
		return nil
	}
}

func TestFollowPrintsEntriesAppendedAfterAttachInOrderExactlyOnce(t *testing.T) {
	f := &fakeLogReads{}
	for i := 1; i <= 3; i++ {
		f.add(entryMsg(uint64(i), "old"))
	}
	r := startFollow(t, f, 2)
	waitTurns(t, r.out, []uint64{2, 3})

	f.add(entryMsg(4, "new\n"), entryMsg(5, "newer"))
	r.pokes <- struct{}{}
	waitTurns(t, r.out, []uint64{2, 3, 4, 5})

	r.pokes <- struct{}{}
	f.add(entryMsg(6, "last"))
	r.pokes <- struct{}{}
	waitTurns(t, r.out, []uint64{2, 3, 4, 5, 6})

	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("a signal ended the follow with %v, want a clean exit", err)
	}
}

func TestFollowEntriesKeepTheShapeOfReadJSON(t *testing.T) {
	f := &fakeLogReads{}
	f.add(entryMsg(1, "a & <b>\n"))
	r := startFollow(t, f, 0)
	waitTurns(t, r.out, []uint64{1})
	r.cancel()
	r.wait(t)

	want, err := readEntries(context.Background(), f, "log", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var wantBuf bytes.Buffer
	writeEntries(&wantBuf, want, true)
	r.out.mu.Lock()
	defer r.out.mu.Unlock()
	if r.out.b.String() != wantBuf.String() {
		t.Fatalf("follow printed %q, read -j prints %q", r.out.b.String(), wantBuf.String())
	}
}

func TestFollowExitsNonZeroWhenTheAriaIsKilled(t *testing.T) {
	f := &fakeLogReads{}
	f.add(entryMsg(1, "one"))
	r := startFollow(t, f, 0)
	waitTurns(t, r.out, []uint64{1})

	f.add(entryMsg(2, "sealed just before the kill"))
	close(r.gone)
	if err := r.wait(t); err == nil {
		t.Fatal("a killed aria ended the follow cleanly")
	}
	waitTurns(t, r.out, []uint64{1, 2})
}
