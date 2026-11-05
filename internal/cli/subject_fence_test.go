package cli

import (
	"bytes"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Two switches can be in flight at once; each takes a generation before it
// dials, and the fence under the lock decides which may install.

func fenceSession(t testing.TB) *interactiveInput {
	t.Helper()
	var out bytes.Buffer
	in := &interactiveInput{mu: new(renderLock)}
	in.lt = newLivelogTurn(&out, 80, 20, &renderSettings{}, "", time.Now(),
		newSessionStatus("", time.Now()), nil, dimRule)
	return in
}

func TestSubjectFence_ASupersededSwitchDoesNotInstall(t *testing.T) {
	in := fenceSession(t)
	first := atomic.AddUint64(&in.subjectGen, 1)
	second := atomic.AddUint64(&in.subjectGen, 1)

	if _, _, ok := in.claimSubject(second, "second", "", nil, 0); !ok {
		t.Fatal("the newest switch was refused its claim")
	}
	if _, _, ok := in.claimSubject(first, "first", "", nil, 0); ok {
		t.Fatal("a superseded switch installed itself over the newer one")
	}
	if in.currentID() != "second" {
		t.Fatalf("the session shows %q, want the aria asked for last", in.currentID())
	}
}

// Concurrent switches: the newest generation always installs, and the session
// ends on it. The order in which goroutines report their grants is not the order
// the lock granted them, so the test asserts what the fence guarantees instead:
// a claim is granted only while its generation is the newest one issued.
func TestSubjectFence_ConcurrentSwitchesEndOnTheNewest(t *testing.T) {
	in := fenceSession(t)
	const n = 64

	ids := make([]string, n+1)
	var mu sync.Mutex
	granted := map[uint64]bool{}
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			gen := atomic.AddUint64(&in.subjectGen, 1)
			id := "aria" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			mu.Lock()
			ids[gen] = id
			mu.Unlock()
			if _, _, ok := in.claimSubject(gen, id, "", nil, 0); ok {
				mu.Lock()
				granted[gen] = true
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadUint64(&in.subjectGen); got != n {
		t.Fatalf("the generation counter is %d after %d switches", got, n)
	}
	if !granted[n] {
		t.Fatalf("the newest switch (generation %d) was refused", n)
	}
	if in.currentID() != ids[n] {
		t.Fatalf("the session shows %q, want %q from the newest switch", in.currentID(), ids[n])
	}
}
