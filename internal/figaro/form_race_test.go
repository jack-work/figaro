package figaro_test

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
)

// TestFormRPCRaceRepro drives the two goroutines that the design
// document names as racing:
//
//   - the AGENT goroutine: Agent.Set enqueues an eventSet on the inbox;
//     Agent.act drains it and calls applyControlPatch -> form.State.Apply,
//     which does `s.snapshot = s.snapshot.Apply(p)` (a write of the map header
//     field) and `s.dirty = true`.
//   - an RPC goroutine: figaro.Handle(rpc.MethodForm) calls
//     Agent.Snapshot() -> form.State.Snapshot() -> s.snapshot.Clone(),
//     which reads the same field and ranges the map behind it.
//
// Nothing serializes the two: Agent.NewAgent does `go a.runWithRecovery(ctx)`
// for the act loop, while StartSocket does `go a.serveConn(...)` per
// connection, and jkrpc calls the handler inline on that connection's
// goroutine. So the read path has no inbox and no mutex.
//
// This test does not need a socket to exercise that: it calls the exact
// methods the RPC handlers call, from other goroutines.
//
// EXPECTED TO FAIL on main under -race. Repro command:
//
//	CHALK_RACE_REPRO=1 go test -race -run TestFormRPCRaceRepro -count=1 ./internal/figaro/
//
// It is gated behind CHALK_RACE_REPRO so the default suite stays green.
func TestFormRPCRaceRepro(t *testing.T) {
	requireRaceRepro(t)

	a, _, _ := newAgentWithForm(t)

	const (
		writers       = 4
		readers       = 8
		setsPerWriter = 250
		// Sets are spaced out on purpose: every drained eventSet appends an
		// IR message and refreshes metrics over the whole log, so an
		// unthrottled writer buries the act loop under a backlog it cannot
		// drain before the test ends. A few hundred applies is far more than
		// the race needs.
		setGap = time.Millisecond
	)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var writersDone sync.WaitGroup

	// Writers: Agent.Set -> inbox -> act goroutine -> form.Apply.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		writersDone.Add(1)
		go func(w int) {
			defer wg.Done()
			defer writersDone.Done()
			for i := 0; i < setsPerWriter; i++ {
				key := fmt.Sprintf("w%d.k%d", w, i%64)
				val, _ := json.Marshal(fmt.Sprintf("v%d", i))
				_, _, err := a.Set(form.Patch{
					Set: map[string]json.RawMessage{key: val},
				}, 0)
				if err != nil {
					t.Errorf("Set: %v", err)
					return
				}
				time.Sleep(setGap)
			}
		}(w)
	}

	// Readers: exactly what rpc.MethodForm does, in a tight loop.
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap := a.Snapshot()
				// Touch the values, as FormResponse marshalling would.
				for k, v := range snap.All() {
					_ = k
					_ = len(v)
				}
			}
		}()
	}

	writersDone.Wait()
	close(stop)
	wg.Wait()
}

// TestFormStateRaceRepro is the same race one layer down, with no
// agent, no inbox and no log: just form.State, whose doc comment
// claims "single-owner (no concurrent access)". It isolates the unsynchronized
// publication of State.snapshot (and State.dirty) so the -race report names
// form/state.go directly.
//
//	CHALK_RACE_REPRO=1 go test -race -run TestFormStateRaceRepro -count=1 ./internal/figaro/
func TestFormStateRaceRepro(t *testing.T) {
	requireRaceRepro(t)

	st, err := form.Open("")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seed := map[string]json.RawMessage{}
	for i := 0; i < 64; i++ {
		seed[fmt.Sprintf("seed.%d", i)] = json.RawMessage(`"x"`)
	}
	st.Apply(form.Patch{Set: seed})

	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() { // the "agent" goroutine
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			st.Apply(form.Patch{Set: map[string]json.RawMessage{
				fmt.Sprintf("hot.%d", i%32): json.RawMessage(`"y"`),
			}})
		}
	}()

	for r := 0; r < 8; r++ { // the "RPC" goroutines
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for k, v := range st.Snapshot().All() {
					_ = k
					_ = len(v)
				}
			}
		}()
	}

	time.Sleep(2 * time.Second)
	close(stop)
	wg.Wait()
}

func requireRaceRepro(t *testing.T) {
	t.Helper()
	if os.Getenv("CHALK_RACE_REPRO") == "" {
		t.Skip("race repro; set CHALK_RACE_REPRO=1 (expected to FAIL before the fix)")
	}
}
