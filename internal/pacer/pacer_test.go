package pacer

import (
	"bytes"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// safeBuffer is bytes.Buffer with concurrent access. The pacer's
// drainer writes from a goroutine; tests read from the test
// goroutine. Without a lock the race detector would (rightly) fire.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *safeBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func TestDisabledPassthrough(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, Options{TargetCPS: 0})
	defer p.Close()

	p.Push("hello world")
	if buf.String() != "hello world" {
		t.Fatalf("expected synchronous passthrough, got %q", buf.String())
	}
}

func TestEmitsAllInOrder(t *testing.T) {
	var buf safeBuffer
	p := New(&buf, Options{TargetCPS: 5000}) // very fast
	p.Push("Largo al factotum della città!")
	p.Flush()
	p.Close()

	got := buf.String()
	want := "Largo al factotum della città!"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRespectsRateApproximate(t *testing.T) {
	var buf safeBuffer
	const cps = 200
	p := New(&buf, Options{TargetCPS: cps, FirstByteBypass: 0})
	const text = "abcdefghijklmnopqrstuvwxyz" // 26 chars
	start := time.Now()
	p.Push(text)
	p.Flush()
	elapsed := time.Since(start)
	p.Close()

	// At 200 cps, 26 runes ≈ 130 ms (no bypass). Allow generous
	// slack (±60ms) so CI scheduling jitter doesn't flake the test.
	min := 80 * time.Millisecond
	if elapsed < min {
		t.Fatalf("emitted too fast: %v (want at least %v)", elapsed, min)
	}
	if buf.String() != text {
		t.Fatalf("got %q want %q", buf.String(), text)
	}
}

func TestFirstByteBypass(t *testing.T) {
	var buf safeBuffer
	p := New(&buf, Options{TargetCPS: 50, FirstByteBypass: 100 * time.Millisecond})
	// Push during the bypass window: should land in `buf` essentially
	// instantly (synchronous write).
	p.Push("HELLO")
	// Give the runtime a beat to settle but we expect the write
	// already happened during Push.
	time.Sleep(5 * time.Millisecond)
	if buf.Len() != 5 {
		t.Fatalf("expected first-byte bypass to write 5 bytes immediately, got %d (%q)", buf.Len(), buf.String())
	}
	p.Close()
}

func TestAdaptiveSpeedupOnLargeQueue(t *testing.T) {
	var buf safeBuffer
	// Low CPS + small max lag → easy to overrun.
	p := New(&buf, Options{TargetCPS: 50, MaxLagRunes: 10})
	// Push 500 runes at once — 10× past the lag cap, so the drainer
	// should burst, finishing well under the naive 10s the strict
	// rate would imply.
	p.Push(strings.Repeat("x", 500))
	start := time.Now()
	p.Flush()
	elapsed := time.Since(start)
	p.Close()

	if buf.Len() != 500 {
		t.Fatalf("expected 500 bytes flushed, got %d", buf.Len())
	}
	// Naive (no speedup): 500/50 = 10s. With speedup we should be
	// well under 3s. Generous bound for CI.
	if elapsed > 3*time.Second {
		t.Fatalf("adaptive speedup did not kick in: elapsed=%v", elapsed)
	}
}

func TestCloseUnblocks(t *testing.T) {
	var buf safeBuffer
	p := New(&buf, Options{TargetCPS: 1}) // 1 cps → 1s per rune naively
	p.Push("never gonna paint at 1cps")

	done := make(chan struct{})
	go func() {
		p.Close()
		close(done)
	}()

	select {
	case <-done:
		// Good: Close drained synchronously.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close did not return promptly; drainer leaked")
	}
	if !strings.Contains(buf.String(), "never gonna paint") {
		t.Fatalf("expected full content drained on Close, got %q", buf.String())
	}
}

func TestConcurrentPushIsSafe(t *testing.T) {
	var buf safeBuffer
	p := New(&buf, Options{TargetCPS: 5000})
	var wg sync.WaitGroup
	const n = 50
	var counter int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.Push("x")
			atomic.AddInt64(&counter, 1)
		}()
	}
	wg.Wait()
	p.Flush()
	p.Close()
	if buf.Len() != n {
		t.Fatalf("expected %d bytes (one per Push), got %d (%q)", n, buf.Len(), buf.String())
	}
}
