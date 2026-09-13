package cli

import (
	"bytes"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TWO SWITCHES CAN BE IN FLIGHT AT ONCE, and the one that finishes dialling
// first is not necessarily the one the reader asked for last. Each takes a
// generation before it dials; the fence under the lock is what decides which
// of them may install.
//
// Found by the reviewer (27068b2c): the generation was taken at the top and
// never looked at again, so a slow hop started first could install AFTER a
// fast hop started second, leaving the session showing an aria nobody had
// named, with the other one's seed reads landing in its store.

func fenceSession(t testing.TB) *interactiveInput {
	t.Helper()
	var out bytes.Buffer
	in := &interactiveInput{mu: &sync.Mutex{}}
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

// The race, driven: many switches at once, each taking its generation and then
// claiming. Whatever the interleaving, a claim may only be granted to a
// generation higher than every claim granted before it, and the subject left
// standing is the one that claimed last. Run with -race.
func TestSubjectFence_ConcurrentSwitchesInstallInOrder(t *testing.T) {
	in := fenceSession(t)
	const n = 64

	var mu sync.Mutex
	var granted []uint64
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			gen := atomic.AddUint64(&in.subjectGen, 1)
			id := "aria" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			if _, _, ok := in.claimSubject(gen, id, "", nil, 0); ok {
				mu.Lock()
				granted = append(granted, gen)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if len(granted) == 0 {
		t.Fatal("no switch installed at all")
	}
	// The claims are appended in the order the lock granted them, so this is
	// the order the subject changed in. Nothing older may follow anything
	// newer.
	for i := 1; i < len(granted); i++ {
		if granted[i] <= granted[i-1] {
			t.Fatalf("generation %d installed after %d", granted[i], granted[i-1])
		}
	}
	if in.currentID() == "" {
		t.Fatal("the session ended with no subject")
	}
	if got := atomic.LoadUint64(&in.subjectGen); got != n {
		t.Fatalf("the generation counter is %d after %d switches", got, n)
	}
}
